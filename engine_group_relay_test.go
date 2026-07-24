package meowcaller

import (
	"bytes"
	"testing"

	"github.com/purpshell/meowcaller/stun"
	"go.mau.fi/whatsmeow/types"
)

func TestGroupRelayAllocateStateRotatesCredentialsOnExistingTransport(t *testing.T) {
	initial := []byte{0x00, 0x01, 0x02}
	state := newGroupRelayAllocateState(initial)
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

	packet, changed, err := state.Apply(endpoint, groupRelay, streamSSRCs, transactionID)
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
	if !bytes.Equal(packet, want) {
		t.Fatalf("rotated allocate = %x, want %x", packet, want)
	}
	if got := state.Current(); !bytes.Equal(got, want) {
		t.Fatalf("current allocate = %x, want %x", got, want)
	}
	if bytes.Equal(state.Current(), initial) {
		t.Fatal("keepalive retained the initial one-to-one allocate")
	}
}
