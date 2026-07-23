package meowcaller

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func testEngineWithOutgoingCall() (*engine, *Call) {
	c := &Client{}
	c.eng = newEngine(c)
	call := &Call{eng: c.eng, id: "CID", peer: peerJID(), phase: CallPhaseCalling}
	c.eng.calls[call.ID()] = &engineCall{
		call:        call,
		direction:   CallDirectionOutgoing,
		from:        peerJID(),
		localVideo:  true,
		remoteVideo: true,
		codec:       AudioCodecMlow,
	}
	return c.eng, call
}

func senderVideoState(sender *videoSender) (active, gated bool) {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return sender.active, sender.sendGated
}

func TestCallVideoUpgradeGatesUntilPeerAcceptAndCanStop(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.localVideo = false
	m.videoTx = &videoSender{}
	var states []types.CallVideoState
	eng.setCallVideo = func(_ context.Context, _ string, state types.CallVideoState, _ *int) error {
		states = append(states, state)
		return nil
	}

	if err := call.StartVideo(); err != nil {
		t.Fatalf("StartVideo: %v", err)
	}
	if len(states) != 1 || states[0] != types.CallVideoStateUpgradeRequestV2 {
		t.Fatalf("StartVideo states = %v, want [11]", states)
	}
	if active, gated := senderVideoState(m.videoTx); !active || !gated {
		t.Fatalf("upgrade sender = active:%v gated:%v, want true,true", active, gated)
	}

	eng.onVideo(&events.CallVideo{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		State:         types.CallVideoStateUpgradeAccept,
	})
	if len(states) != 2 || states[1] != types.CallVideoStateEnabled {
		t.Fatalf("accepted states = %v, want [11 1]", states)
	}
	if active, gated := senderVideoState(m.videoTx); !active || gated {
		t.Fatalf("accepted sender = active:%v gated:%v, want true,false", active, gated)
	}

	if err := call.StopVideo(); err != nil {
		t.Fatalf("StopVideo: %v", err)
	}
	if len(states) != 3 || states[2] != types.CallVideoStateStopped {
		t.Fatalf("stopped states = %v, want [11 1 6]", states)
	}
	if call.IsSendingVideo() || !call.IsReceivingVideo() || !call.IsVideo() {
		t.Fatalf("flows after local stop = send:%v receive:%v any:%v", call.IsSendingVideo(), call.IsReceivingVideo(), call.IsVideo())
	}
}

func TestCallAcceptVideoPreservesDisabledLocalFlow(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.localVideo = false
	m.peerVideoUpgrade = true
	m.videoTx = &videoSender{}
	var states []types.CallVideoState
	eng.setCallVideo = func(_ context.Context, _ string, state types.CallVideoState, _ *int) error {
		states = append(states, state)
		return nil
	}

	if err := call.AcceptVideo(); err != nil {
		t.Fatalf("AcceptVideo: %v", err)
	}
	if len(states) != 2 || states[0] != types.CallVideoStateStopped || states[1] != types.CallVideoStateUpgradeAccept {
		t.Fatalf("AcceptVideo states = %v, want [6 4]", states)
	}
	if call.IsSendingVideo() {
		t.Fatal("accepting peer video enabled the local sender")
	}
}

func TestInboundVideoStopOnlyDisablesRemoteFlow(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.videoTx = &videoSender{active: true}
	eng.onVideo(&events.CallVideo{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		State:         types.CallVideoStateStopped,
	})
	if active, _ := senderVideoState(m.videoTx); !active {
		t.Fatal("peer stopping video disabled the local sender")
	}
	if !call.IsSendingVideo() || call.IsReceivingVideo() || !call.IsVideo() {
		t.Fatalf("flows after peer stop = send:%v receive:%v any:%v", call.IsSendingVideo(), call.IsReceivingVideo(), call.IsVideo())
	}
}

func TestInboundVideoEnabledReleasesPendingLocalUpgrade(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.localVideo = true
	m.remoteVideo = false
	m.videoGate = true
	m.videoTx = &videoSender{active: true, sendGated: true}
	var keyframes int
	call.OnVideoKeyframeRequest(func() { keyframes++ })

	eng.onVideo(&events.CallVideo{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		State:         types.CallVideoStateEnabled,
	})
	if active, gated := senderVideoState(m.videoTx); !active || gated {
		t.Fatalf("sender after peer enabled = active:%v gated:%v, want true,false", active, gated)
	}
	if keyframes != 1 {
		t.Fatalf("keyframe requests = %d, want 1", keyframes)
	}
}

func TestInboundVideoUpgradeWaitsForExplicitAcceptance(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.localVideo = false
	m.videoTx = &videoSender{}
	var got VideoState
	call.OnVideoState(func(state VideoState) { got = state })

	eng.onVideo(&events.CallVideo{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		State:         types.CallVideoStateUpgradeRequestV2,
	})
	if !got.Upgrade || got.Raw != int(types.CallVideoStateUpgradeRequestV2) || !m.peerVideoUpgrade {
		t.Fatalf("upgrade event = %+v, pending:%v", got, m.peerVideoUpgrade)
	}
	if active, _ := senderVideoState(m.videoTx); active {
		t.Fatal("peer upgrade activated local video")
	}
}

func TestCallSetsVideoOrientation(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var state types.CallVideoState
	var orientation int
	eng.setCallVideo = func(_ context.Context, _ string, gotState types.CallVideoState, gotOrientation *int) error {
		state = gotState
		orientation = *gotOrientation
		return nil
	}
	if err := call.SetVideoOrientation(2); err != nil {
		t.Fatalf("SetVideoOrientation: %v", err)
	}
	if state != types.CallVideoStateEnabled || orientation != 2 {
		t.Fatalf("orientation transition = (%d, %d), want (1, 2)", state, orientation)
	}
	if err := call.SetVideoOrientation(4); err == nil {
		t.Fatal("SetVideoOrientation accepted orientation 4")
	}
}

func TestOutgoingPeerAcceptLifecycleAndRekey(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.peerLID = peerJID().String()
	answeringDevice := peerJID()
	answeringDevice.Device = 7
	var rekeyed string
	m.rekeyPeer = func(peer string) error { rekeyed = peer; return nil }

	eng.onPreAccept(&events.CallPreAccept{BasicCallMeta: types.BasicCallMeta{CallID: call.ID()}})
	if call.State() != CallPhaseRinging {
		t.Fatalf("phase after preaccept = %d, want Ringing", call.State())
	}
	eng.onAccept(&events.CallAccept{BasicCallMeta: types.BasicCallMeta{CallID: call.ID(), From: answeringDevice}})
	if call.State() != CallPhaseConnecting || rekeyed != answeringDevice.String() {
		t.Fatalf("accept = phase:%d rekey:%q", call.State(), rekeyed)
	}
	if call.Peer() != answeringDevice {
		t.Fatalf("call peer = %s, want %s", call.Peer(), answeringDevice)
	}
}

func TestCallMediaStopEndsCallOnce(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var canceled, ended int
	eng.calls[call.ID()].cancel = func() { canceled++ }
	call.OnEnd(func(reason string) {
		if reason != "hangup" {
			t.Errorf("reason = %q, want hangup", reason)
		}
		ended++
	})
	event := &events.CallMediaStop{BasicCallMeta: types.BasicCallMeta{CallID: call.ID()}, Reason: "hangup"}
	eng.onMediaStop(event)
	eng.onMediaStop(event)
	if canceled != 1 || ended != 1 || call.State() != CallPhaseEnded {
		t.Fatalf("stop = canceled:%d ended:%d phase:%d", canceled, ended, call.State())
	}
}

func TestCallMuteEventReachesListener(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var muted bool
	call.OnMuteState(func(value bool) { muted = value })
	eng.onMute(&events.CallMute{BasicCallMeta: types.BasicCallMeta{CallID: call.ID()}, Muted: true})
	if !muted {
		t.Fatal("mute event did not reach call listener")
	}
}

func TestCallSetMutedUsesWhatsmeowControlPlane(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	var got bool
	eng.setCallMute = func(_ context.Context, callID string, muted bool) error {
		if callID != call.ID() {
			t.Fatalf("call ID = %q, want %q", callID, call.ID())
		}
		got = muted
		return nil
	}
	if err := call.SetMuted(true); err != nil {
		t.Fatalf("SetMuted: %v", err)
	}
	if !got {
		t.Fatal("SetMuted did not send the local mute state")
	}
}

func TestEngineInviteParticipantDelegatesResolvedTarget(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/160912971e6bc2a4aa79ac3aafcf08360075e3fc/datasheets/api-group-participant-invite.md#L23-L100
	eng, call := testEngineWithOutgoingCall()
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("invite"), "preserved")
	var gotContext context.Context
	var gotCallID string
	var gotTarget types.JID
	eng.inviteCallParticipant = func(callCtx context.Context, callID string, target types.JID) error {
		gotContext = callCtx
		gotCallID = callID
		gotTarget = target
		return nil
	}

	if err := eng.inviteParticipant(ctx, call.ID(), "  +15551234567  "); err != nil {
		t.Fatalf("inviteParticipant: %v", err)
	}
	if gotContext != ctx {
		t.Fatal("inviteParticipant did not preserve the caller context")
	}
	if gotCallID != call.ID() {
		t.Fatalf("call ID = %q, want %q", gotCallID, call.ID())
	}
	wantTarget := types.NewJID("15551234567", types.DefaultUserServer)
	if gotTarget != wantTarget {
		t.Fatalf("target = %s, want %s", gotTarget, wantTarget)
	}
}

func TestEngineInviteParticipantRejectsInvalidStateAndPreservesFailure(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/160912971e6bc2a4aa79ac3aafcf08360075e3fc/datasheets/api-group-participant-invite.md#L23-L100
	eng, call := testEngineWithOutgoingCall()
	if err := eng.inviteParticipant(context.Background(), call.ID(), " "); err == nil {
		t.Fatal("empty target accepted")
	}
	eng.inviteCallParticipant = nil
	if err := eng.inviteParticipant(context.Background(), call.ID(), "15551234567"); err == nil {
		t.Fatal("unavailable signaling accepted")
	}

	sentinel := errors.New("invite rejected")
	invites := 0
	eng.inviteCallParticipant = func(context.Context, string, types.JID) error {
		invites++
		return sentinel
	}
	if err := eng.inviteParticipant(context.Background(), call.ID(), "15551234567"); !errors.Is(err, sentinel) {
		t.Fatalf("inviteParticipant error = %v, want wrapped sentinel", err)
	}

	invites = 0
	call.setPhase(CallPhaseEnded)
	if err := eng.inviteParticipant(context.Background(), call.ID(), "15551234567"); err == nil {
		t.Fatal("ended call accepted participant invite")
	}
	if invites != 0 {
		t.Fatal("ended call delegated participant invite")
	}
}

func TestCallAddParticipantDelegatesSingularInvite(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/160912971e6bc2a4aa79ac3aafcf08360075e3fc/datasheets/api-group-participant-invite.md#L23-L100
	eng, call := testEngineWithOutgoingCall()
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("invite"), "preserved")
	var gotContext context.Context
	var gotCallID string
	var gotTarget types.JID
	eng.inviteCallParticipant = func(callCtx context.Context, callID string, target types.JID) error {
		gotContext = callCtx
		gotCallID = callID
		gotTarget = target
		return nil
	}

	if err := call.AddParticipant(ctx, "  +15551234567  "); err != nil {
		t.Fatalf("AddParticipant: %v", err)
	}
	if gotContext != ctx {
		t.Fatal("AddParticipant did not preserve the caller context")
	}
	if gotCallID != call.ID() {
		t.Fatalf("call ID = %q, want %q", gotCallID, call.ID())
	}
	wantTarget := types.NewJID("15551234567", types.DefaultUserServer)
	if gotTarget != wantTarget {
		t.Fatalf("target = %s, want %s", gotTarget, wantTarget)
	}
}

func TestCallAddParticipantValidationAndFailure(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/160912971e6bc2a4aa79ac3aafcf08360075e3fc/datasheets/api-group-participant-invite.md#L23-L100
	eng, call := testEngineWithOutgoingCall()
	if err := call.AddParticipant(context.Background(), " "); err == nil {
		t.Fatal("empty target accepted")
	}
	eng.inviteCallParticipant = nil
	if err := call.AddParticipant(context.Background(), "15551234567"); err == nil {
		t.Fatal("unavailable signaling accepted")
	}

	sentinel := errors.New("invite rejected")
	eng.inviteCallParticipant = func(context.Context, string, types.JID) error {
		return sentinel
	}
	if err := call.AddParticipant(context.Background(), "15551234567"); !errors.Is(err, sentinel) {
		t.Fatalf("AddParticipant error = %v, want wrapped sentinel", err)
	}

	call.setPhase(CallPhaseEnded)
	if err := call.AddParticipant(context.Background(), "15551234567"); err == nil {
		t.Fatal("ended call accepted participant invite")
	}
}

func TestCallAddParticipantsRetainsOrderedResults(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/160912971e6bc2a4aa79ac3aafcf08360075e3fc/datasheets/api-group-participant-invite.md#L23-L100
	eng, call := testEngineWithOutgoingCall()
	sentinel := errors.New("second invite failed")
	var got []string
	eng.inviteCallParticipant = func(_ context.Context, _ string, target types.JID) error {
		got = append(got, target.User)
		if len(got) == 2 {
			return sentinel
		}
		return nil
	}

	results := call.AddParticipants(context.Background(), "10001", "10002", "10003")
	if len(results) != 3 {
		t.Fatalf("result count = %d, want 3", len(results))
	}
	if results[0] != nil || !errors.Is(results[1], sentinel) || results[2] != nil {
		t.Fatalf("results = %v, want [nil sentinel nil]", results)
	}
	if len(got) != 3 || got[0] != "10001" || got[1] != "10002" || got[2] != "10003" {
		t.Fatalf("invite order = %v, want [10001 10002 10003]", got)
	}
}
