package mpegts

import (
	"io"
	"sync"
	"time"

	"go.uber.org/atomic"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/errors-go"
)

// customInputOpener implements an InputOpener that reads packets from a network connection in a separate reader
// goroutine and dispatches them through channels to consumers. The default consumer is avpipe/ffmpeg for the production
// of the default fragmented mp4 live stream parts. Optionally, packets are also forwarded to an mpegts consumer that
// stores them in MPEG TS format without transcoding in fabric parts.
type customInputOpener struct {
	transport transport.Transport
	seqOpener SequentialOpenerFactory
	cfg       *goavpipe.XcParams
}

func (c *customInputOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {
	e := errors.Template("open", errors.K.Invalid.Default(), "fd", fd, "url", url)

	goavpipe.Log.Debug("calling global input opener to associated fd with recCtx", "fd", fd, "url", url)
	gio := goavpipe.GetGlobalInputOpener()
	if gio == nil {
		return nil, e("reason", "global input opener is not set")
	}
	gih, err := gio.Open(fd, url)
	if err != nil {
		return nil, e(err, "reason", "failed to open global input opener")
	}

	copyStream := c.cfg.InputCfg.CopyMode == goavpipe.CopyModeRaw

	goavpipe.Log.Debug("opening custom input",
		"fd", fd,
		"url", url,
		"transport", c.transport.Handler(),
		"copyStream", copyStream,
	)

	consumers := make([]Consumer, 0, 2)
	var fmp4Consumer *Fmp4Consumer
	var mpegTsConsumer *MpegTsConsumer

	fmp4Consumer = &Fmp4Consumer{
		pktChan: make(chan *pktpool.Packet, c.cfg.InputCfg.Processor.ChannelCap),
	}
	consumers = append(consumers, fmp4Consumer)

	if copyStream {
		tsCfg := TsConfig{
			SegmentLengthSec: 30,
			// Note: This isn't fully correct for future applications, because really the
			// 'PackagingMode' of the config is about how the _output_ is packaged. When the CopyMode of
			// 'repackage' is used, that will need to be handled
			Packaging: c.transport.PackagingMode(),
		}
		processor := NewMpegtsPacketProcessor(
			tsCfg,
			c.seqOpener(fd),
			fd,
		)
		mpegTsConsumer = &MpegTsConsumer{
			pktChan: make(chan *pktpool.Packet, c.cfg.InputCfg.Processor.ChannelCap),
			pp:      processor,
		}
		consumers = append(consumers, mpegTsConsumer)
	}

	netReader := StartNetReader(
		c.cfg.Url,
		time.Millisecond*time.Duration(c.cfg.ConnectionTimeout),
		c.cfg.InputCfg.Processor,
		c.transport,
		consumers,
	)

	handler := &customInputHandler{
		netReader:      netReader,
		fmp4Consumer:   fmp4Consumer,
		mpegTsConsumer: mpegTsConsumer,
		gih:            gih,
	}

	if copyStream {
		handle, hasHandle := goavpipe.GIDHandle()
		handler.wg.Add(1)
		go func() {
			defer handler.wg.Done()
			if hasHandle {
				goavpipe.AssociateGIDWithHandle(handle)
				defer goavpipe.DissociateGIDFromHandle()
			}
			goavpipe.Log.Debug("MPEGTS copy loop initiated")
			handler.mpegTsConsumer.ReaderLoop()
			goavpipe.Log.Debug("MPEGTS copy loop terminated")
		}()
	} else {
	}

	return handler, nil
}

type customInputHandler struct {
	// gih is the global input handler, used to pass input stats through to the normal live
	gih            goavpipe.InputHandler
	netReader      *NetReader
	fmp4Consumer   *Fmp4Consumer
	mpegTsConsumer *MpegTsConsumer
	wg             sync.WaitGroup
}

func (h *customInputHandler) Read(buf []byte) (int, error) {
	return h.fmp4Consumer.Read(buf)
}

func (h *customInputHandler) Close() error {
	h.netReader.Cancel()
	h.wg.Wait()
	return nil
}

func (h *customInputHandler) Seek(_ int64, _ int) (int64, error) {
	return 0, errors.Str("not supported")
}

func (h *customInputHandler) Size() int64 {
	return -1
}

func (h *customInputHandler) Stat(streamIndex int, statType goavpipe.AVStatType, statArgs any) error {
	return h.gih.Stat(streamIndex, statType, statArgs)
}

// ---------------------------------------------------------------------------------------------------------------------

type Fmp4Consumer struct {
	pktChan        chan *pktpool.Packet
	packetsDropped atomic.Uint64
}

func (f *Fmp4Consumer) PacketDropped() {
	f.packetsDropped.Add(1)
}

func (f *Fmp4Consumer) Name() string {
	return "fmp4"
}

func (f *Fmp4Consumer) Chan() chan<- *pktpool.Packet {
	return f.pktChan
}

// Read reads the next packet and is called from ffmpeg.
func (f *Fmp4Consumer) Read(buf []byte) (int, error) {
	select {
	case pkt := <-f.pktChan:
		if pkt == nil {
			return 0, errors.E("read", errors.K.IO.Default(), io.EOF, goavpipe.ErrRetryField, true)
		}
		n := copy(buf, pkt.Data)
		pkt.Release()
		return n, nil
	}
}

// ---------------------------------------------------------------------------------------------------------------------

type MpegTsConsumer struct {
	pktChan        chan *pktpool.Packet
	packetsDropped atomic.Uint64
	pp             *MpegtsPacketProcessor
	onFirstPacket  func()
}

func (f *MpegTsConsumer) PacketDropped() {
	f.packetsDropped.Add(1)
}

func (f *MpegTsConsumer) Name() string {
	return "mpegts"
}

func (f *MpegTsConsumer) Chan() chan<- *pktpool.Packet {
	return f.pktChan
}

func (f *MpegTsConsumer) ReaderLoop() {
	f.pp.RegisterPacketsDropped(&f.packetsDropped)

	nPackets := 0
	f.pp.StartReportingStats()
	defer f.pp.Stop()
	for pkt := range f.pktChan {
		if nPackets == 0 {
			if f.onFirstPacket != nil {
				f.onFirstPacket()
			}
		}
		nPackets++

		if nPackets%1000 == 0 {
			f.pp.UpdateChannelSizeStats(len(f.pktChan))
			goavpipe.Log.Trace("processed mpegts packets",
				"count", nPackets,
				"chan size", len(f.pktChan),
				"chan cap", cap(f.pktChan))
		}

		f.pp.ProcessDatagram(pkt.Data)
		pkt.Release()
	}
}
