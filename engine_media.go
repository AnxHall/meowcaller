package meowcaller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/purpshell/meowcaller/diag"
	"github.com/purpshell/meowcaller/mlow"
	"github.com/purpshell/meowcaller/relay"
	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/srtp"
	"github.com/purpshell/meowcaller/stun"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// The live-relay media loop: connect+allocate to the elected relay, then run the
// per-frame send/recv loop. Outbound pulls frames from the Call's Player (silence when
// idle), encodes via MLow + ProtectAudio, and sends to the relay; inbound classifies
// relay packets, unprotects+decodes RTP, and writes to the Call's sink.

// maybeStartMedia launches the media loop for callID once both the callKey and the relay
// endpoint are known. It is idempotent — the loop starts exactly once per call.
func (e *engine) maybeStartMedia(callID string) {
	e.mu.Lock()
	m := e.calls[callID]
	startMedia := e.startMedia
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L115-L122
	if m == nil || m.call == nil || m.started || m.callKey == nil ||
		m.relay == nil || startMedia == nil {
		e.mu.Unlock()
		return
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L100-L107
	m.started = true
	mctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	call := m.call
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L122
	callKey := bytes.Clone(m.callKey)
	endpointClone := cloneRelayEndpoint(*m.relay)
	selfLID, peerLID := m.selfLID, m.peerLID
	onMediaStopped := e.onMediaStopped
	e.mu.Unlock()

	if call != nil {
		call.setPhase(CallPhaseConnecting)
	}
	e.c.log.Info().Str("call_id", callID).Msg("starting media")
	go func() {
		defer func() {
			clear(callKey)
			clear(endpointClone.Key)
			clear(endpointClone.Token)
			clear(endpointClone.AuthToken)
			if onMediaStopped != nil {
				onMediaStopped(callID)
			}
		}()
		if err := startMedia(mctx, callID, call, callKey, selfLID, peerLID, &endpointClone); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", callID).Msg("media ended")
		}
	}()
}

// connectAndAllocate opens the relay DataChannel and sends the STUN allocate, returning
// the channel and the allocate bytes (re-sent by the keepalive).
//
// NOT VALIDATED: live-relay only.
func (e *engine) connectAndAllocate(ctx context.Context, ep *types.RelayEndpoint, streamSsrcs [9]uint32) (*relay.RelayMediaChannel, []byte, error) {
	log := e.c.log
	if ep == nil || ep.IPv4 == "" || ep.Port == 0 {
		return nil, nil, fmt.Errorf("relay has no usable endpoint")
	}
	addr := &net.UDPAddr{IP: net.ParseIP(ep.IPv4), Port: int(ep.Port)}
	log.Info().Str("relay_name", ep.RelayName).Str("addr", addr.String()).Msg("connecting media transport to relay")
	e.c.diag.Emit("relay", map[string]any{
		"event": "endpoint", "relay_name": ep.RelayName,
		"ipv4": ep.IPv4, "port": ep.Port, "token_id": ep.TokenID, "is_fna": ep.IsFNA,
	})

	type result struct {
		ch  *relay.RelayMediaChannel
		err error
	}
	done := make(chan result, 1)
	go func() {
		ch, err := relay.ConnectRelayMedia(addr, relay.WithLogger(log))
		done <- result{ch, err}
	}()
	var ch *relay.RelayMediaChannel
	select {
	case r := <-done:
		if r.err != nil {
			return nil, nil, fmt.Errorf("relay connect: %w", r.err)
		}
		ch = r.ch
	case <-time.After(12 * time.Second):
		return nil, nil, fmt.Errorf("relay connect timed out (DTLS didn't complete)")
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	log.Info().Str("relay_name", ep.RelayName).Msg("relay DataChannel open")

	if len(ep.Token) == 0 {
		ch.Close()
		return nil, nil, fmt.Errorf("no relay token #%d", ep.TokenID)
	}
	if len(ep.Key) == 0 {
		ch.Close()
		return nil, nil, fmt.Errorf("relay has no <key>")
	}
	e.c.diag.Emit("relay", map[string]any{
		"event": "keying", "token_id": ep.TokenID,
		"relay_key_hex": hex.EncodeToString(ep.Key),
		"token_hex":     hex.EncodeToString(ep.Token),
	})
	endpointXor, ok := stun.EncodeXorRelayEndpoint(ep.IPv4, ep.Port, log)
	if !ok {
		ch.Close()
		return nil, nil, fmt.Errorf("bad endpoint XOR")
	}
	var tx [12]byte
	_, _ = rand.Read(tx[:])
	allocate := stun.BuildWasmStunAllocateRequestWithStreamSsrcs(tx, ep.Token, endpointXor, streamSsrcs, ep.Key, log)
	if _, err := ch.Send(allocate); err != nil {
		ch.Close()
		return nil, nil, fmt.Errorf("allocate send: %w", err)
	}
	log.Info().Int("bytes", len(allocate)).Msg("sent STUN allocate")
	e.c.diag.Emit("stun", map[string]any{
		"event": "allocate_sent", "bytes": len(allocate),
		"tx_id_hex": hex.EncodeToString(tx[:]), "allocate_hex": hex.EncodeToString(allocate),
		"stream_ssrcs": streamSsrcs,
	})
	return ch, allocate, nil
}

// runMedia runs the per-frame media loop over the relay DataChannel: the Player's frames
// (or silence) → MLow → E2E-SRTP protect → DataChannel, and DataChannel → classify →
// unprotect → MLow decode → the Call's sink. A 1 Hz allocate+ping keepalive holds the
// relay's consent freshness; the relay's binding-requests are answered with
// binding-success. The working recipe is preserved exactly: a consent ping (0x0801) goes
// out with the allocate at t+0, BEFORE any RTP; no STUN binding-requests are ever sent.
//
// NOT VALIDATED: live-relay only.
func (e *engine) runMedia(ctx context.Context, callID string, call *Call, callKey []byte, selfLID, peerLID string, ep *types.RelayEndpoint) error {
	log := e.c.log
	selfParticipantID := rtp.FormatE2ESrtpParticipantID(selfLID)
	ssrc, err := rtp.DeriveWasmParticipantSsrc(callID, selfParticipantID, 0, log)
	if err != nil {
		return err
	}
	videoSelfSsrc, err := rtp.DeriveWasmParticipantSsrc(callID, selfParticipantID, rtp.VideoSlotWord, log)
	if err != nil {
		return err
	}
	appDataSelfSsrc, err := rtp.DeriveWasmParticipantSsrc(callID, selfParticipantID, rtp.AppDataSlotWord, log)
	if err != nil {
		return err
	}
	streamSsrcs, err := rtp.DeriveWasmRelayStreamSsrcs(callID, selfParticipantID, log)
	if err != nil {
		return err
	}
	ch, allocate, err := e.connectAndAllocate(ctx, ep, streamSsrcs)
	if err != nil {
		return err
	}
	defer ch.Close()
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L53-L62
	allocateState := newGroupRelayAllocateState(allocate, ep.Key)

	// Send a consent ping (0x0801) immediately, together with the allocate and BEFORE any
	// RTP. The relay won't forward the peer's media until consent (ping → pong) is
	// established; RTP sent before the first ping is dropped and the relay never bridges.
	{
		var ptx [12]byte
		_, _ = rand.Read(ptx[:])
		initPing := stun.BuildWhatsappPing(ptx, log)
		_, _ = ch.Send(initPing[:])
		e.c.diag.Emit("stun", map[string]any{
			"event": "consent_ping_sent", "tx_id_hex": hex.EncodeToString(ptx[:]),
			"ping_hex": hex.EncodeToString(initPing[:]),
		})
	}

	log.Info().
		Str("self_lid", selfLID).
		Str("peer_lid", peerLID).
		Str("ssrc", fmt.Sprintf("0x%08x", ssrc)).
		Str("video_ssrc", fmt.Sprintf("0x%08x", videoSelfSsrc)).
		Str("app_data_ssrc", fmt.Sprintf("0x%08x", appDataSelfSsrc)).
		Msg("media session")
	e.c.diag.Emit("ssrc", map[string]any{
		"call_id": callID, "ssrc": ssrc, "video_ssrc": videoSelfSsrc, "app_data_ssrc": appDataSelfSsrc,
		"stream_ssrcs": streamSsrcs, "self_lid": selfLID,
		"participant_id": selfParticipantID,
	})

	enc := mlow.NewMlowEncoder(mlow.WithLogger(log))
	audioReceivers, err := newParticipantReceiveRegistry(
		callID,
		callKey,
		selfLID,
		peerLID,
		func() participantAudioDecoder {
			return mlow.NewMlowDecoder(mlow.WithLogger(log))
		},
		WithLogger(log),
	)
	if err != nil {
		return err
	}
	audioPlayout := newAudioPlayoutBuffer()
	var audioPlayoutMu sync.Mutex
	var groupMixing atomic.Bool
	defer func() {
		if groupMixing.Load() {
			return
		}
		audioPlayoutMu.Lock()
		defer audioPlayoutMu.Unlock()
		_, sink := callPlayerSink(call)
		_ = audioPlayout.Flush(sink)
	}()
	audioMixer := newParticipantAudioMixer()
	var audioSinkFramer participantAudioSinkFramer
	go func() {
		ticker := time.NewTicker(time.Duration(participantAudioMixChunkSamples) * time.Second / SampleRate)
		defer ticker.Stop()
		playoutStarted := false
		var writeFailures uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if !groupMixing.Load() {
				continue
			}
			_, sink := callPlayerSink(call)
			if sink == nil {
				continue
			}
			chunk, ok := audioMixer.MixChunk()
			if !ok {
				continue
			}
			frame, ok := audioSinkFramer.Push(chunk)
			if !ok {
				continue
			}
			if err := sink.WriteFrame(frame); err != nil {
				if writeFailures++; writeFailures == 1 {
					log.Warn().Err(err).Msg("failed to write mixed WhatsApp audio")
				}
				continue
			}
			if !playoutStarted {
				playoutStarted = true
				log.Info().
					Int("prefill_ms", participantAudioMixerPrefillSamples*1000/SampleRate).
					Int("chunk_ms", participantAudioMixChunkSamples*1000/SampleRate).
					Msg("started participant-mixed inbound audio playout")
				e.c.diag.Emit("meta", map[string]any{
					"event": "audio_playout_started", "call_id": callID,
					"prefill_ms": participantAudioMixerPrefillSamples * 1000 / SampleRate,
					"chunk_ms":   participantAudioMixChunkSamples * 1000 / SampleRate,
				})
			}
		}
	}()
	txPipe, err := NewMediaPipeline(callKey, selfLID, peerLID, ssrc, FrameSamples, WithLogger(log))
	if err != nil {
		return err
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L78-L86
	if err = audioReceivers.attachSendPipeline(txPipe); err != nil {
		return fmt.Errorf("attach audio RTP sender: %w", err)
	}
	audioRtcp, err := newMediaSrtcpSender(callKey, selfLID, ssrc, false)
	if err != nil {
		return err
	}
	if err = audioReceivers.attachSRTCPSender(audioRtcp); err != nil {
		return fmt.Errorf("attach audio SRTCP sender: %w", err)
	}
	// The derived E2E-SRTP keys live inside MediaPipeline; record the derivation INPUTS
	// (callKey + participant-ID info strings) so a reference can re-derive and compare.
	e.c.diag.Emit("srtp", map[string]any{
		"event": "media_keys_input", "call_id": callID, "ssrc": ssrc,
		"self_participant_id": selfParticipantID,
		"peer_participant_id": rtp.FormatE2ESrtpParticipantID(peerLID),
		"call_key_hex":        hex.EncodeToString(callKey),
	})
	e.c.diag.Emit("meta", map[string]any{
		"event": "media_start", "call_id": callID, "self_lid": selfLID,
		"peer_lid": peerLID, "ssrc": ssrc,
	})

	// relayRx counts packets received from the relay, so the silence watchdog can warn if
	// the relay never answers our allocate.
	var relayRx atomic.Uint64

	// Inbound calls are torn down by the caller within ~400ms if the relay bind never
	// comes alive; check at 400ms and 900ms and say so explicitly.
	go func() {
		for _, d := range []time.Duration{400 * time.Millisecond, 900 * time.Millisecond} {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
			if relayRx.Load() == 0 {
				log.Warn().Dur("after", d).Msg("relay silent after allocate, no bytes back yet (allocate undelivered or rejected)")
			}
		}
	}()

	// Keepalive: re-send the Allocate AND a WhatsApp ping (0x0801) ~1 Hz. This matches the
	// working capture exactly — allocate+ping every second, NO STUN binding-requests at
	// all; the relay answers allocate-success + pong and bridges the peer's media.
	// Binding-requests instead flip the relay into ICE-consent mode and the bridge never
	// forms.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		var tickCount uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			var tx [12]byte
			_, _ = rand.Read(tx[:])
			ping := stun.BuildWhatsappPing(tx, log)
			// Source of truth: https://github.com/purpshell/meowcaller/blob/bcfb7f0c076b131422c22f024dfff080448e70f4/datasheets/group-media-relay-refresh.md#L59-L67
			if err := allocateState.SendCurrent(func(packet []byte) error {
				_, sendErr := ch.Send(packet)
				return sendErr
			}); err != nil {
				return
			}
			_, _ = ch.Send(ping[:])
			tickCount++
			e.c.diag.Emit("stun", map[string]any{
				"event": "keepalive", "tick": tickCount,
				"tx_id_hex": hex.EncodeToString(tx[:]), "ping_hex": hex.EncodeToString(ping[:]),
			})
		}
	}()

	// Send loop: frame-paced from connect, NOT gated on the Player. WhatsApp starts media
	// on relay connection and the relay learns our SSRC from our FIRST RTP — it won't
	// bridge the peer's media until it sees our stream. So we send silence frames until the
	// Player has real audio (nextFrame() == nil means send silence).
	frameInterval := time.Duration(FrameSamples) * time.Second / SampleRate
	go func() {
		silence := make([]float32, FrameSamples)
		ticker := time.NewTicker(frameInterval)
		defer ticker.Stop()
		var txCount uint64
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			frame := silence
			if player, _ := callPlayerSink(call); player != nil {
				if f := player.nextFrame(); f != nil {
					frame = f
				}
			}
			payload, err := enc.Encode(frame)
			if err != nil {
				continue
			}
			packet, err := txPipe.ProtectAudio(payload)
			if err != nil {
				continue
			}
			e.c.diag.Emit("media_out", map[string]any{
				"frame": txCount, "frame_samples": len(frame), "pcm_rms": rmsFloat32(frame),
				"payload_len": len(payload), "payload_hex": hex.EncodeToString(payload),
				"packet_len": len(packet), "packet_hex": hex.EncodeToString(packet),
			})
			if _, err := ch.Send(packet); err != nil {
				return
			}
			if txCount++; txCount == 1 {
				log.Info().Int("bytes", len(packet)).Msg("first RTP sent to relay, outbound media flowing")
				e.c.diag.Emit("meta", map[string]any{"event": "first_rtp_sent", "call_id": callID, "bytes": len(packet)})
			}
		}
	}()

	// Receive: DataChannel → classify. RTP → unprotect → decode → sink. A non-RTP STUN
	// binding request gets a binding-success reply (ICE consent freshness, RFC 7675);
	// without it the relay drops the binding and the peer's call fails.
	// Video receive (meowcaller-native): participant-scoped WARP pipelines keyed on
	// each video SSRC (participant slot 2), demuxed off the relay by H.264 payload
	// type 97. NALUs are reassembled into Annex-B access units and emitted on the
	// RTP marker bit, per WaCalls.
	//
	// NOT VALIDATED: no live video-RTP vector; assumes video shares the audio E2E keys and
	// WARP framing, and that the relay bridges the video SSRC.
	rekeyPeer := func(answeringPeer string) error {
		if err := audioReceivers.RekeyFallback(answeringPeer); err != nil {
			return err
		}
		audioMixer.Retain(audioReceivers.ActiveParticipantIDs())
		return nil
	}
	var audioReception rtp.RtcpReceptionStatsSet
	var groupRTCPMu sync.Mutex
	e.mu.Lock()
	currentPeer := peerLID
	applyGroupUpdate := func(update types.GroupCallUpdate) error {
		audioPlayoutMu.Lock()
		defer audioPlayoutMu.Unlock()
		groupRTCPMu.Lock()
		defer groupRTCPMu.Unlock()
		var relayTransactionID [12]byte
		if update.Relay != nil {
			if _, err := rand.Read(relayTransactionID[:]); err != nil {
				return fmt.Errorf("generate group relay transaction ID: %w", err)
			}
		}
		// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L100-L141
		changed, err := applyGroupMediaUpdateTransaction(
			audioReceivers,
			&audioReception,
			allocateState,
			ep,
			streamSsrcs,
			update,
			relayTransactionID,
			func(packet []byte) error {
				_, sendErr := ch.Send(packet)
				return sendErr
			},
		)
		if err != nil {
			return fmt.Errorf("apply group media update: %w", err)
		}
		if changed {
			log.Info().
				Uint32("relay_transaction_id", update.Relay.TransactionID).
				Str("relay_name", ep.RelayName).
				Msg("refreshed group relay allocation")
			e.c.diag.Emit("stun", map[string]any{
				"event":                "group_allocate_sent",
				"bytes":                len(allocateState.Current()),
				"relay_transaction_id": update.Relay.TransactionID,
				"relay_name":           ep.RelayName,
			})
		}
		activeParticipantIDs := audioReceivers.ActiveParticipantIDs()
		audioMixer.Retain(activeParticipantIDs)
		if shouldStartParticipantMixing(activeParticipantIDs) && !groupMixing.Load() {
			_, sink := callPlayerSink(call)
			if err := audioPlayout.Drain(sink); err != nil {
				log.Warn().Err(err).Msg("failed to drain direct audio before group playout")
			}
			groupMixing.Store(true)
		}
		return nil
	}
	applyGroupRekey := func(rekey events.CallEncRekey) error {
		// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L56-L112
		return audioReceivers.ApplyGroupRawEpoch(
			rekey.Rekey.TransactionID,
			rekey.RawKey,
		)
	}
	if m := e.calls[callID]; m != nil {
		m.rekeyPeer = rekeyPeer
		currentPeer = m.peerLID
	}
	e.mu.Unlock()
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L97-L107
	e.activateGroupMedia(callID, applyGroupUpdate, applyGroupRekey)
	if currentPeer != "" && currentPeer != peerLID {
		if err := rekeyPeer(currentPeer); err != nil {
			return fmt.Errorf("rekey media to answering device: %w", err)
		}
		peerLID = currentPeer
	}
	defer func() {
		e.mu.Lock()
		if m := e.calls[callID]; m != nil && m.call == call {
			m.rekeyPeer = nil
			m.applyGroupUpdate = nil
			m.applyGroupRekey = nil
			// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L119-L122
			m.groupActivating = false
			m.groupActive = false
		}
		e.mu.Unlock()
	}()
	type videoReceiveState struct {
		depacketizer rtp.H264Depacketizer
		accessUnit   []byte
		orientation  int
	}
	videoReceiveStates := make(map[*participantAudioReceiver]*videoReceiveState)
	appDataReceivers := make(map[*participantAudioReceiver]*appDataReceiver)
	var videoWirePacket, videoWireFrame uint64

	// Video send: a second WARP pipeline on our video SSRC, registered on the call so
	// Call.SendVideoFrame can push encoded H.264 to the relay. Cleared when the loop exits.
	txVideoPipe, err := NewMediaPipeline(callKey, selfLID, peerLID, videoSelfSsrc, FrameSamples, WithLogger(log))
	if err != nil {
		return err
	}
	if err = audioReceivers.attachSendPipeline(txVideoPipe); err != nil {
		return fmt.Errorf("attach video RTP sender: %w", err)
	}
	videoRtcp, err := newMediaSrtcpSender(callKey, selfLID, videoSelfSsrc, true)
	if err != nil {
		return err
	}
	if err = audioReceivers.attachSRTCPSender(videoRtcp); err != nil {
		return fmt.Errorf("attach video SRTCP sender: %w", err)
	}
	vsender := &videoSender{
		pipe: txVideoPipe, stream: rtp.NewVideoRtpStream(videoSelfSsrc, defaultVideoRtpStepSamples),
		ch: ch, ssrc: videoSelfSsrc, callID: callID, keyframeRequired: true,
		log: log, diag: e.c.diag,
	}
	txAppDataPipe, err := NewMediaPipeline(callKey, selfLID, peerLID, appDataSelfSsrc, FrameSamples, WithLogger(log))
	if err != nil {
		return err
	}
	if err = audioReceivers.attachSendPipeline(txAppDataPipe); err != nil {
		return fmt.Errorf("attach app-data RTP sender: %w", err)
	}
	appSender := newAppDataSender(txAppDataPipe, appDataSelfSsrc, func(packet []byte) (int, error) {
		if header, ok := rtp.ParseRtpHeader(packet); ok {
			e.c.diag.Emit("app_data", map[string]any{
				"event": "out", "ssrc": header.Ssrc, "seq": header.SequenceNumber,
				"ts": header.Timestamp, "pt": header.PayloadType, "bytes": len(packet),
			})
		}
		return ch.Send(packet)
	})
	e.mu.Lock()
	if m := e.calls[callID]; m != nil {
		vsender.active = m.localVideo
		vsender.sendGated = m.videoGate
		m.videoTx = vsender
		m.appDataTx = appSender
	}
	e.mu.Unlock()
	defer func() {
		appSender.close()
		vsender.mu.Lock()
		vsender.ch = nil
		vsender.mu.Unlock()
		e.mu.Lock()
		if m := e.calls[callID]; m != nil {
			m.videoTx = nil
			m.appDataTx = nil
		}
		e.mu.Unlock()
	}()

	var videoReception rtp.RtcpReceptionStats

	// WhatsApp associates the RTP streams with an SRTCP session. Periodic compound
	// SR+SDES packets are required for the caller's video to start flowing to the
	// answerer, and give the peer a target for PLI/FIR recovery feedback.
	go func() {
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		var successfulTicks uint64
		var tick uint64
		var audioReports []*rtp.RtcpReceptionReport
		var audioReportsSent int
		runMediaSRTCPTicks(
			ctx,
			ticker.C,
			func(now time.Time) error {
				tick++
				nowMs := uint64(now.UnixMilli())
				audioReports = audioReception.Reports(nowMs)
				var err error
				audioReportsSent, err = sendMediaSrtcpReceptionReports(
					audioRtcp,
					txPipe.SenderStats(),
					nowMs,
					audioReports,
					func(packet []byte) error {
						_, sendErr := ch.Send(packet)
						return sendErr
					},
				)
				if err != nil {
					return fmt.Errorf("send audio SRTCP reports: %w", err)
				}
				videoStats := txVideoPipe.SenderStats()
				if videoStats.PacketsSent > 0 {
					videoReport := videoReception.Report(nowMs)
					videoPacket, err := videoRtcp.senderReport(videoStats, nowMs, videoReport)
					if err != nil {
						return fmt.Errorf("protect video SRTCP report: %w", err)
					}
					if _, err = ch.Send(videoPacket); err != nil {
						return fmt.Errorf("send video SRTCP report: %w", err)
					}
				}
				successfulTicks++
				if successfulTicks == 1 {
					log.Info().Msg("started periodic SRTCP sender reports")
				}
				e.c.diag.Emit("rtcp", map[string]any{
					"event": "sender_reports", "tick": tick,
					"audio_packets": txPipe.SenderStats().PacketsSent,
					"audio_reports": len(audioReports),
					"video_packets": videoStats.PacketsSent,
				})
				return nil
			},
			func(err error) {
				log.Warn().
					Err(err).
					Uint64("tick", tick).
					Int("audio_reports", len(audioReports)).
					Int("audio_reports_sent", audioReportsSent).
					Msg("periodic SRTCP sender report failed; retrying next tick")
				e.c.diag.Emit("rtcp", map[string]any{
					"event": "sender_reports_failed", "call_id": callID,
					"tick": tick, "audio_reports": len(audioReports),
					"audio_reports_sent": audioReportsSent, "error": err.Error(),
				})
			},
		)
	}()

	buf := make([]byte, 1500)
	var rtpIn, rtpSeen, unprotectFail, rtpInspect, vidIn, appDataIn, appDataUnprotectFail, videoUnprotectFail, videoFrameIn, videoSinkMissing, rtcpIn, rtcpAuthFail uint64
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := ch.Recv(buf)
		if err != nil {
			return fmt.Errorf("relay recv: %w", err)
		}
		relayRx.Add(1)
		pkt := buf[:n]
		packetKind := relay.ClassifyRelayPacket(pkt)
		isRTP := packetKind == relay.RelayPacketRtp
		e.c.diag.Emit("relay", map[string]any{
			"event": "packet_in", "bytes": n, "is_rtp": isRTP,
			"packet_hex": hex.EncodeToString(pkt),
		})
		switch packetKind {
		case relay.RelayPacketRtcp:
			senderSsrc, ok := rtp.ParseRtcpSenderSsrc(pkt)
			if !ok {
				continue
			}
			plain, index, ok := audioReceivers.UnprotectSRTCP(senderSsrc, pkt)
			if !ok {
				if rtcpAuthFail++; rtcpAuthFail == 1 {
					log.Warn().Uint32("ssrc", senderSsrc).Msg("peer SRTCP failed authentication")
				}
				e.c.diag.Emit("rtcp", map[string]any{
					"event": "auth_failed", "ssrc": senderSsrc, "bytes": n,
				})
				continue
			}
			if rtcpIn++; rtcpIn == 1 {
				log.Info().Uint32("ssrc", senderSsrc).Uint32("index", index).Msg("first authenticated peer SRTCP received")
			}
			keyframe := rtp.RtcpRequestsKeyframe(plain, videoSelfSsrc)
			if keyframe {
				vsender.requestKeyframe()
				if call != nil {
					call.requestVideoKeyframe()
				}
				log.Debug().Uint32("video_ssrc", videoSelfSsrc).Msg("peer requested a video keyframe")
			}
			if reportSsrc, ntpSeconds, ntpFraction, ok := rtp.ParseSenderReportTiming(plain); ok {
				nowMs := uint64(time.Now().UnixMilli())
				audioReception.ObserveSenderReport(reportSsrc, ntpSeconds, ntpFraction, nowMs)
				videoReception.ObserveSenderReport(reportSsrc, ntpSeconds, ntpFraction, nowMs)
			}
			e.c.diag.Emit("rtcp", map[string]any{
				"event": "in", "ssrc": senderSsrc, "index": index,
				"plain_hex": hex.EncodeToString(plain), "requests_keyframe": keyframe,
			})
			continue
		case relay.RelayPacketStun:
			mt, isStun := stun.StunMessageType(pkt)
			if isStun && mt == stun.MsgBindingRequest {
				// Source of truth: https://github.com/purpshell/meowcaller/blob/bcfb7f0c076b131422c22f024dfff080448e70f4/datasheets/group-media-relay-refresh.md#L79-L111
				// ASSUMPTION: binding-success uses the committed Allocate integrity
				// key; a live post-rotation response using another key invalidates this.
				resp, answered, err := allocateState.SendBindingSuccess(pkt, func(packet []byte) error {
					_, sendErr := ch.Send(packet)
					return sendErr
				})
				if err != nil {
					return fmt.Errorf("relay send binding-success: %w", err)
				}
				if !answered {
					continue
				}
				e.c.diag.Emit("stun", map[string]any{
					"event":     "binding_request_answered",
					"tx_id_hex": hex.EncodeToString(resp[8:20]), "resp_hex": hex.EncodeToString(resp),
				})
			}
			continue
		case relay.RelayPacketOther:
			continue
		}
		if rtpSeen++; rtpSeen == 1 {
			log.Info().Int("bytes", n).Msg("first RTP-classified packet from relay, relay is bridging the peer's media")
		}
		vh, vok := rtp.ParseRtpHeader(pkt)
		if vok {
			if rtpInspect < 20 || vh.PayloadType == rtp.RtpPayloadTypeH264 || vh.PayloadType == rtp.RtpPayloadTypeAppData {
				log.Debug().
					Uint8("payload_type", vh.PayloadType).
					Uint32("ssrc", vh.Ssrc).
					Uint16("seq", vh.SequenceNumber).
					Uint32("timestamp", vh.Timestamp).
					Bool("marker", vh.Marker).
					Int("bytes", n).
					Msg("relay RTP packet summary")
			}
			rtpInspect++
		}
		if !vok {
			continue
		}
		kind := classifyMediaPayload(vh)
		if kind == mediaPayloadAppData {
			media, ok := audioReceivers.UnprotectAppData(pkt)
			if !ok {
				if appDataUnprotectFail++; appDataUnprotectFail == 1 {
					log.Warn().Uint32("ssrc", vh.Ssrc).Uint16("seq", vh.SequenceNumber).Msg("app-data RTP arrived but failed to unprotect")
				}
				e.c.diag.Emit("app_data", map[string]any{"event": "unprotect_failed", "ssrc": vh.Ssrc, "seq": vh.SequenceNumber})
				continue
			}
			receiver := appDataReceivers[media.receiver]
			if receiver == nil {
				receiver = &appDataReceiver{}
				appDataReceivers[media.receiver] = receiver
			}
			handled, err := handleAppDataReactionFrom(call, receiver, media.UserJID, media.Payload)
			if err != nil {
				log.Warn().Err(err).Uint32("ssrc", media.Header.Ssrc).Uint16("seq", media.Header.SequenceNumber).Msg("invalid RTC app-data payload")
				e.c.diag.Emit("app_data", map[string]any{
					"event": "decode_failed", "ssrc": media.Header.Ssrc, "seq": media.Header.SequenceNumber,
					"payload_hex": hex.EncodeToString(media.Payload), "error": err.Error(),
				})
				continue
			}
			e.c.diag.Emit("app_data", map[string]any{
				"event": "in", "ssrc": media.Header.Ssrc, "seq": media.Header.SequenceNumber,
				"ts": media.Header.Timestamp, "handled": handled, "payload_hex": hex.EncodeToString(media.Payload),
				"participant_id": media.ParticipantID,
			})
			if handled {
				if appDataIn++; appDataIn == 1 {
					log.Info().Uint32("ssrc", media.Header.Ssrc).Msg("first RTC call reaction received")
				}
			}
			continue
		}
		// Demux H.264 (PT 97) to video and emit Annex-B access units on the marker bit.
		// Source of truth: https://github.com/JotaDev66/WaCalls/blob/2d6a1f666426049a89ef9541414e771acdcf8a16/internal/voip/call/callmanager_video.go#L86-L126
		if kind == mediaPayloadVideo {
			media, vunok := audioReceivers.UnprotectVideo(pkt)
			if !vunok {
				if videoUnprotectFail++; videoUnprotectFail == 1 {
					log.Warn().
						Uint32("ssrc", vh.Ssrc).
						Uint16("seq", vh.SequenceNumber).
						Msg("video RTP arrived but failed to unprotect")
				}
				e.c.diag.Emit("video", map[string]any{"event": "unprotect_failed", "ssrc": vh.Ssrc, "seq": vh.SequenceNumber})
				continue
			}
			vh = media.Header
			videoState := videoReceiveStates[media.receiver]
			if videoState == nil {
				videoState = &videoReceiveState{orientation: -1}
				videoReceiveStates[media.receiver] = videoState
			}
			videoReception.Observe(vh.Ssrc, vh.SequenceNumber, vh.Timestamp, uint64(time.Now().UnixMilli()), 90000)
			if vh.VideoExtension != nil {
				orientation := vh.VideoExtension.DisplayOrientation()
				if orientation != videoState.orientation {
					videoState.orientation = orientation
					if sink, ok := callVideoSink(call).(VideoOrientationSink); ok {
						sink.SetOrientation(orientation)
					}
					log.Debug().
						Int("orientation", orientation).
						Uint8("media_frame_info", vh.VideoExtension.MediaFrameInfo).
						Msg("updated peer video orientation from RTP")
					e.c.diag.Emit("video", map[string]any{
						"event": "orientation", "orientation": orientation,
						"media_frame_info": vh.VideoExtension.MediaFrameInfo,
					})
				}
			}
			if videoWirePacket < videoWirePacketLimit {
				headerLen, _ := rtp.RtpHeaderByteLength(pkt)
				_, extension, _ := rtp.RtpExtensionProfileAndData(pkt)
				e.c.diag.Emit("video_wire", map[string]any{
					"event": "packet", "direction": "in", "call_id": callID,
					"packet": videoWirePacket, "frame": videoWireFrame,
					"ssrc": vh.Ssrc, "seq": vh.SequenceNumber, "rtp_ts": vh.Timestamp,
					"marker": vh.Marker, "header_hex": hex.EncodeToString(pkt[:headerLen]),
					"extension_hex": hex.EncodeToString(extension),
					"payload_hex":   hex.EncodeToString(media.Payload), "protected_hex": hex.EncodeToString(pkt),
					"participant_id": media.ParticipantID,
				})
			}
			videoWirePacket++
			for _, nalu := range videoState.depacketizer.Depacketize(media.Payload) {
				videoState.accessUnit = append(videoState.accessUnit, 0x00, 0x00, 0x00, 0x01)
				videoState.accessUnit = append(videoState.accessUnit, nalu...)
			}
			if vh.Marker && len(videoState.accessUnit) > 0 {
				frame := videoState.accessUnit
				videoState.accessUnit = nil
				if videoWireFrame < videoWireFrameLimit {
					e.c.diag.Emit("video_wire", map[string]any{
						"event": "access_unit", "direction": "in", "call_id": callID,
						"frame": videoWireFrame, "ssrc": vh.Ssrc, "rtp_ts": vh.Timestamp,
						"idr": rtp.AUHasIDR(frame), "bytes": len(frame),
						"annexb_hex": hex.EncodeToString(frame),
					})
				}
				videoWireFrame++
				e.c.diag.Emit("video", map[string]any{"event": "frame", "ssrc": vh.Ssrc, "bytes": len(frame)})
				if sink := callVideoSink(call); sink != nil {
					if err := sink.WriteVideo(frame); err != nil {
						log.Warn().Err(err).Uint32("ssrc", vh.Ssrc).Int("bytes", len(frame)).Msg("failed to write WhatsApp video frame to sink")
					} else {
						if videoFrameIn == 0 {
							log.Info().Uint32("ssrc", vh.Ssrc).Int("bytes", len(frame)).Msg("first WhatsApp video frame written to sink")
						}
						videoFrameIn++
					}
				} else {
					if videoSinkMissing == 0 {
						log.Warn().Uint32("ssrc", vh.Ssrc).Int("bytes", len(frame)).Msg("WhatsApp video frame arrived with no sink attached")
					}
					videoSinkMissing++
				}
			}
			if vidIn++; vidIn == 1 {
				log.Info().Uint32("ssrc", vh.Ssrc).Msg("first video RTP demuxed from relay (NOT VALIDATED)")
				e.c.diag.Emit("meta", map[string]any{"event": "first_video_rtp_in", "call_id": callID, "ssrc": vh.Ssrc})
			}
			continue
		}
		if kind != mediaPayloadAudio {
			log.Debug().Uint8("payload_type", vh.PayloadType).Uint32("ssrc", vh.Ssrc).Msg("dropping unknown RTP payload")
			e.c.diag.Emit("rtp", map[string]any{
				"event": "unknown_payload", "pt": vh.PayloadType, "ssrc": vh.Ssrc,
				"seq": vh.SequenceNumber, "ts": vh.Timestamp,
			})
			continue
		}
		audio, ok := audioReceivers.DecodeAudio(pkt)
		if !ok {
			if unprotectFail++; unprotectFail == 1 {
				log.Warn().Uint32("ssrc", vh.Ssrc).Int("bytes", n).Msg("audio RTP arrived but did not match an authenticated active participant")
			}
			e.c.diag.Emit("srtp", map[string]any{"event": "unprotect_failed", "ssrc": vh.Ssrc, "bytes": n})
			continue
		}
		audioReception.Observe(audio.SSRC, vh.SequenceNumber, audio.Timestamp, uint64(time.Now().UnixMilli()), SampleRate)
		e.c.diag.Emit("rtp", map[string]any{
			"event": "in", "ssrc": audio.SSRC, "seq": vh.SequenceNumber,
			"ts": audio.Timestamp, "pt": vh.PayloadType, "marker": vh.Marker,
			"participant": audio.DeviceJID.String(), "pid": audio.PID, "has_pid": audio.HasPID,
		})
		e.c.diag.Emit("srtp", map[string]any{
			"event": "frame_authenticated", "ssrc": audio.SSRC, "seq": vh.SequenceNumber,
			"participant": audio.DeviceJID.String(), "pid": audio.PID, "has_pid": audio.HasPID,
		})
		e.c.diag.Emit("media_in", map[string]any{
			"seq": vh.SequenceNumber, "samples": len(audio.PCM),
			"pcm_rms": rmsFloat32(audio.PCM), "participant": audio.DeviceJID.String(),
		})
		mixedMode := groupMixing.Load()
		playoutLocked := false
		if !mixedMode {
			audioPlayoutMu.Lock()
			playoutLocked = true
			mixedMode = groupMixing.Load()
		}
		if mixedMode {
			audioMixer.Add(audio.ParticipantID, audio.PCM)
		} else {
			_, sink := callPlayerSink(call)
			playoutStarted, playoutErr := audioPlayout.Push(audio.Timestamp, audio.PCM, sink)
			if playoutErr != nil {
				log.Warn().Err(playoutErr).Msg("failed to write timestamp-aligned WhatsApp audio")
			}
			if playoutStarted {
				log.Info().Int("prefill_ms", audioPlayoutPrefillSamples*1000/SampleRate).Msg("started timestamp-aligned inbound audio playout")
				e.c.diag.Emit("meta", map[string]any{
					"event": "audio_playout_started", "call_id": callID,
					"prefill_ms": audioPlayoutPrefillSamples * 1000 / SampleRate,
				})
			}
		}
		if playoutLocked {
			audioPlayoutMu.Unlock()
		}
		if rtpIn++; rtpIn == 1 {
			log.Info().Msg("first RTP decoded from relay, inbound audio flowing")
			e.c.diag.Emit("meta", map[string]any{"event": "first_rtp_in", "call_id": callID})
			if call != nil {
				call.setPhase(CallPhaseActive)
				if fn := call.onReadyFn(); fn != nil {
					fn()
				}
			}
		}
	}
}

func applyGroupAudioRoster(receivers *participantReceiveRegistry, reception *rtp.RtcpReceptionStatsSet, update types.GroupCallUpdate) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/6e202a6d6ec5a9384bae6ccbe621966edeee6592/datasheets/group-media-rtcp-feedback.md#L128-L146
	_, err := applyGroupMediaUpdateTransaction(
		receivers,
		reception,
		nil,
		nil,
		[9]uint32{},
		update,
		[12]byte{},
		nil,
	)
	return err
}

func applyGroupMediaUpdateTransaction(
	receivers *participantReceiveRegistry,
	reception *rtp.RtcpReceptionStatsSet,
	allocateState *groupRelayAllocateState,
	endpoint *types.RelayEndpoint,
	streamSSRCs [9]uint32,
	update types.GroupCallUpdate,
	relayTransactionID [12]byte,
	send func([]byte) error,
) (bool, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/65b1dbf33f365db7392e438c3e3bf3651decb6cf/datasheets/group-media-receive.md#L100-L141
	if receivers == nil {
		return false, fmt.Errorf("meowcaller: participant receive registry is nil")
	}
	if reception == nil {
		return false, fmt.Errorf("meowcaller: RTCP reception set is nil")
	}
	changed := false
	err := receivers.ApplyGroupUpdateTransaction(update, func(commit func()) error {
		if update.Relay == nil {
			commit()
			return nil
		}
		if allocateState == nil {
			return fmt.Errorf("meowcaller: group relay allocate state is nil")
		}
		var err error
		changed, err = allocateState.Apply(
			endpoint,
			update.Relay,
			streamSSRCs,
			relayTransactionID,
			func(packet []byte) error {
				if send == nil {
					return fmt.Errorf("meowcaller: group relay send is nil")
				}
				if sendErr := send(packet); sendErr != nil {
					return sendErr
				}
				commit()
				return nil
			},
		)
		if err != nil {
			return err
		}
		if !changed {
			commit()
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	reception.Retain(receivers.ActiveAudioSSRCs())
	return changed, nil
}

func runMediaSRTCPTicks(ctx context.Context, ticks <-chan time.Time, send func(time.Time) error, onError func(error)) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/6e202a6d6ec5a9384bae6ccbe621966edeee6592/datasheets/group-media-rtcp-feedback.md#L142-L146
	for {
		select {
		case <-ctx.Done():
			return
		case now, ok := <-ticks:
			if !ok {
				return
			}
			if err := send(now); err != nil {
				if onError != nil {
					onError(err)
				}
			}
		}
	}
}

// callPlayerSink returns a Call's current Player and sink, tolerating a nil Call (an
// outbound call may never have had one attached).
func callPlayerSink(call *Call) (*Player, AudioSink) {
	if call == nil {
		return nil, nil
	}
	return call.playerAndSink()
}

// callVideoSink returns a Call's inbound-video sink, tolerating a nil Call.
func callVideoSink(call *Call) VideoSink {
	if call == nil {
		return nil
	}
	return call.videoSinkRef()
}

const defaultVideoRtpStepSamples = 90000 / 30

const (
	videoWirePacketLimit = 120
	videoWireFrameLimit  = 30
)

func videoRtpDurationSamples(duration time.Duration) uint32 {
	if duration <= 0 {
		return defaultVideoRtpStepSamples
	}
	samples := uint32((duration.Nanoseconds()*90000 + int64(time.Second)/2) / int64(time.Second))
	if samples == 0 {
		return defaultVideoRtpStepSamples
	}
	return samples
}

// videoSender packetizes encoded H.264 access units (Annex-B) into PT-97 RTP, E2E-SRTP
// protects them with the video pipeline, and sends them to the relay. The send path is
// fed encoded H.264 (e.g. from the VideoBridge / WebCodecs), not raw frames.
//
// NOT VALIDATED: the video send media path is unproven.
type videoSender struct {
	mu               sync.Mutex
	pipe             *MediaPipeline
	stream           *rtp.VideoRtpStream
	ch               *relay.RelayMediaChannel
	ssrc             uint32
	callID           string
	frame            uint64
	logged           bool
	active           bool
	sendGated        bool
	keyframeRequired bool
	log              zerolog.Logger
	diag             *diag.Recorder
}

type mediaSrtcpSender struct {
	mu      sync.Mutex
	keys    srtp.E2eSrtpKeys
	ssrc    uint32
	cname   [rtp.WhatsappRtcpCnameLen]byte
	profile bool
	index   uint32
}

type mediaSrtcpReceiver struct {
	mu   sync.Mutex
	keys srtp.E2eSrtpKeys
}

func newMediaSrtcpReceiver(callKey []byte, peerLID string) (*mediaSrtcpReceiver, error) {
	keys, err := srtp.DeriveE2eSrtcpKeys(callKey, rtp.FormatE2ESrtpParticipantID(peerLID))
	if err != nil {
		return nil, err
	}
	return &mediaSrtcpReceiver{keys: keys}, nil
}

func (r *mediaSrtcpReceiver) rekey(callKey []byte, peerLID string) error {
	keys, err := srtp.DeriveE2eSrtcpKeys(callKey, rtp.FormatE2ESrtpParticipantID(peerLID))
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.keys = keys
	r.mu.Unlock()
	return nil
}

func (r *mediaSrtcpReceiver) installKeys(keys srtp.E2eSrtpKeys) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L34-L70
	r.mu.Lock()
	r.keys = keys
	r.mu.Unlock()
}

func (r *mediaSrtcpReceiver) unprotect(senderSSRC uint32, packet []byte) ([]byte, uint32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return srtp.UnprotectSrtcp(&r.keys, senderSSRC, packet)
}

func newMediaSrtcpSender(callKey []byte, selfLID string, ssrc uint32, profile bool) (*mediaSrtcpSender, error) {
	keys, err := srtp.DeriveE2eSrtcpKeys(callKey, rtp.FormatE2ESrtpParticipantID(selfLID))
	if err != nil {
		return nil, err
	}
	var entropy [12]byte
	_, _ = rand.Read(entropy[:])
	return &mediaSrtcpSender{
		keys: keys, ssrc: ssrc, cname: rtp.BuildWhatsappRtcpCname(entropy),
		profile: profile, index: 1,
	}, nil
}

func (s *mediaSrtcpSender) installKeys(keys srtp.E2eSrtpKeys) {
	// Source of truth: https://github.com/oxidezap/whatsapp-rust/blob/41095d4e6ba4610e054e9ede3af1d5e88a83faee/wacore/src/voip/e2e_srtp.rs#L34-L70
	s.mu.Lock()
	s.keys = keys
	s.mu.Unlock()
}

func (s *mediaSrtcpSender) senderReport(stats rtp.RtcpSenderStats, nowMs uint64, report *rtp.RtcpReceptionReport) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plain := rtp.BuildSenderReportWithSdesAndReception(s.ssrc, &stats, nowMs, &s.cname, report, s.profile)
	packet, err := srtp.ProtectSrtcp(&s.keys, s.ssrc, s.index, plain)
	if err == nil {
		s.index++
	}
	return packet, err
}

func (s *mediaSrtcpSender) groupSenderReport(stats rtp.RtcpSenderStats, nowMs uint64, report *rtp.RtcpReceptionReport) ([]byte, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/6e202a6d6ec5a9384bae6ccbe621966edeee6592/datasheets/group-media-rtcp-feedback.md#L53-L75
	s.mu.Lock()
	defer s.mu.Unlock()
	plain := rtp.BuildGroupSenderReport(s.ssrc, &stats, nowMs, report, rtp.RTCPGroupReportExtension{})
	packet, err := srtp.ProtectSrtcp(&s.keys, s.ssrc, s.index, plain)
	if err == nil {
		s.index++
	}
	return packet, err
}

func sendMediaSrtcpReceptionReports(
	sender *mediaSrtcpSender,
	stats rtp.RtcpSenderStats,
	nowMs uint64,
	reports []*rtp.RtcpReceptionReport,
	send func([]byte) error,
) (int, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/7594217b4386a1c056d0e3ecd1049b30a1101241/datasheets/group-media-rtcp-feedback.md#L67-L80
	// Source of truth: https://github.com/purpshell/meowcaller/blob/6e202a6d6ec5a9384bae6ccbe621966edeee6592/datasheets/group-media-rtcp-feedback.md#L116-L146
	if sender == nil {
		return 0, fmt.Errorf("meowcaller: SRTCP sender is nil")
	}
	if send == nil {
		return 0, fmt.Errorf("meowcaller: SRTCP send function is nil")
	}
	if len(reports) == 0 {
		packet, err := sender.senderReport(stats, nowMs, nil)
		if err != nil {
			return 0, err
		}
		if err = send(packet); err != nil {
			return 0, err
		}
		return 1, nil
	}
	sent := 0
	for _, report := range reports {
		packet, err := sender.groupSenderReport(stats, nowMs, report)
		if err != nil {
			return sent, err
		}
		if err = send(packet); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

func (vs *videoSender) protectAccessUnit(au []byte, duration time.Duration) [][]byte {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	return vs.protectAccessUnitLocked(au, duration)
}

func (vs *videoSender) protectAccessUnitLocked(au []byte, duration time.Duration) [][]byte {
	if !vs.active || vs.sendGated {
		return nil
	}
	idr := rtp.AUHasIDR(au)
	if vs.keyframeRequired && !idr {
		return nil
	}
	nalus := rtp.SplitAnnexB(au)
	var packedAccessUnit []byte
	for _, n := range nalus {
		if len(n) == 0 || n[0]&0x1f == 9 {
			continue
		}
		if len(packedAccessUnit) > 0 {
			packedAccessUnit = append(packedAccessUnit, 0, 0, 0, 1)
		}
		packedAccessUnit = append(packedAccessUnit, n...)
	}
	if len(packedAccessUnit) == 0 {
		return nil
	}
	// WhatsApp fragments the complete Annex-B access unit as one RTP NAL unit.
	payloads := rtp.PackageH264NALU(packedAccessUnit)
	captureWire := vs.frame < videoWireFrameLimit
	if captureWire {
		vs.diag.Emit("video_wire", map[string]any{
			"event": "access_unit", "direction": "out", "call_id": vs.callID,
			"frame": vs.frame, "ssrc": vs.ssrc, "idr": idr, "bytes": len(au),
			"duration_ms": duration.Milliseconds(), "annexb_hex": hex.EncodeToString(au),
		})
	}
	vs.stream.SetTimestampStride(videoRtpDurationSamples(duration))
	mediaFrameInfo := rtp.VideoMediaFrameInfoDelta
	if idr {
		mediaFrameInfo = rtp.VideoMediaFrameInfoIDR
	}
	packets := make([][]byte, 0, len(payloads))
	for i, payload := range payloads {
		header := vs.stream.NextPacket(i == len(payloads)-1, mediaFrameInfo)
		packet, err := vs.pipe.ProtectRTP(&header, payload)
		if err == nil {
			packets = append(packets, packet)
			if captureWire {
				headerBytes := rtp.EncodeRtpHeader(&header)
				_, extension, _ := rtp.RtpExtensionProfileAndData(headerBytes)
				vs.diag.Emit("video_wire", map[string]any{
					"event": "packet", "direction": "out", "call_id": vs.callID,
					"frame": vs.frame, "packet": i, "ssrc": header.Ssrc,
					"seq": header.SequenceNumber, "rtp_ts": header.Timestamp, "marker": header.Marker,
					"header_hex":    hex.EncodeToString(headerBytes),
					"extension_hex": hex.EncodeToString(extension),
					"payload_hex":   hex.EncodeToString(payload), "protected_hex": hex.EncodeToString(packet),
				})
			}
		}
	}
	if len(packets) > 0 && idr {
		vs.keyframeRequired = false
	}
	return packets
}

func (vs *videoSender) enable(sendGated bool) {
	vs.mu.Lock()
	needsRecovery := !vs.active || (vs.sendGated && !sendGated)
	vs.active = true
	vs.sendGated = sendGated
	vs.keyframeRequired = vs.keyframeRequired || needsRecovery
	vs.mu.Unlock()
}

func (vs *videoSender) disable() {
	vs.mu.Lock()
	vs.active = false
	vs.sendGated = false
	vs.keyframeRequired = true
	vs.mu.Unlock()
}

func (vs *videoSender) requestKeyframe() {
	vs.mu.Lock()
	vs.keyframeRequired = true
	vs.mu.Unlock()
}

// send fragments one Annex-B access unit into PT-97 RTP packets (marker on the last) and
// sends them to the relay.
func (vs *videoSender) send(au []byte, duration time.Duration) {
	if vs == nil || len(au) == 0 {
		return
	}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if vs.ch == nil {
		return
	}
	packets := vs.protectAccessUnitLocked(au, duration)
	if len(packets) == 0 {
		return
	}
	sent := 0
	wireBytes := 0
	for _, pkt := range packets {
		if _, err := vs.ch.Send(pkt); err != nil {
			return
		}
		sent++
		wireBytes += len(pkt)
	}
	frame := vs.frame
	if sent > 0 {
		vs.frame++
		if frame < 10 || frame%30 == 0 {
			vs.diag.Emit("video_out", map[string]any{
				"event": "frame", "call_id": vs.callID, "frame": frame,
				"ssrc": vs.ssrc, "rtp_ts": vs.pipe.SenderStats().RtpTimestamp,
				"packets": sent, "access_unit_bytes": len(au),
				"wire_bytes": wireBytes, "duration_ms": duration.Milliseconds(),
				"duration_samples": videoRtpDurationSamples(duration),
			})
		}
	}
	if sent > 0 && !vs.logged {
		vs.logged = true
		vs.log.Info().
			Int("packets", sent).
			Uint32("ssrc", vs.ssrc).
			Msg("first video RTP sent to relay, outbound video flowing")
	}
}

// rmsFloat32 returns the root-mean-square level of a PCM frame, a cheap loudness
// metric for the media diagnostic streams (avoids inlining raw float32 PCM).
func rmsFloat32(f []float32) float64 {
	if len(f) == 0 {
		return 0
	}
	var sum float64
	for _, s := range f {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(f)))
}
