# Datasheet: `voip/group_invite_offer`

The singular active-call invite offer sent to one added participant and that
participant's candidate devices.

**Validation vector:** `group_invite_offer_corpus.json` — sanitized wire-shape
cases copied from the capture boundaries pinned below.

**Status:** verified in whatsmeow commit
`f39eba1b31bfa1f197bac8017f2df8a6b88322aa`; both captured wire variants and
all required-field validation cases run and pass.

**Reference pinned at:**

- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- capture SHA-256 `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def`

These captures were approved by the human reviewer as the authoritative source
for group-call work on 2026-07-23. Their hashes were re-verified before writing
this datasheet.

## Reference source (verbatim — authoritative)

The immutable raw JSONL lines are the verbatim authority. This datasheet names
the exact boundaries and fields without copying key, token, ciphertext, or
device-identity bytes.

| Capture | Boundary | Authoritative event |
|---|---|---|
| `diag/captures/group-call-add-people-v2-20260723-112208.jsonl` | line 3177 / sequence 12238 | one `inviteToCall(PN, bare LID, four candidate devices)` invocation |
| same | line 3203 / sequence 12260 | decoded singular offer before network wrapping |
| same | line 3306 / sequence 12325 | outbound `<call>` addressed to the invitee's bare LID |
| same | line 3320 | offer ACK transaction 8 with that invitee `outgoing` |
| `diag/captures/group-call-multi-add-v2-20260723-135301.jsonl` | line 2083 / sequence 20385 | singular invite A with four devices |
| same | line 2105 / sequence 20401 | singular invite B with three devices, 30 ms after A |
| same | line 2123 | invite A's independent outbound offer |
| same | line 2319 | invite B's independent outbound offer |
| same | lines 2202 and 2370 | independent offer ACK transactions 8 and 17 |

The vector must preserve these observed paths:

```text
method == inviteToCall
method.args[0] == invitee phone identity
method.args[1] == invitee bare LID
method.args[2] == only that invitee's candidate devices

call.attrs.to == invitee bare LID
call.attrs.id == independently generated stanza ID
call/offer.attrs.call-id == existing active call ID
call/offer.attrs.call-creator == original call creator device
call/offer/audio.attrs.enc == opus
call/offer/audio.attrs.rate == 16000
call/offer/net.attrs.medium == 2
call/offer/destination/to.attrs.jid == each candidate invitee device
call/offer/group_info/user.attrs.jid == an existing participant
call/offer/group_info/user.attrs.state == optional
call/offer/group_info/user/device.attrs.jid == that participant's active device
call/offer/group_info/user/device/capability.attrs.ver == 1
call/offer/group_info/user/device/capability == opaque 7-byte value
```

Observed structural constraints:

```text
one selected invitee produces one offer
two invitees selected together produce two independent offers
each offer targets only one invitee and only that invitee's devices
the original call ID and call creator are unchanged
the outer recipient is the invitee's bare LID, not CALLID@call
the existing roster contains self and the original connected peer
participant state is present in two offers and absent in one accepted offer
there is no group-jid
there is no privacy, enc, encopt, device-identity, or top-level capability child
the server, not the client offer, merges concurrent invitations canonically
```

The captured target-device order is not the same as the `inviteToCall` argument
order. The corpus proves the device set and singular fan-out, but not a general
sorting algorithm.

The current `voip.BuildOffer` is not this wire form. It emits two audio rates,
network medium 3, privacy/capability/keying fields, and encrypted destinations.

## Go envelope (signatures only)

```go
package voip

type GroupInviteOfferParams struct {
	CallID        string
	To            types.JID
	CallCreator   types.JID
	TargetDevices []types.JID
	Participants []types.GroupCallParticipant
}

func BuildGroupInviteOffer(
	params GroupInviteOfferParams,
) (waBinary.Node, error)
```

`BuildGroupInviteOffer` returns the full `<call><offer>…</offer></call>` node
without a stanza ID. The higher-level client API stamps an independently
generated ID before sending.

Only this low-level, singular, audio invite builder belongs to this module. It
does not resolve PN/LID identities, discover devices, seed the existing roster,
establish Signal sessions, send the node, update optimistic state, or provide a
plural API.

## Implementation suggestions (guidance, not authoritative)

- Emit children in captured order: `audio`, `net`, `destination`, `group_info`.
- Emit exactly one Opus/16000 audio node and network medium 2.
- Address the outer call to `To`; do not route the invite offer to
  `CALLID@call`.
- Preserve `TargetDevices` order. Agent suggestion: the capture does not prove a
  sorting rule, so the builder should not invent one.
- Encode each supplied participant and device in slice order. Copy only the
  captured invite-roster subset: user JID and optional state; device JID and
  optional capability/version. Do not emit PN, PID, platform, or relay fields.
- Treat an empty participant state as absent. Both explicit `connected` and
  absent state were accepted by the server.
- Copy capability bytes without logging them. A sanitized KAT may use
  deterministic seven-byte values while preserving per-device differences.
- Do not add group JID, privacy, encrypted call-key nodes, `encopt`,
  device-identity, or a top-level capability node.
- Do not accept multiple invitees in one builder call. A plural convenience API
  must invoke the singular client operation once per target and preserve
  independent results.
- Approved: use a value parameter and `(Node, error)` result so the exported
  entry point rejects an empty call ID, target, creator, device set, or
  participant roster without panicking or emitting an invalid stanza.
- Approved: reuse `types.GroupCallParticipant` as the input roster while
  encoding only the captured subset. The authoritative group snapshot already
  owns ordered user/device/capability state.
- Approved: preserve caller-supplied target-device order because the captured
  reordering mechanism is not established.
- The later client API needs a separate, reviewed roster-seeding path for the
  first 1:1-to-group upgrade. The direct call currently stores identities but
  not both active device capabilities.
- Visible companion encryption-key discovery occurs before one captured offer,
  despite no `enc` child appearing on the wire. Do not guess at that session
  preparation in this builder; resolve it in the higher-level invite API/media
  keying module with separate evidence.
