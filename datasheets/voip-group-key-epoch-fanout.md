# Datasheet: `voip/group_key_epoch_fanout`

Generation, direct Signal encryption, fan-out, and local installation of one
shared group-call keygen-v2 epoch when the server nominates this endpoint with
`group_info rekey="1"`.

**Validation vectors:** immutable two-sided add-person capture, wire-builder KATs
in `voip/group_rekey_test.go`, and state/orchestration KATs in
`call_group_rekey_test.go`.

**Reference pinned at:**

- capture SHA-256 `9d6463714430c55ddb3ccb95e153f1d06d11a1feea7a153d1ea95f39f48b6889`
- wacrg group-call crypto commit `4a2d5488b21251303381661aab1ee9bbf4d2cccc`
- existing Whatsmeow Signal device encryption boundary

## Reference source (verbatim — authoritative)

The immutable two-sided capture records:

```text
tx14: only C receives group_info rekey="1"
tx14: C sends one direct enc_rekey to A
tx14: C sends one direct enc_rekey to B
tx16: only A receives group_info rekey="1"
tx16: A sends one direct enc_rekey to C
```

Each direct stanza has this shape:

```xml
<call to="RECIPIENT_DEVICE@lid" id="REQUEST_ID">
  <enc_rekey
    call-id="CALL_ID"
    call-creator="CALL_CREATOR"
    transaction-id="TRANSACTION_ID">
    <encopt keygen="2"/>
    <enc v="2" type="msg" count="0">SIGNAL_CIPHERTEXT</enc>
  </enc_rekey>
</call>
```

The encrypted Signal plaintext is the existing Whatsmeow protobuf envelope:

```text
Message {
  Call {
    callKey: 32-byte shared raw epoch
  }
}
```

The authoritative group-call crypto specification states that the same 32-byte
call key is shared by every participant. Device-specific Signal ciphertexts
transport that one root; they do not create per-recipient media roots.

## Go envelope

```go
package voip

type GroupEncRekeyParams struct {
	CallID        string
	To            types.JID
	CallCreator   types.JID
	TransactionID uint32
	RequestID     string
	DeviceKey     DeviceKey
}

func BuildGroupEncRekey(params GroupEncRekeyParams) (binary.Node, error)
```

```go
package whatsmeow

func groupRekeyRecipients(update types.GroupCallUpdate, self types.JID) []types.JID

func (cli *Client) distributeRequestedGroupEpoch(
	ctx context.Context,
	meta types.BasicCallMeta,
	update types.GroupCallUpdate,
) error

func (cli *Client) installGroupKeyEpoch(
	meta types.BasicCallMeta,
	rekey types.GroupCallEncRekey,
	rawKey []byte,
	data *binary.Node,
	local bool,
) error
```

`events.CallEncRekey` remains the additive media handoff event, but its contract
is corrected to mean one shared group epoch. `From` identifies the selected
distributor and `Local` distinguishes locally generated from decrypted inbound
epochs.

## Required behavior

```text
Accepted GroupUpdate(U), RekeyRequested false:
  dispatch the roster snapshot only

Accepted GroupUpdate(U), RekeyRequested true:
  dispatch the roster snapshot first
  select every connected PID-bearing device except this exact device
  deduplicate exact device JIDs while preserving roster order
  generate exactly one cryptographically random 32-byte root
  Signal-encrypt the same protobuf plaintext independently to every recipient
  build one direct enc_rekey stanza per recipient with transaction U
  send every stanza
  only after all sends succeed:
    install U/root into call state
    dispatch one Local CallEncRekey event for media

Inbound decrypted EncRekey(K):
  require a registered call and exactly 32 raw bytes
  K < installedKeyTx: ignore
  K == installedKeyTx and bytes equal: no-op
  K == installedKeyTx and bytes differ: reject
  K > installedKeyTx:
    replace callState.callKey with an owned copy
    record K
    dispatch one non-Local CallEncRekey event
    call maybeEmitMediaReady
```

For an added participant's keyless active-call invite, the last step is what
unblocks the first `CallMediaReady`: the enriched offer already installed the
relay and roster, but intentionally carried no 1:1 offer key.

For an existing active participant, `CallMediaReady` remains one-shot. The
`CallEncRekey` event rotates the live Meowcaller media session in place.

## Recipient and ordering rules

- Only `participant.state == "connected"` contributes recipients.
- Only devices with `HasPID` contribute recipients; PID zero is valid.
- Exclude only the exact local device JID, not every device belonging to the
  local user.
- Preserve authoritative participant/device order while deduplicating exact
  device JIDs.
- The locally generated root must not be installed if encryption or any send
  fails. A partial network send is reported and the next server transaction is
  the recovery boundary; do not silently invent a second root for the same
  transaction.
- Duplicate/stale group snapshots do not regenerate because the group-state
  transaction gate rejects them before distribution.

## Validation boundaries

- Builder KATs pin all outer/action/child attributes and reject missing/invalid
  call IDs, creator, recipient, transaction, request ID, ciphertext, version, and
  encryption type.
- Recipient KATs cover connected versus invited/receipt devices, PID zero,
  missing PID, self exclusion, and deterministic deduplication.
- Fan-out KATs prove one 32-byte root, identical plaintext root for every
  encryption, one captured-shape stanza per recipient, unique request IDs, and
  no local handoff before all sends succeed.
- State KATs prove added-participant media readiness, existing-call one-shot
  readiness, stale/duplicate/conflict handling, owned bytes, and no raw key in
  logs.
- The generic Whatsmeow send-log boundary replaces every nested binary node body
  with its byte length before formatting. The original node remains unchanged
  for serialization, while Signal ciphertext, identity data, privacy tokens, and
  other binary secrets never enter debug output.
- Live Signal encryption/decryption and server acceptance remain end-to-end
  boundaries.
