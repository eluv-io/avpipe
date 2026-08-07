package mpegts

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/errors-go"
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

// TestMpegtsInputHandlerReadPacketNoFanOut confirms ReadPacket returns the read datagram with no outputSplit set.
func TestMpegtsInputHandlerReadPacketNoFanOut(t *testing.T) {
	tsPkt := mustTSPacket()

	mih := &mpegtsInputHandler{
		rc:         &onceReadCloser{data: tsPkt[:]},
		transport:  &transport.Mock{Packaging: transport.RawTs},
		seqOpener:  &recordingSequentialOpener{},
		packetPool: pktpool.NewPacketPool(outputTlvWrapCap, inputPacketPoolCap),
	}

	res, err := mih.ReadPacket()
	require.NoError(t, err)
	require.NotNil(t, res)
	defer res.Release()

	require.Equal(t, tsPkt[:], res.T.Data)
	require.EqualValues(t, 0, mih.packetsDropped.Load())
}

// TestMpegtsInputHandlerReadPacketStampsReceivedAt is a regression test: ReadPacket once stamped ReceivedAt before
// calling FromReader, which internally resets the packet (including ReceivedAt) as part of loading - so the stamp was
// always wiped immediately, leaving ReceivedAt permanently zero. That silently broke the MPEG-TS copy track's
// wall-clock segment rotation (MpegtsPacketProcessor.writeDatagram keys off ReceivedAt), since the segment-length
// check could never see elapsed time. ReceivedAt must be stamped after FromReader returns, matching Read() above.
func TestMpegtsInputHandlerReadPacketStampsReceivedAt(t *testing.T) {
	tsPkt := mustTSPacket()

	mih := &mpegtsInputHandler{
		rc:         &onceReadCloser{data: tsPkt[:]},
		transport:  &transport.Mock{Packaging: transport.RawTs},
		seqOpener:  &recordingSequentialOpener{},
		packetPool: pktpool.NewPacketPool(outputTlvWrapCap, inputPacketPoolCap),
	}

	before := time.Now()
	res, err := mih.ReadPacket()
	after := time.Now()
	require.NoError(t, err)
	require.NotNil(t, res)
	defer res.Release()

	require.False(t, res.T.ReceivedAt.IsZero())
	require.False(t, res.T.ReceivedAt.Before(before))
	require.False(t, res.T.ReceivedAt.After(after))
}

// TestMpegtsInputHandlerReadPacketFanOut confirms ReadPacket hands the same underlying packet to both the caller and
// outputSplit, without re-reading.
func TestMpegtsInputHandlerReadPacketFanOut(t *testing.T) {
	tsPkt := mustTSPacket()
	ch := make(chan pktpool.Resource, 1)

	mih := &mpegtsInputHandler{
		rc:          &onceReadCloser{data: tsPkt[:]},
		transport:   &transport.Mock{Packaging: transport.RawTs},
		seqOpener:   &recordingSequentialOpener{},
		packetPool:  pktpool.NewPacketPool(outputTlvWrapCap, inputPacketPoolCap),
		outputSplit: ch,
	}

	res, err := mih.ReadPacket()
	require.NoError(t, err)
	require.NotNil(t, res)

	fanned := <-ch
	require.Same(t, res.T, fanned.T)
	require.Equal(t, tsPkt[:], fanned.T.Data)

	res.Release()
	fanned.Release()
	require.EqualValues(t, 0, mih.packetsDropped.Load())
}

// TestMpegtsInputHandlerReadPacketDropsOnFullChannel confirms ReadPacket drops (rather than blocks) when outputSplit
// has no room, while still returning the packet to the caller.
func TestMpegtsInputHandlerReadPacketDropsOnFullChannel(t *testing.T) {
	tsPkt := mustTSPacket()
	// Unbuffered and never drained: ReadPacket's non-blocking send always takes the drop path.
	ch := make(chan pktpool.Resource)

	mih := &mpegtsInputHandler{
		rc:          &onceReadCloser{data: tsPkt[:]},
		transport:   &transport.Mock{Packaging: transport.RawTs},
		seqOpener:   &recordingSequentialOpener{},
		packetPool:  pktpool.NewPacketPool(outputTlvWrapCap, inputPacketPoolCap),
		outputSplit: ch,
	}

	res, err := mih.ReadPacket()
	require.NoError(t, err)
	require.NotNil(t, res)
	defer res.Release()

	require.Equal(t, tsPkt[:], res.T.Data)
	require.EqualValues(t, 1, mih.packetsDropped.Load())
}

// TestMpegtsInputHandlerReadPacketError confirms a read error is released and wrapped as retryable, matching Read().
func TestMpegtsInputHandlerReadPacketError(t *testing.T) {
	mih := &mpegtsInputHandler{
		rc:         &onceReadCloser{data: nil, read: true}, // immediately returns io.EOF
		transport:  &transport.Mock{Packaging: transport.RawTs},
		seqOpener:  &recordingSequentialOpener{},
		packetPool: pktpool.NewPacketPool(outputTlvWrapCap, inputPacketPoolCap),
	}

	res, err := mih.ReadPacket()
	require.Error(t, err)
	require.Nil(t, res)
	_, ok := errors.GetField(err, goavpipe.ErrRetryField)
	require.True(t, ok)
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
