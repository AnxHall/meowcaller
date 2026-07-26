# Datasheet: `web/initial_group_call`

The localhost browser console starts one audio or video call to multiple
selected people, or to a group resolved from its numeric ID or `@g.us` JID,
while keeping established-call participant invitations as a separate control.

**Validation vectors:** focused controller, HTTP, DOM, and SSE ordering KATs in
`examples/web/controller_test.go` and `examples/web/server_test.go`, backed by
the Task 1 initial-group capture and Task 2 API contract.

**Status:** partial; focused controller, HTTP, DOM, and SSE ordering KATs pass.
Live group audio E2E remains pending.

**Reference pinned at:**

- capture SHA-256
  `1851cf76118bc8ef116df4ea51db73968cef3d415996cdf34013bdee9ac27fc7`
- Task 2 contract commit
  `ceaa2156015e8f24e09328fb7a9c89203295efff`
- Task 2 implementation commit
  `b2bccd011162c8e6acde6d7aa44054af2a9b5fac`
- Task 2 reviewed lifecycle commit
  `affe1ae57eedb555feb7beb655ca5f00618b5f9a`

## Reference source (verbatim — authoritative)

The immutable raw capture is
`diag/captures/group-call-preselected-participants-v2-20260723-140338.jsonl`.
It is 10,313,200 bytes and 8,661 JSONL records. Line 186 / signaling sequence
24380 is the decoded network-out initial offer, line 341 / sequence 24492 is
the transaction-11 server ACK, and line 893 / sequence 24707 is the first
decoded update with a connected remote PID and group relay allocation.

The capture-backed Task 2 contract at
`ceaa2156015e8f24e09328fb7a9c89203295efff` states:

```text
Initial group calls are audio-only. `GroupCallOptions` has no video field and
the root API does not reuse `CallOptions.Video`.

Delegate once to `whatsmeow.Client.OfferGroupCall`. On success create one
outgoing `Call` in `CallPhaseCalling` whose public peer remains the first
selected normalized target.

Cache a cloned `CallOffer.Group` snapshot and publish it before invoking
`OnIncomingCall`, so an active ad-hoc invite can replay its roster
immediately.
```

The implemented Task 2 API at
`b2bccd011162c8e6acde6d7aa44054af2a9b5fac` is:

```go
type GroupCallOptions struct {
	// GroupJID binds the call to a WhatsApp group. Leave empty for an ad-hoc call.
	GroupJID string
	Video    bool
}

func (c *Client) GroupCallWithOptions(
	ctx context.Context,
	targets []string,
	opts GroupCallOptions,
) (*Call, error)
```

At that same commit, `engine.onOffer` stores and publishes the cloned embedded
group snapshot before calling the incoming-call handler, and
`Call.OnGroupState` synchronously replays a cached snapshot on listener
registration. The final reviewed lifecycle at
`affe1ae57eedb555feb7beb655ca5f00618b5f9a` retains those boundaries.

The established participant-invite boundary remains the independently
captured singular path in `datasheets/web-group-participant-invite.md`:

```text
comma/newline-separated text
  -> trim and discard empty entries
  -> POST {"action":"add_participants","targets":[...]}
  -> require an active call and at least one target
  -> Call.AddParticipants(app context, targets...)
  -> publish one transient participant_invite SSE event per input
```

## Go envelope (signatures only)

```go
package main

type webCallController struct {
	startGroupCall func(
		context.Context,
		[]string,
		meowcaller.GroupCallOptions,
	) (*meowcaller.Call, error)
	startGroupCallByID func(
		context.Context,
		string,
		meowcaller.GroupCallOptions,
	) (*meowcaller.Call, error)
}

func (c *webCallController) startGroupAudio(targets []string) error
func (c *webCallController) startGroupCallByIDWithVideo(
	groupID string,
	video bool,
) error
func (c *webCallController) addParticipants(targets []string) error
```

The HTTP control envelope adds:

```json
{"action":"start_group_audio","targets":["15551234567","15557654321"]}
{"action":"start_group_id_video","group_id":"120363411251996986@g.us"}
```

## Implementation suggestions (guidance, not authoritative)

- Reuse `participantInviteTargetKey` to trim and deduplicate aliases while
  preserving the first submitted spelling. Reject fewer than two distinct
  people before delegation.
- Pass `GroupCallOptions{GroupJID: ""}` and delegate once. Do not synthesize
  initial group start from `Call` plus `AddParticipants`.
- For a group-ID start, delegate once to `Client.GroupCallByIDWithOptions`; the
  root API owns roster lookup, self removal, alias deduplication, participant
  limits, and canonical group binding.
- Reject group start while any current, pending, or call-ID ownership exists.
  Attach the one returned call before publishing a distinct group-dialing
  lifecycle event. On attach failure, clear ownership and hang up.
- Require `CallPhaseActive` before recording pending participant outcomes or
  delegating `Call.AddParticipants`; keep its per-target outcome behavior
  unchanged after the gate.
- Register `Call.OnGroupState` during attach before invoking `Call.Answer`.
  Roster replay is signaling state, not media readiness, and must not publish a
  ready event by itself.
- Treat a repeated incoming callback for the controller's exact owned call as
  a roster/signaling refresh, never as a competing call to reject. Different
  calls remain subject to the existing busy rejection.
- Keep the group-start button available only in idle UI state. Keep Add people
  disabled until ready or active, and disable it again on ended/idle.
- A video dial, initial video group call, or audio-to-video upgrade starts the
  browser camera before signaling and rolls it back if signaling fails. Camera
  mute/unmute uses video state 0/1. Stop video sends the captured downgrade
  state 6, then stops camera capture; only the separate Hang up control may end
  the call.
- Live WhatsApp initial offer, ACK, group rekey, relay readiness, and
  bidirectional audio remain the end-to-end validation gate.
