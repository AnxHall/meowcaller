# Datasheet: `voip/group_invite_accept`

Acceptance of a directed invitation into an already-active ad-hoc call, using
the enriched `group_info` embedded in the incoming offer to establish group
state and call-scoped signaling before `preaccept`.

**Validation vector:** `group_invite_accept_corpus.json` — sanitized 1:1 and
active ad-hoc offer fixtures copied from the immutable two-sided capture below.

**Reference pinned at:** capture SHA-256
`9d6463714430c55ddb3ccb95e153f1d06d11a1feea7a153d1ea95f39f48b6889`

## Reference source (verbatim — authoritative)

The verbatim authority is
`diag/captures/whatsapp-20260724-222532.jsonl`: 19,285 events and 26,287,992
bytes. The raw file remains unchanged. The vector contains identifiers and
roster metadata only; it excludes call keys, relay credentials, ciphertext,
device identities, and VoIP settings.

| Raw boundary | Captured fact |
|---|---|
| line 9,467 | the inviter's directed offer contains the existing connected roster but no group transaction attributes |
| line 9,572 | the added endpoint receives an enriched offer with `joinable="1"` and embedded `group_info` transaction 7; no group JID or encrypted call key is present |
| lines 9,608–9,609 | the added endpoint sends `preaccept` to `CALLID@call`, not directly to the inviter device |
| line 10,227 | the added endpoint invokes `acceptCall` |
| lines 10,231–10,237 | it immediately sends `accept` to `CALLID@call` with exactly `audio`, `net`, and `encopt`; no `metadata` child is present |
| line 10,285 | the server acknowledges that `accept` |
| line 10,684 | the first inbound `mute_v2` arrives after the accept and its acknowledgement |

The vector must preserve these relationships:

```text
offer without group_info               -> no group snapshot; direct signaling
offer with enriched group_info         -> transaction-ordered group snapshot
ordinary 1:1 offer                      -> encrypted call key required
enriched active-call offer              -> accepted without an offer call key
offer joinable == "1"                  -> snapshot Joinable true
ad-hoc group without group-jid         -> valid empty GroupJID
snapshot installed before preaccept    -> preaccept target CALLID@call
active-group AcceptCall                -> immediate accept to CALLID@call
active-group accept children           -> audio, net, encopt; no metadata
active-group accept send failure       -> not connected; retry remains possible
ordinary 1:1 AcceptCall                -> existing deferred accept behavior
pending group call without a call key   -> no media-ready event yet
```

The embedded snapshot is the safe bridge across the captured
`group_update-before-offer` order. This module does not retain arbitrary updates
for unknown call IDs, so a late post-terminate update still cannot recreate a
call.

The missing offer key is intentional protocol shape, not a malformed 1:1 offer.
The selected endpoint later originates the membership epoch when a server
snapshot carries `rekey="1"`. Until that separate module supplies the generated
raw key, `maybeEmitMediaReady` remains gated by the existing nil-key check.

This module does not generate or distribute that rekey, allocate a relay, select
the winning invitee device, change media keys, or claim that the invitee has
joined before a later connected PID-bearing snapshot.

## Go envelope (signatures only)

```go
package voip

func ParseGroupInviteSnapshot(offer *binary.Node) (*types.GroupCallUpdate, bool, error)

package whatsmeow

func (cli *Client) AcceptCall(ctx context.Context, callID string) error
```

The existing `acceptInboundOffer` installs the returned snapshot on the newly
registered call before building eager `preaccept`. The existing
`callState.signalingTarget` selects `CALLID@call` once that snapshot exists.
`AcceptCall` branches on that installed group state: group acceptance is sent
immediately, while an ordinary 1:1 call retains the existing deferred path.

## Implementation suggestions (guidance, not authoritative)

- Treat an absent `group_info` as an ordinary 1:1 offer, returning
  `(nil, false, nil)`.
- Parse the embedded `group_info` through the same attribute, participant, and
  device parser used by `ParseGroupUpdate`; do not maintain a second roster
  parser.
- Take call ID and creator from the offer envelope and reject conflicting
  non-empty values on `group_info`.
- Copy `joinable="1"` from the offer envelope onto the typed snapshot because
  the captured embedded `group_info` does not repeat it.
- Require and decrypt the existing `<enc>` key for ordinary 1:1 offers. For an
  enriched active-call offer, register the call with a nil key and let the
  outbound group-rekey module provide the first media epoch.
- Install the complete snapshot while registering the inbound call, before
  `BuildEagerPreaccept`.
- Use `signalingTarget()` for eager `preaccept`.
- For an installed active-group snapshot, send `accept` immediately to
  `CALLID@call` with `audio` Opus/16 kHz, `net` medium 2, and `encopt` keygen 2.
  Do not attach direct-call metadata.
- Send outside the call-state lock. Mark only the exact state connected after a
  successful send; a failed send must clear its in-flight reservation and remain
  retryable.
- Coalesce concurrent or repeated active-group acceptance so one state produces
  at most one successful wire accept. Preserve the ordinary 1:1 deferred path.
- Do not dispatch a synthetic `CallGroupUpdate`; later real server snapshots
  remain the membership and media-roster lifecycle source.
- Preserve the existing media-ready nil-key gate so an accepted active-call
  invite cannot start media with fabricated or stale key material.
