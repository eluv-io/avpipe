package mpegtsxc

import (
	"fmt"
	"sync"

	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
)

// jobSeq makes the per-job pseudo-URL unique so concurrent jobs in one process don't
// collide in the URL-keyed IO handler registry.
var jobSeq atomic.Int64

// videoInputOpener / videoInput are the avpipe custom AVIO reader.
// avpipe xc reads just video and PAT/PMT packets from the source MPEGTS stream
// (pre-filtered by the main MPEGTS reader)
type videoInputOpener struct {
	ch    <-chan []byte
	abort <-chan struct{}
}

func (o *videoInputOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {
	log.Debug("mpegts-xc input Open", "fd", fd, "url", url)
	return &videoInput{ch: o.ch, abort: o.abort}, nil
}

type videoInput struct {
	ch    <-chan []byte
	abort <-chan struct{}
	left  []byte // bytes from the last chunk not yet handed to avpipe
}

func (i *videoInput) Read(buf []byte) (int, error) {
	if len(i.left) == 0 {
		// The abort channel unblocks a parked read on cancel: XcCancel only sets the
		// ffmpeg-side cancel flag, which is not checked while this callback blocks.
		select {
		case chunk, ok := <-i.ch:
			if !ok {
				return 0, nil // closed channel => EOF (avpipe contract: (0, nil))
			}
			i.left = chunk
		case <-i.abort:
			return 0, nil // cancelled => EOF so xc_run unwinds
		}
	}
	n := copy(buf, i.left)
	i.left = i.left[n:]
	return n, nil
}

func (i *videoInput) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (i *videoInput) Close() error                                 { return nil }
func (i *videoInput) Size() int64                                  { return -1 } // live, unknown
func (i *videoInput) Stat(streamIndex int, statType goavpipe.AVStatType, statArgs interface{}) error {
	return nil
}

// videoOutputOpener / videoOutput are the avpipe custom AVIO writer
type videoOutputOpener struct {
	videoOutCh chan<- videoPkt
	classifier *Classifier
	pcrLead    int64
	stripPCR   bool
	timeline   *mediaTimeline // parts mode only
}

func (o *videoOutputOpener) Open(h, fd int64, streamIndex, segIndex int,
	pts int64, outType goavpipe.AVType) (goavpipe.OutputHandler, error) {

	log.Info("mpegts-xc avpipe output open",
		"h", h, "fd", fd, "stream", streamIndex, "seg", segIndex, "type", outType.Name())
	parser := newAvpipeOutParser(o.videoOutCh, o.classifier, o.pcrLead, o.stripPCR, o.timeline)
	return &videoOutput{parser: parser}, nil
}

type videoOutput struct {
	parser *avpipeOutParser
}

func (o *videoOutput) Write(buf []byte) (int, error) {
	o.parser.Parse(buf)
	return len(buf), nil
}

func (o *videoOutput) Seek(offset int64, whence int) (int64, error) { return 0, nil }

// Close releases the final staged access unit (parts mode). avpipe closes the output
// inside xc_run's teardown, before the session goroutine closes videoOutCh.
func (o *videoOutput) Close() error {
	o.parser.Flush()
	return nil
}
func (o *videoOutput) Stat(streamIndex int, avType goavpipe.AVType, statType goavpipe.AVStatType, statArgs interface{}) error {
	return nil
}

// xcSession is one avpipe video-transcode leg with cancellation support.
type xcSession struct {
	url   string
	abort chan struct{}
	done  chan error // receives the XcInit/XcRun result exactly once

	mu              sync.Mutex
	handle          int32
	handleSet       bool
	cancelRequested bool
	abortOnce       sync.Once
}

// startXcSession registers URL-keyed IO handlers for a unique per-job pseudo-URL and
// starts the avpipe transcode of the video leg in a goroutine (xc_init probes the
// input, so it must run while the caller feeds ch). When the run completes the
// handlers are removed and videoOutCh is closed.
//
// The pseudo-URL must remain a non-network name so avpipe treats the input as custom
// AVIO instead of installing its own UDP/RTP reader.
func startXcSession(cfg *Config, ch <-chan []byte, videoOutCh chan<- videoPkt,
	classifier *Classifier, stripPCR bool, timeline *mediaTimeline) *xcSession {

	s := &xcSession{
		url:   fmt.Sprintf("mpegts-xc-%d.ts", jobSeq.Inc()),
		abort: make(chan struct{}),
		done:  make(chan error, 1),
	}

	goavpipe.InitUrlIOHandler(s.url,
		&videoInputOpener{ch: ch, abort: s.abort},
		&videoOutputOpener{
			videoOutCh: videoOutCh,
			classifier: classifier,
			pcrLead:    ticks27(cfg.PcrLead),
			stripPCR:   stripPCR,
			timeline:   timeline,
		},
	)

	params := goavpipe.NewXcParams()
	params.Url = s.url
	params.XcType = goavpipe.XcVideo
	params.Format = "mpegts" // Produce one single continuous MPEGTS output
	params.EncWidth = cfg.EncWidth
	params.EncHeight = cfg.EncHeight
	params.Ecodec = cfg.Ecodec
	params.Dcodec = cfg.Dcodec
	params.VideoBitrate = cfg.VideoBitrate
	// Hard-cap the encoder video bitrate because the output must fit the CBR mux
	// (can't exceed); explicit VBV overrides win.
	if cfg.VideoBitrate > 0 {
		params.RcMaxRate = cfg.VideoBitrate
		params.RcBufferSize = cfg.VideoBitrate
	}
	if cfg.RcMaxRate > 0 {
		params.RcMaxRate = cfg.RcMaxRate
	}
	if cfg.RcBufferSize > 0 {
		params.RcBufferSize = cfg.RcBufferSize
	}
	if cfg.CrfStr != "" {
		params.CrfStr = cfg.CrfStr
	}
	if cfg.Preset != "" {
		params.Preset = cfg.Preset
	}
	if cfg.ForceKeyInt > 0 {
		params.ForceKeyInt = cfg.ForceKeyInt
	}
	if cfg.Profile != "" {
		params.Profile = cfg.Profile
	}
	if cfg.Level > 0 {
		params.Level = cfg.Level
	}
	if cfg.GPUIndex > 0 {
		params.GPUIndex = cfg.GPUIndex
	}
	if cfg.BitDepth > 0 {
		params.BitDepth = cfg.BitDepth
	}
	if cfg.XcParamsHook != nil {
		cfg.XcParamsHook(params)
	}

	log.Info("mpegts-xc starting avpipe", "url", s.url,
		"encWidth", cfg.EncWidth, "encHeight", cfg.EncHeight, "ecodec", cfg.Ecodec,
		"dcodec", cfg.Dcodec, "videoBitrate", cfg.VideoBitrate,
		"rcMaxRate", params.RcMaxRate, "rcBufferSize", params.RcBufferSize,
		"crf", params.CrfStr, "preset", params.Preset, "forceKeyInt", params.ForceKeyInt,
		"profile", params.Profile, "level", params.Level, "gpuIndex", params.GPUIndex,
		"stripPCR", stripPCR)

	go func() {
		defer close(videoOutCh)
		defer goavpipe.Globals.RemoveURLHandlers(s.url)

		handle, err := avpipe.XcInit(params)
		if err != nil {
			s.done <- err
			return
		}

		s.mu.Lock()
		s.handle = handle
		s.handleSet = true
		cancelNow := s.cancelRequested
		s.mu.Unlock()
		if cancelNow {
			_ = avpipe.XcCancel(handle)
		}

		s.done <- avpipe.XcRun(handle)
	}()

	return s
}

// Cancel aborts the transcode. Safe to call from any goroutine, idempotent, and
// honored even if it lands before XcInit assigned the handle.
func (s *xcSession) Cancel() {
	s.mu.Lock()
	s.cancelRequested = true
	handleSet, handle := s.handleSet, s.handle
	s.mu.Unlock()

	if handleSet {
		_ = avpipe.XcCancel(handle)
	}
	// Unblock a Read parked on the video channel after the C-side flag is set.
	s.abortOnce.Do(func() { close(s.abort) })
}
