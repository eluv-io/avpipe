package mp4e

import (
	"bytes"
	"math/big"
	"os"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTrackFrameRate checks the derivation against real muxer output. The
// expectations are ffprobe's r_frame_rate on the full segment, except where
// noted.
//
// The vsegment_head_* assets are real avpipe segments truncated to moov plus
// three moof boxes: small, and the shape the live probe will actually see,
// since it reads the head of a part rather than the whole of one. They live in
// media/ with the rest of the test assets - see scripts/download-test-assets.sh.
func TestTrackFrameRate(t *testing.T) {
	for _, tt := range []struct {
		path string
		want *big.Rat
		note string
	}{
		{
			path: "../media/vsegment_head_24fps_ts12288.mp4",
			want: big.NewRat(24, 1),
			note: "integer rate at a timescale that divides it exactly",
		},
		{
			path: "../media/vsegment_head_23976fps_ts24000.mp4",
			want: big.NewRat(24000, 1001),
			note: "NTSC rate at a timescale that divides it exactly",
		},
		{
			path: "testdata/vfsegment.mp4",
			want: big.NewRat(60000, 1001),
			// ffprobe reports 45000/751 here, which is 90000/1502 - one
			// sample's duration. The durations alternate 1501/1502, so the
			// true average is 1501.5 and the rate is exactly 60000/1001.
			// Averaging over three samples and snapping gets it right where a
			// single sample cannot.
			note: "NTSC rate whose durations alternate; better than ffprobe's",
		},
	} {
		t.Run(tt.note, func(t *testing.T) {
			b, err := os.ReadFile(tt.path)
			require.NoError(t, err)

			mov, err := ExtractMovInfoLazy(bytes.NewReader(b))
			require.NoError(t, err)
			require.NotEmpty(t, mov.Tracks)

			video := mov.Tracks[0]
			require.Equal(t, avdesc.MediaTypeVideo, video.MediaType)
			require.NotNil(t, video.FrameRate, "expected a derived frame rate")
			assert.Equal(t, tt.want.String(), video.FrameRate.String())
		})
	}

	t.Run("init segment alone yields no frame rate", func(t *testing.T) {
		b, err := os.ReadFile("testdata/vinit-stream0.m4s")
		require.NoError(t, err)

		mov, err := ExtractMovInfoLazy(bytes.NewReader(b))
		require.NoError(t, err)
		require.Len(t, mov.Tracks, 1)
		// Sample durations live in the fragments, so an init segment cannot
		// carry a rate. Nil rather than an error: the rest of the description
		// is still good.
		assert.Nil(t, mov.Tracks[0].FrameRate)
	})
}

// TestResolveFrameRate covers the bound directly: how many samples it takes to
// identify a rate, and the two ways it declines to guess.
func TestResolveFrameRate(t *testing.T) {
	// 59.94 at timescale 90000. The muxer floors the cumulative duration, so
	// the sample durations alternate 1501, 1502.
	t.Run("one sample cannot separate 59.94 from 60", func(t *testing.T) {
		_, resolved := resolveFrameRate(90000, 1, 1501)
		assert.False(t, resolved, "60 and 59.94 are both consistent with one sample")
	})

	t.Run("two samples identify it", func(t *testing.T) {
		rate, resolved := resolveFrameRate(90000, 2, 1501+1502)
		require.True(t, resolved)
		assert.Equal(t, "60000/1001", rate.String())
	})

	// The trap a nearest-match falls into: flooring biases a short measurement
	// upwards, so one sample of 59.94 at some timescales lands exactly on 60.
	t.Run("an exact hit on the wrong rate is not accepted", func(t *testing.T) {
		// 24000/59.94 = 400.4, floored to 400, and 24000/400 is exactly 60
		rate, resolved := resolveFrameRate(24000, 1, 400)
		assert.False(t, resolved,
			"exactly 60 by measurement, but 59.94 is equally consistent")
		assert.Nil(t, rate)
	})

	t.Run("a non-standard rate resolves to the measurement", func(t *testing.T) {
		// 15 fps at timescale 90000: no standard rate is anywhere near
		rate, resolved := resolveFrameRate(90000, 1, 6000)
		require.True(t, resolved, "no standard rate is consistent, so reading on cannot help")
		assert.Equal(t, "15/1", rate.String())
	})

	t.Run("integer rates identify without their NTSC neighbour interfering", func(t *testing.T) {
		// 24 fps at timescale 12288 is 512 ticks exactly. One sample leaves
		// both 24 and 23.976 in range; two settles it.
		_, resolved := resolveFrameRate(12288, 1, 512)
		assert.False(t, resolved)
		rate, resolved := resolveFrameRate(12288, 2, 1024)
		require.True(t, resolved)
		assert.Equal(t, "24/1", rate.String())
	})

	t.Run("a degenerate total declines", func(t *testing.T) {
		_, resolved := resolveFrameRate(90000, 1, 1)
		assert.False(t, resolved)
	})
}

// TestFrameRateCoarseTimescaleIsNotGuessed pins the honest failure. At
// timescale 1000 a 59.94 stream and a 60 stream both write 16 or 17 ticks per
// frame, so the distinction is destroyed by quantization rather than merely
// obscured - no sample count recovers it, and the derivation reports what it
// measured instead of picking one.
func TestFrameRateCoarseTimescaleIsNotGuessed(t *testing.T) {
	for n := uint64(1); n <= frameRateMaxSamples; n++ {
		// the cumulative a 59.94 stream writes at timescale 1000
		total := uint64(float64(n)*1000.0*1001.0/60000.0 + 1e-9)
		if _, resolved := resolveFrameRate(1000, n, total); resolved {
			// resolving here would mean claiming to know something the data
			// does not contain; the only acceptable resolution is the
			// measurement itself, never a standard rate
			rate, _ := resolveFrameRate(1000, n, total)
			for _, std := range standardFrameRates {
				assert.NotEqual(t, std.String(), rate.String(),
					"claimed a standard rate from an indistinguishable measurement at n=%d", n)
			}
		}
	}
}
