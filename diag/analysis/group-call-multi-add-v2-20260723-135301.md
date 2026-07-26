# WhatsApp simultaneous multi-person add analysis

## Capture integrity

- Raw capture: `diag/captures/group-call-multi-add-v2-20260723-135301.jsonl`
- Events: 7,192
- Bytes: 9,572,198
- SHA-256: `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def`
- Capture version: v2
- Call ID: `0013AE8E463F60BFE6A1AF5247556691`

The collector was stopped before hashing and the raw JSONL was archived unchanged.
This report omits secret key/token contents while preserving lengths, hashes, and
equality/rotation relationships.

## Executive conclusion

Selecting two people in one WhatsApp “Add people” action produces two independent,
singular participant invitations. It does not produce one offer containing both
new users.

- The UI requests both additions at the same time.
- WhatsApp invokes `inviteToCall` once per user, 30 ms apart.
- Each call has its own phone identity, bare LID, device fan-out, `<offer>`, stanza
  ID, offer ACK, and server transaction progression.
- The server merges both independent invitation tracks into one canonical roster.
- One invitee connects and sends media. The other remains in receipt state until
  the local caller hangs up.

This confirms that the signaling primitive should be singular. A plural public API
or web control can be a convenience wrapper, but it must issue one participant
offer per target.

As in the earlier single-add capture, this is an in-place 1:1-to-ad-hoc-group
upgrade. The call ID and relay UUID remain stable and no `group-jid` is present.

## Participants

| Alias | LID | Candidate devices | Outcome |
|---|---|---|---|
| SELF | `156535032389744@lid` | Web creator `:14` | Connected, PID 1 |
| PEER | `242653052539031@lid` | master and `:1` | Original participant connected as iPhone, PID 0 |
| INVITEE A | `74170125783269@lid` | master, `:43`, `:44`, `:45` | Android master connects as PID 2 |
| INVITEE B | `9908623781998@lid` | master, `:63`, `:64` | Reaches receipt state but does not connect |

## Observed timeline

| Time (UTC) | JSONL evidence | Observed event |
|---|---:|---|
| 11:54:57.175 | line 114, seq 19033 | Initial 1:1 `startCall` to PEER. |
| 11:55:00.709 | line 1105, seq 19760 | Active 1:1 snapshot: two connected participants, not a group. |
| 11:55:03.753 | lines 1496/1499 | Add-people action opens `voip-group-call-picker`. |
| 11:55:14.473 | line 2050, seq 20367 | User confirms the picker selection. |
| 11:55:14.486 | lines 2056/2057 | UI layer logs two invite requests in the same millisecond. |
| 11:55:14.787 | line 2083, seq 20385 | `inviteToCall` for INVITEE A with four candidate devices. |
| 11:55:14.815 | lines 2100/2104 | Local optimistic group has 3 participants; A is numeric state 2. |
| 11:55:14.817 | line 2105, seq 20401 | `inviteToCall` for INVITEE B with three candidate devices. |
| 11:55:14.831 | line 2117, seq 20412 | Local optimistic group has 4 participants; A and B are state 2. |
| 11:55:14.833–14.837 | lines 2119/2123 | A gets a dedicated offer addressed to A's bare LID. |
| 11:55:14.837 | line 2127 | B gets a separate internal offer; companion key discovery follows. |
| 11:55:15.152 | line 2202 | A offer ACK, transaction 8: A outgoing; SELF/PEER connected. |
| 11:55:15.526 | lines 2305/2312 | Transaction 10: A receipt. |
| 11:55:15.532 | line 2319 | B's separate offer is sent after its key lookup. |
| 11:55:15.836 | line 2370 | B offer ACK, transaction 17: B outgoing, A receipt. |
| 11:55:16.311 | lines 2440/2442 | Transaction 19: both A and B receipt. |
| 11:55:20.777 | lines 2788/2790 | Transaction 23: A connected as Android/PID 2; B remains receipt; group relay transaction 1. |
| 11:55:20.866–21.682 | lines 2799–3120 | A sends `enc_rekey`; media graph adds A; connected participant keys update. |
| 11:55:21.682 | lines 3175/3183 | Call model shows A connected and receiving audio; B remains receipt. |
| 11:55:36.203 | lines 5689/5690 | Local user calls `endCall(2, true)`. |
| 11:55:36.206 | lines 5703/5705 | Client sends group terminate through `CALLID@call`. |
| 11:55:36.376 | line 6013 | Transaction 25: SELF becomes invited/departed; A and PEER still connected; B still receipt. |
| 11:55:38.132 | line 6956 | Transaction 26: SELF and PEER departed; A remains connected; B remains receipt. |
| 11:55:39.570 | lines 7042/7048 | Server transaction 27 terminates the group call. |

Confirm-to-A-connected is approximately 6.304 seconds. INVITEE B remains pending
for the roughly 21.7 seconds between confirmation and local hang-up.

## Singular invitations inside one batch UI action

The exact stack calls are:

```text
inviteToCall(
  A phone identity,
  A bare LID,
  [A:43, A:44, A:45, A master]
)

inviteToCall(
  B phone identity,
  B bare LID,
  [B:63, B:64, B master]
)
```

The calls return immediately and are 30 ms apart. Their network behavior remains
independent:

| Invitee | Offer recipient | Device count | Offer ACK transaction | Receipt transaction |
|---|---|---:|---:|---:|
| A | A bare LID | 4 | 8 | 10 |
| B | B bare LID | 3 | 17 | 19 |

The server's B offer ACK already includes A in receipt state, even though B's
client-generated offer does not list A. Transaction 19 then publishes the merged
four-user roster with both invitees in receipt state.

Observed conclusion: the server owns canonical merge/versioning. A client should
not construct one combined destination stanza for multiple new users.

## Offer construction details

Both add offers are audio-only and contain:

- `<audio enc="opus" rate="16000"/>`;
- `<net medium="2"/>`;
- a destination list containing only that invitee's devices;
- a `group_info` containing SELF and the original PEER.

A's offer marks SELF and PEER `connected`. B's offer, generated 4 ms later, lists
the same two users but omits their `state` attributes. The server accepts both and
returns a complete canonical roster.

The reason for the state-attribute difference is not established. It may be a
local optimistic-update race or a permitted shorthand. It should not be turned
into a builder rule without another capture.

Only B required a visible key-discovery IQ in this sample, which delayed its
network offer by about 695 ms. A's stored device keys allowed immediate sending.
Device-key discovery is therefore per invitee and must not block already-ready
invitations.

## Participant-state progression

| Participant | Local optimistic | ACK | Receipt update | Critical update |
|---|---|---|---|---|
| SELF | connected | connected | connected | connected, PID 1 |
| PEER | connected | connected | connected | connected, PID 0 |
| A | numeric 2 | outgoing, tx 8 | receipt, tx 10 | connected, tx 23, PID 2 |
| B | numeric 2 | outgoing, tx 17 | receipt, tx 19 | remains receipt in tx 23 |

Local snapshots corroborate numeric mappings:

- state 1: connected;
- state 2: locally invited/outgoing;
- state 3: receipt.

The top-level call model remains the same outgoing call with PEER as `peerJid`.
It does not emit a new call-model object for either invite.

## Relay behavior

Initial 1:1 and group relay allocations share relay UUID
`4zU_x0oNjDRf-IHS`.

| Property | Initial 1:1 | Group transaction 1 |
|---|---|---|
| Peers represented | PEER | PEER and connected A |
| Relay key | initial digest | rotated |
| HBH key | digest X | same digest X |
| Tokens | three × 191 bytes | all rotated, three × 174 bytes |
| Auth token | 70 bytes | rotated, 70 bytes |
| Active relay | zrh1 | remains zrh1 |
| Standby set | mxp1, fra3 | mxp1, fra5 |

The transport logs `num_peers: 2`, switches to group-call relay mode, and applies
relay transaction 1. Pending B is not counted as a media peer.

## SSRC and media behavior

WhatsApp preallocates a complete SSRC family for every candidate device of both
invitees before either server ACK:

- A master primary audio SSRC: `0x913F26B0`;
- B master primary audio SSRC: `0x7E984B20`;
- additional independent audio/video/screen-share/app-data/IMU identities for
  A's `:43/:44/:45` and B's `:63/:64`.

At transaction 23 only A's master identity is activated:

- A receives PID 2;
- the A audio stream uses preallocated SSRC `0x913F26B0`;
- the media graph subscribes SELF to PEER/PID 0 and A/PID 2;
- `getCallInfo` advances A's received-audio count from 0 to 8;
- WARP receiver reports packets for both PEER and A.

This capture improves on the first add-person sample: it proves actual media flow
from an added participant, not merely SSRC allocation and graph creation.

B's SSRCs remain reserved but inactive because B never becomes connected.

## Rekey behavior

One incoming `enc_rekey` is observed:

- sender: connected INVITEE A;
- group transaction: 23;
- key generation: 2;
- decoded key length: 32 bytes.

The stack reports processing four participant records, matching the canonical
roster, but emits successful SFrame key updates only for the three connected
devices: SELF, PEER, and A. No key update occurs for receipt-state B.

Observed conclusion: the roster can include pending invitees while active
participant key state remains limited to connected membership.

## Termination behavior

This call is explicitly ended by the local user. The captured `endCall(2, true)`
precedes the outbound group terminate. Subsequent transactions remove SELF and
PEER while A briefly remains connected and B remains receipt, then the server
emits `group_call_ended`.

Unlike the earlier single-add sample, there is no ambiguity here about the cause
of the whole-call termination.

## Implementation implications

Evidence now supports this separation:

1. **Signaling primitive:** invite exactly one participant, with that
   participant's PN/LID/device fan-out.
2. **Convenience API:** accept multiple targets and initiate one singular invite
   for each, preserving independent results/state.
3. **Web example:** allow selecting/entering multiple targets, then display
   per-target outgoing/receipt/connected status rather than one batch status.
4. **Roster:** merge server snapshots by transaction ID; do not infer canonical
   membership from locally submitted offers.
5. **Media/keying:** allocate candidate-device identities early, but activate
   streams and SFrame state only for connected devices with PIDs.
6. **Relay:** update the shared relay allocation atomically from critical group
   updates; exclude pending invitees from media peer count.

A suitable surface is a singular backend method plus an optional plural wrapper:

```go
func (c *Call) AddParticipant(ctx context.Context, target string) error
func (c *Call) AddParticipants(ctx context.Context, targets ...string) []error
```

The exact plural error/result type remains a human API decision. A single `error`
would lose the observed independent outcome of each invitation.

## Observed facts versus hypotheses

### Observed

- One picker confirmation triggers two separate `inviteToCall` calls.
- Each invitee receives a separate offer and independent transaction sequence.
- The server merges those sequences into one four-user roster.
- Only one invitee connects; the other stays in receipt state.
- Only the connected invitee gains a PID, active SSRC stream, and SFrame update.
- Added-participant RTP is received on its preallocated SSRC.
- Local hang-up causes the final group termination.
- No `group-jid` is present.

### Hypotheses requiring another sample

- Missing state attributes in B's offer are caused by the near-simultaneous local
  optimistic update.
- Invitations are deliberately processed concurrently rather than merely queued
  rapidly.
- A batch with both invitees answering would produce one combined critical update
  rather than two successive connected updates.
- Transaction-number gaps have no semantics beyond canonical server versioning.

## Highest-value next capture

Repeat the two-person picker action with both invitees answering. That would show
whether membership commits and rekeys are:

- one critical update containing two newly connected PIDs; or
- two ordered critical updates, each adding one participant and rotating keys.
