// Package venueclock decodes the venue clock feed that a contribution encoder carries as SMPTE ST 291 private user
// data (DID 0x50 / SDID 0x01) in its ST 2038 ancillary data stream. The feed is a serial byte stream: each
// ancillary packet holds the bytes received during that frame, so a message can span frames and most frames carry
// none. Stream reassembles the bytes into messages and State gives the fields of the known message ids their
// meaning: score, period, teams, game clock and shot clock.
package venueclock

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/eluv-io/avpipe/broadcastproto/anc"
)

// Message is one message of the feed. The messages are UTF-8 text:
//
//	SOH '4' '|' protocol id '|' message id '|' sequence number '|' data fields... '|' checksum EOT
//
// with SOH 0x01, EOT 0x04 and '|' between fields. '4' is a constant saying a sequence number follows, the protocol
// id is always 6, the message id 1 to 255, and the sequence number counts every message from 0 to 65535 and wraps.
// Fields are trimmed strings and may be empty. The checksum is two hex digits, the CRC-8 of CRC over the bytes from
// the one after SOH to the last '|' before it; it may also be empty, which the protocol allows for hand-made test
// messages. New fields are only ever added in front of the checksum, so decoders read fields by position and
// ignore the ones they do not know, as they ignore unknown message ids.
type Message struct {
	Version     string   // the character after SOH, "4"
	ProtocolID  string   // "6"
	MessageID   string   // selects the layout of Fields: see the Msg constants
	Seq         string   // sequence number, as sent
	Fields      []string // the data fields, in order, without the checksum
	Checksum    uint8    // the CRC as sent, when HasChecksum
	HasChecksum bool     // false for an empty checksum field
	ChecksumOK  bool     // the CRC matched the message, or the checksum was empty
	Raw         []byte   // the message as carried, SOH to EOT, header bytes of the packets removed
	StartPts    int64    // pts of the packet that carried SOH
	Pts         int64    // pts of the packet that carried EOT: the time the message was complete
}

func (m Message) String() string {
	s := fmt.Sprintf("v%s proto %s msg %s seq %s %s", m.Version, m.ProtocolID, m.MessageID, m.Seq,
		strings.Join(m.Fields, "|"))
	if !m.ChecksumOK {
		s += " BAD CRC"
	} else if !m.HasChecksum {
		s += " NO CRC"
	}
	return s
}

// crcTable is the byte table for CRC.
var crcTable = func() (t [256]uint8) {
	for i := range t {
		c := uint8(i)
		for b := 0; b < 8; b++ {
			if c&0x80 != 0 {
				c = c<<1 ^ 0x4d
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// CRC is the feed's CRC-8: polynomial x^8 + x^6 + x^3 + x^2 + 1 (0x4D, Koopman 0xA6), initial value 0, no
// reflection, no final xor. The feed computes it over the bytes after SOH up to and including the separator before
// the checksum.
func CRC(data []byte) uint8 {
	var crc uint8
	for _, b := range data {
		crc = crcTable[crc^b]
	}
	return crc
}

const (
	soh = 0x01
	eot = 0x04
	sep = '|'
)

// ErrFraming is returned for a message between SOH and EOT that is not of the form above.
var ErrFraming = errors.New("venueclock: message framing")

// MaxMessage bounds a message, SOH to EOT. The longest seen is under 80 bytes; a message still open at this size is
// dropped as aborted.
const MaxMessage = 512

// Header is the four bytes the encoder puts in front of the serial bytes in every DID 0x50 / SDID 0x01 packet,
// including a packet with no serial bytes. A message that spans frames has them in the middle of its bytes, so
// Stream strips them before framing.
var Header = []byte{0x18, 0x00, 0xc7, 0x00}

// Stream reassembles the feed from the DID 0x50 / SDID 0x01 packets of consecutive frames. Feed the packets in
// stream order; messages come out as their EOT arrives. The zero value is ready to use.
type Stream struct {
	// Header is a prefix removed from every packet's words before they join the stream; nil means Header.
	Header []byte
	// Aborted counts messages cut short by a new SOH before their EOT, which is how the feed restarts a message,
	// or by running past MaxMessage bytes without one; the partial message is dropped. Malformed counts framed
	// messages that did not parse.
	Aborted, Malformed int

	buf      []byte
	inMsg    bool
	startPts int64
}

// Feed adds the words of one DID 0x50 / SDID 0x01 packet carried at pts and returns the messages it completed.
// Bytes outside SOH..EOT are dropped. A malformed message is dropped and counted; the error names the first one
// met in this packet, after the messages that parsed.
func (s *Stream) Feed(udw []byte, pts int64) ([]Message, error) {
	header := s.Header
	if header == nil {
		header = Header
	}
	udw = bytes.TrimPrefix(udw, header)
	var msgs []Message
	var firstErr error
	for _, b := range udw {
		switch {
		case b == soh:
			if s.inMsg {
				s.Aborted++
			}
			s.buf = append(s.buf[:0], soh)
			s.inMsg = true
			s.startPts = pts
		case !s.inMsg:
			// between messages
		case len(s.buf) >= MaxMessage:
			// No EOT in sight: the line is not carrying messages, drop the bytes rather than hold them
			s.Aborted++
			s.buf = s.buf[:0]
			s.inMsg = false
		case b == eot:
			s.buf = append(s.buf, eot)
			m, err := parseMessage(s.buf)
			if err != nil {
				s.Malformed++
				if firstErr == nil {
					firstErr = err
				}
			} else {
				m.StartPts, m.Pts = s.startPts, pts
				msgs = append(msgs, m)
			}
			s.buf = s.buf[:0]
			s.inMsg = false
		default:
			s.buf = append(s.buf, b)
		}
	}
	return msgs, firstErr
}

// FeedPackets feeds the private user data packets among pkts, the ancillary packets of one frame, in order.
func (s *Stream) FeedPackets(pkts []anc.Packet, pts int64) ([]Message, error) {
	var msgs []Message
	var firstErr error
	for _, p := range pkts {
		if p.Kind() != anc.KindPrivateUserData {
			continue
		}
		m, err := s.Feed(p.UDW, pts)
		msgs = append(msgs, m...)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return msgs, firstErr
}

// parseMessage decodes one message, buf[0] being SOH and buf[len-1] EOT. The returned Raw is a copy.
func parseMessage(buf []byte) (Message, error) {
	raw := append([]byte(nil), buf...)
	m := Message{Raw: raw}
	if len(raw) < 1+1+1 {
		return m, fmt.Errorf("%w: %d bytes", ErrFraming, len(raw))
	}
	var body []byte
	if raw[len(raw)-2] == sep {
		// Empty checksum: the last field's separator is right before EOT
		body = raw[1 : len(raw)-1]
		m.ChecksumOK = true
	} else {
		if len(raw) < 1+1+2+1 {
			return m, fmt.Errorf("%w: %d bytes", ErrFraming, len(raw))
		}
		csText := raw[len(raw)-3 : len(raw)-1]
		cs, err := strconv.ParseUint(string(csText), 16, 8)
		if err != nil {
			return m, fmt.Errorf("%w: checksum %q", ErrFraming, csText)
		}
		m.Checksum, m.HasChecksum = uint8(cs), true
		body = raw[1 : len(raw)-3]
		m.ChecksumOK = CRC(body) == m.Checksum
	}
	// The last data field ends with a separator before the checksum
	body = bytes.TrimSuffix(body, []byte{sep})
	fields := strings.Split(string(body), string(sep))
	if len(fields) < 4 {
		return m, fmt.Errorf("%w: %d fields, want at least version, protocol, message id and sequence",
			ErrFraming, len(fields))
	}
	m.Version, m.ProtocolID, m.MessageID, m.Seq = fields[0], fields[1], fields[2], fields[3]
	m.Fields = fields[4:]
	return m, nil
}
