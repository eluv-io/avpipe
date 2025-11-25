package transport

import (
	"io"
	"strings"

	mio "github.com/eluv-io/common-go/media/io"
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
	if in == RtpTs && strings.HasPrefix(url, "srt://") {
		url = "srt+rtp" + url[3:] // not strictly need, but easier to read in log files...
	}
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
}

func (r *RtpDecapsulator) Read(p []byte) (n int, err error) {
	n, err = r.reader.Read(p)
	if n > 0 {
		hdr, err := ParseRTPHeader(p[:n])
		if err != nil {
			return 0, err
		}
		headerLen := hdr.ByteLength()
		copy(p, p[headerLen:n])
		return n - headerLen, nil
	}
	return n, err
}

func (r *RtpDecapsulator) Close() error {
	return r.reader.Close()
}
