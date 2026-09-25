package goavpipe

import (
	"fmt"
	"sync"
)

// AudioWaveformStats is the payload of AV_IN_STAT_AUDIO_WAVEFORM, reported by an XcAudioWaveform transcode: one batch
// of consecutive buckets, each holding the minimum and maximum decoded sample value of every channel over
// SamplesPerPixel samples. Buckets lie on a global grid where bucket i covers samples [i*spp, (i+1)*spp) of the
// stream, so an input that starts or ends mid-bucket reports a partial first or last bucket. Values are int16
// regardless of the source format, converted the way ffmpeg converts to s16. MinMax is a copy owned by the receiver.
type AudioWaveformStats struct {
	StreamIndex       int
	SampleRate        int
	Channels          int
	ChannelLayout     uint64 // native channel layout mask, 0 if not native
	SamplesPerPixel   int
	TimeBaseNum       int // time base of StartPts, the source stream time base
	TimeBaseDen       int
	StartPts          int64   // pts of the first decoded sample of the first bucket in this batch
	FirstBucketIndex  int64   // absolute index of the first bucket in this batch
	TotalSamples      int64   // samples decoded so far, including this batch
	NumBuckets        int     // buckets in this batch; the terminal batch may be empty
	LastBucketSamples int     // decoded samples in the most recently closed bucket
	IsLast            bool    // terminal batch, emitted at end of stream or on cancel
	MinMax            []int16 // NumBuckets*Channels*2 values laid out [bucket][channel][min,max]
}

// CollectedWaveform is the contiguous waveform of one stream assembled by WaveformCollector.
type CollectedWaveform struct {
	StreamIndex       int
	SampleRate        int
	Channels          int
	ChannelLayout     uint64
	SamplesPerPixel   int
	FirstBucketIndex  int64
	StartPts          int64
	TimeBaseNum       int
	TimeBaseDen       int
	TotalSamples      int64
	LastBucketSamples int
	Batches           int
	Complete          bool    // the terminal batch has arrived
	MinMax            []int16 // all buckets so far, laid out [bucket][channel][min,max]
}

// Length returns the number of buckets collected.
func (w *CollectedWaveform) Length() int {
	if w.Channels == 0 {
		return 0
	}
	return len(w.MinMax) / (w.Channels * 2)
}

// WaveformCollector accumulates AV_IN_STAT_AUDIO_WAVEFORM batches per stream. It is safe for use from the avpipe
// stat callback and from other goroutines.
type WaveformCollector struct {
	mu      sync.Mutex
	streams map[int]*CollectedWaveform
}

// Stat consumes a waveform stat and ignores every other stat type, so it can be called from any InputHandler.Stat.
// A batch that does not continue the previous one is rejected, which stops the transcode.
func (c *WaveformCollector) Stat(streamIndex int, statType AVStatType, statArgs interface{}) error {
	if statType != AV_IN_STAT_AUDIO_WAVEFORM {
		return nil
	}
	s, ok := statArgs.(*AudioWaveformStats)
	if !ok {
		return fmt.Errorf("waveform collector: unexpected stat args %T", statArgs)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streams == nil {
		c.streams = make(map[int]*CollectedWaveform)
	}
	w := c.streams[streamIndex]
	if w == nil {
		w = &CollectedWaveform{
			StreamIndex:      s.StreamIndex,
			SampleRate:       s.SampleRate,
			Channels:         s.Channels,
			ChannelLayout:    s.ChannelLayout,
			SamplesPerPixel:  s.SamplesPerPixel,
			FirstBucketIndex: s.FirstBucketIndex,
			StartPts:         s.StartPts,
			TimeBaseNum:      s.TimeBaseNum,
			TimeBaseDen:      s.TimeBaseDen,
		}
		c.streams[streamIndex] = w
	}
	if w.Complete {
		return fmt.Errorf("waveform collector: batch after the terminal batch, stream %d", streamIndex)
	}
	if expected := w.FirstBucketIndex + int64(w.Length()); s.FirstBucketIndex != expected && s.NumBuckets > 0 {
		return fmt.Errorf("waveform collector: batch gap on stream %d: expected bucket %d, got %d",
			streamIndex, expected, s.FirstBucketIndex)
	}
	if len(s.MinMax) != s.NumBuckets*s.Channels*2 {
		return fmt.Errorf("waveform collector: batch size mismatch on stream %d: %d values for %d buckets",
			streamIndex, len(s.MinMax), s.NumBuckets)
	}
	w.MinMax = append(w.MinMax, s.MinMax...)
	w.TotalSamples = s.TotalSamples
	w.LastBucketSamples = s.LastBucketSamples
	w.Batches++
	w.Complete = s.IsLast
	return nil
}

// Stream returns the collected waveform of a stream, or nil.
func (c *WaveformCollector) Stream(streamIndex int) *CollectedWaveform {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[streamIndex]
}

// Streams returns the collected waveforms keyed by stream index.
func (c *WaveformCollector) Streams() map[int]*CollectedWaveform {
	c.mu.Lock()
	defer c.mu.Unlock()
	res := make(map[int]*CollectedWaveform, len(c.streams))
	for k, v := range c.streams {
		res[k] = v
	}
	return res
}
