# Mobile hand raise and group screen-share analysis

## Capture integrity

- Raw capture: `diag/captures/group-mobile-hand-raise-screen-share-v2-20260726-033924.jsonl`
- Events: 14,147
- Bytes: 21,988,664
- SHA-256: `9ac05ee3161370020172ffeb5e4f5b3b066dcf049fdb1372cbc62e6e1e2e7248`
- Capture schema: `wa-voip-diag/v2` for every event
- Call ID: `006C151B7929963D0FE0DB9E0774817D`

The collector was stopped before hashing and the raw JSONL was renamed unchanged.
Line numbers below refer to that fixed archive. This report is a compact derived
artifact and does not modify the source capture.

## Executive conclusion

The capture answers both open questions.

Raise hand is not an emoji and does not reuse reaction app-data. It is a
call-signaling `user_action` whose action is `raise_hand`, containing a nested
`raise_hand` child with `raise-hand-state="1"` for raised and `"0"` for lowered.
The receiver acknowledges it with call ACK type `user_action`, emits
`RaiseHandStateChanged`, and stores the result as the participant's
`is_hand_raised` model field.

The mobile screen share uses the same version-2 `screen_share` control family seen
in the previous browser capture, but its start includes
`screen_share_id="1"`. It carried actual H.264 screen-share media under stream role
2: the receiver decoded and rendered 10 frames over 3.063 seconds. The mobile
participant then left, so the receiver stopped its screen-share session without
an explicit state-2 stanza.

## Call topology

The call starts as a direct audio call to `242653052539031@lid` and is accepted at
01:47:48. It is then upgraded in place to a three-person, server-created ad-hoc
group call:

| Role | Identity | Active device evidence |
|---|---|---|
| Creator/self | `156535032389744@lid` | `156535032389744:15@lid` |
| Original peer; mobile actor | `242653052539031@lid` | stack identity `...9031:0@lid`, iPhone platform |
| Added peer | `74170125783269@lid` | stack identity `...3269:43@lid`, web platform |

The same call ID is retained. `is_group_call=true`,
`is_group_call_created_on_server=true`, participant count is three, connected
limit is 32, and `group_jid=null`. This is therefore an ad-hoc group call rather
than a call bound to a WhatsApp group chat.

## Timeline

| Time (UTC) | JSONL evidence | Observed event |
|---|---:|---|
| 01:47:44.897 | line 659 | Direct audio `startCall` begins. |
| 01:47:48.496 | line 2,579 | Original peer accepts. |
| 01:48:06.397 | line 4,276 | Group update transaction 12 reports three participants and audio media. |
| 01:48:14.850 | line 5,147 | Call info reports all three participants connected. |
| 01:48:16.132 | line 6,351 | Mobile participant sends hand state 1. |
| 01:48:19.272 | line 6,715 | Same participant sends hand state 0. |
| 01:48:20.643 | line 6,835 | Same participant sends hand state 1 again. |
| 01:48:31.956 | line 8,508 | Participant model confirms `is_hand_raised=true`; call is now video. |
| 01:48:39.332 | line 10,251 | Mobile sends screen-share state 1, version 2, ID 1. |
| 01:48:39.374 | line 10,289 | Receiver starts peer screen-share session. |
| 01:48:39.937 | line 10,349 | H.264 stream-role-2 format changes to 448×960. |
| 01:48:40.403 | line 10,421 | Screen-share format changes to 336×720. |
| 01:48:42.449 | lines 11,145–11,147 | Participant-left event stops the receiver's screen-share session. |
| 01:48:42.548–42.608 | lines 11,379–11,705 | Final screen-share metrics report 3.063 s and 10 decoded/rendered frames. |
| 01:48:42.757 | line 12,217 | Mobile participant is state 11; screen-share model is stopped. |
| 01:48:44.347 | line 13,206 | Remaining call ends separately. |

## Hand-raise signaling

First raise:

```xml
<call from="242653052539031@lid" id="1785026985-195" t="1785030496">
  <user_action
    call-id="006C151B7929963D0FE0DB9E0774817D"
    action="raise_hand"
    call-creator="156535032389744:15@lid">
    <raise_hand raise-hand-state="1" />
  </user_action>
</call>
```

The receiver sends:

```xml
<ack
  to="242653052539031@lid"
  id="1785026985-195"
  class="call"
  type="user_action" />
```

The lower and second raise have the identical envelope, with fresh stanza IDs and
state values 0 and 1. Teardown metrics agree with the wire events:
`rx_raise_hand_count=2` and `rx_lower_hand_count=1`.

The stack logs `update_hand_raise_ranking` after each transition, and later call
info reports the mobile participant with `is_hand_raised=true`. This explains why
the feature behaves differently from reactions:

- a reaction is transient version-2 RTP app-data carrying an emoji string;
- a raised hand is persistent call-signaling state attached to a participant and
  used by the group ranking model.

No hand emoji is transmitted. If the client renders a hand icon, that is UI
presentation of the boolean state.

The capture strongly associates the feature with group-call state: all three
transitions occur after `is_group_call=true`, and the handler is the group
hand-ranking component. It does not directly test sending the action in a
one-to-one call, so whether the protocol rejects it or the UI merely hides it
remains open.

## Mobile group screen-share signaling

Start:

```xml
<call from="242653052539031@lid" id="1785026985-218" t="1785030519">
  <screen_share
    screenshare_state="1"
    call-id="006C151B7929963D0FE0DB9E0774817D"
    call-creator="156535032389744:15@lid"
    version="2"
    screen_share_id="1" />
</call>
```

The receiver sends a direct call ACK to the participant:

```xml
<ack
  to="242653052539031@lid"
  id="1785026985-218"
  class="call"
  type="screen_share" />
```

The child is otherwise compatible with the previous browser/direct capture. The
material difference is `screen_share_id="1"`, which was absent from the browser
sender's direct-call stanza.

The receiving view proves only the server-delivered shape: top-level `from` is
the participant's bare LID, and the ACK returns to that LID. It does not reveal
whether the mobile sender originally addressed the stanza to the call-server JID
and the server fanned it out. Group-origin routing therefore remains an open
question for a sender-side capture.

### Comparison with the browser screen-share capture

| Property | Browser sender, direct call | Mobile sender, ad-hoc group call |
|---|---|---|
| Control child | `screen_share` | `screen_share` |
| Start state | 1 | 1 |
| Version | 2 | 2 |
| `screen_share_id` | absent | 1 |
| Codec/media role | H.264 screen share | H.264, stream role 2 |
| Stop | explicit state-2 stanza | implicit on sharer departure |
| Observed active time | 6.377 s | 3.063 s |
| Decoded screen-share frames | 65 | 10 |
| Source geometry | 1242×720 document share | portrait 448×960, then 336×720 |
| Camera/video prerequisite | browser UI had video active | mobile had camera video active |

The common child, state values, version, media codec, and independent stop
lifecycle support one shared screen-share protocol. The mobile-only ID and
group-routing origin are the remaining meaningful differences. Both interactions
had camera video active before sharing, but neither capture proves that the wire
protocol itself requires video-first.

## Screen-share media evidence

This was not signaling-only. Before the share begins, the call has preallocated
screen-share SSRC families for the mobile participant:

```text
stream id 0: 0x2AC2EB4F, 0xA9DDF11E, 0x7B9AFEB6
stream id 1: 0xA8C884D2, 0x823F1868, 0x7F6B606C
```

On state 1, the receiver:

- sets `Dir 2, screen share session = 1`;
- activates the screen-share-receiver bandwidth rule;
- decodes H.264 under `stream_role 2`;
- changes format from 320×144 to 448×960, then 336×720;
- renders 10 frames and reports 10 decoded output frames;
- reports average receive bitrate 60,825 bps and 3.27 decoded fps;
- reports average encoded dimensions 347×744;
- decodes the first frame 482 ms after accepting the share.

These metrics establish an independent screen-share media role. They do not
correlate a captured encrypted packet to one exact SSRC from the preallocated
families, so the active layer remains unproven.

## Stop behavior

No explicit incoming `screen_share screenshare_state="2"` appears. Instead, a
critical group update removes the mobile screen sharer from the connected set.
The stack emits `Group Participant Left`, calls
`stop_peer_screen_share_and_notify`, changes the receiver session to zero, and
emits local screen-share state 2.

This is important for lifecycle handling: a receiver must clear screen-share
state when its sharer departs, even if the stop control stanza never arrives. The
remaining two-person call continues for about 1.9 seconds and then ends through
its own lifecycle.

## Comparison with the current local branches

The capture exposes two signaling gaps in the current
`whatsmeow-voip-refresh` `rajeh/group-calls` branch:

- `handleCallEvent` has typed cases for video, group updates, and rekeys, but no
  `user_action` or `screen_share` case. Both currently fall through to
  `UnknownCallEvent`.
- The generic deferred ACK copies a top-level stanza `type` attribute. These call
  nodes have no top-level `type`, while the observed acknowledgements require the
  child tag: `type="user_action"` or `type="screen_share"`. Video already has a
  dedicated ACK builder for this same reason. Therefore the current fallback ACK
  is structurally incomplete for both newly observed controls.
- The typed event surface has `CallVideo`, `CallMute`, and group roster updates,
  but no hand-state or screen-share event.
- The call state does not track raised hands or the active screen sharer, so a
  participant departure cannot yet synthesize the observed screen-share stop.

The current meowcaller branch has complete RTC app-data reactions and
participant-attributed group video, but no hand-raise or screen-share public API.
Its web controller likewise has reaction and video controls but no hand or
screen-share action. Existing reactions cannot be reused for hand raise because
the transport, lifecycle, and participant semantics are different.

## Implementation implications

For whatsmeow call control:

- model `user_action action="raise_hand"` with nested
  `raise_hand raise-hand-state={0,1}`;
- ACK incoming actions using call ACK type `user_action`;
- expose sender identity, raised/lowered state, and a state-change event;
- keep the action scoped to the active call and participant roster;
- preserve `screen_share_id` when present and ACK screen share as
  `type="screen_share"`;
- synthesize screen-share stopped state when the sharer leaves.

For meowcaller and the web example:

- hand raise is a signaling/UI participant-state feature, not an app-data
  reaction;
- render raised-hand state per participant and remove it on lower/leave;
- keep screen share as a distinct H.264 stream role rather than replacing the
  participant's camera SSRC identity;
- stop and detach the share renderer on participant departure;
- support the optional numeric screen-share ID without assuming it is always
  absent or always 1.

## Observed facts versus hypotheses

### Observed

- The direct call becomes an ad-hoc group call on the same call ID and without a
  group JID.
- Raise/lower uses `user_action`, not RTP app-data.
- State 1 raises and state 0 lowers the hand.
- Every incoming action receives a `user_action` ACK.
- The final state persists as `is_hand_raised=true`.
- Mobile screen-share start is version 2, state 1, with screen-share ID 1.
- The receiver decodes and renders 10 H.264 screen-share frames under stream role
  2.
- Participant departure stops screen sharing without an explicit stop stanza.

### Hypotheses/open questions

- The UI exposes hand raise only in group calls because it feeds participant
  ranking; one-to-one protocol behavior is not tested.
- `screen_share_id` selects a stream layer, identity, or share generation.
- The call server fans sender-originated hand and screen-share signals out to
  group participants.
- Active media used one of the recorded screen-share SSRCs, but this capture does
  not identify which member of the family carried it.
