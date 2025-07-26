package transport

import "io"

var _ Transport = (*srtProto)(nil)

// srtProto implements the Transport interface for SRT connections.
type srtProto struct {
	Url string
}

func NewSRTTransport(url string) Transport {
	return &srtProto{Url: url}
}

func (s *srtProto) URL() string {
	return s.Url
}

func (s *srtProto) Handler() string {
	return "srt"
}

func (s *srtProto) Open() (io.ReadCloser, error) {
	panic("SRT Protocol not implemented yet")
}
