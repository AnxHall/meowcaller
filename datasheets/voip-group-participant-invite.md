# Datasheet: `voip/group_participant_invite`

The active-call control operation that resolves one target, discovers that
target's devices, assembles the existing roster, and sends one singular group
invite offer.

**Validation vector:** `group_participant_invite_corpus.json` — planned
sanitized direct-roster, canonical-roster, capability-capture, and send-boundary
cases copied from the capture boundaries pinned below.

**Status:** scaffolded in whatsmeow commit
`0b82057314aa56fb2cf675f2e9d4a83cfbdda3fd`; all three KATs are skipped while
the capability, roster, and singular send bodies remain explicit stubs.

**Reference pinned at:**

- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- capture SHA-256 `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def`

These captures were approved by the human reviewer as the authoritative source
for group-call work on 2026-07-23. Their hashes were re-verified before writing
the dependent singular-offer datasheet.

## Reference source (verbatim — authoritative)

The immutable raw JSONL lines are the verbatim authority. This datasheet names
the exact boundaries and metadata without copying key, token, ciphertext, or
device-identity bytes.

| Capture | Boundary | Authoritative event |
|---|---|---|
| `diag/captures/group-call-add-people-v2-20260723-112208.jsonl` | line 822 / sequence 10993 | initial outgoing offer advertises the local active-device capability |
| same | line 1346 / sequence 11373 | peer preaccept advertises the selected peer-device capability |
| same | line 1857 / sequence 11719 | peer accept establishes the active direct call before the invite |
| same | line 3177 / sequence 12238 | `inviteToCall(PN, bare LID, four target devices)` |
| same | line 3203 / sequence 12260 | invite offer contains the local and peer active devices/capabilities |
| same | line 3306 / sequence 12325 | one independently-IDed offer is sent to the invitee's bare LID |
| `diag/captures/group-call-multi-add-v2-20260723-135301.jsonl` | lines 2083 and 2105 / sequences 20385 and 20401 | two selected people produce two singular invite operations |
| same | lines 2120 and 2128 / sequences 20413 and 20416 | the two offers independently retain the established two-person roster |

The vector must preserve these observed paths:

```text
active direct call local capability <- initial local offer capability
active direct call peer capability <- selected peer offer/preaccept capability
invite target input -> bare target LID
bare target LID -> only that target's device discovery
call ID -> existing active call ID
call creator -> original call creator device
direct roster -> local active device + selected peer active device
canonical group roster -> latest accepted group_update snapshot
one target -> one BuildGroupInviteOffer call -> one stanza ID -> one send
two targets -> two independent singular operations
```

Observed constraints:

```text
the invite occurs only after the direct call is accepted
the first invite roster needs both established active-device capabilities
the target is absent from group_info and appears only in destination
the client does not optimistically make its submitted roster authoritative
the server group_update becomes the canonical transaction-ordered roster
the target device set is discovered independently for every singular invite
the outer recipient is the target's bare LID
the operation shown in these captures is audio-only
```

Companion encryption-key discovery is visible after offer construction in one
capture. The capture does not prove whether that lookup prepares Signal
sessions, later media rekeying, or another subsystem. This module must not
advance an encrypted session with discarded ciphertext or otherwise guess at
that behavior.

## Go envelope (signatures only)

```go
package whatsmeow

type callState struct {
	// Existing fields remain.
	connected        bool
	inviteSelfDevice types.GroupCallDevice
	invitePeerDevice types.GroupCallDevice
}

func parseCallInviteDevice(
	device types.JID,
	node *waBinary.Node,
) (types.GroupCallDevice, error)

func (cli *Client) capturePeerInviteDevice(
	callID string,
	device types.JID,
	node *waBinary.Node,
) error

func (cli *Client) groupInviteRoster(
	callID string,
) (types.JID, []types.GroupCallParticipant, error)

func (cli *Client) InviteCallParticipant(
	ctx context.Context,
	callID string,
	target types.JID,
) error
```

`InviteCallParticipant` is deliberately singular. A meowcaller or web
convenience layer may invoke it once per selected target and retain one result
per target.

This module owns direct-roster capability capture, active-call gating, target
identity/device discovery, latest-roster selection, stanza ID creation, and
sending. It does not own participant media, group relay application, SFrame
rekeying, optimistic participant state, or a plural result type.

## Implementation suggestions (guidance, not authoritative)

- Clone capability bytes when storing or returning them so inbound node buffers,
  exported capability variables, and concurrent snapshots cannot mutate call
  state.
- Seed the local active device from the exact capability bytes used by the
  existing initial offer/preaccept implementation.
- For outgoing calls, capture the selected peer device and capability from
  `preaccept`; for incoming calls, capture them from the initial `offer`.
- Mark an outgoing call connected on `accept`; mark an incoming call connected
  only after its deferred `accept` send succeeds.
- For the first 1:1-to-group invite, emit two `connected` participants: local
  bare LID with local active device, then peer bare LID with selected peer
  device.
- Once an accepted `group_update` exists, deep-copy its ordered participants
  instead of reconstructing or merging a second roster.
- Reject an unknown or not-yet-connected call, missing active-device
  capability, video call, self target, existing participant, or target with no
  devices.
- Reuse `resolvePeerCallLID` and `GetUserDevices`; preserve the returned target
  device order.
- Pass the roster and resolved target into the verified
  `voip.BuildGroupInviteOffer`, stamp a fresh `GenerateMessageID`, and call the
  existing send path exactly once.
- Do not mutate group state on local submission or send failure. Server
  `group_update` transactions remain authoritative.
- Do not log capabilities, call keys, relay credentials, ciphertext, or
  identity bytes. Log only call ID, resolved target, and device/participant
  counts.
- Do not add speculative prekey/session preparation. Add it later only when a
  capture or protocol source establishes the required state transition.
