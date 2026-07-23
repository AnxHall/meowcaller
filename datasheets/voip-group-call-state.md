# Datasheet: `voip/group_call_state`

Internal whatsmeow group-call roster, transaction, relay, and signaling-target
state built from parsed `group_update` snapshots.

**Validation vector:** `group_call_state_corpus.json` — state-transition cases
copied from the capture corpus pinned below.

**Status:** verified in whatsmeow commit
`5a6350b9abb1facc20eba96b4c29ebc446053c14`; both state KATs run and pass.

**Reference pinned at:**

- capture SHA-256 `47e4966e1847b686b3a31c4983df8025617d200ec27a71c5884598488af65b90`
- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- capture SHA-256 `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def`
- capture SHA-256 `1851cf76118bc8ef116df4ea51db73968cef3d415996cdf34013bdee9ac27fc7`
- capture SHA-256 `fe6a59e96f37af1459b7ffcca1fb4909253683b3e4d2cb0b3af75cd173579247`

## Reference source (verbatim — authoritative)

The raw JSONL files are the verbatim authority. Their integrity manifest and
cross-capture classification are committed at
`diag/analysis/capture-corpus-v2-20260723-index.json`.

| Capture | Authoritative transition |
|---|---|
| `group-call-outgoing-v2-20260723-100703.jsonl` | transactions 16 → 20 → 22; group JID present; self PID 0; group control uses `CALLID@call` |
| `group-call-add-people-v2-20260723-112208.jsonl` | 1:1 state upgrades in place at transactions 16 → 18; group JID absent; self PID 1 |
| `group-call-multi-add-v2-20260723-135301.jsonl` | transaction 23 contains connected and receipt participants; pending participant has no PID |
| `group-call-preselected-participants-v2-20260723-140338.jsonl` | transactions 21 → 24 → 28 → 33 → 34; terminate arrives before the final roster update |
| `group-chat-selector-single-direct-v2-20260723-140636.jsonl` | one selected participant remains direct: no group snapshot and participant-addressed control |

The state vector must preserve these observed facts:

```text
group mode is established by group-call signaling, not GroupJID presence
GroupJID may be empty in a valid group snapshot
group transaction IDs increase but need not be contiguous
relay transaction IDs are independent from group transaction IDs
the entire participant/device/PID roster is replaced atomically by a newer snapshot
receipt/outgoing/invited participants remain in the roster without PIDs
self PID is advertised and is not a fixed constant
group-wide control routes to CALLID@call
direct control remains routed to the participant
a late update for a call already removed from call state must not recreate the call
```

## Go envelope (signatures only)

```go
package whatsmeow

type groupCallState struct {
	snapshot types.GroupCallUpdate
}

type callState struct {
	// Existing fields remain.
	group *groupCallState
}

func (cli *Client) applyGroupUpdate(update types.GroupCallUpdate) bool

func (cs *callState) signalingTarget() types.JID
```

`applyGroupUpdate` returns whether the snapshot became the call's current group
state. `signalingTarget` returns the existing direct destination until group
state exists, then returns the call address represented by
`types.NewJID(callID, "call")`.

This module does not build group offers, invite participants, emit public events,
or update media.

## Implementation suggestions (guidance, not authoritative)

- Apply and compare snapshots while holding the existing call-state lock so the
  roster, relay, and transaction advance atomically.
- Store the complete `types.GroupCallUpdate` value. Derive participant lookups
  from its ordered slice rather than maintaining a second map that can diverge.
- Accept only a strictly newer group transaction. Transaction gaps are valid.
- Treat an equal transaction as a duplicate and an older transaction as stale;
  neither should mutate state.
- If the call ID is no longer registered, return false and do not recreate it.
  This covers the observed terminate-before-final-update ordering.
- Set group mode from the accepted snapshot even when `GroupJID` is empty.
- Derive the group control target from the call ID and literal server `call`;
  do not use the originating chat JID.
- Do not log participant capabilities, relay keys, tokens, or opaque endpoint
  bytes.
- Approved: store the full ordered snapshot instead of an indexed participant
  map; the server is authoritative and the roster sizes are small.
- Approved: accept only a strictly newer transaction; all captured canonical
  snapshots advance monotonically, and equal delivery is a duplicate.
- Approved: `signalingTarget` falls back to the existing direct destination when
  `group == nil`, preserving current 1:1 routing until a group snapshot is
  accepted.
