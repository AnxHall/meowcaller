package meowcaller

import (
	"bytes"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestParticipantVideoFrameDispatchPreservesIdentityAndOwnsBytes(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L32-L54
	call := &Call{}
	var got ParticipantVideoFrame
	call.OnParticipantVideoFrame(func(frame ParticipantVideoFrame) {
		got = frame
	})
	accessUnit := []byte{0, 0, 0, 1, 0x65, 1, 2, 3}
	want := ParticipantVideoFrame{
		ParticipantID: "333333333333333:43@lid",
		Sender:        types.NewJID("333333333333333", types.HiddenUserServer),
		Device:        types.NewADJID("333333333333333", 0, 43),
		PID:           7,
		HasPID:        true,
		SSRC:          0x12345678,
		Orientation:   3,
		AccessUnit:    accessUnit,
	}
	call.dispatchParticipantVideoFrame(want)
	accessUnit[4] = 0

	if got.ParticipantID != want.ParticipantID || got.Sender != want.Sender ||
		got.Device != want.Device || got.PID != want.PID || !got.HasPID ||
		got.SSRC != want.SSRC || got.Orientation != want.Orientation {
		t.Fatalf("participant frame metadata = %+v, want %+v", got, want)
	}
	if !bytes.Equal(got.AccessUnit, []byte{0, 0, 0, 1, 0x65, 1, 2, 3}) {
		t.Fatalf("participant frame bytes aliased media buffer: %x", got.AccessUnit)
	}
}

func TestParticipantVideoFrameDispatchCanBeDisabled(t *testing.T) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/36d54857c74e45ccb08f6444a32d2afa13f20be9/datasheets/group-video-reactions.md#L32-L54
	call := &Call{}
	calls := 0
	call.OnParticipantVideoFrame(func(ParticipantVideoFrame) { calls++ })
	call.OnParticipantVideoFrame(nil)
	call.dispatchParticipantVideoFrame(ParticipantVideoFrame{AccessUnit: []byte{1}})
	if calls != 0 {
		t.Fatalf("disabled participant callback ran %d times", calls)
	}
}
