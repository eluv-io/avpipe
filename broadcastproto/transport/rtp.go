package transport

import (
	"encoding/binary"
	"io"
	"net"

	"github.com/eluv-io/errors-go"
)

const maxUDPPacketSize = 1<<16 - 1

const diagsRtpRead = true // Diagnostics - RTP reader

// rtpStats holds RTP socket and read statistics
type rtpStats struct {
	udpStats
	seqDisc               int64
	lastSeq               uint16
	lastTimestamp         uint32
	timestampDeltaMinUsec uint32
	timestampDeltaMaxUsec uint32
	timestampBackwards    int64
	bufTooSmall           int64
	badRtp                int64
}

type rtpProto struct {
	Url  string
	Mode TsPackagingMode
}

func NewRTPTransport(url string, stripHeader bool) Transport {
	log.Debug("Creating new RTP transport", "url", url)
	var packagingMode TsPackagingMode
	if stripHeader {
		packagingMode = RawTs
	} else {
		packagingMode = RtpTs
	}
	return &rtpProto{Url: url, Mode: packagingMode}
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
	udpTransport := NewUDPTransport(r.Url)

	rc, err := udpTransport.Open()
	if err != nil {
		return nil, errors.NoTrace("failed to open UDP transport for RTP: %w", err)
	}
	udpConn, ok := rc.(*net.UDPConn)
	if !ok {
		return nil, errors.NoTrace("underlying connection is not a UDP connection")
	}

	return &rtpHandler{
		buf:     make([]byte, maxUDPPacketSize),
		Mode:    r.Mode,
		udpConn: udpConn,
	}, nil
}

type rtpHandler struct {
	buf      []byte
	bufStart int
	bufEnd   int

	Mode TsPackagingMode

	udpConn *net.UDPConn

	stats rtpStats
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
			h.stats.errs++
			return 0, err
		}
	}

	n := min(len(p), h.bufLen())
	copy(p, h.buf[h.bufStart:h.bufStart+n])
	h.bufStart += n

	h.updateDiagsRtp(p)

	return n, nil
}

func (h *rtpHandler) readNewPacket() error {
	n, _, err := h.udpConn.ReadFrom(h.buf)
	h.bufStart = 0
	h.bufEnd = n
	if err != nil {
		return err
	}

	if h.Mode == RawTs {
		headerEnd, err := StripRTP(h.buf[:h.bufEnd])
		if err != nil {
			// TODO(Nate): Is this the best resolution here? Should we just try again at this layer? Or rely on caller to do so?
			log.Warn("Failed to strip RTP header", "err", err)
			return err
		}
		h.bufStart = headerEnd
	}

	return nil
}

func (h *rtpHandler) bufLen() int {
	return h.bufEnd - h.bufStart
}

func (h *rtpHandler) updateDiagsRtp(p []byte) {
	if !diagsRtpRead {
		return
	}
	if h.bufStart != 0 {
		return // Only process full packets
	}
	h.stats.pkts++

	if len(p) < h.bufLen() {
		h.stats.bufTooSmall++
	} else {
		seq, ts, err := parseRtpHeaderMinimal(p)
		if err != nil {
			h.stats.badRtp++
		} else {
			if seq != h.stats.lastSeq+1 {
				h.stats.seqDisc++
			}
			if ts > h.stats.lastTimestamp {
				d := ts - h.stats.lastTimestamp
				if d > h.stats.timestampDeltaMaxUsec {
					h.stats.timestampDeltaMaxUsec = d
				}
				if d < h.stats.timestampDeltaMinUsec {
					h.stats.timestampDeltaMinUsec = d
				}
			} else {
				h.stats.timestampBackwards++
			}
			h.stats.lastSeq = seq
			h.stats.lastTimestamp = ts
		}
	}

	if h.stats.pkts%10_000 == 0 {
		// Update UDP socket stats before logging
		bufSize, occupancy, drops, err := getUdpStats(h.udpConn)
		if err == nil {
			h.stats.bufSize = bufSize
			h.stats.occupancy = occupancy
			h.stats.drops = drops
		}

		log.Info("rtp/udp stats",
			"pkts", h.stats.pkts,
			"errs", h.stats.errs,
			"bufSize", h.stats.bufSize,
			"occupancy", h.stats.occupancy,
			"drops", h.stats.drops,
			"seqDisc", h.stats.seqDisc,
			"tsBackwards", h.stats.timestampBackwards,
			"tsDeltaMin", h.stats.timestampDeltaMinUsec,
			"tsDeltaMax", h.stats.timestampDeltaMaxUsec,
			"bufTooSmall", h.stats.bufTooSmall,
			"badRtp", h.stats.badRtp)
	}
}

func StripRTP(data []byte) (int, error) {
	hdr, err := ParseRTPHeader(data)
	if err != nil {
		return 0, err
	}
	if len(data) < hdr.ByteLength()+188 {
		return 0, errors.NoTrace("packet too short for RTP and TS")
	}
	return hdr.ByteLength(), nil
}

var ErrShortRTP = errors.NoTrace("RTP packet too short")

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
		return nil, errors.NoTrace("RTP packet too short for CSRCs: expected at least %d bytes, got %d", baseHeaderSize+lenCSRC, len(data))
	}
	if header.Version != 2 {
		return nil, errors.NoTrace("unsupported RTP version: %d", header.Version)
	}
	if header.Extension {
		extLen := binary.BigEndian.Uint16(data[baseHeaderSize+lenCSRC+2 : baseHeaderSize+lenCSRC+4]) // Read extension length
		header.ExtensionByteCount = (int(extLen) * 4) + 4                                            // 4 bytes for the extension header
		if len(data) < baseHeaderSize+lenCSRC+header.ExtensionByteCount {
			return nil, errors.NoTrace("RTP packet too short for extension: expected at least %d bytes, got %d", baseHeaderSize+lenCSRC+header.ExtensionByteCount, len(data))
		}
	}

	return header, nil
}

// ----------------------------------------------------------------------------

// parseRtpHeaderMinimal extracts only sequence number and timestamp from RTP packet
func parseRtpHeaderMinimal(packet []byte) (uint16, uint32, error) {
	// RTP header minimum size is 12 bytes
	if len(packet) < 12 {
		return 0, 0, errors.NoTrace("parseRtpHeaderMinimal", errors.K.Invalid,
			"reason", "packet too short", "len", len(packet))
	}

	// Check RTP version (2 bits, should be 2)
	version := packet[0] >> 6
	if version != 2 {
		return 0, 0, errors.NoTrace("parseRtpHeaderMinimal", errors.K.Invalid,
			"reason", "invalid RTP version", "version", version)
	}

	// Sequence number: bytes 2-3 (big-endian)
	seq := uint16(packet[2])<<8 | uint16(packet[3])

	// Timestamp: bytes 4-7 (big-endian)
	ts := uint32(packet[4])<<24 | uint32(packet[5])<<16 |
		uint32(packet[6])<<8 | uint32(packet[7])

	return seq, ts, nil
}
