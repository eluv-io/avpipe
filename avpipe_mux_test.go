package avpipe_test

import (
	"os"
	"path"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/elvxc/cmd"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
)

// TestMuxAACFrameDurationFromContainer muxes an AAC track whose samples last 2048 ticks of its timescale, the
// duration of an HE-AAC frame at the output sample rate. The muxed audio must last as long as its input. A frame
// duration fixed at 1024 ticks used to halve it.
func TestMuxAACFrameDurationFromContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	f := fn()
	url := "./media/case_1_video_and_mono_audio.mp4"
	checkFileExists(t, url)

	videoDir := path.Join(baseOutPath, f, "video")
	audioDir := path.Join(baseOutPath, f, "audio")
	muxDir := path.Join(baseOutPath, f, "mux")

	// One self-contained fragmented mp4 per stream: the first 30 s of video and 15 s of audio, which last 30 s too once
	// stretched. The mux ends with the first stream to end, so the streams must last as long.
	setupOutDir(t, videoDir)
	params := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec:              h264Codec,
		XcType:              goavpipe.XcVideo,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	setFastEncodeParams(params, true)
	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: videoDir})
	boilerXc(t, params)

	setupOutDir(t, audioDir)
	params = &goavpipe.XcParams{
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "15",
		Ecodec2:             "aac",
		XcType:              goavpipe.XcAudio,
		ChannelLayout:       avpipe.ChannelLayout("mono"),
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		DebugFrameLevel:     debugFrameLevel,
	}
	params.AudioIndex = []int32{1}
	goavpipe.InitUrlIOHandler(url, &xc.FileInputOpener{URL: url}, &xc.FileOutputOpener{Dir: audioDir})
	boilerXc(t, params)

	video := path.Join(videoDir, "vsegment-1.mp4")
	stretched := path.Join(audioDir, "asegment0-1-x2.mp4")
	doubleSampleDurations(t, path.Join(audioDir, "asegment0-1.mp4"), stretched)

	setupOutDir(t, muxDir)
	out := path.Join(muxDir, "muxed.mp4")
	goavpipe.InitUrlMuxIOHandler(out, &cmd.AVCmdMuxInputOpener{URL: out}, &cmd.AVCmdMuxOutputOpener{})
	defer goavpipe.RemoveUrlMuxIOHandler(out)
	err := avpipe.Mux(&goavpipe.XcParams{
		MuxingSpec: "abr-mux\nvideo,1," + video + "\naudio,1," + stretched + "\n",
		Format:     "mp4",
		Url:        out,
	})
	failNowOnError(t, err)

	want := probeAudioSeconds(t, stretched)
	require.InDelta(t, 30, want, 0.5, "the stretched input lasts twice its 15 s of content")
	require.InDelta(t, want, probeAudioSeconds(t, out), 0.2)
}

// doubleSampleDurations writes a copy of the fragmented mp4 file src to dst in which every sample lasts twice as
// long, keeping the timescale. The samples themselves are unchanged.
func doubleSampleDurations(t *testing.T, src, dst string) {
	in, err := os.Open(src)
	require.NoError(t, err)
	defer func() { _ = in.Close() }()
	mf, err := mp4.DecodeFile(in)
	require.NoError(t, err)
	require.NotNil(t, mf.Init)
	require.NotEmpty(t, mf.Segments)

	if trex := mf.Init.Moov.Mvex.Trex; trex != nil {
		trex.DefaultSampleDuration *= 2
	}
	for _, seg := range mf.Segments {
		for _, frag := range seg.Fragments {
			traf := frag.Moof.Traf
			if traf.Tfhd.HasDefaultSampleDuration() {
				traf.Tfhd.DefaultSampleDuration *= 2
			}
			if traf.Tfdt != nil {
				traf.Tfdt.SetBaseMediaDecodeTime(traf.Tfdt.BaseMediaDecodeTime() * 2)
			}
			for _, trun := range traf.Truns {
				if trun.HasSampleDuration() {
					for i := range trun.Samples {
						trun.Samples[i].Dur *= 2
					}
				}
			}
		}
	}

	out, err := os.Create(dst)
	require.NoError(t, err)
	defer func() { _ = out.Close() }()
	require.NoError(t, mf.Encode(out))
}

// probeAudioSeconds returns the duration of the audio stream of the given file in seconds.
func probeAudioSeconds(t *testing.T, file string) float64 {
	goavpipe.InitUrlIOHandler(file, &xc.FileInputOpener{URL: file}, &xc.FileOutputOpener{Dir: path.Dir(file)})
	info, err := avpipe.Probe(&goavpipe.XcParams{Url: file, Seekable: true})
	require.NoError(t, err)
	audio := info.StreamByCodecType("audio")
	require.NotNil(t, audio)
	seconds, _ := audio.TimeBase.Float64()
	return float64(audio.DurationTs) * seconds
}
