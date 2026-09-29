package avpipe_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
)

// Tests for XcAudioWaveform: decode audio only and report per-bucket min/max sample values through
// AV_IN_STAT_AUDIO_WAVEFORM, with no encoder, muxer or output.

const (
	waveformStereoAAC  = "./media/bbb-audio-stereo-2min.aac"                // 48 kHz stereo AAC-LC, 120 s
	waveform51AAC      = "./media/case_1_video_and_5.1_audio.mp4"           // stream 1: 44.1 kHz 5.1 AAC, 2056192 samples
	waveformPCM        = "./media/TOS8_Audio_51-2_60s_CCBYblendercloud.mov" // pcm_s24le 48 kHz, stream 6 is stereo
	waveformMultiAudio = "./media/caminandes_llamigos_1080p_4audios.mp4"    // streams 1-4: 48 kHz stereo AAC
)

func waveformParams(url string, audioIndex []int32, spp int32) *goavpipe.XcParams {
	return &goavpipe.XcParams{
		Url:                     url,
		XcType:                  goavpipe.XcAudioWaveform,
		StreamId:                -1,
		SyncAudioToStreamId:     -1,
		AudioIndex:              audioIndex,
		DurationTs:              -1,
		Seekable:                true,
		WaveformSamplesPerPixel: spp,
		DebugFrameLevel:         debugFrameLevel,
	}
}

// runWaveform runs an audio-waveform transcode over a file and returns the collected stats.
func runWaveform(t *testing.T, params *goavpipe.XcParams) *xc.IOStats {
	t.Helper()
	checkFileExists(t, params.Url)
	stats := &xc.IOStats{}
	goavpipe.InitUrlIOHandler(params.Url, &xc.FileInputOpener{URL: params.Url, Stats: stats}, goavpipe.NoopOutputOpener{})
	require.NoError(t, avpipe.Xc(params))
	return stats
}

// checkWaveformStream checks the structural invariants of one collected stream: contiguous batches, exactly one
// terminal batch, min <= max everywhere, values present.
func checkWaveformStream(t *testing.T, stats *xc.IOStats, streamIndex int, channels, sampleRate int,
	spp int32) *goavpipe.CollectedWaveform {

	t.Helper()
	w := stats.Waveform.Stream(streamIndex)
	require.NotNil(t, w, "no waveform for stream %d", streamIndex)
	require.True(t, w.Complete, "terminal batch missing")
	require.Equal(t, channels, w.Channels)
	require.Equal(t, sampleRate, w.SampleRate)
	require.Equal(t, int(spp), w.SamplesPerPixel)
	require.Equal(t, int64(0), w.FirstBucketIndex)

	terminal := 0
	for _, b := range stats.WaveformBatches {
		if b.StreamIndex == streamIndex && b.IsLast {
			terminal++
		}
	}
	require.Equal(t, 1, terminal, "exactly one terminal batch")

	expectedBuckets := (w.TotalSamples + int64(spp) - 1) / int64(spp)
	require.Equal(t, expectedBuckets, int64(w.Length()), "bucket count from total samples")

	nonZero := false
	for i := 0; i+1 < len(w.MinMax); i += 2 {
		require.LessOrEqual(t, w.MinMax[i], w.MinMax[i+1], "min <= max at %d", i)
		if w.MinMax[i] != 0 || w.MinMax[i+1] != 0 {
			nonZero = true
		}
	}
	require.True(t, nonZero, "waveform is all zero")
	return w
}

// ffmpegBuckets decodes one audio stream with ffmpeg to interleaved s16 and buckets it the way avpipe does. It skips
// the test when ffmpeg is not installed.
func ffmpegBuckets(t *testing.T, url string, streamIndex, channels, spp int) []int16 {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available for the reference comparison")
	}
	cmd := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-i", url,
		"-map", "0:"+strconv.Itoa(streamIndex), "-f", "s16le", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())

	samples := len(out) / 2 / channels
	buckets := (samples + spp - 1) / spp
	res := make([]int16, buckets*channels*2)
	for b := 0; b < buckets; b++ {
		for ch := 0; ch < channels; ch++ {
			res[(b*channels+ch)*2] = 32767
			res[(b*channels+ch)*2+1] = -32768
		}
		last := min((b+1)*spp, samples)
		for s := b * spp; s < last; s++ {
			for ch := 0; ch < channels; ch++ {
				v := int16(binary.LittleEndian.Uint16(out[(s*channels+ch)*2:]))
				lo := &res[(b*channels+ch)*2]
				hi := &res[(b*channels+ch)*2+1]
				if v < *lo {
					*lo = v
				}
				if v > *hi {
					*hi = v
				}
			}
		}
	}
	return res
}

// requireBucketsClose compares two bucket arrays of the same layout, allowing a one-LSB rounding difference and a
// length difference of at most one bucket at the end.
func requireBucketsClose(t *testing.T, want, got []int16, channels int) {
	t.Helper()
	vpb := channels * 2
	wantN, gotN := len(want)/vpb, len(got)/vpb
	require.LessOrEqual(t, absInt(wantN-gotN), 1, "bucket count: want %d got %d", wantN, gotN)
	n := min(wantN, gotN) * vpb
	for i := 0; i < n; i++ {
		d := int(want[i]) - int(got[i])
		require.LessOrEqual(t, absInt(d), 1, "value %d (bucket %d): want %d got %d", i, i/vpb, want[i], got[i])
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestAudioWaveformStereoAAC(t *testing.T) {
	const spp = 256
	stats := runWaveform(t, waveformParams(waveformStereoAAC, nil, spp))
	w := checkWaveformStream(t, stats, 0, 2, 48000, spp)
	require.Len(t, stats.Waveform.Streams(), 1)

	// about two minutes at 48 kHz; the exact count is checked against the ffmpeg reference below
	require.Greater(t, w.TotalSamples, int64(100*48000))
	require.Equal(t, 1, w.TimeBaseNum)
	require.Equal(t, 28224000, w.TimeBaseDen)
	require.Greater(t, w.Batches, 1)

	requireBucketsClose(t, ffmpegBuckets(t, waveformStereoAAC, 0, 2, spp), w.MinMax, 2)
}

func TestAudioWaveform5_1(t *testing.T) {
	const spp = 256
	stats := runWaveform(t, waveformParams(waveform51AAC, []int32{1}, spp))
	w := checkWaveformStream(t, stats, 1, 6, 44100, spp)
	require.Equal(t, uint64(avpipe.ChannelLayout("5.1")), w.ChannelLayout)
	require.InDelta(t, 2056192, float64(w.TotalSamples), 2048)

	requireBucketsClose(t, ffmpegBuckets(t, waveform51AAC, 1, 6, spp), w.MinMax, 6)
}

func TestAudioWaveformPCM(t *testing.T) {
	// pcm_s24le decodes to s32; the integer path must match ffmpeg exactly
	const spp = 480
	stats := runWaveform(t, waveformParams(waveformPCM, []int32{6}, spp))
	w := checkWaveformStream(t, stats, 6, 2, 48000, spp)
	require.Equal(t, int64(2880480), w.TotalSamples)

	want := ffmpegBuckets(t, waveformPCM, 6, 2, spp)
	require.Equal(t, want, w.MinMax)
}

func TestAudioWaveformMultiStream(t *testing.T) {
	const spp = 1024
	stats := runWaveform(t, waveformParams(waveformMultiAudio, []int32{1, 2}, spp))
	require.Len(t, stats.Waveform.Streams(), 2)
	w1 := checkWaveformStream(t, stats, 1, 2, 48000, spp)
	w2 := checkWaveformStream(t, stats, 2, 2, 48000, spp)
	require.InDelta(t, 5412864, float64(w1.TotalSamples), 2048)
	require.InDelta(t, 5412864, float64(w2.TotalSamples), 2048)
	require.NotEqual(t, w1.MinMax, w2.MinMax, "the two streams carry different audio")
}

func TestAudioWaveformGridAlignment(t *testing.T) {
	// The same audio placed 100 samples into the global grid must yield the same buckets as the whole-file run
	// shifted by 100 samples: the first bucket is partial, holding spp-100 samples, and every later bucket boundary
	// moves accordingly. Verify by re-bucketing the ffmpeg reference with the offset.
	const spp = 256
	const offset = 100
	params := waveformParams(waveformStereoAAC, nil, spp)
	params.WaveformStartSample = offset
	stats := runWaveform(t, params)
	w := stats.Waveform.Stream(0)
	require.NotNil(t, w)
	require.True(t, w.Complete)
	require.Equal(t, int64(0), w.FirstBucketIndex)

	first := stats.WaveformBatches[0]
	require.Equal(t, int64(0), first.FirstBucketIndex)

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available for the reference comparison")
	}
	cmd := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-i", waveformStereoAAC, "-f", "s16le", "-")
	out, err := cmd.Output()
	require.NoError(t, err)
	// prepend offset samples of silence so the reference lands on the same grid
	shifted := append(make([]byte, offset*2*2), out...)
	samples := len(shifted) / 4
	buckets := (samples + spp - 1) / spp
	want := make([]int16, 0, buckets*4)
	for b := 0; b < buckets; b++ {
		lo := [2]int16{32767, 32767}
		hi := [2]int16{-32768, -32768}
		start := b * spp
		if b == 0 {
			start = offset // the silence padding is not part of the input
		}
		for s := start; s < min((b+1)*spp, samples); s++ {
			for ch := 0; ch < 2; ch++ {
				v := int16(binary.LittleEndian.Uint16(shifted[(s*2+ch)*2:]))
				lo[ch] = min(lo[ch], v)
				hi[ch] = max(hi[ch], v)
			}
		}
		want = append(want, lo[0], hi[0], lo[1], hi[1])
	}
	requireBucketsClose(t, want, w.MinMax, 2)
	require.Equal(t, int64(samples-offset), w.TotalSamples)
}

// nonSeekableInput hides the size of a file so avpipe treats it as a stream, as a fabric part reader would.
type nonSeekableInput struct {
	goavpipe.InputHandler
}

func (n *nonSeekableInput) Seek(int64, int) (int64, error) { return -1, fmt.Errorf("not seekable") }
func (n *nonSeekableInput) Size() int64                    { return -1 }

type nonSeekableOpener struct {
	inner *xc.FileInputOpener
}

func (o *nonSeekableOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {
	h, err := o.inner.Open(fd, url)
	if err != nil {
		return nil, err
	}
	return &nonSeekableInput{InputHandler: h}, nil
}

func TestAudioWaveformFmp4Segment(t *testing.T) {
	// Produce a mezzanine of 30 s fMP4 segments, then run the waveform over the second segment alone as a
	// non-seekable stream, the way a fabric part is read. Mezzanine segments carry local timestamps that restart at
	// 0, so the caller places the segment on the global bucket grid with WaveformStartSample; the pts-derived mode
	// would put it at bucket 0.
	const spp = 256
	checkFileExists(t, waveformStereoAAC)
	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	mezParams := &goavpipe.XcParams{
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		Dcodec:              "aac",
		AudioBitrate:        128000,
		SampleRate:          48000,
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 waveformStereoAAC,
		DebugFrameLevel:     debugFrameLevel,
	}
	goavpipe.InitUrlIOHandler(waveformStereoAAC, &xc.FileInputOpener{URL: waveformStereoAAC},
		&xc.FileOutputOpener{Dir: outputDir})
	require.NoError(t, avpipe.Xc(mezParams))

	segment := fmt.Sprintf("%s/asegment0-2.mp4", outputDir)
	checkFileExists(t, segment)
	whole := fmt.Sprintf("%s/asegment0-1.mp4", outputDir)
	checkFileExists(t, whole)

	// the first segment's sample count is where the second one starts on the global grid
	ref1 := runWaveform(t, waveformParams(whole, nil, spp))
	w1 := ref1.Waveform.Stream(0)
	require.NotNil(t, w1)
	require.InDelta(t, 30*48000, float64(w1.TotalSamples), 2048)

	runSegment := func(startSample int64) (*goavpipe.CollectedWaveform, *goavpipe.AudioWaveformStats) {
		params := waveformParams(segment, nil, spp)
		params.Seekable = false
		params.WaveformStartSample = startSample
		stats := &xc.IOStats{}
		goavpipe.InitUrlIOHandler(segment,
			&nonSeekableOpener{inner: &xc.FileInputOpener{URL: segment, Stats: stats}}, goavpipe.NoopOutputOpener{})
		require.NoError(t, avpipe.Xc(params))
		w := stats.Waveform.Stream(0)
		require.NotNil(t, w)
		require.True(t, w.Complete)
		require.InDelta(t, 30*48000, float64(w.TotalSamples), 2048)
		return w, stats.WaveformBatches[0]
	}

	// pts-derived placement: the segment's local timeline starts at 0
	w2, first := runSegment(-1)
	require.Equal(t, int64(0), first.StartPts, "mezzanine segments carry local timestamps")
	require.Equal(t, int64(0), w2.FirstBucketIndex)

	// explicit placement at the end of the first segment, as the fabric does from the part's timeline start
	start := w1.TotalSamples
	w3, _ := runSegment(start)
	require.Equal(t, start/int64(spp), w3.FirstBucketIndex)
	headPartial := start % int64(spp)
	require.Equal(t, (headPartial+w3.TotalSamples+int64(spp)-1)/int64(spp), int64(w3.Length()),
		"bucket count includes the partial first bucket")

	// the values are the segment's own decode, whatever the placement
	if headPartial == 0 {
		require.Equal(t, w2.MinMax, w3.MinMax)
	}
	requireBucketsClose(t, ffmpegBuckets(t, segment, 0, 2, spp), w2.MinMax, 2)
}

// cancelInput signals the first waveform batch and records any stat that arrives after the run returned.
type cancelInput struct {
	goavpipe.InputHandler
	first    chan struct{}
	once     atomic.Bool
	finished *atomic.Bool
	late     *atomic.Int32
}

func (c *cancelInput) Stat(streamIndex int, statType goavpipe.AVStatType, statArgs interface{}) error {
	if statType == goavpipe.AV_IN_STAT_AUDIO_WAVEFORM {
		if c.finished.Load() {
			c.late.Add(1)
		}
		if c.once.CompareAndSwap(false, true) {
			close(c.first)
		}
	}
	return nil
}

type cancelOpener struct {
	inner *xc.FileInputOpener
	input *cancelInput
}

func (o *cancelOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {
	h, err := o.inner.Open(fd, url)
	if err != nil {
		return nil, err
	}
	o.input.InputHandler = h
	return o.input, nil
}

func TestAudioWaveformCancel(t *testing.T) {
	checkFileExists(t, waveformMultiAudio)
	params := waveformParams(waveformMultiAudio, []int32{1}, 256)
	params.WaveformBatchBuckets = 16

	finished := &atomic.Bool{}
	late := &atomic.Int32{}
	input := &cancelInput{first: make(chan struct{}), finished: finished, late: late}
	goavpipe.InitUrlIOHandler(params.Url, &cancelOpener{inner: &xc.FileInputOpener{URL: params.Url}, input: input},
		goavpipe.NoopOutputOpener{})

	handle, err := avpipe.XcInit(params)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- avpipe.XcRun(handle)
	}()

	select {
	case <-input.first:
	case <-time.After(30 * time.Second):
		t.Fatal("no waveform batch within 30s")
	}
	require.NoError(t, avpipe.XcCancel(handle))

	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("XcRun did not return within 30s of XcCancel")
	}
	finished.Store(true)
	require.ErrorIs(t, err, avpipe.EAV_CANCELLED)
	require.NoError(t, avpipe.XcFini(handle))
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, int32(0), late.Load(), "stats after XcRun returned")
}

func TestAudioWaveformParamRejects(t *testing.T) {
	checkFileExists(t, waveformStereoAAC)

	params := waveformParams(waveformStereoAAC, nil, 256)
	params.BypassTranscoding = true
	goavpipe.InitUrlIOHandler(params.Url, &xc.FileInputOpener{URL: params.Url}, goavpipe.NoopOutputOpener{})
	require.ErrorIs(t, avpipe.Xc(params), avpipe.EAV_PARAM, "bypass")

	params = waveformParams(waveformStereoAAC, nil, 256)
	params.StreamId = 0
	goavpipe.InitUrlIOHandler(params.Url, &xc.FileInputOpener{URL: params.Url}, goavpipe.NoopOutputOpener{})
	require.ErrorIs(t, avpipe.Xc(params), avpipe.EAV_PARAM, "stream id with explicit xc type")

	params = waveformParams(waveformStereoAAC, nil, 1<<20)
	goavpipe.InitUrlIOHandler(params.Url, &xc.FileInputOpener{URL: params.Url}, goavpipe.NoopOutputOpener{})
	require.ErrorIs(t, avpipe.Xc(params), avpipe.EAV_PARAM, "samples per pixel too large")
}
