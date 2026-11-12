package xc_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/mp4e"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/require"
)

const hdrCrashPart = "hqpeYP9znPQgk8SwmqZrMna61q22ZxUsJpM1txSxPrLnAukszkmtH"

var (
	hdrCrashSource    = flag.String("abr-crash-source", "../TST/"+hdrCrashPart+".mp4", "crash replay mezzanine part (relative to xc/ or absolute)")
	hdrCrashEncoder   = flag.String("abr-crash-encoder", "libx265", "crash replay encoder: libx265 (CPU) or hevc_nvenc (GPU)")
	hdrCrashTranscode = flag.Bool("abr-crash-transcode", false, "also replay the 5120x1440 transcode following bypass (expensive on CPU)")
	hdrCrashGPU       = flag.Int("abr-crash-gpu", 0, "NVENC GPU index for transcode; bypass always uses -1 as logged")
)

// TestABRHDRCrashReplay replays the two XCPARAMS records in qfab.log at
// 2026-11-12T15:25:51.812Z and 15:25:52.664Z (repeated at 15:29:19/20).
// The default is CPU-only bypass. Explicitly select hevc_nvenc on the GPU host
// and enable transcode to exercise nvenc_set_mastering_display_data.
func TestABRHDRCrashReplay(t *testing.T) {
	if *hdrCrashEncoder != "libx265" && *hdrCrashEncoder != "hevc_nvenc" {
		t.Fatalf("unsupported -abr-crash-encoder=%s", *hdrCrashEncoder)
	}
	skipIfFileMissing(t, *hdrCrashSource)
	sourceFrames, _, err := xc.SourceMP4FrameTimings(*hdrCrashSource)
	require.NoError(t, err)
	require.Len(t, sourceFrames, 720, "expected the complete 30.030s incident part")
	sourceHDR := readCrashHDR(t, *hdrCrashSource)
	require.EqualValues(t, 7680, sourceHDR.Video.Width)
	require.EqualValues(t, 2160, sourceHDR.Video.Height)

	for _, bypass := range []bool{true, false} {
		name := "bypass"
		if !bypass {
			name = "transcode"
		}
		t.Run(name, func(t *testing.T) {
			if !bypass && !*hdrCrashTranscode {
				t.Skip("enable -abr-crash-transcode to exercise encoding and NVENC metadata on a GPU host")
			}
			// Use a zero-valued struct: NewXcParams adds CRF, segment duration,
			// audio and bitdepth defaults that differ from the incident logs.
			params := &goavpipe.XcParams{
				Url: *hdrCrashSource, StreamId: -1, XcType: goavpipe.XcVideo,
				Format: "dash", BypassTranscoding: bypass, Seekable: false,
				StartPts: 0, StartTimeTs: 0, DurationTs: 720720,
				StartSegmentStr: "1", StartFragmentIndex: 1,
				VideoSegDurationTs: 48048, ForceKeyInt: 48,
				Ecodec: *hdrCrashEncoder, GPUIndex: -1,
				EncWidth: 7680, EncHeight: 2160, VideoBitrate: 23000000,
				Profile: "main10", MaxCLL: "0,0",
				MasterDisplay: "G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,10)",
			}
			if !bypass {
				params.EncWidth, params.EncHeight, params.VideoBitrate = 5120, 1440, 17000000
				if *hdrCrashEncoder == "hevc_nvenc" {
					params.GPUIndex = int32(*hdrCrashGPU)
				}
			}
			outDir := filepath.Join(testOutBase, "xc_test."+t.Name(), *hdrCrashEncoder)
			resetDir(t, outDir)
			encoded, err := json.MarshalIndent(params, "", "  ")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(outDir, "XCPARAMS.json"), encoded, 0644))
			t.Logf("replay bypass=%t encoder=%s gpu=%d %dx%d bitrate=%d; output=%s",
				bypass, params.Ecodec, params.GPUIndex, params.EncWidth, params.EncHeight, params.VideoBitrate, outDir)
			goavpipe.InitUrlIOHandler(params.Url, &xc.FileInputOpener{}, &xc.FileOutputOpener{Dir: outDir})
			require.NoError(t, avpipe.Xc(params))

			initPath := filepath.Join(outDir, "vinit-stream0.m4s")
			require.FileExists(t, initPath)
			require.FileExists(t, filepath.Join(outDir, "dash.mpd"))
			chunks, err := filepath.Glob(filepath.Join(outDir, "vchunk-stream0-*.m4s"))
			require.NoError(t, err)
			sort.Strings(chunks)
			require.Len(t, chunks, 15)
			for i, chunk := range chunks {
				require.Equal(t, fmt.Sprintf("vchunk-stream0-%05d.m4s", i+1), filepath.Base(chunk))
				result, err := xc.ValidateABRSegment(chunk)
				require.NoError(t, err)
				require.NoError(t, result.AllErrors(), chunk)
				require.Equal(t, 48, result.FrameCount, chunk)
				require.EqualValues(t, 1001, result.SampleDur, chunk)
			}
			output, err := xc.DashChunkFrameTimings(chunks)
			require.NoError(t, err)
			require.Empty(t, output.ContinuityMismatches)
			require.Equal(t, 720, output.FrameCount)
			if bypass {
				require.NoError(t, xc.AssertSourceOutputFrameTimings(sourceFrames, output.Frames, 0))
				require.NoError(t, xc.AssertDashMPDSegmentTimelineStart(outDir, sourceFrames, 0))
			}
			// Parse init plus first chunk together to inspect both hvcC/colr
			// and in-band SEI, without requiring an external ffprobe executable.
			hdr := readCrashHDR(t, initPath, chunks[0])
			require.EqualValues(t, params.EncWidth, hdr.Video.Width)
			require.EqualValues(t, params.EncHeight, hdr.Video.Height)
			require.NotNil(t, hdr.SEI.Mastering, "mastering display SEI missing")
			require.Equal(t, sourceHDR.SEI.Mastering, hdr.SEI.Mastering)
			t.Logf("validated 15 segments, 720 frames, 1001 ticks/frame, HDR10 and mastering display metadata")
		})
	}
}

func readCrashHDR(t *testing.T, paths ...string) *mp4e.HDRInfo {
	t.Helper()
	var data bytes.Buffer
	for _, path := range paths {
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		data.Write(b)
	}
	file, err := mp4.DecodeFile(bytes.NewReader(data.Bytes()))
	require.NoError(t, err)
	hdr, err := mp4e.ValidateHDR(file)
	require.NoError(t, err)
	require.Empty(t, hdr.SEI.ParseErrors)
	require.True(t, hdr.HvcC.Present)
	require.EqualValues(t, 2, hdr.HvcC.ProfileIDC)
	require.EqualValues(t, 10, hdr.HvcC.BitDepthLuma)
	require.EqualValues(t, 10, hdr.HvcC.BitDepthChroma)
	require.True(t, hdr.Colr.Present)
	require.EqualValues(t, 9, hdr.Colr.ColorPrimaries)
	require.EqualValues(t, 16, hdr.Colr.TransferCharacteristics)
	require.EqualValues(t, 9, hdr.Colr.MatrixCoefficients)
	require.NotNil(t, hdr.SEI.Mastering)
	return hdr
}
