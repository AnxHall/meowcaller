package meowcaller

import (
	"bytes"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/rtp"
	"github.com/purpshell/meowcaller/srtp"
	"go.mau.fi/whatsmeow/types"
)

type recordingParticipantDecoder struct {
	payloads [][]byte
}

type blockingParticipantDecoder struct {
	entered chan struct{}
	release chan struct{}
}

func (d *recordingParticipantDecoder) Decode(payload []byte) []float32 {
	d.payloads = append(d.payloads, append([]byte(nil), payload...))
	if len(payload) == 0 {
		return nil
	}
	return []float32{float32(payload[0])}
}

func (d *blockingParticipantDecoder) Decode(payload []byte) []float32 {
	close(d.entered)
	<-d.release
	return []float32{float32(payload[0])}
}

func mediaTestJID(user string, device uint16) types.JID {
	return types.JID{User: user, Device: device, Server: types.HiddenUserServer}
}

func mediaTestGroupUpdate(self, peer, added, pending types.JID, transactionID uint32, includeAdded bool) types.GroupCallUpdate {
	participants := []types.GroupCallParticipant{
		{
			JID:   self.ToNonAD(),
			State: "connected",
			Devices: []types.GroupCallDevice{{
				JID: self, PID: 1, HasPID: true,
			}},
		},
		{
			JID:   peer.ToNonAD(),
			State: "connected",
			Devices: []types.GroupCallDevice{{
				JID: peer, PID: 0, HasPID: true,
			}},
		},
		{
			JID:   pending.ToNonAD(),
			State: "receipt",
			Devices: []types.GroupCallDevice{
				{JID: pending, PID: 3, HasPID: true},
			},
		},
	}
	if includeAdded {
		participants = append(participants, types.GroupCallParticipant{
			JID:   added.ToNonAD(),
			State: "connected",
			Devices: []types.GroupCallDevice{
				{JID: added, PID: 2, HasPID: true},
				{JID: mediaTestJID(added.User, added.Device+1)},
			},
		})
	} else {
		participants = append(participants, types.GroupCallParticipant{
			JID:     added.ToNonAD(),
			State:   "invited",
			Devices: []types.GroupCallDevice{{JID: added}},
		})
	}
	return types.GroupCallUpdate{CallID: "CID", TransactionID: transactionID, Participants: participants}
}

func protectParticipantAudio(t *testing.T, callKey []byte, self, sender types.JID, payload []byte) ([]byte, uint32) {
	t.Helper()
	participantID := rtp.FormatE2ESrtpParticipantID(sender.String())
	ssrc, err := rtp.DeriveWasmParticipantSsrc("CID", participantID, 0)
	if err != nil {
		t.Fatalf("derive sender SSRC: %v", err)
	}
	tx, err := NewMediaPipeline(callKey, sender.String(), self.String(), ssrc, FrameSamples)
	if err != nil {
		t.Fatalf("sender pipeline: %v", err)
	}
	packet, err := tx.ProtectAudio(payload)
	if err != nil {
		t.Fatalf("protect sender audio: %v", err)
	}
	return packet, ssrc
}

func protectRawParticipantAudio(t *testing.T, rawKey []byte, sender types.JID, payload []byte) ([]byte, uint32) {
	t.Helper()
	participantID := rtp.FormatE2ESrtpParticipantID(sender.String())
	ssrc, err := rtp.DeriveWasmParticipantSsrc("CID", participantID, 0)
	if err != nil {
		t.Fatalf("derive raw-key sender SSRC: %v", err)
	}
	keys, err := srtp.DeriveE2eKeysFromRaw(rawKey, participantID)
	if err != nil {
		t.Fatalf("derive raw participant keys: %v", err)
	}
	header := rtp.RtpHeader{
		PayloadType:    rtp.RtpPayloadTypeOpus,
		SequenceNumber: 1,
		Timestamp:      FrameSamples,
		Ssrc:           ssrc,
	}
	packet := rtp.EncodeRtpHeader(&header)
	encrypted, err := srtp.CryptPayload(&keys, ssrc, header.SequenceNumber, 0, payload)
	if err != nil {
		t.Fatalf("encrypt raw participant packet: %v", err)
	}
	packet = append(packet, encrypted...)
	return srtp.AppendWarpMITag(keys.AuthKey[:], packet, 0, srtp.WarpMITagLen), ssrc
}

func protectParticipantAudioAt(
	t *testing.T,
	callKey []byte,
	sender types.JID,
	sequence uint16,
	roc uint32,
	payload []byte,
) []byte {
	t.Helper()
	participantID := rtp.FormatE2ESrtpParticipantID(sender.String())
	ssrc, err := rtp.DeriveWasmParticipantSsrc("CID", participantID, 0)
	if err != nil {
		t.Fatalf("derive sender SSRC: %v", err)
	}
	keys, err := srtp.DeriveE2eKeys(callKey, participantID)
	if err != nil {
		t.Fatalf("derive participant keys: %v", err)
	}
	header := rtp.RtpHeader{
		PayloadType:    rtp.RtpPayloadTypeOpus,
		SequenceNumber: sequence,
		Timestamp:      uint32(sequence) * FrameSamples,
		Ssrc:           ssrc,
	}
	packet := rtp.EncodeRtpHeader(&header)
	encrypted, err := srtp.CryptPayload(&keys, ssrc, sequence, roc, payload)
	if err != nil {
		t.Fatalf("encrypt participant packet: %v", err)
	}
	packet = append(packet, encrypted...)
	return srtp.AppendWarpMITag(keys.AuthKey[:], packet, roc, srtp.WarpMITagLen)
}

func TestParticipantReceiveRegistryBuffersAndAppliesParticipantRawRekey(t *testing.T) {
	callKey := iota32()
	rawKey := bytes.Repeat([]byte{0xa5}, 32)
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
	if err = registry.ApplyParticipantRawRekey(17, added, rawKey); err != nil {
		t.Fatalf("buffer participant rekey: %v", err)
	}
	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 17, true)); err != nil {
		t.Fatalf("apply matching roster: %v", err)
	}

	rawPacket, _ := protectRawParticipantAudio(t, rawKey, added, []byte{0x61})
	audio, ok := registry.DecodeAudio(rawPacket)
	if !ok {
		t.Fatal("matching roster did not activate the buffered participant rekey")
	}
	if audio.DeviceJID != added || len(audio.PCM) != 1 || audio.PCM[0] != 0x61 {
		t.Fatalf("raw-key participant audio = %+v", audio)
	}
	peerPacket, _ := protectParticipantAudio(t, callKey, self, peer, []byte{0x62})
	if _, ok = registry.DecodeAudio(peerPacket); !ok {
		t.Fatal("participant rekey modified the original peer's receive keys")
	}
	if err = registry.ApplyParticipantRawRekey(17, added, rawKey); err != nil {
		t.Fatalf("identical duplicate rekey: %v", err)
	}
	conflicting := bytes.Repeat([]byte{0x5a}, 32)
	if err = registry.ApplyParticipantRawRekey(17, added, conflicting); err == nil {
		t.Fatal("conflicting duplicate participant rekey was accepted")
	}
}

func TestParticipantReceiveRegistryDoesNotFallThroughUnknownDeviceToSoleRemote(t *testing.T) {
	callKey := iota32()
	rawKey := bytes.Repeat([]byte{0xa5}, 32)
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
	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 17, false)); err != nil {
		t.Fatalf("apply sole-remote roster: %v", err)
	}
	unknownDevice := mediaTestJID(peer.User, 7)
	if err = registry.ApplyParticipantRawRekey(17, unknownDevice, rawKey); err == nil {
		t.Fatal("unknown author device fell through to sole active remote")
	}
	peerPacket, _ := protectParticipantAudio(t, callKey, self, peer, []byte{0x62})
	if _, ok := registry.DecodeAudio(peerPacket); !ok {
		t.Fatal("rejected unknown-device rekey modified sole remote receive keys")
	}
}

func TestParticipantReceiveRegistryAppliesDelayedRekeyForStillActiveAuthor(t *testing.T) {
	callKey := iota32()
	rawKey := bytes.Repeat([]byte{0xa5}, 32)
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
	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 18, true)); err != nil {
		t.Fatalf("apply newer roster: %v", err)
	}
	if err = registry.ApplyParticipantRawRekey(17, added, rawKey); err != nil {
		t.Fatalf("apply delayed participant rekey: %v", err)
	}
	rawPacket, _ := protectRawParticipantAudio(t, rawKey, added, []byte{0x63})
	if _, ok := registry.DecodeAudio(rawPacket); !ok {
		t.Fatal("delayed participant rekey was dropped solely because roster was newer")
	}
}

func TestParticipantReceiveRegistryRekeyPreservesOtherParticipantRolloverAndDecoder(t *testing.T) {
	callKey := iota32()
	rawKey := bytes.Repeat([]byte{0xa5}, 32)
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
	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 17, true)); err != nil {
		t.Fatalf("apply group roster: %v", err)
	}
	peerReceiver := registry.byPID[0]
	peerDecoder := peerReceiver.decoder
	for _, sequence := range []uint16{0xfffe, 0xffff} {
		packet := protectParticipantAudioAt(t, callKey, peer, sequence, 0, []byte{0x64})
		if _, ok := registry.DecodeAudio(packet); !ok {
			t.Fatalf("peer packet sequence %d was rejected before other participant rekey", sequence)
		}
	}

	if err = registry.ApplyParticipantRawRekey(17, added, rawKey); err != nil {
		t.Fatalf("apply added participant rekey: %v", err)
	}
	addedCallKeyPacket, _ := protectParticipantAudio(t, callKey, self, added, []byte{0x65})
	if _, ok := registry.DecodeAudio(addedCallKeyPacket); ok {
		t.Fatal("added participant's old call-key packet authenticated after raw rekey")
	}
	addedRawPacket, _ := protectRawParticipantAudio(t, rawKey, added, []byte{0x66})
	if _, ok := registry.DecodeAudio(addedRawPacket); !ok {
		t.Fatal("added participant's raw-key packet was rejected after rekey")
	}
	peerWrapped := protectParticipantAudioAt(t, callKey, peer, 0, 1, []byte{0x67})
	if _, ok := registry.DecodeAudio(peerWrapped); !ok {
		t.Fatal("rekeying added participant reset original peer rollover state")
	}
	if registry.byPID[0] != peerReceiver || registry.byPID[0].decoder != peerDecoder {
		t.Fatal("rekeying added participant replaced original peer receiver or decoder")
	}
}

func TestParticipantReceiveRegistryActivatesAndRoutesConnectedPIDDevices(t *testing.T) {
	callKey := iota32()
	self := mediaTestJID("111111111111111", 14)
	peer := mediaTestJID("222222222222222", 0)
	added := mediaTestJID("333333333333333", 43)
	pending := mediaTestJID("444444444444444", 63)
	var decoders []*recordingParticipantDecoder
	registry, err := newParticipantReceiveRegistry("CID", callKey, self.String(), peer.String(), func() participantAudioDecoder {
		decoder := &recordingParticipantDecoder{}
		decoders = append(decoders, decoder)
		return decoder
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	peerID := rtp.FormatE2ESrtpParticipantID(peer.String())
	originalPeer := registry.byDeviceID[peerID]
	if originalPeer == nil || originalPeer.hasPID {
		t.Fatal("direct-call fallback receiver was not seeded without a PID")
	}

	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 16, true)); err != nil {
		t.Fatalf("apply group update: %v", err)
	}
	if len(registry.byPID) != 2 {
		t.Fatalf("active remote PID count = %d, want 2", len(registry.byPID))
	}
	addedID := rtp.FormatE2ESrtpParticipantID(added.String())
	activeIDs := registry.ActiveParticipantIDs()
	if len(activeIDs) != 2 || activeIDs[0] != peerID || activeIDs[1] != addedID {
		t.Fatalf("active participant IDs = %v, want [%s %s]", activeIDs, peerID, addedID)
	}
	if registry.byPID[0] != originalPeer {
		t.Fatal("original peer receiver was replaced instead of promoted to PID 0")
	}
	addedReceiver := registry.byPID[2]
	if addedReceiver == nil || addedReceiver.deviceJID != added {
		t.Fatalf("added receiver = %#v, want winning device %s", addedReceiver, added)
	}
	if _, ok := registry.byPID[1]; ok {
		t.Fatal("local PID was activated as a remote receiver")
	}
	if _, ok := registry.byPID[3]; ok {
		t.Fatal("receipt-state participant with a PID was activated")
	}

	peerPacket, peerSSRC := protectParticipantAudio(t, callKey, self, peer, []byte{0x11})
	peerAudio, ok := registry.DecodeAudio(peerPacket)
	if !ok {
		t.Fatal("authenticated original-peer packet was rejected")
	}
	if peerAudio.ParticipantID != peerID || peerAudio.PID != 0 || !peerAudio.HasPID || peerAudio.SSRC != peerSSRC || peerAudio.DeviceJID != peer {
		t.Fatalf("original-peer metadata = %+v", peerAudio)
	}

	addedPacket, addedSSRC := protectParticipantAudio(t, callKey, self, added, []byte{0x22})
	addedAudio, ok := registry.DecodeAudio(addedPacket)
	if !ok {
		t.Fatal("authenticated added-participant packet was rejected")
	}
	if addedAudio.ParticipantID != addedID || addedAudio.PID != 2 || !addedAudio.HasPID || addedAudio.SSRC != addedSSRC || addedAudio.DeviceJID != added {
		t.Fatalf("added-participant metadata = %+v", addedAudio)
	}
	if len(decoders) != 2 || !bytes.Equal(decoders[0].payloads[0], []byte{0x11}) || !bytes.Equal(decoders[1].payloads[0], []byte{0x22}) {
		t.Fatalf("decoder histories = %#v", decoders)
	}

	parentSSRC, err := rtp.DeriveWasmParticipantSsrc("CID", rtp.FormatE2ESrtpParticipantID(added.ToNonAD().String()), 0)
	if err != nil {
		t.Fatalf("derive parent SSRC: %v", err)
	}
	if _, ok = registry.bySSRC[parentSSRC]; ok && parentSSRC != addedSSRC {
		t.Fatal("parent or nonwinning device SSRC was activated")
	}
}

func TestParticipantReceiveRegistryRemovesDepartedParticipantWithoutResettingPeer(t *testing.T) {
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
	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 16, true)); err != nil {
		t.Fatalf("apply connected update: %v", err)
	}
	peerReceiver := registry.byPID[0]
	addedReceiver := registry.byPID[2]
	addedPacket, addedSSRC := protectParticipantAudio(t, callKey, self, added, []byte{0x31})
	if _, ok := registry.DecodeAudio(addedPacket); !ok {
		t.Fatal("added participant did not route before departure")
	}

	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 18, false)); err != nil {
		t.Fatalf("apply departure update: %v", err)
	}
	if registry.byPID[0] != peerReceiver {
		t.Fatal("departure reset the original peer receiver")
	}
	if _, ok := registry.byPID[2]; ok {
		t.Fatal("departed participant remains indexed by PID")
	}
	if _, ok := registry.bySSRC[addedSSRC]; ok {
		t.Fatal("departed participant remains indexed by SSRC")
	}
	if _, ok := registry.DecodeAudio(addedPacket); ok {
		t.Fatal("late departed-participant packet was accepted")
	}
	if addedReceiver == nil {
		t.Fatal("test did not create the added receiver")
	}

	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 16, true)); err != nil {
		t.Fatalf("apply stale update: %v", err)
	}
	if _, ok := registry.byPID[2]; ok {
		t.Fatal("stale group update reactivated a departed participant")
	}

	peerPacket, _ := protectParticipantAudio(t, callKey, self, peer, []byte{0x41})
	if _, ok := registry.DecodeAudio(peerPacket); !ok {
		t.Fatal("original peer stopped routing after participant departure")
	}
}

func TestParticipantReceiveRegistryRejectedUpdateIsAtomic(t *testing.T) {
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
	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 16, true)); err != nil {
		t.Fatalf("apply connected update: %v", err)
	}
	peerReceiver := registry.byPID[0]
	addedReceiver := registry.byPID[2]
	peerUser := peerReceiver.userJID
	activeIDs := registry.ActiveParticipantIDs()

	invalid := mediaTestGroupUpdate(self, peer, added, pending, 20, true)
	invalid.Participants[1].JID = mediaTestJID("555555555555555", 0).ToNonAD()
	invalid.Participants[len(invalid.Participants)-1].Devices[0].PID = 0
	err = registry.ApplyGroupUpdate(invalid)
	if err == nil {
		t.Fatal("duplicate PID update was accepted")
	}
	if registry.transactionID != 16 {
		t.Fatalf("rejected update advanced transaction to %d", registry.transactionID)
	}
	if registry.byPID[0] != peerReceiver || registry.byPID[2] != addedReceiver {
		t.Fatal("rejected update replaced active receiver maps")
	}
	if peerReceiver.userJID != peerUser || peerReceiver.pid != 0 {
		t.Fatalf("rejected update mutated original peer metadata: user=%s pid=%d", peerReceiver.userJID, peerReceiver.pid)
	}
	if got := registry.ActiveParticipantIDs(); len(got) != len(activeIDs) || got[0] != activeIDs[0] || got[1] != activeIDs[1] {
		t.Fatalf("rejected update changed active IDs from %v to %v", activeIDs, got)
	}
}

func TestParticipantReceiveRegistryDepartureWaitsForInFlightDecode(t *testing.T) {
	callKey := iota32()
	self := mediaTestJID("111111111111111", 14)
	peer := mediaTestJID("222222222222222", 0)
	added := mediaTestJID("333333333333333", 43)
	pending := mediaTestJID("444444444444444", 63)
	addedDecoder := &blockingParticipantDecoder{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	decoderIndex := 0
	registry, err := newParticipantReceiveRegistry("CID", callKey, self.String(), peer.String(), func() participantAudioDecoder {
		decoderIndex++
		if decoderIndex == 2 {
			return addedDecoder
		}
		return &recordingParticipantDecoder{}
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if err = registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 16, true)); err != nil {
		t.Fatalf("apply connected update: %v", err)
	}
	addedPacket, _ := protectParticipantAudio(t, callKey, self, added, []byte{0x51})
	decodeDone := make(chan bool, 1)
	go func() {
		_, ok := registry.DecodeAudio(addedPacket)
		decodeDone <- ok
	}()
	<-addedDecoder.entered

	updateDone := make(chan error, 1)
	updateStarted := make(chan struct{})
	go func() {
		close(updateStarted)
		updateDone <- registry.ApplyGroupUpdate(mediaTestGroupUpdate(self, peer, added, pending, 18, false))
	}()
	<-updateStarted
	select {
	case err = <-updateDone:
		t.Fatalf("departure completed during in-flight decode: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(addedDecoder.release)
	if ok := <-decodeDone; !ok {
		t.Fatal("in-flight authenticated decode was rejected")
	}
	if err = <-updateDone; err != nil {
		t.Fatalf("apply departure: %v", err)
	}
	if _, ok := registry.DecodeAudio(addedPacket); ok {
		t.Fatal("departed participant produced audio after pruning completed")
	}
}
