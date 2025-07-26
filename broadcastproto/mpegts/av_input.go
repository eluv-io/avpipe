package mpegts

import (
	"errors"
	"fmt"
	"io"
	"strings"

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

var transportMap = map[string]func(string) transport.Transport{
	"udp": transport.NewUDPTransport,
	"srt": transport.NewSRTTransport,
	"rtp": transport.NewRTPTransport,
}

// NewAutoInputOpener creates an InputOpener that automatically selects the transport based on the
// URL scheme.
func NewAutoInputOpener(url string, copyStream bool) (goavpipe.InputOpener, error) {
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
		transport: transport,
	}, nil
}

type mpegtsInputOpener struct {
	transport transport.Transport

	copyStream bool
}

func (mio *mpegtsInputOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {
	rc, err := mio.transport.Open()
	if err != nil {
		return nil, err
	}

	var ch chan []byte
	if mio.copyStream {
		// TODO(Nate): Create channel, spin up goroutine to read from rc and write to segmenter until completed.
		// The segmenter will then call AVPipeOpenOutput etc using fd in this call
	}

	mih := &mpegtsInputHandler{
		rc:          rc,
		outputSplit: ch,
	}
	return mih, nil
}

type mpegtsInputHandler struct {
	rc io.ReadCloser

	outputSplit chan<- []byte
}

func (mih *mpegtsInputHandler) Read(buf []byte) (int, error) {
	if mih.outputSplit != nil {
		n, err := mih.rc.Read(buf)
		if err != nil {
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
	return nil
}
