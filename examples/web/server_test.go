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

func TestVideoBridgeControlDispatchesJSONCommand(t *testing.T) {
	vb := &videoBridge{}
	var got vbControl
	vb.OnControl(func(command vbControl) error {
		got = command
		return nil
	})
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(`{"action":"dial_video","target":"15551234567"}`))
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
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(`{"action":"hangup"}`))
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestVideoBridgeControlRejectsUnknownAction(t *testing.T) {
	vb := &videoBridge{}
	vb.OnControl(func(vbControl) error { return nil })
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(`{"action":"explode"}`))
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
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(`{"action":"reaction","emoji":"👍"}`))
	rec := httptest.NewRecorder()

	vb.handleControl(rec, req)

	if rec.Code != http.StatusNoContent || got.Action != "reaction" || got.Emoji != "👍" {
		t.Fatalf("reaction control = (%d, %+v)", rec.Code, got)
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
	req := httptest.NewRequest(http.MethodPost, "/control", bytes.NewBufferString(
		`{"action":"add_participants","targets":["15551234567","15557654321"]}`,
	))
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
