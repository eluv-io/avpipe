package transport

import (
	"testing"

	pionrtp "github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

// benchDatagram returns an RTP datagram carrying 7 TS packets, the usual payload of MPEG-TS over RTP.
func benchDatagram(b *testing.B) []byte {
	payload := make([]byte, 0, 7*188)
	for range 7 {
		payload = append(payload, mustTestTSPacket(7)...)
	}
	pkt := pionrtp.Packet{Header: pionrtp.Header{Version: 2, SequenceNumber: 1, Timestamp: 1}, Payload: payload}
	data, err := pkt.Marshal()
	require.NoError(b, err)
	return data
}

// BenchmarkRtpHandlerStrip measures the RTP handler stripping the RTP header of one datagram. Every iteration also
// copies the datagram into the handler's buffer, standing in for the socket read.
func BenchmarkRtpHandlerStrip(b *testing.B) {
	datagram := benchDatagram(b)
	h := newTestStripHandler()
	b.ReportAllocs()
	for b.Loop() {
		h.bufEnd = copy(h.buf, datagram)
		if _, err := h.prepare(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRtpHandlerCheck measures the RTP handler checking the RTP header of one datagram it passes on unchanged
// (RtpTs). Every iteration also copies the datagram into the handler's buffer, standing in for the socket read.
func BenchmarkRtpHandlerCheck(b *testing.B) {
	datagram := benchDatagram(b)
	h := newTestStripHandler()
	h.Mode, h.strip = RtpTs, false
	b.ReportAllocs()
	for b.Loop() {
		h.bufEnd = copy(h.buf, datagram)
		if _, err := h.prepare(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRtpDecapsulatorStrip measures the RTP decapsulator reading one datagram and stripping its RTP header.
func BenchmarkRtpDecapsulatorStrip(b *testing.B) {
	datagram := benchDatagram(b)
	fake := &fakeReadCloser{data: datagram}
	dec := newTestRtpDecapsulator(fake)
	buf := make([]byte, 1500)
	b.ReportAllocs()
	for b.Loop() {
		fake.read = false
		if _, err := dec.Read(buf); err != nil {
			b.Fatal(err)
		}
	}
}
