# Datasheet: `api/group_call_state`

A sanitized, replayable public view of WhatsApp's authoritative group-call
roster transactions.

**Validation vectors:** focused engine/callback KATs in
`engine_lifecycle_test.go`.

**Reference pinned at:**

- capture SHA-256 `9d6463714430c55ddb3ccb95e153f1d06d11a1feea7a153d1ea95f39f48b6889`
- verified Whatsmeow `types.GroupCallUpdate` parser/state modules

## Reference source (verbatim — authoritative)

The two-sided capture establishes:

```text
group_update is the authoritative transaction-ordered participant snapshot
invited, outgoing, and receipt do not prove that a participant joined
connected plus a selected device PID proves that participant's media endpoint
PID zero is a valid selected endpoint
```

The public event must not expose relay keys/tokens, encrypted key material,
device identity, or raw capability blobs from the signaling snapshot.

## Go envelope

```go
package meowcaller

type GroupCallState struct {
	TransactionID  uint32
	RekeyRequested bool
	Participants   []GroupCallParticipant
}

type GroupCallParticipant struct {
	JID     types.JID
	PN      types.JID
	State   string
	Devices []GroupCallDevice
}

type GroupCallDevice struct {
	JID      types.JID
	Platform string
	PID      uint32
	HasPID   bool
}

func (c *Call) GroupState() (GroupCallState, bool)

func (c *Call) OnGroupState(fn func(GroupCallState))
```

## Required behavior

- Convert only call ID-independent roster metadata: transaction, rekey request,
  participant JID/PN/state, and device JID/platform/PID presence.
- Deep-copy every participant/device slice before storing, returning, or invoking
  user callbacks.
- Ignore stale/equal transactions at the existing engine transaction gate.
- Cache a valid update even when media is not started yet, and notify the call
  immediately.
- Notify only after a live media registry accepts the update when media is
  already active.
- Late `OnGroupState` registration receives the latest state immediately once.
- A nil callback is accepted and does not mark the cached state delivered.
- No callback is fired after call end or for an update rejected by the media
  registry.

## Validation boundaries

- KATs cover update-before-media, update-after-media, stale suppression,
  media-apply failure, deep-copy isolation, late registration, PID zero, and
  ended-call suppression.
- Web presentation/correlation is a dependent module.
