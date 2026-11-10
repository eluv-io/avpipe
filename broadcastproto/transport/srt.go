package transport

import (
	"io"

	mio "github.com/eluv-io/common-go/media/io"
	"github.com/eluv-io/common-go/media/pktpool"
	"github.com/eluv-io/errors-go"
)

var _ Transport = (*srtProto)(nil)

// srtProto implements the Transport interface for SRT connections.
type srtProto struct {
	Source   mio.PacketSource
	Url      string
	In       TsPackagingMode
	Out      TsPackagingMode
	StripRtp bool
}

func NewSRTTransport(url string, in TsPackagingMode, out TsPackagingMode) Transport {
	source, err := mio.CreatePacketSource(url)
	if err != nil {
		log.Warn("failed to create SRT packet source", "url", url, "err", err)
	}
	return &srtProto{
		Source:   source,
		Url:      url,
		In:       in,
		Out:      out,
		StripRtp: in == RtpTs && out != RtpTs,
	}
}

func (s *srtProto) URL() string {
	return s.Url
}

func (s *srtProto) Handler() string {
	return s.Source.URL().Scheme
}

func (s *srtProto) Open() (reader io.ReadCloser, err error) {
	e := errors.Template("srtProto.Open", errors.K.IO, "url", s.Url)

	reader, err = s.Source.Open()
	if err != nil {
		return nil, e(err)
	}

	if s.StripRtp {
		// strip RTP headers if the source actually contains them
		return &RtpDecapsulator{reader: reader}, nil
	}

	return reader, nil
}

func (s *srtProto) PackagingMode() TsPackagingMode {
	return s.Out
}

// ---------------------------------------------------------------------------------------------------------------------

type RtpDecapsulator struct {
	reader io.ReadCloser
	pkt    pktpool.RawPacket // decodes the datagram in place in the caller's buffer, re-pointed by every read
	guard  rtpGuard          // drops datagrams that are not RTP, and fails the source after a run of them
}

// Read reads the next datagram into p and strips its RTP header. A datagram that is not RTP is dropped and the next one
// read, until the guard fails the source.
func (r *RtpDecapsulator) Read(p []byte) (n int, err error) {
	for {
		n, err = r.reader.Read(p)
		if n == 0 {
			return n, err
		}
		payloadLen, notRtp := r.strip(p[:n])
		if notRtp == nil {
			r.guard.accept()
			return payloadLen, err
		}
		if guardErr := r.guard.reject(p[:n], notRtp); guardErr != nil {
			return 0, guardErr
		}
		if err != nil {
			return 0, err
		}
	}
}

// strip replaces the RTP datagram at the start of p with its payload and returns the payload's length, or why the
// datagram is not RTP.
func (r *RtpDecapsulator) strip(p []byte) (int, error) {
	rtpLayer, err := r.pkt.Reset(p).Rtp()
	if err != nil {
		return 0, err
	}
	if rtpLayer.Packet().Header.Version != 2 {
		return 0, errors.Str("unsupported RTP version")
	}
	// The payload aliases p behind the header. copy handles the overlap, moving the payload to the start of p.
	return copy(p, rtpLayer.Payload), nil
}

func (r *RtpDecapsulator) Close() error {
	return r.reader.Close()
}

// ConnStats forwards to the wrapped reader if it implements mio.StatsReporter, so stripping the RTP layer here
// doesn't break the reporter chain for RTP-over-SRT ingest (see srtProto.Open's StripRtp case).
func (r *RtpDecapsulator) ConnStats(into *mio.ConnStats, details bool) {
	if reporter, ok := r.reader.(mio.StatsReporter); ok {
		reporter.ConnStats(into, details)
		return
	}
	*into = mio.ConnStats{}
}
