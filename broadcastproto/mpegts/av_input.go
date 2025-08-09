package mpegts

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Comcast/gots/packet"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"

	smpte "github.com/eluv-io/avpipe/smpte20xx/transport"
)

/*

TODO: Upon starting to read packets, check if it should use UDP/SRT/RTP.

If UDP, read the first few packets to determine if it is RTP over UDP or just MPEGTS over UDP.

If RTP, just strip the RTP headers and pass the payload to the MPEGTS handler.

Then split the output out to a segmenter if configured on

*/

var _ goavpipe.InputOpener = (*mpegtsInputOpener)(nil)

type SequentialOpenerFactory func(inFd int64) smpte.SequentialOpener

var transportMap = map[string]func(string) transport.Transport{
	"udp": transport.NewUDPTransport,
	"srt": transport.NewSRTTransport,
	"rtp": transport.NewRTPTransport,
}

// NewAutoInputOpener creates an InputOpener that automatically selects the transport based on the
// URL scheme.
func NewAutoInputOpener(url string, copyStream bool, seqOpener SequentialOpenerFactory) (goavpipe.InputOpener, error) {
	var transport transport.Transport
	for protoName, f := range transportMap {
		if strings.HasPrefix(url, protoName+"://") {
			transport = f(url)
		}
	}
	if transport == nil {
		return nil, fmt.Errorf("unsupported transport protocol: %s", url)
	}

	return &mpegtsInputOpener{
		transport:  transport,
		seqOpener:  seqOpener,
		copyStream: copyStream,
	}, nil
}

type mpegtsInputOpener struct {
	transport transport.Transport

	seqOpener  SequentialOpenerFactory
	copyStream bool
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

	goavpipe.Log.Trace("MPEGTS custom input opener opened", "fd", fd, "url", url, "transport", mio.transport.Handler())
	mio.copyStream = true

	var ch chan []byte

	if mio.copyStream {
		ch = make(chan []byte, 1024) // SSDBG to size
	}

	mih := &mpegtsInputHandler{
		rc:          rc,
		transport:   mio.transport,
		seqOpener:   mio.seqOpener(fd),
		copyStream:  mio.copyStream,
		outputSplit: ch,
		inFd:        fd,
		gih:         gih,
	}

	if mio.copyStream {
		go func() {
			goavpipe.Log.Trace("MPEGTS copy loop initiated")
			mih.ReaderLoop(ch)
		}()
	}

	return mih, nil
}

type mpegtsInputHandler struct {
	rc          io.ReadCloser
	transport   transport.Transport
	seqOpener   smpte.SequentialOpener
	copyStream  bool
	outputSplit chan<- []byte
	inFd        int64

	// gih is the global input handler, used to pass input stats through to the normal live
	gih goavpipe.InputHandler
}

func (mih *mpegtsInputHandler) Read(buf []byte) (int, error) {
	if mih.outputSplit != nil {
		n, err := mih.rc.Read(buf)
		if err != nil {
			goavpipe.Log.Error("SSDBG MPEGTS Read", err)
			return n, err
		}
		if n > 0 {
			select {
			case mih.outputSplit <- buf[:n]:
			default:
				goavpipe.Log.Warn("Output split channel is full, dropping data", "size", n)
			}
		}

		return n, nil
	}
	// If we don't have an output split channel, doing it like this is more efficient.
	return mih.rc.Read(buf)
}

func (mih *mpegtsInputHandler) Close() error {
	if mih.outputSplit != nil {
		close(mih.outputSplit)
		mih.outputSplit = nil
	}
	return mih.rc.Close()
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

func (mih *mpegtsInputHandler) ReaderLoop(ch chan []byte) {

	tsCfg := TsConfig{
		SegmentLengthSec: 30,
	}

	ts := NewMpegtsPacketProcessor(
		tsCfg,
		mih.seqOpener,
		mih.inFd,
	)

	nPackets := 0
	for buf := range ch {
		nPackets++

		if nPackets%1000 == 0 {
			goavpipe.Log.Trace("Processed packets", "count", nPackets, "chan size", len(ch), "chan cap", cap(ch))
		}

		// TODO(Nate): Automatically handle RTP over UDP

		ts.ProcessPackets(buf)
	}
}

func ToTSPacket(data []byte) (packet.Packet, error) {
	if len(data) < packet.PacketSize {
		return packet.Packet{}, fmt.Errorf("not enough data for TS packet")
	}
	var pkt packet.Packet
	copy(pkt[:], data[:packet.PacketSize])
	return pkt, nil
}
