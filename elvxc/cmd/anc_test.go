package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eluv-io/avpipe/broadcastproto/anctest"
	"github.com/eluv-io/common-go/media/anc"
	"github.com/eluv-io/common-go/media/anc/venueclock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunAnc(t *testing.T) {
	src := "../../media/bbb_1080p_30fps_10sec.mp4"
	if _, err := os.Stat(src); err != nil {
		t.Skipf("no test media: %v", err)
	}
	// One clock message, split over the first two frames behind the encoder's header
	clockBody := "4|6|30|1|11:52|||0|0|1|"
	clockMsg := append(append([]byte{0x01}, []byte(fmt.Sprintf("%s%02X", clockBody, venueclock.CRC([]byte(clockBody))))...), 0x04)
	clockFrames := [][]byte{
		append(append([]byte{}, venueclock.Header...), clockMsg[:7]...),
		append(append([]byte{}, venueclock.Header...), clockMsg[7:]...),
	}
	ts := filepath.Join(t.TempDir(), "anc.ts")
	_, err := anctest.WriteTS(src, ts, func(i int, pts int64) []byte {
		h, m, s, f := anctest.TimecodeAt(i, 30)
		pkts := []anc.Packet{{Line: 9, DID: 0x60, SDID: 0x60, UDW: anctest.TimecodeUDW(h, m, s, f, false, 2)}}
		if i == 0 {
			pkts = append(pkts, anc.Packet{Line: 12, DID: 0x41, SDID: 0x07, UDW: []byte{1, 2, 3}})
		}
		if i < len(clockFrames) {
			pkts = append(pkts, anc.Packet{Line: 14, DID: 0x50, SDID: 0x01, UDW: clockFrames[i]})
		}
		return anc.EncodePES(pkts)
	})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runAnc(&out, ts, ancOpts{max: 2, raw: true}))
	s := out.String()
	assert.Contains(t, s, "DID 0x50 SDID 0x01 private-user-data 11 words")
	assert.NotContains(t, s, "clock pts=")

	// --clock: the message comes out with the pts of the frame that completed it, and a state line
	out.Reset()
	require.NoError(t, runAnc(&out, ts, ancOpts{max: 3, clock: true, quiet: true}))
	s = out.String()
	assert.Contains(t, s, "clock pts=3000 (0:00:00.033) v4 proto 6 msg 30 seq 1 11:52|||0|0|1\n")
	assert.Contains(t, s, "      state:   0-0  clock 11:52 shot - STOPPED\n")
	assert.Contains(t, s, "clock messages: 1, bad crc: 0, aborted: 0, malformed: 0\n")
	assert.Contains(t, s, "clock state:   0-0  clock 11:52 shot - STOPPED\n")

	out.Reset()
	require.NoError(t, runAnc(&out, ts, ancOpts{max: 2, raw: true}))
	s = out.String()
	assert.Contains(t, s, "pts=0 (0:00:00.000)")
	assert.Contains(t, s, "DID 0x60 SDID 0x60 timecode 16 words 00:00:00:00 VITC2")
	assert.Contains(t, s, "DID 0x41 SDID 0x07 scte-104 3 words")
	assert.Contains(t, s, "\n    010203\n", "raw words")
	assert.Contains(t, s, "pts=3000 (0:00:00.033)")
	assert.Contains(t, s, "PES packets: 2, ancillary packets: 5, parity/checksum failures: 0, parse errors: 0")
	assert.Contains(t, s, `codec smpte_2038 tag "VANC"`)
	assert.Contains(t, s, "last time code: 00:00:00:01")
	assert.Equal(t, 2, strings.Count(s, "pts="), "two PES lines with --max 2")

	// Quiet: the whole file, summary only
	out.Reset()
	require.NoError(t, runAnc(&out, ts, ancOpts{quiet: true}))
	s = out.String()
	assert.NotContains(t, s, "pts=")
	assert.Contains(t, s, "PES packets: 300, ancillary packets: 303")
	assert.Contains(t, s, "pts: 0 to 897000 (0:00:00.000 to 0:00:09.966, 9.967 s)")
	assert.Contains(t, s, "DID 0x60 SDID 0x60 timecode       300")

	// A stream the PMT does not mark: nothing without the id, everything with it
	unmarked := filepath.Join(t.TempDir(), "unmarked.ts")
	_, err = anctest.WriteTSUnmarked(src, unmarked, func(i int, pts int64) []byte {
		return anc.EncodePES([]anc.Packet{{Line: 9, DID: 0x41, SDID: 0x07}})
	})
	require.NoError(t, err)
	out.Reset()
	assert.Error(t, runAnc(&out, unmarked, ancOpts{quiet: true}))
	assert.Contains(t, out.String(), "no ST 2038 stream found")
	out.Reset()
	require.NoError(t, runAnc(&out, unmarked, ancOpts{quiet: true, ancStreamID: 0x101}))
	assert.Contains(t, out.String(), "PES packets: 300, ancillary packets: 300")
	assert.Contains(t, out.String(), "codec bin_data")
}
