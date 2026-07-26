package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newControlRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(body))
	req.Header.Set(browserMutationHeader, "1")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestVideoBridgeMutationsRejectSimpleCrossOriginRequests(t *testing.T) {
	vb := &videoBridge{}
	for _, test := range []struct {
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{path: "/control", handler: vb.handleControl},
		{path: "/out", handler: vb.handleOut},
	} {
		req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		test.handler(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", test.path, rec.Code)
		}
	}
}

func TestVideoBridgeControlDispatchesJSONCommand(t *testing.T) {
	vb := &videoBridge{}
	var got vbControl
	vb.OnControl(func(command vbControl) error {
		got = command
		return nil
	})
	req := newControlRequest(`{"action":"dial_video","target":"15551234567"}`)
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got.Action != "dial_video" || got.Target != "15551234567" {
		t.Fatalf("control = %+v", got)
	}
}

func TestVideoBridgeControlReportsCommandFailure(t *testing.T) {
	vb := &videoBridge{}
	vb.OnControl(func(vbControl) error { return errors.New("no active call") })
	req := newControlRequest(`{"action":"hangup"}`)
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestVideoBridgeControlRejectsUnknownAction(t *testing.T) {
	vb := &videoBridge{}
	vb.OnControl(func(vbControl) error { return nil })
	req := newControlRequest(`{"action":"explode"}`)
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestVideoBridgeControlDispatchesReaction(t *testing.T) {
	vb := &videoBridge{}
	var got vbControl
	vb.OnControl(func(command vbControl) error {
		got = command
		return nil
	})
	req := newControlRequest(`{"action":"reaction","emoji":"👍"}`)
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusNoContent || got.Action != "reaction" || got.Emoji != "👍" {
		t.Fatalf("reaction control = (%d, %+v)", rec.Code, got)
	}
}

func TestVideoBridgeControlDispatchesCallLinkAndParticipantStateFields(t *testing.T) {
	vb := &videoBridge{}
	var got []vbControl
	vb.OnControl(func(command vbControl) error {
		got = append(got, command)
		return nil
	})
	for _, body := range []string{
		`{"action":"join_call_link_video","token":"TOKEN"}`,
		`{"action":"start_screen_share","screen_share_id":1,"has_screen_share_id":true}`,
		`{"action":"set_approval_required","enabled":true}`,
		`{"action":"admit_waiting_user","user":"242653052539031@lid"}`,
		`{"action":"deny_waiting_user","user":"15551234567@s.whatsapp.net"}`,
	} {
		req := newControlRequest(body)
		rec := httptest.NewRecorder()
		vb.handleControl(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d", body, rec.Code)
		}
	}
	if got[0].Token != "TOKEN" || !got[1].HasScreenShareID ||
		got[1].ScreenShareID != 1 || !got[2].Enabled ||
		got[3].User != "242653052539031@lid" ||
		got[4].User != "15551234567@s.whatsapp.net" {
		t.Fatalf("controls = %#v", got)
	}
}

func TestVideoBridgeControlDispatchesParticipantTargets(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	vb := &videoBridge{}
	var got vbControl
	vb.OnControl(func(command vbControl) error {
		got = command
		return nil
	})
	req := newControlRequest(
		`{"action":"add_participants","targets":["15551234567","15557654321"]}`,
	)
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got.Action != "add_participants" || len(got.Targets) != 2 ||
		got.Targets[0] != "15551234567" || got.Targets[1] != "15557654321" {
		t.Fatalf("participant control = %+v", got)
	}
}

func TestVideoBridgeControlDispatchesParticipantRing(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	vb := &videoBridge{}
	var got vbControl
	vb.OnControl(func(command vbControl) error {
		got = command
		return nil
	})
	req := newControlRequest(
		`{"action":"ring_participant","target":"74170125783269@lid"}`,
	)
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got.Action != "ring_participant" || got.Target != "74170125783269@lid" {
		t.Fatalf("participant ring control = %+v", got)
	}
}

func TestVideoBridgeControlDispatchesStartGroupAudioTargets(t *testing.T) {
	vb := &videoBridge{}
	var got vbControl
	vb.OnControl(func(command vbControl) error {
		got = command
		return nil
	})
	req := newControlRequest(
		`{"action":"start_group_audio","targets":["15551234567","15557654321"]}`,
	)
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got.Action != "start_group_audio" || len(got.Targets) != 2 ||
		got.Targets[0] != "15551234567" || got.Targets[1] != "15557654321" {
		t.Fatalf("start group audio control = %+v", got)
	}
}

func TestVideoBridgeControlDispatchesGroupIDVideoStart(t *testing.T) {
	vb := &videoBridge{}
	var got vbControl
	vb.OnControl(func(command vbControl) error {
		got = command
		return nil
	})
	req := newControlRequest(
		`{"action":"start_group_id_video","group_id":"120363411251996986@g.us"}`,
	)
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got.Action != "start_group_id_video" ||
		got.GroupID != "120363411251996986@g.us" {
		t.Fatalf("group-ID control = %+v", got)
	}
}

func TestVideoBridgeStartGroupAudioReportsControllerTargetValidation(t *testing.T) {
	vb := &videoBridge{}
	c := &webCallController{ctx: context.Background()}
	vb.OnControl(c.control)
	req := newControlRequest(
		`{"action":"start_group_audio","targets":["15551234567"]}`,
	)
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "at least two distinct participant targets are required" {
		t.Fatalf("body = %q, want controller target validation", got)
	}
}

func TestVideoBridgePageSeparatesStartGroupAndAddPeopleControls(t *testing.T) {
	for _, behavior := range []string{
		`id="startGroupAudio"`,
		`id="startGroupAudio" disabled`,
		`id="addParticipants" disabled`,
		"Start group audio",
		"Add people",
		"invoke('start_group_audio',{targets:participantTargets()})",
		"invoke('add_participants',{targets:participantTargets()})",
		"function updatePeopleControls(s)",
		"s.event==='ready'||(s.event==='phase'&&s.phase===4)",
		"s.event==='idle'||s.event==='ended'",
		"['incoming','dialing','group_dialing','answering'].includes(s.event)||s.event==='phase'||s.event==='pairing'",
		"$('startGroupIDVideo').disabled=true;$('addParticipants').disabled=false",
		"$('startGroupIDVideo').disabled=true;$('addParticipants').disabled=true",
		"updatePeopleControls(s)",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
}

func TestVideoBridgePageStartsGroupCallsFromGroupID(t *testing.T) {
	for _, behavior := range []string{
		`id="groupID"`,
		"invokeVideoCall('start_group_id_video',{group_id:$('groupID').value.trim()})",
		"invoke('start_group_id_audio',{group_id:$('groupID').value.trim()})",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
}

func TestVideoBridgePageCoordinatesCameraWithVideoTransitions(t *testing.T) {
	for _, behavior := range []string{
		"async function startCamera()",
		"invokeVideoCall('start_video')",
		"await control('stop_video')",
		"await stopCamera()",
		"invoke('disable_video')",
		"invoke('enable_video')",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
}

func TestVideoBridgePageClampsCameraToAVCLevel31(t *testing.T) {
	// Source of truth: live web example failure on group call
	// E9707AFBD7A2DCD45B4BAA54EAB1017F at 2026-07-26 00:19:57 Europe/Zurich.
	for _, behavior := range []string{
		"const avcLevel31MaxWidth=1280,avcLevel31MaxHeight=720",
		"width:{ideal:1280,max:1280}",
		"height:{ideal:720,max:720}",
		"function avcLevel31Size(width,height)",
		"const size=avcLevel31Size(f.displayWidth,f.displayHeight)",
		"width:size.width,height:size.height",
		"activeEncoder.state==='configured'",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
}

func TestVideoBridgeParticipantInviteEventDoesNotReplaceReplayState(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/302ff288df89adef44cda74f74da6285b6f13aa2/datasheets/web-group-participant-invite.md#L23-L94
	vb := &videoBridge{}
	vb.PublishState(webCallState{Event: "ready", CallID: "CID"})
	replay := string(vb.state)

	vb.PublishEvent(webParticipantInviteResult{
		Event: "participant_invite", CallID: "CID", Target: "15551234567", Success: true,
	})

	if string(vb.state) != replay {
		t.Fatalf("replay state changed from %s to %s", replay, vb.state)
	}
}

func TestVideoBridgeReplaysLatestGroupStateAfterLifecycleState(t *testing.T) {
	vb := &videoBridge{subs: make(map[chan vbMsg]struct{})}
	vb.PublishState(webCallState{Event: "ready", CallID: "CID"})
	vb.PublishGroupState(webGroupCallState{
		Event: "group_state", CallID: "CID", TransactionID: 18,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/in", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	vb.handleIn(rec, req)

	body := rec.Body.String()
	readyAt := strings.Index(body, `"event":"ready"`)
	groupAt := strings.Index(body, `"event":"group_state"`)
	if readyAt < 0 || groupAt < 0 || readyAt >= groupAt {
		t.Fatalf("SSE replay order is not lifecycle then roster: %s", body)
	}
	vb.ClearGroupState("OTHER")
	if len(vb.groupState) == 0 {
		t.Fatal("clearing a different call removed the roster replay")
	}
	vb.ClearGroupState("CID")
	if len(vb.groupState) != 0 || vb.groupCallID != "" {
		t.Fatal("ended call roster replay was not cleared")
	}
}

func TestVideoBridgeServesCurrentPairingQR(t *testing.T) {
	vb := &videoBridge{}
	vb.setQRCodePNG([]byte("png-bytes"))
	req := httptest.NewRequest(http.MethodGet, "/qr.png", nil)
	rec := httptest.NewRecorder()

	vb.handleQRCode(rec, req)

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("QR response = (%d, %q)", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "png-bytes" {
		t.Fatalf("QR body = %q", rec.Body.String())
	}
}
