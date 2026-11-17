package xc_test

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/internal/testutil"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Temporary incident diagnostic; keep the generated manifests, init and chunks.
// Run: go test ./xc -run '^TestABRTimingDiagnostic$' -count=1 -v
func TestABRTimingDiagnostic(t *testing.T) {
	const source = "../DOWNLOAD/tqw__HSRgApSf2E77BWvMeHvTJUz6VE5Qiw5WAPPtXMwtDrUvjLEpSBy1JJJbu6vTef9hGV3j2JT4XFasSDCxZVR.0/video/hqt_BcFhKjte8ymiqaFqCdfPV8KsCWzg4K7ZtMwuSs8V.mp4"
	skipIfFileMissing(t, source)
	t.Run("original_vui", func(t *testing.T) { runABRTimingDiagnostic(t, source) })
	t.Run("normalized_vui", func(t *testing.T) {
		ffmpeg, err := exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg required for the VUI-only control input")
		}
		dir := filepath.Join(testOutBase, "xc_test.TestABRTimingDiagnostic", "control-input")
		resetDir(t, dir)
		control := filepath.Join(dir, "vui-50fps.mp4")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", source,
			"-map", "0:v:0", "-c:v", "copy", "-bsf:v", "h264_metadata=tick_rate=100",
			"-video_track_timescale", "90000", "-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-y", control).CombinedOutput()
		require.NoError(t, err, "%s", out)
		original, _, err := xc.SourceMP4FrameTimings(source)
		require.NoError(t, err)
		normalized, _, err := xc.SourceMP4FrameTimings(control)
		require.NoError(t, err)
		for i := range original {
			original[i].Chunk = ""
			original[i].NewSegment = false
		}
		for i := range normalized {
			normalized[i].Chunk = ""
			normalized[i].NewSegment = false
		}
		require.Equal(t, original, normalized, "VUI control must preserve sample timing and keyframes")
		runABRTimingDiagnostic(t, control)
	})
}

func runABRTimingDiagnostic(t *testing.T, source string) {
	t.Helper()
	logDiagnosticVUI(t, "source", source, false)
	sourceFrames, _, err := xc.SourceMP4FrameTimings(source)
	require.NoError(t, err)
	require.NotEmpty(t, sourceFrames)
	t.Logf("source: frames=%d first sample duration=%d ticks", len(sourceFrames), sourceFrames[0].Duration)

	for _, encoder := range []string{"libx264", "h264_nvenc", "h264_videotoolbox"} {
		t.Run(encoder, func(t *testing.T) {
			if encoder == "h264_nvenc" && !testutil.NvidiaExist() {
				t.Skip("NVIDIA GPU unavailable")
			}
			if encoder == "h264_videotoolbox" {
				if runtime.GOOS != "darwin" {
					t.Skip("VideoToolbox requires macOS")
				}
				// Require hardware encoding; a software fallback would hide GPU coverage.
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				out, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i",
					"color=size=1280x720:rate=50", "-frames:v", "1", "-c:v", encoder,
					"-allow_sw", "0", "-f", "null", "-").CombinedOutput()
				if err != nil {
					t.Skipf("VideoToolbox hardware preflight unavailable: %v: %s", err, out)
				}
			}
			for _, format := range []string{"dash", "hls"} {
				t.Run(format, func(t *testing.T) {
					outDir := filepath.Join(testOutBase, "xc_test."+t.Name())
					resetDir(t, outDir)
					p := goavpipe.NewXcParams()
					p.Url, p.Format, p.XcType = source, format, goavpipe.XcVideo
					p.Ecodec, p.Profile = encoder, "high"
					p.EncWidth, p.EncHeight, p.VideoBitrate = 1280, 720, 4000000
					p.CrfStr = ""
					p.SegDuration, p.VideoSegDurationTs = "2", 180000
					p.ForceKeyInt = 100 // Two seconds at the source's nominal 50 fps.
					// Deliberately do not override VideoTimeBase or VideoFrameDurationTs.
					if encoder == "libx264" {
						p.Preset = "medium"
					} else if encoder == "h264_nvenc" {
						p.GPUIndex, p.Preset = 0, "p4"
					}
					params, err := json.MarshalIndent(p, "", "  ")
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(filepath.Join(outDir, "XCPARAMS.json"), params, 0644))
					t.Logf("parameters: %s; retained output: %s", params, outDir)
					goavpipe.InitUrlIOHandler(source, &xc.FileInputOpener{}, &xc.FileOutputOpener{Dir: outDir})
					captureDiagnosticLogs(t, outDir)
					require.NoError(t, avpipe.Xc(p))

					initPath := filepath.Join(outDir, "vinit-stream0.m4s")
					logDiagnosticVUI(t, "output init", initPath, true)
					chunks, err := filepath.Glob(filepath.Join(outDir, "vchunk-stream0-*.m4s"))
					require.NoError(t, err)
					sort.Strings(chunks)
					require.NotEmpty(t, chunks)
					assert.Len(t, chunks, 15, "30-second source must produce fifteen 2-second segments")
					totalFrames := 0
					var previousEnd uint64
					for i, chunk := range chunks {
						result, err := xc.ValidateABRSegment(chunk)
						require.NoError(t, err)
						t.Logf("%s: frames=%d duration/sample=%d DTS=[%d,%d) validation=%v",
							filepath.Base(chunk), result.FrameCount, result.SampleDur, result.DtsStart, result.DtsEnd, result.AllErrors())
						assert.NoError(t, result.AllErrors(), chunk)
						assert.EqualValues(t, 1800, result.SampleDur, chunk)
						assert.Equal(t, previousEnd, result.DtsStart, "segment boundary continuity: %s", chunk)
						previousEnd = result.DtsEnd
						if i < len(chunks)-1 {
							assert.Equal(t, 100, result.FrameCount, "full 2-second segment: %s", chunk)
							assert.EqualValues(t, 180000, result.DtsEnd-result.DtsStart, chunk)
						}
						totalFrames += result.FrameCount
					}
					assert.Equal(t, len(sourceFrames), totalFrames, "all source frames must survive")
					assert.EqualValues(t, 2700000, previousEnd, "output must still span 30 seconds")
				})
			}
		})
	}
}

// Save this encode's C logs beside its output, and show the timing checkpoints
// in go test -v so the GPU run does not require digging through shared logs.
func captureDiagnosticLogs(t *testing.T, outDir string) {
	t.Helper()
	logfile, err := os.Open(filepath.Join(testOutBase, "avpipe-test.log"))
	require.NoError(t, err)
	start, err := logfile.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	t.Cleanup(func() {
		defer logfile.Close()
		end, err := logfile.Stat()
		require.NoError(t, err)
		require.GreaterOrEqual(t, end.Size(), start, "shared log rotated during diagnostic")
		reader := io.NewSectionReader(logfile, start, end.Size()-start)
		output, err := os.Create(filepath.Join(outDir, "avpipe.log"))
		require.NoError(t, err)
		_, err = io.Copy(output, reader)
		require.NoError(t, err)
		require.NoError(t, output.Close())
		_, err = reader.Seek(0, io.SeekStart)
		require.NoError(t, err)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		extradataPattern := regexp.MustCompile(`VIDEO TIMING extradata stage=after-open .* bytes=(\d+) shown=(\d+) hex=([0-9a-f]+)`)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "VIDEO TIMING") {
				t.Log(scanner.Text())
			}
			match := extradataPattern.FindStringSubmatch(scanner.Text())
			if match == nil || match[1] != match[2] {
				continue
			}
			data, err := hex.DecodeString(match[3])
			require.NoError(t, err)
			sps, _ := avc.GetParameterSetsFromByteStream(data)
			if len(data) > 0 && data[0] == 1 {
				record, err := avc.DecodeAVCDecConfRec(data)
				require.NoError(t, err)
				sps = record.SPSnalus
			}
			logDiagnosticSPS(t, "encoder before mux", sps, true)
			initPath := filepath.Join(outDir, "vinit-stream0.m4s")
			if _, err := os.Stat(initPath); err == nil {
				init, err := xc.ValidateInitSegment(initPath)
				require.NoError(t, err)
				assert.Equal(t, sps, init.SPS, "encoder SPS must reach init unchanged; mismatch points to muxer rewriting")
			}
		}
		require.NoError(t, scanner.Err())
	})
}

func logDiagnosticVUI(t *testing.T, label, filename string, check bool) {
	t.Helper()
	info, err := xc.ValidateInitSegment(filename)
	require.NoError(t, err)
	t.Logf("%s: track timescale=%d codec=%s %dx%d", label, info.Timescale, info.Codec, info.Width, info.Height)
	if check {
		assert.EqualValues(t, 90000, info.Timescale, label)
	}
	logDiagnosticSPS(t, label, info.SPS, check)
}

func logDiagnosticSPS(t *testing.T, label string, nalus [][]byte, check bool) {
	t.Helper()
	require.NotEmpty(t, nalus, "%s: SPS required", label)
	for _, nalu := range nalus {
		sps, err := avc.ParseSPSNALUnit(nalu, true)
		require.NoError(t, err)
		if sps.VUI == nil || !sps.VUI.TimingInfoPresentFlag {
			t.Logf("%s: SPS has no VUI timing", label)
			if check {
				assert.Fail(t, "output SPS must contain VUI timing", label)
			}
			continue
		}
		vui := sps.VUI
		require.Positive(t, vui.NumUnitsInTick, label)
		t.Logf("%s: VUI time_scale=%d num_units_in_tick=%d fixed_frame_rate_flag=%t frame_mbs_only=%t",
			label, vui.TimeScale, vui.NumUnitsInTick, vui.FixedFrameRateFlag, sps.FrameMbsOnlyFlag)
		t.Logf("%s: VUI clock ratio=%.6f; fixed=%t (a variable-rate clock ratio is not necessarily fps)",
			label, float64(vui.TimeScale)/(2*float64(vui.NumUnitsInTick)), vui.FixedFrameRateFlag)
		// H.264 timing uses two clock ticks per frame. With variable-rate
		// signaling this ratio can describe clock resolution rather than fps.
		if check && vui.FixedFrameRateFlag {
			assert.EqualValues(t, 100*vui.NumUnitsInTick, vui.TimeScale, "VUI must signal 50 fps")
		}
	}
}
