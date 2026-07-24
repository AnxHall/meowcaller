package meowcaller

import (
	"bytes"
	"fmt"
	"slices"
	"sort"
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

type participantRawRekey struct {
	transactionID uint32
	author        types.JID
	rawKey        []byte
}

type installedParticipantRawRekey struct {
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
	pendingRekeys  map[uint32]map[string]participantRawRekey
	installedKeys  map[string]installedParticipantRawRekey
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
		pendingRekeys:  make(map[uint32]map[string]participantRawRekey),
		installedKeys:  make(map[string]installedParticipantRawRekey),
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
	nextInstalledKeys := make(map[string]installedParticipantRawRekey)
	for participantID := range nextByDeviceID {
		if installed, ok := r.installedKeys[participantID]; ok {
			nextInstalledKeys[participantID] = installed
		}
	}
	r.byDeviceID = nextByDeviceID
	r.byPID = nextByPID
	r.bySSRC = nextBySSRC
	r.installedKeys = nextInstalledKeys
	r.transactionID = update.TransactionID
	r.hasGroupUpdate = true
	var matching []participantRawRekey
	for transactionID, byAuthor := range r.pendingRekeys {
		if transactionID <= update.TransactionID {
			for _, rekey := range byAuthor {
				matching = append(matching, rekey)
			}
			delete(r.pendingRekeys, transactionID)
		}
	}
	sort.Slice(matching, func(i, j int) bool {
		if matching[i].transactionID != matching[j].transactionID {
			return matching[i].transactionID < matching[j].transactionID
		}
		return matching[i].author.String() < matching[j].author.String()
	})
	for _, rekey := range matching {
		if err := r.applyParticipantRawRekeyLocked(rekey); err != nil {
			r.log.Warn().
				Err(err).
				Str("call_id", r.callID).
				Uint32("transaction_id", rekey.transactionID).
				Str("author", rekey.author.String()).
				Msg("discarding participant rekey that does not match active roster")
		}
	}
	r.log.Info().
		Str("call_id", r.callID).
		Uint32("transaction_id", update.TransactionID).
		Int("remote_participants", len(nextByPID)).
		Msg("applied group audio receive roster")
	return nil
}

// ApplyParticipantRawRekey installs or buffers one participant-scoped keygen-v2
// receive key according to the authoritative group roster transaction.
func (r *participantReceiveRegistry) ApplyParticipantRawRekey(transactionID uint32, author types.JID, rawKey []byte) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/18618f30d0dc7a7bf822354d9a6c9264b275b221/datasheets/group-media-enc-rekey.md#L64-L93
	if author.IsEmpty() {
		return fmt.Errorf("meowcaller: participant rekey author is empty")
	}
	if len(rawKey) != 32 {
		return fmt.Errorf("meowcaller: participant raw rekey has %d bytes, want 32", len(rawKey))
	}
	rekey := participantRawRekey{
		transactionID: transactionID,
		author:        author,
		rawKey:        bytes.Clone(rawKey),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.hasGroupUpdate || transactionID > r.transactionID {
		return r.bufferParticipantRawRekeyLocked(rekey)
	}
	return r.applyParticipantRawRekeyLocked(rekey)
}

func (r *participantReceiveRegistry) bufferParticipantRawRekeyLocked(rekey participantRawRekey) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/18618f30d0dc7a7bf822354d9a6c9264b275b221/datasheets/group-media-enc-rekey.md#L70-L86
	authorID := rtp.FormatE2ESrtpParticipantID(rekey.author.String())
	byAuthor := r.pendingRekeys[rekey.transactionID]
	if byAuthor == nil {
		byAuthor = make(map[string]participantRawRekey)
		r.pendingRekeys[rekey.transactionID] = byAuthor
	}
	if existing, ok := byAuthor[authorID]; ok {
		if bytes.Equal(existing.rawKey, rekey.rawKey) {
			return nil
		}
		return fmt.Errorf("meowcaller: conflicting participant rekey for transaction %d and author %s", rekey.transactionID, rekey.author)
	}
	byAuthor[authorID] = rekey
	return nil
}

func (r *participantReceiveRegistry) applyParticipantRawRekeyLocked(rekey participantRawRekey) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/18618f30d0dc7a7bf822354d9a6c9264b275b221/datasheets/group-media-enc-rekey.md#L80-L93
	authorID := rtp.FormatE2ESrtpParticipantID(rekey.author.String())
	receiver := r.byDeviceID[authorID]
	if receiver == nil && rekey.author.Device == 0 && rekey.author.RawAgent == 0 {
		var candidate *participantAudioReceiver
		authorUser := rekey.author.ToNonAD()
		for _, active := range r.byDeviceID {
			if active.userJID.ToNonAD() != authorUser {
				continue
			}
			if candidate != nil {
				return fmt.Errorf("meowcaller: participant rekey author %s is ambiguous in active roster", rekey.author)
			}
			candidate = active
		}
		receiver = candidate
	}
	if receiver == nil {
		return fmt.Errorf("meowcaller: participant rekey author %s is not active in roster", rekey.author)
	}
	participantID := receiver.participantID
	if installed, ok := r.installedKeys[participantID]; ok {
		if rekey.transactionID < installed.transactionID {
			return nil
		}
		if rekey.transactionID == installed.transactionID {
			if bytes.Equal(installed.rawKey, rekey.rawKey) {
				return nil
			}
			return fmt.Errorf("meowcaller: conflicting participant rekey for transaction %d and author %s", rekey.transactionID, rekey.author)
		}
	}
	receiver.mu.Lock()
	// ASSUMPTION: the target receiver ROC resets with its key. The live newly
	// activated participant had no authenticated ROC state; an active rollover
	// requires a packet vector to decide whether WhatsApp preserves the ROC.
	err := receiver.pipe.RekeyRecvFromRaw(rekey.rawKey, receiver.deviceJID.String())
	receiver.mu.Unlock()
	if err != nil {
		return fmt.Errorf("meowcaller: install participant raw rekey: %w", err)
	}
	r.installedKeys[participantID] = installedParticipantRawRekey{
		transactionID: rekey.transactionID,
		rawKey:        bytes.Clone(rekey.rawKey),
	}
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
