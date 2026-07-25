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

type participantAudioReceiver struct {
	mu            sync.Mutex
	userJID       types.JID
	deviceJID     types.JID
	participantID string
	pid           uint32
	hasPID        bool
	ssrc          uint32
	pipe          *MediaPipeline
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
	sendPipe       *MediaPipeline
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
	return r, nil
}

func (r *participantReceiveRegistry) newReceiver(userJID, deviceJID types.JID, pid uint32, hasPID bool) (*participantAudioReceiver, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L37-L44
	participantID := rtp.FormatE2ESrtpParticipantID(deviceJID.String())
	ssrc, err := rtp.DeriveWasmParticipantSsrc(r.callID, participantID, 0, r.log)
	if err != nil {
		return nil, fmt.Errorf("meowcaller: derive participant audio SSRC: %w", err)
	}
	pipe, err := NewMediaPipeline(r.callKey, r.selfLID, deviceJID.String(), ssrc, FrameSamples, WithLogger(r.log))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: create participant receive pipeline: %w", err)
	}
	return &participantAudioReceiver{
		userJID: userJID, deviceJID: deviceJID, participantID: participantID,
		pid: pid, hasPID: hasPID, ssrc: ssrc, pipe: pipe, decoder: r.decoderFactory(),
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
	type receiverMetadata struct {
		receiver  *participantAudioReceiver
		userJID   types.JID
		deviceJID types.JID
		pid       uint32
	}
	var metadata []receiverMetadata
	hasConnectedPID := false
	for _, participant := range update.Participants {
		if participant.State != "connected" {
			continue
		}
		for _, device := range participant.Devices {
			if !device.HasPID {
				continue
			}
			hasConnectedPID = true
			participantID := rtp.FormatE2ESrtpParticipantID(device.JID.String())
			if participantID == r.selfID {
				continue
			}
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
			nextByDeviceID[participantID] = receiver
			nextByPID[device.PID] = receiver
			nextBySSRC[receiver.ssrc] = receiver
			metadata = append(metadata, receiverMetadata{
				receiver: receiver, userJID: participant.JID,
				deviceJID: device.JID, pid: device.PID,
			})
		}
	}
	if !hasConnectedPID {
		fallback := r.byDeviceID[r.fallbackID]
		if fallback != nil {
			nextByDeviceID[r.fallbackID] = fallback
			nextBySSRC[fallback.ssrc] = fallback
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
		Msg("applied group audio receive roster")
	return nil
}

func (r *participantReceiveRegistry) attachSendPipeline(sendPipe *MediaPipeline) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/cbe1446dabb5842362b1a4362d4100ec15d8254f/datasheets/group-media-key-epoch.md#L78-L86
	r.mu.Lock()
	r.sendPipe = sendPipe
	r.mu.Unlock()
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
	if r.sendPipe == nil {
		return fmt.Errorf("meowcaller: group media sender is not attached")
	}
	sendKeys, err := srtp.DeriveE2eKeysFromRaw(rawKey, r.selfID)
	if err != nil {
		return fmt.Errorf("meowcaller: derive group send epoch: %w", err)
	}
	receiveKeys := make(map[*participantAudioReceiver]srtp.E2eSrtpKeys, len(r.byDeviceID))
	for _, receiver := range r.byDeviceID {
		keys, deriveErr := srtp.DeriveE2eKeysFromRaw(rawKey, receiver.participantID)
		if deriveErr != nil {
			return fmt.Errorf("meowcaller: derive group receive epoch: %w", deriveErr)
		}
		receiveKeys[receiver] = keys
	}
	r.sendPipe.installSendKeys(sendKeys)
	for receiver, keys := range receiveKeys {
		receiver.pipe.installRecvKeysPreservingROC(keys)
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
	r.byPID = make(map[uint32]*participantAudioReceiver)
	r.fallbackID = participantID
	return nil
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
