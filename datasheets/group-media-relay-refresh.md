# Datasheet: `media/group_relay_refresh`

Refresh the active relay allocation in place when an ordered group update rotates
its credentials.

**Validation vector:** `engine_group_relay_test.go`, derived from the immutable v2
add-people capture.

**Reference pinned at:**

- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- capture version `wa-voip-diag/v2`
- STUN encoder commit `41095d4e6ba4610e054e9ede3af1d5e88a83faee`

## Reference source (verbatim — authoritative)

The immutable capture
`diag/captures/group-call-add-people-v2-20260723-112208.jsonl` records the
critical 1:1-to-group transition:

```text
line 3987: group_update transaction-id="16"
             relay transaction-id="1"
             uuid="AolanqjRtInKNvma"
             participant_uuid="WQbrQfSx"
line 4001: "event":"send"
             "byteLength":336
             "kind":"stun"
             "messageType":3
             "messageLength":316
line 4006: "voip: onCallEvent RelayListUpdate"
line 4008: "get_local_candidates: do nothing, socket already created based on num_conns."
```

The indexed equality analysis records:

```text
"relay_uuid_stable": true
"active_relay_stable": true
"initial_to_group_relay_key_rotated": true
"group_tx1_to_tx2_relay_key_rotated": false
"hbh_key_stable_across_all_allocations": true
"tokens_rotated_each_allocation": true
"auth_token_rotated_each_allocation": true
```

The 336-byte packet is a Web/WASM Allocate request (`0x0003`) carrying the new
relay token, the existing nine stream descriptors, the same active IPv4 relay
endpoint, and MESSAGE-INTEGRITY under the group relay key.

## Go envelope

```go
type groupRelayAllocateState struct {
	// current encoded Allocate, integrity key, and latest committed group relay transaction
}

func newGroupRelayAllocateState(initial, initialKey []byte) *groupRelayAllocateState

func (s *groupRelayAllocateState) Current() []byte

func (s *groupRelayAllocateState) CurrentKey() []byte

func (s *groupRelayAllocateState) Apply(
	endpoint *types.RelayEndpoint,
	relay *types.GroupCallRelay,
	streamSSRCs [9]uint32,
	transactionID [12]byte,
	send func([]byte) error,
) (changed bool, err error)

func buildRelayBindingSuccess(request, integrityKey []byte) (response []byte, ok bool)
```

## Required behavior

1. Match the already-connected active relay by `relay_name`; do not recreate the
   transport while that relay remains present.
2. Select the rotated token through the matched endpoint's `token_id`.
3. Rebuild Allocate with the group relay key, the existing active endpoint, and
   the original stream SSRC set.
4. Send it immediately on the existing DataChannel.
5. Only after that send succeeds, atomically replace both the packet used by the
   one-second Allocate keepalive and the integrity key used for binding-success
   responses.
6. Ignore stale or duplicate group relay transaction IDs.
7. Reject missing active-relay, token, key, or endpoint data without silently
   retaining a mismatched allocation.
8. A failed immediate send must preserve the prior packet, key, and committed
   transaction so the same group relay transaction remains retryable.
9. Authenticate later binding-success responses with the currently committed
   relay key, not the endpoint's original 1:1 key.

## Validation boundaries

- The focused KAT proves that successfully applying relay transaction 1 replaces
  the initial 1:1 Allocate and integrity key with the selected rotated token and
  group key.
- A failed immediate send leaves the initial packet and key intact and permits
  retrying the same group relay transaction.
- A binding-success encoded after the successful transition validates under the
  rotated key and differs from the response authenticated by the initial key.
- The active address, stream SSRCs, RTP pipeline, SSRC, sequence, timestamp, and
  DataChannel remain untouched.
- Active-relay migration is not proven by this capture and is outside this
  module; the observed active relay is stable.
- HBH key installation is not part of this request. The captured HBH key is
  stable and the existing media path does not enable HBH SRTP.
