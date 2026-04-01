// End-to-end tests for mezzanine and ABR segment XC jobs.
//
//	Phase 1 (TestMezCreate): Transcode source file into mez part (fmp4)
//	Phase 2 (TestABRCreate): Generate ABR segments from each mez part
//
// The test matrix is: resolution x encryption x watermark
//
// - resoution:  bypass, 720p, 540p, 360p
// - encryption:  none, cenc, cbcs, aes-128
// - watermark:   none, text, image  (only for sources with Watermark=true)
//
// Each phase can also be run independently:
//
//	go test ./xc/ -run TestMezCreate  (comment out skip)
//	go test ./xc/ -run TestABRCreate  (comment out skip)
//	go test ./xc/ -run TestEndToEnd
//
// Output and logs under ./test_run/  (mez/ and segs/)
package xc_test

import (
	"fmt"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"encoding/hex"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/mp4e"
	"github.com/eluv-io/avpipe/pkg/validate"
	"github.com/eluv-io/avpipe/xc"
	"github.com/eluv-io/log-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test source files
// ---------------------------------------------------------------------------

const mezSourceDir = "../media"
const mezOutputDir = "../test_out/xc/mez"
const segsOutputDir = "../test_out/xc/segs"

// keepMezOutput prevents cleanup of mez output
const keepMezOutput = true

// keepSegsOutput prevents cleanup of ABR segment output
const keepSegsOutput = true

// e2eMode is set to true when tests run inside TestEndToEnd.
var e2eMode bool

// mezTestSource defines a source file and its properties for mez testing.
// Path is relative to mezSourceDir.
type mezTestSource struct {
	Path        string // file name within mezSourceDir
	FrameRate   string // frame rate as "num/den" (e.g. "30000/1001")
	ForceKeyInt int32  // key frame interval in frames (e.g. 48)
	Watermark   bool   // if true, also run watermarked ABR variants
}

// mezTestSources is the list of source files used by TestMezCreate.
// Format:  file, frame rate, key interval, watermark
var mezTestSources = []mezTestSource{
	{"bbb_1080p_30fps_10sec.mp4", "30/1", 60, false},
	{"bbb_1080p_30fps_60sec.mp4", "30/1", 60, true},
	{"bbb_sunflower_2160p_30fps_normal_2min.mp4", "30/1", 60, false},
	{"bbb_sunflower_2160p_30fps_normal_2min.ts", "30/1", 60, false},
	{"caminandes_llamigos_1080p_4audios.mp4", "24/1", 48, false},
	{"03_caminandes_llamigos_1080p.mp4", "24/1", 48, true},
	{"03_caminandes_llamigos_h265_1080p.mp4", "24/1", 48, false},
	{"Rigify-2min.mp4", "24/1", 48, false},
	{"Sintel_30s_5_1_pcm_s24le_60000Hz.mov", "24/1", 48, false},
	{"ELD2_FHD_4_60s_CCBYblendercloud.mov", "24000/1001", 48, false},
	{"BBB0_HD_8_XDCAM_120s_CCBYblendercloud.mxf", "60000/1001", 120, false},
	{"SIN6_4K_MOS_HEVC_60s.mp4", "24000/1001", 48, true},
	{"video-960.mp4", "30/1", 60, false},
	{"TOS0_FHD_2_H264_60s_CCBYblendercloud.mp4", "24/1", 48, false},
	{"TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov", "24000/1001", 48, false},

	// Currently these files don't work
	//{"bbb_sunflower_1080p_29_97_fps_normal.mp4", "30000/1001", 60, false}, // Broken - file actually 30/1 fps
	//{"prores_example.mov",                       "30000/1001", 60, false}, // Broken
	//{"Rigify-2min-10000ts.mp4", "24/1", 48, false},                        // Broken - duration expected 416, got 417
	//{"BBB4_HD_51_AVC_120s_CCBYblendercloud.ts", "60/1", 120, false},  // Broken - improper key frame int
	//{"SIN5_4K_MOS_J2K_60s_CCBYblendercloud.mxf", "24000/1001", 48, false},  // Broken - avg_framerate and timebase

}

// ---------------------------------------------------------------------------
// Mez test profile
// ---------------------------------------------------------------------------

// mezTestProfile defines encoding parameters and the corresponding verification
// parameters for a mez part test.
type mezTestProfile struct {
	name   string
	params *goavpipe.XcParams
	verify *xc.MezPartParams
}

// parseFrameRate parses a "num/den" string into a *big.Rat.
func parseFrameRate(s string) *big.Rat {
	parts := strings.SplitN(s, "/", 2)
	r, ok := new(big.Rat).SetString(parts[0])
	if !ok {
		panic("invalid frame rate numerator: " + s)
	}
	if len(parts) == 2 {
		den, ok2 := new(big.Rat).SetString(parts[1])
		if !ok2 {
			panic("invalid frame rate denominator: " + s)
		}
		r.Quo(r, den)
	}
	return r
}

// mezProfileForSource builds the encoding + verification profile for a given
// source.
func mezProfileForSource(src mezTestSource) *mezTestProfile {
	frameRate := parseFrameRate(src.FrameRate)
	abrSegDuration := new(big.Rat).SetFrac64(2, 1) // 2 seconds

	params := goavpipe.NewXcParams()
	params.Format = "fmp4-segment"
	params.Url = path.Join(mezSourceDir, src.Path)
	params.Ecodec = "libx264"
	params.EncHeight = 720
	params.EncWidth = 1280
	params.VideoBitrate = 2560000
	params.ForceKeyInt = src.ForceKeyInt
	params.XcType = goavpipe.XcVideo
	params.StreamId = -1

	verify := &xc.MezPartParams{
		FrameRate:           frameRate,
		ABRVideoSegDuration: abrSegDuration,
		ABRAudioSegDuration: new(big.Rat).SetFrac64(2, 1),
		ABRSegmentsPerPart:  xc.DefaultABRSegmentsPerPart,
		FramesPerFrag:       xc.OneFramePerFragment,
		StartPTS:            0,
	}

	return &mezTestProfile{
		name:   fmt.Sprintf("%s_fps", frameRate.RatString()),
		params: params,
		verify: verify,
	}
}

// ---------------------------------------------------------------------------
// ABR variant definitions
// ---------------------------------------------------------------------------

// Test encryption key material (fixed for reproducibility)
const (
	testCryptKey = "76a6c65c5ea762046bd749a2e632ccbb"
	testCryptIV  = "a7e61c373e219c38e8706b26936bc899"
	testCryptKID = "a7e61c373e219c38e8706b26936bc899"
)

// Watermark overlay image path (relative to xc/ test directory).
const watermarkImagePath = "../media/avpipe.png"

// watermarkType identifies the type of watermark applied.
type watermarkType int

const (
	wmNone watermarkType = iota
	wmText
	wmImage
)

func (w watermarkType) suffix() string {
	switch w {
	case wmText:
		return "_wmtxt"
	case wmImage:
		return "_wmimg"
	default:
		return ""
	}
}

// abrVariant defines a DASH/HLS encoding variant to generate from a mez part.
type abrVariant struct {
	Name       string               // subtest name
	RepDir     string               // representation subdirectory (e.g. "h264_720_cenc_wmtxt")
	Format     string               // "dash" or "hls"
	Bypass     bool                 // true = copy without transcoding
	Height     int32                // target height (-1 = preserve source)
	Width      int32                // target width  (-1 = preserve source)
	Bitrate    int32                // target video bitrate
	Encryption goavpipe.CryptScheme // CryptNone, CryptCENC, CryptCBCS, CryptAES128
	Watermark  watermarkType        // wmNone, wmText, wmImage
	NeedsWM    bool                 // true = only include for sources with Watermark=true
}

// abrBaseVariants defines the encoding ladder without encryption or watermark.
var abrBaseVariants = []abrVariant{
	{"bypass", "h264_bypass", "dash", true, -1, -1, -1, goavpipe.CryptNone, wmNone, false},
	{"720p", "h264_720", "dash", false, 720, 1280, 5000000, goavpipe.CryptNone, wmNone, false},
	{"540p", "h264_540", "dash", false, 540, 960, 3000000, goavpipe.CryptNone, wmNone, false},
	{"360p", "h264_360", "dash", false, 360, 640, 1000000, goavpipe.CryptNone, wmNone, false},
}

// encryptionSchemes is the set of encryption options applied to each base variant.
var encryptionSchemes = []struct {
	Suffix string
	Scheme goavpipe.CryptScheme
}{
	{"_clear", goavpipe.CryptNone},
	{"_cenc", goavpipe.CryptCENC},
	{"_cbcs", goavpipe.CryptCBCS},
	{"_aes128", goavpipe.CryptAES128},
}

// watermarkOptions applied to each variant (wmNone is always included).
var watermarkOptions = []struct {
	WM      watermarkType
	NeedsWM bool // only include for sources with Watermark=true
}{
	{wmNone, false},
	{wmText, true},
	{wmImage, true},
}

// abrVariants is built by combining base variants × encryption × watermark.
var abrVariants = buildABRVariants()

func buildABRVariants() []abrVariant {
	var variants []abrVariant
	for _, base := range abrBaseVariants {
		for _, enc := range encryptionSchemes {
			for _, wm := range watermarkOptions {
				v := base
				v.Encryption = enc.Scheme
				v.Watermark = wm.WM
				v.NeedsWM = wm.NeedsWM
				v.RepDir = base.RepDir + enc.Suffix + wm.WM.suffix()
				v.Name = base.Name + enc.Suffix + wm.WM.suffix()
				// Watermarking requires transcoding — disable bypass
				if wm.WM != wmNone && v.Bypass {
					v.Bypass = false
				}
				variants = append(variants, v)
			}
		}
	}
	return variants
}

func fileMissing(url string) bool {
	info, err := os.Stat(url)
	if os.IsNotExist(err) {
		log.Warn("Skipping, input file missing", "file", url)
		return true
	}
	return info.IsDir()
}

// ---------------------------------------------------------------------------
// Test
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Phase 1: Mez part generation from source files
// ---------------------------------------------------------------------------

// TestMezCreate creates mez parts from each source in mezTestSources and
// validates them.
func TestMezCreate(t *testing.T) {
	if !e2eMode {
		t.Skip("Use TestEndToEnd or comment out this skip")
	}
	for _, src := range mezTestSources {
		t.Run(src.Path, func(t *testing.T) {
			url := path.Join(mezSourceDir, src.Path)
			log.Info("STARTING TestMezCreate", "source", url)

			if fileMissing(url) {
				t.Skipf("source file missing: %s", url)
				return
			}

			profile := mezProfileForSource(src)

			// Output directory per source file.
			baseName := strings.TrimSuffix(src.Path, path.Ext(src.Path))
			videoMezDir := path.Join(mezOutputDir, baseName)
			err := os.MkdirAll(videoMezDir, 0755)
			require.NoError(t, err)
			if !keepMezOutput && !e2eMode {
				t.Cleanup(func() { os.RemoveAll(videoMezDir) })
			}

			// --- Stage 1: Create video mez parts ---
			goavpipe.InitIOHandler(
				&xc.FileInputOpener{URL: url},
				&xc.FileOutputOpener{Dir: videoMezDir},
			)
			err = avpipe.Xc(profile.params)
			require.NoError(t, err, "Xc failed for %s", url)

			// --- Stage 2: Validate each mez part ---
			partFiles, err := filepath.Glob(filepath.Join(videoMezDir, "vsegment-*.mp4"))
			require.NoError(t, err)
			sort.Strings(partFiles)
			require.NotEmpty(t, partFiles, "no mez parts produced in %s", videoMezDir)
			log.Info("Mez parts produced", "count", len(partFiles), "dir", videoMezDir)

			for partIdx, partFile := range partFiles {
				isLastPart := partIdx == len(partFiles)-1
				t.Run(filepath.Base(partFile), func(t *testing.T) {
					// Validate mez part structure
					result, err := xc.ValidateMezPart(partFile, profile.verify)
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

					if !isLastPart {
						expectedFrames := profile.verify.ExpectedFrameCount()
						assert.Equal(t, expectedFrames, result.FrameCount,
							"frame count mismatch for %s", partFile)
					}
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
						&xc.FileInputOpener{URL: partFile},
						&xc.FileOutputOpener{Dir: videoMezDir},
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
		})
	}
}

// ---------------------------------------------------------------------------
// Phase 2: ABR segment generation from mez parts
// ---------------------------------------------------------------------------

// TestABRCreate generates DASH/HLS segments from mez parts and validates them.
// It uses contiguous segment start numbers and PTS to mimic the production use case.
//
// - re-runs the mez creation if parts are not already on disk.
// - Encrypted segments are decrypted before validation.
//
// Validation:
//   - Init segment codec config (SPS/PPS/VPS) is identical across parts
//   - Consecutive DTS and equal frame durations within and across chunks
//   - Correct segment/fragment numbering across parts
//   - Expected frame counts (except last segment)
func TestABRCreate(t *testing.T) {
	if !e2eMode {
		t.Skip("Use TestEndToEnd or comment out this skip")
	}
	for _, src := range mezTestSources {
		t.Run(src.Path, func(t *testing.T) {
			url := path.Join(mezSourceDir, src.Path)
			if fileMissing(url) {
				t.Skipf("source file missing: %s", url)
				return
			}

			profile := mezProfileForSource(src)
			baseName := strings.TrimSuffix(src.Path, path.Ext(src.Path))
			videoMezDir := path.Join(mezOutputDir, baseName)

			// Ensure mez parts exist (create if needed)
			partFiles := ensureMezParts(t, src, profile, videoMezDir)
			require.NotEmpty(t, partFiles, "no mez parts for %s", src.Path)

			for _, v := range abrVariants {
				// Skip watermark variants for sources that don't have watermarking enabled
				if v.NeedsWM && !src.Watermark {
					continue
				}
				t.Run(v.Name, func(t *testing.T) {
					// State carried across parts for continuity
					var (
						nextSegNum  int                 = 1 // next chunk file number
						nextFragIdx int32               = 1 // next mfhd sequence number
						nextPts     int64               = 0 // next starting PTS/DTS
						refInitInfo *xc.InitSegmentInfo     // init from first part
					)

					for partIdx, partFile := range partFiles {
						isLastPart := partIdx == len(partFiles)-1
						partName := fmt.Sprintf("part_%02d", partIdx+1)
						abrDir := path.Join(segsOutputDir, baseName, v.RepDir, partName)

						t.Run(partName, func(t *testing.T) {
							result := generateAndValidateABRPart(t, v, partFile, abrDir, profile,
								nextSegNum, nextFragIdx, nextPts, isLastPart)

							// Verify init segments are codec-compatible across parts.
							// Use decrypted init for encrypted variants.
							initDir := abrDir
							if v.Encryption != goavpipe.CryptNone {
								initDir = filepath.Join(abrDir, "decrypted")
							}
							initFile := filepath.Join(initDir, "vinit-stream0.m4s")
							initInfo, iErr := xc.ValidateInitSegment(initFile)
							require.NoError(t, iErr, "ValidateInitSegment failed for %s", initFile)

							if partIdx == 0 {
								refInitInfo = initInfo
							} else {
								err := xc.InitSegmentsMatch(refInitInfo, initInfo)
								assert.NoError(t, err,
									"init segment codec config mismatch between part 1 and part %d", partIdx+1)
							}

							// Advance state for next part
							nextSegNum = result.NextSegNum
							nextFragIdx = result.NextFragIdx
							nextPts = result.NextPts
						})
					}
				})
			}
		})
	}
}

// mezProbeInfo captures codec properties from a mez part for ABR configuration.
// ensureMezParts returns existing mez part paths, creating them if absent.
func ensureMezParts(t *testing.T, src mezTestSource, profile *mezTestProfile, videoMezDir string) []string {
	t.Helper()
	partFiles, _ := filepath.Glob(filepath.Join(videoMezDir, "vsegment-*.mp4"))
	sort.Strings(partFiles)
	if len(partFiles) > 0 {
		return partFiles
	}

	// No parts on disk — create them
	url := path.Join(mezSourceDir, src.Path)
	err := os.MkdirAll(videoMezDir, 0755)
	require.NoError(t, err)

	goavpipe.InitIOHandler(
		&xc.FileInputOpener{URL: url},
		&xc.FileOutputOpener{Dir: videoMezDir},
	)
	err = avpipe.Xc(profile.params)
	require.NoError(t, err, "Xc mez creation failed for %s", url)

	partFiles, _ = filepath.Glob(filepath.Join(videoMezDir, "vsegment-*.mp4"))
	sort.Strings(partFiles)
	return partFiles
}

// abrPartResult carries continuity state from one part to the next.
type abrPartResult struct {
	NextSegNum  int   // next chunk file number (for StartSegmentStr)
	NextFragIdx int32 // next mfhd sequence number (for StartFragmentIndex)
	NextPts     int64 // next PTS/DTS (for StartPts)
}

// generateAndValidateABRPart runs a single ABR variant against one mez part,
// validates the output, and returns state for the next part.
func generateAndValidateABRPart(
	t *testing.T,
	v abrVariant,
	partFile, abrDir string,
	profile *mezTestProfile,
	startSegNum int,
	startFragIdx int32,
	startPts int64,
	isLastPart bool,
) abrPartResult {
	t.Helper()

	err := os.MkdirAll(abrDir, 0755)
	require.NoError(t, err)
	if !keepSegsOutput && !e2eMode {
		t.Cleanup(func() { os.RemoveAll(abrDir) })
	}

	// Probe the mez part to get timescale for VideoSegDurationTs
	goavpipe.InitIOHandler(
		&xc.FileInputOpener{URL: partFile},
		&xc.FileOutputOpener{Dir: abrDir},
	)
	probeParams := goavpipe.NewXcParams()
	probeParams.Url = partFile
	probeInfo, err := avpipe.Probe(probeParams)
	require.NoError(t, err, "Probe failed for %s", partFile)
	require.NotEmpty(t, probeInfo.StreamInfo, "no streams in %s", partFile)

	var timescale int64
	for _, si := range probeInfo.StreamInfo {
		if si.CodecType == "video" {
			// TimeBase is num/den (e.g. 1/12288); timescale = denominator
			timescale = si.TimeBase.Denom().Int64()
			break
		}
	}
	require.NotZero(t, timescale, "could not determine timescale for %s", partFile)

	// VideoSegDurationTs = ABR_segment_duration_seconds * timescale
	abrSegDur := profile.verify.ABRVideoSegDuration
	videoSegDurationTs := new(big.Rat).Mul(abrSegDur, new(big.Rat).SetInt64(timescale))
	segDurTs, _ := videoSegDurationTs.Float64()

	// --- Build XcParams for ABR ---
	params := goavpipe.NewXcParams()
	params.Format = v.Format
	params.Url = partFile
	params.XcType = goavpipe.XcVideo
	params.StreamId = -1
	params.BypassTranscoding = v.Bypass
	params.StartSegmentStr = fmt.Sprintf("%d", startSegNum)
	params.StartFragmentIndex = startFragIdx
	params.StartPts = startPts
	params.VideoSegDurationTs = int64(segDurTs)

	if !v.Bypass {
		params.Ecodec = "libx264"
		params.EncHeight = v.Height
		params.EncWidth = v.Width
		params.VideoBitrate = v.Bitrate
		params.ForceKeyInt = profile.params.ForceKeyInt
	}

	// Set encryption parameters
	if v.Encryption != goavpipe.CryptNone {
		params.CryptScheme = v.Encryption
		params.CryptKey = testCryptKey
		params.CryptIV = testCryptIV
		params.CryptKID = testCryptKID
	}

	// Set watermark parameters
	switch v.Watermark {
	case wmText:
		params.WatermarkText = "avpipe test watermark"
		params.WatermarkXLoc = "W/2"
		params.WatermarkYLoc = "H*0.5"
		params.WatermarkRelativeSize = 0.05
		params.WatermarkFontColor = "white"
		params.WatermarkShadow = true
		params.WatermarkShadowColor = "black"
	case wmImage:
		overlayData, rErr := os.ReadFile(watermarkImagePath)
		require.NoError(t, rErr, "failed to read watermark image %s", watermarkImagePath)
		params.WatermarkOverlay = string(overlayData)
		params.WatermarkOverlayLen = len(overlayData)
		params.WatermarkOverlayType = goavpipe.PngImage
		params.WatermarkXLoc = "main_w/2-overlay_w/2"
		params.WatermarkYLoc = "main_h/2-overlay_h/2"
	}

	// Write XCPARAMS file for debugging
	xcparamsFile := filepath.Join(abrDir, "XCPARAMS")
	xcparamsContent := fmt.Sprintf(""+
		"Format:             %s\n"+
		"Url:                %s\n"+
		"BypassTranscoding:  %v\n"+
		"Ecodec:             %s\n"+
		"EncHeight:          %d\n"+
		"EncWidth:           %d\n"+
		"VideoBitrate:       %d\n"+
		"ForceKeyInt:        %d\n"+
		"VideoSegDurationTs: %d\n"+
		"StartSegmentStr:    %s\n"+
		"StartFragmentIndex: %d\n"+
		"StartPts:           %d\n"+
		"Timescale:          %d\n"+
		"CryptScheme:        %d\n"+
		"CryptKey:           %s\n"+
		"CryptIV:            %s\n"+
		"CryptKID:           %s\n"+
		"WatermarkText:      %s\n"+
		"WatermarkOverlay:   %d bytes\n",
		params.Format,
		params.Url,
		params.BypassTranscoding,
		params.Ecodec,
		params.EncHeight,
		params.EncWidth,
		params.VideoBitrate,
		params.ForceKeyInt,
		params.VideoSegDurationTs,
		params.StartSegmentStr,
		params.StartFragmentIndex,
		params.StartPts,
		timescale,
		params.CryptScheme,
		params.CryptKey,
		params.CryptIV,
		params.CryptKID,
		params.WatermarkText,
		params.WatermarkOverlayLen,
	)
	os.WriteFile(xcparamsFile, []byte(xcparamsContent), 0644)

	goavpipe.InitIOHandler(
		&xc.FileInputOpener{URL: partFile},
		&xc.FileOutputOpener{Dir: abrDir},
	)
	err = avpipe.Xc(params)
	require.NoError(t, err, "ABR Xc failed for %s variant %s", partFile, v.Name)

	// --- Decrypt if encrypted ---
	// Validation always runs against clear content. For encrypted variants,
	// decrypt init + chunks into a subdirectory first.
	encrypted := v.Encryption != goavpipe.CryptNone
	validateDir := abrDir
	if encrypted {
		decryptDir := filepath.Join(abrDir, "decrypted")
		err = os.MkdirAll(decryptDir, 0755)
		require.NoError(t, err)

		keyBytes, kErr := hex.DecodeString(testCryptKey)
		require.NoError(t, kErr)
		ivBytes, iErr := hex.DecodeString(testCryptIV)
		require.NoError(t, iErr)

		var scheme mp4e.CryptScheme
		switch v.Encryption {
		case goavpipe.CryptCENC:
			scheme = mp4e.CryptCENC
		case goavpipe.CryptCBCS:
			scheme = mp4e.CryptCBCS
		case goavpipe.CryptAES128:
			scheme = mp4e.CryptAES128
		}

		// Decrypt init segment
		encInitFile := filepath.Join(abrDir, "vinit-stream0.m4s")
		decInitFile := filepath.Join(decryptDir, "vinit-stream0.m4s")
		dErr := mp4e.DecryptFile(encInitFile, decInitFile, scheme, keyBytes, ivBytes)
		require.NoError(t, dErr, "decrypt init failed")

		// Decrypt each chunk (pass encrypted init for CENC/CBCS metadata)
		encChunks, _ := filepath.Glob(filepath.Join(abrDir, "vchunk-stream0-*.m4s"))
		for _, cf := range encChunks {
			dErr := mp4e.DecryptFile(cf,
				filepath.Join(decryptDir, filepath.Base(cf)),
				scheme, keyBytes, ivBytes, encInitFile)
			require.NoError(t, dErr, "decrypt chunk failed for %s", cf)
		}

		validateDir = decryptDir
	}

	// --- Validate init segment ---
	initFile := filepath.Join(validateDir, "vinit-stream0.m4s")
	initInfo, err := xc.ValidateInitSegment(initFile)
	require.NoError(t, err, "ValidateInitSegment failed for %s", initFile)

	expectedHeight := int(v.Height)
	expectedWidth := int(v.Width)
	if v.Bypass || v.Height == -1 {
		// Bypass or preserve-source: output matches mez dimensions
		expectedHeight = int(profile.params.EncHeight)
		expectedWidth = int(profile.params.EncWidth)
	}
	assert.Equal(t, expectedHeight, initInfo.Height, "init height mismatch")
	assert.Equal(t, expectedWidth, initInfo.Width, "init width mismatch")

	log.Info("Init segment validated",
		"file", initFile,
		"codec", initInfo.Codec,
		"width", initInfo.Width,
		"height", initInfo.Height,
		"timescale", initInfo.Timescale,
		"encrypted", encrypted)

	// --- Validate media segments ---
	chunkFiles, err := filepath.Glob(filepath.Join(validateDir, "vchunk-stream0-*.m4s"))
	require.NoError(t, err)
	sort.Strings(chunkFiles)
	require.NotEmpty(t, chunkFiles, "no chunks produced in %s", validateDir)

	// Expected frames per ABR segment
	frameRate := parseFrameRate(profile.verify.FrameRate.RatString())
	abrSegDuration := profile.verify.ABRVideoSegDuration
	framesPerABRSeg := new(big.Rat).Mul(frameRate, abrSegDuration)
	num := framesPerABRSeg.Num().Int64()
	den := framesPerABRSeg.Denom().Int64()
	half := den / 2
	expectedFramesPerChunk := int((num + half) / den)

	// Track the last chunk's result for continuity
	var lastChunkResult *xc.ABRSegmentResult

	for chunkIdx, chunkFile := range chunkFiles {
		isLastChunk := isLastPart && chunkIdx == len(chunkFiles)-1

		t.Run(filepath.Base(chunkFile), func(t *testing.T) {
			result, vErr := xc.ValidateABRSegment(chunkFile)
			require.NoError(t, vErr, "ValidateABRSegment failed for %s", chunkFile)

			log.Info("ABR segment validation",
				"file", chunkFile,
				"frames", result.FrameCount,
				"fragments", result.FragCount,
				"seq_first", result.SeqFirst,
				"seq_last", result.SeqLast,
				"dts_start", result.DtsStart,
				"dts_end", result.DtsEnd,
				"sample_dur", result.SampleDur,
				"dts_problems", result.DtsProblems,
				"dur_problems", result.DurProblems)

			// Frame count — skip for last chunk of last part
			if !isLastChunk {
				assert.Equal(t, expectedFramesPerChunk, result.FrameCount,
					"frame count mismatch for %s", chunkFile)
			}

			// DTS consecutive and equal durations within chunk
			assert.Equal(t, 0, result.DtsProblems,
				"DTS problems in %s", chunkFile)
			assert.Equal(t, 0, result.DurProblems,
				"duration problems in %s", chunkFile)

			// Cross-chunk continuity: DTS of this chunk follows previous
			if chunkIdx > 0 && lastChunkResult != nil {
				assert.Equal(t, lastChunkResult.DtsEnd, result.DtsStart,
					"DTS gap between chunks %d and %d", chunkIdx, chunkIdx+1)
				assert.Equal(t, lastChunkResult.SeqLast+1, result.SeqFirst,
					"fragment sequence gap between chunks %d and %d", chunkIdx, chunkIdx+1)
			}

			lastChunkResult = result
		})
	}

	// Compute next-part state from the last chunk
	result := abrPartResult{
		NextSegNum: startSegNum + len(chunkFiles),
	}
	if lastChunkResult != nil {
		result.NextFragIdx = int32(lastChunkResult.SeqLast) + 1
		result.NextPts = int64(lastChunkResult.DtsEnd)
	}
	return result
}

// TestEndToEnd runs all phases in sequence, reusing intermediate output.
// Cleanup happens once at the end (unless keepMezOutput/keepSegsOutput are set).
//
// Run:
// - individual phases:  go test ./xc/ -run TestMezCreate
// - full pipeline:      go test ./xc/ -run TestEndToEnd
func TestEndToEnd(t *testing.T) {
	e2eMode = true
	defer func() { e2eMode = false }()

	t.Run("phase1-mez", TestMezCreate)
	t.Run("phase2-abr", TestABRCreate)

	// Cleanup all output after both phases complete
	t.Cleanup(func() {
		if !keepMezOutput {
			os.RemoveAll(mezOutputDir)
		}
		if !keepSegsOutput {
			os.RemoveAll(segsOutputDir)
		}
	})
}
