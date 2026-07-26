# WhatsApp “add people” call-flow analysis

## Capture integrity

- Raw capture: `diag/captures/group-call-add-people-v2-20260723-112208.jsonl`
- Events: 8,372
- Bytes: 12,762,523
- SHA-256: `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- Call ID: `0063F48A8B4CA7D1DAF665F1CC8EB545`
- Capture version: v2

The raw JSONL was archived unchanged before this analysis. Line numbers below refer
to that fixed file. Keys, tokens, and encrypted material are deliberately omitted
from this derived report; their equality/rotation relationships are retained.

## Executive conclusion

This is an in-place upgrade of an active 1:1 audio call into an ad-hoc group call.
It is not a new call and it is not tied to a group-chat JID:

- The call ID, original peer, creator, established SSRCs, and elected relay remain
  continuous.
- The initiating client optimistically changes its local model to a three-person
  group as soon as `inviteToCall` returns.
- WhatsApp fans one group offer to all four known devices of the added user.
- Server `group_update` transactions advance the invitee through outgoing,
  receipt, connected, and then departed/invited roster states.
- One invitee device wins. It receives PID 2 and its preallocated SSRC set becomes
  the participant's live media identity.
- The shared group relay allocation is transactioned and rotated as membership
  changes. Per-participant SFrame key state is updated and pruned independently.
- The original 1:1 media identities survive the group graph rebuild.

No `group-jid` occurs in the capture. Any group parser or API that requires one
cannot represent this observed ad-hoc upgrade.

## Identity and topology

Aliases used in this report:

| Alias | Identity | Device evidence |
|---|---|---|
| SELF | `156535032389744@lid` | Web creator device `:14`, later PID 1 |
| PEER | `242653052539031@lid` | Original iPhone participant; master selected, PID 0; companion `:1` cleaned up |
| INVITEE | `74170125783269@lid` | Offer fan-out to master, `:43`, `:44`, `:45`; Android master joins as PID 2 |

The top-level call remains outgoing, `is_caller=true`, and retains PEER as its
`peerJid` after conversion. The group is therefore additional state attached to
the existing call, not a replacement call model.

## Observed timeline

| Time (UTC) | JSONL evidence | Phase | Observed event |
|---|---:|---|---|
| 09:38:30.092 | line 806, seq 10979 | 1:1 start | `startCall` begins an audio call to PEER and its two devices. |
| 09:38:30.291 | line 844, seq 11009 | Offer | Initial 1:1 offer is sent. |
| 09:38:30.658 | line 975, seq 11112 | Relay | Offer ACK supplies three relay choices, PID 0/1 assignments, and joinable state. |
| 09:38:36.961 | line 1835, seq 11709 | Baseline | `is_group_call=false`; SELF and PEER are connected; participant count is 2. |
| 09:38:40.742–46.211 | lines 2325–3155 | Local UI | Add-people panel, picker, search, selection, and confirmation. |
| 09:38:46.548 | line 3177, seq 12238 | Invite | `inviteToCall` receives the invitee PN/LID and master, `:43`, `:44`, `:45` device fan-out. |
| 09:38:46.586 | line 3193, seq 12252 | Optimistic model | Local call becomes a 3-person group; invitee state is 2; server-created remains false. |
| 09:38:46.597 | line 3203, seq 12260 | Group offer | Add offer lists SELF and PEER as connected and targets all four invitee devices. |
| 09:38:46.607 | line 3209, seq 12263 | Key discovery | Companion-device encryption-key discovery begins for `:43`, `:44`, and `:45`. |
| 09:38:47.408 | line 3306, seq 12325 | Send | Group offer is sent to the invitee's bare LID. |
| 09:38:47.662 | raw line 3320 | Transaction 8 | Offer ACK reports invitee `outgoing`, all four devices, and connected limit 32. |
| 09:38:47.776–48.790 | lines 3369–3488 | Receipts | Offer receipts arrive from a companion and the active/bare device. |
| 09:38:48.147 | raw line 3414 | Transaction 10 | `group_update` reports invitee `receipt`; client acknowledges it. |
| 09:38:48.161 | line 3427, seq 12390 | Server commit | Local model becomes server-created; invitee numeric state becomes 3. |
| 09:38:56.997 | raw line 3987; normalized lines 3989/3995 | Transaction 16 | All three participants are connected; invitee's Android master wins and receives PID 2; group relay transaction becomes 1. |
| 09:38:57.163–57.241 | lines 4223–4388 | Media/rekey | Media graph updates, joiner sends `enc_rekey`, two remote participants are subscribed, and three participant keys are updated. |
| 09:39:00.228 | raw line 5603; normalized lines 5605/5613 | Transaction 18 | Invitee returns to wire state `invited`; SELF and PEER remain connected; relay transaction becomes 2. |
| 09:39:00.325 | line 5638, seq 14060 | Departure | `GroupParticipantLeft` is emitted. Invitee remains in the roster but later has numeric state 11 and is no longer invited by self. |
| 09:39:00.325–00.470 | lines 5647–6026 | Rekey/rebuild | PEER sends `enc_rekey`; connected key state and relay peers are reduced to SELF and PEER. |
| 09:39:02.014 | line 7141, seq 15394 | End | CALLID@call sends transaction 20 `terminate`, reason `group_call_ended`. |

The confirm-to-connected interval is about 10.786 seconds. Network-offer-to-connected
is about 9.589 seconds. The invitee is connected for about 3.231 seconds.

## Add-people state machine

```text
active 1:1
  │ local picker + inviteToCall
  ▼
local group, server_created=false
  │ group offer fan-out
  ▼
tx 8: outgoing
  │ device receipts
  ▼
tx 10: receipt, server_created=true
  │ chosen device accepts
  ▼
tx 16: connected, PID 2, relay tx 1, participant rekey
  │ invitee departs
  ▼
tx 18: invitee invited/departed, relay tx 2, remaining-member rekey
  │
  ▼
tx 20: group_call_ended
```

The numeric local participant states correlate with wire states as follows:

- `1`: connected
- `2`: invitation initiated/outgoing
- `3`: receipt
- `11`: departed but retained in the invite roster

Those names are inferred from timing; the numeric values themselves are observed.

## Device routing and offer construction

The add call is not a repeat of the initial 1:1 offer:

1. The UI supplies both the invitee's phone identity and bare LID.
2. Device discovery supplies four destinations: master plus three companions.
3. The generated offer is audio-only at 16 kHz, uses network medium 2, and carries
   a `group_info` roster for the already-connected SELF and PEER.
4. Encryption-key IQs are fetched for the companion devices.
5. The offer is addressed to the bare invitee LID while its destination section
   preserves all device candidates.
6. Receipts can arrive from more than one candidate, but transaction 16 selects
   only the Android master as the connected device.

The earlier `accepted_elsewhere` termination to PEER's `:1` device is consistent
with normal multi-device fan-out cleanup. This interpretation is a hypothesis;
the termination itself is observed at line 1775.

After conversion, group control stanzas route through `CALLID@call`, not through a
single participant JID.

## Relay behavior

The capture contains three related relay allocations:

| Allocation | Membership phase | Relay transaction | Relay UUID | Relay key | HBH key | Tokens/auth | Active relay |
|---|---|---:|---|---|---|---|---|
| Initial ACK | 1:1 | none | unchanged later | initial | initial | initial | zrh |
| Transaction 16 | 3 connected | 1 | same | rotated | unchanged | all rotated | zrh |
| Transaction 18 | invitee departed | 2 | same | unchanged from tx 1 | unchanged | all rotated | zrh |

All three endpoints are rebound at transaction 16 while existing sockets are
reused. At transaction 18 one standby endpoint changes from mxp2 to mxp1 and token
indices are remapped. The elected zrh relay does not change and final diagnostics
report no relay swap.

Observed design implication: group relay state is a shared, atomic,
transaction-ordered allocation. Credentials and standby topology may rotate
without replacing the active transport or resetting RTP identity.

## SSRC and media behavior

Before the add offer is sent, WhatsApp allocates independent media identities for
every candidate device:

- an audio SSRC triple;
- two video stream SSRC triples;
- two screen-share SSRC triples;
- app-data and IMU SSRCs.

The active primary audio SSRCs are:

| Participant | Primary audio SSRC | Continuity |
|---|---:|---|
| SELF | `0x9C68EA26` | Created during 1:1 and reused through group rebuild |
| PEER | `0xE04F5BF5` | Created during 1:1 and reused through group rebuild |
| INVITEE master | `0x23BE6211` | Preallocated at invite time, activated at transaction 16 |

The media graph destroys/recreates stream objects around the critical group update,
but SELF/PEER SSRCs and RTP sequence progression continue. No RTP was observed on
the invitee SSRC and its receive count remains zero. This capture therefore proves
identity allocation and graph membership, but not successful invitee media flow.

Repeated diagnostics report no transmit/receive-stop counters. A packet-recording
gap around the graph rebuild is not enough to claim an audible outage.

The call remains audio-only and unmuted throughout; there is no video upgrade.

## Keying behavior

The evidence distinguishes shared transport keying from participant-specific
media key state:

- The group relay allocation has shared relay/HBH credentials and a shared ordered
  relay transaction.
- Transaction 16 receives `enc_rekey` from the joining participant and updates
  SFrame key state for all three participant records.
- Transaction 18 receives `enc_rekey` from PEER. SELF and PEER retain connected
  chain-key state, while the departed invitee is no longer updated.
- The decoded rekey result is 32 bytes in both the stack path and participant-key
  update path.

Observed conclusion: media key state is participant-indexed and pruned with active
membership. The rule that chooses which participant authors a rekey is not proven
by this single sample.

## Exit semantics

Transaction 18 does not delete the invitee identity. It changes the wire roster
back to `invited`; the local model later maps it to state 11 and clears
`is_invited_by_self`. SELF and PEER remain connected, and the call is still
server-created, joinable, and limited to 32.

A whole-call `group_call_ended` termination arrives about 1.7 seconds later. There
is no captured UI hang-up action after invite confirmation, but this is insufficient
to conclude that invitee departure automatically ended the call. The capture also
does not explain why the invitee departed.

## Independent-analysis comparison

Three agents independently analyzed the same immutable capture:

| Analysis | Primary focus | Independent result |
|---|---|---|
| Signaling | Raw/normalized stanzas and transaction topology | Identified the tx 8 → 10 → 16 → 18 → 20 progression, bare-LID fan-out, CALLID@call routing, and missing `group-jid`. |
| Stack/model | UI, internal API, model and lifecycle events | Identified local optimistic conversion, server-created transition, participant numeric-state progression, and exact invite device list. |
| Media/keying | Relay, SSRC, stream graph, and rekeys | Identified relay credential rotation with transport continuity, per-device SSRC preallocation, stable original SSRCs, and participant-indexed key pruning. |

They agree on the call identity, timing, winning device, PID mapping, membership
progression, relay transactions, and participant-specific rekey behavior. There is
no substantive disagreement. The unresolved points are causal: why the invitee
left, who caused final termination, and the general rekey-author selection rule.

## Code comparison and implementation implications

### whatsmeow

- `calls.go` models a singular peer/relay and exposes only a new 1:1 offer path.
- `voip/group.go` already parses most transaction 16/18 group surfaces, including
  participants/devices, states/PIDs, relay transactions, endpoints, token mapping,
  and group credentials.
- The dispatcher does not route `group_update`, so it becomes an unknown event.
- The current parser requires `group-jid`, but this observed ad-hoc group has none.
- There is no active-call invite API, roster lifecycle event, `enc_rekey` handling,
  group relay update, or group duration/summary surface.

The group offer builder needs its own form: existing-roster `group_info`, destination
device fan-out, group audio/network settings, and companion key discovery. Post-
conversion control must address CALLID@call.

### meowcaller thin rebuild

- `Call`, engine state, and media startup are single-peer.
- Media startup snapshots one direct-call relay and cannot atomically apply ordered
  group relay transactions.
- The RTP receiver/decoder and rekey storage are singular rather than PID-indexed.
- There is no participant roster, participant join/leave callback, or dynamic
  stream add/remove path.
- SFrame exists only as a pairwise self/peer surface and is not wired into the
  media pipeline.

Evidence-backed architecture needs:

1. transaction-ordered group snapshots;
2. active-call invite with device fan-out;
3. PID/device-indexed roster and receive streams;
4. participant-indexed SFrame key state;
5. atomic relay-set/credential updates that preserve RTP/SSRC continuity;
6. critical-update media graph rebuilds for joins and departures.

The capture does not establish the public Go API shape, retry policy, concurrency
contract, timeout semantics, or general behavior for rejected invitations.

Separate existing audit finding: `engine_media.go` currently emits raw relay
credential material around line 103 and the raw call key around line 218. That is
unrelated to the captured protocol behavior, but it violates this repository's
no-secrets-in-diagnostics rule and should be removed before production use.

## Observed facts versus hypotheses

### Observed

- One call ID is used before, during, and after group conversion.
- There is no group-chat JID.
- SELF initiates the add flow.
- The offer is fanned to four invitee devices.
- The Android master becomes connected as PID 2.
- Server transactions 8, 10, 16, 18, and 20 order the flow.
- Original SSRCs survive the graph rebuild.
- Group relay credentials rotate while the active relay remains stable.
- Participant key state changes at join and departure.
- The invitee remains in the roster after leaving.

### Hypotheses requiring more captures

- `accepted_elsewhere` is solely fan-out cleanup.
- Server transaction-number gaps are ordinary version progression.
- The joining participant normally authors the admission rekey.
- A remaining participant normally authors the departure rekey.
- Invitee departure caused the later whole-call termination.
- Local numeric state names map exactly to the inferred labels above.

## Highest-value follow-up captures

1. Invitee explicitly rejects.
2. Invitee never answers and times out.
3. A companion device, rather than the master, accepts.
4. Two people are added sequentially to the same call.
5. An added participant leaves while the original two keep talking.
6. Audio group upgrades to video, then downgrades.
7. A true group-chat call, to contrast `group-jid` behavior with this ad-hoc flow.
