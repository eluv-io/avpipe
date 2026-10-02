package transport

import (
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/eluv-io/common-go/media/pktpool"
)

const maxUDPPacketSize = 1<<16 - 1

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
		// Stripping is needed: own a scratch packet, reused across reads via repeated From() calls, to decode the RTP
		// header via the same lazy decoder as the rest of the codebase instead of a bespoke parser.
		h.stripPkt = pktpool.NewPacket(0, maxUDPPacketSize)
	}
	return h, nil
}

type rtpHandler struct {
	buf      []byte
	bufStart int
	bufEnd   int

	Mode TsPackagingMode

	udpConn *net.UDPConn

	stripPkt *pktpool.Packet // non-nil only when Mode != RtpTs (stripping is needed)
}

func (h *rtpHandler) Close() error {
	if h.udpConn != nil {
		return h.udpConn.Close()
	}
	return nil
}

// Read reads precisely one datagram and returns it fully if it fits in the requesting buffer,
// or else partially, and returns the remainder in the next Read() call(s).  It only reads a new
// datagram from the network once it has fully return the previous datagram.
// SS thinking we might discard the rest of the datagram instead which is the standard OS behavior for datagrams
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

func (h *rtpHandler) readNewPacket() error {
	n, _, err := h.udpConn.ReadFrom(h.buf)
	h.bufStart = 0
	h.bufEnd = n
	if err != nil {
		return err
	}

	// Strip the RTP header for any packaging that does not retain RTP framing (RawTs, AtsTs), leaving the delivered
	// datagram as raw TS.
	if h.stripPkt != nil {
		if err := h.stripPkt.From(h.buf[:h.bufEnd]); err != nil {
			// TODO(Nate): Is this the best resolution here? Should we just try again at this layer? Or rely on caller to do so?
			log.Warn("Failed to load RTP packet for stripping", "err", err)
			return err
		}
		rtpLayer, err := h.stripPkt.Rtp()
		if err != nil {
			log.Warn("Failed to strip RTP header", "err", err)
			return err
		}
		if rtpLayer.Packet().Header.Version != 2 {
			err := fmt.Errorf("unsupported RTP version: %d", rtpLayer.Packet().Header.Version)
			log.Warn("Failed to strip RTP header", "err", err)
			return err
		}
		// Normalize to start at 0 instead of tracking a separate header-length offset: rtpLayer.Payload excludes any
		// RTP padding too, unlike the previous header-length-only offset.
		copy(h.buf, rtpLayer.Payload)
		h.bufStart = 0
		h.bufEnd = len(rtpLayer.Payload)
	}

	return nil
}

func (h *rtpHandler) bufLen() int {
	return h.bufEnd - h.bufStart
}
