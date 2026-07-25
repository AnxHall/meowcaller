package meowcaller

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/diag"
	"github.com/purpshell/meowcaller/rtp"
	"go.mau.fi/whatsmeow/types"
)

func TestVideoRtpDurationSamples(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     uint32
	}{
		{name: "zero uses fallback", duration: 0, want: defaultVideoRtpStepSamples},
		{name: "negative uses fallback", duration: -time.Millisecond, want: defaultVideoRtpStepSamples},
		{name: "30 fps", duration: time.Second / 30, want: 3000},
		{name: "60 fps", duration: time.Second / 60, want: 1500},
		{name: "sub sample uses fallback", duration: time.Nanosecond, want: defaultVideoRtpStepSamples},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := videoRtpDurationSamples(tt.duration); got != tt.want {
				t.Fatalf("videoRtpDurationSamples(%s) = %d, want %d", tt.duration, got, tt.want)
			}
		})
	}
}

func TestVideoSenderStartsAtIDRAndUsesWhatsappHeaders(t *testing.T) {
	callKey := iota32()
	pipe, err := NewMediaPipeline(callKey, "111111111111111:0@lid", "222222222222222:0@lid", 0x55667788, FrameSamples)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	sender := &videoSender{
		pipe:             pipe,
		stream:           rtp.NewVideoRtpStream(0x55667788, 4500),
		active:           true,
		keyframeRequired: true,
	}

	delta := []byte{0, 0, 0, 1, 0x41, 1, 2, 3}
	if packets := sender.protectAccessUnit(delta, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("dependent frame produced %d packets before an IDR", len(packets))
	}

	idr := []byte{
		0, 0, 0, 1, 0x67, 0x42, 0, 0x1f,
		0, 0, 0, 1, 0x68, 0xce, 6, 0xe2,
		0, 0, 0, 1, 0x65, 1, 2, 3,
	}
	packets := sender.protectAccessUnit(idr, 50*time.Millisecond)
	if len(packets) != 1 {
		t.Fatalf("IDR produced %d packets, want one packed access-unit packet", len(packets))
	}
	receiver, err := NewMediaPipeline(callKey, "222222222222222:0@lid", "111111111111111:0@lid", 0x55667788, FrameSamples)
	if err != nil {
		t.Fatalf("receiver pipe: %v", err)
	}
	var depack rtp.H264Depacketizer
	var reconstructed []byte
	for i, packet := range packets {
		header, ok := rtp.ParseRtpHeader(packet)
		if !ok {
			t.Fatalf("packet %d has no RTP header", i)
		}
		wantHeaderSize := rtp.WhatsappVideoRtpHeaderSize
		if i == 0 {
			wantHeaderSize += 4
		}
		if n, ok := rtp.RtpHeaderByteLength(packet); !ok || n != wantHeaderSize {
			t.Fatalf("packet %d header length = (%d, %v), want (%d, true)", i, n, ok, wantHeaderSize)
		}
		if header.VideoExtension == nil || header.VideoExtension.MediaFrameInfo != rtp.VideoMediaFrameInfoIDR {
			t.Fatalf("packet %d video extension = %+v", i, header.VideoExtension)
		}
		_, payload, ok := receiver.UnprotectAudio(packet)
		if !ok {
			t.Fatalf("packet %d did not unprotect", i)
		}
		for _, nalu := range depack.Depacketize(payload) {
			reconstructed = append(reconstructed, 0, 0, 0, 1)
			reconstructed = append(reconstructed, nalu...)
		}
	}
	if !bytes.Equal(reconstructed, idr) {
		t.Fatalf("reconstructed access unit = %x, want %x", reconstructed, idr)
	}
}

func TestVideoSenderRecordsWireDiagnostics(t *testing.T) {
	dir := t.TempDir()
	rec, err := diag.NewRecorder(dir)
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	pipe, err := NewMediaPipeline(iota32(), "111111111111111:0@lid", "222222222222222:0@lid", 0x55667788, FrameSamples)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	sender := &videoSender{
		pipe: pipe, stream: rtp.NewVideoRtpStream(0x55667788, 4500),
		callID: "test-call", active: true, keyframeRequired: true, diag: rec,
	}
	idr := []byte{0, 0, 0, 1, 0x65, 1, 2, 3}
	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) != 1 {
		t.Fatalf("IDR produced %d packets, want 1", len(packets))
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("close recorder: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "video_wire.jsonl"))
	if err != nil {
		t.Fatalf("read video wire diagnostics: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"event":"access_unit"`, `"event":"packet"`, `"direction":"out"`, `"call_id":"test-call"`, `"header_hex":`, `"payload_hex":`, `"protected_hex":`} {
		if !strings.Contains(text, want) {
			t.Errorf("video wire diagnostics missing %s: %s", want, text)
		}
	}
}

func TestVideoSenderGatesUpgradeUntilPeerAcceptance(t *testing.T) {
	pipe, err := NewMediaPipeline(iota32(), "111111111111111:0@lid", "222222222222222:0@lid", 0x55667788, FrameSamples)
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	sender := &videoSender{pipe: pipe, stream: rtp.NewVideoRtpStream(0x55667788, 4500)}
	idr := []byte{0, 0, 0, 1, 0x65, 1, 2, 3}
	delta := []byte{0, 0, 0, 1, 0x41, 1, 2, 3}

	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("inactive sender produced %d packets", len(packets))
	}
	sender.enable(true)
	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("send-gated sender produced %d packets", len(packets))
	}
	sender.enable(false)
	if packets := sender.protectAccessUnit(delta, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("ungated sender sent dependent frame before recovery IDR: %d packets", len(packets))
	}
	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) == 0 {
		t.Fatal("ungated sender did not send recovery IDR")
	}
	sender.disable()
	if packets := sender.protectAccessUnit(idr, 50*time.Millisecond); len(packets) != 0 {
		t.Fatalf("disabled sender produced %d packets", len(packets))
	}
}

func TestMediaSrtcpSenderProtectsVideoReport(t *testing.T) {
	sender, err := newMediaSrtcpSender(iota32(), "111111111111111:0@lid", 0x55667788, true)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	packet, err := sender.senderReport(rtp.RtcpSenderStats{
		PacketsSent:  3,
		OctetsSent:   400,
		RtpTimestamp: 90000,
	}, 1700000000000, nil)
	if err != nil {
		t.Fatalf("sender report: %v", err)
	}
	if kind := rtp.IsRtcpPacket(packet); !kind {
		t.Fatal("protected sender report is not classified as RTCP")
	}
	if len(packet) != 60+14 {
		t.Fatalf("protected report length = %d, want 74", len(packet))
	}
}

func TestMediaSrtcpSenderEmitsCaptureShapedPacketPerGroupAudioReport(t *testing.T) {
	callKey := iota32()
	const (
		selfLID   = "111111111111111:14@lid"
		localSSRC = uint32(0x10203040)
		ssrcA     = uint32(0x59754A60)
		ssrcC     = uint32(0x66FDE7F1)
	)
	sender, err := newMediaSrtcpSender(callKey, selfLID, localSSRC, false)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	receiver, err := newMediaSrtcpReceiver(callKey, selfLID)
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	reports := []*rtp.RtcpReceptionReport{
		{Ssrc: ssrcA, ExtendedHighestSequence: 300},
		{Ssrc: ssrcC, ExtendedHighestSequence: 40},
	}
	var packets [][]byte
	_, err = sendMediaSrtcpReceptionReports(
		sender,
		rtp.RtcpSenderStats{PacketsSent: 7, OctetsSent: 700, RtpTimestamp: 9600},
		1_700_000_000_000,
		reports,
		true,
		func(packet []byte) error {
			packets = append(packets, bytes.Clone(packet))
			return nil
		},
	)
	if err != nil {
		t.Fatalf("send group reports: %v", err)
	}
	if len(packets) != 2 {
		t.Fatalf("packet count = %d, want 2", len(packets))
	}
	wantPlaintexts := []string{
		"81c8000e10203040e8fe6f80000000000000258000000007000002bc59754a60000000000000012c0000000000000000000000000000000000000000",
		"81c8000e10203040e8fe6f80000000000000258000000007000002bc66fde7f100000000000000280000000000000000000000000000000000000000",
	}
	for i, packet := range packets {
		if len(packet) != 60+rtp.SrtcpTrailerLen {
			t.Fatalf("packet %d protected bytes = %d, want 74", i, len(packet))
		}
		plain, index, ok := receiver.unprotect(localSSRC, packet)
		if !ok {
			t.Fatalf("packet %d failed SRTCP authentication", i)
		}
		if index != uint32(i+1) {
			t.Fatalf("packet %d SRTCP index = %d, want %d", i, index, i+1)
		}
		if got := hex.EncodeToString(plain); got != wantPlaintexts[i] {
			t.Fatalf("packet %d plaintext = %s, want capture-shaped %s", i, got, wantPlaintexts[i])
		}
	}
}

func TestMediaSrtcpSenderTransitionsFromDirectToCommittedGroupWire(t *testing.T) {
	callKey := iota32()
	const (
		selfLID   = "111111111111111:14@lid"
		localSSRC = uint32(0x10203040)
		peerSSRC  = uint32(0x59754A60)
	)
	self := mediaTestJID("111111111111111", 14)
	peer := mediaTestJID("222222222222222", 0)
	added := mediaTestJID("333333333333333", 43)
	pending := mediaTestJID("444444444444444", 63)
	registry, err := newParticipantReceiveRegistry("CID", callKey, selfLID, peer.String(), func() participantAudioDecoder {
		return &recordingParticipantDecoder{}
	})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	sender, err := newMediaSrtcpSender(callKey, selfLID, localSSRC, false)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	receiver, err := newMediaSrtcpReceiver(callKey, selfLID)
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	report := &rtp.RtcpReceptionReport{Ssrc: peerSSRC, ExtendedHighestSequence: 300}
	var packets [][]byte
	send := func(packet []byte) error {
		packets = append(packets, bytes.Clone(packet))
		return nil
	}

	commitFailure := errors.New("relay allocation failed")
	update := mediaTestGroupUpdate(self, peer, added, pending, 16, true)
	err = registry.ApplyGroupUpdateTransaction(update, func(func()) error {
		return commitFailure
	})
	if !errors.Is(err, commitFailure) || registry.HasCommittedGroupUpdate() {
		t.Fatalf("failed group transaction = (%v, committed %t), want failure without commit",
			err, registry.HasCommittedGroupUpdate())
	}
	if _, err = sendMediaSrtcpReceptionReports(
		sender,
		rtp.RtcpSenderStats{PacketsSent: 7, OctetsSent: 700, RtpTimestamp: 9600},
		1_700_000_000_000,
		[]*rtp.RtcpReceptionReport{report},
		registry.HasCommittedGroupUpdate(),
		send,
	); err != nil {
		t.Fatalf("send direct report: %v", err)
	}
	err = registry.ApplyGroupUpdateTransaction(update, func(commit func()) error {
		commit()
		return nil
	})
	if err != nil || !registry.HasCommittedGroupUpdate() {
		t.Fatalf("retried group transaction = (%v, committed %t), want committed",
			err, registry.HasCommittedGroupUpdate())
	}
	if _, err = sendMediaSrtcpReceptionReports(
		sender,
		rtp.RtcpSenderStats{PacketsSent: 8, OctetsSent: 800, RtpTimestamp: 10_560},
		1_700_000_001_500,
		[]*rtp.RtcpReceptionReport{report},
		registry.HasCommittedGroupUpdate(),
		send,
	); err != nil {
		t.Fatalf("send committed group report: %v", err)
	}
	if len(packets) != 2 {
		t.Fatalf("packet count = %d, want direct then group", len(packets))
	}
	wantWireLengths := []int{108 + rtp.SrtcpTrailerLen, 60 + rtp.SrtcpTrailerLen}
	wantPlainLengths := []int{108, 60}
	wantRTCPWords := []uint16{18, 14}
	for i, packet := range packets {
		if len(packet) != wantWireLengths[i] {
			t.Fatalf("packet %d protected bytes = %d, want %d", i, len(packet), wantWireLengths[i])
		}
		plain, index, ok := receiver.unprotect(localSSRC, packet)
		if !ok {
			t.Fatalf("packet %d failed SRTCP authentication", i)
		}
		if index != uint32(i+1) {
			t.Fatalf("packet %d index = %d, want %d", i, index, i+1)
		}
		if len(plain) != wantPlainLengths[i] || binary.BigEndian.Uint16(plain[2:4]) != wantRTCPWords[i] {
			t.Fatalf("packet %d plaintext shape = (%d bytes, %d words), want (%d, %d)",
				i, len(plain), binary.BigEndian.Uint16(plain[2:4]), wantPlainLengths[i], wantRTCPWords[i])
		}
		if got := binary.BigEndian.Uint32(plain[28:32]); got != peerSSRC {
			t.Fatalf("packet %d report SSRC = %#x, want %#x", i, got, peerSSRC)
		}
		if i == 0 && !bytes.Equal(plain[76:80], []byte{0x81, rtp.RtcpPtSdes, 0, 7}) {
			t.Fatalf("direct packet SDES header = %x, want 81ca0007", plain[76:80])
		}
	}
}

func TestMediaSrtcpSenderStillEmitsBeforeInboundGroupAudio(t *testing.T) {
	sender, err := newMediaSrtcpSender(iota32(), "111111111111111:14@lid", 0x10203040, false)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	sent := 0
	_, err = sendMediaSrtcpReceptionReports(
		sender,
		rtp.RtcpSenderStats{},
		1_700_000_000_000,
		nil,
		true,
		func([]byte) error {
			sent++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("send empty reception reports: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent packets = %d, want 1 baseline sender report", sent)
	}
}

func TestMediaSrtcpReportFailureBeforeFirstSendRetriesWithFreshIndex(t *testing.T) {
	callKey := iota32()
	const (
		selfLID   = "111111111111111:14@lid"
		localSSRC = uint32(0x10203040)
	)
	sender, err := newMediaSrtcpSender(callKey, selfLID, localSSRC, false)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	receiver, err := newMediaSrtcpReceiver(callKey, selfLID)
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	reports := []*rtp.RtcpReceptionReport{
		{Ssrc: 0x59754A60},
		{Ssrc: 0x66FDE7F1},
	}
	sendFailure := errors.New("send failed")
	sent, err := sendMediaSrtcpReceptionReports(
		sender,
		rtp.RtcpSenderStats{},
		1_700_000_000_000,
		reports,
		true,
		func([]byte) error {
			return sendFailure
		},
	)
	if !errors.Is(err, sendFailure) || sent != 0 {
		t.Fatalf("first attempt = (%d, %v), want (0, send failed)", sent, err)
	}

	var retried [][]byte
	sent, err = sendMediaSrtcpReceptionReports(
		sender,
		rtp.RtcpSenderStats{},
		1_700_000_001_500,
		reports,
		true,
		func(packet []byte) error {
			retried = append(retried, bytes.Clone(packet))
			return nil
		},
	)
	if err != nil || sent != 2 {
		t.Fatalf("retry = (%d, %v), want (2, nil)", sent, err)
	}
	for i, packet := range retried {
		if _, index, ok := receiver.unprotect(localSSRC, packet); !ok || index != uint32(i+2) {
			t.Fatalf("retry packet %d = index %d authenticated %t, want fresh index %d", i, index, ok, i+2)
		}
	}
}

func TestMediaSrtcpPartialSendReportsProgressAndRetriesAllWithFreshIndexes(t *testing.T) {
	callKey := iota32()
	const (
		selfLID   = "111111111111111:14@lid"
		localSSRC = uint32(0x10203040)
	)
	sender, err := newMediaSrtcpSender(callKey, selfLID, localSSRC, false)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	receiver, err := newMediaSrtcpReceiver(callKey, selfLID)
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	reports := []*rtp.RtcpReceptionReport{
		{Ssrc: 0x59754A60},
		{Ssrc: 0x66FDE7F1},
	}
	sendFailure := errors.New("send failed")
	attempted := 0
	sent, err := sendMediaSrtcpReceptionReports(
		sender,
		rtp.RtcpSenderStats{},
		1_700_000_000_000,
		reports,
		true,
		func([]byte) error {
			attempted++
			if attempted == 2 {
				return sendFailure
			}
			return nil
		},
	)
	if !errors.Is(err, sendFailure) || sent != 1 {
		t.Fatalf("partial attempt = (%d, %v), want (1, send failed)", sent, err)
	}

	var retried [][]byte
	sent, err = sendMediaSrtcpReceptionReports(
		sender,
		rtp.RtcpSenderStats{},
		1_700_000_001_500,
		reports,
		true,
		func(packet []byte) error {
			retried = append(retried, bytes.Clone(packet))
			return nil
		},
	)
	if err != nil || sent != 2 {
		t.Fatalf("retry = (%d, %v), want (2, nil)", sent, err)
	}
	for i, packet := range retried {
		if _, index, ok := receiver.unprotect(localSSRC, packet); !ok || index != uint32(i+3) {
			t.Fatalf("retry packet %d = index %d authenticated %t, want fresh index %d", i, index, ok, i+3)
		}
	}
}

func TestMediaSRTCPTicksContinueAfterSendFailure(t *testing.T) {
	ticks := make(chan time.Time, 2)
	ticks <- time.UnixMilli(1_700_000_000_000)
	ticks <- time.UnixMilli(1_700_000_001_500)
	close(ticks)
	sendFailure := errors.New("send failed")
	attempts := 0
	failures := 0

	runMediaSRTCPTicks(
		t.Context(),
		ticks,
		func(time.Time) error {
			attempts++
			if attempts == 1 {
				return sendFailure
			}
			return nil
		},
		func(err error) {
			if !errors.Is(err, sendFailure) {
				t.Fatalf("failure callback error = %v, want send failed", err)
			}
			failures++
		},
	)
	if attempts != 2 || failures != 1 {
		t.Fatalf("ticker attempts/failures = (%d, %d), want (2, 1)", attempts, failures)
	}
}

func TestApplyGroupAudioRosterPrunesDepartedReceptionAndResetsRejoin(t *testing.T) {
	callKey := iota32()
	self := mediaTestJID("111111111111111", 14)
	peer := mediaTestJID("222222222222222", 0)
	added := mediaTestJID("333333333333333", 43)
	pending := mediaTestJID("444444444444444", 63)
	registry, err := newParticipantReceiveRegistry("CID", callKey, self.String(), peer.String(), func() participantAudioDecoder {
		return &recordingParticipantDecoder{}
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	var reception rtp.RtcpReceptionStatsSet
	if err = applyGroupAudioRoster(registry, &reception, mediaTestGroupUpdate(self, peer, added, pending, 17, true)); err != nil {
		t.Fatalf("apply connected roster: %v", err)
	}
	active := registry.ActiveAudioSSRCs()
	if len(active) != 2 {
		t.Fatalf("connected audio SSRC count = %d, want 2", len(active))
	}
	for i, ssrc := range active {
		reception.Observe(ssrc, uint16(100+i), uint32(96_000+i*960), uint64(1_000+i*60), SampleRate)
	}

	if err = applyGroupAudioRoster(registry, &reception, mediaTestGroupUpdate(self, peer, added, pending, 18, false)); err != nil {
		t.Fatalf("apply departure roster: %v", err)
	}
	reports := reception.Reports(1_500)
	if len(reports) != 1 || reports[0].Ssrc != registry.ActiveAudioSSRCs()[0] {
		t.Fatalf("post-departure reports = %+v, want only authoritative active SSRC", reports)
	}

	addedSSRC, err := rtp.DeriveWasmParticipantSsrc("CID", rtp.FormatE2ESrtpParticipantID(added.String()), 0)
	if err != nil {
		t.Fatalf("derive rejoined SSRC: %v", err)
	}
	if err = applyGroupAudioRoster(registry, &reception, mediaTestGroupUpdate(self, peer, added, pending, 19, true)); err != nil {
		t.Fatalf("apply rejoin roster: %v", err)
	}
	reception.Observe(addedSSRC, 900, 8_640_000, 2_000, SampleRate)
	reports = reception.Reports(2_100)
	if len(reports) != 2 {
		t.Fatalf("post-rejoin reports = %+v, want two active SSRCs", reports)
	}
	var rejoined *rtp.RtcpReceptionReport
	for _, report := range reports {
		if report.Ssrc == addedSSRC {
			rejoined = report
		}
	}
	if rejoined == nil || rejoined.ExtendedHighestSequence != 900 || rejoined.CumulativeLost != 0 {
		t.Fatalf("rejoined report = %+v, want fresh sequence 900", rejoined)
	}
}

func TestApplyGroupMediaUpdateTransactionKeepsRelayRosterAndReceptionAtomic(t *testing.T) {
	callKey := iota32()
	self := mediaTestJID("111111111111111", 14)
	peer := mediaTestJID("222222222222222", 0)
	added := mediaTestJID("333333333333333", 43)
	pending := mediaTestJID("444444444444444", 63)
	registry, err := newParticipantReceiveRegistry("CID", callKey, self.String(), peer.String(), func() participantAudioDecoder {
		return &recordingParticipantDecoder{}
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	var reception rtp.RtcpReceptionStatsSet
	const staleReceptionSSRC = 0xdeadbeef
	reception.Observe(staleReceptionSSRC, 7, 960, 20, SampleRate)

	initialAllocate := []byte{0x00, 0x01, 0x02}
	allocateState := newGroupRelayAllocateState(initialAllocate, bytes.Repeat([]byte{0x12}, 16))
	endpoint := &types.RelayEndpoint{
		RelayName: "zrh1c01",
		IPv4:      "157.240.17.62",
		Port:      3478,
	}
	update := mediaTestGroupUpdate(self, peer, added, pending, 18, true)
	update.Relay = &types.GroupCallRelay{
		TransactionID: 4,
		Key:           bytes.Repeat([]byte{0x24}, 16),
		Tokens:        [][]byte{bytes.Repeat([]byte{0x42}, 174)},
		Endpoints: []types.GroupCallRelayEndpoint{{
			RelayName: "zrh1c01",
			TokenID:   0,
		}},
	}
	relayTransactionID := [12]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	sendErr := errors.New("relay unavailable")
	sendCalls := 0
	changed, err := applyGroupMediaUpdateTransaction(
		registry,
		&reception,
		allocateState,
		endpoint,
		[9]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9},
		update,
		relayTransactionID,
		func([]byte) error {
			sendCalls++
			return sendErr
		},
	)
	if !errors.Is(err, sendErr) {
		t.Fatalf("failed apply error = %v, want %v", err, sendErr)
	}
	if changed || sendCalls != 1 {
		t.Fatalf("failed apply changed/send calls = (%t, %d), want (false, 1)", changed, sendCalls)
	}
	if registry.HasCommittedGroupUpdate() || registry.transactionID != 0 {
		t.Fatalf("failed apply committed roster: group=%t tx=%d", registry.HasCommittedGroupUpdate(), registry.transactionID)
	}
	if got := allocateState.Current(); !bytes.Equal(got, initialAllocate) {
		t.Fatalf("failed apply changed relay allocate: %x", got)
	}
	if reports := reception.Reports(40); len(reports) != 1 || reports[0].Ssrc != staleReceptionSSRC {
		t.Fatalf("failed apply changed reception set: %+v", reports)
	}

	changed, err = applyGroupMediaUpdateTransaction(
		registry,
		&reception,
		allocateState,
		endpoint,
		[9]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9},
		update,
		relayTransactionID,
		func([]byte) error {
			sendCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("retry apply: %v", err)
	}
	if !changed || sendCalls != 2 {
		t.Fatalf("retry changed/send calls = (%t, %d), want (true, 2)", changed, sendCalls)
	}
	if !registry.HasCommittedGroupUpdate() || registry.transactionID != 18 || registry.byPID[2] == nil {
		t.Fatalf("retry did not commit roster: group=%t tx=%d pids=%v", registry.HasCommittedGroupUpdate(), registry.transactionID, registry.byPID)
	}
	if got := allocateState.Current(); bytes.Equal(got, initialAllocate) {
		t.Fatal("retry did not commit relay allocate")
	}
	if reports := reception.Reports(40); len(reports) != 0 {
		t.Fatalf("retry did not retain only committed reception SSRCs: %+v", reports)
	}

	stale := update
	stale.Relay = &types.GroupCallRelay{TransactionID: 5}
	changed, err = applyGroupMediaUpdateTransaction(
		registry,
		&reception,
		allocateState,
		nil,
		[9]uint32{},
		stale,
		[12]byte{},
		func([]byte) error {
			sendCalls++
			return nil
		},
	)
	if err != nil || changed || sendCalls != 2 {
		t.Fatalf("stale apply = changed:%t calls:%d err:%v, want false,2,nil", changed, sendCalls, err)
	}

	withoutRelay := mediaTestGroupUpdate(self, peer, added, pending, 19, false)
	changed, err = applyGroupMediaUpdateTransaction(
		registry,
		&reception,
		nil,
		nil,
		[9]uint32{},
		withoutRelay,
		[12]byte{},
		nil,
	)
	if err != nil || changed || registry.transactionID != 19 {
		t.Fatalf("non-relay apply = changed:%t tx:%d err:%v, want false,19,nil", changed, registry.transactionID, err)
	}
}

func TestMediaSrtcpReceiverRekeysForAnsweringDevice(t *testing.T) {
	callKey := iota32()
	const ssrc = 0x55667788
	receiver, err := newMediaSrtcpReceiver(callKey, "222222222222222:0@lid")
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	sender, err := newMediaSrtcpSender(callKey, "222222222222222:7@lid", ssrc, true)
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	packet, err := sender.senderReport(rtp.RtcpSenderStats{PacketsSent: 1}, 1700000000000, nil)
	if err != nil {
		t.Fatalf("sender report: %v", err)
	}

	if err = receiver.rekey(callKey, "222222222222222:7@lid"); err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if _, _, ok := receiver.unprotect(ssrc, packet); !ok {
		t.Fatal("rekeyed SRTCP receiver rejected answering-device report")
	}
}
