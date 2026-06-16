// End-to-end tests for mezzanine and ABR segment XC jobs.
//
//	Phase 1 (TestMezCreate): Transcode a source file into mez part (fmp4)
//	Phase 2 (TestABRCreate): Generate ABR segments from each mez part
//
// The test matrix is: resolution x encryption x watermark
//
// - Resoution:  bypass, 720p, 540p, 360p
// - Encryption:  none, cenc, cbcs, aes-128
// - Watermark:   none, text, image  (only for sources with Watermark=true)
//
// Each phase can also be run independently:
//
//	go test ./xc/ -run TestMezCreate  (comment out skip)
//	go test ./xc/ -run TestABRCreate  (comment out skip)
//	go test ./xc/ -run TestEndToEnd
//
// Output goes under ../test_out/xc_test.<TestName>/mez and .../segs.
package xc_test

import (
	"flag"
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
	"github.com/eluv-io/avpipe/internal/testutil"
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
const testOutBase = "../test_out"

var deleteMezOutput = flag.Bool("delete-mez-output", false, "delete mez output after tests")
var deleteSegsOutput = flag.Bool("delete-segs-output", false, "delete segment output after tests")
var srcOverrideAbsFilePath = flag.String("src-file", "", "absolute path to source file for TestEndToEndSingle (overrides default; fps and keyint are derived by probing)")

func mezOutputDir(t *testing.T) string  { return path.Join(testOutBase, "xc_test."+t.Name(), "mez") }
func segsOutputDir(t *testing.T) string { return path.Join(testOutBase, "xc_test."+t.Name(), "segs") }

// mezTestSource defines a source file and its properties for mez testing.
// Path is relative to mezSourceDir, or absolute.
type mezTestSource struct {
	Path        string // file name within mezSourceDir, or an absolute path
	FrameRate   string // frame rate as "num/den" (e.g. "30000/1001")
	ForceKeyInt int32  // key frame interval in frames (e.g. 48)
	Watermark   bool   // if true, also run watermarked ABR variants
	Ecodec      string // video encoder name; default "libx264" when empty
}

// mezURL returns the resolved source URL for src.
func mezURL(src mezTestSource) string {
	if filepath.IsAbs(src.Path) {
		return src.Path
	}
	return path.Join(mezSourceDir, src.Path)
}

// mezTestSources is the list of source files used by TestMezCreate.
// Format:  file, frame rate, key interval, watermark, ecodec ("" → libx264)
var mezTestSources = []mezTestSource{
	{"bbb_1080p_30fps_10sec.mp4", "30/1", 60, false, ""},
	{"bbb_1080p_30fps_60sec.mp4", "30/1", 60, true, ""},
	{"bbb_sunflower_2160p_30fps_normal_2min.mp4", "30/1", 60, false, ""},
	{"bbb_sunflower_2160p_30fps_normal_2min.ts", "30/1", 60, false, ""},
	{"caminandes_llamigos_1080p_4audios.mp4", "24/1", 48, false, ""},
	{"03_caminandes_llamigos_1080p.mp4", "24/1", 48, true, ""},
	{"03_caminandes_llamigos_h265_1080p.mp4", "24/1", 48, false, ""},
	{"Rigify-2min.mp4", "24/1", 48, false, ""},
	{"Sintel_30s_5_1_pcm_s24le_60000Hz.mov", "24/1", 48, false, ""},
	{"ELD2_FHD_4_60s_CCBYblendercloud.mov", "24000/1001", 48, false, ""},
	{"BBB0_HD_8_XDCAM_120s_CCBYblendercloud.mxf", "60000/1001", 120, false, ""},
	{"BBB0_HD_8_XDCAM_120s_CCBYblendercloud.mxf", "60000/1001", 120, false, "libx265"},
	{"SIN6_4K_MOS_HEVC_60s.mp4", "24000/1001", 48, true, ""},
	{"video-960.mp4", "30/1", 60, false, ""},
	{"TOS0_FHD_2_H264_60s_CCBYblendercloud.mp4", "24/1", 48, false, ""},
	{"TOS8_FHD_51-2_PRHQ_60s_CCBYblendercloud.mov", "24000/1001", 48, false, ""},
	{"yuvj420p.mov", "30/1", 60, false, "libx265"}, // test if color_range full/pc is preserved

	// Currently these files don't work
	//{"bbb_sunflower_1080p_29_97_fps_normal.mp4", "30000/1001", 60, false, ""}, // Broken - file actually 30/1 fps
	//{"prores_example.mov",                       "30000/1001", 60, false, ""}, // Broken
	//{"Rigify-2min-10000ts.mp4", "24/1", 48, false, ""},                        // Broken - duration expected 416, got 417
	//{"BBB4_HD_51_AVC_120s_CCBYblendercloud.ts", "60/1", 120, false, ""},  // Broken - improper key frame int
	//{"SIN5_4K_MOS_J2K_60s_CCBYblendercloud.mxf", "24000/1001", 48, false, ""},  // Broken - avg_framerate and timebase

}

// mezTestSourceID returns a unique identifier for a source entry that disambiguates
// duplicates with the same Path (e.g. when the same file is encoded with different codecs).
// Used as the test subtest name and as the output directory name.
func mezTestSourceID(src mezTestSource) string {
	p := src.Path
	if filepath.IsAbs(p) {
		p = filepath.Base(p)
	}
	id := strings.TrimSuffix(p, path.Ext(p))
	if src.Ecodec != "" && src.Ecodec != "libx264" {
		id += "_" + src.Ecodec
	}
	return id
}

// Append nvenc-encoded variants when an NVIDIA GPU is available.
func init() {
	if !testutil.NvidiaExist() {
		return
	}
	mezTestSources = append(mezTestSources,
		// SDR HEVC mez via nvenc - sister to the libx265 BBB0 entry above.
		mezTestSource{"BBB0_HD_8_XDCAM_120s_CCBYblendercloud.mxf", "60000/1001", 120, false, "hevc_nvenc"},
	)
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

// mezProfileForSource builds the encoding and verification profile for a given
// source.
func mezProfileForSource(src mezTestSource) *mezTestProfile {
	frameRate := parseFrameRate(src.FrameRate)
	abrSegDuration := new(big.Rat).SetFrac64(2, 1) // 2 seconds

	params := goavpipe.NewXcParams()
	params.Format = "fmp4-segment"
	params.Url = mezURL(src)
	params.Ecodec = src.Ecodec
	if params.Ecodec == "" {
		params.Ecodec = "libx264"
	}
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
	RepDir     string               // representation subdirectory (e.g. "720_cenc_wmtxt")
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
	{"bypass", "bypass", "dash", true, -1, -1, -1, goavpipe.CryptNone, wmNone, false},
	{"720p", "720", "dash", false, 720, 1280, 5000000, goavpipe.CryptNone, wmNone, false},
	{"540p", "540", "dash", false, 540, 960, 3000000, goavpipe.CryptNone, wmNone, false},
	{"360p", "360", "dash", false, 360, 640, 1000000, goavpipe.CryptNone, wmNone, false},
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
	NeedsWM bool // only include it for sources with Watermark=true
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

func removeAll(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		log.Warn("failed to remove directory", "dir", dir, "err", err)
	}
}

func skipIfFileMissing(t *testing.T, url string) {
	t.Helper()
	info, err := os.Stat(url)
	if err != nil || info.IsDir() {
		t.Skipf("input file not accessible: %s", url)
	}
}

// videoColor groups general video color metadata for the first video stream of a file,
// as signaled by the container and/or encoded bitstream.
type videoColor struct {
	Primaries string
	Transfer  string
	Space     string
	Range     string
}

// probeVideoColor returns the color metadata of the first video stream of url.
// Empty fields are returned for color values that are unspecified in the source.
func probeVideoColor(t *testing.T, url string) videoColor {
	t.Helper()
	goavpipe.InitIOHandler(
		&xc.FileInputOpener{URL: url},
		&xc.FileOutputOpener{Dir: filepath.Dir(url)},
	)
	probeParams := goavpipe.NewXcParams()
	probeParams.Url = url
	info, err := avpipe.Probe(probeParams)
	require.NoError(t, err, "probe failed for %s", url)
	for _, si := range info.Streams {
		if si.CodecType == "video" {
			return videoColor{
				Primaries: si.ColorPrimaries,
				Transfer:  si.ColorTransfer,
				Space:     si.ColorSpace,
				Range:     si.ColorRange,
			}
		}
	}
	return videoColor{}
}

// mezSourceFromFile probes url and returns a mezTestSource with Path set to url,
// FrameRate derived from the first video stream, ForceKeyInt set to round(2 × fps),
// and Ecodec set to "libx265" if the source is HEVC.
func mezSourceFromFile(t *testing.T, url string) mezTestSource {
	t.Helper()
	goavpipe.InitIOHandler(
		&xc.FileInputOpener{URL: url},
		&xc.FileOutputOpener{Dir: filepath.Dir(url)},
	)
	probeParams := goavpipe.NewXcParams()
	probeParams.Url = url
	info, err := avpipe.Probe(probeParams)
	require.NoError(t, err, "probe failed for %s", url)
	for _, si := range info.Streams {
		if si.CodecType == "video" && si.FrameRate != nil {
			num := si.FrameRate.Num().Int64()
			den := si.FrameRate.Denom().Int64()
			keyInt := int32((2*num + den/2) / den) // round(2 * fps)
			ecodec := ""
			if si.CodecName == "hevc" {
				ecodec = "libx265"
			}
			return mezTestSource{Path: url, FrameRate: si.FrameRate.RatString(), ForceKeyInt: keyInt, Ecodec: ecodec}
		}
	}
	t.Fatalf("no video stream found in %s", url)
	return mezTestSource{}
}

// assertColorMatches compares two videoColor sets with field-level error messages.
//
// For primaries/trc/space:
//   - If the source value is known (not "unknown"), assert it passes through unchanged.
//   - If the source value is "unknown" and expectSynthesis is true (DASH/HLS output),
//     dash_synthesize_color_defaults fills in BT.709 when all three fields are
//     UNSPECIFIED and color_range is known — assert "bt709" in that case.
//   - Otherwise skip (mez/fmp4-segment output does not synthesize).
//
// The color_range is always asserted verbatim.
//
// NOT TESTED: partial synthesis — when one or two of primaries/trc/space are set and
// dash_synthesize_color_defaults fills in the remaining fields to match. Covering that
// case would require sources with partial color metadata, which none of the current
// mezTestSources have.
func assertColorMatches(t *testing.T, want, got videoColor, label, url string, expectSynthesis bool) {
	t.Helper()
	// synthesis fires only when color_range is known and all three primaries/trc/space are UNSPECIFIED
	synthesized := expectSynthesis &&
		want.Range != "" && want.Range != "unknown" &&
		want.Primaries == "unknown" && want.Transfer == "unknown" && want.Space == "unknown"
	assertColor := func(wantVal, gotVal, field string) {
		if wantVal != "" && wantVal != "unknown" {
			assert.Equal(t, wantVal, gotVal, "%s pass-through (%s) in %s", field, label, url)
		} else if synthesized {
			assert.Equal(t, "bt709", gotVal, "synthesized %s (%s) in %s", field, label, url)
		}
	}
	assertColor(want.Primaries, got.Primaries, "color_primaries")
	assertColor(want.Transfer, got.Transfer, "color_transfer")
	assertColor(want.Space, got.Space, "color_space")
	assert.Equal(t, want.Range, got.Range, "color_range pass-through (%s) in %s", label, url)
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
	mezDir := mezOutputDir(t)
	if *deleteMezOutput {
		t.Cleanup(func() { removeAll(mezDir) })
	}
	t.Skip("run via TestEndToEnd, TestEndToEndSingle, or TestEndToEndBT709Synthesis")
	runMezCreate(t, mezTestSources, mezDir)
}

func runMezCreate(t *testing.T, sources []mezTestSource, mezDir string) {
	t.Helper()
	for _, src := range sources {
		t.Run(mezTestSourceID(src), func(t *testing.T) {
			url := mezURL(src)
			log.Info("STARTING TestMezCreate", "source", url)

			skipIfFileMissing(t, url)

			// Capture source color metadata so we can verify mez parts pass it through
			// to the mp4 'colr' atom (UNSPECIFIED fields are passed through unchanged).
			srcColor := probeVideoColor(t, url)
			log.Info("Source color metadata",
				"source", url,
				"primaries", srcColor.Primaries,
				"transfer", srcColor.Transfer,
				"space", srcColor.Space,
				"range", srcColor.Range)

			profile := mezProfileForSource(src)

			// Output directory per source file.
			baseName := mezTestSourceID(src)
			videoMezDir := path.Join(mezDir, baseName)
			err := os.MkdirAll(videoMezDir, 0755)
			require.NoError(t, err)

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
					require.NotEmpty(t, probeInfo.Streams, "no streams in %s", partFile)

					// Check video stream properties
					for _, si := range probeInfo.Streams {
						if si.CodecType == "video" {
							assert.Equal(t, 720, si.Height, "height mismatch")
							assert.Equal(t, 1280, si.Width, "width mismatch")
							log.Info("Probe result",
								"codec", si.CodecName,
								"profile", si.Profile,
								"level", si.Level,
								"timescale", si.TimeBase,
								"primaries", si.ColorPrimaries,
								"transfer", si.ColorTransfer,
								"space", si.ColorSpace,
								"range", si.ColorRange)
							// Color metadata must round-trip from source to mez 'colr' atom.
							mezColor := videoColor{
								Primaries: si.ColorPrimaries,
								Transfer:  si.ColorTransfer,
								Space:     si.ColorSpace,
								Range:     si.ColorRange,
							}
							assertColorMatches(t, srcColor, mezColor, "mez", partFile, false)
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
// - Re-runs the mez creation if parts are not already on disk.
// - Encrypted segments are decrypted before validation.
//
// Validation:
//   - Init segment codec config (SPS/PPS/VPS) is identical across parts
//   - Consecutive DTS and equal frame durations within and across chunks
//   - Correct segment/fragment numbering across parts
//   - Expected frame counts (except the last segment)
func TestABRCreate(t *testing.T) {
	mezDir := mezOutputDir(t)
	segsDir := segsOutputDir(t)
	if *deleteMezOutput {
		t.Cleanup(func() { removeAll(mezDir) })
	}
	if *deleteSegsOutput {
		t.Cleanup(func() { removeAll(segsDir) })
	}
	t.Skip("run via TestEndToEnd, TestEndToEndSingle, or TestEndToEndBT709Synthesis")
	runABRCreate(t, mezTestSources, mezDir, segsDir)
}

func runABRCreate(t *testing.T, sources []mezTestSource, mezDir, segsDir string) {
	t.Helper()
	for _, src := range sources {
		t.Run(mezTestSourceID(src), func(t *testing.T) {
			url := mezURL(src)
			skipIfFileMissing(t, url)

			profile := mezProfileForSource(src)
			baseName := mezTestSourceID(src)
			videoMezDir := path.Join(mezDir, baseName)

			// Ensure mez parts exist (create if needed)
			partFiles := ensureMezParts(t, src, profile, videoMezDir)
			require.NotEmpty(t, partFiles, "no mez parts for %s", src.Path)

			// Capture source color metadata so we can verify ABR init segments pass it through.
			srcColor := probeVideoColor(t, url)
			log.Info("Source color metadata",
				"source", url,
				"primaries", srcColor.Primaries,
				"transfer", srcColor.Transfer,
				"space", srcColor.Space,
				"range", srcColor.Range)

			for _, v := range abrVariants {
				// Skip watermark variants for sources that don't have watermarking enabled
				if v.NeedsWM && !src.Watermark {
					continue
				}
				t.Run(v.Name, func(t *testing.T) {
					// State carried across parts for continuity
					var (
						nextSegNum                      = 1 // next chunk file number
						nextFragIdx int32               = 1 // next mfhd sequence number
						nextPts     int64               = 0 // next starting PTS/DTS
						refInitInfo *xc.InitSegmentInfo     // init from the first part
						prevResult  *abrPartResult          // result from the previous part
					)

					for partIdx, partFile := range partFiles {
						isLastPart := partIdx == len(partFiles)-1
						partName := fmt.Sprintf("part_%02d", partIdx+1)
						abrDir := path.Join(segsDir, baseName, v.RepDir, partName)

						t.Run(partName, func(t *testing.T) {
							result := generateAndValidateABRPart(t, v, partFile, abrDir, profile,
								nextSegNum, nextFragIdx, nextPts, isLastPart)

							// Part boundary continuity: verify first segment of this part follows last of previous part
							if prevResult != nil {
								assert.Equal(t, uint64(prevResult.NextPts), result.FirstDts,
									"DTS gap between part %d and part %d", partIdx, partIdx+1)
								assert.Equal(t, uint32(prevResult.NextFragIdx), result.FirstSeq,
									"fragment sequence gap between part %d and part %d", partIdx, partIdx+1)
							}

							// Verify init segments are codec-compatible across parts.
							// Use decrypted init for encrypted variants.
							initDir := abrDir
							if v.Encryption != goavpipe.CryptNone {
								initDir = filepath.Join(abrDir, "decrypted")
							}
							initFile := filepath.Join(initDir, "vinit-stream0.m4s")
							initInfo, iErr := xc.ValidateInitSegment(initFile)
							require.NoError(t, iErr, "ValidateInitSegment failed for %s", initFile)

							// Color metadata must round-trip from source through mez to ABR 'colr' atom.
							assertColorMatches(t, srcColor, probeVideoColor(t, initFile), "abr-init", initFile, true)

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
							prevResult = &result
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
	url := mezURL(src)
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

// abrPartResult carries a continuity state from one part to the next.
type abrPartResult struct {
	NextSegNum  int    // next chunk file number (for StartSegmentStr)
	NextFragIdx int32  // next mfhd sequence number (for StartFragmentIndex)
	NextPts     int64  // next PTS/DTS (for StartPts)
	FirstSeq    uint32 // actual first mfhd sequence number of this part
	FirstDts    uint64 // actual first DTS of this part
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

	// Probe the mez part to get timescale for VideoSegDurationTs
	goavpipe.InitIOHandler(
		&xc.FileInputOpener{URL: partFile},
		&xc.FileOutputOpener{Dir: abrDir},
	)
	probeParams := goavpipe.NewXcParams()
	probeParams.Url = partFile
	probeInfo, err := avpipe.Probe(probeParams)
	require.NoError(t, err, "Probe failed for %s", partFile)
	require.NotEmpty(t, probeInfo.Streams, "no streams in %s", partFile)

	var timescale int64
	for _, si := range probeInfo.Streams {
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
	case wmNone:
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
	if err = os.WriteFile(xcparamsFile, []byte(xcparamsContent), 0644); err != nil {
		log.Warn("failed to write file", "file", xcparamsFile, "err", err)
	}

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
		case goavpipe.CryptNone:
		case goavpipe.CryptCBC1:
		case goavpipe.CryptCENS:
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
	var firstChunkResult *xc.ABRSegmentResult
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

			if firstChunkResult == nil {
				firstChunkResult = result
			}
			lastChunkResult = result
		})
	}

	// Compute next-part state from the last chunk
	partResult := abrPartResult{
		NextSegNum: startSegNum + len(chunkFiles),
	}
	if firstChunkResult != nil {
		partResult.FirstSeq = firstChunkResult.SeqFirst
		partResult.FirstDts = firstChunkResult.DtsStart
	}
	if lastChunkResult != nil {
		partResult.NextFragIdx = int32(lastChunkResult.SeqLast) + 1
		partResult.NextPts = int64(lastChunkResult.DtsEnd)
	}
	return partResult
}

// runE2E runs both phases against the given sources and cleans up.
func runE2E(t *testing.T, sources []mezTestSource, mezDir, segsDir string) {
	t.Helper()
	t.Run("phase1-mez", func(t *testing.T) { runMezCreate(t, sources, mezDir) })
	t.Run("phase2-abr", func(t *testing.T) { runABRCreate(t, sources, mezDir, segsDir) })

	t.Cleanup(func() {
		if *deleteMezOutput {
			removeAll(mezDir)
		}
		if *deleteSegsOutput {
			removeAll(segsDir)
		}
	})
}

// TestEndToEnd runs all phases against the full mezTestSources matrix.
// Skipped under -short; use TestEndToEndSingle for quick feedback.
//
// Run:
// - Individual phases:  go test ./xc/ -run TestMezCreate
// - Full pipeline:      go test ./xc/ -run TestEndToEnd
func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("use TestEndToEndSingle for a quick smoke test")
	}
	runE2E(t, mezTestSources, mezOutputDir(t), segsOutputDir(t))
}

// TestEndToEndSingle runs the full mez + ABR pipeline on a single small source.
// Suitable for quick development feedback; not skipped under -short.
//
// Override the source file with -src-file (absolute path):
//
//	go test ./xc/ -run TestEndToEndSingle -src-file /abs/path/to/file.mov
func TestEndToEndSingle(t *testing.T) {
	src := mezTestSource{"bbb_1080p_30fps_10sec.mp4", "30/1", 60, false, ""}
	if *srcOverrideAbsFilePath != "" {
		src = mezSourceFromFile(t, *srcOverrideAbsFilePath)
	}
	runE2E(t, []mezTestSource{src}, mezOutputDir(t), segsOutputDir(t))
}

// TestEndToEndBT709Synthesis exercises dash_synthesize_color_defaults using a
// source that has color_range=tv but unknown primaries/trc/space. The ABR init
// segment must contain color_primaries=bt709, color_trc=bt709, color_space=bt709
// synthesized by dash_synthesize_color_defaults.
func TestEndToEndBT709Synthesis(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	runE2E(t, []mezTestSource{
		{"03_caminandes_llamigos_h265_1080p.mp4", "24/1", 48, false, ""},
	}, mezOutputDir(t), segsOutputDir(t))
}
