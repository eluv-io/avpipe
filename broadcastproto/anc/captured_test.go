package anc_test

import (
	"encoding/hex"
	"testing"

	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturedPES are whole PES packets (header included) of an ST 2038 stream from a broadcast encoder: DID 0x50 /
// SDID 0x01 packets on line 14, one CEA-708 caption packet, and ST 352 payload identifiers with ST 12-2 time code
// on lines 9 and 10.
var capturedPES = []string{
	"000001bd0016858005231a339e4d0003800250405048620071e008d3",
	"000001bd0016858005231a3392930003800250405048620071e008d3",
	"000001bd004a858005231a33929300030001614062ba5a698ad7f50d9b8f2727aafc60180bea00802fa80200bea00802fa80200bea00802fa80200bea00802fa80200bea008027466e3c7418dfffffff",
	"000001bd0016858005231a390c050003800250405048620071e008d3",
	"000001bd0026858005231a3917bf00025e2260981106620098200502008c20098200482008020098200463ff",
	"000001bd0036858005231a3917bf00029e224140504626ca6010146b00029e4e6098110a420098200502008c20098200482008020098200843ffffff",
	"000001bd0016858005231a3917bf0003800250405048620071e008d3",
	"000001bd0026858005231a39237b00025e226098110a410898200502006c20098200482008020098200a63ff",
	"000001bd0036858005231a39237b00029e224140504626ca6010146b00029e4e6098110a420098200502008c20098200482008020098200843ffffff",
	"000001bd0016858005231a39237b0003800250405048620071e008d3",
	"000001bd0026858005231a392f3500025e2260981104220050200942008c200982004820080200982009e3ff",
	"000001bd0036858005231a392f3500029e224140504626ca6010146b00029e4e60981108020050200942008c200982004820080200982005c3ffffff",
}

// TestParsePESCaptured runs the parser over packets a real encoder produced: every packet must parse with its
// parity and checksum intact, and the time codes must read as the 29.97 drop-frame sequence they are.
func TestParsePESCaptured(t *testing.T) {
	kinds := map[anc.Kind]int{}
	var timecodes []anc.Timecode
	for i, s := range capturedPES {
		b, err := hex.DecodeString(s)
		require.NoError(t, err)
		headerLen := int(b[8])
		pkts, err := anc.ParsePES(b[9+headerLen:])
		require.NoError(t, err, "PES %d", i)
		require.NotEmpty(t, pkts, "PES %d", i)
		for j, p := range pkts {
			assert.True(t, p.ParityOK, "PES %d packet %d parity: %s", i, j, p)
			assert.True(t, p.ChecksumOK, "PES %d packet %d checksum: %s", i, j, p)
			kinds[p.Kind()]++
			if p.Kind() == anc.KindTimecode {
				tc, err := anc.ParseTimecode(p.UDW)
				require.NoError(t, err, "PES %d packet %d", i, j)
				t.Logf("PES %d packet %d: %s -> %s %s DBB2 0x%02x", i, j, p, tc, tc.Source(), tc.DBB2)
				timecodes = append(timecodes, tc)
			}
		}
	}
	assert.Equal(t, 1, kinds[anc.KindCaptions708])
	assert.Equal(t, 3, kinds[anc.KindPayloadID])
	assert.Equal(t, 6, kinds[anc.KindTimecode])
	assert.Equal(t, 5, kinds[anc.KindPrivateUserData], "DID 0x50 / SDID 0x01, four constant words, not clock messages")
	require.Len(t, timecodes, 6)
	// A 29.97 drop-frame signal: VITC on line 9 and LTC on line 10 for field 1 and field 2 of one frame, then
	// field 1 of the next. The VITC field 2 packet carries the 525-line field mark (bit 27); LTC repeats per field.
	want := []struct{ tc, src string }{
		{"20:26:34;29", "VITC1"}, {"20:26:34;29", "LTC"},
		{"20:26:34;29", "VITC2"}, {"20:26:34;29", "LTC"},
		{"20:26:35;00", "VITC1"}, {"20:26:35;00", "LTC"},
	}
	for i, w := range want {
		assert.Equal(t, w.tc, timecodes[i].String(), "time code %d", i)
		assert.Equal(t, w.src, timecodes[i].Source(), "time code %d", i)
		assert.Equal(t, w.src == "VITC2", timecodes[i].Flags[0], "time code %d field mark", i)
		assert.True(t, timecodes[i].DropFrame, "time code %d", i)
	}
}
