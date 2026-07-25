# Datasheet: `web/group_call_outcomes`

Authoritative group roster and joined-participant outcomes in the local web call
test console.

**Validation vectors:** focused controller/page KATs in
`examples/web/controller_test.go` and `examples/web/server_test.go`.

**Reference pinned at:**

- capture SHA-256 `9d6463714430c55ddb3ccb95e153f1d06d11a1feea7a153d1ea95f39f48b6889`
- verified `api/group_call_state` commit `25526bc`

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
- Remove failed submissions from pending correlation.
- Publish `participant_join` only when state is `connected` and at least one
  device has `HasPID`; preserve PID zero.
- Publish at most one join outcome per submitted target.
- Do not claim `invited`, `outgoing`, or `receipt` as joined.
- Group/invite/join transients must not replace the lifecycle replay state or
  header.
- Render the latest roster transaction and connected count in the page, and
  replace “submitted” guidance with explicit “waiting for roster confirmation.”

## Validation boundaries

- KATs cover target normalization, LID and PN matching, PID zero, intermediate
  states, failed invite removal, one-shot joins, generic state serialization,
  lifecycle replay protection, and page presentation.
- Live WhatsApp behavior remains the final end-to-end gate.
