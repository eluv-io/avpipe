package mpegts

import (
	"errors"
	"io"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
)

var _ goavpipe.InputOpener = (*mpegtsInputOpener)(nil)

type mpegtsInputOpener struct {
	transport transport.Transport
}

func (mio *mpegtsInputOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {
	rc, err := mio.transport.Open()
	if err != nil {
		return nil, err
	}

	mih := &mpegtsInputHandler{
		rc: rc,
	}
	return mih, nil
}

type mpegtsInputHandler struct {
	rc io.ReadCloser
}

func (mih *mpegtsInputHandler) Read(buf []byte) (int, error) {
	// Processing step + splitting some out
	return mih.rc.Read(buf)
}

func (mih *mpegtsInputHandler) Close() error {
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
