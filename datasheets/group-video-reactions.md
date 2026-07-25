# Group video and reactions

**Status:** initial group-video signaling accepted live; encoded media validation pending

**Reference pinned at:** UNMAPPED — WhatsApp Web group-video signaling was not
present in the archived group-call captures.

## Observed facts

- Group roster devices carry independent audio, video, and app-data SSRCs.
- The installed group key epoch derives and installs participant-specific receive
  pipelines for all three media kinds.
- Authenticated video and app-data packets can therefore be attributed to the
  participant device selected by their SSRC.
- Captured RTC app-data reaction payloads contain a monotonically increasing
  transaction ID and an emoji. Sender identity comes from the authenticated
  participant media pipeline, not from the protobuf payload.
- Whatsmeow's verified direct-call offer places the H.264 `video` capability after
  the audio capabilities and before `net`.
- On 2026-07-26, initial video group call
  `E9707AFBD7A2DCD45B4BAA54EAB1017F` was accepted by WhatsApp. All three
  participants reached connected PID-backed roster state, the call reached media
  ready with `video=true`, and peers exchanged enabled/orientation video-state
  stanzas. This validates initial group-video signaling and the offer's H.264
  capability placement.
- Outgoing H.264 media did not flow in that run because the web example attempted
  1920×1080 with AVC Baseline Level 3.1. The browser rejected the encoder
  configuration before it produced a frame.

## Inferences to validate live

- A singular add-person offer for an active video call carries that same `video`
  child and advertises the video capability to the invited device.
- A participant added to a video call joins the existing shared group relay/key
  epoch and receives subsequent group updates rather than negotiating a separate
  media session.

The remaining inferences must stay marked unvalidated until a live add-person video
call proves the offer is accepted and bidirectional video flows for original and
added participants.

## Go envelope

```go
type GroupCallOptions struct {
	GroupJID string
	Video    bool
}

type ParticipantVideoFrame struct {
	ParticipantID string
	Sender        types.JID
	Device        types.JID
	PID           uint32
	HasPID        bool
	SSRC          uint32
	Orientation   int
	AccessUnit    []byte
}

func (c *Call) OnParticipantVideoFrame(func(ParticipantVideoFrame))
```

The existing `ReceiveVideo` sink remains source-compatible and continues receiving
all access units. The participant callback is the group-aware surface and receives
an owned copy so callers cannot retain or mutate the media loop's buffer.

## Web example target

- Start either an audio or video group call from the selector.
- Keep one H.264 decoder and canvas per authenticated participant identity.
- Route reactions to the sender's participant tile, with a shared fallback before
  that participant has a tile.
- Preserve the existing direct-call canvas and reaction behavior.

## Validation

- Builder tests must prove exact child order and video capability substitution.
- Call-state tests must prove group calls and video add-person offers retain video
  state.
- Media tests must prove participant metadata and frame bytes survive dispatch
  without aliasing.
- Web tests must prove tagged participant video messages and sender-attributed
  reaction rendering are present.
- Initial group-video signaling is live-accepted. Bidirectional H.264, video
  add-person acceptance, and live group reactions remain pending.
