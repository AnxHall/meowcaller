# Outgoing WhatsApp group-call capture: timeline and inventory

## Scope and reproducibility

This report analyzes the archived WhatsApp Web capture
[`group-call-outgoing-20260723-100408.jsonl`](../captures/group-call-outgoing-20260723-100408.jsonl).
It is an observation report, not an implementation specification. Observed facts
and interpretations are kept separate below.

The raw capture was hashed before processing and was not modified:

| Property | Value |
|---|---|
| Schema | `meowcaller-wa-diagnostics/v1` |
| Capture session | `a853331b-5045-4d2a-9c44-5d1e68ea5d54` |
| Events / lines | 10,472 |
| Bytes | 3,938,672 |
| SHA-256 | `f126974bcdbbb7f6325e17f0acc3b8eea4119a48d17bbf9c0691c3228df81936` |
| First event | sequence 1, `2026-07-23T08:03:56.617Z` |
| Last event | sequence 10,472, `2026-07-23T08:06:17.382Z` |
| Span | 140.765 seconds |
| Sequence gaps | none |

Code comparison points:

| Repository | Branch | Commit |
|---|---|---|
| meowcaller | `codex/rebuild-thin-media` | `bbaf912ef89748fe2ed5442c84cc9758d252506e` |
| whatsmeow | `codex/refresh-voip-call-api` | `edfef3172122795d517177a5762eb4157cd6ec82` |

The extension hooks `WAWap.encodeStanza` and `WAWap.decodeStanza`. These stages
are serialization boundaries, not reliable network-direction labels by
themselves. For example, an inbound network `<call><video/></call>` is decoded,
then its bare `<video/>` is encoded for the VoIP stack. Direction claims below
therefore use the outer stanza, addresses, correlation IDs, and stack logs
together.

The hook selects an event if any nested tag looks call-related. The initial
`media_conn` IQ at sequence 6 is consequently present because it contains nested
`video` tags; it is ordinary media-upload configuration, not group-call relay
allocation.

## Event inventory

| Source / event | Count | Coverage |
|---|---:|---|
| `wa-logger/log` | 10,184 | Internal VoIP/WASM state, SSRCs, relay election, packet counters, codecs, key-slot updates |
| `wawap/signaling` | 167 | Decoded/encoded call-related node trees |
| `voip-stack/call` | 42 | Calls into the WhatsApp Web VoIP interface |
| `voip-stack/result` | 42 | Corresponding successful results; no stack error event |
| `call-model/state` | 13 | Changes to the active UI call model |
| `media-track/observed` | 11 | Newly acquired audio/video tracks |
| `media/getUserMedia` | 9 | Browser capture constraints |
| `capture/ready` | 4 | Page, WAWap, VoIP-stack, and logger hooks |

The 84 VoIP-stack events are 42 call/result pairs:

| Method | Call/result pairs |
|---|---:|
| `getCallInfo` | 25 |
| `setCallVideoMute` | 4 |
| `startVideoPreview` | 4 |
| `consumeVideoCaptureFps` | 2 |
| `endCall` | 2 |
| `videoStreamResume` | 2 |
| `startGroupCall` | 1 |
| `joinOngoingCall` | 1 |
| `acceptCall` | 1 |

Top-level WAWap nodes:

| Stage | Top-level tags and counts |
|---|---|
| decode (79) | `call` 39, `ack` 7, `relaylatency` 6, `video` 6, `receipt` 5, `mute_v2` 4, `connect_stat` 2, `enc_rekey` 2, `terminate` 2, `transport` 2, and one each of `accept`, `iq`, `notification`, `offer` |
| encode (88) | `call` 37, `relaylatency` 9, `video` 9, `ack` 7, `mute_v2` 7, `group_update` 5, `receipt` 5, `iq` 3, `transport` 3, `enc_rekey` 2, `terminate` 1 |

No structured reaction, screen-share activation, app-data send, participant
removal, or nonzero mute-state event appears. App-data and screen-share SSRCs
are precomputed, but their generation does not prove use.

## Entity inventory

| Role | Phone-number JID | LID / selected device | Platform | PID |
|---|---|---|---|---:|
| SELF / call creator | `96179377559@s.whatsapp.net` | `156535032389744:14@lid` | web | 0 |
| Participant A | `96170600887@s.whatsapp.net` | `242653052539031:0@lid` | iPhone | 1 |
| Participant B | `96176891546@s.whatsapp.net` | `74170125783269:0@lid` | Android | 2 |

Other offered devices were A device 1 (`macos`) and B devices 43, 44, and 45
(`web`). They received candidate SSRC allocations, but no group update assigned
them a PID and no evidence shows them becoming active.

| Identifier | Value / behavior |
|---|---|
| Logical call ID | `00954571727022506957B88D578C9487` |
| Group-wide call route | `00954571727022506957B88D578C9487@call` |
| Group JID | `120363411251996986@g.us` |
| Call creator | `156535032389744:14@lid`, constant across both local legs |
| Initial UI/model peer | `242653052539031@lid`; this is representative, not the complete roster |
| Relay call UUID | `yDWFn3J_xA_Lv4Hz`, constant across both local legs |
| Initial self participant UUID | `anGBIwJc` |
| Rejoin self participant UUID | `xTItIExM` |
| Connected limit | 32 |
| `global_call_id` | Empty in all inspected `getCallInfo` snapshots |

The stable call ID, group JID, creator, relay UUID, PIDs, and SSRCs identify one
logical group call. The changed self participant UUID and rotated relay/HBH
keys identify a new local media leg after rejoin.

## Indexed timeline

Times are UTC. Elapsed time is measured from the `startGroupCall` invocation at
sequence 151.

| Elapsed | Sequence / time | Observed event |
|---:|---|---|
| -1.078 s | [134](../captures/group-call-outgoing-20260723-100408.jsonl#L134), `08:05:06.773` | UI logs “Placing Group call”; microphone permission/capture begins. |
| 0.000 s | [151](../captures/group-call-outgoing-20260723-100408.jsonl#L151), `08:05:07.851` | `startGroupCall` for A and B, audio only. |
| 0.124 s | [154](../captures/group-call-outgoing-20260723-100408.jsonl#L154) / [156](../captures/group-call-outgoing-20260723-100408.jsonl#L156), `08:05:07.975–.976` | Stack-generated offer is wrapped for `call-id@call`. It advertises Opus 8 kHz and 16 kHz and enumerates SELF/A/B candidate devices. |
| 0.268 s | [289](../captures/group-call-outgoing-20260723-100408.jsonl#L289), `08:05:08.119` | UI call model state 1; logs name it `Calling`. |
| 0.697 s | [290](../captures/group-call-outgoing-20260723-100408.jsonl#L290), `08:05:08.548` | Offer ack, group transaction 8: SELF `connected`, A/B `outgoing`, audio, joinable, limit 32. Preliminary relay transaction 0 is supplied. |
| 1.669 s | [371](../captures/group-call-outgoing-20260723-100408.jsonl#L371), `08:05:09.520` | Offer receipt from A device 0. Group transaction 10 changes A to `receipt`. |
| 1.828 s | [399](../captures/group-call-outgoing-20260723-100408.jsonl#L399), `08:05:09.679` | Offer receipt from B device 0. Group transaction 12 changes B to `receipt`. |
| 1.994 s | [408](../captures/group-call-outgoing-20260723-100408.jsonl#L408), `08:05:09.845` | Additional receipt from A device 1. B web devices do not produce receipts. |
| 3.536 s | [483](../captures/group-call-outgoing-20260723-100408.jsonl#L483), `08:05:11.387` | Group transaction 14: A device 0 becomes `connected`, PID 1; relay transaction 1 supplies three European relay candidates. |
| 3.599 s | [517](../captures/group-call-outgoing-20260723-100408.jsonl#L517), `08:05:11.450` | Participant A sends its encrypted rekey. It is decrypted and delivered to the stack as a distinct 32-byte key at sequence 546. |
| 3.773 s | [600](../captures/group-call-outgoing-20260723-100408.jsonl#L600), `08:05:11.624` | UI model state 6; logs name it `CallActive`. |
| 3.857 s | [701](../captures/group-call-outgoing-20260723-100408.jsonl#L701), `08:05:11.708` | Relay 0, zrh1, is elected. |
| 4.292 s | [896](../captures/group-call-outgoing-20260723-100408.jsonl#L896), `08:05:12.143` | First RTP packet from A’s audio SSRC is logged. |
| 12.013 s | [1088](../captures/group-call-outgoing-20260723-100408.jsonl#L1088), `08:05:19.864` | Group transaction 18: B device 0 becomes `connected`, PID 2; all three participants are connected. |
| 12.128 s | [1303](../captures/group-call-outgoing-20260723-100408.jsonl#L1303), `08:05:19.979` | `connect_stat` reports `rtp_traffic_started=1`. |
| 12.961 s | [2244](../captures/group-call-outgoing-20260723-100408.jsonl#L2244), `08:05:20.812` | Participant B sends a different encrypted rekey. It is delivered to the stack as a distinct 32-byte key at sequence 2250. |
| 23.998 s | [2783](../captures/group-call-outgoing-20260723-100408.jsonl#L2783), `08:05:31.849` | Group transaction 20 changes group media from audio to video; B device 0 is the `av-upgrader`. Relay transaction 3 adds `warp_mi_tag_len=4`. |
| 24.011 s | [2786](../captures/group-call-outgoing-20260723-100408.jsonl#L2786), `08:05:31.862` | B reports video state 1 (`Enabled` in stack logs). |
| 24.271 s | [3434](../captures/group-call-outgoing-20260723-100408.jsonl#L3434), `08:05:32.122` | UI model now reports a video call. SELF is still stopped; participant video states are tracked separately in `getCallInfo`. |
| 27.777 s | [4522](../captures/group-call-outgoing-20260723-100408.jsonl#L4522), `08:05:35.628` | SELF calls `setCallVideoMute(false)` and acquires a 1280×720, 15 fps camera track. |
| 33.655 s | [4987](../captures/group-call-outgoing-20260723-100408.jsonl#L4987), `08:05:41.506` | SELF calls `setCallVideoMute(true)`; group media remains video. |
| 42.742 s | [5312](../captures/group-call-outgoing-20260723-100408.jsonl#L5312), `08:05:50.593` | SELF enables camera again. |
| 46.272 s | [5602](../captures/group-call-outgoing-20260723-100408.jsonl#L5602), `08:05:54.123` | SELF stops camera again. |
| 47.339 s | [5669](../captures/group-call-outgoing-20260723-100408.jsonl#L5669), `08:05:55.190` | First local `endCall([2,true])`. |
| 47.345 s | [5672](../captures/group-call-outgoing-20260723-100408.jsonl#L5672) / [5674](../captures/group-call-outgoing-20260723-100408.jsonl#L5674), `08:05:55.196` | Local terminate goes to the call server: duration 43,583 ms, audio 20,480 ms, video 23,103 ms. |
| 47.463 s | [6224](../captures/group-call-outgoing-20260723-100408.jsonl#L6224), `08:05:55.314` | Local state becomes 0 (`None`). |
| 47.641 s | [6448](../captures/group-call-outgoing-20260723-100408.jsonl#L6448), `08:05:55.492` | Server group transaction 21 still has A/B `connected` and SELF `invited`: the group call survived the local leave. |
| 54.651 s | [7329](../captures/group-call-outgoing-20260723-100408.jsonl#L7329), `08:06:02.502` | `joinOngoingCall` uses the same call ID, creator, group, and three-person roster. |
| 54.667 s | [7334](../captures/group-call-outgoing-20260723-100408.jsonl#L7334), `08:06:02.518` | `lobby` is sent to the same `call-id@call` route. |
| 54.778 s | [7473](../captures/group-call-outgoing-20260723-100408.jsonl#L7473), `08:06:02.629` | UI model state 9; logs name it `Rejoining`. |
| 56.495 s | [7565](../captures/group-call-outgoing-20260723-100408.jsonl#L7565), `08:06:04.346` | `acceptCall([true,true])`. |
| 56.502 s | [7573](../captures/group-call-outgoing-20260723-100408.jsonl#L7573) / [7575](../captures/group-call-outgoing-20260723-100408.jsonl#L7575), `08:06:04.353–.354` | Video accept advertises Opus 16 kHz, H.264, `keygen=2`; UI state becomes 4 (`AcceptSent`). |
| 56.646 s | [7604](../captures/group-call-outgoing-20260723-100408.jsonl#L7604), `08:06:04.497` | Group transaction 23, `rekey=1`: SELF/A/B connected with PIDs 0/1/2. Relay UUID and SSRCs remain stable, self participant UUID and relay/HBH keys rotate. |
| 56.688 s | [7613](../captures/group-call-outgoing-20260723-100408.jsonl#L7613) / [7637](../captures/group-call-outgoing-20260723-100408.jsonl#L7637), `08:06:04.539–.545` | Stack emits the same 32-byte SELF rekey twice. The app encrypts it separately to A and B at sequences 7648 and 7657. |
| 56.833 s | [7850](../captures/group-call-outgoing-20260723-100408.jsonl#L7850), `08:06:04.684` | State 6 (`CallActive`) resumes. |
| 57.925 s | [8739](../captures/group-call-outgoing-20260723-100408.jsonl#L8739), `08:06:05.776` | Rejoin leg reports `rtp_traffic_started=1`. |
| 61.756 s | [8955](../captures/group-call-outgoing-20260723-100408.jsonl#L8955), `08:06:09.607` | Second local `endCall([2,true])`. |
| 61.760 s | [8958](../captures/group-call-outgoing-20260723-100408.jsonl#L8958), `08:06:09.611` | Local terminate reports 4,927 ms, all video-call duration. |
| 61.967 s | [9569](../captures/group-call-outgoing-20260723-100408.jsonl#L9569), `08:06:09.818` | Group transaction 25 again has A/B `connected` and SELF `invited`. |
| 64.555 s | [10456](../captures/group-call-outgoing-20260723-100408.jsonl#L10456) / [10458](../captures/group-call-outgoing-20260723-100408.jsonl#L10458), `08:06:12.406–.408` | Server terminates the logical group call, transaction 27, reason `group_call_ended`; summary duration 64,413 ms. |

No A/B leave transition occurs. SELF alone cycles
`connected → invited → connected → invited`; the final server terminate ends the
logical group call.

## Offer, rejoin, and device routing

### `startGroupCall`

The positional call at sequence 151 is:

```text
0  ["96170600887@s.whatsapp.net", "96176891546@s.whatsapp.net"]
1  ["242653052539031@lid", "74170125783269@lid"]
2  ["242653052539031:1@lid,242653052539031@lid",
    "74170125783269:43@lid,74170125783269:44@lid,74170125783269:45@lid,74170125783269@lid"]
3  "00954571727022506957B88D578C9487"
4  false
5  "120363411251996986@g.us"
6  false
7  ""
8  "test"
9  "https://static.whatsapp.net/rsrc.php/yK/r/EMya3e4mGbW.webp"
10 24
11 5
12 null
```

Only positions 0–3 and 5 are self-describing from this capture. Assigning names
to the remaining flags/numbers would be guesswork.

### `joinOngoingCall`

The positional call at sequence 7329 is:

```text
0  "00954571727022506957B88D578C9487"
1  "156535032389744:14@lid"
2  ""
3  ["96179377559@s.whatsapp.net",
    "96170600887@s.whatsapp.net",
    "96176891546@s.whatsapp.net"]
4  ["156535032389744@lid",
    "242653052539031@lid",
    "74170125783269@lid"]
5  ["156535032389744:7@lid,156535032389744@lid",
    "242653052539031:1@lid,242653052539031@lid",
    "74170125783269:43@lid,74170125783269:44@lid,74170125783269:45@lid,74170125783269@lid"]
6  true
7  "120363411251996986@g.us"
8  0
9  true
10 ""
11 false
12 ""
13 false
14 "test"
15 22
16 false
```

The rejoin candidate list contains SELF device 7 plus the bare SELF LID even
though device 14 remains the call creator and active web device. The capture
does not establish the reason for that difference.

### Routing facts

- Offer, lobby, accept, heartbeat, group status, and terminate use
  `00954571727022506957B88D578C9487@call`.
- Group updates arrive from that call route and carry ordered group transaction
  IDs: 8, 10, 12, 14, 18, 20, 21, 23, 25, 27.
- The initial offer stanza ID `39852.52661-23` is echoed by its ack and by
  receipts from A device 0, B device 0, and A device 1.
- Initial transport goes directly to A’s bare LID; transport and rekey return
  from A device 0.
- Rejoin rekeys go separately to A and B bare LIDs (`...-58` and `...-60`);
  receipts return from their selected device-0 JIDs.
- Other rejoin transport/status goes through the call server while peer
  transport records still identify A/B device 0.
- Relay latency and call-server acks use outer stanza IDs independently of
  group transaction IDs.

This is a mixed control topology: authoritative roster/lifecycle state is
group-wide at `call-id@call`, while device-specific key distribution and some
transport exchange are participant-addressed.

## Participant state and model behavior

The group roster transitions are:

| Group transaction | SELF | A | B | Media |
|---:|---|---|---|---|
| 8 | connected, PID not shown | outgoing | outgoing | audio |
| 10 | connected | receipt | outgoing | audio |
| 12 | connected | receipt | receipt | audio |
| 14 | connected, PID 0 | connected, PID 1 | receipt | audio |
| 18 | connected, PID 0 | connected, PID 1 | connected, PID 2 | audio |
| 20 | connected, PID 0 | connected, PID 1 | connected, PID 2 | video |
| 21 | invited | connected | connected | video |
| 23 | connected, PID 0 | connected, PID 1 | connected, PID 2 | video, rekey |
| 25 | invited | connected | connected | video |
| 27 | group call ended | summary only | summary only | video |

Stack logs make the local call-state mapping explicit: 0=`None`, 1=`Calling`,
4=`AcceptSent`, 6=`CallActive`, 9=`Rejoining`.

The UI call model remains singular (`peer=242653052539031@lid`) even with three
participants. Its `peerVideoState` and `peerMicMuted` fields are therefore not a
complete group roster. `getCallInfo.participants[]` and `group_info` carry the
participant-specific state.

The `outgoing → receipt → connected` and `invited` labels are observed wire
values. “Ringing/delivered/joined” and “eligible to rejoin” are plausible UI
meanings, but they are interpretations, not definitions supplied by the wire.

## Relay and transport topology

### Relay allocations

| Relay transaction | Sequence | Relay set | Other observed fields |
|---:|---:|---|---|
| 0 | 290 | `sea5c01`: `63.127.208.217:3478` and IPv6 counterpart | one 167-byte token; 70-byte auth token; 24-byte relay key; 40-byte HBH key; self PID 0; UUID `yDWFn3J_xA_Lv4Hz`; participant UUID `anGBIwJc` |
| 1 | 483 | `zrh1c01` `157.240.17.133:3478`; `mxp1c01` `31.13.86.63:3478`; `fra3c01` `57.144.249.57:3478`, each with IPv6 counterpart | three 193-byte participant tokens; measured c2r RTT 18/22/27 ms |
| 2 | 1088 | same zrh/mxp/fra3 set | three 174-byte participant tokens; all PIDs present |
| 3 | 2783 | same zrh/mxp/fra3 set | three 188-byte participant tokens; `warp_mi_tag_len=4`; media becomes video |
| 5 | 7604 | zrh/mxp plus `fra5c02` `157.240.253.133:3478` | three 188-byte participant tokens; new self participant UUID; `warp_mi_tag_len=4`; rotated relay/HBH keys |

Each allocation has a mirrored hook-stage record (292, 485, 1090, 2785, 7606);
those mirrors are not separate allocations. Relay transaction 4 is absent.

The browser creates DTLS/SCTP data-channel connections to all candidates, but
relay 0 (`zrh1c01`, `157.240.17.133:3478`) is elected and remains selected for
both peers and both local legs. No relay swap is logged. The stack names the
topology `WARP SFU`.

Transport-destroy counters show relay-only media in this capture:

| Local leg | Relay TX/RX packets | P2P RTP |
|---|---:|---:|
| Initial outgoing leg | 465 / 754 | 0 |
| Rejoin leg | 94 / 125 | 0 |

Transport signaling reports network medium 2 for SELF and A. B reports medium 3.
The numeric medium names are not defined by this capture.

## SSRC and media inventory

The stack deterministically precomputes one three-SSRC tuple per stream: primary,
FEC, and NACK/retransmission. The active device SSRCs are:

| PID / device | Audio | Video 0 | App data |
|---|---|---|---|
| 0 / `156535032389744:14@lid` | `BE20AD8C / F5D68220 / 97C0E72F` | `F86E9744 / 58222943 / ABE65EAB` | `7BF3B9B0` |
| 1 / `242653052539031:0@lid` | `AB8E3737 / 957833BC / 1EA0520C` | `AB1B4748 / 54200863 / 1C5FA708` | `4A1A38B7` |
| 2 / `74170125783269:0@lid` | `ED7F90D9 / 1145C787 / ED05D943` | `8A5F7EF7 / 1C5930B0 / 33CEB78B` | `02DF725A` |

Each device also gets a second video tuple, two screen-share tuples, and an IMU
SSRC. SELF additionally logs HBH FEC TX/RX SSRCs. Candidate device SSRC sets are
generated for A device 1 and B devices 43/44/45, but the active transport only
attaches selected device-0 media for A/B.

The same SSRC values are regenerated/reused after rejoin even though the local
participant UUID and key material rotate. PIDs are likewise stable. This is
direct evidence that call/device identity, not the per-leg participant UUID, is
the stable input to this capture’s SSRC plan.

Subscriptions are participant-specific. With only A connected, SELF audio,
video, and app-data SSRCs subscribe PID 1. After B connects, the lists contain
PIDs 1 and 2. WARP bandwidth reports later carry separate RX records for PID 1
and PID 2 and one TX record for PID 0.

### Audio

- The signaling offer advertises Opus at 8 and 16 kHz.
- The active RTP stack selects `mlow-1`, mono 16 kHz, 60 ms frames, VBR/DTX/FEC.
- RTP payload types are 120=`mlow-1`, 121=`mlow-red-1`, 122=`mlow-1`.
- Browser capture requests 16 kHz mono, while the acquired browser track reports
  48 kHz; the VoIP stack performs the runtime adaptation.
- The first leg reports 409 audio payload packets / 46,518 bytes sent.
- The rejoin snapshots receive audio from both peers: 15 packets from A and 11
  from B by sequence 8786.

### Video

- The group starts as audio and is upgraded by B; no downgrade is observed.
- Video wire state 1 is logged as `Enabled`; state 6 is logged as `Stopped`.
- A and B have independent state messages. SELF camera stop/start does not
  change the group’s `media=video` state.
- SELF acquires 1280×720 at a 15 fps target. The encoder logs H.264 at 320×240.
- Measured average capture rates are 13 fps for the first leg and 14 fps for the
  rejoin leg.
- First leg: SELF sends 39 video payload packets / 8,743 bytes. B receives 15
  video packets / 7,873 bytes; A receives none.
- Rejoin: accept explicitly advertises H.264. Both peers later signal Stopped;
  SELF still sends 60 H.264 payload packets / 13,294 bytes and receives no peer
  video payload packets.

No screen share occurs. No track-level `mute`, `unmute`, or `ended` browser event
is recorded; all 11 media-track events are initial observations.

## Keying evidence

### Relay and participant keys

Every active relay allocation contains:

- participant token records, one per relay/token slot;
- one 70-byte relay authorization token;
- one 24-byte relay `key`;
- one 40-byte `hbh_key`.

The 24-byte relay key and 40-byte HBH key are identical across relay transactions
0, 1, 2, and 3. Both rotate at transaction 5 after rejoin:

| Material | Initial-leg SHA-256 | Rejoin SHA-256 |
|---|---|---|
| 24-byte relay key | `54a3656f76c00bd67b8750ec0883ae4dd2998e43187138f5918d5cf0c6a98995` | `7debfe9a4103c4ec5e94fa80b46fa0a5ae48ca1c61a6f38cb4f557957d9dc83b` |
| 40-byte HBH key | `f508ef53b59fc0e44c7dd1842d1bfea38daa6a5f275ad13ff0aa3b6b6cedf085` | `6195f2c2db59fa89d1b1aa6b6ba4cda6a8dbfc3005eee51ed5c6869af8c3a1cd` |

### Participant rekeys

All observed participant rekeys use `encopt keygen=2`.

| Sender / phase | Encrypted input | Stack-visible 32-byte key | Result |
|---|---|---|---|
| A joins, transaction 14 | sequence 517, `type=msg`, 146 bytes | sequence 546, SHA-256 `2ac8537b9ca2f2ae8165de3c964718e296689774664964ef1edb80faf03a767c` | SELF and A participant key slots installed |
| B joins, transaction 18 | sequence 2244, `type=pkmsg`, 231 bytes | sequence 2250, SHA-256 `8982d8414dc8c9637b4b2078ba1e6fd06a4bd44d8c7a442ee61e12365b81d235` | Existing SELF/A slots retained; B slot installed |
| SELF rejoins, transaction 23 | identical 32-byte payload at sequences 7613 and 7637, SHA-256 `7aa4809a1f6ea3da1ece1d0702c96431fc2907f162fd93114db93ed34d194ff5` | separately encrypted to A and B as two different 98-byte ciphertexts at 7648/7657 | all three participant slots updated; SELF TX activation delayed 1,500 ms |

The supported model is per-sender key distribution: A and B contribute different
sender keys; SELF contributes one sender key and distributes it separately to
both remote participants. The stack maintains participant-specific key slots.
This does not prove whether every final derived subkey differs; the derivation
state and on-wire plaintext are not captured.

### SRTP, HBH, SFrame, and WARP

The stack attaches SRTP transports to participant SSRCs and logs that it is in
“E2E mode.” `srtp_enc_type=1` is recorded, but this capture does not define that
numeric value normatively.

Although functions named `update_sframe_key_for_participant` run and configure
codec mask `0x7` / cipher suite `0x3`, every observed audio and video stream logs:

```text
sframe_enabled 0->0
sframe_tx_enabled 0->0
sframe_rx_enabled 0->0
```

Active SFrame frame protection is therefore not demonstrated and should not be
assumed for this call.

An HBH key is signaled, HBH feedback/FEC subscriptions exist, and HBH NACK
features are enabled, but the stack also logs `use_hbh_srtp 0` and “we are in E2E
mode, skip.” The precise protection order and use of the HBH key cannot be proven
without packets.

WARP is directly active: the stack names the SFU mode, carries participant PIDs,
reports per-participant bandwidth, transmits WARP headers, and finishes with zero
WARP integrity errors. The signaled media-integrity tag length is 4.

## Mute, video-state, and upgrade evidence

- All 22 captured `mute_v2` nodes carry `mute-state=0`.
- Every call-model snapshot has `selfMicMuted=false` and
  `peerMicMuted=false`.
- No mute action occurs, so this capture proves the unmuted representation only.
- The group changes from `media=audio` to `media=video` once, in transaction 20.
- B device 0 is explicitly named as the AV upgrader.
- Individual participant video state changes continue independently after the
  group upgrade.
- SELF toggles camera twice with `setCallVideoMute(false/true)` without
  downgrading the group media type.
- No `media=audio` group update follows the upgrade and stack statistics say the
  call was not created by a group-call downgrade.

## Comparison with current code

| Observed concept | Refreshed whatsmeow | meowcaller thin rebuild |
|---|---|---|
| Group lifecycle | Missing. Public call API is explicitly 1:1 ([calls.go](https://github.com/tulir/whatsmeow/blob/edfef3172122795d517177a5762eb4157cd6ec82/calls.go#L37)); `GroupJID` is metadata only. No `group_update`, `lobby`, group `enc_rekey`, `connect_stat`, or group-duration handling. | Missing. Client/Call and engine are explicitly one-peer ([client.go](../../client.go#L12), [livecall.go](../../livecall.go#L10), [engine.go](../../engine.go#L28)). |
| Participant/device roster | Missing call-specific roster. State stores one destination and events expose one self/peer pair. | Missing participant join/leave/state callbacks, per-participant key slots, sinks, mixers, and demux. |
| Group rekey | 1:1 offer-key encryption/decryption exists but is live-unvalidated; no group rekey epochs or participant ownership. | One receive rekey overwrites one receive keystream ([session.go](../../session.go#L166)); no simultaneous participant-key set. |
| Relay description | Parses relay key/token/auth-token/`te2` and elects one endpoint, but drops participant UUID/PID, HBH key, dynamic WARP tag length, and group relay state. | UDP→DTLS→SCTP→DataChannel relay path exists but live send/receive/connect remain `NOT VALIDATED` ([relay/relay.go](../../relay/relay.go#L100)); it receives whatsmeow’s reduced endpoint. |
| Latency/transport/heartbeat | Relay-latency parsing/replies and transport events exist. Heartbeat builder exists without a scheduler. `connect_stat` and group routing are absent. | STUN allocation, consent, descriptors, and binding response exist, but the live loop is unvalidated; group `connect_stat` belongs in call control. |
| SSRC roster binding | No media plane. | Participant-ID/SSRC derivation is KAT-covered ([rtp/ssrc.go](../../rtp/ssrc.go#L23)), but the live engine derives its fixed descriptor plan from one local participant rather than the observed roster ([engine_media.go](../../engine_media.go#L137)). |
| E2E/HBH/SFrame | Does not surface HBH key or participant key slots. | SFrame and HBH primitives are KAT-covered but unwired ([srtp/sframe.go](../../srtp/sframe.go#L145), [srtp/hbh.go](../../srtp/hbh.go#L63)). Current production pipeline uses 1:1 E2E SRTP and omits SFrame ([session.go](../../session.go#L150)). The capture’s disabled SFrame flags mean wiring SFrame cannot be justified from this trace alone. |
| WARP tag length | Signaled value is discarded. | Tag length is fixed at 4 ([srtp/warp.go](../../srtp/warp.go#L16)), matching this capture but not accepting a future signaled value. |
| Audio/video | Codec selection, mute, and independent 1:1 video state exist. | Audio plane is implemented. H.264 send/receive/feedback is surfaced but explicitly unvalidated ([livecall.go](../../livecall.go#L161), [engine_media.go](../../engine_media.go#L319)). No multi-participant mixer/demux. |

Meowcaller’s `go.mod` pins the refreshed whatsmeow worktree, so this comparison
uses aligned versions. No relevant group-call surface is merely scaffolded:
these gaps are missing, 1:1-only, unwired primitives, or explicitly
`NOT VALIDATED`.

## Observed conclusions

1. One logical server-side group call contains two local media legs.
2. Local leave does not end the group call; SELF becomes `invited` while A/B
   remain connected, and lobby/accept rejoins the same call.
3. Group state and lifecycle are centralized at `call-id@call`, while key
   distribution and some transport exchange are participant/device-specific.
4. The media topology is a shared WARP SFU relay with stable participant PIDs,
   stable device-derived SSRCs, and participant-specific subscriptions.
5. Relay UUID, PIDs, and SSRCs survive rejoin. Self participant UUID, relay key,
   HBH key, and SELF sender key rotate.
6. Media keying is per sender at the distribution layer: A and B provide
   different keys; SELF’s one sender key is separately encrypted to A and B.
7. The group media type is shared, but participant video directions/states are
   independent. Camera stop does not downgrade the group.
8. This trace actively uses WARP/SFU and E2E SRTP machinery. It does not show
   active SFrame stream protection.

## Hypotheses and unresolved questions

- `outgoing`, `receipt`, `connected`, and `invited` likely correspond to
  invitation, delivery, join, and rejoin eligibility, but only the raw labels
  are proven.
- The 32-byte participant values appear to be sender chain/source keys. Their
  exact KDF, epoch/count rules, and media-key outputs are not exposed.
- The 24-byte relay key and 40-byte HBH key clearly rotate per local leg, but the
  exact consumers of each field are not fully visible.
- The capture cannot prove RTP header extensions, ROC handling, SRTP transform
  order, retransmission packet form, or the exact HBH/E2E boundary because it has
  no packet bytes.
- LobbyAck is visible in stack logs but its complete decoded stanza is absent.
- The meanings of undocumented `startGroupCall`/`joinOngoingCall` positional
  flags and numeric values remain unknown.
- Local `endCall` reason code 2 is not defined by this capture.
- The reason for missing group transactions 16, 22, 24, 26 and relay
  transaction 4 is not observable.
- No remote leave, mute transition, group downgrade, screen share, reaction, or
  app-data transmission occurs. Each needs a targeted follow-up capture.

## Evidence needed before implementation changes

1. A packet-level or recorder-level RTP/SRTP/WARP vector tied to PID and SSRC,
   with keys supplied separately, to establish header format and transform
   order.
2. A late-join capture where a fourth participant joins after media is flowing,
   to establish rekey ownership, epoch/count, SSRC allocation, and roster
   updates.
3. Targeted mute/unmute and participant-leave captures.
4. A video downgrade plus independent camera-state capture.
5. Screen-share, reaction, and app-data captures.
6. An incoming-from-scratch group call to compare with outgoing-create and
   join-ongoing flows.

Until those exist, current 1:1 behavior should remain unchanged.
