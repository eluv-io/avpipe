package mpegts

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	mio "github.com/eluv-io/common-go/media/io"
	"github.com/eluv-io/common-go/media/tracker"
)

// newTestTSStats returns a TSStats with PacketsDropped registered, matching what every production caller does via
// MpegtsPacketProcessor.RegisterPacketsDropped - ExportedStats.populate dereferences it unconditionally.
func newTestTSStats() *TSStats {
	ts := NewTSStats()
	ts.PacketsDropped = &atomic.Uint64{}
	return ts
}

// TestExportedStats_StringRequiresPointer documents why PushStats must log its ExportedStats destination by
// pointer rather than by value: String() has a pointer receiver, so only *ExportedStats satisfies fmt.Stringer.
// Passing the value (the original bug) silently falls back to Go's native struct formatting instead of the
// intended JSON.
func TestExportedStats_StringRequiresPointer(t *testing.T) {
	ts := newTestTSStats()
	var stats ExportedStats
	stats.populate(ts, &RTPStats{})

	_, ok := any(stats).(fmt.Stringer)
	require.False(t, ok, "ExportedStats value must not implement fmt.Stringer")
	t.Log(stats)

	_, ok = any(&stats).(fmt.Stringer)
	require.True(t, ok, "*ExportedStats must implement fmt.Stringer")
	t.Log(&stats)
}

// TestExportedStats_FmtSprintProducesValidJSON confirms String() is called from fmt.Sprint() and renders as JSON, not Go's
// native "%v" struct format.
func TestExportedStats_FmtSprintProducesValidJSON(t *testing.T) {
	ts := newTestTSStats()
	srt := &mio.SrtConnStats{Version: 5, Encrypted: true}
	stats := ExportedStats{Stream: &tracker.Stats{Packets: 42, Bytes: 1234}, Srt: srt}
	stats.populate(ts, &RTPStats{})

	s := fmt.Sprint(&stats)

	require.True(t, json.Valid([]byte(s)), "String() output must be valid JSON, got: %s", s)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &decoded))

	tsFields, ok := decoded["ts"].(map[string]any)
	require.True(t, ok, "decoded JSON must have a \"ts\" object")
	require.EqualValues(t, 42, tsFields["packets_received"])
	require.EqualValues(t, 1234, tsFields["bytes_received"])

	srtFields, ok := decoded["srt"].(map[string]any)
	require.True(t, ok, "decoded JSON must have an \"srt\" object when the source is SRT")
	require.EqualValues(t, 5, srtFields["version"])
	require.EqualValues(t, true, srtFields["encrypted"])
}

// TestExportedStats_SrtOmittedForNonSrtSource verifies the "srt" field is absent entirely (not just null) for a
// non-SRT source, so a dashboard consuming this JSON can key its SRT-specific UI off the field's presence.
func TestExportedStats_SrtOmittedForNonSrtSource(t *testing.T) {
	var stats ExportedStats
	stats.populate(newTestTSStats(), &RTPStats{})

	bb, err := json.Marshal(stats)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(bb, &decoded))
	require.NotContains(t, decoded, "srt")
}

// TestExportedStats_MarshalJSON confirms ExportedStats marshals to valid, round-trippable JSON on its own, independent
// of String().
func TestExportedStats_MarshalJSON(t *testing.T) {
	ts := newTestTSStats()
	ts.PacketsWritten.Store(7)
	rtp := &RTPStats{}
	rtp.BadPackets.Store(99)
	stats := ExportedStats{Srt: &mio.SrtConnStats{Version: 5}}
	stats.populate(ts, rtp)

	bb, err := json.Marshal(stats)
	require.NoError(t, err)
	require.True(t, json.Valid(bb))

	var roundTripped ExportedStats
	require.NoError(t, json.Unmarshal(bb, &roundTripped))
	require.Equal(t, stats, roundTripped)
}

// TestExportedStats_CopyInto_DetachesStream is a regression test for the data race this whole reuse design would
// otherwise permit: a caller retaining an ExportedStats past a single Stat call (e.g. content-fabric's
// live-recorder) must not alias Stream or Srt, since MpegtsPacketProcessor mutates both in place on later PushStats
// calls (Srt via connStatsSource.ConnStats's copy-into contract - see NetReader.ConnStats).
func TestExportedStats_CopyInto_DetachesStream(t *testing.T) {
	src := ExportedStats{
		TS:     ExportedTSStats{PacketsWritten: 7},
		RTP:    ExportedRTPStats{BadPackets: 3},
		Stream: &tracker.Stats{Packets: 10, Clocks: []tracker.ClockStats{{Source: "rtp", Samples: 5}}},
		Srt:    &mio.SrtConnStats{Version: 1},
	}

	var dst ExportedStats
	src.CopyInto(&dst)

	require.Equal(t, src.TS, dst.TS)
	require.Equal(t, src.RTP, dst.RTP)
	require.NotNil(t, dst.Srt)
	require.NotSame(t, src.Srt, dst.Srt, "CopyInto must not alias the original *mio.SrtConnStats")
	require.Equal(t, uint32(1), dst.Srt.Version)
	require.NotNil(t, dst.Stream)
	require.NotSame(t, src.Stream, dst.Stream, "CopyInto must not alias the original *tracker.Stats")
	require.EqualValues(t, 10, dst.Stream.Packets)

	// Simulate MpegtsPacketProcessor reusing/mutating its Stream and Srt on the next PushStats call.
	src.Stream.Packets = 999
	src.Stream.Clocks[0].Samples = 999
	src.Srt.Version = 999

	require.EqualValues(t, 10, dst.Stream.Packets, "dst must be unaffected by mutating the source afterward")
	require.EqualValues(t, 5, dst.Stream.Clocks[0].Samples)
	require.Equal(t, uint32(1), dst.Srt.Version, "dst must be unaffected by mutating the source afterward")
}

// TestExportedStats_CopyInto_ReusesDestinationStream verifies CopyInto reuses dst.Stream/dst.Srt in place (the whole
// point of Snapshot-/ConnStats-based reuse) rather than allocating new ones when the destination already has them.
func TestExportedStats_CopyInto_ReusesDestinationStream(t *testing.T) {
	src := ExportedStats{Stream: &tracker.Stats{Packets: 1}, Srt: &mio.SrtConnStats{Version: 1}}
	dst := ExportedStats{Stream: &tracker.Stats{Packets: 999}, Srt: &mio.SrtConnStats{Version: 999}}
	existingStream, existingSrt := dst.Stream, dst.Srt

	src.CopyInto(&dst)

	require.Same(t, existingStream, dst.Stream, "CopyInto must reuse dst's existing *tracker.Stats, not allocate a new one")
	require.EqualValues(t, 1, dst.Stream.Packets)
	require.Same(t, existingSrt, dst.Srt, "CopyInto must reuse dst's existing *mio.SrtConnStats, not allocate a new one")
	require.EqualValues(t, 1, dst.Srt.Version)
}

// TestExportedStats_CopyInto_NilStream verifies CopyInto clears dst.Stream/dst.Srt (rather than panicking or leaving
// a stale value) when the source has neither - e.g. before the first stats push, or a non-SRT source for Srt alone.
func TestExportedStats_CopyInto_NilStream(t *testing.T) {
	dst := ExportedStats{Stream: &tracker.Stats{Packets: 999}, Srt: &mio.SrtConnStats{Version: 999}}

	var src ExportedStats
	src.CopyInto(&dst)

	require.Nil(t, dst.Stream)
	require.Nil(t, dst.Srt)
}

// TestExportedStats_Clone_DetachesEvenWhenSourceWasShallowCopied is a regression test for the bug CopyInto alone
// permits when the caller's destination came from a shallow struct copy of the source (e.g. content-fabric's
// recPeriodStatusReport.Clone doing `o := *r` before detaching InputStats): dst.Stream would already alias
// src.Stream, so CopyInto would mistake that alias for a genuine, independent destination to reuse and copy the
// source into itself - leaving the "clone" aliased after all. Clone sidesteps this entirely by always copying into
// a fresh, zero-value ExportedStats, never an existing destination that might itself be the alias.
func TestExportedStats_Clone_DetachesEvenWhenSourceWasShallowCopied(t *testing.T) {
	src := ExportedStats{Stream: &tracker.Stats{Packets: 10}}
	shallow := src // mirrors `o := *r`: shallow.Stream aliases src.Stream

	shallow = src.Clone()

	require.NotSame(t, src.Stream, shallow.Stream, "Clone must not alias the original *tracker.Stats")
	require.EqualValues(t, 10, shallow.Stream.Packets)

	src.Stream.Packets = 999
	require.EqualValues(t, 10, shallow.Stream.Packets, "clone must be unaffected by mutating the source afterward")
}
