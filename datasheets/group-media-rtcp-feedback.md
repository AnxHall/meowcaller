# Datasheet: `media/group_rtcp_feedback`

Participant-indexed RTCP reception feedback for ad-hoc group-call audio.

**Validation vector:** focused deterministic Go KATs using the independently
observed group audio SSRCs and sequence spans below.

**Reference pinned at:**

- capture SHA-256 `9d6463714430c55ddb3ccb95e153f1d06d11a1feea7a153d1ea95f39f48b6889`
- capture version `wa-voip-diag/v2`
- RTCP reception-state commit `d37b1756d05fb34c9b6c2410c48dd20d27394929`

The capture was approved by the human reviewer as authoritative on 2026-07-24.

## Reference source (verbatim — authoritative)

The immutable raw JSONL file
`diag/captures/whatsapp-20260724-222532.jsonl` remains the verbatim authority.
Its compact derived report
`diag/analysis/group-call-two-sided-add-v2-20260724-222532.md` records:

```text
A -> C primary audio SSRC 0x59754A60, received sequence 299-323
C -> A primary audio SSRC 0x66FDE7F1, sent sequence 39-76
B -> A primary audio SSRC 0x63C8EB02, received sequence 1-168
Observed RTCP includes SR, SDES, BYE, PSFB, and WhatsApp extended packet types.
```

The capture therefore proves that one endpoint can receive distinct,
independently sequenced audio streams from multiple participants during one
call. The pinned RTCP reception implementation defines one
`RtcpReceptionStats` instance as the state for one inbound SSRC: changing its
SSRC resets sequence, loss, jitter, and sender-report timing.

The existing byte-verified one-report Sender Report plus SDES packet remains the
wire authority. The capture does not establish the byte layout of a compound
Sender Report containing multiple reception blocks.

## Go envelope (signatures only)

```go
package rtp

type RtcpReceptionStatsSet struct {
	// Internal synchronized SSRC-to-reception-state map.
}

func (s *RtcpReceptionStatsSet) Observe(
	ssrc uint32,
	sequence uint16,
	rtpTimestamp uint32,
	arrivalMs uint64,
	clockRate uint32,
)

func (s *RtcpReceptionStatsSet) ObserveSenderReport(
	senderSSRC uint32,
	ntpSeconds uint32,
	ntpFraction uint32,
	arrivalMs uint64,
)

func (s *RtcpReceptionStatsSet) Reports(nowMs uint64) []*RtcpReceptionReport
```

The live engine emits one already-verified single-reception-block SRTCP Sender
Report for every returned audio report. When the set is empty, it emits the
existing report without a reception block.

## Implementation suggestions (guidance, not authoritative)

- Allocate one `RtcpReceptionStats` per first-seen authenticated SSRC.
- Never route one SSRC through another SSRC's sequence, loss, jitter, or
  sender-report timing state.
- Return snapshots in ascending SSRC order so output and KATs are deterministic.
- Feed an inbound Sender Report only to the state matching its sender SSRC.
- Reuse the existing verified one-reception-block SR+SDES builder per report;
  do not invent an unobserved multiple-block WhatsApp extension layout.
- Continue emitting a no-reception-block Sender Report before any inbound audio
  has been authenticated.
