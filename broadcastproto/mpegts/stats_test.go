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
// MpegtsPacketProcessor.RegisterPacketsDropped - exportStats dereferences it unconditionally.
func newTestTSStats() *TSStats {
	ts := NewTSStats()
	ts.PacketsDropped = &atomic.Uint64{}
	return ts
}

// TestExportedStats_StringRequiresPointer documents why PushStats must log &exportStats rather than exportStats:
// String() has a pointer receiver, so only *ExportedStats satisfies fmt.Stringer. Passing the value (the original
// bug) silently falls back to Go's native struct formatting instead of the intended JSON.
func TestExportedStats_StringRequiresPointer(t *testing.T) {
	ts := newTestTSStats()
	stats := exportStats(ts, &RTPStats{}, nil, nil)

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
	stats := exportStats(ts, &RTPStats{}, &tracker.Stats{Packets: 42, Bytes: 1234}, srt)

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
	stats := exportStats(newTestTSStats(), &RTPStats{}, nil, nil)

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
	stats := exportStats(ts, rtp, nil, &mio.SrtConnStats{Version: 5})

	bb, err := json.Marshal(stats)
	require.NoError(t, err)
	require.True(t, json.Valid(bb))

	var roundTripped ExportedStats
	require.NoError(t, json.Unmarshal(bb, &roundTripped))
	require.Equal(t, stats, roundTripped)
}

// TestExportedStats_CopyInto_DetachesStream is a regression test for the data race this whole reuse design would
// otherwise permit: a caller retaining an ExportedStats past a single Stat call (e.g. content-fabric's
// live-recorder) must not alias Stream, since MpegtsPacketProcessor mutates it in place on later PushStats calls.
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
	require.Same(t, src.Srt, dst.Srt, "Srt is never reused/mutated in place, so a plain pointer assignment is correct")
	require.NotNil(t, dst.Stream)
	require.NotSame(t, src.Stream, dst.Stream, "CopyInto must not alias the original *tracker.Stats")
	require.EqualValues(t, 10, dst.Stream.Packets)

	// Simulate MpegtsPacketProcessor reusing/mutating its Stream on the next PushStats call.
	src.Stream.Packets = 999
	src.Stream.Clocks[0].Samples = 999

	require.EqualValues(t, 10, dst.Stream.Packets, "dst must be unaffected by mutating the source afterward")
	require.EqualValues(t, 5, dst.Stream.Clocks[0].Samples)
}

// TestExportedStats_CopyInto_ReusesDestinationStream verifies CopyInto reuses dst.Stream in place (the whole point
// of Snapshot-based reuse) rather than allocating a new one when the destination already has one.
func TestExportedStats_CopyInto_ReusesDestinationStream(t *testing.T) {
	src := ExportedStats{Stream: &tracker.Stats{Packets: 1}}
	dst := ExportedStats{Stream: &tracker.Stats{Packets: 999}}
	existing := dst.Stream

	src.CopyInto(&dst)

	require.Same(t, existing, dst.Stream, "CopyInto must reuse dst's existing *tracker.Stats, not allocate a new one")
	require.EqualValues(t, 1, dst.Stream.Packets)
}

// TestExportedStats_CopyInto_NilStream verifies CopyInto clears dst.Stream (rather than panicking or leaving a
// stale value) when the source has no stream - e.g. a non-SRT/non-mpegts source, or before the first stats push.
func TestExportedStats_CopyInto_NilStream(t *testing.T) {
	dst := ExportedStats{Stream: &tracker.Stats{Packets: 999}}

	var src ExportedStats
	src.CopyInto(&dst)

	require.Nil(t, dst.Stream)
}
