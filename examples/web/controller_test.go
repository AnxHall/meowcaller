package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
)

func TestWebCallStatePreservesDisabledVideoState(t *testing.T) {
	data, err := json.Marshal(webCallState{Event: "video_state", VideoState: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"video_state":0`) {
		t.Fatalf("disabled video state was omitted: %s", data)
	}
}

func TestVideoBridgePageHidesPeerFramesWhileRemoteVideoIsOff(t *testing.T) {
	for _, behavior := range []string{
		"setRemoteVideoActive(false)",
		"setRemoteVideoActive(true)",
		"if(remoteVideoActive)",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
}

func TestWebCallStateIncludesReactionEmoji(t *testing.T) {
	data, err := json.Marshal(webCallState{Event: "reaction", Emoji: "👍"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"emoji":"👍"`) {
		t.Fatalf("reaction emoji was omitted: %s", data)
	}
}

func TestVideoBridgePageDisplaysIncomingReactions(t *testing.T) {
	for _, behavior := range []string{
		`id="reactions"`,
		"showReaction(s.emoji,s.sender)",
		"s.event==='reaction'",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
}

func TestVideoBridgePageSendsCallReactions(t *testing.T) {
	for _, behavior := range []string{
		"data-reaction=",
		"invoke('reaction',{emoji:b.dataset.reaction})",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
}

func TestVideoBridgePageRotatesPixelsInsideStableStage(t *testing.T) {
	for _, behavior := range []string{
		"function drawRemoteFrame(f)",
		"remote.width=portrait?h:w",
		"paint.rotate(Math.PI/2)",
		"remoteOrientation=+e.data||0",
		".remote-wrap,.local-wrap",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
	if strings.Contains(videoBridgePage, "remote.style.transform") {
		t.Fatal("page still rotates the canvas element and can break the layout")
	}
}

func TestVideoBridgePageUsesCapturedFrameDimensions(t *testing.T) {
	for _, behavior := range []string{
		"const size=avcLevel31Size(f.displayWidth,f.displayHeight)",
		"size.width!==encodedWidth||size.height!==encodedHeight",
		"width:size.width,height:size.height",
		"forceKeyframe=true",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
	if strings.Contains(videoBridgePage, "width:640,height:480") {
		t.Fatal("page still hardcodes the encoder to 640x480")
	}
}

func TestWebParticipantInviteResultPreservesFalseSuccess(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	data, err := json.Marshal(webParticipantInviteResult{
		Event: "participant_invite", Target: "15551234567", Success: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"success":false`) {
		t.Fatalf("failed invite success was omitted: %s", data)
	}
}

func TestWebCallControllerPublishesJoinOnlyForConnectedPIDDevice(t *testing.T) {
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 8)
	bridge.subs[events] = struct{}{}
	c := &webCallController{
		bridge:                   bridge,
		log:                      zerolog.Nop(),
		activeCallID:             "CID",
		pendingParticipantCallID: "CID",
		pendingParticipantInvites: map[string]string{
			"15550002": "+15550002",
		},
		pendingParticipantOrder: []string{"15550002"},
	}
	participant := meowcaller.GroupCallParticipant{
		JID:   types.NewJID("222222222222222", types.HiddenUserServer),
		PN:    types.NewJID("15550002", types.DefaultUserServer),
		State: "receipt",
		Devices: []meowcaller.GroupCallDevice{{
			JID: types.NewJID("222222222222222", types.HiddenUserServer),
		}},
	}
	c.handleGroupState("CID", meowcaller.GroupCallState{
		TransactionID: 17, Participants: []meowcaller.GroupCallParticipant{participant},
	})
	first := <-events
	var receiptState webGroupCallState
	if err := json.Unmarshal(first.data, &receiptState); err != nil {
		t.Fatalf("receipt state JSON: %v", err)
	}
	if receiptState.Event != "group_state" || receiptState.Participants[0].State != "receipt" {
		t.Fatalf("receipt group state = %+v", receiptState)
	}
	select {
	case unexpected := <-events:
		t.Fatalf("receipt snapshot published join: %s", unexpected.data)
	default:
	}

	participant.State = "connected"
	participant.Devices[0].HasPID = true
	participant.Devices[0].PID = 0
	c.handleGroupState("CID", meowcaller.GroupCallState{
		TransactionID: 18, RekeyRequested: true,
		Participants: []meowcaller.GroupCallParticipant{participant},
	})
	_ = <-events
	joined := <-events
	var outcome webParticipantJoin
	if err := json.Unmarshal(joined.data, &outcome); err != nil {
		t.Fatalf("participant join JSON: %v", err)
	}
	if outcome.Event != "participant_join" || outcome.Target != "+15550002" ||
		outcome.Participant != participant.JID.String() ||
		outcome.Device != participant.Devices[0].JID.String() ||
		outcome.PID != 0 ||
		outcome.TransactionID != 18 {
		t.Fatalf("participant join = %+v", outcome)
	}
	c.handleGroupState("CID", meowcaller.GroupCallState{
		TransactionID: 19, Participants: []meowcaller.GroupCallParticipant{participant},
	})
	_ = <-events
	select {
	case unexpected := <-events:
		t.Fatalf("connected target joined more than once: %s", unexpected.data)
	default:
	}
}

func TestWebCallControllerIgnoresStaleGroupStateFromOldCall(t *testing.T) {
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 2)
	bridge.subs[events] = struct{}{}
	c := &webCallController{
		bridge: bridge, log: zerolog.Nop(), activeCallID: "NEW",
		pendingParticipantCallID: "NEW",
		pendingParticipantInvites: map[string]string{
			"15550002": "+15550002",
		},
		pendingParticipantOrder: []string{"15550002"},
	}
	c.handleGroupState("OLD", meowcaller.GroupCallState{
		TransactionID: 99,
		Participants: []meowcaller.GroupCallParticipant{{
			JID: types.NewJID("222222222222222", types.HiddenUserServer),
			PN:  types.NewJID("15550002", types.DefaultUserServer), State: "connected",
			Devices: []meowcaller.GroupCallDevice{{
				JID: types.NewJID("222222222222222", types.HiddenUserServer),
				PID: 2, HasPID: true,
			}},
		}},
	})
	if got := c.pendingParticipantInvites["15550002"]; got != "+15550002" {
		t.Fatalf("stale callback consumed new-call invite: %q", got)
	}
	select {
	case msg := <-events:
		t.Fatalf("stale callback published event: %s", msg.data)
	default:
	}
}

func TestWebCallControllerCorrelatesParticipantAliasesOnce(t *testing.T) {
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 3)
	bridge.subs[events] = struct{}{}
	c := &webCallController{
		bridge: bridge, log: zerolog.Nop(), activeCallID: "CID",
		pendingParticipantCallID: "CID",
		pendingParticipantInvites: map[string]string{
			"222222222222222": "222222222222222@lid",
			"15550002":        "+15550002",
		},
		pendingParticipantOrder: []string{"222222222222222", "15550002"},
	}
	c.handleGroupState("CID", meowcaller.GroupCallState{
		TransactionID: 20,
		Participants: []meowcaller.GroupCallParticipant{{
			JID: types.NewJID("222222222222222", types.HiddenUserServer),
			PN:  types.NewJID("15550002", types.DefaultUserServer), State: "connected",
			Devices: []meowcaller.GroupCallDevice{{
				JID: types.NewJID("222222222222222", types.HiddenUserServer),
				PID: 2, HasPID: true,
			}},
		}},
	})
	_ = <-events
	var joins int
	for {
		select {
		case msg := <-events:
			var event webParticipantJoin
			if err := json.Unmarshal(msg.data, &event); err != nil {
				t.Fatal(err)
			}
			if event.Event == "participant_join" {
				joins++
			}
		default:
			if joins != 1 {
				t.Fatalf("join events = %d, want 1", joins)
			}
			if len(c.pendingParticipantInvites) != 0 {
				t.Fatalf("matched aliases remain pending: %+v", c.pendingParticipantInvites)
			}
			return
		}
	}
}

func TestParticipantInviteTargetKeyNormalizesPhoneAndDeviceJID(t *testing.T) {
	for input, want := range map[string]string{
		" +15550002 ":             "15550002",
		"222222222222222:43@lid":  "222222222222222",
		"15550002@s.whatsapp.net": "15550002",
	} {
		if got := participantInviteTargetKey(input); got != want {
			t.Errorf("participantInviteTargetKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWebCallControllerAddParticipantsRequiresActiveCall(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	c := &webCallController{ctx: context.Background(), log: zerolog.Nop()}

	err := c.addParticipants([]string{"15551234567"})

	if err == nil || err.Error() != "no active call" {
		t.Fatalf("error = %v, want no active call", err)
	}
}

func TestWebCallControllerAddParticipantsRequiresNormalizedTarget(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	c := &webCallController{
		ctx: context.Background(), call: &meowcaller.Call{}, log: zerolog.Nop(),
		callPhase: func(*meowcaller.Call) meowcaller.CallPhase {
			return meowcaller.CallPhaseActive
		},
	}

	err := c.addParticipants([]string{" ", "\n"})

	if err == nil || err.Error() != "at least one participant target is required" {
		t.Fatalf("error = %v, want target validation", err)
	}
}

func TestWebCallControllerAddParticipantsPublishesOneResultPerTarget(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 2)
	bridge.subs[events] = struct{}{}
	var gotTargets []string
	c := &webCallController{
		ctx: context.Background(), call: &meowcaller.Call{}, bridge: bridge, log: zerolog.Nop(),
		callPhase: func(*meowcaller.Call) meowcaller.CallPhase {
			return meowcaller.CallPhaseActive
		},
		inviteParticipants: func(_ context.Context, _ *meowcaller.Call, targets ...string) []error {
			gotTargets = append(gotTargets, targets...)
			return []error{nil, errors.New("invite rejected")}
		},
	}

	err := c.addParticipants([]string{" 15551234567 ", "", " 15557654321"})

	if err != nil {
		t.Fatalf("add participants: %v", err)
	}
	if strings.Join(gotTargets, ",") != "15551234567,15557654321" {
		t.Fatalf("targets = %v", gotTargets)
	}
	if got := c.pendingParticipantInvites["15551234567"]; got != "15551234567" {
		t.Fatalf("successful pending invite = %q", got)
	}
	if _, exists := c.pendingParticipantInvites["15557654321"]; exists {
		t.Fatal("failed invite remained pending for join correlation")
	}
	for i, want := range []webParticipantInviteResult{
		{Event: "participant_invite", Target: "15551234567", Success: true},
		{Event: "participant_invite", Target: "15557654321", Success: false, Message: "invite rejected"},
	} {
		select {
		case msg := <-events:
			var got webParticipantInviteResult
			if err = json.Unmarshal(msg.data, &got); err != nil {
				t.Fatalf("event %d JSON: %v", i, err)
			}
			if got != want {
				t.Fatalf("event %d = %+v, want %+v", i, got, want)
			}
		default:
			t.Fatalf("event %d was not published", i)
		}
	}
}

func TestWebCallControllerAddParticipantsStagesSynchronousJoinUntilInviteResult(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9246e88b842f82bb35932542bdac2c0775e0108/datasheets/web-group-call-outcomes.md#L56-L70
	participant := meowcaller.GroupCallParticipant{
		JID:   types.NewJID("222222222222222", types.HiddenUserServer),
		PN:    types.NewJID("15550002", types.DefaultUserServer),
		State: "connected",
		Devices: []meowcaller.GroupCallDevice{{
			JID: types.NewJID("222222222222222", types.HiddenUserServer),
			PID: 2, HasPID: true,
		}},
	}
	for _, tt := range []struct {
		name        string
		inviteErr   error
		wantJoin    bool
		wantSuccess bool
	}{
		{name: "success publishes staged join", wantJoin: true, wantSuccess: true},
		{name: "failure discards staged join", inviteErr: errors.New("invite rejected")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
			events := make(chan vbMsg, 4)
			bridge.subs[events] = struct{}{}
			call := &meowcaller.Call{}
			var c *webCallController
			c = &webCallController{
				ctx: context.Background(), call: call, bridge: bridge, log: zerolog.Nop(),
				callPhase: func(*meowcaller.Call) meowcaller.CallPhase {
					return meowcaller.CallPhaseActive
				},
				inviteParticipants: func(context.Context, *meowcaller.Call, ...string) []error {
					c.handleGroupState(call.ID(), meowcaller.GroupCallState{
						TransactionID: 18,
						Participants:  []meowcaller.GroupCallParticipant{participant},
					})
					return []error{tt.inviteErr}
				},
			}

			if err := c.addParticipants([]string{"+15550002"}); err != nil {
				t.Fatalf("add participants: %v", err)
			}

			var eventOrder []string
			var invite webParticipantInviteResult
			for {
				select {
				case msg := <-events:
					var envelope struct {
						Event string `json:"event"`
					}
					if err := json.Unmarshal(msg.data, &envelope); err != nil {
						t.Fatalf("event envelope: %v", err)
					}
					eventOrder = append(eventOrder, envelope.Event)
					if envelope.Event == "participant_invite" {
						if err := json.Unmarshal(msg.data, &invite); err != nil {
							t.Fatalf("invite event: %v", err)
						}
					}
				default:
					goto drained
				}
			}
		drained:
			wantOrder := []string{"group_state", "participant_invite"}
			if tt.wantJoin {
				wantOrder = append(wantOrder, "participant_join")
			}
			if strings.Join(eventOrder, ",") != strings.Join(wantOrder, ",") {
				t.Fatalf("event order = %v, want %v", eventOrder, wantOrder)
			}
			if invite.Success != tt.wantSuccess {
				t.Fatalf("invite success = %t, want %t", invite.Success, tt.wantSuccess)
			}
		})
	}
}

func TestWebCallControllerAddParticipantsIgnoresResultFromReplacedCall(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/2185887715c3ef6b2c0d76f14c8f13eab36aa224/datasheets/web-group-call-outcomes.md#L64-L66
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 2)
	bridge.subs[events] = struct{}{}
	callA := &meowcaller.Call{}
	callB := &meowcaller.Call{}
	inviteStarted := make(chan struct{})
	releaseInvite := make(chan struct{})
	candidateB := &pendingParticipantJoinCandidate{
		outcome: webParticipantJoin{Event: "participant_join", Target: "+15550002"},
	}
	c := &webCallController{
		ctx: context.Background(), call: callA, bridge: bridge, log: zerolog.Nop(),
		callPhase: func(*meowcaller.Call) meowcaller.CallPhase {
			return meowcaller.CallPhaseActive
		},
		inviteParticipants: func(context.Context, *meowcaller.Call, ...string) []error {
			close(inviteStarted)
			<-releaseInvite
			return []error{errors.New("old call rejected")}
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- c.addParticipants([]string{"+15550002"})
	}()
	<-inviteStarted

	c.mu.Lock()
	c.call = callB
	c.activeCallID = callB.ID()
	c.pendingParticipantCallID = callB.ID()
	c.pendingParticipantInvites = map[string]string{"15550002": "+15550002"}
	c.pendingParticipantOrder = []string{"15550002"}
	c.participantInviteInFlight = map[string]bool{"15550002": true}
	c.pendingParticipantJoins = map[string]*pendingParticipantJoinCandidate{"15550002": candidateB}
	c.mu.Unlock()

	close(releaseInvite)
	if err := <-done; err != nil {
		t.Fatalf("old add participants: %v", err)
	}
	c.mu.Lock()
	if c.pendingParticipantInvites["15550002"] != "+15550002" ||
		!c.participantInviteInFlight["15550002"] ||
		c.pendingParticipantJoins["15550002"] != candidateB {
		t.Fatalf("old result mutated replacement state: invites=%v inflight=%v joins=%v",
			c.pendingParticipantInvites, c.participantInviteInFlight, c.pendingParticipantJoins)
	}
	c.mu.Unlock()
	select {
	case msg := <-events:
		t.Fatalf("old result published into replacement call: %s", msg.data)
	default:
	}
}

func TestWebCallControllerAddParticipantsDoesNotRegisterAfterCallReplacement(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/2185887715c3ef6b2c0d76f14c8f13eab36aa224/datasheets/web-group-call-outcomes.md#L64-L71
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 2)
	bridge.subs[events] = struct{}{}
	callA := &meowcaller.Call{}
	callB := &meowcaller.Call{}
	beforeRegister := make(chan struct{})
	releaseRegister := make(chan struct{})
	c := &webCallController{
		ctx: context.Background(), call: callA, bridge: bridge, log: zerolog.Nop(),
		callPhase: func(*meowcaller.Call) meowcaller.CallPhase {
			return meowcaller.CallPhaseActive
		},
		beforeParticipantRegister: func() {
			close(beforeRegister)
			<-releaseRegister
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- c.addParticipants([]string{"+15550002"})
	}()
	<-beforeRegister
	c.mu.Lock()
	c.call = callB
	c.activeCallID = callB.ID()
	c.pendingParticipantCallID = callB.ID()
	c.pendingParticipantInvites = make(map[string]string)
	c.pendingParticipantOrder = nil
	c.participantInviteInFlight = make(map[string]bool)
	c.pendingParticipantJoins = make(map[string]*pendingParticipantJoinCandidate)
	c.mu.Unlock()
	close(releaseRegister)
	if err := <-done; err == nil || err.Error() != "active call changed before participant invite" {
		t.Fatalf("stale registration error = %v", err)
	}

	c.handleGroupState(callB.ID(), meowcaller.GroupCallState{
		TransactionID: 20,
		Participants: []meowcaller.GroupCallParticipant{{
			JID:   types.NewJID("222222222222222", types.HiddenUserServer),
			PN:    types.NewJID("15550002", types.DefaultUserServer),
			State: "connected",
			Devices: []meowcaller.GroupCallDevice{{
				JID: types.NewJID("222222222222222", types.HiddenUserServer),
				PID: 2, HasPID: true,
			}},
		}},
	})
	var eventOrder []string
	for {
		select {
		case msg := <-events:
			var event struct {
				Event string `json:"event"`
			}
			if err := json.Unmarshal(msg.data, &event); err != nil {
				t.Fatalf("event: %v", err)
			}
			eventOrder = append(eventOrder, event.Event)
		default:
			if strings.Join(eventOrder, ",") != "group_state" {
				t.Fatalf("replacement roster events = %v, want group_state only", eventOrder)
			}
			return
		}
	}
}

func TestWebCallControllerSerializesOverlappingParticipantSubmissions(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/2185887715c3ef6b2c0d76f14c8f13eab36aa224/datasheets/web-group-call-outcomes.md#L64-L66
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	call := &meowcaller.Call{}
	firstInviteStarted := make(chan struct{})
	releaseFirstInvite := make(chan struct{})
	secondCallerStarted := make(chan struct{})
	secondInviteStarted := make(chan struct{})
	var inviteCalls atomic.Int32
	c := &webCallController{
		ctx: context.Background(), call: call, bridge: bridge, log: zerolog.Nop(),
		callPhase: func(*meowcaller.Call) meowcaller.CallPhase {
			return meowcaller.CallPhaseActive
		},
		inviteParticipants: func(context.Context, *meowcaller.Call, ...string) []error {
			if inviteCalls.Add(1) == 1 {
				close(firstInviteStarted)
				<-releaseFirstInvite
			} else {
				close(secondInviteStarted)
			}
			return []error{nil}
		},
	}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- c.addParticipants([]string{"+15550002"})
	}()
	<-firstInviteStarted
	secondDone := make(chan error, 1)
	go func() {
		close(secondCallerStarted)
		secondDone <- c.addParticipants([]string{"+15550002"})
	}()
	<-secondCallerStarted
	if c.participantInviteMu.TryLock() {
		c.participantInviteMu.Unlock()
		t.Fatal("overlapping add call was not serialized")
	}
	close(releaseFirstInvite)
	if err := <-firstDone; err != nil {
		t.Fatalf("first add participants: %v", err)
	}
	<-secondInviteStarted
	if err := <-secondDone; err != nil {
		t.Fatalf("second add participants: %v", err)
	}
	if got := inviteCalls.Load(); got != 2 {
		t.Fatalf("invite calls = %d, want 2 serialized calls", got)
	}
}

func TestWebCallControllerDoesNotPublishRosterAfterCallEnd(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/2185887715c3ef6b2c0d76f14c8f13eab36aa224/datasheets/web-group-call-outcomes.md#L78-L82
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 1)
	bridge.subs[events] = struct{}{}
	beforePublish := make(chan struct{})
	releasePublish := make(chan struct{})
	c := &webCallController{
		bridge: bridge, log: zerolog.Nop(), activeCallID: "OLD",
		beforeGroupStatePublish: func() {
			close(beforePublish)
			<-releasePublish
		},
	}
	done := make(chan struct{})
	go func() {
		c.handleGroupState("OLD", meowcaller.GroupCallState{TransactionID: 19})
		close(done)
	}()
	<-beforePublish
	c.mu.Lock()
	c.activeCallID = ""
	bridge.ClearGroupState("OLD")
	c.mu.Unlock()
	close(releasePublish)
	<-done
	select {
	case msg := <-events:
		t.Fatalf("stale roster published after call end: %s", msg.data)
	default:
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if len(bridge.groupState) != 0 || bridge.groupCallID != "" {
		t.Fatalf("stale roster repopulated replay cache: call=%q state=%s", bridge.groupCallID, bridge.groupState)
	}
}

func TestWebCallControllerDeduplicatesNormalizedParticipantTargets(t *testing.T) {
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 2)
	bridge.subs[events] = struct{}{}
	var gotTargets []string
	c := &webCallController{
		ctx: context.Background(), call: &meowcaller.Call{}, bridge: bridge, log: zerolog.Nop(),
		callPhase: func(*meowcaller.Call) meowcaller.CallPhase {
			return meowcaller.CallPhaseActive
		},
		inviteParticipants: func(_ context.Context, _ *meowcaller.Call, targets ...string) []error {
			gotTargets = append(gotTargets, targets...)
			return make([]error, len(targets))
		},
	}
	if err := c.addParticipants([]string{
		"+15551234567",
		"15551234567@s.whatsapp.net",
		" 15551234567 ",
	}); err != nil {
		t.Fatalf("add participants: %v", err)
	}
	if len(gotTargets) != 1 || gotTargets[0] != "+15551234567" {
		t.Fatalf("submitted targets = %v, want first normalized target once", gotTargets)
	}
	if len(c.pendingParticipantInvites) != 1 {
		t.Fatalf("pending invites = %+v, want one", c.pendingParticipantInvites)
	}
}

func TestWebCallControllerStartsOneAudioGroupCallWithDistinctTargets(t *testing.T) {
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 1)
	bridge.subs[events] = struct{}{}
	call := &meowcaller.Call{}
	var calls int
	var attaches int
	var gotTargets []string
	var gotOptions meowcaller.GroupCallOptions
	c := &webCallController{
		ctx: context.Background(), bridge: bridge, log: zerolog.Nop(),
		startGroupCall: func(
			_ context.Context,
			targets []string,
			options meowcaller.GroupCallOptions,
		) (*meowcaller.Call, error) {
			calls++
			gotTargets = append(gotTargets, targets...)
			gotOptions = options
			return call, nil
		},
		inviteParticipants: func(context.Context, *meowcaller.Call, ...string) []error {
			t.Fatal("initial group start used the established-call participant invite path")
			return nil
		},
		attachCall: func(got *meowcaller.Call) error {
			if got != call {
				t.Fatalf("attached call %p, want %p", got, call)
			}
			attaches++
			return nil
		},
	}

	err := c.control(vbControl{
		Action: "start_group_audio",
		Targets: []string{
			" +15551234567 ",
			"15551234567@s.whatsapp.net",
			" 222222222222222:43@lid ",
		},
	})

	if err != nil {
		t.Fatalf("start group audio: %v", err)
	}
	if calls != 1 {
		t.Fatalf("group start calls = %d, want 1", calls)
	}
	if attaches != 1 {
		t.Fatalf("call attachments = %d, want 1", attaches)
	}
	if strings.Join(gotTargets, ",") != "+15551234567,222222222222222:43@lid" {
		t.Fatalf("group targets = %v", gotTargets)
	}
	if gotOptions.GroupJID != "" {
		t.Fatalf("group JID = %q, want empty selector-flow binding", gotOptions.GroupJID)
	}
	if c.call != call || c.pending != nil {
		t.Fatalf("controller ownership = (call %p, pending %p), want (%p, nil)", c.call, c.pending, call)
	}
	select {
	case msg := <-events:
		var state webCallState
		if err = json.Unmarshal(msg.data, &state); err != nil {
			t.Fatalf("group dialing JSON: %v", err)
		}
		if state.Event != "group_dialing" || state.Video {
			t.Fatalf("group dialing state = %+v", state)
		}
	default:
		t.Fatal("group dialing state was not published")
	}
}

func TestWebCallControllerStartsVideoGroupCall(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L56-L63
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	call := &meowcaller.Call{}
	var gotOptions meowcaller.GroupCallOptions
	c := &webCallController{
		ctx: context.Background(), bridge: bridge, log: zerolog.Nop(),
		startGroupCall: func(
			_ context.Context,
			_ []string,
			options meowcaller.GroupCallOptions,
		) (*meowcaller.Call, error) {
			gotOptions = options
			return call, nil
		},
		attachCall: func(*meowcaller.Call) error { return nil },
	}
	if err := c.control(vbControl{
		Action:  "start_group_video",
		Targets: []string{"15550001", "15550002"},
	}); err != nil {
		t.Fatalf("start group video: %v", err)
	}
	if !gotOptions.Video {
		t.Fatal("start group video delegated audio-only options")
	}
}

func TestVideoBridgePublishesParticipantTaggedFrame(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L32-L63
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 1)
	bridge.subs[events] = struct{}{}
	frame := meowcaller.ParticipantVideoFrame{
		ParticipantID: "333333333333333:43@lid",
		Sender:        types.NewJID("333333333333333", types.HiddenUserServer),
		Device:        types.NewADJID("333333333333333", 0, 43),
		SSRC:          0x12345678,
		Orientation:   2,
		AccessUnit:    []byte{0, 0, 0, 1, 0x65},
	}
	bridge.WriteParticipantFrame(frame)
	msg := <-events
	if msg.event != "participant_video" {
		t.Fatalf("bridge event = %q, want participant_video", msg.event)
	}
	var got vbParticipantVideo
	if err := json.Unmarshal(msg.data, &got); err != nil {
		t.Fatalf("decode participant video event: %v", err)
	}
	if got.ParticipantID != frame.ParticipantID || got.Sender != frame.Sender.String() ||
		got.Device != frame.Device.String() || got.SSRC != frame.SSRC ||
		got.Orientation != frame.Orientation ||
		got.AccessUnit != base64.StdEncoding.EncodeToString(frame.AccessUnit) {
		t.Fatalf("participant video event = %+v", got)
	}
}

func TestWebCallControllerStartGroupAudioRequiresTwoDistinctPeople(t *testing.T) {
	var calls int
	c := &webCallController{
		ctx: context.Background(), log: zerolog.Nop(),
		startGroupCall: func(
			context.Context,
			[]string,
			meowcaller.GroupCallOptions,
		) (*meowcaller.Call, error) {
			calls++
			return &meowcaller.Call{}, nil
		},
	}

	err := c.control(vbControl{
		Action:  "start_group_audio",
		Targets: []string{" +15551234567 ", "15551234567@s.whatsapp.net", ""},
	})

	if err == nil || err.Error() != "at least two distinct participant targets are required" {
		t.Fatalf("error = %v, want distinct target validation", err)
	}
	if calls != 0 {
		t.Fatalf("group start delegated %d times after validation failure", calls)
	}
}

func TestWebCallControllerStartGroupAudioRejectsEveryBusyOwner(t *testing.T) {
	for _, test := range []struct {
		name    string
		call    *meowcaller.Call
		pending *meowcaller.Call
		callID  string
	}{
		{name: "call", call: &meowcaller.Call{}},
		{name: "pending", pending: &meowcaller.Call{}},
		{name: "active call ID", callID: "CID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			c := &webCallController{
				ctx: context.Background(), log: zerolog.Nop(),
				call: test.call, pending: test.pending, activeCallID: test.callID,
				startGroupCall: func(
					context.Context,
					[]string,
					meowcaller.GroupCallOptions,
				) (*meowcaller.Call, error) {
					calls++
					return &meowcaller.Call{}, nil
				},
			}

			err := c.startGroupAudio([]string{"15550001", "15550002"})

			if err == nil || err.Error() != "another call is already active" {
				t.Fatalf("error = %v, want busy rejection", err)
			}
			if calls != 0 {
				t.Fatalf("group start delegated %d times while busy", calls)
			}
		})
	}
}

func TestWebCallControllerDirectDialReservationExcludesGroupStart(t *testing.T) {
	directEntered := make(chan struct{})
	releaseDirect := make(chan struct{})
	directSignalErr := errors.New("direct signaling stopped")
	groupSignalErr := errors.New("group signaling must not start")
	var directCalls int
	var groupCalls int
	c := &webCallController{
		ctx: context.Background(), log: zerolog.Nop(),
		dialCall: func(
			context.Context,
			string,
			meowcaller.CallOptions,
		) (*meowcaller.Call, error) {
			directCalls++
			close(directEntered)
			<-releaseDirect
			return nil, directSignalErr
		},
		startGroupCall: func(
			context.Context,
			[]string,
			meowcaller.GroupCallOptions,
		) (*meowcaller.Call, error) {
			groupCalls++
			return nil, groupSignalErr
		},
	}
	directDone := make(chan error, 1)
	go func() {
		directDone <- c.control(vbControl{Action: "dial_audio", Target: "15550001"})
	}()
	<-directEntered

	groupErr := c.control(vbControl{
		Action: "start_group_audio", Targets: []string{"15550002", "15550003"},
	})
	close(releaseDirect)
	directErr := <-directDone

	if groupErr == nil || groupErr.Error() != "another call is already active" {
		t.Fatalf("group start error = %v, want busy rejection", groupErr)
	}
	if !errors.Is(directErr, directSignalErr) {
		t.Fatalf("direct dial error = %v, want %v", directErr, directSignalErr)
	}
	if directCalls != 1 || groupCalls != 0 {
		t.Fatalf("network starts = (direct %d, group %d), want (1, 0)", directCalls, groupCalls)
	}
	if c.call != nil || c.pending != nil || c.activeCallID != "" {
		t.Fatalf("failed direct dial retained ownership: call=%p pending=%p id=%q", c.call, c.pending, c.activeCallID)
	}
}

func TestWebCallControllerDirectDialTransfersReturnedCallOwnership(t *testing.T) {
	call := &meowcaller.Call{}
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 1)
	bridge.subs[events] = struct{}{}
	var attaches int
	c := &webCallController{
		ctx: context.Background(), bridge: bridge, log: zerolog.Nop(),
		dialCall: func(
			context.Context,
			string,
			meowcaller.CallOptions,
		) (*meowcaller.Call, error) {
			return call, nil
		},
		attachCall: func(got *meowcaller.Call) error {
			if got != call {
				t.Fatalf("attached call %p, want %p", got, call)
			}
			attaches++
			return nil
		},
	}

	if err := c.dial("15550001", true); err != nil {
		t.Fatalf("dial: %v", err)
	}

	if attaches != 1 || c.call != call || c.pending != nil {
		t.Fatalf("direct ownership = (attachments %d, call %p, pending %p)", attaches, c.call, c.pending)
	}
	select {
	case msg := <-events:
		var state webCallState
		if err := json.Unmarshal(msg.data, &state); err != nil {
			t.Fatalf("dialing JSON: %v", err)
		}
		if state.Event != "dialing" || !state.Video {
			t.Fatalf("dialing state = %+v", state)
		}
	default:
		t.Fatal("dialing state was not published")
	}
}

func TestWebCallControllerDirectDialSignalingFailurePreservesReplacementOwner(t *testing.T) {
	signalErr := errors.New("direct signaling failed")
	c := &webCallController{ctx: context.Background(), log: zerolog.Nop()}
	c.dialCall = func(
		context.Context,
		string,
		meowcaller.CallOptions,
	) (*meowcaller.Call, error) {
		c.mu.Lock()
		c.activeCallID = "OTHER"
		c.mu.Unlock()
		return nil, signalErr
	}

	err := c.dial("15550001", false)

	if !errors.Is(err, signalErr) {
		t.Fatalf("dial error = %v, want %v", err, signalErr)
	}
	if c.call != nil || c.pending != nil || c.activeCallID != "OTHER" {
		t.Fatalf("signaling rollback clobbered replacement owner: call=%p pending=%p id=%q", c.call, c.pending, c.activeCallID)
	}
}

func TestWebCallControllerDirectDialOwnershipChangeHangsUpReturnedCall(t *testing.T) {
	call := &meowcaller.Call{}
	var hangups int
	c := &webCallController{ctx: context.Background(), log: zerolog.Nop()}
	c.dialCall = func(
		context.Context,
		string,
		meowcaller.CallOptions,
	) (*meowcaller.Call, error) {
		c.mu.Lock()
		c.activeCallID = "OTHER"
		c.mu.Unlock()
		return call, nil
	}
	c.hangupCall = func(got *meowcaller.Call) error {
		if got != call {
			t.Fatalf("hung up call %p, want %p", got, call)
		}
		hangups++
		return nil
	}

	err := c.dial("15550001", false)

	if err == nil || err.Error() != "call ownership changed while dialing" {
		t.Fatalf("dial error = %v, want ownership change", err)
	}
	if hangups != 1 {
		t.Fatalf("hangups = %d, want 1", hangups)
	}
	if c.call != nil || c.pending != nil || c.activeCallID != "OTHER" {
		t.Fatalf("cleanup disturbed replacement owner: call=%p pending=%p id=%q", c.call, c.pending, c.activeCallID)
	}
}

func TestWebCallControllerDirectDialAttachFailureClearsAndHangsUp(t *testing.T) {
	call := &meowcaller.Call{}
	var hangups int
	c := &webCallController{
		ctx: context.Background(), log: zerolog.Nop(),
		dialCall: func(
			context.Context,
			string,
			meowcaller.CallOptions,
		) (*meowcaller.Call, error) {
			return call, nil
		},
		attachCall: func(*meowcaller.Call) error { return errors.New("attach failed") },
		hangupCall: func(got *meowcaller.Call) error {
			if got != call {
				t.Fatalf("hung up call %p, want %p", got, call)
			}
			hangups++
			return nil
		},
	}

	err := c.dial("15550001", false)

	if err == nil || err.Error() != "attach failed" {
		t.Fatalf("dial error = %v, want attach failure", err)
	}
	if hangups != 1 {
		t.Fatalf("hangups = %d, want 1", hangups)
	}
	if c.call != nil || c.pending != nil || c.activeCallID != "" {
		t.Fatalf("attach failure retained ownership: call=%p pending=%p id=%q", c.call, c.pending, c.activeCallID)
	}
}

func TestWebCallControllerRejectsIncomingCallDuringGroupStartReservation(t *testing.T) {
	incoming := &meowcaller.Call{}
	var rejected int
	c := &webCallController{
		bridge:       &videoBridge{subs: make(map[chan vbMsg]struct{})},
		log:          zerolog.Nop(),
		activeCallID: "web-group-start-pending",
		rejectCall: func(got *meowcaller.Call) error {
			if got != incoming {
				t.Fatalf("rejected call %p, want %p", got, incoming)
			}
			rejected++
			return nil
		},
	}

	c.onIncomingCall(incoming)

	if rejected != 1 {
		t.Fatalf("incoming rejections = %d, want 1", rejected)
	}
	if c.pending != nil || c.activeCallID != "web-group-start-pending" {
		t.Fatalf("incoming call replaced group-start ownership: pending=%p id=%q", c.pending, c.activeCallID)
	}
}

func TestWebCallControllerStartGroupAudioOwnershipChangeHangsUpReturnedCall(t *testing.T) {
	call := &meowcaller.Call{}
	var hangups int
	c := &webCallController{ctx: context.Background(), log: zerolog.Nop()}
	c.startGroupCall = func(
		context.Context,
		[]string,
		meowcaller.GroupCallOptions,
	) (*meowcaller.Call, error) {
		c.mu.Lock()
		c.activeCallID = "OTHER"
		c.mu.Unlock()
		return call, nil
	}
	c.hangupCall = func(got *meowcaller.Call) error {
		if got != call {
			t.Fatalf("hung up call %p, want %p", got, call)
		}
		hangups++
		return nil
	}

	err := c.startGroupAudio([]string{"15550001", "15550002"})

	if err == nil || err.Error() != "group call ownership changed while starting" {
		t.Fatalf("error = %v, want ownership change", err)
	}
	if hangups != 1 {
		t.Fatalf("hangups = %d, want 1", hangups)
	}
	if c.call != nil || c.pending != nil || c.activeCallID != "OTHER" {
		t.Fatalf("cleanup disturbed current ownership: call=%p pending=%p id=%q", c.call, c.pending, c.activeCallID)
	}
}

func TestWebCallControllerStartGroupAudioAttachFailureClearsAndHangsUp(t *testing.T) {
	call := &meowcaller.Call{}
	var hangups int
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	bridge.PublishGroupState(webGroupCallState{Event: "group_state"})
	c := &webCallController{
		ctx: context.Background(), bridge: bridge, log: zerolog.Nop(),
		startGroupCall: func(
			context.Context,
			[]string,
			meowcaller.GroupCallOptions,
		) (*meowcaller.Call, error) {
			return call, nil
		},
		attachCall: func(*meowcaller.Call) error { return errors.New("attach failed") },
		hangupCall: func(got *meowcaller.Call) error {
			if got != call {
				t.Fatalf("hung up call %p, want %p", got, call)
			}
			hangups++
			return nil
		},
	}

	err := c.startGroupAudio([]string{"15550001", "15550002"})

	if err == nil || err.Error() != "attach failed" {
		t.Fatalf("error = %v, want attach failure", err)
	}
	if c.call != nil || c.pending != nil || c.activeCallID != "" {
		t.Fatalf("attach failure retained ownership: call=%p pending=%p id=%q", c.call, c.pending, c.activeCallID)
	}
	if len(bridge.groupState) != 0 || bridge.groupCallID != "" {
		t.Fatal("attach failure retained replayable group state")
	}
	if hangups != 1 {
		t.Fatalf("hangups = %d, want 1", hangups)
	}
}

func TestWebCallControllerAddParticipantsRequiresEstablishedCall(t *testing.T) {
	for _, phase := range []meowcaller.CallPhase{
		meowcaller.CallPhaseIdle,
		meowcaller.CallPhaseCalling,
		meowcaller.CallPhaseRinging,
		meowcaller.CallPhaseConnecting,
		meowcaller.CallPhaseEnded,
	} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			var delegated bool
			c := &webCallController{
				ctx: context.Background(), call: &meowcaller.Call{}, log: zerolog.Nop(),
				callPhase: func(*meowcaller.Call) meowcaller.CallPhase { return phase },
				inviteParticipants: func(context.Context, *meowcaller.Call, ...string) []error {
					delegated = true
					return nil
				},
			}

			err := c.addParticipants([]string{"15551234567"})

			if err == nil || err.Error() != "participant invites require an active call" {
				t.Fatalf("phase %d error = %v", phase, err)
			}
			if delegated {
				t.Fatalf("phase %d delegated participant invite", phase)
			}
			if c.pendingParticipantCallID != "" ||
				len(c.pendingParticipantInvites) != 0 ||
				len(c.pendingParticipantOrder) != 0 {
				t.Fatalf("phase %d recorded pending outcomes before delegation", phase)
			}
		})
	}
}

func TestWebCallControllerAnswerPublishesIncomingRosterOverSSEBeforeConnecting(t *testing.T) {
	call := &meowcaller.Call{}
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	events := make(chan vbMsg, 4)
	bridge.subs[events] = struct{}{}
	c := &webCallController{
		bridge: bridge, log: zerolog.Nop(), pending: call,
		listenGroupState: func(_ *meowcaller.Call, listener func(meowcaller.GroupCallState)) {
			listener(meowcaller.GroupCallState{TransactionID: 17})
		},
		attachMedia: func(*meowcaller.Call) error { return nil },
		callVideo:   func(*meowcaller.Call) bool { return false },
		rejectCall:  func(*meowcaller.Call) error { return nil },
	}
	c.answerCall = func(*meowcaller.Call) error {
		c.publish(webCallState{
			Event: "phase", Phase: int(meowcaller.CallPhaseConnecting),
		})
		return nil
	}

	if err := c.answer(); err != nil {
		t.Fatalf("answer: %v", err)
	}

	var got []string
	for {
		select {
		case msg := <-events:
			var event struct {
				Event string `json:"event"`
			}
			if err := json.Unmarshal(msg.data, &event); err != nil {
				t.Fatalf("state JSON: %v", err)
			}
			got = append(got, event.Event)
		default:
			if strings.Join(got, ",") != "group_state,phase,answering" {
				t.Fatalf("answer publication order = %v", got)
			}
			for _, event := range got {
				if event == "ready" {
					t.Fatal("roster replay or Answer synthesized ready")
				}
			}
			return
		}
	}
}

func TestWebCallControllerFailedAnswerReleasesIncomingCall(t *testing.T) {
	call := &meowcaller.Call{}
	bridge := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	rejected := 0
	c := &webCallController{
		bridge: bridge, log: zerolog.Nop(), pending: call,
		attachCall: func(*meowcaller.Call) error { return nil },
		answerCall: func(*meowcaller.Call) error { return errors.New("accept failed") },
		rejectCall: func(*meowcaller.Call) error {
			rejected++
			return nil
		},
	}
	err := c.answer()
	if err == nil || err.Error() != "accept failed" {
		t.Fatalf("answer error = %v", err)
	}
	if c.pending != nil || c.call != nil || c.activeCallID != "" {
		t.Fatalf("failed answer left controller busy: pending=%p call=%p id=%q", c.pending, c.call, c.activeCallID)
	}
	if rejected != 1 {
		t.Fatalf("reject calls = %d, want 1", rejected)
	}
}

func TestVideoBridgePageAddsCommaOrNewlineSeparatedPeople(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	for _, behavior := range []string{
		`id="participants"`,
		`id="addParticipants"`,
		"split(/[,\\n]/)",
		"invoke('add_participants',{targets:participantTargets()})",
		"Add people",
		"waiting for roster confirmation",
		`id="participantStatus"`,
		"s.event==='participant_join'",
		"s.event==='group_state'",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
}

func TestVideoBridgePageKeepsParticipantInviteOutOfLifecycleHeader(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	if !strings.Contains(videoBridgePage, "s.event!=='participant_invite'") {
		t.Fatal("participant invite events can replace the lifecycle header")
	}
	for _, event := range []string{"participant_join", "group_state"} {
		if !strings.Contains(videoBridgePage, "s.event!=='"+event+"'") {
			t.Fatalf("%s events can replace the lifecycle header", event)
		}
	}
}
