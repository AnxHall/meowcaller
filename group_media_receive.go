package meowcaller

import (
	"fmt"
	"sync"

	"github.com/purpshell/meowcaller/mlow"
	"github.com/purpshell/meowcaller/rtp"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
)

type participantAudioDecoder interface {
	Decode([]byte) []float32
}

type decodedParticipantAudio struct {
	UserJID   types.JID
	DeviceJID types.JID
	PID       uint32
	HasPID    bool
	SSRC      uint32
	Timestamp uint32
	PCM       []float32
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
			receiver := r.byDeviceID[participantID]
			if receiver == nil {
				var err error
				receiver, err = r.newReceiver(participant.JID, device.JID, device.PID, true)
				if err != nil {
					return err
				}
			}
			receiver.mu.Lock()
			receiver.userJID = participant.JID
			receiver.deviceJID = device.JID
			receiver.pid = device.PID
			receiver.hasPID = true
			receiver.mu.Unlock()
			if _, exists := nextByPID[device.PID]; exists {
				return fmt.Errorf("meowcaller: duplicate group participant PID %d", device.PID)
			}
			if _, exists := nextBySSRC[receiver.ssrc]; exists {
				return fmt.Errorf("meowcaller: duplicate group participant SSRC %d", receiver.ssrc)
			}
			nextByDeviceID[participantID] = receiver
			nextByPID[device.PID] = receiver
			nextBySSRC[receiver.ssrc] = receiver
		}
	}
	r.byDeviceID = nextByDeviceID
	r.byPID = nextByPID
	r.bySSRC = nextBySSRC
	r.transactionID = update.TransactionID
	r.hasGroupUpdate = true
	r.log.Info().
		Str("call_id", r.callID).
		Uint32("transaction_id", update.TransactionID).
		Int("remote_participants", len(nextByPID)).
		Msg("applied group audio receive roster")
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
		UserJID: receiver.userJID, DeviceJID: receiver.deviceJID,
		PID: receiver.pid, HasPID: receiver.hasPID, SSRC: authenticatedHeader.Ssrc,
		Timestamp: authenticatedHeader.Timestamp, PCM: pcm,
	}, true
}
