package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
		"showReaction(s.emoji)",
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
		"f.displayWidth!==encodedWidth",
		"width:encodedWidth,height:encodedHeight",
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
		bridge: bridge,
		log:    zerolog.Nop(),
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
