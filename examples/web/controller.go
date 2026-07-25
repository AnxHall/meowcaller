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
	Event       string `json:"event"`
	CallID      string `json:"call_id,omitempty"`
	Peer        string `json:"peer,omitempty"`
	Phase       int    `json:"phase,omitempty"`
	Video       bool   `json:"video,omitempty"`
	VideoState  int    `json:"video_state"`
	Orientation int    `json:"orientation,omitempty"`
	Message     string `json:"message,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	Sender      string `json:"sender,omitempty"`
	Removed     bool   `json:"removed,omitempty"`
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

type webCallController struct {
	ctx    context.Context
	client *meowcaller.Client
	bridge *videoBridge
	log    zerolog.Logger

	mu                        sync.Mutex
	call                      *meowcaller.Call
	pending                   *meowcaller.Call
	activeCallID              string
	inviteParticipants        func(context.Context, *meowcaller.Call, ...string) []error
	attachCall                func(*meowcaller.Call) error
	answerCall                func(*meowcaller.Call) error
	rejectCall                func(*meowcaller.Call) error
	pendingParticipantCallID  string
	pendingParticipantInvites map[string]string
	pendingParticipantOrder   []string
}

func newWebCallController(ctx context.Context, client *meowcaller.Client, bridge *videoBridge, log zerolog.Logger) *webCallController {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	c := &webCallController{
		ctx: ctx, client: client, bridge: bridge, log: log,
		inviteParticipants: func(ctx context.Context, call *meowcaller.Call, targets ...string) []error {
			return call.AddParticipants(ctx, targets...)
		},
		pendingParticipantInvites: make(map[string]string),
	}
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
	c.mu.Lock()
	if c.call != nil || c.pending != nil {
		c.mu.Unlock()
		_ = call.Reject()
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
	call.ReceiveVideo(meowcaller.VideoSinkFunc(c.bridge.WriteFrame))
	call.OnVideoKeyframeRequest(c.bridge.RequestKeyframe)
	call.OnPeerAccept(c.bridge.RequestKeyframe)
	call.OnReaction(func(reaction meowcaller.CallReaction) {
		c.publishReaction(webCallState{
			Event: "reaction", CallID: call.ID(), Peer: call.Peer().String(),
			Emoji: reaction.Emoji, Sender: reaction.Sender.String(), Removed: reaction.Removed,
		})
	})
	// Source of truth: https://github.com/purpshell/meowcaller/blob/8a22c339e92fa086d5d2d35569980af734d61c3e/datasheets/web-group-call-outcomes.md#L45-L66
	call.OnGroupState(func(state meowcaller.GroupCallState) {
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
			if c.bridge != nil {
				c.bridge.ClearGroupState(call.ID())
			}
		}
		c.mu.Unlock()
		c.publish(webCallState{Event: "ended", CallID: call.ID(), Peer: call.Peer().String(), Message: reason})
	})
	if err := wireMic(call); err != nil {
		return err
	}
	if err := wireSpeaker(call); err != nil {
		return err
	}
	return nil
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
	switch command.Action {
	case "dial_audio":
		return c.dial(command.Target, false)
	case "dial_video":
		return c.dial(command.Target, true)
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
	call, err := c.activeCall()
	if err != nil {
		return err
	}
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
	if len(normalized) == 0 {
		return errors.New("at least one participant target is required")
	}
	inviteParticipants := c.inviteParticipants
	if inviteParticipants == nil {
		inviteParticipants = func(ctx context.Context, call *meowcaller.Call, targets ...string) []error {
			return call.AddParticipants(ctx, targets...)
		}
	}
	c.mu.Lock()
	if c.pendingParticipantCallID != "" && c.pendingParticipantCallID != call.ID() {
		c.pendingParticipantInvites = make(map[string]string)
		c.pendingParticipantOrder = nil
	}
	c.pendingParticipantCallID = call.ID()
	if c.pendingParticipantInvites == nil {
		c.pendingParticipantInvites = make(map[string]string)
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
		if inviteErr != nil {
			c.mu.Lock()
			delete(c.pendingParticipantInvites, participantInviteTargetKey(target))
			c.mu.Unlock()
			result.Message = inviteErr.Error()
			c.log.Warn().Err(inviteErr).Str("call_id", call.ID()).Str("target", target).
				Msg("web participant invite failed")
		} else {
			c.log.Info().Str("call_id", call.ID()).Str("target", target).
				Msg("web participant invite submitted")
		}
		c.bridge.PublishEvent(result)
	}
	return nil
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
	c.bridge.PublishGroupState(webState)

	var joined []webParticipantJoin
	c.mu.Lock()
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
		var outcome *webParticipantJoin
		for _, key := range c.pendingParticipantOrder {
			target, pending := c.pendingParticipantInvites[key]
			if !pending || (key != participant.JID.User && key != participant.PN.User) {
				continue
			}
			delete(c.pendingParticipantInvites, key)
			if outcome == nil {
				next := webParticipantJoin{
					Event: "participant_join", CallID: callID,
					TransactionID: state.TransactionID, Target: target,
					Participant: participant.JID.String(), Device: selected.JID.String(),
					PID: selected.PID,
				}
				outcome = &next
			}
		}
		if outcome != nil {
			joined = append(joined, *outcome)
		}
	}
	c.mu.Unlock()
	for _, outcome := range joined {
		c.bridge.PublishEvent(outcome)
		c.log.Info().
			Str("call_id", callID).
			Uint32("transaction_id", outcome.TransactionID).
			Str("target", outcome.Target).
			Str("participant", outcome.Participant).
			Str("device", outcome.Device).
			Uint32("pid", outcome.PID).
			Msg("web participant joined")
	}
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
	if target == "" {
		return errors.New("target is required")
	}
	c.mu.Lock()
	busy := c.call != nil || c.pending != nil || c.activeCallID != ""
	c.mu.Unlock()
	if busy {
		return errors.New("another call is already active")
	}
	call, err := c.client.CallWithOptions(c.ctx, target, meowcaller.CallOptions{Video: video})
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.activeCallID = call.ID()
	c.mu.Unlock()
	if err = c.attach(call); err != nil {
		c.mu.Lock()
		if c.activeCallID == call.ID() {
			c.activeCallID = ""
		}
		c.mu.Unlock()
		_ = call.Hangup()
		return err
	}
	c.mu.Lock()
	c.call = call
	c.mu.Unlock()
	c.publish(webCallState{Event: "dialing", CallID: call.ID(), Peer: call.Peer().String(), Video: video})
	return nil
}

func (c *webCallController) answer() error {
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
	c.publish(webCallState{Event: "answering", CallID: call.ID(), Peer: call.Peer().String(), Video: call.IsVideo()})
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
