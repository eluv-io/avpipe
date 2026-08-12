package mpegts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/goleak"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/format/duration"
	mio "github.com/eluv-io/common-go/media/io"
	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/common-go/util/byteutil"
	"github.com/eluv-io/common-go/util/syncutil"
	"github.com/eluv-io/common-go/util/testutil"
	"github.com/eluv-io/common-go/util/timeutil"
	"github.com/eluv-io/log-go"
)

func init() {
	lc := log.NewConfig()
	lc.Handler = "text"
	log.SetDefault(lc)
	log.Root().SetDebug()
}

func TestNetReader_happyPath(t *testing.T) {
	defer goleak.VerifyNone(t)

	watch := timeutil.StartWatch()

	reader := newTestReader(250, 1328, 100) // 250 packets of 1328 bytes at 100 packets per second
	ctx := createNetReader(&transport.Mock{
		Reader:    reader,
		Packaging: transport.RtpTs,
	})

	// Consume exactly the data produced by the source. The NetReader treats io.EOF as recoverable and never abandons a
	// source on its own, so shutdown is driven explicitly via Cancel() below, mirroring how production callers
	// (BypassProcessor / customInputHandler) tear it down.
	buf := &bytes.Buffer{}
	for buf.Len() < len(reader.src) {
		res := <-ctx.consumer.pktChan
		require.NotNil(t, res)
		buf.Write(res.T.Data)
		res.Release()
	}
	require.Equal(t, reader.src, buf.Bytes())
	require.EqualValues(t, 0, ctx.consumer.pktDropped.Load())
	require.Greater(t, watch.Duration(), 2*time.Second+400*time.Millisecond)

	// Cancel closes the consumer channels; draining to close confirms a clean shutdown (and lets goleak verify no
	// goroutines are left behind).
	ctx.netReader.Cancel()
	for res := range ctx.consumer.pktChan {
		res.Release()
	}
}

// TestNetReader_CancelNoInput tests canceling the NetReader in the case no packets are received.
func TestNetReader_CancelNoInput(t *testing.T) {
	ctx := createNetReader(transport.NewUDPTransport("udp://localhost:12345", transport.RawTs))

	running, err := ctx.netReader.Status()
	require.True(t, running)
	require.NoError(t, err)

	time.Sleep(time.Second)

	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		ctx.netReader.Cancel()
		wg.Done()
	}()

	require.False(t, syncutil.WaitTimeout(wg, time.Second))
}

// TestNetReader_CancelNoInput_Srt tests canceling the NetReader while it is blocked in an SRT Accept
// waiting for a source that never connects. Cancel must promptly interrupt the blocked Accept (rather
// than hang until a connection arrives or the connect/peer-idle timeout elapses) and tear down cleanly,
// closing the consumer channels and leaking no goroutines. This mirrors a live recorder being stopped
// while its SRT source is down.
func TestNetReader_CancelNoInput_Srt(t *testing.T) {
	defer goleak.VerifyNone(t)

	port, err := testutil.FreePort()
	require.NoError(t, err)
	url := fmt.Sprintf("srt://127.0.0.1:%d?mode=listener", port)

	ctx := createNetReader(transport.NewSRTTransport(url, transport.RawTs, transport.RawTs))

	running, err := ctx.netReader.Status()
	require.True(t, running)
	require.NoError(t, err)

	// Give the SRT listener time to start and block in Accept waiting for a source that never connects.
	time.Sleep(500 * time.Millisecond)

	// Cancel must return promptly despite the pending Accept.
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx.netReader.Cancel()
	}()
	require.False(t, syncutil.WaitTimeout(wg, time.Second), "Cancel did not return promptly")

	// Cancel closes the consumer channels; draining to close confirms a clean shutdown of the read path.
	for res := range ctx.consumer.pktChan {
		res.Release()
	}

	ctx.assertStaus(t, false)
}

// TestNetReader_StatusDistinguishesSuccessFromCancellation is a regression test for Status() reporting every clean
// stop as canceled: context.WithCancelCause records context.Canceled as the cause even when its CancelCauseFunc is
// invoked with a nil cause, so deriving Status()'s error straight from context.Cause(ctx) made "stopped, no error"
// indistinguishable from an explicit Cancel(). cancel(errNetReaderCleanStop) must report a clean stop instead.
func TestNetReader_StatusDistinguishesSuccessFromCancellation(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	r := &NetReader{ctx: ctx}

	running, err := r.Status()
	require.True(t, running)
	require.NoError(t, err)

	cancel(errNetReaderCleanStop)

	running, err = r.Status()
	require.False(t, running)
	require.NoError(t, err)
}

// TestNetReader_StatusReportsFirstResultOnly verifies Status() reports the first cancellation cause, mirroring the
// first-cancel-wins semantics of context.WithCancelCause.
func TestNetReader_StatusReportsFirstResultOnly(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	r := &NetReader{ctx: ctx}
	firstErr := errors.New("first")
	secondErr := errors.New("second")

	cancel(firstErr)
	cancel(secondErr)

	_, err := r.Status()
	require.ErrorIs(t, err, firstErr)
	require.NotErrorIs(t, err, secondErr)
}

// TestNetReader_ConnStats verifies ConnStats reports (ok=false, zero value) when there's no active reader yet or the
// active reader doesn't implement mio.StatsReporter (e.g. a plain UDP socket), and forwards to the reader - details
// included - when it does (e.g. an SRT connection).
func TestNetReader_ConnStats(t *testing.T) {
	var nr NetReader

	_, ok := nr.ConnStats(true)
	require.False(t, ok, "no active reader yet")

	var plain io.ReadCloser = &noopReadCloser{}
	nr.reader.Store(&plain)
	_, ok = nr.ConnStats(true)
	require.False(t, ok, "the active reader doesn't implement mio.StatsReporter")

	fake := &fakeStatsReporterReadCloser{stats: mio.ConnStats{RemoteAddr: "1.2.3.4:5678"}}
	var reporter io.ReadCloser = fake
	nr.reader.Store(&reporter)

	stats, ok := nr.ConnStats(true)
	require.True(t, ok)
	require.Equal(t, "1.2.3.4:5678", stats.RemoteAddr)
	require.True(t, fake.lastDetails, "details is forwarded to the underlying reporter")

	_, _ = nr.ConnStats(false)
	require.False(t, fake.lastDetails)
}

type noopReadCloser struct{}

func (*noopReadCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (*noopReadCloser) Close() error             { return nil }

// fakeStatsReporterReadCloser is a minimal io.ReadCloser that also implements mio.StatsReporter, for testing
// NetReader.ConnStats/RtpDecapsulator.ConnStats without a real SRT connection.
type fakeStatsReporterReadCloser struct {
	stats       mio.ConnStats
	lastDetails bool
}

func (*fakeStatsReporterReadCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (*fakeStatsReporterReadCloser) Close() error             { return nil }
func (f *fakeStatsReporterReadCloser) ConnStats(into *mio.ConnStats, details bool) {
	f.lastDetails = details
	*into = f.stats
}

func createNetReader(tp transport.Transport) netReaderTestCtx {
	cfg := &goavpipe.XcParams{
		Url:               tp.URL(),
		ConnectionTimeout: 400,
		InputCfg: goavpipe.InputConfig{
			CopyMode:          goavpipe.CopyModeRaw,
			CopyPackaging:     transport.RtpTs,
			InputPackaging:    transport.RtpTs,
			BypassLibavReader: true,
			Processor: goavpipe.InputProcessorConfig{
				ReconnectDelay:     200 * duration.Millisecond,
				PartDuration:       duration.Second,
				MaxRecoverAttempts: 0,
			},
		},
	}

	tc := &testConsumer{
		pktChan: make(chan pktpool.Resource, 100),
	}
	netReader := StartNetReader(
		cfg.Url,
		time.Millisecond*time.Duration(cfg.ConnectionTimeout),
		cfg.InputCfg.Processor,
		tp,
		[]Consumer{tc},
	)

	return netReaderTestCtx{
		netReader: netReader,
		consumer:  tc,
	}
}

// ---------------------------------------------------------------------------------------------------------------------

type netReaderTestCtx struct {
	netReader *NetReader
	consumer  *testConsumer
}

func (ctx *netReaderTestCtx) assertStaus(t *testing.T, wantRunning bool) {
	running, err := ctx.netReader.Status()
	require.Equal(t, wantRunning, running)
	if wantRunning {
		require.NoError(t, err)
	} else {
		require.Error(t, err)
	}
}

// ---------------------------------------------------------------------------------------------------------------------

type testConsumer struct {
	pktChan    chan pktpool.Resource
	pktDropped atomic.Int64
}

func (t *testConsumer) Name() string {
	return "test-consumer"
}

func (t *testConsumer) Chan() chan<- pktpool.Resource {
	return t.pktChan
}

func (t *testConsumer) PacketDropped() {
	t.pktDropped.Add(1)
}

// ---------------------------------------------------------------------------------------------------------------------

// newTestReader returns a reader that returns packets of the given packetSize at a rate of packetRate packets per
// second. Once packetCount packets have been read, the reader will return io.EOF. The data returned by the reader are
// random bytes, stored in src and accessible to tests for verification.
func newTestReader(packetCount, packetSize, packetRate int) *testReader {
	randomBytes := byteutil.RandomBytes(packetCount * packetSize)
	return &testReader{
		packetSize: packetSize,
		ipd:        time.Second / time.Duration(packetRate),
		src:        randomBytes,
		reader:     bytes.NewReader(randomBytes),
		closeCh:    make(chan struct{}),
	}
}

type testReader struct {
	packetSize int
	src        []byte
	reader     io.Reader
	closed     atomic.Bool
	closeCh    chan struct{} // closed by Close to unblock a Read waiting after the source data is exhausted
	ipd        time.Duration // inter-packet delay
	ticker     *time.Ticker
}

func (t *testReader) Close() error {
	if t.closed.CompareAndSwap(false, true) {
		if t.ticker != nil {
			t.ticker.Stop()
		}
		close(t.closeCh)
	}
	return nil
}

func (t *testReader) Read(p []byte) (n int, err error) {
	if t.closed.Load() {
		return 0, io.EOF
	}
	if t.ticker == nil {
		t.ticker = time.NewTicker(t.ipd)
		time.Sleep(t.ipd / 2)
	} else {
		select {
		case <-t.ticker.C:
		case <-t.closeCh:
			return 0, io.EOF
		}
	}
	n, err = t.reader.Read(p[:min(len(p), t.packetSize)])
	if err == io.EOF && n == 0 {
		// Source data exhausted. Model a live source that simply stops delivering more data: block until closed rather
		// than returning EOF, so the NetReader keeps the source open until the test cancels it (as production does).
		<-t.closeCh
		return 0, io.EOF
	}
	return n, err
}
