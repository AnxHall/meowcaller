package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
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
		"submitted, not joined",
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
}
