package mpegts

import (
	"testing"
	"time"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/common-go/media/pktpool"
)

// benchDatagram builds an n-TS-packet datagram (null packets with a sync byte).
func benchDatagram(n int) []byte {
	data := make([]byte, n*188)
	for i := range n {
		data[i*188] = 0x47 // TS sync byte
	}
	return data
}

// BenchmarkFraming compares the two output-framing paths for an ATS-TS datagram. Both incur the unavoidable read-load
// (From) that happens in production. The "copy" path is the pool-less []byte fallback, which copies the header, prefix
// and payload into the output buffer; the "frametlv" path is what the pooled consumers use, framing zero-copy into the
// packet's reserved head room. The gap is the per-datagram memcpy (and TLV-header allocation) that FrameTlv eliminates.
func BenchmarkFraming(b *testing.B) {
	const packets = 7 // ~1316 bytes, a typical MTU-sized datagram
	datagram := benchDatagram(packets)
	now := time.Unix(1000, 0)

	newProc := func() *MpegtsPacketProcessor {
		return NewMpegtsPacketProcessor(
			TsConfig{SegmentLengthSec: 0, Packaging: transport.AtsTs},
			&recordingSequentialOpener{},
			1,
		)
	}

	b.Run("copy", func(b *testing.B) {
		pp := newProc()
		pool := pktpool.NewPacketPool(outputTlvWrapCap, 2048)
		b.SetBytes(int64(len(datagram)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			res := pool.Borrow()
			_ = res.T.From(datagram)
			_, _ = pp.frame(now, res.T.Data, nil, false, 0) // nil packet -> []byte copy fallback
			res.Release()
		}
	})

	b.Run("frametlv", func(b *testing.B) {
		pp := newProc()
		pool := pktpool.NewPacketPool(outputTlvWrapCap, 2048)
		b.SetBytes(int64(len(datagram)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			res := pool.Borrow()
			_ = res.T.From(datagram)
			_, _ = pp.frame(now, res.T.Data, res.T, false, 0) // pooled packet -> zero-copy FrameTlv
			res.Release()
		}
	})
}
