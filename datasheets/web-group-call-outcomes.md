# Datasheet: `web/group_call_outcomes`

Authoritative group roster and joined-participant outcomes in the local web call
test console.

**Validation vectors:** focused controller/page KATs in
`examples/web/controller_test.go` and `examples/web/server_test.go`.

**Reference pinned at:**

- capture SHA-256 `9d6463714430c55ddb3ccb95e153f1d06d11a1feea7a153d1ea95f39f48b6889`
- verified `api/group_call_state` commit
  `25526bc64f1427f3c24ebd151e16237677fb7328`

## Reference source (verbatim — authoritative)

```text
participant_invite success means the directed offer was submitted
invited, outgoing, and receipt are intermediate signaling states
connected plus a PID-bearing selected device is the authoritative join outcome
PID zero is valid
```

## Web envelope

```go
type webGroupCallState struct {
	Event          string
	CallID         string
	TransactionID  uint32
	RekeyRequested bool
	Participants   []webGroupCallParticipant
}

type webParticipantJoin struct {
	Event         string
	CallID        string
	TransactionID uint32
	Target        string
	Participant   string
	Device        string
	PID           uint32
}
```

The controller retains successful submitted targets until an authoritative state
connects the corresponding user. Matching uses the target's normalized user part
against participant LID user or PN user; the generic roster event remains visible
when WhatsApp omits a PN and an entered phone number cannot be correlated to a
different LID.

## Required behavior

- Register `Call.OnGroupState` for every dialed or accepted call.
- Publish every accepted transaction as a transient `group_state` event with
  only the sanitized public roster.
- Track invite targets before signaling starts so a fast group update cannot
  race ahead of correlation.
- While the per-target signaling result is still in flight, stage a matching
  connected PID-bearing roster outcome instead of publishing it.
- Publish the `participant_invite` result first. On success, publish any staged
  join afterward; on failure, discard the staged join and remove the target from
  pending correlation.
- Serialize overlapping web invite submissions. Before publishing or applying a
  returned result, revalidate the exact `Call` pointer and call-scoped pending
  state so an old call cannot publish into or consume a replacement call.
- Publish `participant_join` only when state is `connected` and at least one
  device has `HasPID`; preserve PID zero.
- Publish at most one join outcome per submitted target.
- Scope roster callbacks and pending invite correlation to the exact active call
  ID so an old in-flight callback cannot consume a later call's target.
- Deduplicate targets by normalized user part before signaling. If one
  participant matches both pending PN and LID aliases, consume both aliases but
  publish one join.
- Do not claim `invited`, `outgoing`, or `receipt` as joined.
- Group/invite/join transients must not replace the lifecycle replay state or
  header.
- Cache the latest authoritative roster independently and replay lifecycle first,
  roster second on SSE reconnect; clear it when that exact call ends.
- Revalidate call ownership immediately before roster publication and make that
  publication atomic with lifecycle cleanup, preventing an old callback from
  repopulating the replay cache after call end.
- Reject and release an incoming call when media attachment or acceptance fails
  so the console does not remain busy.
- Render the latest roster transaction and connected count in the page, and
  replace “submitted” guidance with explicit “waiting for roster confirmation.”

## Validation boundaries

- KATs cover target normalization/deduplication, LID and PN alias matching, PID
  zero, intermediate states, synchronous roster/result ordering, failed invite
  removal without a false join, serialized duplicate submissions, old-call
  result suppression, one-shot joins, stale-callback publication suppression,
  failed-answer cleanup, generic state serialization,
  lifecycle/roster reconnect replay, and page presentation.
- Live WhatsApp behavior remains the final end-to-end gate.
