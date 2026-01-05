package mpegts

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/goleak"

	"github.com/eluv-io/avpipe/broadcastproto/pktpool"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/format/duration"
	"github.com/eluv-io/common-go/util/byteutil"
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
	ctx := createNetReader(t, &transport.Mock{
		Reader:    reader,
		Packaging: transport.RtpTs,
	})

	res := &bytes.Buffer{}
	for pkt := range ctx.consumer.pktChan {
		res.Write(pkt.Data)
		pkt.Release()
	}
	require.Equal(t, reader.src, res.Bytes())
	require.EqualValues(t, 0, ctx.consumer.pktDropped.Load())

	require.Greater(t, watch.Duration(), 2*time.Second+400*time.Millisecond)
}

func createNetReader(t *testing.T, tp *transport.Mock) netReaderTestCtx {
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
		pktChan: make(chan *pktpool.Packet, 100),
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
	pktChan    chan *pktpool.Packet
	pktDropped atomic.Int64
}

func (t *testConsumer) Name() string {
	return "test-consumer"
}

func (t *testConsumer) Chan() chan<- *pktpool.Packet {
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
	}
}

type testReader struct {
	packetSize int
	src        []byte
	reader     io.Reader
	closed     atomic.Bool
	ipd        time.Duration // inter-packet delay
	ticker     *time.Ticker
}

func (t *testReader) Close() error {
	if t.closed.CompareAndSwap(false, true) {
		if t.ticker != nil {
			t.ticker.Stop()
		}
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
		<-t.ticker.C
	}
	return t.reader.Read(p[:min(len(p), t.packetSize)])
}
