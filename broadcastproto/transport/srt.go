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
		return &RtpDecapsulator{reader: reader, pkt: pktpool.NewPacket(0, maxUDPPacketSize)}, nil
	}

	return reader, nil
}

func (s *srtProto) PackagingMode() TsPackagingMode {
	return s.Out
}

// ---------------------------------------------------------------------------------------------------------------------

type RtpDecapsulator struct {
	reader io.ReadCloser
	pkt    *pktpool.Packet // scratch packet reused across reads via repeated From() calls
}

func (r *RtpDecapsulator) Read(p []byte) (n int, err error) {
	n, err = r.reader.Read(p)
	if n > 0 {
		if fromErr := r.pkt.From(p[:n]); fromErr != nil {
			return 0, fromErr
		}
		rtpLayer, rtpErr := r.pkt.Rtp()
		if rtpErr != nil {
			return 0, rtpErr
		}
		if rtpLayer.Packet().Header.Version != 2 {
			return 0, errors.Str("unsupported RTP version")
		}
		// payload aliases r.pkt's own buffer (not p), so this is a plain forward copy, not an overlap concern.
		copy(p, rtpLayer.Payload)
		return len(rtpLayer.Payload), err
	}
	return n, err
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
