# WhatsApp group calls, call links, participant state, and RTC app-data

This document records the local implementation on the `rajeh/group-calls`
branches of meowcaller and whatsmeow. It separates captured facts from the few
remaining protocol inferences.

## Implemented surfaces

### Whatsmeow call control

- Create server-issued audio/video call links with one click, then copy or join
  the generated URL without manually entering a token; preview/join controls for
  existing links use a separate input.
- Keep the reusable link token separate from each active call ID.
- Represent direct admission and approval-gated waiting-room admission.
- Send pending waiting-room heartbeats and stop them on admission, denial,
  hangup, or call removal.
- Enable or disable approval and admit or deny one pending user.
- Parse and acknowledge authoritative waiting-room updates.
- Start calls from explicit participants or from a WhatsApp group JID.
- Add one or more participants to an active call.
- Re-ring a non-connected participant already present in an active roster.
- Parse group membership, relay allocation, participant PID/device selection,
  and group key epochs.
- Send and receive persistent raised-hand state with typed `user_action` ACKs.
- Send and receive version-2 screen-share state with typed `screen_share` ACKs.
- Clear raised-hand and screen-share state when a participant leaves.

### Meowcaller API and media state

- Create, preview, and join call links through `Client`.
- Expose a dormant `CallPhaseWaitingRoom`; relay/media startup remains blocked
  until admission produces ordinary group-call state.
- Expose sanitized waiting-room state without retaining the link token on
  `Call`.
- Expose approval, admit, and deny controls on `Call`.
- Expose raised-hand and screen-share callbacks and local controls.
- Preserve raised-hand state in the public group roster.
- Reuse existing group relay, key epoch, Opus, H.264, reaction app-data, and
  participant-attributed multi-video paths after admission.
- Expose `Call.RingParticipant` separately from `Call.AddParticipant`.
- Preserve arbitrary valid UTF-8 emoji in RTC app-data reactions.

### Browser test console

- Start ad-hoc group calls or group-JID-bound calls.
- Add multiple people to an active call.
- Render the authoritative participant roster, including disconnected states,
  selected endpoint counts, raised hands, and per-participant Ring actions.
- Create, preview, and join audio/video call links.
- Show pending waiting-room users and admit or deny them individually.
- Toggle approval requirements.
- Raise/lower hand and render participant hand state.
- Send fixed or arbitrary emoji reactions.
- Start display capture and send independent screen-share signaling; stopping
  sharing restores camera capture without ending the call or downgrading video.

## Public API summary

```go
call, err := client.GroupCallWithOptions(
	ctx,
	[]string{"+15551230001", "+15551230002"},
	meowcaller.GroupCallOptions{Video: true},
)
boundCall, err := client.GroupCallByIDWithOptions(
	ctx,
	"123456789@g.us",
	meowcaller.GroupCallOptions{Video: true},
)
inviteResults := call.AddParticipants(ctx, "+15551230003", "+15551230004")
err = call.RingParticipant(ctx, "74170125783269@lid")

link, err := client.CreateCallLink(ctx, meowcaller.CallLinkOptions{Video: true})
preview, err := client.PreviewCallLink(ctx, link.URL, meowcaller.CallLinkOptions{Video: true})
call, err := client.JoinCallLink(ctx, link.URL, meowcaller.CallLinkOptions{Video: true})

call.OnWaitingRoomState(func(state meowcaller.WaitingRoomState) {})
call.SetApprovalRequired(ctx, true)
call.AdmitParticipant(ctx, "242653052539031@lid")
call.DenyParticipant(ctx, "242653052539031@lid")

call.SetHandRaised(true)
call.OnHandRaise(func(state meowcaller.HandRaiseState) {})

screenShareID := uint32(1)
call.StartScreenShare(&screenShareID)
call.OnScreenShare(func(state meowcaller.ScreenShareState) {})
call.StopScreenShare()

call.SendReaction("🧑🏽‍💻")
```

Whatsmeow exposes the corresponding lower-level methods using
`types.CallLinkMedia`, `types.CallScreenShareState`, and typed events in
`types/events`.

## Authoritative capture evidence

| Capture | SHA-256 | What it establishes |
|---|---|---|
| `group-call-add-people-v2-20260723-112208.jsonl` | `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6` | Add-person signaling and direct-to-group upgrade. |
| `custom-reaction-video-screen-share-v2-20260726-033309.jsonl` | `99cd42822715706d42bb1e672c1f13323154f7fe69cf01e589aef60d212b5631` | Arbitrary emoji app-data, video upgrade, screen-share start/stop, and camera restoration. |
| `group-mobile-hand-raise-screen-share-v2-20260726-033924.jsonl` | `9ac05ee3161370020172ffeb5e4f5b3b066dcf049fdb1372cbc62e6e1e2e7248` | Raise/lower hand signaling, group screen-share ID, H.264 stream role 2, and leave-driven stop. |
| `group-unanswered-ringing-v2-20260726-035236.jsonl` | `218d30b5775d6b4ccdb666c2a6302f4e72ff17a135da39cb35efb3ba275b9a64` | Ring, timeout, self-rejoin, and re-ring of an existing non-connected participant. |
| `call-link-e2e-v2-20260726-041034.jsonl` | `48bd3e71b3da3e3a0fcc954597de4f80208b4636f7ab4a5dabe1f7b7f8b16943` | Link create/query/join, approval enable, waiting heartbeat, deny, rejoin, admit, relay, and rekey. |

The compact analyses and indexes live under `diag/analysis/`. Raw JSONL files
remain unchanged.

## Observed facts

- A link token is reusable and maps to a new active call ID after the previous
  active session ends.
- Waiting-room operations and heartbeats route to `CALL_ID@call`.
- Admission resumes through ordinary group update, relay, and key-epoch
  machinery; there is no separate lobby media transport.
- Raised hand is persistent `user_action` signaling, not an emoji reaction.
- Reactions are transient version-2 RTP app-data and preserve the selected
  Unicode string.
- Screen-share state is independent from camera video state. State 1 starts,
  state 2 stops, and stop does not end or downgrade the call.
- A participant leaving clears their active screen share even without a state-2
  stanza.
- Both captures advertise `is_dual_stream_ss_enabled=false`. Screen capture
  replaces camera capture on the ordinary primary H.264 SSRC; it does not create
  a simultaneous second media stream.
- The direct capture proves RTP continuity across camera → display → camera:
  SSRC `0x22E5F501` continues from sequence 137 to 138 at screen-share start and
  from 428 to 429 when camera resumes.
- WhatsApp precomputes screen-share SSRC families, but neither capture attaches
  those families to transport. `screen_share_id=1` therefore must not select a
  second sender in the captured non-dual-stream mode.
- Ring and re-ring use the same singular active-call offer family as adding a
  person. The call ID stays unchanged, destinations are the target's devices,
  and `group_info` contains connected participants while excluding the target.

## Inferences and current evidence boundaries

- Approval disable uses the symmetric captured toggle envelope with
  `enabled="0"`; the end-to-end capture contains only the enable operation.
- Audio call-link builders use the observed shared topology with `media="audio"`
  and omit the video child; the end-to-end link capture is video.
- Outgoing group raised-hand and screen-share controls route through
  `CALL_ID@call`, matching all other group-wide controls. The mobile capture
  observes receiver-side server fanout, not the sender's original destination.
- The meaning of `screen_share_id=1` and the dormant precomputed screen-share
  families remains unknown. A future capture with
  `is_dual_stream_ss_enabled=true` is required before enabling a second sender.
- There is no separate participant “ping” stanza in the ring capture. Relay
  ping/consent traffic is transport keepalive and is not a user-facing ring.
- A scheduled-call button is messaging-layer work: it sends an
  `EventMessage` containing the call link. It is not call signaling and is not
  exposed by this VoIP example yet.

## Verification

```bash
cd /Users/purpshell/Documents/Programming/whatsmeow-voip-refresh
go test ./... -count=1

cd /Users/purpshell/Documents/Programming/meowcaller-thin-rebuild
go test ./... -count=1

cd /Users/purpshell/Documents/Programming/meowcaller-thin-rebuild/examples/web
go test ./... -count=1
```

The browser console is the end-to-end test surface. A waiting participant should
remain in `CallPhaseWaitingRoom` with no media devices attached, transition to
connecting only after admission, then enter the existing group media path after
relay and key readiness.
