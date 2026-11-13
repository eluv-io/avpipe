package venueclock_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/eluv-io/avpipe/broadcastproto/anc/venueclock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// message frames a message as the feed does, CRC included.
func message(msgID, seq string, fields ...string) []byte {
	body := "4|6|" + msgID + "|" + seq + "|"
	for _, f := range fields {
		body += f + "|"
	}
	return append(append([]byte{0x01}, []byte(fmt.Sprintf("%s%02X", body, venueclock.CRC([]byte(body))))...), 0x04)
}

// TestCRC checks the CRC against values computed bit by bit from the polynomial, independently of the table.
func TestCRC(t *testing.T) {
	for body, want := range map[string]uint8{
		"4|6|31|100||0|0|1|":            0xE2,
		"4|6|30|101|8:55|||0|0|0|":      0x4F,
		"4|6|4|102|1|1|PrG|":            0x69,
		"4|6|17|103|Home|Away|HOM|AWY|": 0x9F,
		"4|6|40|104|FSC||LBL|0|15||":    0xC4,
		"":                              0x00,
	} {
		assert.Equal(t, want, venueclock.CRC([]byte(body)), body)
	}
}

// frame is the words of one DID 0x50 / SDID 0x01 packet: the encoder's header and the serial bytes of that frame.
func frame(serial ...byte) []byte {
	return append(append([]byte{}, venueclock.Header...), serial...)
}

func TestStreamOneFrame(t *testing.T) {
	var s venueclock.Stream
	raw := message("30", "1042", "11:52", "", "", "0", "0", "1")
	msgs, err := s.Feed(frame(raw...), 9000)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	m := msgs[0]
	assert.Equal(t, "4", m.Version)
	assert.Equal(t, "6", m.ProtocolID)
	assert.Equal(t, "30", m.MessageID)
	assert.Equal(t, "1042", m.Seq)
	assert.Equal(t, []string{"11:52", "", "", "0", "0", "1"}, m.Fields)
	assert.Equal(t, venueclock.CRC([]byte("4|6|30|1042|11:52|||0|0|1|")), m.Checksum)
	assert.True(t, m.ChecksumOK)
	assert.Equal(t, raw, m.Raw)
	assert.Equal(t, int64(9000), m.StartPts)
	assert.Equal(t, int64(9000), m.Pts)
	assert.Equal(t, "v4 proto 6 msg 30 seq 1042 11:52|||0|0|1", m.String())

	// A corrupted byte fails the CRC; the message is still delivered. Lower-case hex is accepted.
	bad := append([]byte{}, raw...)
	bad[12] ^= 0x01
	msgs, err = s.Feed(frame(bad...), 9001)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.False(t, msgs[0].ChecksumOK)
	assert.Contains(t, msgs[0].String(), " BAD CRC")
	lower := append([]byte{}, raw...)
	copy(lower[len(lower)-3:len(lower)-1], bytes.ToLower(lower[len(lower)-3:len(lower)-1]))
	msgs, err = s.Feed(frame(lower...), 9002)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.True(t, msgs[0].ChecksumOK)

	// An empty checksum is valid: the protocol allows it for hand-made messages
	noCS := append(append([]byte{0x01}, "4|6|1|7|112|92|"...), 0x04)
	msgs, err = s.Feed(frame(noCS...), 9003)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.False(t, msgs[0].HasChecksum)
	assert.True(t, msgs[0].ChecksumOK)
	assert.Equal(t, []string{"112", "92"}, msgs[0].Fields)
	assert.Equal(t, "v4 proto 6 msg 1 seq 7 112|92 NO CRC", msgs[0].String())
	assert.True(t, m.HasChecksum)

	// Header only: nothing. Two messages in one frame with junk between them: both.
	msgs, err = s.Feed(frame(), 10501)
	require.NoError(t, err)
	assert.Empty(t, msgs)
	a, b := message("1", "7", "0", "2"), message("31", "8", "24")
	msgs, err = s.Feed(frame(append(append(append([]byte{0xff}, a...), 0, 0), b...)...), 12002)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, []string{"0", "2"}, msgs[0].Fields)
	assert.Equal(t, []string{"24"}, msgs[1].Fields)
	assert.Equal(t, 0, s.Aborted+s.Malformed)
}

// TestStreamSplit cuts a message at every byte and feeds the two halves as consecutive frames, each with the
// encoder's header: the message must come out whole with the pts of the frame that completed it.
func TestStreamSplit(t *testing.T) {
	raw := message("17", "99", "Home", "Away", "HOM", "AWY")
	for cut := 1; cut < len(raw); cut++ {
		var s venueclock.Stream
		msgs, err := s.Feed(frame(raw[:cut]...), 1000)
		require.NoError(t, err, "cut %d", cut)
		assert.Empty(t, msgs, "cut %d: not complete yet", cut)
		msgs, err = s.Feed(frame(raw[cut:]...), 2501)
		require.NoError(t, err, "cut %d", cut)
		require.Len(t, msgs, 1, "cut %d", cut)
		assert.Equal(t, raw, msgs[0].Raw, "cut %d: header bytes removed", cut)
		assert.Equal(t, []string{"Home", "Away", "HOM", "AWY"}, msgs[0].Fields)
		assert.Equal(t, int64(1000), msgs[0].StartPts)
		assert.Equal(t, int64(2501), msgs[0].Pts)
	}

	// Three frames, the middle one with no serial bytes
	var s venueclock.Stream
	_, err := s.Feed(frame(raw[:5]...), 0)
	require.NoError(t, err)
	_, err = s.Feed(frame(), 1)
	require.NoError(t, err)
	msgs, err := s.Feed(frame(raw[5:]...), 2)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, raw, msgs[0].Raw)
}

func TestStreamAbortAndMalformed(t *testing.T) {
	var s venueclock.Stream
	good := message("2", "6003", "0", "0")

	// A message cut short by the next SOH: the partial one is dropped and counted
	partial := message("2", "6002", "0", "0")[:9]
	msgs, err := s.Feed(frame(append(partial, good...)...), 5)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "6003", msgs[0].Seq)
	assert.Equal(t, 1, s.Aborted)

	// Too few fields and a non-hex checksum: dropped, counted, reported; the good message after them is returned
	bad1 := []byte{0x01, '4', '|', '6', 'A', '5', 0x04}
	bad2 := []byte{0x01, '4', '|', '6', '|', '1', '|', '2', '|', 'Z', 'Z', 0x04}
	msgs, err = s.Feed(frame(append(append(bad1, bad2...), good...)...), 6)
	assert.ErrorIs(t, err, venueclock.ErrFraming)
	require.Len(t, msgs, 1)
	assert.Equal(t, 2, s.Malformed)

	// EOT with no message open is ignored, as are bytes before the first SOH
	msgs, err = s.Feed(frame(0x04, 'x', 0x04), 7)
	require.NoError(t, err)
	assert.Empty(t, msgs)

	// A SOH followed by a line of junk with no EOT: dropped once the message would be too long, then the feed
	// recovers at the next SOH
	junk := bytes.Repeat([]byte{'x'}, venueclock.MaxMessage)
	msgs, err = s.Feed(frame(append([]byte{0x01}, junk...)...), 8)
	require.NoError(t, err)
	assert.Empty(t, msgs)
	assert.Equal(t, 2, s.Aborted)
	msgs, err = s.Feed(frame(good...), 9)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	// A packet without the header is taken as serial bytes
	msgs, err = s.Feed(good, 10)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	// A different header
	s2 := venueclock.Stream{Header: []byte{0xAA}}
	msgs, err = s2.Feed(append([]byte{0xAA}, good...), 11)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.True(t, bytes.Equal(good, msgs[0].Raw))
}

func TestStreamFeedPackets(t *testing.T) {
	var s venueclock.Stream
	raw := message("4", "12", "3", "1", "1st")
	frame1 := []anc.Packet{
		{DID: 0x60, SDID: 0x60, UDW: make([]byte, 16)},
		{DID: 0x50, SDID: 0x01, UDW: frame(raw[:4]...)},
		{DID: 0x61, SDID: 0x01, UDW: []byte{0x01}}, // a caption packet's SOH-looking byte is not the feed
	}
	msgs, err := s.FeedPackets(frame1, 100)
	require.NoError(t, err)
	assert.Empty(t, msgs)
	msgs, err = s.FeedPackets([]anc.Packet{{DID: 0x50, SDID: 0x01, UDW: frame(raw[4:]...)}}, 200)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, []string{"3", "1", "1st"}, msgs[0].Fields)
	assert.Equal(t, int64(100), msgs[0].StartPts)
	assert.Equal(t, int64(200), msgs[0].Pts)
}
