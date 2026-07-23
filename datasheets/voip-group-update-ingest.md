# Datasheet: `voip/group_update_ingest`

Inbound WhatsApp `group_update` parsing and typed event dispatch at the
whatsmeow call-control boundary.

**Validation vector:** `group_update_corpus.json` — sanitized fixtures copied
from the capture events pinned below.

**Status:** scaffolded in whatsmeow commit
`285aa8bb99e242a207a29e1c86711902769c35aa`; the KAT is skipped on the handler
body.

**Reference pinned at:**

- capture SHA-256 `47e4966e1847b686b3a31c4983df8025617d200ec27a71c5884598488af65b90`
- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- capture SHA-256 `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def`
- capture SHA-256 `1851cf76118bc8ef116df4ea51db73968cef3d415996cdf34013bdee9ac27fc7`

These captures were approved by the human reviewer as the authoritative source
for this module on 2026-07-23.

## Reference source (verbatim — authoritative)

The raw JSONL files are immutable and remain the verbatim authority. The compact
corpus manifest is
`diag/analysis/capture-corpus-v2-20260723-index.json`.

| Capture | Boundary | Authoritative events |
|---|---|---|
| `diag/captures/group-call-outgoing-v2-20260723-100703.jsonl` | 18,629 events; 21,836,404 bytes | line 2627 / sequence 607: group-JID-backed transaction 16; line 3488 / sequence 1264: transaction 20 |
| `diag/captures/group-call-add-people-v2-20260723-112208.jsonl` | 8,372 events; 12,762,523 bytes | line 3995 / sequence 12667: ad-hoc transaction 16; line 5613 / sequence 14036: transaction 18 |
| `diag/captures/group-call-multi-add-v2-20260723-135301.jsonl` | 7,192 events; 9,572,198 bytes | line 2796 / sequence 20787: transaction 23 with connected and receipt participants |
| `diag/captures/group-call-preselected-participants-v2-20260723-140338.jsonl` | 8,661 events; 10,313,200 bytes | line 899 / sequence 24709: ad-hoc transaction 21; line 3046 / sequence 26476: transaction 28 |

The vector must preserve these exact observed paths:

```text
call.attrs.from
call.attrs.id
call.attrs.t
call/group_update.attrs.call-id
call/group_update.attrs.call-creator
call/group_update/group_info.attrs.call-id
call/group_update/group_info.attrs.call-creator
call/group_update/group_info.attrs.transaction-id
call/group_update/group_info.attrs.media
call/group_update/group_info.attrs.connected-limit
call/group_update/group_info.attrs.group-jid        optional
call/group_update/group_info.attrs.joinable         optional
call/group_update/group_info/user.attrs.jid
call/group_update/group_info/user.attrs.user_pn     optional
call/group_update/group_info/user.attrs.state
call/group_update/group_info/user/device.attrs.jid
call/group_update/group_info/user/device.attrs.pid  connected device only
call/group_update/group_info/user/device.attrs.platform
call/group_update/group_info/user/device/capability.attrs.ver
call/group_update/av_upgrade.attrs.av-upgradable    optional
call/group_update/relay.attrs.transaction-id
call/group_update/relay.attrs.self_pid
call/group_update/relay.attrs.uuid
call/group_update/relay.attrs.participant_uuid
call/group_update/relay.attrs.warp_mi_tag_len
call/group_update/relay/token.attrs.id
call/group_update/relay/auth_token.attrs.id
call/group_update/relay/key
call/group_update/relay/hbh_key
call/group_update/relay/te2.attrs.relay_id
call/group_update/relay/te2.attrs.token_id
call/group_update/relay/te2.attrs.auth_token_id
call/group_update/relay/te2.attrs.relay_name
call/group_update/relay/te2.attrs.domain_name
call/group_update/relay/te2.attrs.c2r_rtt
call/group_update/relay/te2.attrs.is_fna
call/group_update/relay/te2
```

Observed constraints represented by those events:

```text
group-jid is present in a group-chat-backed call and absent in valid ad-hoc calls
group transaction IDs are monotonic but not contiguous
relay transaction IDs are independent from group transaction IDs
outgoing, receipt, connected, and invited users can coexist in one snapshot
only connected devices carry PIDs
the outer call address is CALLID@call
the existing deferred call ACK covers group_update
```

## Go envelope (signatures only)

```go
package events

type CallGroupUpdate struct {
	types.BasicCallMeta
	Update types.GroupCallUpdate
	Data   *waBinary.Node
}
```

```go
package whatsmeow

func (cli *Client) onCallGroupUpdate(
	ctx context.Context,
	child *waBinary.Node,
	meta types.BasicCallMeta,
)
```

The existing dispatcher gains a `case "group_update"` branch that calls
`onCallGroupUpdate`. The existing `voip.ParseGroupUpdate` and
`types.GroupCallUpdate`/participant/device/relay declarations remain the parser
surface. On successful parse, the handler delegates transaction acceptance to
the verified `applyGroupUpdate` state module and dispatches `CallGroupUpdate`
only when that module accepts the snapshot. This module does not implement
call-state merging or media behavior itself.

## Implementation suggestions (guidance, not authoritative)

- Parse the action child with `voip.ParseGroupUpdate`.
- Pass the value snapshot to `applyGroupUpdate` before dispatch. Dispatch exactly
  one typed event only when the state module accepts the snapshot; duplicate,
  stale, and post-terminate updates remain acknowledged but do not escape as
  authoritative state changes.
- Keep the existing deferred call ACK path; captured clients ACK
  `group_update`, and the current dispatcher already defers that ACK for every
  non-video action.
- Do not require `GroupJID`; commit `f6065a6` already KAT-covers the valid
  ad-hoc shape.
- Keep transaction ordering and call-state mutation logic out of this ingestion
  module. `applyGroupUpdate` owns those decisions.
- Preserve relay keys/tokens as opaque bytes in the typed snapshot, but never
  log their contents.
- Approved scaffold choice: `Update` is a value, so the event owns a stable
  snapshot while `Data` retains the raw node for consumers that need unparsed
  fields.
- Approved scaffold choice: retain `Data *waBinary.Node` for consistency with
  existing call events until the parser covers every observed optional child.
- `TODO(human)`: approve dispatching only snapshots accepted by
  `applyGroupUpdate`, rather than every successfully parsed wire update. Agent
  suggestion: accepted only, so stale, duplicate, and post-terminate delivery
  cannot leak around the verified state gate.
- `TODO(human)`: choose malformed-update behavior.
  Agent suggestion: warn with sanitized call metadata, dispatch
  `UnknownCallEvent`, and still allow the deferred ACK to run so a malformed
  optional field cannot trigger a server resend loop.
