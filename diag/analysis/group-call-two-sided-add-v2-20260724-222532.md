# Two-sided WhatsApp add-to-call analysis

## Capture integrity

- Raw capture: `diag/captures/whatsapp-20260724-222532.jsonl`
- Capture schema: `wa-voip-diag/v2`
- Events: 19,285
- Bytes: 26,287,992
- SHA-256: `9d6463714430c55ddb3ccb95e153f1d06d11a1feea7a153d1ea95f39f48b6889`
- WhatsApp Web version: `2.3000.1043800089`
- Captured tabs: 2
- Call ID: `0032CD59A427AD3B9B48F33A71C85FE8`

The collector was stopped before hashing. The raw JSONL was not changed. Capture
line order is not always chronological because both tabs flush concurrently, so
the timeline below is sorted by the events' UTC timestamps.

## Executive conclusion

This capture observes both sides of a successful add-to-call operation: the Web
client that adds a participant and the Web client that is added.

WhatsApp upgrades the existing 1:1 call in place. It does not create another call
or another independent media leg:

- The call ID and relay UUID remain unchanged.
- The server owns a call-scoped `@call` state machine.
- The active participant-device map becomes PID 0, PID 1, and PID 2.
- All clients independently derive the same SSRC bundle for each active device.
- Relay candidates are shared, while relay credentials and `self_pid` are
  personalized for each participant.
- A server `rekey="1"` directive selects one participant to generate and send a
  participant-scoped 32-byte media key to every other connected device.
- RTP keeps each sender's SSRC and sequence space across receivers. This is one
  shared relay media graph, not pairwise RTP sessions.

The strongest implementation gap is outbound group rekeying. The current
whatsmeow scaffold parses incoming `enc_rekey` messages, but its group-update type
does not parse or retain the server's `rekey="1"` directive. It consequently
cannot generate and distribute the requested local key. The current meowcaller
send pipeline also remains keyed from the original call key and has no operation
to install a participant-scoped raw send key. Receive-side participant rekey
support already exists.

## Identities

| Alias | Role | User | Active device | Tab | PID |
|---|---|---|---|---:|---:|
| A | creator and adder | `74170125783269@lid` | `74170125783269:43@lid`, Web | `818525627` | 1 |
| B | original callee | `242653052539031@lid` | bare device, iPhone | not captured | 0 |
| C | added participant | `156535032389744@lid` | `156535032389744:14@lid`, Web | `825906294` | 2 |

A is proven to be the adder by its `inviteToCall` invocation at line 9448, not
merely inferred from `call-creator`.

## Signaling timeline

| Time UTC | Evidence | Observed event |
|---|---:|---|
| 20:27:16.784 | line 6546 | A starts a 1:1 audio call to B, targeting B bare and B`:1`. |
| 20:27:17.737 | line 6729 | A sends the initial offer. |
| 20:27:18.072 | line 6733 | Offer ACK assigns A PID 1, B PID 0, B`:1` PID 2, and relay UUID `6A8PiepgzdOjjRhD`. |
| 20:27:20.267–20.339 | lines 7484, 7750 | B`:1` is dismissed as `accepted_elsewhere`; B's iPhone accepts. |
| 20:27:37.191 | line 9448 | A invokes `inviteToCall` for C with candidate devices bare, `:14`, and `:7`. |
| 20:27:37.214 | line 9473 | A sends the add offer to C's bare LID. Its local `group_info` contains A and B as connected. |
| 20:27:37.469 | raw lines 9533–9534 | Offer ACK transaction 7 lists A/B connected and all three C devices outgoing. |
| 20:27:37.473 | raw lines 9561–9564 | C receives transaction 7 as a call-scoped `group_update` before receiving the directed offer. |
| 20:27:37.482 | line 9572 | C receives the offer from A`:43`; it includes `caller_pn`, `joinable=1`, and transaction 7. |
| 20:27:37.484–37.532 | lines 9575, 9609 | C receipts the offer and sends `preaccept` to `CALLID@call`. |
| 20:27:37.749–37.870 | lines 9774–9776; raw line 9895 | A receives C`:14` and bare-device receipts; transaction 9 advances C to `receipt`. |
| 20:27:42.280 | line 10237 | C sends `accept` to `CALLID@call`. |
| 20:27:42.435–42.478 | lines 10293, 10302, 10309, 10351 | Transaction 14 makes A, B, and C connected as PIDs 1, 0, and 2. Relay transaction becomes 1. Only C's copy has `rekey="1"`. |
| 20:27:42.531–42.736 | lines 10480, 10604, 10979, 11032 | C sends transaction-14 `enc_rekey` separately to A and B; A receives and receipts its copy. |
| 20:27:47.979–47.994 | lines 13640, 13646, 13650, 13656 | Transaction 16 moves B to `invited` without a PID. A and C stay connected; relay transaction becomes 2. Only A's copy has `rekey="1"`. |
| 20:27:48.087–48.229 | lines 14135, 15223, 15322, 15462 | A sends transaction-16 `enc_rekey` to C; C receives and receipts it. |
| 20:27:50.984–51.074 | lines 17101, 17111, 17118, 17168 | A ends the call; transaction 18 reports `group_call_ended` to both tabs. |

The same call ID is used throughout. `CALLID@call` is a signaling address, not a
second call ID.

## Group state and device routing

The outgoing add offer contains three destination devices but only the two
incumbent users in its local roster. The server expands this into transaction 7,
which adds C as `outgoing`. C receives that group update before the direct offer.

C`:14` wins device arbitration:

- C`:14` and C bare receipt the offer; C`:7` does not.
- Transaction 9 retains capability data for C`:14`.
- Transaction 14 assigns C`:14` PID 2.
- Rekeys are routed directly to C`:14`.

PID 2 is reused. It first belongs to B`:1` during the initial multi-device offer,
then belongs to C`:14` after B`:1` is excluded. This capture proves reuse, but not
the server's general PID allocation policy.

The endpoint-local call model remains asymmetric after the upgrade: A's `peerJid`
remains B, while C's `peerJid` is A. The authoritative group roster is therefore
the `group_info` state, not the legacy singular `peerJid`.

## Relay topology

The relay UUID stays `6A8PiepgzdOjjRhD`.

At group relay transaction 1, both captured clients receive the same candidate
pool:

- `zrh1c01` at `157.240.17.133:3478` plus IPv6
- `mxp1c01` at `31.13.86.63:3478` plus IPv6
- `fra3c01` at `57.144.249.57:3478` plus IPv6

Both elect `zrh1c01`, but A receives `self_pid=1` and participant UUID
`rx__CZ9X`, while C receives `self_pid=2` and participant UUID `kUs0gCyw`.
Their relay tokens, relay key, HBH key, and reflexive endpoints differ.

When B leaves, relay transaction 2 preserves the UUID and `zrh1c01` but rotates
the secondary pool to `mxp2c01` and `fra5c02`. Each participant's credential
bundle remains participant-specific.

Therefore:

- shared: relay UUID, relay transaction, server candidate pool;
- participant-specific: `self_pid`, participant UUID, tokens, relay key, HBH key,
  and reflexive address.

## SSRC and RTP evidence

The SSRC bundles are deterministic and reproduced identically on both Web tabs.
The audio slot-0 SSRCs are:

| Participant | Hex | Decimal |
|---|---:|---:|
| A`:43` | `59754A60` | 1,500,858,976 |
| B bare | `63C8EB02` | 1,674,111,746 |
| C`:14` | `66FDE7F1` | 1,727,916,017 |

The captured RTP proves sender identity is preserved end to end:

| Sender → receiver | Evidence | Sequence span | Packets |
|---|---:|---:|---:|
| A → C, worker hook | A send lines 7729–17112; C receive lines 15660–17093 | 299–323 at C, matching A | 25 at C |
| C → A, worker hook | C send lines 15213–17133; A receive lines 15253–17173 | 39–75/76, matching | 37 at A |
| B → A | A receive lines 7745–10281 | 1–168 | 168 |

A's SSRC and sequence space are continuous across the 1:1-to-group transition.
C receives A's existing stream at sequence 299 rather than a new stream starting
at sequence 1. C's own stream uses its independently derived SSRC.

The worker-hook trace is incomplete across stream/channel recreation. A's captured
send sequence jumps from 251 at 20:27:42.473 to 299 at 20:27:48.182, while C's
VoIP stack explicitly reports first RTP from A at 20:27:43.329 (line 12631).
Therefore the missing worker-hook packets and zeroed intermediate call-info
counters are capture-visibility gaps, not proof of silence or absent media.

The audio codec is MLow at 16 kHz mono with 60 ms frames, 25 kbps target bitrate,
VBR, DTX, FEC, and PLC enabled. Payload type 120 is registered as `mlow-1`.
Observed RTCP includes SR, SDES, BYE, PSFB, and WhatsApp extended packet types.

## Rekey and media-stream lifecycle

This capture exposes a particularly useful ordering:

1. C accepts at 20:27:42.280.
2. C is connected as PID 2 in transaction 14 by 20:27:42.435.
3. C's microphone track is live at 20:27:42.274 and its 16 kHz audio contexts are
   running by 20:27:43.216.
4. C sends its transaction-14 participant key to A and B.
5. C's decoder explicitly reports first RTP from A at 20:27:43.329, before
   transaction 16.
6. Transaction 16 nominates A for rekey after B leaves; A sends that key to C at
   20:27:48.087.
7. Both endpoints then tear down and recreate affected encoder/decoder streams.
8. Worker-hook packet visibility resumes at 20:27:48.149: C sends sequence 39,
   A receives it at 20:27:48.182, and C's hook sees A at 20:27:48.209.

Observed fact: transaction-16 rekeying coincides with a media-stream
reconfiguration and the diagnostic worker channel becoming visible again. It
does not mark the start of all group media: C had already received A's RTP about
4.8 seconds earlier.

The capture does not prove whether C's outbound audio was absent before
transaction 16. The first hook-visible C packet is sequence 39, which itself
implies an earlier sender sequence history, but the missing packets were not
captured. Do not use this gap as evidence that key exchange gates audio.

## Comparison with the local implementation

### whatsmeow `rajeh/group-calls`

Already scaffolded:

- parses and caches `group_update`;
- switches call-wide signaling to `CALLID@call`;
- builds and sends the directed group invite offer;
- decrypts incoming participant `enc_rekey` and dispatches its 32-byte raw key.

Missing relative to this capture:

- `group_info rekey="1"` is not represented in `GroupCallUpdate` and is not
  parsed;
- no outbound `enc_rekey` builder/distributor exists;
- no fan-out to every other connected active device exists;
- no local event hands the newly generated send key to meowcaller.

### meowcaller `rajeh/group-calls`

Already scaffolded:

- applies PID-bearing group rosters;
- maintains a receiver per participant device and SSRC;
- applies incoming raw rekeys without disturbing other participants;
- refreshes relay allocation on relay transaction changes;
- mixes multiple participant audio streams.

Missing relative to this capture:

- the outbound media pipeline always derives `sendKeys` from the original call
  key and self LID;
- it has no method to install a new raw participant send key;
- it has no coordinated key/ROC transition for a new membership epoch.

This is a directional mismatch: receive-side group rekeying exists, but the
server-selected participant cannot originate the keying transition required by
the observed protocol.

## Observed facts versus hypotheses

Observed:

- one call ID and one shared relay UUID survive the upgrade;
- group signaling moves to `CALLID@call`;
- C`:14` is selected and becomes PID 2;
- PIDs and SSRC bundles are replicated call-global state;
- relay credentials are participant-specific;
- `rekey="1"` is sent to only one participant per observed transaction;
- that participant immediately sends direct `enc_rekey` messages to the other
  connected devices;
- RTP uses participant-specific SSRCs and retains the sender's sequence space;
- C receives A's RTP before the transaction-16 rekey;
- the later rekey coincides with stream teardown/recreation and a worker-hook
  capture gap;
- both official Web clients eventually exchange audio RTP successfully.

Hypotheses:

- `rekey="1"` is the authoritative instruction to originate the next
  participant-key epoch;
- PID 2 is allocated from a reusable pool rather than tied to a user;
- the final call summary's `connected` labels are historical rather than a live
  roster, because transaction 16 had already moved B out of the active set.

## Evidence-backed next implementation unit

The next bounded unit should be outbound group rekey generation and fan-out in
whatsmeow, together with an explicit media-plane handoff carrying the generated
raw send key to meowcaller. The KAT should cover:

1. only a snapshot with `rekey="1"` triggers generation;
2. one encrypted `enc_rekey` is sent to every other connected active device;
3. the transaction ID matches the group snapshot;
4. the local raw key is surfaced without logging or retaining extra copies;
5. meowcaller installs it for outbound SRTP without changing participant SSRC;
6. duplicate/stale transactions do not rotate the key twice.
