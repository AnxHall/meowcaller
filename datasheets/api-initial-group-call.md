# Datasheet: `api/initial_group_call`

The Meowcaller root API starts one preselected multi-participant audio call,
publishes the selected remotes immediately, and hands authoritative roster and
key epochs to media only after Whatsmeow identifies a connected device.

**Validation vectors:** focused root-package KATs in
`group_call_start_test.go`, backed by the Task 1 initial-offer capture contract.

**Reference pinned at:** `7cb6045001dafd2514f53e85cd8c3e419c13adbe`

## Reference source (verbatim — authoritative)

From `datasheets/voip-initial-group-call.md` at the pinned commit:

```text
Only the local device carries `capability ver="1"` and the seven-byte
capability blob. Every selected remote bare LID and all discovered candidate
devices are present, in selected-user order. The group-chat-bound capture adds
only `group-jid="120363411251996986@g.us"` to the `offer`; the ad-hoc offer
omits it.

At transaction 11, self is `connected` without a PID and all three remotes are
`outgoing` without PIDs. At transaction 21, self is `connected` with PID 0,
`242653052539031@lid` is `connected` through device
`242653052539031@lid` with PID 1, and the other selected users are `receipt`.
```

The pinned Go/Whatsmeow envelope is:

```go
type GroupCallOfferOptions struct {
	GroupJID types.JID
}

func (cli *Client) OfferGroupCall(
	ctx context.Context,
	targets []types.JID,
	options ...GroupCallOfferOptions,
) (callID string, err error)

type CallOffer struct {
	types.BasicCallMeta
	types.CallRemoteMeta
	Data  *waBinary.Node
	Video bool
	Group *types.GroupCallUpdate
}
```

The pinned implementation guidance states:

```text
Keep the selected first bare LID only as the legacy direct peer fallback.
Group acceptance must not overwrite it.

Clone the parsed optional invite snapshot onto `events.CallOffer`; direct
offers leave the field nil.
```

## Go envelope

```go
package meowcaller

type GroupCallOptions struct {
	GroupJID string
}

func (c *Client) GroupCall(
	ctx context.Context,
	targets ...string,
) (*Call, error)

func (c *Client) GroupCallWithOptions(
	ctx context.Context,
	targets []string,
	opts GroupCallOptions,
) (*Call, error)
```

## Required behavior

- Initial group calls are audio-only. `GroupCallOptions` has no video field and
  the root API does not reuse `CallOptions.Video`.
- Trim and parse every target, normalize device JIDs to their non-AD form, and
  deduplicate exact normalized JIDs while preserving first order. Require at
  least two remaining targets.
- Keep PN and LID aliases distinct at this boundary. Whatsmeow remains
  authoritative for PN-to-LID resolution and for duplicate/self rejection
  after resolution.
- Parse a non-empty optional group JID as one canonical `g.us` JID. Reject
  malformed, non-canonical, non-group, device, or empty-user values.
- Delegate once to `whatsmeow.Client.OfferGroupCall`. On success create one
  outgoing `Call` in `CallPhaseCalling` whose public peer remains the first
  selected normalized target.
- Publish a transaction-zero selected-only public roster with captured
  `outgoing` states. This speculative UI seed never enters the media receive
  registry; the first authoritative Whatsmeow snapshot replaces it.
- Mark group scope explicitly in engine state. Group accept and media-ready
  events never overwrite the stable public peer or direct-peer rekey state.
- Cache a cloned `CallOffer.Group` snapshot and publish it before invoking
  `OnIncomingCall`, so an active ad-hoc invite can replay its roster
  immediately.
- Coalesce participant-directed offers that reuse an active logical call ID:
  advance the cloned authoritative roster without invoking `OnIncomingCall`
  again. Once the logical call has ended, ignore later same-ID offers instead
  of mutating or redispatching its tombstoned engine entry.
- Group media readiness supplies the connected PID-bearing device used for
  media key/receiver derivation. Launch media once, then replay the latest
  authoritative roster followed by every queued raw key epoch.
- Reject explicit remote targets unless they contain exactly one `@`, a
  non-empty user, and either the PN (`s.whatsapp.net`) or LID (`lid`) user
  server. The singular `Call` parser remains unchanged.
- Unknown roster/rekey placeholders have a bounded lifetime. Offer failure and
  placeholder expiry remove only the expected entry and clear every owned
  pending raw key and call key before deletion. Attachment cancels expiry, and
  a racing stale expiry cannot remove the attached call.
- Group readiness received before the outgoing public call attaches caches
  media fields on the placeholder without synthesizing a `Call` or launching
  media. Attachment supplies the selected public identity and then starts
  media once with the readiness-selected device.
- Media activation keeps live handlers unpublished while draining. It applies
  each captured batch as roster then raw epochs, repeats for arrivals queued
  during the drain, and publishes the live handlers only after the queues are
  empty. A second activation cannot replace handlers during or after the first.

## Validation boundaries

- Focused KATs cover target normalization/deduplication/order, strict optional
  group-JID parsing, one-shot delegation, error preservation, PN/LID handoff,
  the selected-only public seed, incoming snapshot cloning/replay, stable peer
  behavior, repeated same-ID offer coalescing, post-end offer suppression,
  connected-device media identity, one-shot launch, and queued roster/key
  ordering.
- Review regressions cover strict remote-user JIDs, pointer-safe offer-error
  cleanup, deterministic unknown-placeholder expiry, racing expiry after
  attachment, pre-return roster/rekey/readiness ordering, and arrivals during
  an activation drain.
- Existing direct-call and active participant-invite KATs remain the
  compatibility gate.
- Live WhatsApp offer-to-ACK-to-rekey-to-media audio remains the human E2E gate.
