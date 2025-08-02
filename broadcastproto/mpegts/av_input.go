package mpegts

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
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

	var err error
	var gih goavpipe.InputHandler

	gio := goavpipe.GetGlobalInputOpener()
	if gio != nil {
		log.Debug("Calling global input opener to associated fd with recCtx", "fd", fd, "url", url)
		gih, err = gio.Open(fd, url)
		if err != nil {
			return nil, fmt.Errorf("failed to open global input opener: %w", err)
		}
	}

	rc, err := mio.transport.Open()
	if err != nil {
		return nil, err
	}

	fmt.Println("SSDBG MPEGTS OPEN")
	mio.copyStream = true

	var ch chan []byte

	if mio.copyStream {
		ch = make(chan []byte, 1024*1024) // SSDBG to size
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

		// TODO(Nate): Create channel, spin up goroutine to read from rc and write to segmenter until completed.
		// The segmenter will then call AVPipeOpenOutput etc using fd in this call

		go func() {
			fmt.Printf("SSDBG MPEGTS LOOP")
			mih.ReaderLoop(ch)
		}()
	}

	fmt.Println("SSDBG OPEN", "mih", mih)
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
			log.Error("SSDBG MPEGTS Read", err)
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
	if mih.gih == nil {
		return nil
	}
	return mih.gih.Stat(streamIndex, statType, statArgs)
}

func (mih *mpegtsInputHandler) ReaderLoop(ch chan []byte) {

	// SSDBG copy from main.go
	segCfg := smpte.SegmenterConfig{
		DurationSec: 30, // SSDBG needs to be xcparams seg duration
		Output: smpte.Output{
			Kind:    smpte.OutputNone, // Set to OutputFile to write ts test files
			Locator: "OUT",
		},
	}
	tsCfg := smpte.TsConfig{
		Url:            mih.transport.URL(),
		SaveFrameFiles: false,
		ProcessVideo:   false,
		ProcessData:    true,
		MaxPackets:     0,
		SegCfg:         segCfg,
	}

	ts := smpte.NewTs(tsCfg, mih.seqOpener, mih.inFd)

	var outConn net.Conn
	var err error
	if tsCfg.ProcessVideo {

		outConn, err = ConnectUnixSocket("UNSET")
		if err != nil {
			fmt.Println("ERROR: failed to connect to output unix socket", err)
			os.Exit(-1)
		}
	}

	var nPackets = 0

	for buf := range ch {
		nPackets++

		if nPackets%1000 == 0 {
			goavpipe.Log.Debug("Processed packets", "count", nPackets, "chan size", len(ch), "chan cap", cap(ch))
		}

		// PENDING(SS) must configure RTP processing based on input
		tsData, err := transport.StripRTP(buf)
		if err != nil {
			continue
		}

		// Process all TS packets in this payload
		for offset := 0; offset+188 <= len(tsData); offset += 188 {
			p, err := ToTSPacket(tsData[offset : offset+188])
			if err != nil {
				continue
			}
			ts.HandleTSPacket(p, outConn)
		}

		if ts.Cfg.MaxPackets > 0 && nPackets > int(ts.Cfg.MaxPackets) {
			fmt.Println("Max packets - exit")
			break
		}
	}
}

// SSDBG copied from smpte / main.go
// ConnectUnixSocket connects to a Unix domain socket at the given path.
func ConnectUnixSocket(socketPath string) (net.Conn, error) {
	addr := net.UnixAddr{
		Name: socketPath,
		Net:  "unix",
	}
	conn, err := net.DialUnix("unix", nil, &addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to unix socket: %w", err)
	}
	return conn, nil
}

func ToTSPacket(data []byte) (packet.Packet, error) {
	if len(data) < packet.PacketSize {
		return packet.Packet{}, fmt.Errorf("not enough data for TS packet")
	}
	var pkt packet.Packet
	copy(pkt[:], data[:packet.PacketSize])
	return pkt, nil
}
