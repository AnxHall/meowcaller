# Datasheet: `web/group_participant_invite`

The localhost browser-console control that submits one or more selected people
to an established call and reports each singular invite result independently.

**Validation vector:** focused Go request/controller KATs plus static page
behavior checks.

**Status:** planned.

**Reference pinned at:**

- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- capture SHA-256 `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def`
- meowcaller API commit `b376a0093b034ec2eab3022790431f02208c1a99`

The captures were approved by the human reviewer as authoritative on
2026-07-23. The meowcaller pin provides the verified ordered plural convenience
method over singular Whatsmeow signaling.

## Reference source (verbatim — authoritative)

| Capture | Boundary | Authoritative event |
|---|---|---|
| `diag/captures/group-call-add-people-v2-20260723-112208.jsonl` | line 3177 / sequence 12238 | one selected person invokes one singular invite |
| `diag/captures/group-call-multi-add-v2-20260723-135301.jsonl` | lines 2083 and 2105 / sequences 20385 and 20401 | two selected people invoke two independent singular invites |

The browser-console boundary is:

```text
comma/newline-separated text
  -> trim and discard empty entries
  -> POST {"action":"add_participants","targets":[...]}
  -> require an active call and at least one target
  -> Call.AddParticipants(app context, targets...)
  -> publish one transient participant_invite SSE event per input
  -> HTTP 204 after all target attempts
```

Each result event has this shape:

```json
{
  "event": "participant_invite",
  "call_id": "CID",
  "target": "15551234567",
  "success": false,
  "message": "meowcaller: add participant: ..."
}
```

The `success` field must be present for both true and false results. A successful
event means only that the singular invite stanza was submitted; it does not
claim that the participant joined.

## Go envelope (signatures only)

```go
package main

type vbControl struct {
	// Existing fields remain.
	Targets []string `json:"targets,omitempty"`
}

type webParticipantInviteResult struct {
	Event   string `json:"event"`
	CallID  string `json:"call_id"`
	Target  string `json:"target"`
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

func (c *webCallController) addParticipants(targets []string) error
```

The controller may carry an unexported injectable plural function so its
request/result behavior is testable without constructing live media or a
WhatsApp socket.

## Implementation suggestions (guidance, not authoritative)

- Keep the public HTTP action name `add_participants` and the JSON field
  `targets`.
- Reject request-level blockers such as no active call or no non-empty targets
  with the existing HTTP 409 control contract.
- Once attempts begin, publish every target result and return success from the
  control handler even when individual invitations fail.
- Use `PublishEvent`, not `PublishState`, so transient invite results do not
  replace the lifecycle state replayed to a new SSE subscriber.
- Prevent `participant_invite` events from replacing the header's current call
  lifecycle label; keep them in the event log.
- Label the UI and README so “submitted” is not confused with “joined.”
- Do not add roster rendering, optimistic membership, group media, or rekeying.
