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
	hbhFECSSRCs   [2]uint32
}

func newGroupRelayAllocateState(initial, initialKey []byte) *groupRelayAllocateState {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L53-L58
	return newGroupRelayAllocateStateWithHBHFEC(initial, initialKey, [2]uint32{})
}

func newGroupRelayAllocateStateWithHBHFEC(
	initial, initialKey []byte,
	hbhFECSSRCs [2]uint32,
) *groupRelayAllocateState {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/99134bb900df3ee83a69d9a38112e623817597ae/datasheets/group-video-reactions.md#L36-L50
	return &groupRelayAllocateState{
		packet:      append([]byte(nil), initial...),
		key:         append([]byte(nil), initialKey...),
		hbhFECSSRCs: hbhFECSSRCs,
	}
}

func (s *groupRelayAllocateState) Current() []byte {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L60
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.packet...)
}

func (s *groupRelayAllocateState) SendCurrent(send func([]byte) error) error {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/bcfb7f0c076b131422c22f024dfff080448e70f4/datasheets/group-media-relay-refresh.md#L59-L67
	if send == nil {
		return fmt.Errorf("meowcaller: relay keepalive send is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return send(s.packet)
}

func (s *groupRelayAllocateState) Apply(
	endpoint *types.RelayEndpoint,
	relay *types.GroupCallRelay,
	streamSSRCs [9]uint32,
	transactionID [12]byte,
	send func([]byte) error,
) (bool, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/a9e4195fb846a730f30ce98c26a7d1c03993fdb2/datasheets/group-media-relay-refresh.md#L64-L92
	return s.apply(endpoint, relay, streamSSRCs, 0, nil, transactionID, send)
}

func (s *groupRelayAllocateState) ApplyWithSubscriptions(
	endpoint *types.RelayEndpoint,
	relay *types.GroupCallRelay,
	streamSSRCs [9]uint32,
	appDataSSRC uint32,
	participantPIDs []uint32,
	transactionID [12]byte,
	send func([]byte) error,
) (bool, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/99134bb900df3ee83a69d9a38112e623817597ae/datasheets/group-video-reactions.md#L36-L50
	return s.apply(
		endpoint,
		relay,
		streamSSRCs,
		appDataSSRC,
		participantPIDs,
		transactionID,
		send,
	)
}

func (s *groupRelayAllocateState) apply(
	endpoint *types.RelayEndpoint,
	relay *types.GroupCallRelay,
	streamSSRCs [9]uint32,
	appDataSSRC uint32,
	participantPIDs []uint32,
	transactionID [12]byte,
	send func([]byte) error,
) (bool, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/99134bb900df3ee83a69d9a38112e623817597ae/datasheets/group-video-reactions.md#L36-L50
	if relay == nil {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasGroup && relay.TransactionID <= s.transactionID {
		return false, nil
	}
	if endpoint == nil || endpoint.RelayName == "" || endpoint.IPv4 == "" || endpoint.Port == 0 {
		return false, fmt.Errorf("meowcaller: active relay endpoint is incomplete")
	}
	if len(relay.Key) == 0 {
		return false, fmt.Errorf("meowcaller: group relay has no key")
	}
	if send == nil {
		return false, fmt.Errorf("meowcaller: group relay send is nil")
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
	packet := stun.BuildWasmStunAllocateRequestWithGroupSubscriptionsAndHBHFEC(
		transactionID,
		relay.Tokens[matched.TokenID],
		endpointXOR,
		streamSSRCs,
		appDataSSRC,
		s.hbhFECSSRCs,
		participantPIDs,
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
	// Source of truth: https://github.com/purpshell/meowcaller/blob/bcfb7f0c076b131422c22f024dfff080448e70f4/datasheets/group-media-relay-refresh.md#L52-L54
	// ASSUMPTION: binding-success uses the committed Allocate integrity key; a live
	// post-rotation binding-success authenticated by another key invalidates this.
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

func (s *groupRelayAllocateState) SendBindingSuccess(
	request []byte,
	send func([]byte) error,
) ([]byte, bool, error) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/bcfb7f0c076b131422c22f024dfff080448e70f4/datasheets/group-media-relay-refresh.md#L77-L111
	// ASSUMPTION: binding-success uses the committed Allocate integrity key; a live
	// post-rotation binding-success authenticated by another key invalidates this.
	if send == nil {
		return nil, false, fmt.Errorf("meowcaller: relay binding-success send is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	response, ok := buildRelayBindingSuccess(request, s.key)
	if !ok {
		return nil, false, nil
	}
	if err := send(response); err != nil {
		return response, true, err
	}
	return response, true, nil
}
