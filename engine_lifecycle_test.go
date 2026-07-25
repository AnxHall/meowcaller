package meowcaller

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func testEngineWithOutgoingCall() (*engine, *Call) {
	c := &Client{log: zerolog.Nop()}
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

func TestEngineGroupUpdatePreservesOriginalPeerAndQueuesLatestRoster(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	originalPeer := call.Peer()
	added := mediaTestJID("333333333333333", 43)
	first := types.GroupCallUpdate{CallID: call.ID(), TransactionID: 16}
	eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID(), From: added},
		Update:        first,
	})
	m := eng.calls[call.ID()]
	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 16 {
		t.Fatalf("queued group update = %#v, want transaction 16", m.groupUpdate)
	}
	if call.Peer() != originalPeer {
		t.Fatalf("group update replaced original peer with %s", call.Peer())
	}

	var applied []uint32
	m.applyGroupUpdate = func(update types.GroupCallUpdate) error {
		applied = append(applied, update.TransactionID)
		return nil
	}
	second := types.GroupCallUpdate{CallID: call.ID(), TransactionID: 18}
	eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID(), From: added},
		Update:        second,
	})
	if len(applied) != 1 || applied[0] != 18 {
		t.Fatalf("applied transactions = %v, want [18]", applied)
	}
	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 18 {
		t.Fatalf("latest group update = %#v, want transaction 18", m.groupUpdate)
	}
	if call.Peer() != originalPeer {
		t.Fatalf("second group update replaced original peer with %s", call.Peer())
	}

	eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID(), From: added},
		Update:        first,
	})
	if len(applied) != 1 {
		t.Fatalf("stale group update reached live callback: %v", applied)
	}
	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 18 {
		t.Fatalf("stale group update replaced transaction 18: %#v", m.groupUpdate)
	}

	m.applyGroupUpdate = func(update types.GroupCallUpdate) error {
		applied = append(applied, update.TransactionID)
		if update.TransactionID == 20 {
			return errors.New("invalid roster")
		}
		return nil
	}
	rejected := types.GroupCallUpdate{CallID: call.ID(), TransactionID: 20}
	eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID(), From: added},
		Update:        rejected,
	})
	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 18 {
		t.Fatalf("rejected update advanced cached roster: %#v", m.groupUpdate)
	}
	recovery := types.GroupCallUpdate{CallID: call.ID(), TransactionID: 19}
	eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID(), From: added},
		Update:        recovery,
	})
	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 19 {
		t.Fatalf("valid update below rejected transaction was blocked: %#v", m.groupUpdate)
	}
	if len(applied) != 3 || applied[1] != 20 || applied[2] != 19 {
		t.Fatalf("error/recovery callbacks = %v, want [18 20 19]", applied)
	}
}

func TestCallGroupStatePublishesSanitizedAuthoritativeRosterBeforeMedia(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	self := mediaTestJID("111111111111111", 14)
	peer := mediaTestJID("222222222222222", 0)
	pending := mediaTestJID("333333333333333", 43)
	update := types.GroupCallUpdate{
		CallID: call.ID(), TransactionID: 17, RekeyRequested: true,
		Relay: &types.GroupCallRelay{
			Key: []byte("must-not-escape"), Tokens: [][]byte{[]byte("must-not-escape")},
		},
		Participants: []types.GroupCallParticipant{
			{
				JID: self.ToNonAD(), State: "connected",
				Devices: []types.GroupCallDevice{{
					JID: self, PID: 1, HasPID: true, Platform: "web",
					Capability: []byte("must-not-escape"),
				}},
			},
			{
				JID: peer.ToNonAD(), PN: types.NewJID("15550002", types.DefaultUserServer),
				State: "connected",
				Devices: []types.GroupCallDevice{{
					JID: peer, PID: 0, HasPID: true, Platform: "iphone",
				}},
			},
			{
				JID: pending.ToNonAD(), State: "receipt",
				Devices: []types.GroupCallDevice{{JID: pending}},
			},
		},
	}
	eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		Update:        update,
	})

	state, ok := call.GroupState()
	if !ok {
		t.Fatal("call did not cache group state before media startup")
	}
	if state.TransactionID != 17 || !state.RekeyRequested || len(state.Participants) != 3 {
		t.Fatalf("group state = %+v", state)
	}
	if got := state.Participants[1].Devices[0]; got.PID != 0 || !got.HasPID || got.Platform != "iphone" {
		t.Fatalf("PID-zero selected device = %+v", got)
	}
	if state.Participants[2].State != "receipt" || state.Participants[2].Devices[0].HasPID {
		t.Fatalf("receipt participant = %+v", state.Participants[2])
	}
	update.Participants[1].State = "mutated"
	update.Participants[1].Devices[0].Platform = "mutated"
	if got, _ := call.GroupState(); got.Participants[1].State != "connected" ||
		got.Participants[1].Devices[0].Platform != "iphone" {
		t.Fatal("cached public group state aliases the Whatsmeow event")
	}

	var replay GroupCallState
	var replayFirstState string
	call.OnGroupState(func(got GroupCallState) {
		replay = got
		replayFirstState = got.Participants[0].State
		got.Participants[0].State = "callback-mutated"
	})
	if replay.TransactionID != 17 || replayFirstState != "connected" {
		t.Fatalf("late group-state replay = %+v", replay)
	}
	if got, _ := call.GroupState(); got.Participants[0].State != "connected" {
		t.Fatal("callback mutation changed cached group state")
	}
}

func TestCallGroupStateNotifiesOnlyAcceptedNewerTransactions(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	var transactions []uint32
	call.OnGroupState(func(state GroupCallState) {
		transactions = append(transactions, state.TransactionID)
	})
	m.applyGroupUpdate = func(update types.GroupCallUpdate) error {
		if update.TransactionID == 18 {
			return errors.New("invalid roster")
		}
		return nil
	}
	for _, transactionID := range []uint32{17, 17, 18, 16, 19} {
		eng.onGroupUpdate(&events.CallGroupUpdate{
			BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
			Update: types.GroupCallUpdate{
				CallID: call.ID(), TransactionID: transactionID,
			},
		})
	}
	if !slices.Equal(transactions, []uint32{17, 19}) {
		t.Fatalf("notified transactions = %v, want [17 19]", transactions)
	}
	call.setPhase(CallPhaseEnded)
	eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		Update:        types.GroupCallUpdate{CallID: call.ID(), TransactionID: 20},
	})
	if !slices.Equal(transactions, []uint32{17, 19}) {
		t.Fatalf("ended call notified group state: %v", transactions)
	}
}

func TestEngineQueuesAndRoutesSharedGroupEpochWithDistributorMetadata(t *testing.T) {
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	author := mediaTestJID("333333333333333", 43)
	creator := mediaTestJID("111111111111111", 14)
	rawKey := bytes.Repeat([]byte{0xa5}, 32)
	event := &events.CallEncRekey{
		BasicCallMeta: types.BasicCallMeta{
			CallID: call.ID(), From: author, CallCreator: creator,
		},
		Rekey:  types.GroupCallEncRekey{TransactionID: 17},
		RawKey: rawKey,
	}
	eng.onEncRekey(event)
	rawKey[0] ^= 0xff
	if len(m.pendingGroupRekeys) != 1 {
		t.Fatalf("queued group epochs = %d, want 1", len(m.pendingGroupRekeys))
	}
	if m.pendingGroupRekeys[0].From != author || m.pendingGroupRekeys[0].RawKey[0] != 0xa5 {
		t.Fatalf("queued group epoch = %+v", m.pendingGroupRekeys[0])
	}

	var applied events.CallEncRekey
	m.applyGroupRekey = func(got events.CallEncRekey) error {
		applied = got
		return nil
	}
	next := &events.CallEncRekey{
		BasicCallMeta: types.BasicCallMeta{
			CallID: call.ID(), From: author, CallCreator: creator,
		},
		Rekey:  types.GroupCallEncRekey{TransactionID: 18},
		RawKey: bytes.Repeat([]byte{0x5a}, 32),
	}
	eng.onEncRekey(next)
	if applied.From != author || applied.From == creator || applied.Rekey.TransactionID != 18 {
		t.Fatalf("applied group epoch = %+v", applied)
	}

	m.applyGroupRekey = nil
	eng.onEncRekey(next)
	if len(m.pendingGroupRekeys) != 2 {
		t.Fatalf("queued group epochs before end = %d, want 2", len(m.pendingGroupRekeys))
	}
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L122
	pendingRelayKey := bytes.Repeat([]byte{0xc3}, 32)
	m.pendingGroupUpdate = &types.GroupCallUpdate{
		CallID: call.ID(),
		Relay:  &types.GroupCallRelay{Key: pendingRelayKey},
	}
	m.applyGroupUpdate = func(types.GroupCallUpdate) error { return nil }
	m.groupActivating = true
	m.groupActive = true
	eng.finishCall(call.ID(), "ended")
	if len(m.pendingGroupRekeys) != 0 || m.applyGroupRekey != nil {
		t.Fatal("call end retained pending group key material or callback")
	}
	if m.pendingGroupUpdate != nil || !allZero(pendingRelayKey) {
		t.Fatal("call end retained pending group roster key material")
	}
	if m.applyGroupUpdate != nil || m.groupActivating || m.groupActive {
		t.Fatal("call end retained group roster callback or activation state")
	}
	applied = events.CallEncRekey{}
	eng.onEncRekey(next)
	if applied.Rekey.TransactionID != 0 {
		t.Fatal("ended call applied a participant rekey")
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
