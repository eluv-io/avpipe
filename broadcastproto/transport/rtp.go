package transport

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

type rtpProto struct {
	Url string
}

func NewRTPTransport(url string) Transport {
	return &rtpProto{Url: url}
}

func (r *rtpProto) URL() string {
	return r.Url
}

func (r *rtpProto) Handler() string {
	return "rtp"
}

func (r *rtpProto) Open() (io.ReadCloser, error) {
	udpTransport := NewUDPTransport(r.Url)

	rc, err := udpTransport.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open UDP transport for RTP: %w", err)
	}
	return &rtpHandler{
		buf: make([]byte, 0, 10*1024*1024),
		rc:  rc,
	}, nil
}

type rtpHandler struct {
	buf []byte
	rc  io.ReadCloser
}

func (h *rtpHandler) Close() error {
	if h.rc != nil {
		return h.rc.Close()
	}
	return nil
}

func (h *rtpHandler) expandBufferIfNecessary(n int) {
	if len(h.buf) < n {
		newBuf := make([]byte, n)
		copy(newBuf, h.buf)
		h.buf = newBuf
	}
}

func (h *rtpHandler) Read(p []byte) (n int, err error) {
	h.expandBufferIfNecessary(len(p))
	n, err = h.rc.Read(h.buf)
	if err != nil {
		return n, err
	}

	stripped, err := StripRTP(h.buf[:n])
	if err != nil {
		return n, err
	}

	copy(p, stripped)

	return len(stripped), nil
}

func StripRTP(data []byte) ([]byte, error) {
	hdr, err := ParseRTPHeader(data)
	if err != nil {
		return nil, err
	}
	if len(data) < hdr.ByteLength()+188 {
		return nil, fmt.Errorf("packet too short for RTP and TS")
	}
	return data[hdr.ByteLength():], nil
}

var ErrShortRTP = errors.New("RTP packet too short")

type RTPHeader struct {
	Version        uint8
	Padding        bool
	Extension      bool
	CSRCCount      uint8
	Marker         bool
	PayloadType    uint8
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	// PENDING(SS) CSRCs and extension not included
	ExtensionByteCount int // Number of bytes in the extension (header + payload), if present
}

func (h *RTPHeader) ByteLength() int {
	length := 12 // Base RTP header length
	if h.CSRCCount > 0 {
		length += int(h.CSRCCount) * 4
	}
	if h.Extension {
		length += h.ExtensionByteCount
	}
	return length
}

func ParseRTPHeader(data []byte) (*RTPHeader, error) {
	baseHeaderSize := 12 // Minimum size of RTP header
	if len(data) < baseHeaderSize {
		return nil, ErrShortRTP
	}

	b0 := data[0]
	b1 := data[1]

	header := &RTPHeader{
		Version:        b0 >> 6,
		Padding:        (b0>>5)&0x01 == 1,
		Extension:      (b0>>4)&0x01 == 1,
		CSRCCount:      b0 & 0x0F,
		Marker:         (b1>>7)&0x01 == 1,
		PayloadType:    b1 & 0x7F,
		SequenceNumber: binary.BigEndian.Uint16(data[2:4]),
		Timestamp:      binary.BigEndian.Uint32(data[4:8]),
		SSRC:           binary.BigEndian.Uint32(data[8:12]),
	}
	lenCSRC := 4 * int(header.CSRCCount)
	if len(data) < baseHeaderSize+lenCSRC {
		return nil, fmt.Errorf("RTP packet too short for CSRCs: expected at least %d bytes, got %d", baseHeaderSize+lenCSRC, len(data))
	}
	if header.Extension {
		extLen := binary.BigEndian.Uint16(data[baseHeaderSize+lenCSRC+2 : baseHeaderSize+lenCSRC+4]) // Read extension length
		header.ExtensionByteCount = (int(extLen) * 4) + 4                                            // 4 bytes for the extension header
		if len(data) < baseHeaderSize+lenCSRC+header.ExtensionByteCount {
			return nil, fmt.Errorf("RTP packet too short for extension: expected at least %d bytes, got %d", baseHeaderSize+lenCSRC+header.ExtensionByteCount, len(data))
		}
	}

	return header, nil
}
