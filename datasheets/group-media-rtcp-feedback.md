# Datasheet: `media/group_rtcp_feedback`

Participant-indexed RTCP reception feedback for ad-hoc group-call audio.

**Validation vector:** authenticated, decrypted, sanitized RTCP plaintext from
the immutable two-sided capture, plus focused deterministic Go KATs using the
independently observed group audio SSRCs and sequence spans below.

**Reference pinned at:**

- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
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

The add-people capture records a direct-call sender report at raw JSONL line
2181 and a post-group-commit sender report at line 7079. The two-sided capture
independently records the same transition at direct line 7846 and group lines
16751, 16841, and 16929:

```text
add-people line 2181: 122 protected bytes, first bytes 81 c8 00 12
add-people line 7079: 74 protected bytes, first bytes 81 c8 00 0e
two-sided line 7846:  122 protected bytes, first bytes 81 c8 00 12
two-sided line 16751: 74 protected bytes, first bytes 81 c8 00 0e
two-sided line 16841: 74 protected bytes, first bytes 81 c8 00 0e
two-sided line 16929: 74 protected bytes, first bytes 81 c8 00 0e
```

The SRTCP trailer is 14 bytes, so every group packet carries exactly 60 bytes
of plaintext. Each group packet authenticated under the capture's transaction
16 raw media epoch and the normalized device-qualified sender identity. The
raw epoch, derived keys, ciphertext, and authentication tag are intentionally
omitted from this sanitized vector.

The authenticated plaintexts are:

```text
81c8000e59754a60ee0e4948176163a50007d6ca000001310000bbe266fde7f100000000000000310000000000000000000000000000019000004650
81c8000e66fde7f1ee0e494991a0cd7b0002d6cf000000330000197459754a600000000000000133000000004948176100002c5300000a1e00004a6d
81c8000e66fde7f1ee0e494a2e4ae3750002fdb40000003c000019ce59754a60000000000000013800000000494817610000c8fd0000101d00004e72
```

All three have one SR report (`81 c8`), RTCP length 14 (60 bytes), the normal
28-byte sender section, one 24-byte RFC 3550 reception block, and an opaque
8-byte WhatsApp extension. There is no SDES packet. The extension is non-zero
and changes over time; this capture proves its placement and exact bytes but
does not name its two fields or establish how to calculate them.

The earlier direct packet has RTCP length 18 for a 76-byte SR section, followed
by a 32-byte SDES section and the 14-byte SRTCP trailer. Reusing that 1:1
`SR + 24-byte WhatsApp extension + SDES` builder for group audio therefore
produces the wrong 122-byte wire shape.

Reception reports exist in both phases, so their presence is not a group-mode
signal. The live sender must retain the direct 108-byte plaintext / 122-byte
protected form until a newer group roster transaction has committed
successfully. A validation failure, external relay-allocation failure, or stale
transaction does not cross that boundary. After the commit, each non-empty
audio reception report uses the 60-byte group form.

The capture does not show one sender reporting two simultaneous remote SSRCs in
one RTCP packet. Emitting one 60-byte group report per active remote SSRC remains
an explicit inference until a multi-receiver capture or live validation proves
whether native WhatsApp instead aggregates multiple blocks.

## Go envelope (signatures only)

```go
package rtp

type RtcpReceptionStatsSet struct {
	// Internal synchronized SSRC-to-reception-state map.
}

type RTCPGroupReportExtension [8]byte

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

func (s *RtcpReceptionStatsSet) Retain(ssrcs []uint32)

func (s *RtcpReceptionStatsSet) Reports(nowMs uint64) []*RtcpReceptionReport

func BuildGroupSenderReport(
	localSSRC uint32,
	stats *RtcpSenderStats,
	nowMs uint64,
	report *RtcpReceptionReport,
	extension RTCPGroupReportExtension,
) []byte
```

Before a committed group roster exists, the live engine emits one direct
108-byte `SR + SDES` plaintext even when its direct peer has produced a
reception report. After a successful accepted group-roster commit, it emits one
60-byte group Sender Report for every returned audio report. The current engine
has no authoritative source for the opaque extension values, so it uses the
all-zero unavailable representation while preserving the observed eight-byte
slot. This is an assumption: a peer that requires non-zero values or a specific
calculation invalidates it.

When the set is empty, the engine retains the existing 1:1 baseline
`SR + SDES` report. No capture proves the empty group-call report shape, so that
compatibility behavior also remains an assumption.

## Implementation suggestions (guidance, not authoritative)

- Allocate one `RtcpReceptionStats` per first-seen authenticated SSRC.
- Never route one SSRC through another SSRC's sequence, loss, jitter, or
  sender-report timing state.
- Return snapshots in ascending SSRC order so output and KATs are deterministic.
- Feed an inbound Sender Report only to the state matching its sender SSRC.
- Before the first authoritative roster commit, accept first-seen authenticated
  SSRCs for direct-call compatibility. On every later authoritative roster
  update, atomically replace the allowed SSRC set and retain only those active
  remote primary-audio SSRCs. Observations outside that set must not recreate a
  departed stream; a later roster may re-allow it, and its next observation
  begins a fresh report interval.
- Select the wire builder from committed group-media state, not from whether
  reception reports are present. Preserve one direct 108/122-byte report until
  the roster transaction's infallible commit, then use the group builder.
- Encode each non-empty group report as the observed single 60-byte SR: one RFC
  block, exactly eight extension bytes, and no SDES.
- Keep the eight extension bytes opaque. Use zero as the unavailable live value
  until another capture establishes their meaning.
- Continue emitting a no-reception-block Sender Report before any inbound audio
  has been authenticated, but keep that boundary marked unverified.
- If protecting or sending one report fails, record the error and retry on the
  next periodic tick. Never reuse an SRTCP index: a protected-but-unsent packet
  consumes its index, and the next attempt uses fresh sender state and a fresh
  index. A partial tick may therefore deliver early participant reports and
  retry all active participants on the next tick.

## Verification boundary

The exact sanitized plaintext KAT proves the 60-byte group builder, header,
field order, opaque extension placement, and absence of SDES. Focused tests
also prove the capture-observed 122-to-74-byte committed transition, independent
state, authoritative pruning under interleaving observations, leave/rejoin
reset, SRTCP protection length, and retryable first/partial send failures.

The module remains `partial`: the opaque extension calculation, empty-set group
shape, multi-report aggregation policy, and peer acceptance of zero extension
bytes require another authoritative capture or live end-to-end validation.
