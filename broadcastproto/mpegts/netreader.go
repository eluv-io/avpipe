package mpegts

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/format/duration"
	"github.com/eluv-io/common-go/media"
	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/common-go/util/timeutil"
	"github.com/eluv-io/errors-go"
	elog "github.com/eluv-io/log-go"
)

var logNetReader = elog.Get("avpipe/broadcastproto/netreader")

// Consumer is a consumer of packets from the NetReader. See pktpool.Packet for correct handling of packets.
type Consumer interface {
	// Name returns the name of this consumer
	Name() string
	// Chan returns the channel on which packets are sent
	Chan() chan<- *pktpool.Packet
	// PacketDropped is called when a packet is dropped because the consumer's channel is full. This call must not
	// block!
	PacketDropped()
}

// StartNetReader creates and starts a NetReader that reads packets from the given transport and forwards them to the
// provided consumers.
func StartNetReader(
	url string,
	connectionTimeout time.Duration,
	config goavpipe.InputProcessorConfig,
	tp transport.Transport,
	consumers []Consumer,
) *NetReader {

	ctx, cancel := context.WithCancelCause(context.Background())
	transformer := media.NewNoopTransformer()

	config = config.ApplyDefaults()
	nr := &NetReader{
		url:               url,
		connectionTimeout: connectionTimeout,
		config:            config,
		transport:         tp,
		consumers:         consumers,
		transformer:       transformer,
		packetPool:        pktpool.NewPacketPool(config.MaxPacketSize),
		ctx:               ctx,
		cancel:            cancel,
	}

	_ = nr.start() // cannot error, since it was just created...
	return nr
}

// NetReader reads packets from a network source and forwards them to consumers. It uses a pool of packets to avoid
// allocations. Consumers must call pkt.Release() when they are done with the packet.
type NetReader struct {
	url               string
	connectionTimeout time.Duration
	config            goavpipe.InputProcessorConfig
	transport         transport.Transport
	consumers         []Consumer

	transformer media.Transformer
	packetPool  *pktpool.PacketPool
	ctx         context.Context
	cancel      context.CancelCauseFunc

	running   atomic.Bool
	waitGroup sync.WaitGroup
	reader    atomic.Pointer[io.ReadCloser] // the current reader, used for closing when the NetReader is canceled
}

func (r *NetReader) Status() (running bool, err error) {
	err = context.Cause(r.ctx)
	return err == nil, err
}

func (r *NetReader) start() error {
	e := errors.Template("NetReader.Start", errors.K.Invalid.Default(), "url", r.url)

	if !r.running.CompareAndSwap(false, true) {
		return e("reason", "already started")
	}

	r.waitGroup.Add(1)
	go func() {
		defer r.waitGroup.Done()
		defer func() {
			for _, consumer := range r.consumers {
				close(consumer.Chan())
			}
		}()
		err := r.process()
		if err != nil {
			err = e(err)
			if errors.Is(err, io.EOF) {
				logNetReader.Info("netreader terminated with EOF")
			} else {
				logNetReader.Warn("netreader failed", err)
			}
			r.cancel(e.IfNotNil(err))
		} else {
			r.cancel(nil)
		}
	}()

	return nil
}

func (r *NetReader) Cancel() {
	r.cancel(errors.E("NetReader.Cancel", errors.K.Warn, "reason", "canceled by user request"))
	reader := r.reader.Swap(nil)
	if reader != nil {
		errors.Log((*reader).Close, logNetReader.Info)
	}
	r.waitGroup.Wait()
}

func (r *NetReader) process() error {
	e := errors.Template("process", errors.K.IO.Default())

	logNetReader.Debug("starting processor", e.Fields()...)
	for i := 0; ; i++ {
		reader, err := r.connect()
		if err != nil {
			return e(err)
		}
		r.reader.Store(&reader)

		cont, err := r.readLoop(reader) // readLoop closes reader!
		if cont {
			if i < r.config.MaxRecoverAttempts {
				logNetReader.Info("recoverable processor error, will retry", e(err))
				continue
			}
			return e("reason", "max recovery attempts reached", "attempts", r.config.MaxRecoverAttempts).WithCause(err)
		}
		if err != nil {
			return e(err)
		}
		return nil
	}

}

func (r *NetReader) connect() (io.ReadCloser, error) {
	attempts := 0
	watch := timeutil.StartWatch()

	logNetReader.Debug("connecting to source", "url", r.transport.URL(), "timeout", r.connectionTimeout)

	for {
		if r.ctx.Err() != nil {
			return nil, context.Cause(r.ctx)
		}

		// PENDING(LUK): add timeout to transport.Open()
		//               bp.cfg.ConnectionTimeout
		attempts++
		reader, err := r.transport.Open()
		if err != nil {
			logNetReader.Debug("failed to connect to source", err,
				"attempt", attempts,
				"max_attempts", r.config.MaxConnectAttempts,
				"after", duration.Rounded(watch.Duration()))
			if attempts > r.config.MaxConnectAttempts || watch.Duration() > r.connectionTimeout {
				return nil, errors.E("connect", err,
					"reason", "failed to connect to transport",
					"attempts", attempts,
					"after", duration.Rounded(watch.Duration()))
			}
			select {
			case <-time.After(r.config.ReconnectDelay.Duration()):
			case <-r.ctx.Done():
				return nil, errors.E("connect", r.ctx.Err())
			}
			continue
		}
		logNetReader.Debug("source connected",
			"url", r.transport.URL(),
			"attempts", attempts,
			"after", duration.Rounded(watch.Duration()))
		return reader, nil
	}
}

func (r *NetReader) readLoop(reader io.ReadCloser) (cont bool, err error) {
	defer errors.Log(reader.Close, logNetReader.Info)
	throttledLog := logNetReader.Throttle("net-reader-dropped-packet", 100*time.Millisecond)
	for {
		pkt := r.packetPool.GetPacket()
		n, err := reader.Read(pkt.Data)
		if err != nil {
			return r.isRecoverable(err), errors.E("readLoop", errors.K.IO.Default(), err)
		}
		pkt.Data, err = r.transformer.Transform(pkt.Data[:n])
		if err != nil {
			return r.isRecoverable(err), errors.E("readLoop", errors.K.IO.Default(), err)
		}

		for _, consumer := range r.consumers {
			pkt.Reference()
			select {
			case consumer.Chan() <- pkt:
			case <-r.ctx.Done():
				pkt.Release(2) // release both the packet and the reference to it since we return
				return false, r.ctx.Err()
			default:
				// consumer busy: drop packet
				pkt.Release()
				consumer.PacketDropped()
				throttledLog.Warn("packet dropped",
					"reason", "consumer channel full",
					"consumer", consumer.Name(),
					"channel_size", len(consumer.Chan()),
					"channel_cap", cap(consumer.Chan()))
			}
		}
		pkt.Release()
	}
}

func (r *NetReader) isRecoverable(_ error) bool {
	return true
}

func (r *NetReader) Close() error {
	r.Cancel()
	return nil
}
