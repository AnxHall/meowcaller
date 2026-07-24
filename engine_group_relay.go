package meowcaller

import (
	"fmt"
	"sync"

	"github.com/purpshell/meowcaller/stun"
	"go.mau.fi/whatsmeow/types"
)

type groupRelayAllocateState struct {
	mu            sync.RWMutex
	packet        []byte
	transactionID uint32
	hasGroup      bool
}

func newGroupRelayAllocateState(initial []byte) *groupRelayAllocateState {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/6b568ebf25068e2720ba474a7092d482f53e3091/datasheets/group-media-relay-refresh.md#L53-L60
	return &groupRelayAllocateState{packet: append([]byte(nil), initial...)}
}

func (s *groupRelayAllocateState) Current() []byte {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/6b568ebf25068e2720ba474a7092d482f53e3091/datasheets/group-media-relay-refresh.md#L53-L60
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.packet...)
}

func (s *groupRelayAllocateState) Apply(
	endpoint *types.RelayEndpoint,
	relay *types.GroupCallRelay,
	streamSSRCs [9]uint32,
	transactionID [12]byte,
) ([]byte, bool, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/6b568ebf25068e2720ba474a7092d482f53e3091/datasheets/group-media-relay-refresh.md#L62-L81
	if endpoint == nil || endpoint.RelayName == "" || endpoint.IPv4 == "" || endpoint.Port == 0 {
		return nil, false, fmt.Errorf("meowcaller: active relay endpoint is incomplete")
	}
	if relay == nil {
		return nil, false, nil
	}
	if len(relay.Key) == 0 {
		return nil, false, fmt.Errorf("meowcaller: group relay has no key")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasGroup && relay.TransactionID <= s.transactionID {
		return nil, false, nil
	}

	var matched *types.GroupCallRelayEndpoint
	for i := range relay.Endpoints {
		candidate := &relay.Endpoints[i]
		if candidate.RelayName == endpoint.RelayName && candidate.IsFNA == endpoint.IsFNA {
			matched = candidate
			break
		}
	}
	if matched == nil {
		return nil, false, fmt.Errorf("meowcaller: active relay %s missing from group allocation", endpoint.RelayName)
	}
	if int(matched.TokenID) >= len(relay.Tokens) || len(relay.Tokens[matched.TokenID]) == 0 {
		return nil, false, fmt.Errorf("meowcaller: group relay token %d is missing", matched.TokenID)
	}
	endpointXOR, ok := stun.EncodeXorRelayEndpoint(endpoint.IPv4, endpoint.Port)
	if !ok {
		return nil, false, fmt.Errorf("meowcaller: active relay IPv4 is malformed")
	}
	packet := stun.BuildWasmStunAllocateRequestWithStreamSsrcs(
		transactionID,
		relay.Tokens[matched.TokenID],
		endpointXOR,
		streamSSRCs,
		relay.Key,
	)
	s.packet = append(s.packet[:0], packet...)
	s.transactionID = relay.TransactionID
	s.hasGroup = true
	return append([]byte(nil), packet...), true, nil
}
