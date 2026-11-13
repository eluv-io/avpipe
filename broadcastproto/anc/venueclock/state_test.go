package venueclock_test

import (
	"testing"

	"github.com/eluv-io/avpipe/broadcastproto/anc/venueclock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseValue(t *testing.T) {
	for _, c := range []struct {
		text  string
		ms    int
		valid bool
	}{
		{"12:00", 720000, true},
		{"9:01", 541000, true},
		{"0:43", 43000, true},
		{"58.8", 58800, true},
		{"4.9", 4900, true},
		{"0.0", 0, true},
		{"23", 23000, true},
		{"", 0, false},
		{"1:60", 0, false},
		{"-1", 0, false},
		{"x", 0, false},
	} {
		v := venueclock.ParseValue(c.text)
		assert.Equal(t, c.valid, v.Valid, c.text)
		assert.Equal(t, c.ms, v.Ms, c.text)
		assert.Equal(t, c.text, v.Text)
	}
	assert.Equal(t, "-", venueclock.ParseValue("").String())
	assert.Equal(t, "58.8", venueclock.ParseValue("58.8").String())
}

// spec parses one of the protocol document's example messages, "4|6|id|seq|fields...|" without SOH, checksum and
// EOT.
func spec(t *testing.T, text string) venueclock.Message {
	var s venueclock.Stream
	msgs, err := s.Feed(append(append([]byte{0x01}, text...), 0x04), 1000)
	require.NoError(t, err, text)
	require.Len(t, msgs, 1, text)
	return msgs[0]
}

// TestStateApplySpecExamples applies the example messages of the protocol document.
func TestStateApplySpecExamples(t *testing.T) {
	var s venueclock.State
	assert.True(t, s.Apply(spec(t, "4|6|17|1|Home|Visitor|HOM|VIS|")))
	assert.Equal(t, [2]string{"Home", "Visitor"}, s.Teams)
	assert.Equal(t, "HOM", s.Abbrevs[venueclock.Home])
	assert.Equal(t, "VIS", s.Abbrevs[venueclock.Visitor])

	assert.True(t, s.Apply(spec(t, "4|6|1|2|112|92|")))
	assert.Equal(t, [2]int{112, 92}, s.Score)

	assert.True(t, s.Apply(spec(t, "4|6|2|3|5|3|0|0|")))
	assert.Equal(t, [2]int{5, 3}, s.Fouls)
	assert.Equal(t, [2]int{0, 0}, s.Bonus)

	assert.True(t, s.Apply(spec(t, "4|6|4|4|4|6|OVT2|")))
	assert.Equal(t, venueclock.PeriodOvertime, s.PeriodType)
	assert.Equal(t, 6, s.PeriodNumber)
	assert.Equal(t, "OVT2", s.PeriodName)

	assert.True(t, s.Apply(spec(t, "4|6|5|5|1|")))
	assert.Equal(t, venueclock.PossessionHome, s.Possession)

	// Game time: running, last minute, quarter break, timeout with the game time held
	assert.True(t, s.Apply(spec(t, "4|6|30|6|9:01|||0|0|0|")))
	assert.Equal(t, 541000, s.Clock.Ms)
	assert.False(t, s.BreakTime.Valid)
	assert.False(t, s.TimeoutTime.Valid)
	assert.False(t, s.Horn || s.EndOfQuarter || s.Stopped)
	assert.True(t, s.Apply(spec(t, "4|6|30|7|58.8|||0|0|0|")))
	assert.Equal(t, 58800, s.Clock.Ms)
	assert.True(t, s.Apply(spec(t, "4|6|30|8|12:00|1:30||0|0|0|")))
	assert.Equal(t, 90000, s.BreakTime.Ms)
	assert.True(t, s.Apply(spec(t, "4|6|30|9|2:02||1:45|0|0|1|")))
	assert.Equal(t, 122000, s.Clock.Ms)
	assert.False(t, s.BreakTime.Valid)
	assert.Equal(t, 105000, s.TimeoutTime.Ms)
	assert.True(t, s.Stopped)

	// Shot clock: counting down, the last five seconds, expired
	assert.True(t, s.Apply(spec(t, "4|6|31|10|23|0|0|1|")))
	assert.Equal(t, 23000, s.ShotClock.Ms)
	assert.True(t, s.ShotRunning)
	assert.False(t, s.ShotHorn || s.ShotExpired)
	assert.True(t, s.Apply(spec(t, "4|6|31|11|4.9|0|0|1|")))
	assert.Equal(t, 4900, s.ShotClock.Ms)
	assert.True(t, s.Apply(spec(t, "4|6|31|12|0.0|1|1|0|")))
	assert.Equal(t, 0, s.ShotClock.Ms)
	assert.True(t, s.ShotClock.Valid)
	assert.True(t, s.ShotHorn && s.ShotExpired && !s.ShotRunning)

	assert.True(t, s.Apply(spec(t, "4|6|32|13|A|1|0|5|6|0|0|1|1|")))
	assert.Equal(t, venueclock.Timeouts{Running: "A", FullTaken: [2]int{1, 0}, FullLeft: [2]int{5, 6},
		ShortTaken: [2]int{0, 0}, ShortLeft: [2]int{1, 1}}, s.Timeouts)

	assert.True(t, s.Apply(spec(t, "4|6|33|14|9:01 AM|9:01:25|")))
	assert.Equal(t, "9:01 AM", s.DayTimeLocal)
	assert.Equal(t, "9:01:25", s.DayTime)

	assert.Equal(t, "14", s.Seq)
	assert.Equal(t, int64(1000), s.Pts)
	assert.Equal(t, "HOM VIS 112-92 OVT2 clock 2:02 shot 0.0 timeout 1:45 STOPPED SHOT-EXPIRED poss HOM "+
		"fouls 5-3 bonus 0-0 TO A", s.String())
}

func TestStateApplyEdgeCases(t *testing.T) {
	msg := func(id, seq string, fields ...string) venueclock.Message {
		return venueclock.Message{ProtocolID: venueclock.Protocol, MessageID: id, Seq: seq, Fields: fields, Pts: 1000}
	}
	var s venueclock.State

	// Another sport's protocol: its message 1 is not a basketball score, only Pts and Seq move
	other := msg("1", "0", "3", "1")
	other.ProtocolID = "7"
	assert.False(t, s.Apply(other))
	assert.Equal(t, [2]int{0, 0}, s.Score)
	assert.Equal(t, "0", s.Seq)

	assert.True(t, s.Apply(msg("17", "1", "Home", "Away", "HOM", "AWY")))
	assert.True(t, s.Apply(msg("4", "2", "3", "1", "1st")))
	assert.True(t, s.Apply(msg("30", "3", "12:00", "", "", "0", "0", "1")))
	assert.True(t, s.Apply(msg("31", "4", "24", "0", "0", "1")))
	assert.True(t, s.Apply(msg("1", "5", "0", "2")))
	assert.Equal(t, venueclock.PeriodPlaying, s.PeriodType)
	assert.Equal(t, 1, s.PeriodNumber)
	assert.Equal(t, "1st", s.PeriodName)
	assert.Equal(t, "HOM AWY 0-2 1st clock 12:00 shot 24 STOPPED", s.String())

	// A repeated message changes nothing but Pts and Seq
	m := msg("1", "6", "0", "2")
	m.Pts = 2000
	assert.False(t, s.Apply(m))
	assert.Equal(t, int64(2000), s.Pts)
	assert.Equal(t, "6", s.Seq)

	// The horn at zero, then a blank clock
	assert.True(t, s.Apply(msg("30", "7", "0.0", "", "", "1", "0", "0")))
	assert.True(t, s.Horn)
	assert.False(t, s.Stopped)
	assert.Equal(t, 0, s.Clock.Ms)
	assert.True(t, s.Clock.Valid)
	assert.Contains(t, s.String(), " HORN")
	assert.True(t, s.Apply(msg("30", "8", "", "", "", "0", "1", "0")))
	assert.False(t, s.Clock.Valid)
	assert.True(t, s.EndOfQuarter)
	assert.Contains(t, s.String(), "clock - shot 24")
	assert.Contains(t, s.String(), " EOQ")

	// An empty int field clears the value; a field that does not parse is skipped
	assert.True(t, s.Apply(msg("1", "9", "", "")))
	assert.Equal(t, [2]int{0, 0}, s.Score)
	assert.False(t, s.Apply(msg("1", "10", "x", "0")))
	assert.Equal(t, [2]int{0, 0}, s.Score)
	assert.True(t, s.Apply(msg("1", "11", "x", "7")))
	assert.Equal(t, [2]int{0, 7}, s.Score)
	assert.True(t, s.Apply(msg("4", "12", "", "", "")))
	assert.Equal(t, venueclock.PeriodNone, s.PeriodType)
	assert.Equal(t, 0, s.PeriodNumber)
	assert.Equal(t, "", s.PeriodName)

	// A message with fewer fields than its layout is applied as far as it goes; extra fields are ignored
	assert.True(t, s.Apply(msg("30", "13", "5:00")))
	assert.Equal(t, 300000, s.Clock.Ms)
	assert.True(t, s.EndOfQuarter, "field 5 absent leaves the flag as it was")
	assert.True(t, s.Apply(msg("2", "14", "3", "4", "1", "2", "9", "9")))
	assert.Equal(t, [2]int{3, 4}, s.Fouls)
	assert.Equal(t, [2]int{1, 2}, s.Bonus)
	assert.Contains(t, s.String(), " fouls 3-4 bonus 1-2")

	// Unknown ids are ignored but still move Seq
	assert.False(t, s.Apply(msg("40", "15", "FSC", "", "SCT", "50", "100")))
	assert.False(t, s.Apply(msg("90", "16", "0", "ABCDEF", "3", "63", "0")))
	assert.Equal(t, "16", s.Seq, "Seq moves on every message")

	// A space is the empty string: no team runs a timeout
	assert.True(t, s.Apply(msg("32", "20", "A", "1", "0", "5", "6", "0", "0", "1", "1")))
	assert.Contains(t, s.String(), " TO A")
	assert.True(t, s.Apply(msg("32", "21", " ", "1", "0", "5", "6", "0", "0", "1", "1")))
	assert.Equal(t, "", s.Timeouts.Running)
	assert.NotContains(t, s.String(), " TO ")
	assert.False(t, s.Apply(msg("17", "22", " Home ", "Away", "HOM", "AWY")), "trimmed, so nothing changed")
	assert.Equal(t, "Home", s.Teams[venueclock.Home])

	// Possession in the state line
	assert.True(t, s.Apply(msg("5", "17", "2")))
	assert.Contains(t, s.String(), " poss AWY")
	assert.True(t, s.Apply(msg("5", "18", "3")))
	assert.Contains(t, s.String(), " poss both")
	assert.True(t, s.Apply(msg("5", "19", "")))
	assert.NotContains(t, s.String(), " poss")
}
