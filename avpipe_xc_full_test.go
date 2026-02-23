package avpipe_test

import (
	"fmt"
	"math/big"
	"path"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/pkg/validate"
	"github.com/eluv-io/log-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mezTestProfile defines encoding parameters and the corresponding verification
// parameters for a mez part test.
type mezTestProfile struct {
	name   string
	params *goavpipe.XcParams
	verify *validate.MezPartParams
}

// defaultMezProfile returns the default/basic encoding profile for testing.
// Source: bbb_1080p_30fps_60sec.mp4 (30fps, 60 seconds)
// At 30fps with 2-second ABR segments: 60 frames per segment, 15 segments per part = 900 frames.
// With 60 seconds of content and 30-second parts, we expect 2 mez parts.
func defaultMezProfile(url string) *mezTestProfile {
	frameRate := new(big.Rat).SetFrac64(30, 1)
	abrSegDuration := new(big.Rat).SetFrac64(2, 1) // 2 seconds

	params := goavpipe.NewXcParams()
	params.Format = "fmp4-segment"
	params.Url = url
	params.Ecodec = "libx264"
	params.EncHeight = 720
	params.EncWidth = 1280
	params.VideoBitrate = 2560000
	params.ForceKeyInt = 60 // key frame every 60 frames = every 2 seconds at 30fps
	params.XcType = goavpipe.XcVideo
	params.StreamId = -1

	verify := &validate.MezPartParams{
		FrameRate:           frameRate,
		ABRVideoSegDuration: abrSegDuration,
		ABRAudioSegDuration: new(big.Rat).SetFrac64(2, 1),
		ABRSegmentsPerPart:  validate.DefaultABRSegmentsPerPart,
		FramesPerFrag:       validate.OneFramePerFragment,
		StartPTS:            0,
	}

	return &mezTestProfile{
		name:   "default_30fps",
		params: params,
		verify: verify,
	}
}

// TestMezPartEndToEnd creates mez parts from a video file and validates them.
func TestMezPartEndToEnd(t *testing.T) {
	f := fn()
	log.Info("STARTING " + f)

	url := videoBigBuckBunnyPath
	if fileMissing(url, f) {
		return
	}

	profile := defaultMezProfile(url)
	videoMezDir := path.Join(baseOutPath, f, "VideoMez")

	// --- Stage 1: Create video mez parts ---
	setupOutDir(t, videoMezDir)
	goavpipe.InitIOHandler(
		&fileInputOpener{url: url},
		&fileOutputOpener{dir: videoMezDir},
	)
	boilerXc(t, profile.params)

	// --- Stage 2: Validate each mez part ---
	expectedParts := 2 // 60 seconds / 30 seconds per part
	for i := 1; i <= expectedParts; i++ {
		partFile := fmt.Sprintf("./%s/vsegment-%d.mp4", videoMezDir, i)
		t.Run(fmt.Sprintf("validate_vsegment_%d", i), func(t *testing.T) {
			// Verify file exists
			require.False(t, fileMissing(partFile, f),
				"mez part file missing: %s", partFile)

			// Validate mez part structure
			result, err := validate.ValidateMezPart(partFile, profile.verify)
			require.NoError(t, err, "ValidateMezPart failed for %s", partFile)

			log.Info("Mez part validation",
				"file", partFile,
				"frames", result.FrameCount,
				"fragments", result.FragmentCount,
				"timescale", result.Timescale,
				"sample_duration", result.SampleDuration,
				"missing_key_frames", result.MissingKeyFrames,
				"continuity_problems", len(result.ContinuityProblems),
				"errors", len(result.Errors))

			// Report all errors
			if !result.Valid() {
				allErr := result.AllErrors()
				assert.NoError(t, allErr, "mez part validation failed for %s", partFile)
			}

			// Explicit checks
			expectedFrames := profile.verify.ExpectedFrameCount()
			assert.Equal(t, expectedFrames, result.FrameCount,
				"frame count mismatch for %s", partFile)
			assert.Equal(t, 0, result.MissingKeyFrames,
				"missing key frames in %s", partFile)
			assert.Empty(t, result.ContinuityProblems,
				"continuity problems in %s", partFile)

			// Also run the generic mez validator
			genericResult, err := validate.ValidateMez(partFile, 10, 100000)
			require.NoError(t, err, "ValidateMez failed for %s", partFile)
			assert.True(t, genericResult.Valid,
				"generic mez validation failed for %s: %v", partFile, genericResult.Issues)

			// Probe for codec properties
			probeParams := goavpipe.NewXcParams()
			probeParams.Url = partFile
			goavpipe.InitIOHandler(
				&fileInputOpener{url: partFile},
				&fileOutputOpener{dir: videoMezDir},
			)
			probeInfo, err := avpipe.Probe(probeParams)
			require.NoError(t, err, "Probe failed for %s", partFile)
			require.NotEmpty(t, probeInfo.StreamInfo, "no streams in %s", partFile)

			// Check video stream properties
			for _, si := range probeInfo.StreamInfo {
				if si.CodecType == "video" {
					assert.Equal(t, 720, si.Height, "height mismatch")
					assert.Equal(t, 1280, si.Width, "width mismatch")
					log.Info("Probe result",
						"codec", si.CodecName,
						"profile", si.Profile,
						"level", si.Level,
						"timescale", si.TimeBase)
				}
			}
		})
	}
}
