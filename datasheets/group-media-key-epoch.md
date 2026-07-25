# Datasheet: `media/group_key_epoch`

Transaction-wide installation of one decrypted keygen-v2 raw E2E epoch across
the local sender and every active group-audio receiver without recreating RTP
streams.

**Validation vectors:** immutable two-sided add-person capture plus focused Go
KATs in `session_test.go`, `group_media_receive_test.go`, and
`engine_lifecycle_test.go`.

**Reference pinned at:**

- capture SHA-256 `9d6463714430c55ddb3ccb95e153f1d06d11a1feea7a153d1ea95f39f48b6889`
- wacrg group-call crypto commit `4a2d5488b21251303381661aab1ee9bbf4d2cccc`
- Rust raw-key KDF commit `41095d4e6ba4610e054e9ede3af1d5e88a83faee`

This module corrects the participant-scoped interpretation recorded by
`media/group_enc_rekey`. The capture proves that the outer `from` selects the
rekey master/distributor: C distributes transaction 14 to A and B, then A
distributes transaction 16 to C. It does not identify the only media direction
that consumes the new raw key.

## Reference source (verbatim — authoritative)

The immutable two-sided capture report records:

```text
tx14: only C receives group_info rekey="1"
tx14: C sends the same transaction's enc_rekey separately to A and B
tx16: only A receives group_info rekey="1"
tx16: A sends enc_rekey to C
RTP sender SSRC and sequence continuity survive the key transition
```

The user designated the local wacrg specification authoritative. Its group-call
crypto contract states:

```text
A single 32-byte call key is shared with every group-call participant
The same callKey is used by every member; there is no per-pair key
The info label always carries the sender's participant id
Each participant MUST derive its send key from its own normalized id
To receive, it MUST derive a peer's key from that peer's normalized id
```

The pinned Rust keygen-v2 KDF states:

```text
derive_e2e_keys_from_raw(raw_e2e, participant_lid)
  rejects raw_e2e shorter than 32 bytes
  uses raw_e2e[0..32] as HKDF-SHA256 IKM
  uses participant_lid as HKDF info
  derives the RFC 3711 cipher, authentication, and salt keys
```

Together these sources establish that one accepted raw epoch must derive:

```text
send keys = KDF(raw epoch, local normalized device ID)
recv keys for peer P = KDF(raw epoch, P's normalized device ID)
```

The outer `enc_rekey from` remains signaling metadata for authenticating and
auditing the selected distributor. It is not a media-key ownership selector.

## Go envelope

```go
package meowcaller

func (p *MediaPipeline) RekeySendFromRaw(rawE2E []byte, selfJID string) error

func (p *MediaPipeline) RekeyRecvFromRawPreservingROC(
	rawE2E []byte,
	peerJID string,
) error

func (r *participantReceiveRegistry) ApplyGroupRawEpoch(
	transactionID uint32,
	rawKey []byte,
) error
```

The live engine installs one accepted epoch into the audio sender pipeline and
the receive registry as one operation. The signaling author is retained in
diagnostics only.

## Required state machine

```text
GroupUpdate(U), U <= rosterTx:
  ignore

GroupUpdate(U), U > rosterTx:
  atomically replace the active connected PID-bearing device indexes
  preserve same-device receiver, decoder, RTP, and authenticated ROC objects
  remove departed receivers
  install the newest buffered epoch K where K <= U
  keep future buffered epochs

EncRekey(K), no roster or K > rosterTx:
  buffer one transaction-wide raw epoch by K

EncRekey(K), roster exists and K <= rosterTx:
  K < installedTx: ignore
  K == installedTx and bytes equal: no-op
  K == installedTx and bytes differ: reject
  K > installedTx:
    derive every active receive key before mutating any pipeline
    install the raw epoch into all active receivers
    install the same raw epoch into the local send pipeline
    record K only after every derivation and installation succeeds

Call end:
  discard pending and installed epochs with the call
```

An update may remove every remote receiver. The sender still adopts the accepted
epoch so that a subsequently connected participant receives media under the
current call key.

## Continuity and concurrency requirements

- Rekeying must not recreate the audio RTP stream.
- The next outbound packet keeps the same SSRC and advances the existing
  sequence/timestamp/counter state.
- The sender ROC is preserved because the packet sequence space is preserved.
- Each receiver ROC is preserved because the capture shows a key change over a
  continuing RTP stream; resetting the estimator would make sequence rollover
  depend on the key boundary rather than the stream boundary.
- Send protection and send-key replacement must share one mutex so a packet
  cannot combine a key from one epoch with ROC/stream state from another.
- Receive authentication and receive-key replacement must share the existing
  receive mutex.
- Derive all replacement keys before installing any of them; a malformed raw key
  must leave the working epoch untouched.

## Validation boundaries

- A packet under the new raw epoch must fail before installation and authenticate
  after installation for every active receiver.
- A packet emitted after installation must authenticate only under the new raw
  epoch derived with the local participant ID.
- The outbound packet immediately after installation must preserve SSRC and
  continue sequence/timestamp state.
- Receive ROC state must continue across installation and authenticate the next
  post-wrap packet.
- Rekey-before-roster and roster-before-rekey must converge on the same
  transaction-wide epoch.
- Stale, identical duplicate, conflicting duplicate, malformed-key, future,
  departure, and call-cleanup cases must be covered.
- Ordinary 1:1 media remains on its original call key unless a typed group rekey
  event is received.
- Live Signal encryption/decryption and WhatsApp acceptance remain end-to-end
  boundaries outside this module.
