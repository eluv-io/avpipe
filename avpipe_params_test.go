package avpipe

import (
	"testing"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/require"
)

func TestDeinterlaceOutputParams(t *testing.T) {
	for _, format := range []string{"dash", "hls", "fmp4-segment"} {
		for _, mode := range []int{0, 1, 2} {
			for _, timescale := range []int{0, 12800} {
				params := goavpipe.NewXcParams()
				params.Url = "missing-deinterlace-validation-input.ts"
				params.Format, params.XcType = format, goavpipe.XcVideo
				params.Deinterlace, params.VideoTimeBase = mode, timescale
				params.VideoSegDurationTs = 491520
				params.SegDuration = ""

				// XcInit must validate without opening or decoding the missing input.
				handle, err := XcInit(params)
				if format != "fmp4-segment" && (mode != 0 || timescale > 0) {
					require.ErrorIs(t, err, EAV_PARAM, "format=%s mode=%d timescale=%d", format, mode, timescale)
					require.Equal(t, int32(-1), handle)
					continue
				}
				require.NoError(t, err, "format=%s mode=%d timescale=%d", format, mode, timescale)
				require.NoError(t, XcFini(handle))
			}
		}
	}
}

func TestVideoSegmentDurationParams(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timescale int
		ticks     int64
		seconds   string
	}{
		{"ticks_override_seconds", 60000, 2702700, "30.03"},
		{"matching", 60000, 1801800, "30.03"},
		{"rounding", 60000, 1801801, "30.03"},
		{"ticks_only", 60000, 2702700, ""},
		{"inherited_timescale", 0, 2702700, "30.03"},
		{"promoted_muxer_timescale", 50, 384000, "30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := goavpipe.NewXcParams()
			params.Url = "missing-segment-validation-input.ts"
			params.Format, params.XcType = "fmp4-segment", goavpipe.XcVideo
			params.VideoTimeBase, params.VideoSegDurationTs = tc.timescale, tc.ticks
			params.SegDuration = tc.seconds
			handle, err := XcInit(params)
			require.NoError(t, err)
			require.NoError(t, XcFini(handle))
		})
	}
}
