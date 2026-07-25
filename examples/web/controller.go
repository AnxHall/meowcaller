package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
)

const browserVideoFrameDuration = time.Second / 15

type webCallState struct {
	Event         string `json:"event"`
	CallID        string `json:"call_id,omitempty"`
	Peer          string `json:"peer,omitempty"`
	Phase         int    `json:"phase,omitempty"`
	Video         bool   `json:"video,omitempty"`
	VideoState    int    `json:"video_state"`
	Orientation   int    `json:"orientation,omitempty"`
	Message       string `json:"message,omitempty"`
	Emoji         string `json:"emoji,omitempty"`
	ParticipantID string `json:"participant_id,omitempty"`
	Sender        string `json:"sender,omitempty"`
	Device        string `json:"device,omitempty"`
	PID           uint32 `json:"pid,omitempty"`
	HasPID        bool   `json:"has_pid,omitempty"`
	Removed       bool   `json:"removed,omitempty"`
}

type webParticipantInviteResult struct {
	Event   string `json:"event"`
	CallID  string `json:"call_id"`
	Target  string `json:"target"`
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

type webGroupCallState struct {
	Event          string                    `json:"event"`
	CallID         string                    `json:"call_id"`
	TransactionID  uint32                    `json:"transaction_id"`
	RekeyRequested bool                      `json:"rekey_requested"`
	Participants   []webGroupCallParticipant `json:"participants"`
}

type webGroupCallParticipant struct {
	JID     string               `json:"jid"`
	PN      string               `json:"pn,omitempty"`
	State   string               `json:"state"`
	Devices []webGroupCallDevice `json:"devices"`
}

type webGroupCallDevice struct {
	JID      string `json:"jid"`
	Platform string `json:"platform,omitempty"`
	PID      uint32 `json:"pid"`
	HasPID   bool   `json:"has_pid"`
}

type webParticipantJoin struct {
	Event         string `json:"event"`
	CallID        string `json:"call_id"`
	TransactionID uint32 `json:"transaction_id"`
	Target        string `json:"target"`
	Participant   string `json:"participant"`
	Device        string `json:"device"`
	PID           uint32 `json:"pid"`
}

type pendingParticipantJoinCandidate struct {
	outcome webParticipantJoin
}

type webCallController struct {
	ctx    context.Context
	client *meowcaller.Client
	bridge *videoBridge
	log    zerolog.Logger

	participantInviteMu       sync.Mutex
	mu                        sync.Mutex
	call                      *meowcaller.Call
	pending                   *meowcaller.Call
	activeCallID              string
	dialCall                  func(context.Context, string, meowcaller.CallOptions) (*meowcaller.Call, error)
	startGroupCall            func(context.Context, []string, meowcaller.GroupCallOptions) (*meowcaller.Call, error)
	inviteParticipants        func(context.Context, *meowcaller.Call, ...string) []error
	callPhase                 func(*meowcaller.Call) meowcaller.CallPhase
	callVideo                 func(*meowcaller.Call) bool
	listenGroupState          func(*meowcaller.Call, func(meowcaller.GroupCallState))
	attachMedia               func(*meowcaller.Call) error
	attachCall                func(*meowcaller.Call) error
	answerCall                func(*meowcaller.Call) error
	rejectCall                func(*meowcaller.Call) error
	hangupCall                func(*meowcaller.Call) error
	beforeParticipantRegister func()
	beforeGroupStatePublish   func()
	pendingParticipantCallID  string
	pendingParticipantInvites map[string]string
	pendingParticipantOrder   []string
	participantInviteInFlight map[string]bool
	pendingParticipantJoins   map[string]*pendingParticipantJoinCandidate
}

func newWebCallController(ctx context.Context, client *meowcaller.Client, bridge *videoBridge, log zerolog.Logger) *webCallController {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	c := &webCallController{
		ctx: ctx, client: client, bridge: bridge, log: log,
		inviteParticipants: func(ctx context.Context, call *meowcaller.Call, targets ...string) []error {
			return call.AddParticipants(ctx, targets...)
		},
		callPhase: func(call *meowcaller.Call) meowcaller.CallPhase {
			return call.State()
		},
		callVideo: func(call *meowcaller.Call) bool {
			return call.IsVideo()
		},
		listenGroupState: func(call *meowcaller.Call, listener func(meowcaller.GroupCallState)) {
			call.OnGroupState(listener)
		},
		attachMedia: func(call *meowcaller.Call) error {
			if err := wireMic(call); err != nil {
				return err
			}
			return wireSpeaker(call)
		},
		hangupCall: func(call *meowcaller.Call) error {
			return call.Hangup()
		},
		pendingParticipantInvites: make(map[string]string),
		participantInviteInFlight: make(map[string]bool),
		pendingParticipantJoins:   make(map[string]*pendingParticipantJoinCandidate),
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L40-L120
	c.dialCall = client.CallWithOptions
	c.startGroupCall = client.GroupCallWithOptions
	bridge.OnControl(c.control)
	bridge.OnFrame(c.sendVideoFrame)
	client.OnIncomingCall(c.onIncomingCall)
	return c
}

func (c *webCallController) publish(state webCallState) {
	c.bridge.PublishState(state)
	c.log.Info().
		Str("event", state.Event).
		Str("call_id", state.CallID).
		Str("peer", state.Peer).
		Bool("video", state.Video).
		Int("video_state", state.VideoState).
		Str("message", state.Message).
		Msg("web call console state")
}

func (c *webCallController) publishReaction(state webCallState) {
	c.bridge.PublishEvent(state)
	c.log.Info().Str("event", state.Event).Str("call_id", state.CallID).
		Str("sender", state.Sender).Str("emoji", state.Emoji).Bool("removed", state.Removed).
		Msg("web call console reaction")
}

func (c *webCallController) onIncomingCall(call *meowcaller.Call) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L102-L120
	c.mu.Lock()
	if c.call != nil || c.pending != nil || c.activeCallID != "" {
		c.mu.Unlock()
		if c.rejectCall != nil {
			_ = c.rejectCall(call)
		} else {
			_ = call.Reject()
		}
		return
	}
	c.pending = call
	c.activeCallID = call.ID()
	c.mu.Unlock()
	c.publish(webCallState{
		Event: "incoming", CallID: call.ID(), Peer: call.Peer().String(), Video: call.IsVideo(),
	})
}

func (c *webCallController) attach(call *meowcaller.Call) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L40-L120
	call.OnParticipantVideoFrame(c.bridge.WriteParticipantFrame)
	call.OnVideoKeyframeRequest(c.bridge.RequestKeyframe)
	call.OnPeerAccept(c.bridge.RequestKeyframe)
	call.OnReaction(func(reaction meowcaller.CallReaction) {
		c.publishReaction(webCallState{
			Event: "reaction", CallID: call.ID(), Peer: call.Peer().String(),
			Emoji: reaction.Emoji, ParticipantID: reaction.ParticipantID,
			Sender: reaction.Sender.String(), Device: reaction.Device.String(),
			PID: reaction.PID, HasPID: reaction.HasPID, Removed: reaction.Removed,
		})
	})
	listenGroupState := c.listenGroupState
	if listenGroupState == nil {
		listenGroupState = func(call *meowcaller.Call, listener func(meowcaller.GroupCallState)) {
			call.OnGroupState(listener)
		}
	}
	listenGroupState(call, func(state meowcaller.GroupCallState) {
		c.handleGroupState(call.ID(), state)
	})
	call.OnVideoState(func(state meowcaller.VideoState) {
		c.bridge.SetOrientation(state.Orientation)
		c.publish(webCallState{
			Event: "video_state", CallID: call.ID(), Peer: call.Peer().String(),
			Video: call.IsVideo(), VideoState: state.Raw, Orientation: state.Orientation,
		})
	})
	call.OnReady(func() {
		c.publish(webCallState{Event: "ready", CallID: call.ID(), Peer: call.Peer().String(), Video: call.IsVideo()})
	})
	call.OnStateChange(func(phase meowcaller.CallPhase) {
		c.publish(webCallState{
			Event: "phase", CallID: call.ID(), Peer: call.Peer().String(), Phase: int(phase), Video: call.IsVideo(),
		})
	})
	call.OnEnd(func(reason string) {
		c.mu.Lock()
		if c.call == call {
			c.call = nil
		}
		if c.pending == call {
			c.pending = nil
		}
		if c.activeCallID == call.ID() {
			c.activeCallID = ""
			c.pendingParticipantCallID = ""
			c.pendingParticipantInvites = make(map[string]string)
			c.pendingParticipantOrder = nil
			c.participantInviteInFlight = make(map[string]bool)
			c.pendingParticipantJoins = make(map[string]*pendingParticipantJoinCandidate)
			if c.bridge != nil {
				c.bridge.ClearGroupState(call.ID())
			}
		}
		c.mu.Unlock()
		c.publish(webCallState{Event: "ended", CallID: call.ID(), Peer: call.Peer().String(), Message: reason})
	})
	if c.attachMedia != nil {
		return c.attachMedia(call)
	}
	if err := wireMic(call); err != nil {
		return err
	}
	return wireSpeaker(call)
}

func (c *webCallController) sendVideoFrame(accessUnit []byte) {
	c.mu.Lock()
	call := c.call
	c.mu.Unlock()
	if call == nil || !call.IsVideo() {
		return
	}
	if err := call.SendVideoWithDuration(accessUnit, browserVideoFrameDuration); err != nil {
		c.log.Debug().Err(err).Str("call_id", call.ID()).Msg("browser video frame was not sent")
	}
}

func (c *webCallController) control(command vbControl) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L40-L120
	switch command.Action {
	case "dial_audio":
		return c.dial(command.Target, false)
	case "dial_video":
		return c.dial(command.Target, true)
	case "start_group_audio":
		return c.startGroupAudio(command.Targets)
	case "start_group_video":
		return c.startGroupVideo(command.Targets)
	case "answer":
		return c.answer()
	case "reject":
		return c.reject()
	case "start_video":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.StartVideo()
	case "accept_video":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.AcceptVideo()
	case "stop_video":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.StopVideo()
	case "orientation":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.SetVideoOrientation(command.Orientation)
	case "reaction":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		if err = call.SendReaction(command.Emoji); err != nil {
			return err
		}
		c.publishReaction(webCallState{
			Event: "reaction", CallID: call.ID(), Peer: call.Peer().String(),
			Emoji: command.Emoji, Sender: "self", Removed: command.Emoji == "",
		})
		return nil
	case "add_participants":
		// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
		return c.addParticipants(command.Targets)
	case "hangup":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.Hangup()
	default:
		return fmt.Errorf("unknown action %q", command.Action)
	}
}

func (c *webCallController) addParticipants(targets []string) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	// Source of truth: https://github.com/purpshell/meowcaller/blob/2185887715c3ef6b2c0d76f14c8f13eab36aa224/datasheets/web-group-call-outcomes.md#L64-L66
	c.participantInviteMu.Lock()
	defer c.participantInviteMu.Unlock()
	call, err := c.activeCall()
	if err != nil {
		return err
	}
	callPhase := c.callPhase
	if callPhase == nil {
		callPhase = func(call *meowcaller.Call) meowcaller.CallPhase {
			return call.State()
		}
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L102-L120
	if callPhase(call) != meowcaller.CallPhaseActive {
		return errors.New("participant invites require an active call")
	}
	normalized := normalizeParticipantTargets(targets)
	if len(normalized) == 0 {
		return errors.New("at least one participant target is required")
	}
	if c.beforeParticipantRegister != nil {
		c.beforeParticipantRegister()
	}
	inviteParticipants := c.inviteParticipants
	if inviteParticipants == nil {
		inviteParticipants = func(ctx context.Context, call *meowcaller.Call, targets ...string) []error {
			return call.AddParticipants(ctx, targets...)
		}
	}
	c.mu.Lock()
	// Source of truth: https://github.com/purpshell/meowcaller/blob/2185887715c3ef6b2c0d76f14c8f13eab36aa224/datasheets/web-group-call-outcomes.md#L64-L66
	if c.call != call || c.activeCallID != call.ID() {
		c.mu.Unlock()
		return errors.New("active call changed before participant invite")
	}
	if c.pendingParticipantCallID != "" && c.pendingParticipantCallID != call.ID() {
		c.pendingParticipantInvites = make(map[string]string)
		c.pendingParticipantOrder = nil
		c.participantInviteInFlight = make(map[string]bool)
		c.pendingParticipantJoins = make(map[string]*pendingParticipantJoinCandidate)
	}
	c.pendingParticipantCallID = call.ID()
	if c.pendingParticipantInvites == nil {
		c.pendingParticipantInvites = make(map[string]string)
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9246e88b842f82bb35932542bdac2c0775e0108/datasheets/web-group-call-outcomes.md#L56-L62
	if c.participantInviteInFlight == nil {
		c.participantInviteInFlight = make(map[string]bool)
	}
	if c.pendingParticipantJoins == nil {
		c.pendingParticipantJoins = make(map[string]*pendingParticipantJoinCandidate)
	}
	for _, target := range normalized {
		key := participantInviteTargetKey(target)
		if key == "" {
			continue
		}
		if _, exists := c.pendingParticipantInvites[key]; !exists {
			c.pendingParticipantOrder = append(c.pendingParticipantOrder, key)
		}
		c.pendingParticipantInvites[key] = target
		c.participantInviteInFlight[key] = true
		delete(c.pendingParticipantJoins, key)
	}
	c.mu.Unlock()
	results := inviteParticipants(c.ctx, call, normalized...)
	for i, target := range normalized {
		var inviteErr error
		if i < len(results) {
			inviteErr = results[i]
		} else {
			inviteErr = errors.New("participant invite returned no result")
		}
		result := webParticipantInviteResult{
			Event: "participant_invite", CallID: call.ID(), Target: target,
			Success: inviteErr == nil,
		}
		// Source of truth: https://github.com/purpshell/meowcaller/blob/a9246e88b842f82bb35932542bdac2c0775e0108/datasheets/web-group-call-outcomes.md#L58-L70
		// Source of truth: https://github.com/purpshell/meowcaller/blob/2185887715c3ef6b2c0d76f14c8f13eab36aa224/datasheets/web-group-call-outcomes.md#L61-L66
		if inviteErr != nil {
			result.Message = inviteErr.Error()
		}
		key := participantInviteTargetKey(target)
		var joined *webParticipantJoin
		c.mu.Lock()
		if c.call != call || c.activeCallID != call.ID() || c.pendingParticipantCallID != call.ID() {
			c.mu.Unlock()
			continue
		}
		delete(c.participantInviteInFlight, key)
		candidate := c.pendingParticipantJoins[key]
		if inviteErr != nil {
			delete(c.pendingParticipantInvites, key)
			delete(c.pendingParticipantJoins, key)
		} else if candidate != nil {
			candidate.outcome.Target = target
			for candidateKey, pending := range c.pendingParticipantJoins {
				if pending != candidate {
					continue
				}
				delete(c.pendingParticipantJoins, candidateKey)
				delete(c.participantInviteInFlight, candidateKey)
				delete(c.pendingParticipantInvites, candidateKey)
			}
			outcome := candidate.outcome
			joined = &outcome
		}
		c.bridge.PublishEvent(result)
		if joined != nil {
			c.publishParticipantJoin(*joined)
		}
		c.mu.Unlock()
		if inviteErr != nil {
			c.log.Warn().Err(inviteErr).Str("call_id", call.ID()).Str("target", target).
				Msg("web participant invite failed")
		} else {
			c.log.Info().Str("call_id", call.ID()).Str("target", target).
				Msg("web participant invite submitted")
		}
	}
	return nil
}

func (c *webCallController) startGroupAudio(targets []string) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L56-L63
	return c.startGroupCallWithVideo(targets, false)
}

func (c *webCallController) startGroupVideo(targets []string) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L56-L63
	return c.startGroupCallWithVideo(targets, true)
}

func (c *webCallController) startGroupCallWithVideo(targets []string, video bool) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L40-L120
	normalized := normalizeParticipantTargets(targets)
	if len(normalized) < 2 {
		return errors.New("at least two distinct participant targets are required")
	}

	const reservation = "web-group-start-pending"
	c.mu.Lock()
	if c.call != nil || c.pending != nil || c.activeCallID != "" {
		c.mu.Unlock()
		return errors.New("another call is already active")
	}
	c.activeCallID = reservation
	c.mu.Unlock()

	startGroupCall := c.startGroupCall
	if startGroupCall == nil {
		if c.client == nil {
			c.clearCallStartReservation(reservation)
			return errors.New("group call signaling is unavailable")
		}
		startGroupCall = c.client.GroupCallWithOptions
	}
	call, err := startGroupCall(c.ctx, normalized, meowcaller.GroupCallOptions{Video: video})
	if err != nil {
		c.clearCallStartReservation(reservation)
		return err
	}
	if call == nil {
		c.clearCallStartReservation(reservation)
		return errors.New("group call returned no call")
	}

	c.mu.Lock()
	if c.activeCallID != reservation {
		c.mu.Unlock()
		_ = c.hangup(call)
		return errors.New("group call ownership changed while starting")
	}
	c.activeCallID = call.ID()
	c.mu.Unlock()

	attach := c.attach
	if c.attachCall != nil {
		attach = c.attachCall
	}
	if err = attach(call); err != nil {
		c.clearFailedCallStart(call)
		_ = c.hangup(call)
		return err
	}
	c.mu.Lock()
	if c.activeCallID != call.ID() {
		c.mu.Unlock()
		_ = c.hangup(call)
		return errors.New("group call ended while attaching")
	}
	c.call = call
	c.mu.Unlock()
	c.publish(webCallState{
		Event: "group_dialing", CallID: call.ID(), Peer: call.Peer().String(),
		Video: video,
	})
	return nil
}

func normalizeParticipantTargets(targets []string) []string {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L102-L120
	normalized := make([]string, 0, len(targets))
	seenTargets := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		key := participantInviteTargetKey(target)
		if target == "" || key == "" {
			continue
		}
		if _, exists := seenTargets[key]; exists {
			continue
		}
		seenTargets[key] = struct{}{}
		normalized = append(normalized, target)
	}
	return normalized
}

func (c *webCallController) clearCallStartReservation(reservation string) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L102-L120
	c.mu.Lock()
	if c.activeCallID == reservation {
		c.activeCallID = ""
	}
	c.mu.Unlock()
}

func (c *webCallController) clearFailedCallStart(call *meowcaller.Call) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L102-L120
	c.mu.Lock()
	if c.call == call {
		c.call = nil
	}
	if c.pending == call {
		c.pending = nil
	}
	if c.activeCallID == call.ID() {
		c.activeCallID = ""
		c.pendingParticipantCallID = ""
		c.pendingParticipantInvites = make(map[string]string)
		c.pendingParticipantOrder = nil
		c.participantInviteInFlight = make(map[string]bool)
		c.pendingParticipantJoins = make(map[string]*pendingParticipantJoinCandidate)
		if c.bridge != nil {
			c.bridge.ClearGroupState(call.ID())
		}
	}
	c.mu.Unlock()
}

func (c *webCallController) hangup(call *meowcaller.Call) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L102-L120
	if c.hangupCall != nil {
		return c.hangupCall(call)
	}
	return call.Hangup()
}

func participantInviteTargetKey(target string) string {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/8a22c339e92fa086d5d2d35569980af734d61c3e/datasheets/web-group-call-outcomes.md#L45-L62
	target = strings.TrimSpace(target)
	target = strings.TrimPrefix(target, "+")
	if at := strings.IndexByte(target, '@'); at >= 0 {
		target = target[:at]
	}
	if colon := strings.IndexByte(target, ':'); colon >= 0 {
		target = target[:colon]
	}
	return strings.TrimSpace(target)
}

func (c *webCallController) handleGroupState(callID string, state meowcaller.GroupCallState) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/8a22c339e92fa086d5d2d35569980af734d61c3e/datasheets/web-group-call-outcomes.md#L45-L72
	c.mu.Lock()
	current := c.activeCallID == callID
	c.mu.Unlock()
	if !current {
		return
	}
	webState := webGroupCallState{
		Event:          "group_state",
		CallID:         callID,
		TransactionID:  state.TransactionID,
		RekeyRequested: state.RekeyRequested,
		Participants:   make([]webGroupCallParticipant, len(state.Participants)),
	}
	for participantIndex, participant := range state.Participants {
		devices := make([]webGroupCallDevice, len(participant.Devices))
		for deviceIndex, device := range participant.Devices {
			devices[deviceIndex] = webGroupCallDevice{
				JID: device.JID.String(), Platform: device.Platform,
				PID: device.PID, HasPID: device.HasPID,
			}
		}
		webState.Participants[participantIndex] = webGroupCallParticipant{
			JID: participant.JID.String(), PN: participant.PN.String(),
			State: participant.State, Devices: devices,
		}
	}
	if c.beforeGroupStatePublish != nil {
		c.beforeGroupStatePublish()
	}
	var joined []webParticipantJoin
	c.mu.Lock()
	if c.activeCallID != callID {
		c.mu.Unlock()
		return
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/2185887715c3ef6b2c0d76f14c8f13eab36aa224/datasheets/web-group-call-outcomes.md#L78-L82
	c.bridge.PublishGroupState(webState)
	if c.pendingParticipantCallID != callID {
		c.mu.Unlock()
		return
	}
	for _, participant := range state.Participants {
		if participant.State != "connected" {
			continue
		}
		var selected *meowcaller.GroupCallDevice
		for deviceIndex := range participant.Devices {
			if participant.Devices[deviceIndex].HasPID {
				selected = &participant.Devices[deviceIndex]
				break
			}
		}
		if selected == nil {
			continue
		}
		// Source of truth: https://github.com/purpshell/meowcaller/blob/a9246e88b842f82bb35932542bdac2c0775e0108/datasheets/web-group-call-outcomes.md#L58-L70
		var matchedKeys []string
		var target string
		for _, key := range c.pendingParticipantOrder {
			pendingTarget, pending := c.pendingParticipantInvites[key]
			if !pending || (key != participant.JID.User && key != participant.PN.User) {
				continue
			}
			matchedKeys = append(matchedKeys, key)
			if target == "" {
				target = pendingTarget
			}
		}
		if len(matchedKeys) == 0 {
			continue
		}
		outcome := webParticipantJoin{
			Event: "participant_join", CallID: callID,
			TransactionID: state.TransactionID, Target: target,
			Participant: participant.JID.String(), Device: selected.JID.String(),
			PID: selected.PID,
		}
		allInFlight := true
		for _, key := range matchedKeys {
			if !c.participantInviteInFlight[key] {
				allInFlight = false
				break
			}
		}
		if allInFlight {
			candidate := &pendingParticipantJoinCandidate{outcome: outcome}
			for _, key := range matchedKeys {
				c.pendingParticipantJoins[key] = candidate
			}
			continue
		}
		for _, key := range matchedKeys {
			delete(c.pendingParticipantInvites, key)
			delete(c.participantInviteInFlight, key)
			delete(c.pendingParticipantJoins, key)
		}
		joined = append(joined, outcome)
	}
	for _, outcome := range joined {
		c.publishParticipantJoin(outcome)
	}
	c.mu.Unlock()
}

func (c *webCallController) publishParticipantJoin(outcome webParticipantJoin) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9246e88b842f82bb35932542bdac2c0775e0108/datasheets/web-group-call-outcomes.md#L60-L70
	c.bridge.PublishEvent(outcome)
	c.log.Info().
		Str("call_id", outcome.CallID).
		Uint32("transaction_id", outcome.TransactionID).
		Str("target", outcome.Target).
		Str("participant", outcome.Participant).
		Str("device", outcome.Device).
		Uint32("pid", outcome.PID).
		Msg("web participant joined")
}

func (c *webCallController) activeCall() (*meowcaller.Call, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.call == nil {
		return nil, errors.New("no active call")
	}
	return c.call, nil
}

func (c *webCallController) dial(target string, video bool) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L109-L120
	if target == "" {
		return errors.New("target is required")
	}
	const reservation = "web-direct-start-pending"
	c.mu.Lock()
	if c.call != nil || c.pending != nil || c.activeCallID != "" {
		c.mu.Unlock()
		return errors.New("another call is already active")
	}
	c.activeCallID = reservation
	c.mu.Unlock()

	dialCall := c.dialCall
	if dialCall == nil {
		if c.client == nil {
			c.clearCallStartReservation(reservation)
			return errors.New("call signaling is unavailable")
		}
		dialCall = c.client.CallWithOptions
	}
	call, err := dialCall(c.ctx, target, meowcaller.CallOptions{Video: video})
	if err != nil {
		c.clearCallStartReservation(reservation)
		return err
	}
	if call == nil {
		c.clearCallStartReservation(reservation)
		return errors.New("direct call returned no call")
	}

	c.mu.Lock()
	if c.activeCallID != reservation {
		c.mu.Unlock()
		_ = c.hangup(call)
		return errors.New("call ownership changed while dialing")
	}
	c.activeCallID = call.ID()
	c.mu.Unlock()

	attach := c.attach
	if c.attachCall != nil {
		attach = c.attachCall
	}
	if err = attach(call); err != nil {
		c.clearFailedCallStart(call)
		_ = c.hangup(call)
		return err
	}
	c.mu.Lock()
	if c.activeCallID != call.ID() {
		c.mu.Unlock()
		_ = c.hangup(call)
		return errors.New("call ended while attaching")
	}
	c.call = call
	c.mu.Unlock()
	c.publish(webCallState{Event: "dialing", CallID: call.ID(), Peer: call.Peer().String(), Video: video})
	return nil
}

func (c *webCallController) answer() error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/f62ccfb2a431fc25008423954287fd3009fed161/datasheets/web-initial-group-call.md#L40-L120
	c.mu.Lock()
	call := c.pending
	c.mu.Unlock()
	if call == nil {
		return errors.New("no incoming call")
	}
	attach := c.attach
	if c.attachCall != nil {
		attach = c.attachCall
	}
	reject := call.Reject
	if c.rejectCall != nil {
		reject = func() error { return c.rejectCall(call) }
	}
	if err := attach(call); err != nil {
		_ = reject()
		c.clearFailedIncoming(call)
		return err
	}
	answer := call.Answer
	if c.answerCall != nil {
		answer = func() error { return c.answerCall(call) }
	}
	if err := answer(); err != nil {
		_ = reject()
		c.clearFailedIncoming(call)
		return err
	}
	c.mu.Lock()
	if c.pending != call || c.activeCallID != call.ID() {
		c.mu.Unlock()
		_ = reject()
		return errors.New("incoming call ended while answering")
	}
	c.pending = nil
	c.call = call
	c.mu.Unlock()
	var video bool
	if c.callVideo != nil {
		video = c.callVideo(call)
	} else {
		video = call.IsVideo()
	}
	c.publish(webCallState{Event: "answering", CallID: call.ID(), Peer: call.Peer().String(), Video: video})
	return nil
}

func (c *webCallController) clearFailedIncoming(call *meowcaller.Call) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/8a22c339e92fa086d5d2d35569980af734d61c3e/datasheets/web-group-call-outcomes.md#L51-L66
	c.mu.Lock()
	if c.pending == call {
		c.pending = nil
	}
	if c.call == call {
		c.call = nil
	}
	if c.activeCallID == call.ID() {
		c.activeCallID = ""
		c.pendingParticipantCallID = ""
		c.pendingParticipantInvites = make(map[string]string)
		c.pendingParticipantOrder = nil
		c.participantInviteInFlight = make(map[string]bool)
		c.pendingParticipantJoins = make(map[string]*pendingParticipantJoinCandidate)
		if c.bridge != nil {
			c.bridge.ClearGroupState(call.ID())
		}
	}
	c.mu.Unlock()
}

func (c *webCallController) reject() error {
	c.mu.Lock()
	call := c.pending
	c.pending = nil
	if call != nil && c.activeCallID == call.ID() {
		c.activeCallID = ""
		c.pendingParticipantCallID = ""
		c.pendingParticipantInvites = make(map[string]string)
		c.pendingParticipantOrder = nil
		c.participantInviteInFlight = make(map[string]bool)
		c.pendingParticipantJoins = make(map[string]*pendingParticipantJoinCandidate)
		if c.bridge != nil {
			c.bridge.ClearGroupState(call.ID())
		}
	}
	c.mu.Unlock()
	if call == nil {
		return errors.New("no incoming call")
	}
	return call.Reject()
}

func runWebConsole(ctx context.Context, rec *diag.Recorder) error {
	bridge, err := newVideoBridge(*zerolog.Ctx(ctx))
	if err != nil {
		return err
	}
	defer bridge.Close()
	zerolog.Ctx(ctx).Info().Str("url", bridge.URL()).Msg("web call console ready")
	wa, client, err := connectManagedClient(ctx, rec, func(code string, validFor time.Duration) {
		if err := bridge.SetQRCode(code); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to render pairing QR")
			return
		}
		bridge.PublishState(webCallState{Event: "pairing", Message: validFor.Round(time.Second).String()})
	})
	if err != nil {
		return err
	}
	defer wa.Disconnect()
	newWebCallController(ctx, client, bridge, *zerolog.Ctx(ctx))
	bridge.PublishState(webCallState{Event: "idle", Message: "connected"})
	<-ctx.Done()
	return nil
}
