# Datasheet: `api/group_participant_invite`

The public meowcaller adapter that adds one selected person to an established
call by delegating signaling to Whatsmeow.

**Validation vector:** focused Go KATs for target parsing, exact call-ID/JID
delegation, unavailable signaling, and wrapped downstream failure.

**Status:** partial; the engine adapter and singular public method KATs pass,
while the plural method KAT remains skipped on its explicit stub.

**Reference pinned at:**

- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- capture SHA-256 `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def`
- whatsmeow commit `7dc1db147f07af4a7b8878a4823e516386547164`

The captures were approved by the human reviewer as authoritative on
2026-07-23. The Whatsmeow pin contains the singular active-call operation and
its accepted-call lifecycle gate.

## Reference source (verbatim — authoritative)

The immutable raw JSONL lines are the capture authority. This adapter preserves
the following established boundaries without copying secret or identity bytes:

| Capture | Boundary | Authoritative event |
|---|---|---|
| `diag/captures/group-call-add-people-v2-20260723-112208.jsonl` | line 3177 / sequence 12238 | one picker target invokes one singular `inviteToCall` operation |
| same | line 3306 / sequence 12325 | that operation sends one independently-IDed offer to the target's bare LID |
| `diag/captures/group-call-multi-add-v2-20260723-135301.jsonl` | lines 2083 and 2105 / sequences 20385 and 20401 | two selected people invoke two independent singular operations |

The adapter must preserve this flow:

```text
Call.AddParticipant(ctx, target text)
  -> existing call-target parser
  -> one Whatsmeow InviteCallParticipant(ctx, call ID, parsed JID)
  -> return that target's result

Call.AddParticipants(ctx, target texts...)
  -> repeat AddParticipant sequentially once per input
  -> retain one index-aligned error per input

web multi-select -> Call.AddParticipants -> one result event per input
```

Observed and inherited constraints:

```text
one selected person -> one singular invite operation
two selected people -> two independent singular invite operations
the existing call ID is reused
Whatsmeow owns active-call validation, device discovery, offer construction,
stanza IDs, sending, and server-authoritative membership
```

## Go envelope (signatures only)

```go
package meowcaller

type engine struct {
	// Existing fields remain.
	inviteCallParticipant func(context.Context, string, types.JID) error
}

func (e *engine) inviteParticipant(
	ctx context.Context,
	callID string,
	target string,
) error

func (c *Call) AddParticipant(
	ctx context.Context,
	target string,
) error

func (c *Call) AddParticipants(
	ctx context.Context,
	targets ...string,
) []error
```

Signaling remains singular. The plural method is a thin ordered convenience
loop that lets the web example report each target independently and continue
after one target fails.

## Implementation suggestions (guidance, not authoritative)

- Initialize the injected signaling function from
  `whatsmeow.Client.InviteCallParticipant`.
- Reuse `parseCallTarget` so phone numbers, LIDs, whitespace, and parse errors
  behave consistently with `Client.Call`.
- Return a clear unavailable-signaling error when the injected function is nil.
- Wrap the Whatsmeow error with the meowcaller operation name while preserving
  it for `errors.Is`.
- Pass the caller's context through unchanged.
- Implement the plural helper as an input-order loop over the singular method;
  invoke every input and return an index-aligned error slice.
- Do not add participant state, media routing, rekeying, device discovery, or a
  plural signaling primitive to meowcaller.
