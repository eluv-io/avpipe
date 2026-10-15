package goavpipe

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eluv-io/common-go/format/duration"
	"github.com/eluv-io/common-go/media/rtp"
)

func TestXcParamsJSONOmitsVerticalDataReader(t *testing.T) {
	p := XcParams{
		Vertical:           1,
		VerticalDataReader: io.NopCloser(bytes.NewReader([]byte{1, 2, 3, 4})),
	}

	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.NotContains(t, string(data), "vertical_data_reader")
	require.NotContains(t, string(data), "AQIDBA==")
}

// TestInputProcessorConfig_ApplyDefaults_ReorderBufferEnabled verifies that ApplyDefaults resolves ReorderBuffer's
// own defaults too, when enabled - not just every other InputProcessorConfig field. Without this, anything
// inspecting the config right after ApplyDefaults (logging, a status endpoint) would see zero values next to
// Enabled: true, even though custom.go/bypass.go separately default it again before actually using it.
func TestInputProcessorConfig_ApplyDefaults_ReorderBufferEnabled(t *testing.T) {
	i := InputProcessorConfig{
		ReorderBuffer: ReorderBufferConfig{Enabled: true},
	}.ApplyDefaults()

	require.EqualValues(t, rtp.DefaultReorderMaxWindow, i.ReorderBuffer.MaxWindow)
	require.EqualValues(t, duration.Spec(rtp.DefaultReorderMaxWait), i.ReorderBuffer.MaxWait)
	require.EqualValues(t, 4*rtp.DefaultReorderMaxWindow, i.ReorderBuffer.MaxJump)
}

// TestInputProcessorConfig_ApplyDefaults_ReorderBufferDisabled verifies that ApplyDefaults leaves ReorderBuffer
// untouched when it is disabled (the default) - defaulting an unused config would be pointless work, and could
// mask a caller's own accidental zero values if Enabled is flipped on later without re-running ApplyDefaults.
func TestInputProcessorConfig_ApplyDefaults_ReorderBufferDisabled(t *testing.T) {
	i := InputProcessorConfig{}.ApplyDefaults()

	require.Zero(t, i.ReorderBuffer.MaxWindow)
	require.Zero(t, i.ReorderBuffer.MaxWait)
	require.Zero(t, i.ReorderBuffer.MaxJump)
}
