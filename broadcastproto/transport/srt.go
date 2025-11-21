package transport

import (
	"io"
	"strings"

	"github.com/datarhei/gosrt"

	"github.com/eluv-io/errors-go"
)

var _ Transport = (*srtProto)(nil)

// srtProto implements the Transport interface for SRT connections.
type srtProto struct {
	Url      string
	In       TsPackagingMode
	Out      TsPackagingMode
	StripRtp bool
}

func NewSRTTransport(url string, in TsPackagingMode, out TsPackagingMode) Transport {
	return &srtProto{
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
	return "srt"
}

func (s *srtProto) Open() (reader io.ReadCloser, err error) {
	e := errors.Template("srtProto.Open", errors.K.IO, "url", s.Url)

	srtConfig := srt.DefaultConfig()
	hostPort, err := srtConfig.UnmarshalURL(s.Url)
	if err != nil {
		return nil, e(err)
	}

	// force `message` transmission method: https://github.com/Haivision/srt/blob/master/docs/API/API.md#transmission-method-message
	// ensures message boundaries of the sender are preserved
	srtConfig.MessageAPI = true

	if !strings.Contains(s.Url, "listen") {
		// connect mode: connect to SRT server and pull the stream
		conn, err := srt.Dial("srt", hostPort, srtConfig)
		if err != nil {
			return nil, e(err)
		}
		return conn, nil
	}

	// listen mode: act as SRT server and accept incoming connections
	listener, err := srt.Listen("srt", hostPort, srtConfig)
	if err != nil {
		return nil, e(err)
	}
	defer listener.Close()

	req, err := listener.Accept2()
	if err != nil {
		return nil, e(err)
	}

	streamId := req.StreamId()
	log.Debug("new connection", "remote", req.RemoteAddr(), "srt_version", req.Version(), "stream_id", streamId)

	if req.Version() > 4 && strings.Contains(streamId, "subscribe") {
		req.Reject(srt.REJX_BAD_MODE)
		return nil, e("reason", "accepting only publish (push) connections",
			"remote", req.RemoteAddr(),
			"srt_version", req.Version(),
			"stream_id", streamId)
	}

	if srtConfig.Passphrase == "" {
		err = req.SetPassphrase(srtConfig.Passphrase)
		if err != nil {
			req.Reject(srt.REJX_UNAUTHORIZED)
			return nil, e(err, "reason", "invalid passphrase")
		}
	}

	// accept the connection
	conn, err := req.Accept()
	if err != nil {
		return nil, e(err, "reason", "invalid passphrase")
	}

	if s.StripRtp {
		// strip RTP headers if the source actually contains them
		return &RtpDecapsulator{conn: conn}, nil
	}

	return conn, nil
}

func (s *srtProto) PackagingMode() TsPackagingMode {
	return s.Out
}

// ---------------------------------------------------------------------------------------------------------------------

type RtpDecapsulator struct {
	conn srt.Conn
}

func (r *RtpDecapsulator) Read(p []byte) (n int, err error) {
	n, err = r.conn.Read(p)
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
	return r.conn.Close()
}
