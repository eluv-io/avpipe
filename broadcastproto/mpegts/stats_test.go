package mpegts

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

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
	ts.BytesReceived.Store(1234)
	stats := exportStats(ts, &RTPStats{}, nil)

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
	ts.BytesReceived.Store(1234)
	stats := exportStats(ts, &RTPStats{}, &tracker.Stats{Packets: 42})

	s := fmt.Sprint(&stats)

	require.True(t, json.Valid([]byte(s)), "String() output must be valid JSON, got: %s", s)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &decoded))

	tsFields, ok := decoded["ts"].(map[string]any)
	require.True(t, ok, "decoded JSON must have a \"ts\" object")
	require.EqualValues(t, 42, tsFields["packets_received"])
	require.EqualValues(t, 1234, tsFields["bytes_received"])
}

// TestExportedStats_MarshalJSON confirms ExportedStats marshals to valid, round-trippable JSON on its own, independent
// of String().
func TestExportedStats_MarshalJSON(t *testing.T) {
	ts := newTestTSStats()
	ts.PacketsWritten.Store(7)
	rtp := &RTPStats{}
	rtp.LastSeqNum.Store(99)
	stats := exportStats(ts, rtp, nil)

	bb, err := json.Marshal(stats)
	require.NoError(t, err)
	require.True(t, json.Valid(bb))

	var roundTripped ExportedStats
	require.NoError(t, json.Unmarshal(bb, &roundTripped))
	require.Equal(t, stats, roundTripped)
}
