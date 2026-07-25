# Datasheet: `voip/group_rekey_directive`

Typed preservation of the server-selected `group_info rekey="1"` participant
nomination at the whatsmeow group-call parsing boundary.

**Validation vector:** `group_rekey_directive_corpus.json` — sanitized
group-info fixtures copied from the immutable two-sided capture below.

**Reference pinned at:** capture SHA-256
`9d6463714430c55ddb3ccb95e153f1d06d11a1feea7a153d1ea95f39f48b6889`

## Reference source (verbatim — authoritative)

The verbatim authority is
`diag/captures/whatsapp-20260724-222532.jsonl`: 19,285 events and 26,287,992
bytes. The raw file remains unchanged. The vector uses metadata only and does
not copy any key, token, ciphertext, or relay credential.

| Raw boundary | Captured fact |
|---|---|
| lines 10,293 and 10,302; transaction 14 | A, B, and C are connected; only C's personalized `group_info` contains `rekey="1"` |
| lines 13,640–13,656; transaction 16 | A and C remain connected; only A's personalized `group_info` contains `rekey="1"` |
| lines 14,135 and 15,322; transaction 16 | the selected A endpoint sends `enc_rekey` to the other connected endpoint |

The vector must preserve these exact relationships:

```text
group_info.attrs.rekey absent  -> RekeyRequested false
group_info.attrs.rekey == "1"  -> RekeyRequested true
rekey is personalized per receiving endpoint
the group transaction ID remains the epoch identifier
the directive does not itself contain key material
```

This module does not generate keys, encrypt `enc_rekey`, choose recipients,
deduplicate transactions, retry sends, or change media keys.

## Go envelope (signatures only)

```go
package types

type GroupCallUpdate struct {
	// Existing fields remain.
	RekeyRequested bool
}
```

The existing `voip.ParseGroupUpdate` parser remains the function surface.

## Implementation suggestions (guidance, not authoritative)

- Read the optional `rekey` attribute from `group_info` while parsing the same
  snapshot that owns `transaction-id`.
- Treat only the captured literal `"1"` as a request. Absence remains false.
- Keep the value on the immutable `GroupCallUpdate` snapshot so transaction
  ordering and duplicate rejection remain owned by the existing group-state
  module.
- Do not infer a request from participant count, relay transaction changes, or
  receipt/connected state.
- Do not log or attach any key material to the directive.
