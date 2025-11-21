package mpegts

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
)

/*

TODO: Upon starting to read packets, check if it should use UDP/SRT/RTP.

If UDP, read the first few packets to determine if it is RTP over UDP or just MPEGTS over UDP.

If RTP, just strip the RTP headers and pass the payload to the MPEGTS handler.

Then split the output out to a segmenter if configured on

*/

var _ goavpipe.InputOpener = (*mpegtsInputOpener)(nil)

type SequentialOpenerFactory func(inFd int64) SequentialOpener

// NewAutoInputOpener creates an InputOpener that automatically selects the transport based on the
// URL scheme.
func NewAutoInputOpener(url string, cfg *goavpipe.InputConfig, seqOpener SequentialOpenerFactory) (goavpipe.InputOpener, error) {
	var tp transport.Transport

	scheme := strings.SplitN(url, "://", 2)[0]
	if len(scheme) == 0 {
		return nil, fmt.Errorf("invalid url: %s", url)
	}

	switch scheme {
	case "rtp":
		tp = transport.NewRTPTransport(url, cfg.CopyPackaging != transport.RtpTs)
	case "udp":
		tp = transport.NewUDPTransport(url)
	case "srt+rtp": // same as srt:// with RTP TS input packaging
		cfg.InputPackaging = transport.RtpTs
		fallthrough
	case "srt":
		tp = transport.NewSRTTransport(url, cfg.InputPackaging, cfg.CopyPackaging)
	}

	if tp == nil {
		return nil, fmt.Errorf("unsupported transport protocol: %s", url)
	}

	return &mpegtsInputOpener{
		transport: tp,
		seqOpener: seqOpener,
		cfg:       cfg,
	}, nil
}

type mpegtsInputOpener struct {
	transport transport.Transport

	seqOpener SequentialOpenerFactory
	cfg       *goavpipe.InputConfig
}

func (mio *mpegtsInputOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {

	goavpipe.Log.Debug("Calling global input opener to associated fd with recCtx", "fd", fd, "url", url)
	gio := goavpipe.GetGlobalInputOpener()
	if gio == nil {
		return nil, errors.New("global input opener is not set")
	}
	gih, err := gio.Open(fd, url)
	if err != nil {
		return nil, fmt.Errorf("failed to open global input opener: %w", err)
	}

	rc, err := mio.transport.Open()
	if err != nil {
		return nil, err
	}

	goavpipe.Log.Debug("MPEGTS custom input opener opened", "fd", fd, "url", url, "transport", mio.transport.Handler())

	var ch chan []byte

	copyStream := mio.cfg.CopyMode == goavpipe.CopyModeRaw
	if copyStream {
		ch = make(chan []byte, 20*1024)
	}

	mih := &mpegtsInputHandler{
		rc:               rc,
		transport:        mio.transport,
		seqOpener:        mio.seqOpener(fd),
		copyStream:       copyStream,
		outputSplit:      ch,
		readerLoopDoneCh: make(chan struct{}),
		inFd:             fd,
		gih:              gih,
	}

	if copyStream {
		go func() {
			handle, ok := goavpipe.GIDHandle()
			if ok {
				goavpipe.AssociateGIDWithHandle(handle)
			}
			goavpipe.Log.Debug("MPEGTS copy loop initiated")
			mih.ReaderLoop(ch, &mih.packetsDropped)
			close(mih.readerLoopDoneCh)
		}()
	} else {
		close(mih.readerLoopDoneCh)
	}

	return mih, nil
}

type mpegtsInputHandler struct {
	rc        io.ReadCloser
	transport transport.Transport
	seqOpener SequentialOpener
	inFd      int64

	copyStream       bool
	outputSplit      chan<- []byte
	packetsDropped   atomic.Uint64
	readerLoopDoneCh chan struct{}

	// gih is the global input handler, used to pass input stats through to the normal live
	gih goavpipe.InputHandler
}

func (mih *mpegtsInputHandler) Read(buf []byte) (int, error) {
	if len(buf) < 7*188 {
		mpegtslog.Warn("buffer size smaller than 7 TS packets", "size", len(buf))
	}
	if mih.outputSplit != nil {
		n, err := mih.rc.Read(buf)
		if err != nil {
			goavpipe.Log.Error("MPEGTS Read", err)
			return n, err
		}
		if n > 0 {
			select {
			case mih.outputSplit <- buf[:n]:
			default:
				mih.packetsDropped.Inc()
				goavpipe.Log.Warn("Output split channel is full, dropping data", "size", n)
			}
		}

		return n, nil
	}
	// If we don't have an output split channel, doing it like this is more efficient.
	return mih.rc.Read(buf)
}

func (mih *mpegtsInputHandler) Close() error {
	err := mih.rc.Close()

	if mih.outputSplit != nil {
		ch := mih.outputSplit
		mih.outputSplit = nil
		initLen := len(ch)
		close(ch)

		fiveSecond := time.After(5 * time.Second)
		select {
		case <-fiveSecond:
			goavpipe.Log.Warn("mpegts read loop still running 5 seconds after channel closure", "initLen", initLen, "curLen", len(ch))
		case <-mih.readerLoopDoneCh:
		}
	}

	return err
}

func (mih *mpegtsInputHandler) Seek(_ int64, _ int) (int64, error) {
	return 0, errors.New("not supported")
}

func (mih *mpegtsInputHandler) Size() int64 {
	return -1
}

func (mih *mpegtsInputHandler) Stat(streamIndex int, statType goavpipe.AVStatType, statArgs any) error {
	return mih.gih.Stat(streamIndex, statType, statArgs)
}

func (mih *mpegtsInputHandler) ReaderLoop(ch chan []byte, packetsDropped *atomic.Uint64) {

	tsCfg := TsConfig{
		SegmentLengthSec: 30,
		// Note: This isn't fully correct for future applications, because really the
		// 'PackagingMode' of the config is about how the _output_ is packaged. When the CopyMode of
		// 'repackage' is used, that will need to be handled
		Packaging: mih.transport.PackagingMode(),
	}

	ts := NewMpegtsPacketProcessor(
		tsCfg,
		mih.seqOpener,
		mih.inFd,
	)
	ts.RegisterPacketsDropped(packetsDropped)

	nPackets := 0
	ts.StartReportingStats()
	defer ts.Stop()
	for buf := range ch {
		nPackets++

		if nPackets%1000 == 0 {
			ts.UpdateChannelSizeStats(len(ch))
			goavpipe.Log.Trace("Processed packets", "count", nPackets, "chan size", len(ch), "chan cap", cap(ch))
		}

		ts.ProcessDatagram(buf)
	}
}
