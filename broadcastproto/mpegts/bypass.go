package mpegts

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/errors-go"
)

func NewBypassProcessor(cfg *goavpipe.XcParams, seqOpener SequentialOpenerFactory) (goavpipe.BypassProcessor, error) {
	tp, err := createTransport(cfg.Url, &cfg.InputCfg)
	if err != nil {
		return nil, err
	}
	return NewBypassProcessorWithTransport(cfg, seqOpener, tp)
}

func NewBypassProcessorWithTransport(
	cfg *goavpipe.XcParams,
	seqOpener SequentialOpenerFactory,
	tp transport.Transport,
) (goavpipe.BypassProcessor, error) {

	cfg.InputCfg.Processor = cfg.InputCfg.Processor.ApplyDefaults()

	return &BypassProcessor{
		transport: tp,
		seqOpener: seqOpener,
		xcParams:  cfg,
		fd:        -1,
	}, nil
}

type BypassProcessor struct {
	transport transport.Transport
	seqOpener SequentialOpenerFactory
	xcParams  *goavpipe.XcParams
	fd        int64
	netReader *NetReader
	running   atomic.Bool
	waitGroup sync.WaitGroup
}

func (bp *BypassProcessor) XcParams() *goavpipe.XcParams {
	return bp.xcParams
}

func (bp *BypassProcessor) Status() (running bool, err error) {
	netReader := bp.netReader
	if netReader != nil {
		return netReader.Status()
	}
	return false, nil
}

func (bp *BypassProcessor) Start(fd int64) error {
	e := errors.Template("BypassProcessor.Start", errors.K.Invalid.Default(), "url", bp.xcParams.Url, "fd", fd)

	logNetReader.Debug("starting bypass processor", e.Fields()...)

	if !bp.running.CompareAndSwap(false, true) {
		return e("reason", "already started")
	}

	bp.fd = fd

	tsCfg := TsConfig{
		SegmentLengthSec: uint64(bp.xcParams.InputCfg.Processor.PartDuration.Duration() / time.Second),
		// Note: This isn't fully correct for future applications, because really the
		// 'PackagingMode' of the config is about how the _output_ is packaged. When the CopyMode of
		// 'repackage' is used, that will need to be handled
		Packaging: bp.transport.PackagingMode(),
	}
	processor := NewMpegtsPacketProcessor(
		tsCfg,
		bp.seqOpener(bp.fd),
		bp.fd,
	)
	mpegTsConsumer := &MpegTsConsumer{
		pktChan: make(chan *pktpool.Packet, bp.xcParams.InputCfg.Processor.ChannelCap),
		pp:      processor,
		onFirstPacket: func() {
			logNetReader.Debug("first packet received", e.Fields()...)
			processor.ReportStart()
		},
	}

	bp.waitGroup.Add(1)
	go func() {
		defer bp.waitGroup.Done()
		goavpipe.Log.Debug("bypass processor copy loop initiated")
		mpegTsConsumer.ReaderLoop()
		goavpipe.Log.Debug("bypass processor copy loop terminated")
	}()

	bp.netReader = StartNetReader(bp.xcParams.Url,
		time.Millisecond*time.Duration(bp.xcParams.ConnectionTimeout),
		bp.xcParams.InputCfg.Processor,
		bp.transport,
		[]Consumer{mpegTsConsumer},
	)

	return nil
}

func (bp *BypassProcessor) Cancel() {
	netReader := bp.netReader
	if netReader != nil {
		bp.netReader.Cancel()
	}
}

func (bp *BypassProcessor) Wait() {
	netReader := bp.netReader
	if netReader != nil {
		bp.netReader.waitGroup.Wait()
	}
	bp.waitGroup.Wait()
}
