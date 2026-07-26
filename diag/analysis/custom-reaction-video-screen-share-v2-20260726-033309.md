# Custom reaction, video upgrade, and screen-share analysis

## Capture integrity

- Raw capture: `diag/captures/custom-reaction-video-screen-share-v2-20260726-033309.jsonl`
- Events: 22,846
- Bytes: 34,680,076
- SHA-256: `99cd42822715706d42bb1e672c1f13323154f7fe69cf01e589aef60d212b5631`
- Capture schema: `wa-voip-diag/v2` for every event
- Call ID: `00A91AD82043E1FAF5A2B9B2C4507FFC`

The collector was stopped before hashing and the raw JSONL was renamed unchanged.
Line numbers below refer to that fixed archive. This report only records compact
derived evidence.

## Executive conclusion

The capture contains one direct two-person call that starts as audio, sends a
custom `😃` reaction, upgrades in place to H.264 video, switches the caller's
capture source from camera to screen, then switches back to camera.

The three operations use two different control planes:

- The emoji is RTC app-data. `sendReaction("😃")` produces no XMPP call stanza.
  The receiver observes a version-2 app-data RTP packet and both endpoints emit
  `ReactionStateChanged`.
- Video upgrade uses the existing `video` signaling state machine on the same call
  ID.
- Screen sharing uses a separate version-2 `screen_share` call stanza. State `1`
  starts sharing and state `2` stops it. Stopping sharing restores camera capture;
  it does not stop video or end the call.

The capture supports the user's UI observation that WhatsApp Web exposes screen
sharing only after video is active. It does not prove that the wire protocol itself
rejects screen sharing on an audio-only call.

## Identities and topology

| Role | Tab | Identity |
|---|---:|---|
| Caller and screen sharer | `818526425` | creator device `156535032389744:15@lid` |
| Callee and screen-share receiver | `825906857` | peer `242653052539031@lid`, active device `:2` |

This is not a group call: participant count remains two, `is_group_call=false`,
and no group JID is present. The results still matter for group-call work because
the reaction media format and screen-share child stanza are participant-oriented
surfaces that can be compared with the next group/mobile capture.

## Timeline

| Time (UTC) | JSONL evidence | Observed event |
|---|---:|---|
| 01:36:43.225 | line 7,978 | `startCall` begins a direct audio call. |
| 01:36:43.296 | line 7,992 | Initial offer contains audio/net/capability children and no video child. |
| 01:36:47.161 | line 9,785 | Callee invokes `acceptCall`; both sides reach connected state. |
| 01:36:56.886 | page input | Caller clicks the control whose exact ARIA label is `😃`. |
| 01:36:56.939 | line 11,691 | VoIP stack invokes `sendReaction("😃")`. |
| 01:36:56.978–57.027 | lines 11,699–11,712 | Receiver and sender emit `ReactionStateChanged`; app-data stats report one 24-byte packet with no errors. |
| 01:37:01.966–02.115 | lines 12,252/12,261 and adjacent logs | Both sides emit another `ReactionStateChanged` about five seconds later. No second app-data transmission is logged. |
| 01:37:07.957 | line 12,946 | Caller invokes `requestVideoUpgrade`. |
| 01:37:08.013 | line 13,007 | `video state="11"` requests H.264 video. |
| 01:37:08.511–08.918 | lines 13,659–14,911 | State exchange converges through states 6/4 to enabled state 1. |
| 01:37:08.577–09.485 | lines 14,137/15,648 | Both tabs acquire 1280×720 30 fps camera tracks. |
| 01:37:15.844 | line 16,686 | Caller requests `getDisplayMedia`, including display video and optional system audio. |
| 01:37:21.505 | line 17,467 | Display chooser returns; the serialized result is empty, but subsequent capture and signaling prove success. |
| 01:37:21.656 | line 17,508 | Caller sends `screen_share`, state 1, version 2, to the callee device. |
| 01:37:21.663–21.728 | lines 17,532/17,583 | Dedicated screen-share driver starts; captured content is 1242×720 and classified as document content. |
| 01:37:21.806–21.869 | lines 17,636 onward | Callee receives and applies the state-1 screen-share request. |
| 01:37:28.033 | line 19,040 | Caller sends `screen_share`, state 2, version 2. |
| 01:37:28.092 | line 19,071 | Stack reports camera capture successfully restored after screen sharing stops. |
| 01:37:28.148–28.227 | lines 19,110 onward | Callee receives and applies the state-2 request. |
| 01:37:29.808 | line 19,516 | Caller separately ends the call 1.775 seconds after the stop signal. |

## Custom reaction transport

The reaction path is directly observed:

1. The clicked element has `ariaLabel="😃"`.
2. The VoIP API receives the identical Unicode string in `sendReaction`.
3. The caller's app-data stream is version 2 with SSRC `0x7E2590E7`.
4. The callee reports `Rx App Data RTP Stats [packets=1, bytes=24, bad=0,
   dup=0, ooo=0]`.
5. Both sides emit `ReactionStateChanged`.
6. No `reaction` child appears in WAWap/XMPP signaling.

The second state-change event occurs about five seconds later without another
app-data transmit statistic. Automatic transient-reaction expiry is therefore the
best explanation, but the callback did not expose the cleared value and this
remains an inference.

Meowcaller's current app-data codec already accepts any valid UTF-8 string, so
`😃` does not require a media-protocol change. Its web example exposes only fixed
reaction buttons (`👍 ❤️ 😂 😮 😢 🙏`), making arbitrary emoji selection a UI
capability gap rather than a wire-format gap. This one sample demonstrates an
emoji outside that fixed list; it does not establish limits for multi-code-point
emoji or arbitrary text.

## Video upgrade

The original offer is audio-only. The upgrade stays on the same call and uses
H.264:

```text
requestVideoUpgrade
  -> video state 11, dec=H264
  -> peer state exchange 6 / 4
  -> video state 1
  -> camera capture and H.264 media
```

Both participants acquire 1280×720 camera tracks at 30 fps. No new call ID, group
identity, or parallel call is created.

## Screen-share signaling

Start:

```xml
<call to="242653052539031:2@lid" id="8160.63444-142">
  <screen_share
    call-id="00A91AD82043E1FAF5A2B9B2C4507FFC"
    call-creator="156535032389744:15@lid"
    screenshare_state="1"
    version="2" />
</call>
```

Stop is the same shape with a new stanza ID and `screenshare_state="2"`. The
recipient acknowledges each with `class="call" type="screen_share"`.

The state meanings are not inferred from names alone:

- state 1 coincides with creation of the shared screen-capture driver, local
  `screen share session = 1`, a 1242×720 capture, and receiver start handling;
- state 2 coincides with stopping that driver, `screen share session = 0`,
  receiver stop handling, and successful restoration of camera capture.

The screen-share interval lasts about 6.377 seconds. The call ends later through
a separate `endCall`, proving that state 2 is not call termination.

## Media topology

At call setup, WhatsApp deterministically preallocates separate SSRC families for
each participant's audio, two camera-video layers, two screen-share layers, and
app-data. The screen-share identities therefore exist even though the call starts
as audio-only.

During sharing, the stack:

- creates a dedicated screenshare capture instance;
- reports `Dir 1, screen share session = 1` at the sender and `Dir 2` at the
  receiver;
- keeps H.264 as the negotiated video codec and emits no new codec-negotiation
  stanza;
- classifies the selected source as `doc_screen_share=1`,
  `video_screen_share=0`;
- records 65 decoded screen-share video frames at teardown;
- returns to the camera source when state 2 is sent.

This proves a distinct logical screen-share media role and capture path. The
extension did not record a decrypted RTP packet-to-SSRC correlation during the
short sharing interval, so it does not yet prove which preallocated screen-share
SSRC/layer carried the active frames.

## Implementation implications

For meowcaller:

- Reaction encoding is already compatible with the observed custom emoji. The web
  example can expose an arbitrary emoji input/picker without changing app-data
  encoding.
- Screen sharing needs explicit state separate from camera video state. A stop
  operation must restore camera media and must not call hangup or video downgrade.
- The media model needs a screen-share role/SSRC in addition to the existing
  primary-video slot before it can faithfully transmit or attribute screen-share
  media.

For whatsmeow call control:

- Add a `screen_share` child with `call-id`, `call-creator`,
  `screenshare_state`, and `version=2`.
- Route it to the participant device for a direct call and require the ordinary
  call-class ACK.
- Keep screen-share lifecycle independent from the `video` state machine. This
  capture does not yet establish group routing or whether a group control stanza
  targets `CALLID@call`.

## Observed facts versus hypotheses

### Observed

- `😃` is passed unchanged from UI to `sendReaction`.
- Reaction delivery uses version-2 RTP app-data and not call signaling.
- The receiver accepts one 24-byte app-data RTP packet without error.
- The same direct call upgrades from audio to H.264 video.
- Display capture begins only after video is enabled in this interaction.
- Screen-share signaling version is 2.
- State 1 starts screen sharing; state 2 stops it.
- Screen share has a dedicated capture driver and preallocated SSRC families.
- Stopping screen share restores camera capture and leaves the call active.

### Hypotheses/open questions

- The roughly five-second reaction state change is automatic expiry.
- Valid UTF-8 strings beyond the tested emoji are accepted without further
  semantic validation.
- Video-first is a WhatsApp Web UI prerequisite rather than a protocol
  prerequisite.
- Active screen-share RTP uses the preallocated screen-share SSRC family.
- Group calls use the same child payload but different stanza routing.

The active follow-up capture is intended to resolve the hand-raise payload and
then compare its transport with reactions and screen-share signaling.
