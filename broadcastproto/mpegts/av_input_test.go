package mpegts

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/common-go/media/pktpool"
)

// TestMpegtsInputHandlerPoolRoundTrip confirms a packet survives the cross-goroutine hand-off from Read() (the
// producer, called synchronously here as ffmpeg's custom I/O callback would) through the pktpool.Resource channel to
// ReaderLoop (the consumer), and is processed and released exactly once.
func TestMpegtsInputHandlerPoolRoundTrip(t *testing.T) {
	tsPkt := mustTSPacket()
	opener := &recordingSequentialOpener{}
	ch := make(chan pktpool.Resource, 10)

	mih := &mpegtsInputHandler{
		rc:          &onceReadCloser{data: tsPkt[:]},
		transport:   &transport.Mock{Packaging: transport.RawTs},
		seqOpener:   opener,
		packetPool:  pktpool.NewPacketPool(outputTlvWrapCap, inputPacketPoolCap),
		outputSplit: ch,
	}

	n, err := mih.Read(make([]byte, 1500))
	require.NoError(t, err)
	require.Equal(t, 188, n)

	// No more sends expected: closing now lets ReaderLoop drain the one queued packet and return.
	close(ch)

	done := make(chan struct{})
	go func() {
		mih.ReaderLoop(ch, &mih.packetsDropped)
		close(done)
	}()
	<-done

	require.Equal(t, 1, opener.opens)
	require.EqualValues(t, 0, mih.packetsDropped.Load())
}

// TestMpegtsInputHandlerDropsOnFullChannel confirms Read() drops (rather than blocks or panics) when outputSplit has
// no room, and releases the borrowed packet back to the pool on that path.
func TestMpegtsInputHandlerDropsOnFullChannel(t *testing.T) {
	tsPkt := mustTSPacket()
	// Unbuffered and never drained: Read's non-blocking send always takes the drop path.
	ch := make(chan pktpool.Resource)

	mih := &mpegtsInputHandler{
		rc:          &onceReadCloser{data: tsPkt[:]},
		transport:   &transport.Mock{Packaging: transport.RawTs},
		seqOpener:   &recordingSequentialOpener{},
		packetPool:  pktpool.NewPacketPool(outputTlvWrapCap, inputPacketPoolCap),
		outputSplit: ch,
	}

	n, err := mih.Read(make([]byte, 1500))

	require.NoError(t, err)
	require.Equal(t, 188, n)
	require.EqualValues(t, 1, mih.packetsDropped.Load())
}

// onceReadCloser returns data on the first Read call, then io.EOF on any subsequent call.
type onceReadCloser struct {
	data []byte
	read bool
}

func (r *onceReadCloser) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	return copy(p, r.data), nil
}

func (r *onceReadCloser) Close() error {
	return nil
}
