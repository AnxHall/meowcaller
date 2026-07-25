package meowcaller

import (
	"context"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Call is one live direct or group call. A direct call may become an ad-hoc
// group call through AddParticipant. Place one with Client.Call or Client.GroupCall, or receive one
// (unanswered) in an OnIncomingCall listener. Attach outbound audio with
// Subscribe/Play and inbound audio with Receive, and lifecycle listeners with
// OnReady/OnEnd/OnStateChange. All methods are safe for concurrent use.
type Call struct {
	eng  *engine
	id   string
	peer types.JID

	mu                      sync.Mutex
	phase                   CallPhase
	player                  *Player
	sink                    AudioSink
	onReady                 func()
	onEnd                   func(reason string)
	onState                 func(CallPhase)
	onPeerAccept            func()
	peerAccepted            bool
	acceptNotified          bool
	onMuteState             func(muted bool)
	videoSink               VideoSink
	onVideoState            func(VideoState)
	onVideoKeyframeRequest  func()
	onParticipantVideoFrame func(ParticipantVideoFrame)
	onReaction              func(CallReaction)
	groupState              *GroupCallState
	onGroupState            func(GroupCallState)
	groupCallbackGeneration uint64
}

// GroupCallState is a sanitized group-call roster. Transaction zero may contain
// selected outgoing targets before the first authoritative server transaction.
type GroupCallState struct {
	TransactionID  uint32
	RekeyRequested bool
	Participants   []GroupCallParticipant
}

// GroupCallParticipant is one participant in an authoritative group-call roster.
type GroupCallParticipant struct {
	JID     types.JID
	PN      types.JID
	State   string
	Devices []GroupCallDevice
}

// GroupCallDevice is one participant device selected or considered by WhatsApp.
type GroupCallDevice struct {
	JID      types.JID
	Platform string
	PID      uint32
	HasPID   bool
}

// CallReaction is a transient emoji reaction received over the call's RTC app-data stream.
// An empty Emoji with Removed set means the sender cleared their reaction.
type CallReaction struct {
	Emoji         string
	ParticipantID string
	Sender        types.JID
	Device        types.JID
	PID           uint32
	HasPID        bool
	Removed       bool
}

// ID returns the call-id (32 uppercase hex chars).
func (c *Call) ID() string { return c.id }

// Peer returns the remote party's LID.
func (c *Call) Peer() types.JID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peer
}

// GroupState returns the latest group-call roster, if this is a group call.
func (c *Call) GroupState() (GroupCallState, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a95ab63017f996313ca7e4cfbbfb96fead1717a7/datasheets/api-group-call-state.md#L28-L72
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.groupState == nil {
		return GroupCallState{}, false
	}
	return cloneGroupCallState(*c.groupState), true
}

// OnGroupState registers a callback for group-call roster state. The latest
// cached state, including a selected-target seed, is replayed immediately.
func (c *Call) OnGroupState(fn func(GroupCallState)) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a95ab63017f996313ca7e4cfbbfb96fead1717a7/datasheets/api-group-call-state.md#L28-L72
	c.mu.Lock()
	c.onGroupState = fn
	c.groupCallbackGeneration++
	generation := c.groupCallbackGeneration
	var replay *GroupCallState
	if fn != nil && c.groupState != nil {
		state := cloneGroupCallState(*c.groupState)
		replay = &state
	}
	c.mu.Unlock()
	if replay == nil {
		return
	}
	c.mu.Lock()
	current := c.onGroupState != nil &&
		c.groupCallbackGeneration == generation &&
		c.groupState != nil &&
		c.groupState.TransactionID == replay.TransactionID
	c.mu.Unlock()
	if current {
		fn(*replay)
	}
}

func (c *Call) setGroupState(state GroupCallState) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a95ab63017f996313ca7e4cfbbfb96fead1717a7/datasheets/api-group-call-state.md#L58-L72
	stored := cloneGroupCallState(state)
	c.mu.Lock()
	if c.groupState != nil && state.TransactionID <= c.groupState.TransactionID {
		c.mu.Unlock()
		return
	}
	c.groupState = &stored
	fn := c.onGroupState
	generation := c.groupCallbackGeneration
	c.mu.Unlock()
	if fn == nil {
		return
	}
	c.mu.Lock()
	current := c.onGroupState != nil &&
		c.groupCallbackGeneration == generation &&
		c.groupState != nil &&
		c.groupState.TransactionID == stored.TransactionID
	c.mu.Unlock()
	if current {
		fn(cloneGroupCallState(stored))
	}
}

func (c *Call) setGroupStateAfterRejected(
	rejectedTransactionID uint32,
	state GroupCallState,
) bool {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L119-L122
	stored := cloneGroupCallState(state)
	c.mu.Lock()
	if c.groupState == nil ||
		c.groupState.TransactionID != rejectedTransactionID {
		c.mu.Unlock()
		return false
	}
	c.groupState = &stored
	fn := c.onGroupState
	generation := c.groupCallbackGeneration
	c.mu.Unlock()
	if fn == nil {
		return true
	}
	c.mu.Lock()
	current := c.onGroupState != nil &&
		c.groupCallbackGeneration == generation &&
		c.groupState != nil &&
		c.groupState.TransactionID == stored.TransactionID
	c.mu.Unlock()
	if current {
		fn(cloneGroupCallState(stored))
	}
	return true
}

func groupCallStateFromUpdate(update types.GroupCallUpdate) GroupCallState {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a95ab63017f996313ca7e4cfbbfb96fead1717a7/datasheets/api-group-call-state.md#L58-L68
	state := GroupCallState{
		TransactionID:  update.TransactionID,
		RekeyRequested: update.RekeyRequested,
		Participants:   make([]GroupCallParticipant, len(update.Participants)),
	}
	for participantIndex, participant := range update.Participants {
		devices := make([]GroupCallDevice, len(participant.Devices))
		for deviceIndex, device := range participant.Devices {
			devices[deviceIndex] = GroupCallDevice{
				JID:      device.JID,
				Platform: device.Platform,
				PID:      device.PID,
				HasPID:   device.HasPID,
			}
		}
		state.Participants[participantIndex] = GroupCallParticipant{
			JID: participant.JID, PN: participant.PN, State: participant.State,
			Devices: devices,
		}
	}
	return state
}

func cloneGroupCallState(state GroupCallState) GroupCallState {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a95ab63017f996313ca7e4cfbbfb96fead1717a7/datasheets/api-group-call-state.md#L58-L72
	clone := state
	clone.Participants = make([]GroupCallParticipant, len(state.Participants))
	for participantIndex, participant := range state.Participants {
		clone.Participants[participantIndex] = participant
		clone.Participants[participantIndex].Devices = append([]GroupCallDevice(nil), participant.Devices...)
	}
	return clone
}

func (c *Call) setPeer(peer types.JID) {
	if peer.IsEmpty() {
		return
	}
	c.mu.Lock()
	c.peer = peer
	c.mu.Unlock()
}

// State returns the call's current phase.
func (c *Call) State() CallPhase {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase
}

// IsVideo reports whether either direction of the call currently has video.
func (c *Call) IsVideo() bool {
	return c.eng.callIsVideo(c.id)
}

// IsSendingVideo reports whether this client currently owns an active or pending
// outbound video flow.
func (c *Call) IsSendingVideo() bool {
	return c.eng.callIsSendingVideo(c.id)
}

// IsReceivingVideo reports whether the peer currently owns an active inbound video flow.
func (c *Call) IsReceivingVideo() bool {
	return c.eng.callIsReceivingVideo(c.id)
}

// Answer accepts an inbound call (preaccept + accept) and brings media up. No-op error
// if the call is not in a ringing state.
func (c *Call) Answer() error { return c.eng.answer(c) }

// Reject declines an inbound call.
func (c *Call) Reject() error { return c.eng.reject(c) }

// Hangup ends the call (either direction) and tears down media.
func (c *Call) Hangup() error { return c.eng.hangup(c) }

// StartVideo requests an audio-to-video upgrade. Outbound video remains gated until
// the peer acknowledges the transition with state 4 or state 1.
func (c *Call) StartVideo() error {
	return c.eng.transitionVideo(c.id, types.CallVideoStateUpgradeRequestV2)
}

// AcceptVideo accepts a pending peer video upgrade without changing this client's
// independent outbound video state.
func (c *Call) AcceptVideo() error {
	return c.eng.transitionVideo(c.id, types.CallVideoStateUpgradeAccept)
}

// StopVideo stops this client's outbound video while preserving peer video and audio.
func (c *Call) StopVideo() error {
	return c.eng.transitionVideo(c.id, types.CallVideoStateStopped)
}

// SetVideoEnabled mutes or unmutes this client's outbound camera flow with state 0/1.
// It does not change whether the peer's video is received.
func (c *Call) SetVideoEnabled(enabled bool) error {
	return c.eng.setVideoEnabled(c.id, enabled)
}

// SetVideoOrientation announces the local camera rotation as quarter turns clockwise.
func (c *Call) SetVideoOrientation(orientation int) error {
	return c.eng.setVideoOrientation(c.id, orientation)
}

// SetMuted announces this client's microphone mute state to the peer.
func (c *Call) SetMuted(muted bool) error {
	return c.eng.setMuted(c.id, muted)
}

// AddParticipant invites one person to this established call.
func (c *Call) AddParticipant(ctx context.Context, target string) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/160912971e6bc2a4aa79ac3aafcf08360075e3fc/datasheets/api-group-participant-invite.md#L23-L100
	return c.eng.inviteParticipant(ctx, c.id, target)
}

// AddParticipants invites each person independently and returns index-aligned results.
func (c *Call) AddParticipants(ctx context.Context, targets ...string) []error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/160912971e6bc2a4aa79ac3aafcf08360075e3fc/datasheets/api-group-participant-invite.md#L23-L100
	results := make([]error, len(targets))
	for i, target := range targets {
		results[i] = c.AddParticipant(ctx, target)
	}
	return results
}

// SendReaction sends an emoji over this call's RTC app-data stream. Pass an empty
// string to clear the reaction previously sent by this client.
func (c *Call) SendReaction(emoji string) error {
	return c.eng.sendReaction(c.id, emoji)
}

// Subscribe attaches p as the call's outbound audio player, replacing any previous one.
// While the player is Playing, its source frames are encoded and sent to the peer;
// otherwise silence is sent (the call must keep sending to hold the relay bridge).
func (c *Call) Subscribe(p *Player) {
	c.mu.Lock()
	c.player = p
	c.mu.Unlock()
}

// Play is a shortcut: it creates a Player, subscribes it, starts src, and returns the
// Player (use it for Pause/Stop/OnFinish).
func (c *Call) Play(src AudioSource) *Player {
	p := NewPlayer()
	c.Subscribe(p)
	p.Play(src)
	return p
}

// Receive attaches a sink for the peer's decoded audio (16 kHz mono frames), replacing
// any previous one. Without a sink the inbound audio is decoded and discarded.
func (c *Call) Receive(sink AudioSink) {
	c.mu.Lock()
	c.sink = sink
	c.mu.Unlock()
}

// ReceiveVideo attaches a sink for the peer's H.264 video, delivered as Annex-B access units
// (one per frame, reassembled on the RTP marker), replacing any previous one. Without a sink
// the inbound video is discarded. The video analog of Receive; AnnexBRecorder records to a
// .h264 file, or use VideoSinkFunc to forward to a callback.
//
// NOT VALIDATED: the inbound-video media path is unproven (no captured video-RTP vector).
func (c *Call) ReceiveVideo(sink VideoSink) {
	c.mu.Lock()
	c.videoSink = sink
	c.mu.Unlock()
}

// videoSinkRef returns the Call's current video sink under its lock.
func (c *Call) videoSinkRef() VideoSink {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.videoSink
}

// OnVideoState registers a callback fired for each inbound <video> state stanza — the peer's
// video on/off, the audio→video upgrade, and device orientation (rotate by Orientation × 90°).
func (c *Call) OnVideoState(fn func(VideoState)) {
	c.mu.Lock()
	c.onVideoState = fn
	c.mu.Unlock()
}

// OnVideoKeyframeRequest registers a callback for authenticated WhatsApp PLI/FIR
// feedback. Encoded video sources should make their next access unit an IDR.
func (c *Call) OnVideoKeyframeRequest(fn func()) {
	c.mu.Lock()
	c.onVideoKeyframeRequest = fn
	c.mu.Unlock()
}

// OnParticipantVideoFrame registers a callback for authenticated H.264 access
// units with their group participant identity. It is additive to ReceiveVideo.
func (c *Call) OnParticipantVideoFrame(fn func(ParticipantVideoFrame)) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L32-L54
	c.mu.Lock()
	c.onParticipantVideoFrame = fn
	c.mu.Unlock()
}

func (c *Call) dispatchParticipantVideoFrame(frame ParticipantVideoFrame) bool {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L32-L54
	c.mu.Lock()
	fn := c.onParticipantVideoFrame
	c.mu.Unlock()
	if fn == nil {
		return false
	}
	fn(cloneParticipantVideoFrame(frame))
	return true
}

// OnReaction registers a callback for emoji reactions targeting this call.
func (c *Call) OnReaction(fn func(CallReaction)) {
	c.mu.Lock()
	c.onReaction = fn
	c.mu.Unlock()
}

func (c *Call) onReactionFn() func(CallReaction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onReaction
}

func (c *Call) requestVideoKeyframe() {
	c.mu.Lock()
	fn := c.onVideoKeyframeRequest
	c.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// onVideoStateFn returns the Call's video-state callback under its lock.
func (c *Call) onVideoStateFn() func(VideoState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onVideoState
}

// SendVideo sends one already-encoded H.264 access unit (Annex-B) to the peer — fed from an
// external encoder (browser WebCodecs, ffmpeg, hardware). Returns an error if the call has no
// active video media yet. meowcaller does not encode pixels (no pure-Go H.264 encoder); this
// is the video analog of writing a sample to a track.
//
// NOT VALIDATED: the video send media path is unproven.
func (c *Call) SendVideo(accessUnit []byte) error {
	return c.SendVideoWithDuration(accessUnit, 0)
}

// SendVideoWithDuration sends one already-encoded H.264 access unit and advances the
// outgoing RTP timestamp by duration for the next frame. A zero duration uses the sender
// fallback frame step.
func (c *Call) SendVideoWithDuration(accessUnit []byte, duration time.Duration) error {
	return c.eng.sendVideoFrame(c.id, accessUnit, duration)
}

// OnReady registers a callback fired once media is flowing (relay bound, first frames
// exchanged).
func (c *Call) OnReady(fn func()) {
	c.mu.Lock()
	c.onReady = fn
	c.mu.Unlock()
}

// OnEnd registers a callback fired when the call ends, with a short reason string.
func (c *Call) OnEnd(fn func(reason string)) {
	c.mu.Lock()
	c.onEnd = fn
	c.mu.Unlock()
}

// OnStateChange registers a callback fired on each phase transition.
func (c *Call) OnStateChange(fn func(CallPhase)) {
	c.mu.Lock()
	c.onState = fn
	c.mu.Unlock()
}

// OnPeerAccept registers a one-shot callback for the remote peer accepting an outgoing
// call. If acceptance arrived before registration, the callback is invoked immediately.
func (c *Call) OnPeerAccept(fn func()) {
	c.mu.Lock()
	c.onPeerAccept = fn
	shouldNotify := c.peerAccepted && !c.acceptNotified && fn != nil
	if shouldNotify {
		c.acceptNotified = true
	}
	c.mu.Unlock()
	if shouldNotify {
		fn()
	}
}

func (c *Call) markPeerAccepted() {
	c.mu.Lock()
	c.peerAccepted = true
	fn := c.onPeerAccept
	shouldNotify := !c.acceptNotified && fn != nil
	if shouldNotify {
		c.acceptNotified = true
	}
	c.mu.Unlock()
	if shouldNotify {
		fn()
	}
}

// OnMuteState registers a callback fired for each inbound WhatsApp mute_v2 state.
// The callback describes the remote party's microphone state: true means muted.
func (c *Call) OnMuteState(fn func(muted bool)) {
	c.mu.Lock()
	c.onMuteState = fn
	c.mu.Unlock()
}

// onMuteStateFn returns the Call's remote mute-state callback under its lock.
func (c *Call) onMuteStateFn() func(bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.onMuteState
}

// setPhase advances the call's phase and fires OnStateChange (used by the engine).
func (c *Call) setPhase(next CallPhase) {
	c.mu.Lock()
	if c.phase == next {
		c.mu.Unlock()
		return
	}
	c.phase = next
	fn := c.onState
	c.mu.Unlock()
	if fn != nil {
		fn(next)
	}
}
