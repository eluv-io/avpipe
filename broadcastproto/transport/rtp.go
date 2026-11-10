package transport

import (
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/eluv-io/common-go/media/pktpool"
)

const maxUDPPacketSize = 1<<16 - 1

// rtpHeaderLen is the length of the fixed RTP header.
const rtpHeaderLen = 12

type rtpProto struct {
	Url  string
	Mode TsPackagingMode
}

// NewRTPTransport creates a transport for an RTP source. The packaging argument selects the desired output packaging:
//   - RtpTs keeps the RTP framing (the RTP header is retained in the delivered datagram).
//   - RawTs and AtsTs strip the RTP header so the delivered datagram is raw TS; AtsTs additionally records the packet
//     arrival timestamp downstream.
//
// An unset packaging defaults to RtpTs.
func NewRTPTransport(url string, packaging TsPackagingMode) Transport {
	if packaging == UnknownPackagingMode {
		packaging = RtpTs
	}
	log.Debug("Creating new RTP transport", "url", url, "packaging", string(packaging))
	return &rtpProto{Url: url, Mode: packaging}
}

func (r *rtpProto) URL() string {
	return r.Url
}

func (r *rtpProto) Handler() string {
	return "rtp"
}

func (r *rtpProto) PackagingMode() TsPackagingMode {
	return r.Mode
}

func (r *rtpProto) Open() (io.ReadCloser, error) {
	// The inner UDP transport is used only to open the connection for reading; RTP framing is handled by the rtpHandler
	// below, so its packaging mode is irrelevant here.
	udpTransport := NewUDPTransport(r.Url, RawTs)

	rc, err := udpTransport.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open UDP transport for RTP: %w", err)
	}
	udpConn, ok := rc.(*net.UDPConn)
	if !ok {
		return nil, errors.New("underlying connection is not a UDP connection")
	}

	h := &rtpHandler{
		buf:     make([]byte, maxUDPPacketSize),
		Mode:    r.Mode,
		udpConn: udpConn,
	}
	if h.Mode != RtpTs {
		// Stripping is needed: decode the RTP header in place in h.buf, via the same lazy decoder as the rest of the
		// codebase instead of a bespoke parser.
		h.strip = new(pktpool.RawPacket)
	}
	return h, nil
}

type rtpHandler struct {
	buf      []byte
	bufStart int
	bufEnd   int

	Mode TsPackagingMode

	udpConn *net.UDPConn

	strip *pktpool.RawPacket // decodes h.buf in place, non-nil only when Mode != RtpTs (stripping is needed)
	guard rtpGuard           // drops datagrams that are not RTP, and fails the source after a run of them
}

func (h *rtpHandler) Close() error {
	if h.udpConn != nil {
		return h.udpConn.Close()
	}
	return nil
}

// Read reads precisely one datagram and returns it fully if it fits in the requesting buffer, or else partially, and
// returns the remainder in the next Read() call(s).  It only reads a new datagram from the network once it has fully
// returned the previous datagram.
//
// PENDING(LUK): SS suggested that we might discard the rest of the datagram instead, which is the standard OS
// behavior for datagrams.
func (h *rtpHandler) Read(p []byte) (int, error) {
	if h.bufStart >= h.bufEnd {
		err := h.readNewPacket()
		if err != nil {
			return 0, err
		}
	}

	n := min(len(p), h.bufLen())
	copy(p, h.buf[h.bufStart:h.bufStart+n])
	h.bufStart += n

	return n, nil
}

// readNewPacket reads the next datagram into h.buf. A datagram that is not RTP is dropped and the next one read, until
// the guard fails the source.
func (h *rtpHandler) readNewPacket() error {
	for {
		n, _, err := h.udpConn.ReadFrom(h.buf)
		h.bufStart = 0
		h.bufEnd = n
		if err != nil {
			return err
		}

		delivered, notRtp := h.prepare()
		if notRtp == nil {
			h.guard.accept()
			return nil
		}
		h.bufEnd = 0
		if err = h.guard.reject(h.buf[:n], notRtp); err != nil {
			return err
		}
		if delivered {
			// RtpTs passes the datagram on as it is, see prepare.
			h.bufEnd = n
			return nil
		}
	}
}

// prepare checks the datagram in h.buf and returns why it is not RTP, or nil. For any packaging that does not retain
// RTP framing (RawTs, AtsTs), it strips the RTP header, leaving the delivered datagram as raw TS. delivered reports
// whether a datagram that is not RTP is passed on anyway. RtpTs passes it on, so that the stream's statistics count it
// as they count any other malformed datagram.
func (h *rtpHandler) prepare() (delivered bool, notRtp error) {
	if h.strip == nil {
		switch {
		case h.bufEnd < rtpHeaderLen:
			return true, fmt.Errorf("datagram shorter than an RTP header: %d bytes", h.bufEnd)
		case h.buf[0]>>6 != 2:
			return true, fmt.Errorf("unsupported RTP version: %d", h.buf[0]>>6)
		}
		return false, nil
	}

	rtpLayer, err := h.strip.Reset(h.buf[:h.bufEnd]).Rtp()
	if err != nil {
		return false, err
	}
	hdr := rtpLayer.Packet().Header
	if hdr.Version != 2 {
		return false, fmt.Errorf("unsupported RTP version: %d", hdr.Version)
	}
	// The payload aliases h.buf, so the read window is moved onto it rather than the payload copied. The header length
	// is derived from where the payload actually lies, which also excludes any RTP padding at the end.
	payloadLen := len(rtpLayer.Payload)
	h.bufStart = h.bufEnd - payloadLen - int(hdr.PaddingSize)
	h.bufEnd = h.bufStart + payloadLen
	return false, nil
}

func (h *rtpHandler) bufLen() int {
	return h.bufEnd - h.bufStart
}
