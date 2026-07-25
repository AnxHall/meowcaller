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
	key           []byte
	transactionID uint32
	hasGroup      bool
}

func newGroupRelayAllocateState(initial, initialKey []byte) *groupRelayAllocateState {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L53-L58
	return &groupRelayAllocateState{
		packet: append([]byte(nil), initial...),
		key:    append([]byte(nil), initialKey...),
	}
}

func (s *groupRelayAllocateState) Current() []byte {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L60
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.packet...)
}

func (s *groupRelayAllocateState) CurrentKey() []byte {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L62
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.key...)
}

func (s *groupRelayAllocateState) Apply(
	endpoint *types.RelayEndpoint,
	relay *types.GroupCallRelay,
	streamSSRCs [9]uint32,
	transactionID [12]byte,
	send func([]byte) error,
) (bool, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L64-L92
	if endpoint == nil || endpoint.RelayName == "" || endpoint.IPv4 == "" || endpoint.Port == 0 {
		return false, fmt.Errorf("meowcaller: active relay endpoint is incomplete")
	}
	if relay == nil {
		return false, nil
	}
	if len(relay.Key) == 0 {
		return false, fmt.Errorf("meowcaller: group relay has no key")
	}
	if send == nil {
		return false, fmt.Errorf("meowcaller: group relay send is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasGroup && relay.TransactionID <= s.transactionID {
		return false, nil
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
		return false, fmt.Errorf("meowcaller: active relay %s missing from group allocation", endpoint.RelayName)
	}
	if int(matched.TokenID) >= len(relay.Tokens) || len(relay.Tokens[matched.TokenID]) == 0 {
		return false, fmt.Errorf("meowcaller: group relay token %d is missing", matched.TokenID)
	}
	endpointXOR, ok := stun.EncodeXorRelayEndpoint(endpoint.IPv4, endpoint.Port)
	if !ok {
		return false, fmt.Errorf("meowcaller: active relay IPv4 is malformed")
	}
	packet := stun.BuildWasmStunAllocateRequestWithStreamSsrcs(
		transactionID,
		relay.Tokens[matched.TokenID],
		endpointXOR,
		streamSSRCs,
		relay.Key,
	)
	if err := send(packet); err != nil {
		return false, err
	}
	s.packet = append(s.packet[:0], packet...)
	s.key = append(s.key[:0], relay.Key...)
	s.transactionID = relay.TransactionID
	s.hasGroup = true
	return true, nil
}

func buildRelayBindingSuccess(request, integrityKey []byte) ([]byte, bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L72-L92
	messageType, ok := stun.StunMessageType(request)
	if !ok || messageType != stun.MsgBindingRequest || len(integrityKey) == 0 {
		return nil, false
	}
	transactionID, ok := stun.StunTransactionID(request)
	if !ok || len(transactionID) != 12 {
		return nil, false
	}
	var transaction [12]byte
	copy(transaction[:], transactionID)
	return stun.EncodeStunRequest(stun.MsgBindingSuccess, transaction, nil, integrityKey, true), true
}
