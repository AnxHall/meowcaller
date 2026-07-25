package meowcaller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// engine adapts whatsmeow's call control plane to meowcaller's media plane.
// Whatsmeow owns signaling, call-key exchange, and relay election. Meowcaller owns
// RTP/SRTP, codecs, media pacing, reactions, and public media callbacks.
type engine struct {
	c *Client

	mu                             sync.Mutex
	calls                          map[string]*engineCall
	offerGroupCall                 func(context.Context, []types.JID, ...whatsmeow.GroupCallOfferOptions) (string, error)
	setCallVideo                   func(context.Context, string, types.CallVideoState, *int) error
	setCallMute                    func(context.Context, string, bool) error
	inviteCallParticipant          func(context.Context, string, types.JID) error
	startMedia                     func(context.Context, string, *Call, []byte, string, string, *types.RelayEndpoint) error
	onMediaStopped                 func(string)
	scheduleGroupPlaceholderExpiry func(string, time.Duration, func()) func()
}

type engineCall struct {
	call    *Call
	callKey []byte
	relay   *types.RelayEndpoint
	selfLID string
	peerLID string
	from    types.JID

	direction          CallDirection
	group              bool
	codec              AudioCodec
	localVideo         bool
	remoteVideo        bool
	videoGate          bool
	peerVideoUpgrade   bool
	videoTx            *videoSender
	appDataTx          *appDataSender
	rekeyPeer          func(string) error
	groupUpdate        *types.GroupCallUpdate
	pendingGroupUpdate *types.GroupCallUpdate
	queuedGroupUpdates []types.GroupCallUpdate
	applyGroupUpdate   func(types.GroupCallUpdate) error
	pendingGroupRekeys []events.CallEncRekey
	applyGroupRekey    func(events.CallEncRekey) error
	groupActivating    bool
	groupActive        bool
	placeholder        bool
	cancelPlaceholder  func()
	started            bool
	ended              bool
	cancel             context.CancelFunc
}

func newEngine(c *Client) *engine {
	e := &engine{c: c, calls: make(map[string]*engineCall)}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L94-L107
	e.startMedia = e.runMedia
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L108-L122
	e.scheduleGroupPlaceholderExpiry = func(_ string, ttl time.Duration, expire func()) func() {
		timer := time.AfterFunc(ttl, expire)
		return func() {
			timer.Stop()
		}
	}
	if c != nil && c.wa != nil {
		e.offerGroupCall = c.wa.OfferGroupCall
		e.setCallVideo = c.wa.SetCallVideo
		e.setCallMute = c.wa.SetCallMute
		e.inviteCallParticipant = c.wa.InviteCallParticipant
	}
	return e
}

func (e *engine) sendCallVideo(ctx context.Context, callID string, state types.CallVideoState, orientation *int) error {
	if e.setCallVideo == nil {
		return errors.New("meowcaller: call signaling is unavailable")
	}
	return e.setCallVideo(ctx, callID, state, orientation)
}

func (e *engine) inviteParticipant(ctx context.Context, callID, target string) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/160912971e6bc2a4aa79ac3aafcf08360075e3fc/datasheets/api-group-participant-invite.md#L23-L100
	e.mu.Lock()
	m := e.calls[callID]
	active := m != nil && m.call != nil && m.call.State() != CallPhaseEnded
	e.mu.Unlock()
	if !active {
		return errors.New("meowcaller: call is not active")
	}
	jid, err := parseCallTarget(target)
	if err != nil {
		return err
	}
	if e.inviteCallParticipant == nil {
		return errors.New("meowcaller: call signaling is unavailable")
	}
	if err = e.inviteCallParticipant(ctx, callID, jid); err != nil {
		return fmt.Errorf("meowcaller: add participant: %w", err)
	}
	return nil
}

func (c *Call) onEndFn() func(string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onEnd
}

func (c *Call) onReadyFn() func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onReady
}

func (c *Call) playerAndSink() (*Player, AudioSink) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.player, c.sink
}

func (e *engine) install() {
	e.c.wa.AddEventHandler(func(evt any) {
		switch ev := evt.(type) {
		case *events.CallOffer:
			e.onOffer(ev)
		case *events.CallPreAccept:
			e.onPreAccept(ev)
		case *events.CallAccept:
			e.onAccept(ev)
		case *events.CallMediaReady:
			e.onMediaReady(ev)
		case *events.CallMediaStop:
			e.onMediaStop(ev)
		case *events.CallMute:
			e.onMute(ev)
		case *events.CallVideo:
			e.onVideo(ev)
		case *events.CallGroupUpdate:
			e.onGroupUpdate(ev)
		case *events.CallEncRekey:
			e.onEncRekey(ev)
		}
	})
}

func (e *engine) entry(callID string) *engineCall {
	if e.calls[callID] == nil {
		e.calls[callID] = &engineCall{codec: AudioCodecMlow}
	}
	return e.calls[callID]
}

const groupPlaceholderTTL = 30 * time.Second

func (e *engine) groupEntry(callID string) *engineCall {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L108-L122
	m := e.calls[callID]
	if m != nil {
		m.group = true
		return m
	}
	m = &engineCall{
		codec:       AudioCodecMlow,
		group:       true,
		placeholder: true,
	}
	e.calls[callID] = m
	if e.scheduleGroupPlaceholderExpiry != nil {
		m.cancelPlaceholder = e.scheduleGroupPlaceholderExpiry(
			callID,
			groupPlaceholderTTL,
			func() {
				e.removeGroupPlaceholderIfSame(callID, m)
			},
		)
	}
	return m
}

func (e *engine) attachGroupPlaceholder(m *engineCall) func() {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	if m == nil || !m.placeholder {
		return nil
	}
	m.placeholder = false
	cancel := m.cancelPlaceholder
	m.cancelPlaceholder = nil
	return cancel
}

func (e *engine) removeGroupPlaceholderIfSame(callID string, expected *engineCall) bool {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	if callID == "" || expected == nil {
		return false
	}
	e.mu.Lock()
	current := e.calls[callID]
	if current != expected || !current.placeholder {
		e.mu.Unlock()
		return false
	}
	current.placeholder = false
	cancel := current.cancelPlaceholder
	current.cancelPlaceholder = nil
	clearEngineCallKeyMaterial(current)
	delete(e.calls, callID)
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func clearEngineCallKeyMaterial(m *engineCall) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	if m == nil {
		return
	}
	clear(m.callKey)
	m.callKey = nil
	for i := range m.pendingGroupRekeys {
		clear(m.pendingGroupRekeys[i].RawKey)
	}
	m.pendingGroupRekeys = nil
	clearGroupCallUpdateKeyMaterial(m.groupUpdate)
	m.groupUpdate = nil
	clearGroupCallUpdateKeyMaterial(m.pendingGroupUpdate)
	m.pendingGroupUpdate = nil
	for i := range m.queuedGroupUpdates {
		clearGroupCallUpdateKeyMaterial(&m.queuedGroupUpdates[i])
	}
	m.queuedGroupUpdates = nil
	if m.relay != nil {
		clear(m.relay.Key)
		clear(m.relay.Token)
		clear(m.relay.AuthToken)
	}
	m.relay = nil
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func clearGroupCallUpdateKeyMaterial(update *types.GroupCallUpdate) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	if update == nil || update.Relay == nil {
		return
	}
	clear(update.Relay.Key)
	clear(update.Relay.HBHKey)
	for i := range update.Relay.Tokens {
		clear(update.Relay.Tokens[i])
	}
	for i := range update.Relay.AuthTokens {
		clear(update.Relay.AuthTokens[i])
	}
}

func cloneRelayEndpoint(endpoint types.RelayEndpoint) types.RelayEndpoint {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	clone := endpoint
	clone.Key = bytes.Clone(endpoint.Key)
	clone.Token = bytes.Clone(endpoint.Token)
	clone.AuthToken = bytes.Clone(endpoint.AuthToken)
	return clone
}

func discardQueuedGroupUpdatesAtOrBelow(
	updates []types.GroupCallUpdate,
	transactionID uint32,
) []types.GroupCallUpdate {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L119-L122
	kept := make([]types.GroupCallUpdate, 0, len(updates))
	for i := range updates {
		if updates[i].TransactionID <= transactionID {
			clearGroupCallUpdateKeyMaterial(&updates[i])
			continue
		}
		kept = append(kept, updates[i])
		updates[i] = types.GroupCallUpdate{}
	}
	clear(updates)
	return kept
}

func (e *engine) lookup(callID string) *engineCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[callID]
}

func (e *engine) callIsVideo(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	return m != nil && (m.localVideo || m.remoteVideo)
}

func (e *engine) callIsSendingVideo(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	return m != nil && m.localVideo
}

func (e *engine) callIsReceivingVideo(callID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := e.calls[callID]
	return m != nil && m.remoteVideo
}

func (e *engine) sendReaction(callID, emoji string) error {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call is not active")
	}
	sender := m.appDataTx
	e.mu.Unlock()
	if sender == nil {
		return errAppDataUnavailable
	}
	return sender.sendReaction(emoji)
}

func (e *engine) sendVideoFrame(callID string, accessUnit []byte, duration time.Duration) error {
	e.mu.Lock()
	var sender *videoSender
	if m := e.calls[callID]; m != nil {
		sender = m.videoTx
	}
	e.mu.Unlock()
	if sender == nil {
		return errors.New("meowcaller: call has no active video media")
	}
	sender.send(accessUnit, duration)
	return nil
}

func (e *engine) transitionVideo(callID string, transition types.CallVideoState) error {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call is not active")
	}
	sender := m.videoTx
	localVideoActive := m.localVideo
	switch transition {
	case types.CallVideoStateUpgradeRequestV2:
		m.localVideo = true
		m.videoGate = true
	case types.CallVideoStateUpgradeAccept:
		if !m.peerVideoUpgrade {
			e.mu.Unlock()
			return errors.New("meowcaller: no pending peer video upgrade")
		}
		m.peerVideoUpgrade = false
	case types.CallVideoStateStopped:
		m.localVideo = false
		m.videoGate = false
	default:
		e.mu.Unlock()
		return fmt.Errorf("meowcaller: unsupported local video transition %d", transition)
	}
	e.mu.Unlock()

	if sender != nil {
		switch transition {
		case types.CallVideoStateUpgradeRequestV2:
			sender.enable(true)
		case types.CallVideoStateStopped:
			sender.disable()
		}
	}

	orientation := 0
	var err error
	switch transition {
	case types.CallVideoStateUpgradeRequestV2, types.CallVideoStateStopped:
		err = e.sendCallVideo(context.Background(), callID, transition, &orientation)
	case types.CallVideoStateUpgradeAccept:
		if !localVideoActive {
			err = e.sendCallVideo(context.Background(), callID, types.CallVideoStateStopped, &orientation)
		}
		if err == nil {
			err = e.sendCallVideo(context.Background(), callID, transition, nil)
		}
	}
	if err == nil || transition == types.CallVideoStateStopped {
		return err
	}

	e.mu.Lock()
	var currentSender *videoSender
	if current := e.calls[callID]; current == m {
		if transition == types.CallVideoStateUpgradeAccept {
			current.peerVideoUpgrade = true
		} else {
			current.localVideo = false
			current.videoGate = false
			currentSender = current.videoTx
		}
	}
	e.mu.Unlock()
	if currentSender != nil {
		currentSender.disable()
	}
	return err
}

func (e *engine) setVideoEnabled(callID string, enabled bool) error {
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return errors.New("meowcaller: call is not active")
	}
	m.localVideo = enabled
	m.videoGate = false
	sender := m.videoTx
	e.mu.Unlock()

	if sender != nil {
		if enabled {
			sender.enable(false)
		} else {
			sender.disable()
		}
	}
	state := types.CallVideoStateDisabled
	if enabled {
		state = types.CallVideoStateEnabled
	}
	err := e.sendCallVideo(context.Background(), callID, state, nil)
	if err == nil || !enabled {
		return err
	}
	e.mu.Lock()
	if current := e.calls[callID]; current == m {
		current.localVideo = false
	}
	e.mu.Unlock()
	if sender != nil {
		sender.disable()
	}
	return err
}

func (e *engine) setVideoOrientation(callID string, orientation int) error {
	if orientation < 0 || orientation > 3 {
		return fmt.Errorf("meowcaller: video orientation %d is outside 0..3", orientation)
	}
	e.mu.Lock()
	m := e.calls[callID]
	active := m != nil && m.call != nil && m.call.State() != CallPhaseEnded && m.localVideo
	e.mu.Unlock()
	if !active {
		return errors.New("meowcaller: call has no active video media")
	}
	return e.sendCallVideo(context.Background(), callID, types.CallVideoStateEnabled, &orientation)
}

func (e *engine) setMuted(callID string, muted bool) error {
	e.mu.Lock()
	m := e.calls[callID]
	active := m != nil && m.call != nil && m.call.State() != CallPhaseEnded
	e.mu.Unlock()
	if !active {
		return errors.New("meowcaller: call is not active")
	}
	if e.setCallMute == nil {
		return errors.New("meowcaller: call signaling is unavailable")
	}
	if err := e.setCallMute(context.Background(), callID, muted); err != nil {
		return fmt.Errorf("meowcaller: set call mute: %w", err)
	}
	return nil
}

func (e *engine) placeCall(ctx context.Context, target string, opts CallOptions) (*Call, error) {
	jid, err := parseCallTarget(target)
	if err != nil {
		return nil, err
	}
	callID, err := e.c.wa.OfferCall(ctx, jid, whatsmeowCallOptions(opts))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: offer call: %w", err)
	}
	call := &Call{eng: e, id: callID, peer: jid, phase: CallPhaseCalling}
	e.mu.Lock()
	m := e.entry(callID)
	if m.call == nil {
		m.call = call
	} else {
		call = m.call
	}
	m.from = jid
	m.direction = CallDirectionOutgoing
	m.localVideo = opts.Video
	m.remoteVideo = opts.Video
	e.mu.Unlock()
	e.c.diag.Emit("meta", map[string]any{
		"event": "offer_sent", "call_id": callID, "peer": jid.String(), "direction": "out", "video": opts.Video,
	})
	return call, nil
}

func (e *engine) placeGroupCall(
	ctx context.Context,
	targets []string,
	opts GroupCallOptions,
) (*Call, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L107
	selected, err := parseGroupCallTargets(targets)
	if err != nil {
		return nil, err
	}
	groupJID, err := parseOptionalGroupJID(opts.GroupJID)
	if err != nil {
		return nil, err
	}
	if e.offerGroupCall == nil {
		return nil, errors.New("meowcaller: call signaling is unavailable")
	}
	callID, err := e.offerGroupCall(
		ctx,
		append([]types.JID(nil), selected...),
		whatsmeow.GroupCallOfferOptions{GroupJID: groupJID},
	)
	if err != nil {
		// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
		e.mu.Lock()
		placeholder := e.calls[callID]
		e.mu.Unlock()
		e.removeGroupPlaceholderIfSame(callID, placeholder)
		return nil, fmt.Errorf("meowcaller: offer group call: %w", err)
	}

	call := &Call{
		eng: e, id: callID, peer: selected[0], phase: CallPhaseCalling,
		groupState: selectedGroupCallState(selected),
	}
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil {
		m = e.entry(callID)
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L122
	wasPlaceholder := m.placeholder
	cancelPlaceholder := e.attachGroupPlaceholder(m)
	if m.call == nil || wasPlaceholder {
		m.call = call
	} else {
		call = m.call
	}
	m.from = selected[0]
	m.direction = CallDirectionOutgoing
	m.group = true
	authoritative := m.groupUpdate
	e.mu.Unlock()
	if cancelPlaceholder != nil {
		cancelPlaceholder()
	}
	if authoritative != nil {
		call.setGroupState(groupCallStateFromUpdate(*authoritative))
	}
	e.c.diag.Emit("meta", map[string]any{
		"event": "group_offer_sent", "call_id": callID,
		"peer": selected[0].String(), "target_count": len(selected), "direction": "out",
	})
	e.maybeStartMedia(callID)
	return call, nil
}

func parseGroupCallTargets(targets []string) ([]types.JID, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L99
	selected := make([]types.JID, 0, len(targets))
	seen := make(map[types.JID]struct{}, len(targets))
	for i, target := range targets {
		target = strings.TrimSpace(target)
		// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L108-L110
		if strings.ContainsRune(target, '@') && strings.Count(target, "@") != 1 {
			return nil, fmt.Errorf(
				"meowcaller: parse group call target %d: expected one @",
				i,
			)
		}
		jid, err := parseCallTarget(target)
		if err != nil {
			return nil, fmt.Errorf("meowcaller: parse group call target %d: %w", i, err)
		}
		jid = jid.ToNonAD()
		if jid.IsEmpty() || jid.User == "" {
			return nil, fmt.Errorf("meowcaller: group call target %d is empty", i)
		}
		if jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
			return nil, fmt.Errorf(
				"meowcaller: group call target %d uses non-user server %q",
				i,
				jid.Server,
			)
		}
		if _, exists := seen[jid]; exists {
			continue
		}
		seen[jid] = struct{}{}
		selected = append(selected, jid)
	}
	if len(selected) < 2 {
		return nil, errors.New("meowcaller: group call requires at least two distinct targets")
	}
	return selected, nil
}

func parseOptionalGroupJID(raw string) (types.JID, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L96
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return types.EmptyJID, nil
	}
	if strings.Count(raw, "@") != 1 {
		return types.EmptyJID, fmt.Errorf("meowcaller: parse group JID %q: expected one @", raw)
	}
	jid, err := types.ParseJID(raw)
	if err != nil {
		return types.EmptyJID, fmt.Errorf("meowcaller: parse group JID %q: %w", raw, err)
	}
	if jid.User == "" || jid.Server != types.GroupServer ||
		jid.RawAgent != 0 || jid.Device != 0 || jid.Integrator != 0 ||
		jid.String() != raw {
		return types.EmptyJID, fmt.Errorf("meowcaller: group JID %q is not a canonical g.us JID", raw)
	}
	return jid, nil
}

func selectedGroupCallState(targets []types.JID) *GroupCallState {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L97-L104
	state := &GroupCallState{
		Participants: make([]GroupCallParticipant, len(targets)),
	}
	for i, target := range targets {
		state.Participants[i] = GroupCallParticipant{
			JID:   target,
			State: "outgoing",
		}
	}
	return state
}

func cloneGroupCallUpdate(update types.GroupCallUpdate) types.GroupCallUpdate {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L100-L107
	clone := update
	clone.Participants = make([]types.GroupCallParticipant, len(update.Participants))
	for participantIndex, participant := range update.Participants {
		clone.Participants[participantIndex] = participant
		clone.Participants[participantIndex].Devices = make(
			[]types.GroupCallDevice,
			len(participant.Devices),
		)
		for deviceIndex, device := range participant.Devices {
			clone.Participants[participantIndex].Devices[deviceIndex] = device
			clone.Participants[participantIndex].Devices[deviceIndex].Capability =
				bytes.Clone(device.Capability)
		}
	}
	if update.Relay == nil {
		return clone
	}
	relay := *update.Relay
	relay.Key = bytes.Clone(update.Relay.Key)
	relay.HBHKey = bytes.Clone(update.Relay.HBHKey)
	relay.Tokens = cloneByteSlices(update.Relay.Tokens)
	relay.AuthTokens = cloneByteSlices(update.Relay.AuthTokens)
	relay.Endpoints = append(
		[]types.GroupCallRelayEndpoint(nil),
		update.Relay.Endpoints...,
	)
	for i := range relay.Endpoints {
		relay.Endpoints[i].Address = bytes.Clone(update.Relay.Endpoints[i].Address)
	}
	clone.Relay = &relay
	return clone
}

func cloneByteSlices(values [][]byte) [][]byte {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L100-L107
	clones := make([][]byte, len(values))
	for i, value := range values {
		clones[i] = bytes.Clone(value)
	}
	return clones
}

func whatsmeowCallOptions(opts CallOptions) whatsmeow.CallOfferOptions {
	return whatsmeow.CallOfferOptions{Video: opts.Video}
}

func parseCallTarget(target string) (types.JID, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return types.EmptyJID, errors.New("meowcaller: empty call target")
	}
	if strings.ContainsRune(target, '@') {
		jid, err := types.ParseJID(target)
		if err != nil {
			return types.EmptyJID, fmt.Errorf("meowcaller: parse target JID %q: %w", target, err)
		}
		return jid, nil
	}
	return types.NewJID(strings.TrimPrefix(target, "+"), types.DefaultUserServer), nil
}

func (e *engine) onOffer(ev *events.CallOffer) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L100-L104
	peer := ev.CallCreator
	if peer.IsEmpty() {
		peer = ev.From
	}
	call := &Call{eng: e, id: ev.CallID, peer: peer, phase: CallPhaseRinging}
	e.mu.Lock()
	m := e.entry(ev.CallID)
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L122
	cancelPlaceholder := e.attachGroupPlaceholder(m)
	if m.call == nil {
		m.call = call
	} else {
		call = m.call
	}
	m.from = ev.From
	m.direction = CallDirectionIncoming
	m.localVideo = ev.Video
	m.remoteVideo = ev.Video
	var snapshot *types.GroupCallUpdate
	if ev.Group != nil {
		update := cloneGroupCallUpdate(*ev.Group)
		m.group = true
		if m.groupUpdate == nil || update.TransactionID > m.groupUpdate.TransactionID {
			cached := cloneGroupCallUpdate(update)
			pending := cloneGroupCallUpdate(update)
			m.groupUpdate = &cached
			m.pendingGroupUpdate = &pending
		}
		cached := cloneGroupCallUpdate(*m.groupUpdate)
		snapshot = &cached
	}
	e.mu.Unlock()
	if cancelPlaceholder != nil {
		cancelPlaceholder()
	}
	if snapshot != nil {
		call.setGroupState(groupCallStateFromUpdate(*snapshot))
	}
	e.c.diag.Emit("meta", map[string]any{
		"event": "offer_received", "call_id": ev.CallID, "from": ev.From.String(), "peer": peer.String(), "video": ev.Video,
	})
	if fn := e.c.incomingCallHandler(); fn != nil {
		fn(call)
	}
}

func (e *engine) onPreAccept(ev *events.CallPreAccept) {
	e.mu.Lock()
	m := e.calls[ev.CallID]
	if m != nil && m.call != nil && m.direction == CallDirectionOutgoing && m.call.State() == CallPhaseCalling {
		m.call.setPhase(CallPhaseRinging)
	}
	e.mu.Unlock()
}

func (e *engine) onAccept(ev *events.CallAccept) {
	e.mu.Lock()
	m := e.calls[ev.CallID]
	if m == nil || m.direction != CallDirectionOutgoing || m.call == nil || m.call.State() == CallPhaseEnded {
		e.mu.Unlock()
		return
	}
	answeringPeer := ev.From.String()
	var rekeyPeer func(string) error
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L100-L107
	if !m.group && answeringPeer != "" && answeringPeer != m.peerLID {
		m.peerLID = answeringPeer
		rekeyPeer = m.rekeyPeer
		m.call.setPeer(ev.From)
	}
	if !ev.From.IsEmpty() {
		m.from = ev.From
	}
	call := m.call
	e.mu.Unlock()
	if rekeyPeer != nil {
		if err := rekeyPeer(answeringPeer); err != nil {
			e.c.log.Warn().Err(err).Str("call_id", ev.CallID).Str("peer_lid", answeringPeer).Msg("failed to rekey media to answering device")
		}
	}
	if call.State() < CallPhaseConnecting {
		call.setPhase(CallPhaseConnecting)
	}
	call.markPeerAccepted()
}

func (e *engine) onGroupUpdate(ev *events.CallGroupUpdate) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ca4ba64503efeb86c337ee37cb00c4da540c632c/datasheets/group-media-receive.md#L83-L85
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L122
	if ev == nil || ev.CallID == "" {
		return
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L97-L107
	update := cloneGroupCallUpdate(ev.Update)
	e.mu.Lock()
	m := e.calls[ev.CallID]
	if m == nil {
		// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L122
		m = e.groupEntry(ev.CallID)
	}
	m.group = true
	if m.ended || (m.call != nil && m.call.State() == CallPhaseEnded) {
		e.mu.Unlock()
		return
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L119-L122
	if m.groupActivating && m.applyGroupUpdate == nil {
		m.queuedGroupUpdates = append(m.queuedGroupUpdates, update)
		e.mu.Unlock()
		return
	}
	if m.groupUpdate != nil && update.TransactionID <= m.groupUpdate.TransactionID {
		e.mu.Unlock()
		return
	}
	apply := m.applyGroupUpdate
	if apply == nil {
		cached := cloneGroupCallUpdate(update)
		pending := cloneGroupCallUpdate(update)
		m.groupUpdate = &cached
		m.pendingGroupUpdate = &pending
		call := m.call
		e.mu.Unlock()
		if call != nil {
			call.setGroupState(groupCallStateFromUpdate(update))
		}
		return
	}
	e.mu.Unlock()
	if err := apply(update); err != nil {
		e.c.log.Warn().
			Err(err).
			Str("call_id", ev.CallID).
			Uint32("transaction_id", update.TransactionID).
			Msg("failed to apply group media roster")
		return
	}
	e.mu.Lock()
	m = e.calls[ev.CallID]
	var call *Call
	if m != nil && !m.ended && m.call != nil && m.call.State() != CallPhaseEnded &&
		(m.groupUpdate == nil || update.TransactionID > m.groupUpdate.TransactionID) {
		cached := cloneGroupCallUpdate(update)
		m.groupUpdate = &cached
		call = m.call
	}
	e.mu.Unlock()
	if call != nil {
		call.setGroupState(groupCallStateFromUpdate(update))
	}
}

func (e *engine) onEncRekey(ev *events.CallEncRekey) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/18618f30d0dc7a7bf822354d9a6c9264b275b221/datasheets/group-media-enc-rekey.md#L48-L93
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L122
	if ev == nil || ev.CallID == "" || len(ev.RawKey) != 32 {
		return
	}
	rekey := *ev
	rekey.RawKey = bytes.Clone(ev.RawKey)
	rekey.Rekey.Ciphertext = nil
	rekey.Data = nil
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L97-L107
	e.mu.Lock()
	m := e.calls[ev.CallID]
	if m == nil {
		// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L122
		m = e.groupEntry(ev.CallID)
	}
	m.group = true
	if m.ended || (m.call != nil && m.call.State() == CallPhaseEnded) {
		e.mu.Unlock()
		return
	}
	apply := m.applyGroupRekey
	if apply == nil {
		m.pendingGroupRekeys = append(m.pendingGroupRekeys, rekey)
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	if err := apply(rekey); err != nil {
		e.c.log.Warn().
			Err(err).
			Str("call_id", ev.CallID).
			Uint32("transaction_id", ev.Rekey.TransactionID).
			Str("author", ev.From.String()).
			Msg("failed to apply participant media rekey")
	}
}

func (e *engine) activateGroupMedia(
	callID string,
	applyGroupUpdate func(types.GroupCallUpdate) error,
	applyGroupRekey func(events.CallEncRekey) error,
) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L97-L107
	if applyGroupUpdate == nil || applyGroupRekey == nil {
		return
	}
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.ended || m.groupActivating || m.groupActive {
		e.mu.Unlock()
		return
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L119-L122
	m.groupActivating = true
	e.mu.Unlock()

	firstBatch := true
	var rejectedPublicTransactionID *uint32
	for {
		e.mu.Lock()
		current := e.calls[callID]
		if current != m || m.ended {
			m.groupActivating = false
			e.mu.Unlock()
			return
		}
		var pendingGroupUpdate *types.GroupCallUpdate
		pendingGroupUpdateWasPublic := false
		if firstBatch && m.pendingGroupUpdate != nil {
			update := cloneGroupCallUpdate(*m.pendingGroupUpdate)
			pendingGroupUpdate = &update
			pendingGroupUpdateWasPublic = m.groupUpdate != nil &&
				m.groupUpdate.TransactionID == update.TransactionID
			clearGroupCallUpdateKeyMaterial(m.pendingGroupUpdate)
			m.pendingGroupUpdate = nil
		} else if firstBatch && m.groupUpdate != nil {
			update := cloneGroupCallUpdate(*m.groupUpdate)
			pendingGroupUpdate = &update
			pendingGroupUpdateWasPublic = true
		} else if len(m.queuedGroupUpdates) > 0 {
			update := cloneGroupCallUpdate(m.queuedGroupUpdates[0])
			pendingGroupUpdate = &update
			clearGroupCallUpdateKeyMaterial(&m.queuedGroupUpdates[0])
			m.queuedGroupUpdates[0] = types.GroupCallUpdate{}
			m.queuedGroupUpdates = m.queuedGroupUpdates[1:]
			if len(m.queuedGroupUpdates) == 0 {
				m.queuedGroupUpdates = nil
			}
		}
		if firstBatch && pendingGroupUpdate != nil {
			clearGroupCallUpdateKeyMaterial(m.groupUpdate)
			m.groupUpdate = nil
		}
		pendingGroupRekeys := append(
			[]events.CallEncRekey(nil),
			m.pendingGroupRekeys...,
		)
		m.pendingGroupRekeys = nil
		if pendingGroupUpdate == nil && len(pendingGroupRekeys) == 0 {
			m.applyGroupUpdate = applyGroupUpdate
			m.applyGroupRekey = applyGroupRekey
			m.groupActivating = false
			m.groupActive = true
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
		firstBatch = false

		pendingGroupUpdateAccepted := true
		if pendingGroupUpdate != nil {
			if err := applyGroupUpdate(*pendingGroupUpdate); err != nil {
				pendingGroupUpdateAccepted = false
				if pendingGroupUpdateWasPublic {
					rejectedTransactionID := pendingGroupUpdate.TransactionID
					rejectedPublicTransactionID = &rejectedTransactionID
				}
				e.c.log.Warn().
					Err(err).
					Uint32("transaction_id", pendingGroupUpdate.TransactionID).
					Msg("ignored invalid pending group media roster")
			}
		}
		for _, rekey := range pendingGroupRekeys {
			if err := applyGroupRekey(rekey); err != nil {
				e.c.log.Warn().
					Err(err).
					Uint32("transaction_id", rekey.Rekey.TransactionID).
					Str("author", rekey.From.String()).
					Msg("ignored pending participant media rekey")
			}
		}

		e.mu.Lock()
		current = e.calls[callID]
		if current != m || m.ended {
			m.groupActivating = false
			e.mu.Unlock()
			return
		}
		// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L119-L122
		var call *Call
		var publishedGroupUpdate *types.GroupCallUpdate
		var rejectedTransactionID *uint32
		if pendingGroupUpdate != nil && pendingGroupUpdateAccepted &&
			(m.groupUpdate == nil ||
				pendingGroupUpdate.TransactionID > m.groupUpdate.TransactionID) {
			clearGroupCallUpdateKeyMaterial(m.groupUpdate)
			cached := cloneGroupCallUpdate(*pendingGroupUpdate)
			m.groupUpdate = &cached
			if m.call != nil && m.call.State() != CallPhaseEnded {
				call = m.call
				published := cloneGroupCallUpdate(*pendingGroupUpdate)
				publishedGroupUpdate = &published
				if rejectedPublicTransactionID != nil &&
					pendingGroupUpdate.TransactionID <
						*rejectedPublicTransactionID {
					rejected := *rejectedPublicTransactionID
					rejectedTransactionID = &rejected
				}
				rejectedPublicTransactionID = nil
			}
		}
		if m.groupUpdate != nil {
			m.queuedGroupUpdates = discardQueuedGroupUpdatesAtOrBelow(
				m.queuedGroupUpdates,
				m.groupUpdate.TransactionID,
			)
		}
		e.mu.Unlock()
		if call != nil && publishedGroupUpdate != nil {
			state := groupCallStateFromUpdate(*publishedGroupUpdate)
			if rejectedTransactionID != nil {
				call.setGroupStateAfterRejected(*rejectedTransactionID, state)
			} else {
				call.setGroupState(state)
			}
		}
	}
}

func (e *engine) onMediaReady(ev *events.CallMediaReady) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L100-L107
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	relay := cloneRelayEndpoint(ev.Relay)
	e.mu.Lock()
	m := e.entry(ev.CallID)
	if m.ended {
		e.mu.Unlock()
		return
	}
	if m.call == nil {
		// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L115-L122
		if !m.group {
			phase := CallPhaseRinging
			if ev.Direction == types.CallDirectionOutgoing {
				phase = CallPhaseCalling
			}
			m.call = &Call{eng: e, id: ev.CallID, peer: ev.PeerLID, phase: phase}
		}
	}
	m.callKey = append(m.callKey[:0], ev.CallKey...)
	m.relay = &relay
	m.selfLID = ev.SelfLID.String()
	m.peerLID = ev.PeerLID.String()
	if !m.group && m.call != nil {
		m.call.setPeer(ev.PeerLID)
	}
	m.localVideo = m.localVideo || ev.Video
	m.remoteVideo = m.remoteVideo || ev.Video
	if ev.Codec == types.CallCodecOpus {
		m.codec = AudioCodecOpus
	} else {
		m.codec = AudioCodecMlow
	}
	e.mu.Unlock()
	e.c.diag.Emit("meta", map[string]any{
		"event": "media_ready", "call_id": ev.CallID, "self_lid": ev.SelfLID.String(),
		"peer_lid": ev.PeerLID.String(), "codec": ev.Codec.String(), "video": ev.Video,
	})
	e.maybeStartMedia(ev.CallID)
}

func (e *engine) onMute(ev *events.CallMute) {
	e.mu.Lock()
	m := e.calls[ev.CallID]
	var call *Call
	if m != nil {
		call = m.call
	}
	e.mu.Unlock()
	if call != nil {
		if fn := call.onMuteStateFn(); fn != nil {
			fn(ev.Muted)
		}
	}
}

func (e *engine) onVideo(ev *events.CallVideo) {
	e.mu.Lock()
	m := e.calls[ev.CallID]
	if m == nil {
		e.mu.Unlock()
		return
	}
	call := m.call
	sender := m.videoTx
	enableSender := false
	disableSender := false
	requestKeyframe := false
	announceEnabled := false
	switch ev.State {
	case types.CallVideoStateUpgradeRequest, types.CallVideoStateUpgradeRequestV2:
		m.peerVideoUpgrade = true
	case types.CallVideoStateEnabled:
		m.remoteVideo = true
		if m.localVideo && m.videoGate {
			m.videoGate = false
			enableSender = true
			requestKeyframe = true
		}
	case types.CallVideoStateDisabled, types.CallVideoStateStopped:
		m.remoteVideo = false
	case types.CallVideoStateUpgradeAccept:
		m.localVideo = true
		m.videoGate = false
		announceEnabled = true
	case types.CallVideoStateUpgradeReject, types.CallVideoStateUpgradeCancel:
		m.peerVideoUpgrade = false
		if m.videoGate {
			m.localVideo = false
			m.videoGate = false
			disableSender = true
		}
	}
	e.mu.Unlock()

	if announceEnabled {
		orientation := 0
		if err := e.sendCallVideo(context.Background(), ev.CallID, types.CallVideoStateEnabled, &orientation); err != nil {
			e.mu.Lock()
			if current := e.calls[ev.CallID]; current == m {
				current.localVideo = false
				current.videoGate = false
			}
			e.mu.Unlock()
			disableSender = true
		} else {
			enableSender = true
			requestKeyframe = true
		}
	}
	if sender != nil {
		if enableSender {
			sender.enable(false)
		} else if disableSender {
			sender.disable()
		}
	}
	if requestKeyframe && call != nil {
		call.requestVideoKeyframe()
	}
	if call != nil {
		if fn := call.onVideoStateFn(); fn != nil {
			fn(VideoState{
				Active:      ev.State == types.CallVideoStateEnabled,
				Upgrade:     ev.State == types.CallVideoStateUpgradeRequest || ev.State == types.CallVideoStateUpgradeRequestV2,
				Orientation: ev.Orientation,
				Raw:         int(ev.State),
			})
		}
	}
}

func (e *engine) onMediaStop(ev *events.CallMediaStop) {
	e.finishCall(ev.CallID, ev.Reason)
}

func (e *engine) answer(c *Call) error {
	if err := e.c.wa.AcceptCall(context.Background(), c.id); err != nil {
		return fmt.Errorf("meowcaller: accept call: %w", err)
	}
	c.setPhase(CallPhaseConnecting)
	return nil
}

func (e *engine) reject(c *Call) error {
	from := c.Peer()
	if m := e.lookup(c.id); m != nil && !m.from.IsEmpty() {
		from = m.from
	}
	if err := e.c.wa.RejectCall(context.Background(), from, c.id); err != nil {
		return fmt.Errorf("meowcaller: reject call: %w", err)
	}
	return nil
}

func (e *engine) hangup(c *Call) error {
	if err := e.c.wa.HangupCall(context.Background(), c.id); err != nil {
		return fmt.Errorf("meowcaller: hangup call: %w", err)
	}
	return nil
}

func (e *engine) finishCall(callID, reason string) {
	if callID == "" {
		return
	}
	e.mu.Lock()
	m := e.calls[callID]
	if m == nil || m.ended {
		e.mu.Unlock()
		return
	}
	m.ended = true
	// Source of truth: https://github.com/purpshell/meowcaller/blob/18618f30d0dc7a7bf822354d9a6c9264b275b221/datasheets/group-media-enc-rekey.md#L88-L89
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L122
	clearEngineCallKeyMaterial(m)
	m.applyGroupUpdate = nil
	m.applyGroupRekey = nil
	m.groupActivating = false
	m.groupActive = false
	call := m.call
	e.mu.Unlock()
	if call == nil || call.State() == CallPhaseEnded {
		return
	}
	call.setPhase(CallPhaseEnded)
	if fn := call.onEndFn(); fn != nil {
		fn(reason)
	}
}
