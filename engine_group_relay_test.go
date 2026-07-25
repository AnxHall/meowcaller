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
	if got := state.CurrentKey(); !bytes.Equal(got, groupKey) {
		t.Fatalf("current key = %x, want %x", got, groupKey)
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
	if got := state.CurrentKey(); !bytes.Equal(got, initialKey) {
		t.Fatalf("current key after failure = %x, want %x", got, initialKey)
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
	response, ok := buildRelayBindingSuccess(request, state.CurrentKey())
	if !ok {
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
