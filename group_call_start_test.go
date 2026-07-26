package meowcaller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestClientGroupCallNormalizesDeduplicatesAndDelegatesOnce(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L115
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("group-call"), "preserved")
	first := types.NewJID("111111111111111", types.HiddenUserServer)
	second := types.NewJID("15550002", types.DefaultUserServer)
	groupJID := types.NewJID("120363411251996986", types.GroupServer)

	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	var calls int
	var gotContext context.Context
	var gotTargets []types.JID
	var gotOptions []whatsmeow.GroupCallOfferOptions
	c.eng.offerGroupCall = func(
		callCtx context.Context,
		targets []types.JID,
		options ...whatsmeow.GroupCallOfferOptions,
	) (string, error) {
		calls++
		gotContext = callCtx
		gotTargets = append([]types.JID(nil), targets...)
		gotOptions = append([]whatsmeow.GroupCallOfferOptions(nil), options...)
		return "GROUP-CID", nil
	}

	call, err := c.GroupCallWithOptions(ctx, []string{
		" 111111111111111:7@lid ",
		"111111111111111@lid",
		" +15550002 ",
		"15550002@s.whatsapp.net",
	}, GroupCallOptions{GroupJID: " 120363411251996986@g.us ", Video: true})
	if err != nil {
		t.Fatalf("GroupCallWithOptions: %v", err)
	}
	if calls != 1 || gotContext != ctx {
		t.Fatalf("delegation = calls:%d context_preserved:%t, want 1,true", calls, gotContext == ctx)
	}
	if !slices.Equal(gotTargets, []types.JID{first, second}) {
		t.Fatalf("delegated targets = %v, want [%s %s]", gotTargets, first, second)
	}
	if len(gotOptions) != 1 || gotOptions[0].GroupJID != groupJID || !gotOptions[0].Video {
		t.Fatalf("delegated options = %+v, want video group JID %s", gotOptions, groupJID)
	}
	if call.ID() != "GROUP-CID" || call.Peer() != first || call.State() != CallPhaseCalling {
		t.Fatalf("call = id:%q peer:%s phase:%d", call.ID(), call.Peer(), call.State())
	}
	state, ok := call.GroupState()
	if !ok || state.TransactionID != 0 || len(state.Participants) != 2 {
		t.Fatalf("selected group seed = (%+v, %t), want transaction 0 with two remotes", state, ok)
	}
	if state.Participants[0].JID != first || state.Participants[0].State != "outgoing" ||
		state.Participants[1].JID != second || state.Participants[1].State != "outgoing" {
		t.Fatalf("selected group seed participants = %+v", state.Participants)
	}
	var replay GroupCallState
	call.OnGroupState(func(state GroupCallState) {
		replay = state
	})
	if replay.TransactionID != 0 || len(replay.Participants) != 2 {
		t.Fatalf("selected group seed replay = %+v", replay)
	}
	m := c.eng.calls[call.ID()]
	if m == nil || !m.group || m.groupUpdate != nil {
		t.Fatalf("engine group state = %+v, want explicit group with no speculative media roster", m)
	}
	if !m.localVideo || !m.remoteVideo || !call.IsVideo() {
		t.Fatalf("video group state = local:%t remote:%t call:%t, want all true", m.localVideo, m.remoteVideo, call.IsVideo())
	}
}

func TestClientGroupCallByIDResolvesRemoteMembersAndBindsGroup(t *testing.T) {
	ctx := context.Background()
	groupJID := types.NewJID("120363411251996986", types.GroupServer)
	selfPN := types.NewJID("15550001", types.DefaultUserServer)
	selfLID := types.NewJID("111111111111111", types.HiddenUserServer)
	first := types.NewJID("222222222222222", types.HiddenUserServer)
	second := types.NewJID("15550003", types.DefaultUserServer)

	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	c.getGroupInfo = func(gotContext context.Context, gotJID types.JID) (*types.GroupInfo, error) {
		if gotContext != ctx || gotJID != groupJID {
			t.Fatalf("group lookup = (%p, %s), want (%p, %s)", gotContext, gotJID, ctx, groupJID)
		}
		return &types.GroupInfo{
			JID: groupJID,
			Participants: []types.GroupParticipant{
				{JID: selfLID, PhoneNumber: selfPN, LID: selfLID},
				{JID: first, LID: first},
				{JID: second, PhoneNumber: second},
				{JID: first, LID: first},
			},
		}, nil
	}
	c.ownGroupJIDs = func() []types.JID {
		return []types.JID{selfPN, selfLID}
	}
	var gotTargets []types.JID
	var gotOptions []whatsmeow.GroupCallOfferOptions
	c.eng.offerGroupCall = func(
		_ context.Context,
		targets []types.JID,
		options ...whatsmeow.GroupCallOfferOptions,
	) (string, error) {
		gotTargets = append([]types.JID(nil), targets...)
		gotOptions = append([]whatsmeow.GroupCallOfferOptions(nil), options...)
		return "GROUP-BY-ID-CID", nil
	}

	call, err := c.GroupCallByIDWithOptions(ctx, " 120363411251996986 ", GroupCallOptions{Video: true})
	if err != nil {
		t.Fatalf("GroupCallByIDWithOptions: %v", err)
	}
	if !slices.Equal(gotTargets, []types.JID{first, second}) {
		t.Fatalf("resolved targets = %v, want [%s %s]", gotTargets, first, second)
	}
	if len(gotOptions) != 1 || gotOptions[0].GroupJID != groupJID || !gotOptions[0].Video {
		t.Fatalf("resolved options = %+v, want bound video group %s", gotOptions, groupJID)
	}
	if call.ID() != "GROUP-BY-ID-CID" {
		t.Fatalf("call ID = %q, want GROUP-BY-ID-CID", call.ID())
	}
}

func TestClientGroupCallByIDStopsBeforeOfferWhenRosterIsNotCallable(t *testing.T) {
	groupJID := types.NewJID("120363411251996986", types.GroupServer)
	for _, tc := range []struct {
		name         string
		groupID      string
		participants []types.GroupParticipant
		wantError    string
	}{
		{
			name:      "empty group ID",
			groupID:   " ",
			wantError: "group ID is required",
		},
		{
			name:      "invalid group ID",
			groupID:   "15550001@s.whatsapp.net",
			wantError: "not a canonical g.us JID",
		},
		{
			name:    "one remote member",
			groupID: groupJID.String(),
			participants: []types.GroupParticipant{
				{JID: types.NewJID("15550001", types.DefaultUserServer)},
				{JID: types.NewJID("15550002", types.DefaultUserServer)},
			},
			wantError: "requires at least two remote members",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{log: zerolog.Nop()}
			c.eng = newEngine(c)
			c.getGroupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
				return &types.GroupInfo{JID: groupJID, Participants: tc.participants}, nil
			}
			c.ownGroupJIDs = func() []types.JID {
				return []types.JID{types.NewJID("15550001", types.DefaultUserServer)}
			}
			var offers int
			c.eng.offerGroupCall = func(
				context.Context,
				[]types.JID,
				...whatsmeow.GroupCallOfferOptions,
			) (string, error) {
				offers++
				return "CID", nil
			}

			_, err := c.GroupCallByID(context.Background(), tc.groupID)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantError)
			}
			if offers != 0 {
				t.Fatalf("invalid group roster delegated %d offers", offers)
			}
		})
	}
}

func TestClientGroupCallValidationStopsBeforeDelegation(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L115
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L108-L110
	tests := []struct {
		name    string
		targets []string
		options GroupCallOptions
	}{
		{name: "no targets"},
		{name: "one target", targets: []string{"111111111111111@lid"}},
		{
			name: "device duplicate leaves one target",
			targets: []string{
				"111111111111111:7@lid",
				"111111111111111@lid",
			},
		},
		{
			name:    "empty target",
			targets: []string{"111111111111111@lid", " "},
		},
		{
			name:    "multiple at signs",
			targets: []string{"111111111111111@lid@junk", "222222222222222@lid"},
		},
		{
			name:    "empty explicit user",
			targets: []string{"@lid", "222222222222222@lid"},
		},
		{
			name:    "group target",
			targets: []string{"120363411251996986@g.us", "222222222222222@lid"},
		},
		{
			name:    "call target",
			targets: []string{"GROUP-CID@call", "222222222222222@lid"},
		},
		{
			name:    "unknown target server",
			targets: []string{"111111111111111@example.invalid", "222222222222222@lid"},
		},
		{
			name:    "non-group optional JID",
			targets: []string{"111111111111111@lid", "222222222222222@lid"},
			options: GroupCallOptions{GroupJID: "15550002@s.whatsapp.net"},
		},
		{
			name:    "non-canonical optional group JID",
			targets: []string{"111111111111111@lid", "222222222222222@lid"},
			options: GroupCallOptions{GroupJID: "120363411251996986@g.us@invalid"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{log: zerolog.Nop()}
			c.eng = newEngine(c)
			var calls int
			c.eng.offerGroupCall = func(
				context.Context,
				[]types.JID,
				...whatsmeow.GroupCallOfferOptions,
			) (string, error) {
				calls++
				return "GROUP-CID", nil
			}

			if _, err := c.GroupCallWithOptions(context.Background(), tc.targets, tc.options); err == nil {
				t.Fatal("GroupCallWithOptions accepted invalid input")
			}
			if calls != 0 {
				t.Fatalf("invalid input delegated %d times", calls)
			}
		})
	}
}

func TestClientGroupCallLeavesPNToLIDAliasResolutionToWhatsmeow(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L115
	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	var got []types.JID
	c.eng.offerGroupCall = func(
		_ context.Context,
		targets []types.JID,
		_ ...whatsmeow.GroupCallOfferOptions,
	) (string, error) {
		got = append([]types.JID(nil), targets...)
		return "GROUP-CID", nil
	}

	_, err := c.GroupCall(
		context.Background(),
		"15550002@s.whatsapp.net",
		"15550002@lid",
	)
	if err != nil {
		t.Fatalf("GroupCall: %v", err)
	}
	if !slices.Equal(got, []types.JID{
		types.NewJID("15550002", types.DefaultUserServer),
		types.NewJID("15550002", types.HiddenUserServer),
	}) {
		t.Fatalf("delegated aliases = %v", got)
	}
}

func TestClientGroupCallPreservesOfferFailureWithoutInstallingCall(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L115
	sentinel := errors.New("offer rejected")
	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	c.eng.offerGroupCall = func(
		context.Context,
		[]types.JID,
		...whatsmeow.GroupCallOfferOptions,
	) (string, error) {
		return "GROUP-CID", sentinel
	}

	call, err := c.GroupCall(context.Background(), "111111111111111@lid", "222222222222222@lid")
	if call != nil || !errors.Is(err, sentinel) {
		t.Fatalf("GroupCall = (%v, %v), want nil wrapped sentinel", call, err)
	}
	if len(c.eng.calls) != 0 {
		t.Fatalf("failed offer installed %d engine calls", len(c.eng.calls))
	}
}

func TestGroupCallOfferFailureRemovesPlaceholderAndClearsOwnedKeys(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	first := types.NewJID("222222222222222", types.HiddenUserServer)
	second := types.NewJID("333333333333333", types.HiddenUserServer)
	connectedDevice := first
	connectedDevice.Device = 7
	self := types.NewJID("111111111111111", types.HiddenUserServer)
	self.Device = 14
	sentinel := errors.New("offer rejected")

	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	c.eng.startMedia = func(
		context.Context,
		string,
		*Call,
		[]byte,
		string,
		string,
		*types.RelayEndpoint,
	) error {
		return nil
	}
	var placeholder *engineCall
	var ownedRawKey []byte
	var ownedCallKey []byte
	var ownedRelayKey []byte
	var ownedRelayToken []byte
	var ownedRelayAuthToken []byte
	relayKey := bytes.Repeat([]byte{0x71}, 32)
	relayToken := bytes.Repeat([]byte{0x72}, 16)
	relayAuthToken := bytes.Repeat([]byte{0x73}, 16)
	c.eng.offerGroupCall = func(
		context.Context,
		[]types.JID,
		...whatsmeow.GroupCallOfferOptions,
	) (string, error) {
		c.eng.onGroupUpdate(&events.CallGroupUpdate{
			BasicCallMeta: types.BasicCallMeta{CallID: "GROUP-CID"},
			Update: types.GroupCallUpdate{
				CallID: "GROUP-CID", TransactionID: 11,
				Participants: []types.GroupCallParticipant{{
					JID: first, State: "connected",
					Devices: []types.GroupCallDevice{{
						JID: connectedDevice, PID: 1, HasPID: true,
					}},
				}},
			},
		})
		c.eng.onEncRekey(&events.CallEncRekey{
			BasicCallMeta: types.BasicCallMeta{CallID: "GROUP-CID", From: connectedDevice},
			Rekey:         types.GroupCallEncRekey{TransactionID: 11},
			RawKey:        bytes.Repeat([]byte{0x11}, 32),
		})
		c.eng.onMediaReady(&events.CallMediaReady{
			BasicCallMeta: types.BasicCallMeta{CallID: "GROUP-CID"},
			SelfLID:       self,
			PeerLID:       connectedDevice,
			CallKey:       bytes.Repeat([]byte{0xa5}, 32),
			Relay: types.RelayEndpoint{
				IPv4: "157.240.17.133", Port: 3478,
				Key: relayKey, Token: relayToken, AuthToken: relayAuthToken,
			},
			Codec:     types.CallCodecOpus,
			Direction: types.CallDirectionOutgoing,
		})
		c.eng.mu.Lock()
		placeholder = c.eng.calls["GROUP-CID"]
		ownedRawKey = placeholder.pendingGroupRekeys[0].RawKey
		ownedCallKey = placeholder.callKey
		ownedRelayKey = placeholder.relay.Key
		ownedRelayToken = placeholder.relay.Token
		ownedRelayAuthToken = placeholder.relay.AuthToken
		c.eng.mu.Unlock()
		return "GROUP-CID", sentinel
	}

	call, err := c.GroupCall(context.Background(), first.String(), second.String())
	if call != nil || !errors.Is(err, sentinel) {
		t.Fatalf("GroupCall = (%v, %v), want nil wrapped sentinel", call, err)
	}
	c.eng.mu.Lock()
	retained := c.eng.calls["GROUP-CID"]
	c.eng.mu.Unlock()
	if retained != nil {
		t.Fatalf("failed offer retained placeholder %+v", retained)
	}
	if placeholder == nil {
		t.Fatal("offer did not create the expected pre-return placeholder")
	}
	if !allZero(ownedRawKey) || !allZero(ownedCallKey) {
		t.Fatalf(
			"failed offer retained key bytes = raw_zero:%t call_zero:%t",
			allZero(ownedRawKey),
			allZero(ownedCallKey),
		)
	}
	if !allZero(ownedRelayKey) || !allZero(ownedRelayToken) ||
		!allZero(ownedRelayAuthToken) {
		t.Fatal("failed offer retained engine-owned relay credential bytes")
	}
	if !allEqual(relayKey, 0x71) || !allEqual(relayToken, 0x72) ||
		!allEqual(relayAuthToken, 0x73) {
		t.Fatal("failed offer cleanup mutated event-owned relay credential bytes")
	}
}

func allZero(data []byte) bool {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func allEqual(data []byte, want byte) bool {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	for _, value := range data {
		if value != want {
			return false
		}
	}
	return true
}

func TestIncomingGroupOfferPublishesClonedSnapshotBeforeCallback(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L100-L115
	self := types.NewJID("111111111111111", types.HiddenUserServer)
	self.Device = 14
	peer := types.NewJID("222222222222222", types.HiddenUserServer)
	peer.Device = 7
	snapshot := &types.GroupCallUpdate{
		CallID: "GROUP-CID", TransactionID: 17,
		Relay: &types.GroupCallRelay{
			Key: []byte{4, 5, 6},
			Endpoints: []types.GroupCallRelayEndpoint{{
				Address: []byte{1, 2, 3, 4, 0x0d, 0x96},
			}},
		},
		Participants: []types.GroupCallParticipant{{
			JID: peer.ToNonAD(), State: "connected",
			Devices: []types.GroupCallDevice{{
				JID: peer, PID: 1, HasPID: true, Capability: []byte{1, 2, 3},
			}},
		}},
	}
	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	var callbackState GroupCallState
	var callbackOK bool
	c.OnIncomingCall(func(call *Call) {
		callbackState, callbackOK = call.GroupState()
	})

	c.eng.onOffer(&events.CallOffer{
		BasicCallMeta: types.BasicCallMeta{
			CallID: "GROUP-CID", From: peer, CallCreator: self,
		},
		Group: snapshot,
	})

	m := c.eng.calls["GROUP-CID"]
	if m == nil || !m.group || m.groupUpdate == nil || m.groupUpdate.TransactionID != 17 {
		t.Fatalf("incoming engine group state = %+v", m)
	}
	if !callbackOK || callbackState.TransactionID != 17 {
		t.Fatalf("callback group state = (%+v, %t), want transaction 17", callbackState, callbackOK)
	}
	snapshot.Participants[0].State = "mutated"
	snapshot.Participants[0].Devices[0].Capability[0] = 0xff
	snapshot.Relay.Key[0] = 0xff
	snapshot.Relay.Endpoints[0].Address[0] = 0xff
	if m.groupUpdate.Participants[0].State != "connected" ||
		m.groupUpdate.Participants[0].Devices[0].Capability[0] != 1 ||
		m.groupUpdate.Relay.Key[0] != 4 ||
		m.groupUpdate.Relay.Endpoints[0].Address[0] != 1 {
		t.Fatal("incoming engine group snapshot aliases the Whatsmeow event")
	}
	if got, _ := m.call.GroupState(); got.Participants[0].State != "connected" {
		t.Fatal("incoming public group state aliases the Whatsmeow event")
	}
}

func TestRepeatedIncomingGroupOfferUpdatesRosterWithoutSecondCallback(t *testing.T) {
	creator := types.NewJID("222222222222222", types.HiddenUserServer)
	firstSender := creator
	firstSender.Device = 7
	addedSender := types.NewJID("333333333333333", types.HiddenUserServer)
	addedSender.Device = 43
	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	var calls []*Call
	c.OnIncomingCall(func(call *Call) {
		calls = append(calls, call)
	})

	c.eng.onOffer(&events.CallOffer{
		BasicCallMeta: types.BasicCallMeta{
			CallID: "GROUP-CID", From: firstSender, CallCreator: creator,
		},
		Group: &types.GroupCallUpdate{
			CallID: "GROUP-CID", TransactionID: 38,
		},
	})
	c.eng.onOffer(&events.CallOffer{
		BasicCallMeta: types.BasicCallMeta{
			CallID: "GROUP-CID", From: addedSender, CallCreator: creator,
		},
		Group: &types.GroupCallUpdate{
			CallID: "GROUP-CID", TransactionID: 43,
		},
	})

	if len(calls) != 1 {
		t.Fatalf("incoming callbacks = %d, want one logical call", len(calls))
	}
	state, ok := calls[0].GroupState()
	if !ok || state.TransactionID != 43 {
		t.Fatalf("coalesced group state = (%+v, %t), want transaction 43", state, ok)
	}
}

func TestEndedIncomingGroupCallDoesNotRedispatchSameIDOffer(t *testing.T) {
	creator := types.NewJID("222222222222222", types.HiddenUserServer)
	sender := creator
	sender.Device = 7
	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	var calls []*Call
	c.OnIncomingCall(func(call *Call) {
		calls = append(calls, call)
	})
	offer := &events.CallOffer{
		BasicCallMeta: types.BasicCallMeta{
			CallID: "GROUP-CID", From: sender, CallCreator: creator,
		},
		Group: &types.GroupCallUpdate{
			CallID: "GROUP-CID", TransactionID: 38,
		},
	}

	c.eng.onOffer(offer)
	var rosterTransactions []uint32
	calls[0].OnGroupState(func(state GroupCallState) {
		rosterTransactions = append(rosterTransactions, state.TransactionID)
	})
	c.eng.finishCall("GROUP-CID", "group_call_ended")
	lateOffer := *offer
	lateGroup := *offer.Group
	lateGroup.TransactionID = 43
	lateOffer.Group = &lateGroup
	c.eng.onOffer(&lateOffer)

	if len(calls) != 1 {
		t.Fatalf("incoming callbacks after end = %d, want one", len(calls))
	}
	if calls[0].State() != CallPhaseEnded {
		t.Fatalf("call phase = %d, want ended", calls[0].State())
	}
	if !slices.Equal(rosterTransactions, []uint32{38}) {
		t.Fatalf("post-end roster notifications = %v, want [38]", rosterTransactions)
	}
	state, ok := calls[0].GroupState()
	if !ok || state.TransactionID != 38 {
		t.Fatalf("post-end group state = (%+v, %t), want transaction 38", state, ok)
	}
}

func TestGroupAcceptPreservesSelectedPeerAndDirectRekeyState(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L100-L115
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.group = true
	selected := call.Peer()
	m.peerLID = selected.String()
	answeringDevice := types.NewJID("333333333333333", types.HiddenUserServer)
	answeringDevice.Device = 43
	var rekeys int
	m.rekeyPeer = func(string) error {
		rekeys++
		return nil
	}

	eng.onAccept(&events.CallAccept{BasicCallMeta: types.BasicCallMeta{
		CallID: call.ID(),
		From:   answeringDevice,
	}})

	if call.Peer() != selected || m.peerLID != selected.String() || rekeys != 0 {
		t.Fatalf(
			"group accept mutated direct peer state = public:%s media:%q rekeys:%d",
			call.Peer(),
			m.peerLID,
			rekeys,
		)
	}
	if call.State() != CallPhaseConnecting {
		t.Fatalf("group accept phase = %d, want connecting", call.State())
	}
}

func TestGroupReadinessStartsOnceWithConnectedDeviceAndReplaysOnlyAuthoritativeQueue(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L97-L115
	first := types.NewJID("222222222222222", types.HiddenUserServer)
	second := types.NewJID("333333333333333", types.HiddenUserServer)
	connectedDevice := first
	connectedDevice.Device = 7
	self := types.NewJID("111111111111111", types.HiddenUserServer)
	self.Device = 14

	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	c.eng.offerGroupCall = func(
		context.Context,
		[]types.JID,
		...whatsmeow.GroupCallOfferOptions,
	) (string, error) {
		return "GROUP-CID", nil
	}
	call, err := c.GroupCall(context.Background(), first.String(), second.String())
	if err != nil {
		t.Fatalf("GroupCall: %v", err)
	}

	update := types.GroupCallUpdate{
		CallID: call.ID(), TransactionID: 21,
		Participants: []types.GroupCallParticipant{{
			JID: first, State: "connected",
			Devices: []types.GroupCallDevice{{
				JID: connectedDevice, PID: 1, HasPID: true,
			}},
		}},
	}
	c.eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		Update:        update,
	})
	for _, transactionID := range []uint32{19, 21} {
		c.eng.onEncRekey(&events.CallEncRekey{
			BasicCallMeta: types.BasicCallMeta{
				CallID: call.ID(),
				From:   connectedDevice,
			},
			Rekey: types.GroupCallEncRekey{TransactionID: transactionID},
			RawKey: bytes.Repeat(
				[]byte{byte(transactionID)},
				32,
			),
		})
	}

	type launch struct {
		peerLID string
		order   []string
	}
	launched := make(chan launch, 2)
	c.eng.startMedia = func(
		_ context.Context,
		callID string,
		_ *Call,
		_ []byte,
		_ string,
		peerLID string,
		_ *types.RelayEndpoint,
	) error {
		result := launch{peerLID: peerLID}
		c.eng.activateGroupMedia(
			callID,
			func(got types.GroupCallUpdate) error {
				result.order = append(result.order, fmt.Sprintf("roster:%d", got.TransactionID))
				return nil
			},
			func(got events.CallEncRekey) error {
				result.order = append(result.order, fmt.Sprintf("epoch:%d", got.Rekey.TransactionID))
				return nil
			},
		)
		launched <- result
		return nil
	}
	ready := &events.CallMediaReady{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		SelfLID:       self,
		PeerLID:       connectedDevice,
		CallKey:       bytes.Repeat([]byte{0xa5}, 32),
		Relay: types.RelayEndpoint{
			IPv4: "157.240.17.133",
			Port: 3478,
		},
		Codec:     types.CallCodecOpus,
		Direction: types.CallDirectionOutgoing,
	}
	c.eng.onMediaReady(ready)
	c.eng.onMediaReady(ready)

	select {
	case got := <-launched:
		if got.peerLID != connectedDevice.String() {
			t.Fatalf("media peer = %q, want connected device %q", got.peerLID, connectedDevice)
		}
		if !slices.Equal(got.order, []string{"roster:21", "epoch:19", "epoch:21"}) {
			t.Fatalf("media replay order = %v", got.order)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("group media did not start")
	}
	select {
	case duplicate := <-launched:
		t.Fatalf("group media started twice: %+v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}

	if call.Peer() != first {
		t.Fatalf("readiness replaced stable public peer with %s", call.Peer())
	}
	m := c.eng.calls[call.ID()]
	if m.peerLID != connectedDevice.String() {
		t.Fatalf("engine media peer = %q, want %q", m.peerLID, connectedDevice)
	}
	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 21 {
		t.Fatalf("latest authoritative media roster = %#v, want transaction 21", m.groupUpdate)
	}
}

func TestGroupCallAttachesRosterAndEpochEmittedBeforeOfferReturns(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L94-L115
	first := types.NewJID("222222222222222", types.HiddenUserServer)
	second := types.NewJID("333333333333333", types.HiddenUserServer)
	connectedDevice := first
	connectedDevice.Device = 7
	self := types.NewJID("111111111111111", types.HiddenUserServer)
	self.Device = 14
	rawKey := bytes.Repeat([]byte{0x2a}, 32)

	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	c.eng.offerGroupCall = func(
		context.Context,
		[]types.JID,
		...whatsmeow.GroupCallOfferOptions,
	) (string, error) {
		c.eng.onGroupUpdate(&events.CallGroupUpdate{
			BasicCallMeta: types.BasicCallMeta{CallID: "GROUP-CID"},
			Update: types.GroupCallUpdate{
				CallID: "GROUP-CID", TransactionID: 11,
				Participants: []types.GroupCallParticipant{{
					JID: first, State: "outgoing",
					Devices: []types.GroupCallDevice{{JID: connectedDevice}},
				}},
			},
		})
		c.eng.onEncRekey(&events.CallEncRekey{
			BasicCallMeta: types.BasicCallMeta{
				CallID: "GROUP-CID",
				From:   connectedDevice,
			},
			Rekey:  types.GroupCallEncRekey{TransactionID: 11},
			RawKey: rawKey,
		})
		return "GROUP-CID", nil
	}

	call, err := c.GroupCall(context.Background(), first.String(), second.String())
	if err != nil {
		t.Fatalf("GroupCall: %v", err)
	}
	state, ok := call.GroupState()
	if !ok || state.TransactionID != 11 || len(state.Participants) != 1 {
		t.Fatalf("attached public roster = (%+v, %t), want authoritative transaction 11", state, ok)
	}
	m := c.eng.calls[call.ID()]
	if m == nil || m.call != call || !m.group || len(m.pendingGroupRekeys) != 1 {
		t.Fatalf("attached placeholder = %+v", m)
	}
	rawKey[0] = 0xff
	if m.pendingGroupRekeys[0].RawKey[0] != 0x2a {
		t.Fatal("pre-return queued epoch aliases the Whatsmeow event")
	}

	launched := make(chan []string, 2)
	c.eng.startMedia = func(
		_ context.Context,
		callID string,
		_ *Call,
		_ []byte,
		_ string,
		_ string,
		_ *types.RelayEndpoint,
	) error {
		var order []string
		c.eng.activateGroupMedia(
			callID,
			func(update types.GroupCallUpdate) error {
				order = append(order, fmt.Sprintf("roster:%d", update.TransactionID))
				return nil
			},
			func(rekey events.CallEncRekey) error {
				order = append(order, fmt.Sprintf("epoch:%d", rekey.Rekey.TransactionID))
				return nil
			},
		)
		launched <- order
		return nil
	}
	ready := &events.CallMediaReady{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		SelfLID:       self,
		PeerLID:       connectedDevice,
		CallKey:       bytes.Repeat([]byte{0xa5}, 32),
		Relay:         types.RelayEndpoint{IPv4: "157.240.17.133", Port: 3478},
		Codec:         types.CallCodecOpus,
		Direction:     types.CallDirectionOutgoing,
	}
	c.eng.onMediaReady(ready)
	c.eng.onMediaReady(ready)

	select {
	case order := <-launched:
		if !slices.Equal(order, []string{"roster:11", "epoch:11"}) {
			t.Fatalf("pre-return event replay = %v", order)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attached placeholder did not start media")
	}
	select {
	case order := <-launched:
		t.Fatalf("attached placeholder started media twice: %v", order)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestActivateGroupMediaNilCallbacksDoNotConsumeQueue(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L105-L115
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.group = true
	m.groupUpdate = &types.GroupCallUpdate{
		CallID: call.ID(), TransactionID: 17,
	}
	m.pendingGroupRekeys = []events.CallEncRekey{{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		Rekey:         types.GroupCallEncRekey{TransactionID: 17},
		RawKey:        bytes.Repeat([]byte{0x11}, 32),
	}}

	eng.activateGroupMedia(call.ID(), nil, nil)

	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 17 ||
		len(m.pendingGroupRekeys) != 1 ||
		m.applyGroupUpdate != nil || m.applyGroupRekey != nil {
		t.Fatalf("nil activation callbacks consumed or attached queue: %+v", m)
	}
}

func TestGroupCallDefersPreReturnReadinessUntilSelectedIdentityAttaches(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L115-L122
	first := types.NewJID("222222222222222", types.HiddenUserServer)
	second := types.NewJID("333333333333333", types.HiddenUserServer)
	connectedDevice := first
	connectedDevice.Device = 7
	self := types.NewJID("111111111111111", types.HiddenUserServer)
	self.Device = 14

	type launch struct {
		call    *Call
		peerLID string
		order   []string
	}
	launched := make(chan launch, 2)
	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	c.eng.startMedia = func(
		_ context.Context,
		callID string,
		call *Call,
		_ []byte,
		_ string,
		peerLID string,
		_ *types.RelayEndpoint,
	) error {
		result := launch{call: call, peerLID: peerLID}
		c.eng.activateGroupMedia(
			callID,
			func(update types.GroupCallUpdate) error {
				result.order = append(result.order, fmt.Sprintf("roster:%d", update.TransactionID))
				return nil
			},
			func(rekey events.CallEncRekey) error {
				result.order = append(result.order, fmt.Sprintf("epoch:%d", rekey.Rekey.TransactionID))
				return nil
			},
		)
		launched <- result
		return nil
	}
	var placeholderHadCall bool
	var placeholderStarted bool
	c.eng.offerGroupCall = func(
		context.Context,
		[]types.JID,
		...whatsmeow.GroupCallOfferOptions,
	) (string, error) {
		c.eng.onGroupUpdate(&events.CallGroupUpdate{
			BasicCallMeta: types.BasicCallMeta{CallID: "GROUP-CID"},
			Update: types.GroupCallUpdate{
				CallID: "GROUP-CID", TransactionID: 11,
				Participants: []types.GroupCallParticipant{{
					JID: first, State: "connected",
					Devices: []types.GroupCallDevice{{
						JID: connectedDevice, PID: 1, HasPID: true,
					}},
				}},
			},
		})
		c.eng.onEncRekey(&events.CallEncRekey{
			BasicCallMeta: types.BasicCallMeta{CallID: "GROUP-CID", From: connectedDevice},
			Rekey:         types.GroupCallEncRekey{TransactionID: 11},
			RawKey:        bytes.Repeat([]byte{0x11}, 32),
		})
		c.eng.onMediaReady(&events.CallMediaReady{
			BasicCallMeta: types.BasicCallMeta{CallID: "GROUP-CID"},
			SelfLID:       self,
			PeerLID:       connectedDevice,
			CallKey:       bytes.Repeat([]byte{0xa5}, 32),
			Relay:         types.RelayEndpoint{IPv4: "157.240.17.133", Port: 3478},
			Codec:         types.CallCodecOpus,
			Direction:     types.CallDirectionOutgoing,
		})
		c.eng.mu.Lock()
		placeholder := c.eng.calls["GROUP-CID"]
		placeholderHadCall = placeholder != nil && placeholder.call != nil
		placeholderStarted = placeholder != nil && placeholder.started
		c.eng.mu.Unlock()
		return "GROUP-CID", nil
	}

	call, err := c.GroupCall(context.Background(), first.String(), second.String())
	if err != nil {
		t.Fatalf("GroupCall: %v", err)
	}
	if placeholderHadCall || placeholderStarted {
		t.Fatalf(
			"pre-return readiness synthesized or started a call = call:%t started:%t",
			placeholderHadCall,
			placeholderStarted,
		)
	}
	if call.Peer() != first {
		t.Fatalf("public peer = %s, want first selected %s", call.Peer(), first)
	}
	state, ok := call.GroupState()
	if !ok || state.TransactionID != 11 {
		t.Fatalf("public group state = (%+v, %t), want authoritative transaction 11", state, ok)
	}

	select {
	case got := <-launched:
		if got.call != call {
			t.Fatal("media launch did not receive the selected-identity Call")
		}
		if got.peerLID != connectedDevice.String() {
			t.Fatalf("media peer = %q, want readiness device %q", got.peerLID, connectedDevice)
		}
		if !slices.Equal(got.order, []string{"roster:11", "epoch:11"}) {
			t.Fatalf("pre-return media replay = %v", got.order)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("media did not start after selected identity attached")
	}
	select {
	case duplicate := <-launched:
		t.Fatalf("pre-return readiness started media twice: %+v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestActivateGroupMediaDrainsArrivalsBeforePublishingLiveCallbacks(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L119-L122
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.group = true
	m.groupUpdate = &types.GroupCallUpdate{
		CallID: call.ID(), TransactionID: 21,
	}
	for _, transactionID := range []uint32{19, 20} {
		m.pendingGroupRekeys = append(m.pendingGroupRekeys, events.CallEncRekey{
			BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
			Rekey:         types.GroupCallEncRekey{TransactionID: transactionID},
			RawKey:        bytes.Repeat([]byte{byte(transactionID)}, 32),
		})
	}

	rosterStarted := make(chan struct{})
	releaseRoster := make(chan struct{})
	activationDone := make(chan struct{})
	var orderMu sync.Mutex
	var order []string
	applyUpdate := func(update types.GroupCallUpdate) error {
		orderMu.Lock()
		order = append(order, fmt.Sprintf("roster:%d", update.TransactionID))
		orderMu.Unlock()
		close(rosterStarted)
		<-releaseRoster
		return nil
	}
	applyRekey := func(rekey events.CallEncRekey) error {
		orderMu.Lock()
		order = append(order, fmt.Sprintf("epoch:%d", rekey.Rekey.TransactionID))
		orderMu.Unlock()
		return nil
	}
	go func() {
		eng.activateGroupMedia(call.ID(), applyUpdate, applyRekey)
		close(activationDone)
	}()
	<-rosterStarted

	var secondCallbacks atomic.Int32
	eng.activateGroupMedia(
		call.ID(),
		func(types.GroupCallUpdate) error {
			secondCallbacks.Add(1)
			return nil
		},
		func(events.CallEncRekey) error {
			secondCallbacks.Add(1)
			return nil
		},
	)
	eng.onEncRekey(&events.CallEncRekey{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		Rekey:         types.GroupCallEncRekey{TransactionID: 21},
		RawKey:        bytes.Repeat([]byte{0x21}, 32),
	})
	close(releaseRoster)
	<-activationDone

	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	if !slices.Equal(gotOrder, []string{
		"roster:21",
		"epoch:19",
		"epoch:20",
		"epoch:21",
	}) {
		t.Fatalf("activation order = %v", gotOrder)
	}
	if secondCallbacks.Load() != 0 {
		t.Fatalf("second activation published callbacks %d times", secondCallbacks.Load())
	}
}

func TestActivateGroupMediaRejectedRosterAllowsLowerRecovery(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L119-L122
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	var publicMu sync.Mutex
	var publicTransactions []uint32
	call.OnGroupState(func(state GroupCallState) {
		publicMu.Lock()
		publicTransactions = append(publicTransactions, state.TransactionID)
		publicMu.Unlock()
	})
	eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		Update: types.GroupCallUpdate{
			CallID: call.ID(), TransactionID: 20,
		},
	})
	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 20 {
		t.Fatalf("pre-activation roster cache = %+v, want transaction 20", m.groupUpdate)
	}
	if state, ok := call.GroupState(); !ok || state.TransactionID != 20 {
		t.Fatalf("pre-activation public roster = (%+v, %t), want transaction 20", state, ok)
	}
	applyStarted := make(chan struct{})
	releaseApply := make(chan struct{})
	activationDone := make(chan struct{})
	var appliedMu sync.Mutex
	var applied []uint32
	go func() {
		eng.activateGroupMedia(
			call.ID(),
			func(update types.GroupCallUpdate) error {
				appliedMu.Lock()
				applied = append(applied, update.TransactionID)
				appliedMu.Unlock()
				if update.TransactionID == 20 {
					close(applyStarted)
					<-releaseApply
					return errors.New("rejected roster")
				}
				return nil
			},
			func(events.CallEncRekey) error { return nil },
		)
		close(activationDone)
	}()
	<-applyStarted

	eng.onGroupUpdate(&events.CallGroupUpdate{
		BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
		Update: types.GroupCallUpdate{
			CallID: call.ID(), TransactionID: 19,
		},
	})
	close(releaseApply)
	<-activationDone

	appliedMu.Lock()
	gotApplied := append([]uint32(nil), applied...)
	appliedMu.Unlock()
	if !slices.Equal(gotApplied, []uint32{20, 19}) {
		t.Fatalf("applied roster transactions = %v, want [20 19]", gotApplied)
	}
	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 19 {
		t.Fatalf("recovery roster cache = %+v, want transaction 19", m.groupUpdate)
	}
	publicMu.Lock()
	gotPublicTransactions := append([]uint32(nil), publicTransactions...)
	publicMu.Unlock()
	if !slices.Equal(gotPublicTransactions, []uint32{20, 19}) {
		t.Fatalf(
			"public roster transactions = %v, want [20 19]",
			gotPublicTransactions,
		)
	}
	state, ok := call.GroupState()
	if !ok || state.TransactionID != 19 {
		t.Fatalf("public recovery roster = (%+v, %t), want transaction 19", state, ok)
	}
}

func TestActivateGroupMediaAcceptedRosterSuppressesLowerAndDrainsNewer(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L119-L122
	eng, call := testEngineWithOutgoingCall()
	m := eng.calls[call.ID()]
	m.group = true
	m.groupUpdate = &types.GroupCallUpdate{
		CallID: call.ID(), TransactionID: 20,
	}
	applyStarted := make(chan struct{})
	releaseApply := make(chan struct{})
	activationDone := make(chan struct{})
	var appliedMu sync.Mutex
	var applied []uint32
	go func() {
		eng.activateGroupMedia(
			call.ID(),
			func(update types.GroupCallUpdate) error {
				appliedMu.Lock()
				applied = append(applied, update.TransactionID)
				appliedMu.Unlock()
				if update.TransactionID == 20 {
					close(applyStarted)
					<-releaseApply
				}
				return nil
			},
			func(events.CallEncRekey) error { return nil },
		)
		close(activationDone)
	}()
	<-applyStarted

	for _, transactionID := range []uint32{19, 21} {
		eng.onGroupUpdate(&events.CallGroupUpdate{
			BasicCallMeta: types.BasicCallMeta{CallID: call.ID()},
			Update: types.GroupCallUpdate{
				CallID: call.ID(), TransactionID: transactionID,
			},
		})
	}
	close(releaseApply)
	<-activationDone

	appliedMu.Lock()
	gotApplied := append([]uint32(nil), applied...)
	appliedMu.Unlock()
	if !slices.Equal(gotApplied, []uint32{20, 21}) {
		t.Fatalf("applied roster transactions = %v, want [20 21]", gotApplied)
	}
	if m.groupUpdate == nil || m.groupUpdate.TransactionID != 21 {
		t.Fatalf("accepted roster cache = %+v, want transaction 21", m.groupUpdate)
	}
	state, ok := call.GroupState()
	if !ok || state.TransactionID != 21 {
		t.Fatalf("public accepted roster = (%+v, %t), want transaction 21", state, ok)
	}
}

func TestUnknownGroupPlaceholderExpiryIsBoundedAndAttachmentSafe(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	var expiryMu sync.Mutex
	expiries := make(map[string]func())
	var scheduledCallID string
	var scheduledTTL time.Duration
	var cancels atomic.Int32
	c.eng.scheduleGroupPlaceholderExpiry = func(
		callID string,
		ttl time.Duration,
		expire func(),
	) func() {
		expiryMu.Lock()
		scheduledCallID = callID
		scheduledTTL = ttl
		expiries[callID] = expire
		expiryMu.Unlock()
		return func() {
			cancels.Add(1)
		}
	}

	unknownKey := bytes.Repeat([]byte{0x33}, 32)
	c.eng.onEncRekey(&events.CallEncRekey{
		BasicCallMeta: types.BasicCallMeta{CallID: "UNKNOWN-CID"},
		Rekey:         types.GroupCallEncRekey{TransactionID: 1},
		RawKey:        unknownKey,
	})
	c.eng.mu.Lock()
	unknown := c.eng.calls["UNKNOWN-CID"]
	ownedUnknownKey := unknown.pendingGroupRekeys[0].RawKey
	c.eng.mu.Unlock()
	expiryMu.Lock()
	expireUnknown := expiries["UNKNOWN-CID"]
	expiryMu.Unlock()
	if scheduledCallID != "UNKNOWN-CID" || scheduledTTL <= 0 || expireUnknown == nil {
		t.Fatalf(
			"placeholder expiry = call:%q ttl:%s callback:%t",
			scheduledCallID,
			scheduledTTL,
			expireUnknown != nil,
		)
	}
	expireUnknown()
	c.eng.mu.Lock()
	retainedUnknown := c.eng.calls["UNKNOWN-CID"]
	c.eng.mu.Unlock()
	if retainedUnknown != nil || !allZero(ownedUnknownKey) {
		t.Fatalf(
			"expired placeholder = retained:%t key_zero:%t",
			retainedUnknown != nil,
			allZero(ownedUnknownKey),
		)
	}

	first := types.NewJID("222222222222222", types.HiddenUserServer)
	second := types.NewJID("333333333333333", types.HiddenUserServer)
	attachedKey := bytes.Repeat([]byte{0x44}, 32)
	c.eng.offerGroupCall = func(
		context.Context,
		[]types.JID,
		...whatsmeow.GroupCallOfferOptions,
	) (string, error) {
		c.eng.onEncRekey(&events.CallEncRekey{
			BasicCallMeta: types.BasicCallMeta{CallID: "ATTACHED-CID"},
			Rekey:         types.GroupCallEncRekey{TransactionID: 2},
			RawKey:        attachedKey,
		})
		return "ATTACHED-CID", nil
	}
	call, err := c.GroupCall(context.Background(), first.String(), second.String())
	if err != nil {
		t.Fatalf("GroupCall: %v", err)
	}
	expiryMu.Lock()
	expireAttached := expiries["ATTACHED-CID"]
	expiryMu.Unlock()
	if expireAttached == nil {
		t.Fatal("attached placeholder did not schedule its original expiry")
	}
	expireAttached()
	c.eng.mu.Lock()
	attached := c.eng.calls["ATTACHED-CID"]
	c.eng.mu.Unlock()
	if attached == nil || attached.call != call || len(attached.pendingGroupRekeys) != 1 {
		t.Fatalf("stale expiry removed attached call: %+v", attached)
	}
	if allZero(attached.pendingGroupRekeys[0].RawKey) {
		t.Fatal("stale expiry cleared the attached call's pending epoch")
	}
	if cancels.Load() < 2 {
		t.Fatalf("placeholder expiry cancellations = %d, want at least 2", cancels.Load())
	}
}

func TestGroupEventsRejectEmptyCallIDBeforePlaceholderAllocation(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/0606f5102f94131b3a77a0f979153d9cc72cbfb7/datasheets/api-initial-group-call.md#L111-L118
	c := &Client{log: zerolog.Nop()}
	c.eng = newEngine(c)
	var scheduled atomic.Int32
	c.eng.scheduleGroupPlaceholderExpiry = func(
		string,
		time.Duration,
		func(),
	) func() {
		scheduled.Add(1)
		return func() {}
	}
	rawKey := bytes.Repeat([]byte{0x55}, 32)

	c.eng.onGroupUpdate(&events.CallGroupUpdate{
		Update: types.GroupCallUpdate{TransactionID: 1},
	})
	c.eng.onEncRekey(&events.CallEncRekey{
		Rekey:  types.GroupCallEncRekey{TransactionID: 1},
		RawKey: rawKey,
	})

	c.eng.mu.Lock()
	empty := c.eng.calls[""]
	callCount := len(c.eng.calls)
	c.eng.mu.Unlock()
	if empty != nil || callCount != 0 || scheduled.Load() != 0 {
		t.Fatalf(
			"empty call ID allocated state = entry:%t calls:%d scheduled:%d",
			empty != nil,
			callCount,
			scheduled.Load(),
		)
	}
	if !allEqual(rawKey, 0x55) {
		t.Fatal("empty call ID handling mutated the event-owned raw key")
	}
}
