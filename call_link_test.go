package meowcaller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func callLinkTestClient() *Client {
	client := &Client{log: zerolog.Nop()}
	client.eng = newEngine(client)
	return client
}

func TestCreateAndPreviewCallLinkMapPublicValues(t *testing.T) {
	client := callLinkTestClient()
	client.eng.createCallLink = func(context.Context, types.CallLinkMedia) (*types.CallLink, error) {
		return &types.CallLink{Token: "TOKEN", Media: types.CallLinkMediaVideo}, nil
	}
	client.eng.previewCallLink = func(_ context.Context, token string, media types.CallLinkMedia) (*types.CallLinkPreview, error) {
		if token != "TOKEN" || media != types.CallLinkMediaVideo {
			t.Fatalf("preview request = (%q, %q)", token, media)
		}
		return &types.CallLinkPreview{
			Token:              token,
			Media:              media,
			Creator:            types.JID{User: "1", Server: types.HiddenUserServer},
			WaitingRoomEnabled: true,
			IsAdmin:            true,
		}, nil
	}
	link, err := client.CreateCallLink(t.Context(), CallLinkOptions{Video: true})
	if err != nil {
		t.Fatal(err)
	}
	if link.URL != "https://call.whatsapp.com/video/TOKEN" || !link.Video {
		t.Fatalf("link = %#v", link)
	}
	preview, err := client.PreviewCallLink(
		t.Context(),
		"https://call.whatsapp.com/video/TOKEN",
		CallLinkOptions{Video: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Token != "TOKEN" || !preview.ApprovalRequired || !preview.IsAdmin {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestJoinCallLinkWaitingDoesNotStartMedia(t *testing.T) {
	client := callLinkTestClient()
	creator := types.JID{User: "156535032389744", Server: types.HiddenUserServer, Device: 15}
	client.eng.joinCallLink = func(_ context.Context, token string, media types.CallLinkMedia) (*types.CallLinkJoin, error) {
		if token != "TOKEN" || media != types.CallLinkMediaVideo {
			t.Fatalf("join request = (%q, %q)", token, media)
		}
		return &types.CallLinkJoin{
			Token:              token,
			Media:              media,
			CallID:             "CALL",
			CallCreator:        creator,
			WaitingRoomEnabled: true,
			InWaitingRoom:      true,
		}, nil
	}
	var mediaStarts int
	client.eng.startMedia = func(context.Context, string, *Call, []byte, string, string, *types.RelayEndpoint) error {
		mediaStarts++
		return nil
	}
	call, err := client.JoinCallLink(t.Context(), "TOKEN", CallLinkOptions{Video: true})
	if err != nil {
		t.Fatal(err)
	}
	if call.State() != CallPhaseWaitingRoom || mediaStarts != 0 {
		t.Fatalf("waiting join = phase %d, media starts %d", call.State(), mediaStarts)
	}
	state, ok := call.WaitingRoomState()
	if !ok || !state.Enabled || !state.InWaitingRoom {
		t.Fatalf("waiting room = %#v, %t", state, ok)
	}

	client.eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID(), CallCreator: creator},
		Update: types.GroupCallUpdate{
			CallID:        call.ID(),
			CallCreator:   creator,
			TransactionID: 3,
			Media:         "video",
		},
	})
	if call.State() != CallPhaseConnecting {
		t.Fatalf("admitted call phase = %d, want connecting", call.State())
	}
}

func TestWaitingRoomEventsAndAdminControls(t *testing.T) {
	client := callLinkTestClient()
	call := &Call{
		eng:   client.eng,
		id:    "CALL",
		phase: CallPhaseWaitingRoom,
	}
	client.eng.calls[call.ID()] = &engineCall{call: call, group: true}
	user := types.JID{User: "242653052539031", Server: types.HiddenUserServer}
	var got WaitingRoomState
	call.OnWaitingRoomState(func(state WaitingRoomState) { got = state })
	client.eng.onWaitingRoomUpdate(&events.CallWaitingRoomUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		WaitingRoom: types.CallLinkWaitingRoom{
			CallID:        call.ID(),
			Enabled:       true,
			IsAdmin:       true,
			TransactionID: 4,
			Users: []types.CallLinkWaitingRoomUser{{
				JID:   user,
				State: "waiting_room_joined",
			}},
		},
	})
	if !got.IsAdmin || got.TransactionID != 4 || len(got.Users) != 1 {
		t.Fatalf("waiting-room callback = %#v", got)
	}
	var admitted types.JID
	client.eng.admitCallLinkParticipant = func(_ context.Context, callID string, participant types.JID) error {
		if callID != call.ID() {
			t.Fatalf("call ID = %q", callID)
		}
		admitted = participant
		return nil
	}
	if err := call.AdmitParticipant(t.Context(), user.String()); err != nil {
		t.Fatal(err)
	}
	if admitted != user {
		t.Fatalf("admitted = %s", admitted)
	}
}

func TestWaitingRoomReplayCannotFinishAfterNewerUpdate(t *testing.T) {
	call := &Call{}
	call.setWaitingRoomState(WaitingRoomState{TransactionID: 1})

	replayStarted := make(chan struct{})
	releaseReplay := make(chan struct{})
	var callbackMu sync.Mutex
	var callbacks []uint32
	replayDone := make(chan struct{})
	go func() {
		call.OnWaitingRoomState(func(state WaitingRoomState) {
			if state.TransactionID == 1 {
				close(replayStarted)
				<-releaseReplay
			}
			callbackMu.Lock()
			callbacks = append(callbacks, state.TransactionID)
			callbackMu.Unlock()
		})
		close(replayDone)
	}()
	<-replayStarted

	updateDone := make(chan struct{})
	go func() {
		call.setWaitingRoomState(WaitingRoomState{TransactionID: 2})
		close(updateDone)
	}()
	select {
	case <-updateDone:
		t.Fatal("newer waiting-room update bypassed the in-flight replay")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseReplay)
	<-replayDone
	<-updateDone

	callbackMu.Lock()
	defer callbackMu.Unlock()
	if len(callbacks) != 2 || callbacks[0] != 1 || callbacks[1] != 2 {
		t.Fatalf("waiting-room callbacks = %v, want [1 2]", callbacks)
	}
}

func TestHandAndScreenSharePublicState(t *testing.T) {
	client := callLinkTestClient()
	call := &Call{eng: client.eng, id: "CALL", phase: CallPhaseActive}
	client.eng.calls[call.ID()] = &engineCall{call: call, group: true}
	participant := types.JID{User: "242653052539031", Server: types.HiddenUserServer}
	var raised bool
	var screen ScreenShareState
	call.OnHandRaise(func(state HandRaiseState) { raised = state.Raised })
	call.OnScreenShare(func(state ScreenShareState) { screen = state })
	client.eng.onHandRaise(&events.CallHandRaise{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		Participant:   participant,
		Raised:        true,
	})
	client.eng.onScreenShare(&events.CallScreenShare{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		CallScreenShare: types.CallScreenShare{
			State:            types.CallScreenShareStateStarted,
			Version:          2,
			ScreenShareID:    1,
			HasScreenShareID: true,
		},
		Participant: participant,
	})
	if !raised || screen.Participant != participant || !screen.Active || screen.ScreenShareID != 1 {
		t.Fatalf("participant states = raised:%t screen:%#v", raised, screen)
	}

	var localRaised bool
	client.eng.setCallHandRaised = func(_ context.Context, callID string, value bool) error {
		localRaised = value
		return nil
	}
	if err := call.SetHandRaised(true); err != nil || !localRaised {
		t.Fatalf("SetHandRaised = %v, delegated %t", err, localRaised)
	}
}

func TestScreenShareTransitionsRequestSourceKeyframesAfterSignaling(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L45-L48
	client := callLinkTestClient()
	call := &Call{eng: client.eng, id: "CALL", phase: CallPhaseActive}
	sender := &videoSender{active: true}
	client.eng.calls[call.ID()] = &engineCall{call: call, group: true, videoTx: sender}
	client.eng.setCallScreenShare = func(
		_ context.Context,
		callID string,
		_ types.CallScreenShareState,
		_ *uint32,
	) error {
		if callID != call.ID() {
			t.Fatalf("call ID = %q, want %q", callID, call.ID())
		}
		return nil
	}
	keyframes := 0
	call.OnVideoKeyframeRequest(func() { keyframes++ })

	screenShareID := uint32(1)
	if err := call.StartScreenShare(&screenShareID); err != nil {
		t.Fatalf("StartScreenShare: %v", err)
	}
	sender.mu.Lock()
	startRequired := sender.keyframeRequired
	sender.keyframeRequired = false
	sender.mu.Unlock()
	if !startRequired || keyframes != 1 {
		t.Fatalf("start transition = sender keyframe:%t callback:%d, want true/1", startRequired, keyframes)
	}

	if err := call.StopScreenShare(); err != nil {
		t.Fatalf("StopScreenShare: %v", err)
	}
	sender.mu.Lock()
	stopRequired := sender.keyframeRequired
	sender.mu.Unlock()
	if !stopRequired || keyframes != 2 {
		t.Fatalf("stop transition = sender keyframe:%t callback:%d, want true/2", stopRequired, keyframes)
	}
}
