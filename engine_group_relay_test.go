package meowcaller

import (
	"bytes"
	"errors"
	"testing"

	"github.com/purpshell/meowcaller/stun"
	"go.mau.fi/whatsmeow/types"
)

func TestGroupRelayAllocateStateRotatesCredentialsOnExistingTransport(t *testing.T) {
	initial := []byte{0x00, 0x01, 0x02}
	initialKey := bytes.Repeat([]byte{0x12}, 16)
	state := newGroupRelayAllocateState(initial, initialKey)
	endpoint := &types.RelayEndpoint{
		RelayName: "zrh1c01",
		IPv4:      "157.240.17.62",
		Port:      3478,
	}
	groupToken := bytes.Repeat([]byte{0x42}, 174)
	groupKey := bytes.Repeat([]byte{0x24}, 16)
	groupRelay := &types.GroupCallRelay{
		TransactionID: 1,
		Key:           groupKey,
		Tokens:        [][]byte{bytes.Repeat([]byte{0x11}, 174), groupToken},
		Endpoints: []types.GroupCallRelayEndpoint{
			{RelayName: "fra3c01", TokenID: 0},
			{RelayName: "zrh1c01", TokenID: 1},
		},
	}
	streamSSRCs := [9]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}
	transactionID := [12]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}

	var sent []byte
	changed, err := state.Apply(endpoint, groupRelay, streamSSRCs, transactionID, func(packet []byte) error {
		sent = append([]byte(nil), packet...)
		return nil
	})
	if err != nil {
		t.Fatalf("apply group relay: %v", err)
	}
	if !changed {
		t.Fatal("group relay credentials were not applied")
	}
	endpointXOR, ok := stun.EncodeXorRelayEndpoint(endpoint.IPv4, endpoint.Port)
	if !ok {
		t.Fatal("encode relay endpoint")
	}
	want := stun.BuildWasmStunAllocateRequestWithStreamSsrcs(
		transactionID,
		groupToken,
		endpointXOR,
		streamSSRCs,
		groupKey,
	)
	if !bytes.Equal(sent, want) {
		t.Fatalf("rotated allocate = %x, want %x", sent, want)
	}
	if got := state.Current(); !bytes.Equal(got, want) {
		t.Fatalf("current allocate = %x, want %x", got, want)
	}
	if bytes.Equal(state.Current(), initial) {
		t.Fatal("keepalive retained the initial one-to-one allocate")
	}

	staleSent := false
	staleTransactionID := [12]byte{11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
	changed, err = state.Apply(endpoint, groupRelay, streamSSRCs, staleTransactionID, func([]byte) error {
		staleSent = true
		return nil
	})
	if err != nil {
		t.Fatalf("apply duplicate group relay: %v", err)
	}
	if changed {
		t.Fatal("duplicate group relay transaction reported a change")
	}
	if staleSent {
		t.Fatal("duplicate group relay transaction reached the transport")
	}
}

func TestGroupRelayAllocateStateIgnoresMalformedStaleTransaction(t *testing.T) {
	initial := []byte{0x00, 0x01, 0x02}
	state := newGroupRelayAllocateState(initial, bytes.Repeat([]byte{0x12}, 16))
	endpoint := &types.RelayEndpoint{
		RelayName: "zrh1c01",
		IPv4:      "157.240.17.62",
		Port:      3478,
	}
	current := &types.GroupCallRelay{
		TransactionID: 2,
		Key:           bytes.Repeat([]byte{0x24}, 16),
		Tokens:        [][]byte{bytes.Repeat([]byte{0x42}, 174)},
		Endpoints: []types.GroupCallRelayEndpoint{{
			RelayName: "zrh1c01",
			TokenID:   0,
		}},
	}
	if changed, err := state.Apply(endpoint, current, [9]uint32{}, [12]byte{}, func([]byte) error {
		return nil
	}); err != nil || !changed {
		t.Fatalf("apply current relay = (%v, %v), want (true, nil)", changed, err)
	}
	committed := state.Current()

	stale := &types.GroupCallRelay{TransactionID: 1}
	changed, err := state.Apply(nil, stale, [9]uint32{}, [12]byte{}, nil)
	if err != nil {
		t.Fatalf("malformed stale relay returned error: %v", err)
	}
	if changed {
		t.Fatal("malformed stale relay reported a change")
	}
	if got := state.Current(); !bytes.Equal(got, committed) {
		t.Fatalf("malformed stale relay changed allocate = %x, want %x", got, committed)
	}
}

func TestGroupRelayAllocateStateSendFailureKeepsPriorCredentialsRetryable(t *testing.T) {
	initial := []byte{0x00, 0x01, 0x02}
	initialKey := bytes.Repeat([]byte{0x12}, 16)
	state := newGroupRelayAllocateState(initial, initialKey)
	endpoint := &types.RelayEndpoint{
		RelayName: "zrh1c01",
		IPv4:      "157.240.17.62",
		Port:      3478,
	}
	groupRelay := &types.GroupCallRelay{
		TransactionID: 1,
		Key:           bytes.Repeat([]byte{0x24}, 16),
		Tokens:        [][]byte{bytes.Repeat([]byte{0x42}, 174)},
		Endpoints: []types.GroupCallRelayEndpoint{{
			RelayName: "zrh1c01",
			TokenID:   0,
		}},
	}
	streamSSRCs := [9]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}
	transactionID := [12]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	sendErr := errors.New("relay unavailable")

	changed, err := state.Apply(endpoint, groupRelay, streamSSRCs, transactionID, func([]byte) error {
		return sendErr
	})
	if !errors.Is(err, sendErr) {
		t.Fatalf("apply error = %v, want %v", err, sendErr)
	}
	if changed {
		t.Fatal("failed relay send reported a committed change")
	}
	if got := state.Current(); !bytes.Equal(got, initial) {
		t.Fatalf("current allocate after failure = %x, want %x", got, initial)
	}
	bindingTransactionID := [12]byte{11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
	request := stun.EncodeStunRequest(stun.MsgBindingRequest, bindingTransactionID, nil, nil, false)
	response, answered, err := state.SendBindingSuccess(request, func([]byte) error {
		return nil
	})
	if err != nil {
		t.Fatalf("send binding success after failed refresh: %v", err)
	}
	if !answered {
		t.Fatal("binding request was not answered after failed refresh")
	}
	wantInitial := stun.EncodeStunRequest(stun.MsgBindingSuccess, bindingTransactionID, nil, initialKey, true)
	if !bytes.Equal(response, wantInitial) {
		t.Fatalf("binding success after failed refresh = %x, want initial-key response %x", response, wantInitial)
	}

	changed, err = state.Apply(endpoint, groupRelay, streamSSRCs, transactionID, func([]byte) error {
		return nil
	})
	if err != nil {
		t.Fatalf("retry group relay: %v", err)
	}
	if !changed {
		t.Fatal("failed relay transaction was not retryable")
	}
}

func TestGroupRelayBindingSuccessUsesCommittedRotatedKey(t *testing.T) {
	initial := []byte{0x00, 0x01, 0x02}
	initialKey := bytes.Repeat([]byte{0x12}, 16)
	rotatedKey := bytes.Repeat([]byte{0x24}, 16)
	state := newGroupRelayAllocateState(initial, initialKey)
	endpoint := &types.RelayEndpoint{
		RelayName: "zrh1c01",
		IPv4:      "157.240.17.62",
		Port:      3478,
	}
	groupRelay := &types.GroupCallRelay{
		TransactionID: 1,
		Key:           rotatedKey,
		Tokens:        [][]byte{bytes.Repeat([]byte{0x42}, 174)},
		Endpoints: []types.GroupCallRelayEndpoint{{
			RelayName: "zrh1c01",
			TokenID:   0,
		}},
	}
	streamSSRCs := [9]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}
	allocateTransactionID := [12]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	changed, err := state.Apply(endpoint, groupRelay, streamSSRCs, allocateTransactionID, func([]byte) error {
		return nil
	})
	if err != nil {
		t.Fatalf("apply group relay: %v", err)
	}
	if !changed {
		t.Fatal("group relay credentials were not applied")
	}

	bindingTransactionID := [12]byte{11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
	request := stun.EncodeStunRequest(stun.MsgBindingRequest, bindingTransactionID, nil, nil, false)
	response, answered, err := state.SendBindingSuccess(request, func([]byte) error {
		return nil
	})
	if err != nil {
		t.Fatalf("send binding success: %v", err)
	}
	if !answered {
		t.Fatal("binding request was not answered")
	}
	wantRotated := stun.EncodeStunRequest(stun.MsgBindingSuccess, bindingTransactionID, nil, rotatedKey, true)
	if !bytes.Equal(response, wantRotated) {
		t.Fatalf("binding success = %x, want rotated-key response %x", response, wantRotated)
	}
	wantInitial := stun.EncodeStunRequest(stun.MsgBindingSuccess, bindingTransactionID, nil, initialKey, true)
	if bytes.Equal(response, wantInitial) {
		t.Fatal("binding success still authenticates with the initial one-to-one key")
	}
}

func TestGroupRelayRotatedApplyWaitsForOldKeepaliveSend(t *testing.T) {
	initial := []byte{0x00, 0x01, 0x02}
	initialKey := bytes.Repeat([]byte{0x12}, 16)
	rotatedKey := bytes.Repeat([]byte{0x24}, 16)
	groupToken := bytes.Repeat([]byte{0x42}, 174)
	state := newGroupRelayAllocateState(initial, initialKey)
	endpoint := &types.RelayEndpoint{
		RelayName: "zrh1c01",
		IPv4:      "157.240.17.62",
		Port:      3478,
	}
	groupRelay := &types.GroupCallRelay{
		TransactionID: 1,
		Key:           rotatedKey,
		Tokens:        [][]byte{groupToken},
		Endpoints: []types.GroupCallRelayEndpoint{{
			RelayName: "zrh1c01",
			TokenID:   0,
		}},
	}
	streamSSRCs := [9]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}
	allocateTransactionID := [12]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	endpointXOR, ok := stun.EncodeXorRelayEndpoint(endpoint.IPv4, endpoint.Port)
	if !ok {
		t.Fatal("encode relay endpoint")
	}
	wantRotated := stun.BuildWasmStunAllocateRequestWithStreamSsrcs(
		allocateTransactionID,
		groupToken,
		endpointXOR,
		streamSSRCs,
		rotatedKey,
	)

	oldSendEntered := make(chan struct{})
	releaseOldSend := make(chan struct{})
	oldSendReleased := false
	defer func() {
		if !oldSendReleased {
			close(releaseOldSend)
		}
	}()
	oldPacket := make(chan []byte, 1)
	oldSendDone := make(chan error, 1)
	go func() {
		oldSendDone <- state.SendCurrent(func(packet []byte) error {
			oldPacket <- append([]byte(nil), packet...)
			close(oldSendEntered)
			<-releaseOldSend
			return nil
		})
	}()
	<-oldSendEntered
	if got := <-oldPacket; !bytes.Equal(got, initial) {
		t.Fatalf("blocked old keepalive = %x, want initial allocate %x", got, initial)
	}
	if state.mu.TryLock() {
		state.mu.Unlock()
		t.Fatal("blocked keepalive released relay state before its send completed")
	}

	applyStarted := make(chan struct{})
	applySendEntered := make(chan []byte, 1)
	applyDone := make(chan error, 1)
	go func() {
		close(applyStarted)
		_, err := state.Apply(endpoint, groupRelay, streamSSRCs, allocateTransactionID, func(packet []byte) error {
			applySendEntered <- append([]byte(nil), packet...)
			return nil
		})
		applyDone <- err
	}()
	<-applyStarted

	close(releaseOldSend)
	oldSendReleased = true
	if err := <-oldSendDone; err != nil {
		t.Fatalf("send old keepalive: %v", err)
	}
	if got := <-applySendEntered; !bytes.Equal(got, wantRotated) {
		t.Fatalf("rotated Apply packet = %x, want %x", got, wantRotated)
	}
	if err := <-applyDone; err != nil {
		t.Fatalf("apply group relay: %v", err)
	}

	var nextPacket []byte
	if err := state.SendCurrent(func(packet []byte) error {
		nextPacket = append([]byte(nil), packet...)
		return nil
	}); err != nil {
		t.Fatalf("send post-commit keepalive: %v", err)
	}
	if !bytes.Equal(nextPacket, wantRotated) {
		t.Fatalf("post-commit keepalive = %x, want rotated allocate %x", nextPacket, wantRotated)
	}
}

func TestGroupRelayRotatedApplyWaitsForOldBindingSend(t *testing.T) {
	initial := []byte{0x00, 0x01, 0x02}
	initialKey := bytes.Repeat([]byte{0x12}, 16)
	rotatedKey := bytes.Repeat([]byte{0x24}, 16)
	state := newGroupRelayAllocateState(initial, initialKey)
	endpoint := &types.RelayEndpoint{
		RelayName: "zrh1c01",
		IPv4:      "157.240.17.62",
		Port:      3478,
	}
	groupRelay := &types.GroupCallRelay{
		TransactionID: 1,
		Key:           rotatedKey,
		Tokens:        [][]byte{bytes.Repeat([]byte{0x42}, 174)},
		Endpoints: []types.GroupCallRelayEndpoint{{
			RelayName: "zrh1c01",
			TokenID:   0,
		}},
	}
	streamSSRCs := [9]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9}
	allocateTransactionID := [12]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}

	bindingTransactionID := [12]byte{11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
	request := stun.EncodeStunRequest(stun.MsgBindingRequest, bindingTransactionID, nil, nil, false)
	oldSendEntered := make(chan struct{})
	releaseOldSend := make(chan struct{})
	oldSendReleased := false
	defer func() {
		if !oldSendReleased {
			close(releaseOldSend)
		}
	}()
	oldBindingPacket := make(chan []byte, 1)
	type bindingResult struct {
		answered bool
		err      error
	}
	oldBindingDone := make(chan bindingResult, 1)
	go func() {
		_, answered, err := state.SendBindingSuccess(request, func(packet []byte) error {
			oldBindingPacket <- append([]byte(nil), packet...)
			close(oldSendEntered)
			<-releaseOldSend
			return nil
		})
		oldBindingDone <- bindingResult{answered: answered, err: err}
	}()
	<-oldSendEntered
	wantInitial := stun.EncodeStunRequest(stun.MsgBindingSuccess, bindingTransactionID, nil, initialKey, true)
	if got := <-oldBindingPacket; !bytes.Equal(got, wantInitial) {
		t.Fatalf("blocked old binding success = %x, want initial-key response %x", got, wantInitial)
	}
	if state.mu.TryLock() {
		state.mu.Unlock()
		t.Fatal("blocked binding response released relay state before its send completed")
	}

	applyStarted := make(chan struct{})
	applySendEntered := make(chan []byte, 1)
	applyDone := make(chan error, 1)
	go func() {
		close(applyStarted)
		_, err := state.Apply(endpoint, groupRelay, streamSSRCs, allocateTransactionID, func(packet []byte) error {
			applySendEntered <- append([]byte(nil), packet...)
			return nil
		})
		applyDone <- err
	}()
	<-applyStarted

	close(releaseOldSend)
	oldSendReleased = true
	oldResult := <-oldBindingDone
	if oldResult.err != nil {
		t.Fatalf("send old binding success: %v", oldResult.err)
	}
	if !oldResult.answered {
		t.Fatal("old binding request was not answered")
	}
	if got := <-applySendEntered; len(got) == 0 {
		t.Fatal("rotated Apply sent an empty allocate")
	}
	if err := <-applyDone; err != nil {
		t.Fatalf("apply group relay: %v", err)
	}

	var nextBindingPacket []byte
	_, answered, err := state.SendBindingSuccess(request, func(packet []byte) error {
		nextBindingPacket = append([]byte(nil), packet...)
		return nil
	})
	if err != nil {
		t.Fatalf("send post-commit binding success: %v", err)
	}
	if !answered {
		t.Fatal("post-commit binding request was not answered")
	}
	wantRotated := stun.EncodeStunRequest(stun.MsgBindingSuccess, bindingTransactionID, nil, rotatedKey, true)
	if !bytes.Equal(nextBindingPacket, wantRotated) {
		t.Fatalf("post-commit binding success = %x, want rotated-key response %x", nextBindingPacket, wantRotated)
	}
}
