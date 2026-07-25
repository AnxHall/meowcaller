package meowcaller

import (
	"bytes"
	"fmt"
	"slices"
	"sync"

	"github.com/purpshell/meowcaller/mlow"
	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/srtp"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
)

type participantAudioDecoder interface {
	Decode([]byte) []float32
}

type decodedParticipantAudio struct {
	ParticipantID string
	UserJID       types.JID
	DeviceJID     types.JID
	PID           uint32
	HasPID        bool
	SSRC          uint32
	Timestamp     uint32
	PCM           []float32
}

type unprotectedParticipantMedia struct {
	ParticipantID string
	UserJID       types.JID
	DeviceJID     types.JID
	PID           uint32
	HasPID        bool
	Header        rtp.RtpHeader
	Payload       []byte
	receiver      *participantAudioReceiver
}

type participantAudioReceiver struct {
	mu            sync.Mutex
	userJID       types.JID
	deviceJID     types.JID
	participantID string
	pid           uint32
	hasPID        bool
	ssrc          uint32
	streamSSRCs   [9]uint32
	videoSSRC     uint32
	appDataSSRC   uint32
	pipe          *MediaPipeline
	videoPipe     *MediaPipeline
	appDataPipe   *MediaPipeline
	srtcp         *mediaSrtcpReceiver
	decoder       participantAudioDecoder
}

type installedGroupRawEpoch struct {
	transactionID uint32
	rawKey        []byte
}

type participantReceiveRegistry struct {
	mu             sync.RWMutex
	callID         string
	callKey        []byte
	selfLID        string
	selfID         string
	fallbackID     string
	transactionID  uint32
	hasGroupUpdate bool
	decoderFactory func() participantAudioDecoder
	byDeviceID     map[string]*participantAudioReceiver
	byPID          map[uint32]*participantAudioReceiver
	bySSRC         map[uint32]*participantAudioReceiver
	byVideoSSRC    map[uint32]*participantAudioReceiver
	byAppDataSSRC  map[uint32]*participantAudioReceiver
	bySRTCPSSRC    map[uint32]*participantAudioReceiver
	sendPipes      []*MediaPipeline
	srtcpSenders   []*mediaSrtcpSender
	pendingEpochs  map[uint32][]byte
	installedEpoch installedGroupRawEpoch
	hasEpoch       bool
	log            zerolog.Logger
}

func newParticipantReceiveRegistry(
	callID string,
	callKey []byte,
	selfLID string,
	peerLID string,
	decoderFactory func() participantAudioDecoder,
	opts ...Option,
) (*participantReceiveRegistry, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L24-L44
	if decoderFactory == nil {
		decoderFactory = func() participantAudioDecoder {
			return mlow.NewMlowDecoder()
		}
	}
	r := &participantReceiveRegistry{
		callID:         callID,
		callKey:        append([]byte(nil), callKey...),
		selfLID:        selfLID,
		selfID:         rtp.FormatE2ESrtpParticipantID(selfLID),
		decoderFactory: decoderFactory,
		byDeviceID:     make(map[string]*participantAudioReceiver),
		byPID:          make(map[uint32]*participantAudioReceiver),
		bySSRC:         make(map[uint32]*participantAudioReceiver),
		byVideoSSRC:    make(map[uint32]*participantAudioReceiver),
		byAppDataSSRC:  make(map[uint32]*participantAudioReceiver),
		bySRTCPSSRC:    make(map[uint32]*participantAudioReceiver),
		pendingEpochs:  make(map[uint32][]byte),
		log:            resolveConfig(opts).log,
	}
	peerJID, err := types.ParseJID(peerLID)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: parse initial media peer: %w", err)
	}
	receiver, err := r.newReceiver(peerJID.ToNonAD(), peerJID, 0, false)
	if err != nil {
		return nil, err
	}
	r.fallbackID = receiver.participantID
	r.byDeviceID[receiver.participantID] = receiver
	r.bySSRC[receiver.ssrc] = receiver
	r.byVideoSSRC[receiver.videoSSRC] = receiver
	r.byAppDataSSRC[receiver.appDataSSRC] = receiver
	for _, streamSSRC := range receiver.streamSSRCs {
		r.bySRTCPSSRC[streamSSRC] = receiver
	}
	return r, nil
}

func (r *participantReceiveRegistry) newReceiver(userJID, deviceJID types.JID, pid uint32, hasPID bool) (*participantAudioReceiver, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L37-L44
	participantID := rtp.FormatE2ESrtpParticipantID(deviceJID.String())
	streamSSRCs, err := rtp.DeriveWasmRelayStreamSsrcs(r.callID, participantID, r.log)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: derive participant stream SSRCs: %w", err)
	}
	ssrc := streamSSRCs[0]
	videoSSRC, err := rtp.DeriveWasmParticipantSsrc(r.callID, participantID, rtp.VideoSlotWord, r.log)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: derive participant video SSRC: %w", err)
	}
	appDataSSRC, err := rtp.DeriveWasmParticipantSsrc(r.callID, participantID, rtp.AppDataSlotWord, r.log)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: derive participant app-data SSRC: %w", err)
	}
	pipe, err := NewMediaPipeline(r.callKey, r.selfLID, deviceJID.String(), ssrc, FrameSamples, WithLogger(r.log))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant receive pipeline: %w", err)
	}
	videoPipe, err := NewMediaPipeline(r.callKey, r.selfLID, deviceJID.String(), videoSSRC, FrameSamples, WithLogger(r.log))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant video receive pipeline: %w", err)
	}
	appDataPipe, err := NewMediaPipeline(r.callKey, r.selfLID, deviceJID.String(), appDataSSRC, FrameSamples, WithLogger(r.log))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant app-data receive pipeline: %w", err)
	}
	srtcpReceiver, err := newMediaSrtcpReceiver(r.callKey, deviceJID.String())
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant SRTCP receiver: %w", err)
	}
	return &participantAudioReceiver{
		userJID: userJID, deviceJID: deviceJID, participantID: participantID,
		pid: pid, hasPID: hasPID, ssrc: ssrc, streamSSRCs: streamSSRCs,
		videoSSRC: videoSSRC, appDataSSRC: appDataSSRC,
		pipe: pipe, videoPipe: videoPipe, appDataPipe: appDataPipe,
		srtcp: srtcpReceiver, decoder: r.decoderFactory(),
	}, nil
}

func (r *participantReceiveRegistry) ApplyGroupUpdate(update types.GroupCallUpdate) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L83-L100
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasGroupUpdate && update.TransactionID <= r.transactionID {
		return nil
	}

	nextByDeviceID := make(map[string]*participantAudioReceiver)
	nextByPID := make(map[uint32]*participantAudioReceiver)
	nextBySSRC := make(map[uint32]*participantAudioReceiver)
	nextByVideoSSRC := make(map[uint32]*participantAudioReceiver)
	nextByAppDataSSRC := make(map[uint32]*participantAudioReceiver)
	nextBySRTCPSSRC := make(map[uint32]*participantAudioReceiver)
	type receiverMetadata struct {
		receiver  *participantAudioReceiver
		userJID   types.JID
		deviceJID types.JID
		pid       uint32
	}
	var metadata []receiverMetadata
	hasConnectedRemotePID := false
	for _, participant := range update.Participants {
		if participant.State != "connected" {
			continue
		}
		for _, device := range participant.Devices {
			if !device.HasPID {
				continue
			}
			participantID := rtp.FormatE2ESrtpParticipantID(device.JID.String())
			if participantID == r.selfID {
				continue
			}
			hasConnectedRemotePID = true
			if _, exists := nextByDeviceID[participantID]; exists {
				return fmt.Errorf("meowcaller: duplicate group participant device %s", participantID)
			}
			receiver := r.byDeviceID[participantID]
			if receiver == nil {
				var err error
				receiver, err = r.newReceiver(participant.JID, device.JID, device.PID, true)
				if err != nil {
					return err
				}
			}
			if _, exists := nextByPID[device.PID]; exists {
				return fmt.Errorf("meowcaller: duplicate group participant PID %d", device.PID)
			}
			if _, exists := nextBySSRC[receiver.ssrc]; exists {
				return fmt.Errorf("meowcaller: duplicate group participant SSRC %d", receiver.ssrc)
			}
			if _, exists := nextByVideoSSRC[receiver.videoSSRC]; exists {
				return fmt.Errorf("meowcaller: duplicate group participant video SSRC %d", receiver.videoSSRC)
			}
			if _, exists := nextByAppDataSSRC[receiver.appDataSSRC]; exists {
				return fmt.Errorf("meowcaller: duplicate group participant app-data SSRC %d", receiver.appDataSSRC)
			}
			for _, streamSSRC := range receiver.streamSSRCs {
				if _, exists := nextBySRTCPSSRC[streamSSRC]; exists {
					return fmt.Errorf("meowcaller: duplicate group participant stream SSRC %d", streamSSRC)
				}
				nextBySRTCPSSRC[streamSSRC] = receiver
			}
			nextByDeviceID[participantID] = receiver
			nextByPID[device.PID] = receiver
			nextBySSRC[receiver.ssrc] = receiver
			nextByVideoSSRC[receiver.videoSSRC] = receiver
			nextByAppDataSSRC[receiver.appDataSSRC] = receiver
			metadata = append(metadata, receiverMetadata{
				receiver: receiver, userJID: participant.JID,
				deviceJID: device.JID, pid: device.PID,
			})
		}
	}
	if !hasConnectedRemotePID {
		fallback := r.byDeviceID[r.fallbackID]
		if fallback != nil {
			nextByDeviceID[r.fallbackID] = fallback
			nextBySSRC[fallback.ssrc] = fallback
			nextByVideoSSRC[fallback.videoSSRC] = fallback
			nextByAppDataSSRC[fallback.appDataSSRC] = fallback
			for _, streamSSRC := range fallback.streamSSRCs {
				nextBySRTCPSSRC[streamSSRC] = fallback
			}
		}
	}
	for _, next := range metadata {
		next.receiver.mu.Lock()
		next.receiver.userJID = next.userJID
		next.receiver.deviceJID = next.deviceJID
		next.receiver.pid = next.pid
		next.receiver.hasPID = true
		next.receiver.mu.Unlock()
	}
	r.byDeviceID = nextByDeviceID
	r.byPID = nextByPID
	r.bySSRC = nextBySSRC
	r.byVideoSSRC = nextByVideoSSRC
	r.byAppDataSSRC = nextByAppDataSSRC
	r.bySRTCPSSRC = nextBySRTCPSSRC
	r.transactionID = update.TransactionID
	r.hasGroupUpdate = true
	var newestPendingEpoch installedGroupRawEpoch
	var hasNewestPendingEpoch bool
	for transactionID, rawKey := range r.pendingEpochs {
		if transactionID > update.TransactionID {
			continue
		}
		if !hasNewestPendingEpoch || transactionID > newestPendingEpoch.transactionID {
			newestPendingEpoch = installedGroupRawEpoch{
				transactionID: transactionID,
				rawKey:        bytes.Clone(rawKey),
			}
			hasNewestPendingEpoch = true
		}
		delete(r.pendingEpochs, transactionID)
	}
	if hasNewestPendingEpoch {
		if err := r.applyGroupRawEpochLocked(newestPendingEpoch.transactionID, newestPendingEpoch.rawKey); err != nil {
			return err
		}
	} else if r.hasEpoch {
		if err := r.installGroupRawEpochLocked(r.installedEpoch.rawKey); err != nil {
			return err
		}
	}
	r.log.Info().
		Str("call_id", r.callID).
		Uint32("transaction_id", update.TransactionID).
		Int("remote_participants", len(nextByPID)).
		Msg("applied group media receive roster")
	return nil
}

func (r *participantReceiveRegistry) attachSendPipeline(sendPipe *MediaPipeline) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L78-L86
	if sendPipe == nil {
		return fmt.Errorf("meowcaller: nil media send pipeline")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasEpoch {
		keys, err := srtp.DeriveE2eKeysFromRaw(r.installedEpoch.rawKey, r.selfID, r.log)
		if err != nil {
			return fmt.Errorf("meowcaller: derive late group send epoch: %w", err)
		}
		sendPipe.installSendKeys(keys)
	}
	r.sendPipes = append(r.sendPipes, sendPipe)
	return nil
}

func (r *participantReceiveRegistry) attachSRTCPSender(sender *mediaSrtcpSender) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L78-L120
	if sender == nil {
		return fmt.Errorf("meowcaller: nil SRTCP sender")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasEpoch {
		keys, err := srtp.DeriveE2eSRTCPKeysFromRaw(r.installedEpoch.rawKey, r.selfID, r.log)
		if err != nil {
			return fmt.Errorf("meowcaller: derive late group SRTCP send epoch: %w", err)
		}
		sender.installKeys(keys)
	}
	r.srtcpSenders = append(r.srtcpSenders, sender)
	return nil
}

// ApplyGroupRawEpoch installs or buffers one shared keygen-v2 media epoch
// according to the authoritative group roster transaction.
func (r *participantReceiveRegistry) ApplyGroupRawEpoch(transactionID uint32, rawKey []byte) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L88-L120
	if len(rawKey) != 32 {
		return fmt.Errorf("meowcaller: group raw epoch has %d bytes, want 32", len(rawKey))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hasGroupUpdate || transactionID > r.transactionID {
		if existing, ok := r.pendingEpochs[transactionID]; ok {
			if bytes.Equal(existing, rawKey) {
				return nil
			}
			return fmt.Errorf("meowcaller: conflicting group raw epoch for transaction %d", transactionID)
		}
		r.pendingEpochs[transactionID] = bytes.Clone(rawKey)
		return nil
	}
	return r.applyGroupRawEpochLocked(transactionID, rawKey)
}

func (r *participantReceiveRegistry) applyGroupRawEpochLocked(transactionID uint32, rawKey []byte) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L101-L120
	if r.hasEpoch {
		if transactionID < r.installedEpoch.transactionID {
			return nil
		}
		if transactionID == r.installedEpoch.transactionID {
			if bytes.Equal(r.installedEpoch.rawKey, rawKey) {
				return nil
			}
			return fmt.Errorf("meowcaller: conflicting group raw epoch for transaction %d", transactionID)
		}
	}
	if err := r.installGroupRawEpochLocked(rawKey); err != nil {
		return err
	}
	r.installedEpoch = installedGroupRawEpoch{
		transactionID: transactionID,
		rawKey:        bytes.Clone(rawKey),
	}
	r.hasEpoch = true
	return nil
}

func (r *participantReceiveRegistry) installGroupRawEpochLocked(rawKey []byte) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	if len(r.sendPipes) == 0 {
		return fmt.Errorf("meowcaller: group media sender is not attached")
	}
	sendKeys, err := srtp.DeriveE2eKeysFromRaw(rawKey, r.selfID)
	if err != nil {
		return fmt.Errorf("meowcaller: derive group send epoch: %w", err)
	}
	sendRTCPKeys, err := srtp.DeriveE2eSRTCPKeysFromRaw(rawKey, r.selfID, r.log)
	if err != nil {
		return fmt.Errorf("meowcaller: derive group SRTCP send epoch: %w", err)
	}
	receiveKeys := make(map[*participantAudioReceiver]srtp.E2eSrtpKeys, len(r.byDeviceID))
	receiveRTCPKeys := make(map[*participantAudioReceiver]srtp.E2eSrtpKeys, len(r.byDeviceID))
	for _, receiver := range r.byDeviceID {
		keys, deriveErr := srtp.DeriveE2eKeysFromRaw(rawKey, receiver.participantID)
		if deriveErr != nil {
			return fmt.Errorf("meowcaller: derive group receive epoch: %w", deriveErr)
		}
		receiveKeys[receiver] = keys
		srtcpKeys, deriveErr := srtp.DeriveE2eSRTCPKeysFromRaw(rawKey, receiver.participantID, r.log)
		if deriveErr != nil {
			return fmt.Errorf("meowcaller: derive group SRTCP receive epoch: %w", deriveErr)
		}
		receiveRTCPKeys[receiver] = srtcpKeys
	}
	for _, sendPipe := range r.sendPipes {
		sendPipe.installSendKeys(sendKeys)
	}
	for _, sender := range r.srtcpSenders {
		sender.installKeys(sendRTCPKeys)
	}
	for receiver, keys := range receiveKeys {
		receiver.pipe.installRecvKeysPreservingROC(keys)
		receiver.videoPipe.installRecvKeysPreservingROC(keys)
		receiver.appDataPipe.installRecvKeysPreservingROC(keys)
		receiver.srtcp.installKeys(receiveRTCPKeys[receiver])
	}
	r.log.Info().
		Str("call_id", r.callID).
		Int("remote_participants", len(receiveKeys)).
		Msg("installed shared group media key epoch")
	return nil
}

func (r *participantReceiveRegistry) RekeyFallback(peerLID string) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L89-L100
	peerJID, err := types.ParseJID(peerLID)
	if err != nil {
		return fmt.Errorf("meowcaller: parse answering media peer: %w", err)
	}
	participantID := rtp.FormatE2ESrtpParticipantID(peerJID.String())
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasGroupUpdate {
		if _, ok := r.byDeviceID[participantID]; ok {
			r.fallbackID = participantID
			return nil
		}
		return fmt.Errorf("meowcaller: answering media peer is not active in group roster")
	}
	receiver, err := r.newReceiver(peerJID.ToNonAD(), peerJID, 0, false)
	if err != nil {
		return err
	}
	r.byDeviceID = map[string]*participantAudioReceiver{participantID: receiver}
	r.bySSRC = map[uint32]*participantAudioReceiver{receiver.ssrc: receiver}
	r.byVideoSSRC = map[uint32]*participantAudioReceiver{receiver.videoSSRC: receiver}
	r.byAppDataSSRC = map[uint32]*participantAudioReceiver{receiver.appDataSSRC: receiver}
	r.bySRTCPSSRC = make(map[uint32]*participantAudioReceiver, len(receiver.streamSSRCs))
	for _, streamSSRC := range receiver.streamSSRCs {
		r.bySRTCPSSRC[streamSSRC] = receiver
	}
	r.byPID = make(map[uint32]*participantAudioReceiver)
	r.fallbackID = participantID
	return nil
}

func (r *participantReceiveRegistry) UnprotectVideo(packet []byte) (unprotectedParticipantMedia, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	header, ok := rtp.ParseRtpHeader(packet)
	if !ok {
		return unprotectedParticipantMedia{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	receiver := r.byVideoSSRC[header.Ssrc]
	if receiver == nil {
		r.log.Debug().Uint32("ssrc", header.Ssrc).Msg("dropping video from inactive participant SSRC")
		return unprotectedParticipantMedia{}, false
	}
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	authenticatedHeader, payload, ok := receiver.videoPipe.UnprotectAudio(packet)
	if !ok {
		return unprotectedParticipantMedia{}, false
	}
	return receiver.unprotectedMedia(authenticatedHeader, payload), true
}

func (r *participantReceiveRegistry) UnprotectAppData(packet []byte) (unprotectedParticipantMedia, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	header, ok := rtp.ParseRtpHeader(packet)
	if !ok {
		return unprotectedParticipantMedia{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	receiver := r.byAppDataSSRC[header.Ssrc]
	if receiver == nil {
		r.log.Debug().Uint32("ssrc", header.Ssrc).Msg("dropping app-data from inactive participant SSRC")
		return unprotectedParticipantMedia{}, false
	}
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	authenticatedHeader, payload, ok := receiver.appDataPipe.UnprotectAudio(packet)
	if !ok {
		return unprotectedParticipantMedia{}, false
	}
	return receiver.unprotectedMedia(authenticatedHeader, payload), true
}

func (r *participantAudioReceiver) unprotectedMedia(header rtp.RtpHeader, payload []byte) unprotectedParticipantMedia {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	return unprotectedParticipantMedia{
		ParticipantID: r.participantID,
		UserJID:       r.userJID,
		DeviceJID:     r.deviceJID,
		PID:           r.pid,
		HasPID:        r.hasPID,
		Header:        header,
		Payload:       payload,
		receiver:      r,
	}
}

func (r *participantReceiveRegistry) UnprotectSRTCP(senderSSRC uint32, packet []byte) ([]byte, uint32, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L104-L136
	r.mu.RLock()
	defer r.mu.RUnlock()
	receiver := r.bySRTCPSSRC[senderSSRC]
	if receiver == nil {
		r.log.Debug().Uint32("ssrc", senderSSRC).Msg("dropping SRTCP from inactive participant SSRC")
		return nil, 0, false
	}
	return receiver.srtcp.unprotect(senderSSRC, packet)
}

func (r *participantReceiveRegistry) ActiveParticipantIDs() []string {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L89-L100
	r.mu.RLock()
	defer r.mu.RUnlock()
	participantIDs := make([]string, 0, len(r.byDeviceID))
	for participantID := range r.byDeviceID {
		participantIDs = append(participantIDs, participantID)
	}
	slices.Sort(participantIDs)
	return participantIDs
}

func (r *participantReceiveRegistry) DecodeAudio(packet []byte) (decodedParticipantAudio, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L37-L44
	header, ok := rtp.ParseRtpHeader(packet)
	if !ok {
		return decodedParticipantAudio{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	receiver := r.bySSRC[header.Ssrc]
	if receiver == nil {
		r.log.Debug().Uint32("ssrc", header.Ssrc).Msg("dropping audio from inactive participant SSRC")
		return decodedParticipantAudio{}, false
	}

	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	authenticatedHeader, payload, ok := receiver.pipe.UnprotectAudio(packet)
	if !ok {
		r.log.Debug().
			Uint32("ssrc", header.Ssrc).
			Str("participant_id", receiver.participantID).
			Msg("participant audio failed authentication")
		return decodedParticipantAudio{}, false
	}
	pcm := receiver.decoder.Decode(payload)
	return decodedParticipantAudio{
		ParticipantID: receiver.participantID,
		UserJID:       receiver.userJID, DeviceJID: receiver.deviceJID,
		PID: receiver.pid, HasPID: receiver.hasPID, SSRC: authenticatedHeader.Ssrc,
		Timestamp: authenticatedHeader.Timestamp, PCM: pcm,
	}, true
}
