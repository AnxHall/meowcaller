package rtp

import "sync"

// RtcpReceptionStatsSet retains independent RTCP reception state per inbound
// media SSRC.
type RtcpReceptionStatsSet struct {
	mu      sync.Mutex
	streams map[uint32]*RtcpReceptionStats
}

// Observe records one authenticated RTP packet in its SSRC-specific state.
func (s *RtcpReceptionStatsSet) Observe(
	ssrc uint32,
	sequence uint16,
	rtpTimestamp uint32,
	arrivalMs uint64,
	clockRate uint32,
) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/7594217b4386a1c056d0e3ecd1049b30a1101241/datasheets/group-media-rtcp-feedback.md#L30-L38
	// TODO
	// agent suggestion: lazily allocate one RtcpReceptionStats per SSRC and
	// delegate the packet without holding the set lock during stream mutation.
	// human input:
}

// ObserveSenderReport records sender timing in the matching SSRC-specific state.
func (s *RtcpReceptionStatsSet) ObserveSenderReport(
	senderSSRC uint32,
	ntpSeconds uint32,
	ntpFraction uint32,
	arrivalMs uint64,
) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/7594217b4386a1c056d0e3ecd1049b30a1101241/datasheets/group-media-rtcp-feedback.md#L30-L38
	// TODO
	// agent suggestion: look up the exact sender SSRC and ignore reports for
	// streams that have not produced authenticated RTP.
	// human input:
}

// Reports snapshots every tracked stream in ascending SSRC order.
func (s *RtcpReceptionStatsSet) Reports(nowMs uint64) []*RtcpReceptionReport {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/7594217b4386a1c056d0e3ecd1049b30a1101241/datasheets/group-media-rtcp-feedback.md#L64-L69
	// TODO
	// agent suggestion: copy the stream pointers under lock, sort their SSRC
	// keys, then snapshot each independent tracker.
	// human input:
	return nil
}
