package xc_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/require"
)

// Replay generated TS through custom I/O with a UDP URL to exercise live ingest timing
// and GAP compensation deterministically, without loss, timeouts or imperfactions.
type deinterlaceInput struct{ source string }

func (i deinterlaceInput) Open(fd int64, _ string) (goavpipe.InputHandler, error) {
	return (&xc.FileInputOpener{}).Open(fd, i.source)
}

func TestDeinterlaceMez(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to generate interlaced test media")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is required to verify progressive output")
	}
	previousInput, previousOutput := goavpipe.GetGlobalInputOpener(), goavpipe.GetGlobalOutputOpener()
	t.Cleanup(func() { goavpipe.InitIOHandler(previousInput, previousOutput) })

	for _, rate := range []struct {
		name, progressive           string
		timescale, duration, keyint int32
		encoderTimescale            int32
	}{
		{"25i", "50", 12800, 256, 100, 12800},
		{"25i_legacy_timebase", "50", 12800, 256, 100, 50},
		{"2997i", "60000/1001", 60000, 1001, 120, 60000},
	} {
		for _, live := range []bool{false, true} {
			for _, gap := range []bool{false, true} {
				if gap && !live {
					continue
				}
				for _, override := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/live=%t/gap=%t/frame_duration=%t", rate.name, live, gap, override), func(t *testing.T) {
						// FileOutputOpener uses paths relative to the working directory.
						dir, err := os.MkdirTemp(".", "deinterlace-test-")
						require.NoError(t, err)
						t.Cleanup(func() { os.RemoveAll(dir) })
						source := filepath.Join(dir, "input.ts")
						inputFrames := int(15*rate.keyint/2) + 10
						filter := "testsrc2=size=320x180:rate=" + rate.progressive + ",interlace=scan=tff"
						if gap {
							// Skip four input-frame periods halfway through the source.
							filter += fmt.Sprintf(",setpts=PTS+if(gte(N\\,65)\\,4\\,0)*%d/%d/TB", 2*rate.duration, rate.timescale)
						}
						cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", filter,
							"-frames:v", fmt.Sprint(inputFrames), "-c:v", "mpeg2video", "-flags", "+ilme+ildct", "-top", "1",
							"-g", "12", "-bf", "0", "-fps_mode", "passthrough", "-f", "mpegts", source)
						output, err := cmd.CombinedOutput()
						require.NoError(t, err, "%s", output)

						params := goavpipe.NewXcParams()
						params.Url = source
						params.Format = "fmp4-segment"
						params.XcType = goavpipe.XcVideo
						params.Deinterlace = 1
						params.VideoTimeBase = int(rate.encoderTimescale)
						if override {
							params.VideoFrameDurationTs = int(rate.duration)
						}
						params.ForceKeyInt = rate.keyint
						params.VideoSegDurationTs = int64(15 * rate.keyint * rate.duration)
						params.SegDuration = ""
						params.Preset = "ultrafast"
						if live {
							params.Url = "udp://deinterlace-test:1234"
							params.InputCfg.BypassLibavReader = true
						}
						if gap {
							params.VideoSegDurationTs *= 10 // Keep the gap in a single part.
						}
						goavpipe.InitIOHandler(deinterlaceInput{source}, &xc.FileOutputOpener{Dir: dir})
						require.NoError(t, avpipe.Xc(params))

						parts, err := filepath.Glob(filepath.Join(dir, "vsegment-*.mp4"))
						require.NoError(t, err)
						sort.Strings(parts)
						if gap {
							require.Len(t, parts, 1)
						} else {
							require.Len(t, parts, 2)
						}
						total := 0
						for partIndex, part := range parts {
							probeOutput, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
								"-show_entries", "stream=r_frame_rate:frame=interlaced_frame", "-of", "json", part).CombinedOutput()
							require.NoError(t, err, "%s", probeOutput)
							var probe struct {
								Streams []struct {
									Rate string `json:"r_frame_rate"`
								} `json:"streams"`
								Frames []struct {
									Interlaced int `json:"interlaced_frame"`
								} `json:"frames"`
							}
							require.NoError(t, json.Unmarshal(probeOutput, &probe))
							require.Len(t, probe.Streams, 1)
							if !gap {
								expectedRate := rate.progressive
								if expectedRate == "50" {
									expectedRate = "50/1"
								}
								require.Equal(t, expectedRate, probe.Streams[0].Rate)
							}
							for _, frame := range probe.Frames {
								require.Zero(t, frame.Interlaced)
							}
							frames, _, err := xc.SourceMP4FrameTimings(part)
							require.NoError(t, err)
							require.NotEmpty(t, frames)
							if !gap && partIndex == 0 {
								require.Len(t, frames, int(15*rate.keyint))
							}
							for n, frame := range frames {
								if !gap || override {
									require.Equal(t, uint32(rate.duration), frame.Duration)
									require.Equal(t, int64(n)*int64(rate.duration), frame.PTS)
								}
								if !gap {
									require.Equal(t, n%int(rate.keyint) == 0, frame.Sync, "keyframe at output frame %d", total+n)
								}
							}
							require.Len(t, probe.Frames, len(frames), "all samples must decode")
							if gap {
								elapsedFrames := 2*inputFrames - 1
								if !override {
									elapsedFrames += 8
								}
								require.Equal(t, int64(elapsedFrames)*int64(rate.duration), frames[len(frames)-1].PTS,
									"source timing is preserved only when no duration override is supplied")
							}
							total += len(frames)
						}
						require.Equal(t, 2*inputFrames, total, "both fields, including buffered frames at EOF, must be encoded")
					})
				}
			}
		}
	}
}

// Validate encoder timebase before encoding, including alternating 1501/1502
// ticks for 59.94p with the inherited MPEG-TS 1/90000 timebase.
func TestVideoSegmentGOPAlignment(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to generate interlaced test media")
	}
	dir, err := os.MkdirTemp(".", "segment-alignment-test-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	source := filepath.Join(dir, "input.ts")
	output, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "testsrc2=size=320x180:rate=60000/1001,interlace=scan=tff", "-frames:v", "30",
		"-c:v", "mpeg2video", "-flags", "+ilme+ildct", "-top", "1", "-bf", "0", "-f", "mpegts", source).CombinedOutput()
	require.NoError(t, err, "%s", output)
	previousInput, previousOutput := goavpipe.GetGlobalInputOpener(), goavpipe.GetGlobalOutputOpener()
	t.Cleanup(func() { goavpipe.InitIOHandler(previousInput, previousOutput) })
	for _, tc := range []struct {
		name      string
		timescale int
		ticks     int64
		seconds   string
		invalid   bool
	}{
		{"explicit_misaligned", 60000, 2702700, "45.045", true},
		{"explicit_aligned", 60000, 1801800, "30.03", false},
		{"ticks_override_misaligned_seconds", 60000, 1801800, "45.045", false},
		{"ticks_override_aligned_seconds", 60000, 2702700, "30.03", true},
		{"inherited_misaligned", 0, 4054050, "", true},
		{"inherited_aligned", 0, 2702700, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputDir := filepath.Join(dir, tc.name)
			require.NoError(t, os.Mkdir(outputDir, 0755))
			params := goavpipe.NewXcParams()
			params.Url, params.Format, params.XcType = source, "fmp4-segment", goavpipe.XcVideo
			params.Deinterlace, params.ForceKeyInt = 1, 120
			params.VideoTimeBase, params.VideoSegDurationTs = tc.timescale, tc.ticks
			params.SegDuration, params.Preset = tc.seconds, "ultrafast"
			goavpipe.InitIOHandler(deinterlaceInput{source}, &xc.FileOutputOpener{Dir: outputDir})
			err := avpipe.Xc(params)
			if tc.invalid {
				require.ErrorIs(t, err, avpipe.EAV_PARAM)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVideoEncoderTimebase(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is required to generate test media")
	}
	previousInput, previousOutput := goavpipe.GetGlobalInputOpener(), goavpipe.GetGlobalOutputOpener()
	t.Cleanup(func() { goavpipe.InitIOHandler(previousInput, previousOutput) })
	dir, err := os.MkdirTemp(".", "timebase-test-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, rate := range []string{"50", "60000/1001"} {
		source := filepath.Join(dir, fmt.Sprintf("input-%s.ts", map[string]string{"50": "25i", "60000/1001": "2997i"}[rate]))
		output, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
			"-i", "testsrc2=size=320x180:rate="+rate+",interlace=scan=tff", "-frames:v", "30",
			"-c:v", "mpeg2video", "-flags", "+ilme+ildct", "-top", "1", "-bf", "0", "-f", "mpegts", source).CombinedOutput()
		require.NoError(t, err, "%s", output)
	}
	for _, tc := range []struct {
		name, source                     string
		deinterlace, timescale, duration int
		valid                            bool
	}{
		{"25i_inherited", "25i", 1, 0, 0, true},
		{"25i_explicit_25_rejected", "25i", 1, 25, 0, false},
		{"25i_explicit_50", "25i", 1, 50, 0, true},
		{"25i_send_frame_25", "25i", 2, 25, 0, true},
		{"25i_override_does_not_hide_bad_timebase", "25i", 1, 25, 256, false},
		{"2997i_inherited_fractional_ticks", "2997i", 1, 0, 0, true},
		{"2997i_explicit_90000_fractional_ticks", "2997i", 1, 90000, 0, true},
		{"2997i_explicit_60000", "2997i", 1, 60000, 0, true},
		{"2997i_regular_inherited", "2997i", 0, 0, 0, true},
		{"2997i_regular_fractional_ticks", "2997i", 0, 10000, 0, true},
		{"2997i_regular_25_rejected", "2997i", 0, 25, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outputDir := filepath.Join(dir, tc.name)
			require.NoError(t, os.Mkdir(outputDir, 0755))
			params := goavpipe.NewXcParams()
			params.Url = filepath.Join(dir, "input-"+tc.source+".ts")
			params.Format = "fmp4-segment"
			params.XcType = goavpipe.XcVideo
			params.Deinterlace = tc.deinterlace
			params.VideoTimeBase = tc.timescale
			params.VideoFrameDurationTs = tc.duration
			params.Preset = "ultrafast"
			goavpipe.InitIOHandler(deinterlaceInput{params.Url}, &xc.FileOutputOpener{Dir: outputDir})
			err := avpipe.Xc(params)
			if tc.valid {
				require.NoError(t, err)
				if tc.deinterlace == 1 && tc.source == "2997i" && (tc.timescale == 0 || tc.timescale == 90000) {
					parts, err := filepath.Glob(filepath.Join(outputDir, "vsegment-*.mp4"))
					require.NoError(t, err)
					require.Len(t, parts, 1)
					frames, _, err := xc.SourceMP4FrameTimings(parts[0])
					require.NoError(t, err)
					require.Len(t, frames, 60)
					seen := map[uint32]bool{}
					for n, frame := range frames {
						require.Contains(t, []uint32{1501, 1502}, frame.Duration)
						seen[frame.Duration] = true
						// Compare doubled PTS to the exact 3003/2-tick cadence.
						require.InDelta(t, int64(n)*3003, 2*(frame.PTS-frames[0].PTS), 2,
							"timestamp error must remain within one encoder tick")
					}
					require.Len(t, seen, 2, "both adjacent durations must be used")
				}
			} else {
				require.Equal(t, avpipe.EAV_TIMEBASE, err)
			}
		})
	}
}
