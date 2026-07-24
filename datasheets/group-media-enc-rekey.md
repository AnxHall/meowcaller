# Datasheet: `media/group_enc_rekey`

Transaction-correlated installation of a decrypted keygen-v2 participant key into
exactly one group-audio receive pipeline.

**Validation vectors:** focused Go KATs in `session_test.go`,
`group_media_receive_test.go`, and `engine_lifecycle_test.go`.

**Reference pinned at:**

- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- capture SHA-256 `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def`
- Rust raw-key KDF commit `41095d4e6ba4610e054e9ede3af1d5e88a83faee`
- Rust receive-rekey commit `aafac5cf46e770f59a1ef2f842d2404154038692`

Live call `D66652FC17BF1F8BBA898DE097B428FA` on 2026-07-24 independently
corroborated the missing boundary: transaction 17 retained one remote participant,
packets reached that participant's derived SSRC but failed WARP authentication, and
that same author sent a transaction-17 `enc_rekey`.

## Reference source (verbatim — authoritative)

The immutable raw JSONL capture files are the wire authority. Their compact indexed
reports establish these ordering and ownership facts:

```text
group_update is an authoritative transaction-ordered roster snapshot
enc_rekey is authored by outer call[from], which identifies a participant device
enc_rekey distributes a per-sender participant key, not a shared global key
an enc_rekey is correlated with its transaction roster
on departure the remaining participant receiver persists and departed state is pruned
the decrypted keygen-v2 plaintext is exactly 32 raw key bytes
```

The existing verified raw-key KDF is:

```text
DeriveE2eKeysFromRaw(raw_e2e, canonical participant ID)
  -> HKDF-SHA256 raw_e2e[:32] with participant ID
  -> RFC 3711 cipher/auth/salt receive keys
```

The reference receive rekey swaps only receive keys and resets that receive
pipeline's ROC tracker. The live newly activated participant had no authenticated
ROC state, so reset and preserve are equivalent for that capture. Continuation
across an already-active participant's rollover remains unproven.

## Go envelope

```go
package meowcaller

func (p *MediaPipeline) RekeyRecvFromRaw(rawE2E []byte, peerJID string) error

func (r *participantReceiveRegistry) ApplyParticipantRawRekey(
	transactionID uint32,
	author types.JID,
	rawKey []byte,
) error

func (e *engine) onEncRekey(ev *events.CallEncRekey)
```

## Required state machine

```text
GroupUpdate(U), U <= rosterTx:
  ignore

GroupUpdate(U), U > rosterTx:
  atomically replace active connected PID-bearing device indexes
  preserve same-device receiver and decoder objects
  remove departed receivers and installed key epochs
  apply buffered rekeys through U in transaction order when their authors remain active
  discard unresolved past rekeys and retain future rekeys

EncRekey(K), no roster or K > rosterTx:
  buffer by (K, wire author); never mutate the 1:1 fallback

EncRekey(K), roster exists and K <= rosterTx:
  resolve the exact active device first
  otherwise resolve a bare user only when exactly one active device matches
  compare K with that participant's installed key epoch, not global rosterTx
  ignore K only when older than that participant's installed key epoch
  install only into that resolved participant receiver
  identical duplicate is a no-op; conflicting duplicate is rejected

Call end:
  discard pending and installed group-rekey state with the call
```

Author resolution must never select the sole remaining remote merely because only
one receiver exists.

## Validation boundaries

- The synthetic raw-key RTP KAT must reject before install and authenticate after.
- A participant rekey must leave every other participant's packet path unchanged.
- Rekey-before-roster and roster-before-rekey must reach the same state.
- A delayed rekey older than the latest roster must still install for an active
  author when that author's key epoch has not advanced.
- Per-author stale, future, unknown-author, ambiguous-author, duplicate-conflict,
  and departure cases must be covered.
- Signal decryption remains a live end-to-end boundary in Whatsmeow.
- The target receiver ROC reset is `NOT VALIDATED` for an already-active stream
  crossing a sequence rollover; no capture vector proves preserve versus reset.
