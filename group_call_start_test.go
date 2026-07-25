package meowcaller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
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
	}, GroupCallOptions{GroupJID: " 120363411251996986@g.us "})
	if err != nil {
		t.Fatalf("GroupCallWithOptions: %v", err)
	}
	if calls != 1 || gotContext != ctx {
		t.Fatalf("delegation = calls:%d context_preserved:%t, want 1,true", calls, gotContext == ctx)
	}
	if !slices.Equal(gotTargets, []types.JID{first, second}) {
		t.Fatalf("delegated targets = %v, want [%s %s]", gotTargets, first, second)
	}
	if len(gotOptions) != 1 || gotOptions[0].GroupJID != groupJID {
		t.Fatalf("delegated options = %+v, want group JID %s", gotOptions, groupJID)
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
}

func TestClientGroupCallValidationStopsBeforeDelegation(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/ceaa2156015e8f24e09328fb7a9c89203295efff/datasheets/api-initial-group-call.md#L82-L115
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
