package venueclock

import (
	"fmt"
	"strconv"
	"strings"
)

// Message ids of the protocol and their data fields. Team A is the home team, team B the visitor. An int field is
// sent as a decimal string with no leading zero; an empty field clears the value (0 for an int, blank for a
// string). The feed sends a message when its data changes, the running clocks up to ten times a second, and
// repeats every message at least once every ten seconds.
//
//	1   score          score A | score B
//	2   fouls          fouls A | fouls B | bonus A | bonus B           (bonus 0, 1 or 2)
//	4   period         type | number | name                           (type: see the Period constants; name "1",
//	                                                                  "B2", "OVT1" or as the venue configures it)
//	5   possession     team                                           (see the Possession constants, NCAA rules)
//	17  teams          name A | name B | short A | short B            (names up to 32 characters, short up to 3)
//	30  game time      game | break | timeout | horn | red back light | red stripe
//	                                                                  (times as text, see Value; horn 1 while the
//	                                                                  game-time horn sounds; red back light 1 at the
//	                                                                  end of a quarter; red stripe 1 while the game
//	                                                                  time is stopped)
//	31  shot clock     time | horn | yellow back light | running      (yellow back light 1 when the shot clock has
//	                                                                  expired; running 0 stopped, 1 running)
//	32  timeouts       team | full taken A | full taken B | full left A | full left B |
//	                   short taken A | short taken B | short left A | short left B
//	                                                                  (team "A" or "B" running a timeout, empty for
//	                                                                  a mandatory timeout with no team)
//	33  day time       local format | hh:mm:ss
//	40  brightness, 41 broadcast test pattern, 90 shot clock keep-alive, 91 bootloader: control of the venue's
//	                   displays (device types FSC front shot clock, SSC side shot clock, LST light stripe table, STB
//	                   stanchion box; device ids unused), ignored
const (
	// Protocol is the protocol id of the basketball messages below. A console for another sport sends another id
	// with its own message table; State ignores those messages.
	Protocol = "6"

	MsgScore      = "1"
	MsgFouls      = "2"
	MsgPeriod     = "4"
	MsgPossession = "5"
	MsgTeams      = "17"
	MsgGameTime   = "30"
	MsgShotClock  = "31"
	MsgTimeouts   = "32"
	MsgDayTime    = "33"
)

// Period types, the first field of MsgPeriod.
const (
	PeriodNone     = 0
	PeriodPreGame  = 1
	PeriodPause    = 2 // a break between periods, or the introduction
	PeriodPlaying  = 3 // a regular period
	PeriodOvertime = 4
)

// Ball possession, the field of MsgPossession.
const (
	PossessionNone    = 0
	PossessionHome    = 1
	PossessionVisitor = 2
	PossessionBoth    = 3
)

// Indexes of the per-team arrays of State.
const (
	Home    = 0
	Visitor = 1
)

// State is the venue clock as the feed's messages describe it: Apply updates it message by message. Strings are
// kept as sent; the clock values are parsed to milliseconds with Valid false for a blank field. Per-team arrays
// are indexed by Home and Visitor.
type State struct {
	Pts int64  // pts of the last message applied
	Seq string // sequence number of the last message applied

	Teams   [2]string // names
	Abbrevs [2]string // short names
	Score   [2]int
	Fouls   [2]int
	Bonus   [2]int // 0, 1 or 2

	Possession   int // PossessionNone to PossessionBoth
	PeriodType   int // PeriodNone to PeriodOvertime
	PeriodNumber int // 1 to 7
	PeriodName   string

	Clock        Value // game time, also the pre-game and half-time countdowns
	BreakTime    Value // quarter and overtime breaks, the introduction
	TimeoutTime  Value
	Horn         bool // the game-time horn sounds with this message
	EndOfQuarter bool // red back light
	Stopped      bool // red stripe: the game time is held

	ShotClock   Value
	ShotHorn    bool
	ShotExpired bool // yellow back light
	ShotRunning bool

	Timeouts Timeouts

	DayTime      string // hh:mm:ss
	DayTimeLocal string // in the venue's local format, "9:01 AM"
}

// Timeouts is the timeout count of MsgTimeouts.
type Timeouts struct {
	Running                                    string // "A" (home) or "B" (visitor) during a timeout, "" otherwise
	FullTaken, FullLeft, ShortTaken, ShortLeft [2]int
}

// Value is a clock field: the text as sent and its value.
type Value struct {
	Text  string
	Ms    int  // milliseconds, when Valid
	Valid bool // false for a blank field or one that did not parse
}

func (v Value) String() string {
	if !v.Valid {
		return "-"
	}
	return v.Text
}

// key is the state without Pts and Seq, for change detection.
func (s *State) key() State {
	k := *s
	k.Pts, k.Seq = 0, ""
	return k
}

// Apply updates the state from m and reports whether anything other than Pts and Seq changed. Every message moves
// Pts and Seq. Messages of other protocols or unknown ids, and fields beyond the known ones, are ignored; a known
// message with fewer fields than its layout is applied as far as it goes. An empty int field clears its value to 0,
// as the protocol says; an int field that does not parse is skipped.
func (s *State) Apply(m Message) bool {
	before := s.key()
	s.Pts, s.Seq = m.Pts, m.Seq
	if m.ProtocolID != Protocol {
		return false
	}
	f := fieldsOf(m.Fields)
	switch m.MessageID {
	case MsgScore:
		f.ints(0, &s.Score[Home], &s.Score[Visitor])
	case MsgFouls:
		f.ints(0, &s.Fouls[Home], &s.Fouls[Visitor], &s.Bonus[Home], &s.Bonus[Visitor])
	case MsgPeriod:
		f.ints(0, &s.PeriodType, &s.PeriodNumber)
		f.str(2, &s.PeriodName)
	case MsgPossession:
		f.ints(0, &s.Possession)
	case MsgTeams:
		f.str(0, &s.Teams[Home])
		f.str(1, &s.Teams[Visitor])
		f.str(2, &s.Abbrevs[Home])
		f.str(3, &s.Abbrevs[Visitor])
	case MsgGameTime:
		f.value(0, &s.Clock)
		f.value(1, &s.BreakTime)
		f.value(2, &s.TimeoutTime)
		f.flag(3, &s.Horn)
		f.flag(4, &s.EndOfQuarter)
		f.flag(5, &s.Stopped)
	case MsgShotClock:
		f.value(0, &s.ShotClock)
		f.flag(1, &s.ShotHorn)
		f.flag(2, &s.ShotExpired)
		f.flag(3, &s.ShotRunning)
	case MsgTimeouts:
		t := &s.Timeouts
		f.str(0, &t.Running)
		f.ints(1, &t.FullTaken[Home], &t.FullTaken[Visitor], &t.FullLeft[Home], &t.FullLeft[Visitor],
			&t.ShortTaken[Home], &t.ShortTaken[Visitor], &t.ShortLeft[Home], &t.ShortLeft[Visitor])
	case MsgDayTime:
		f.str(0, &s.DayTimeLocal)
		f.str(1, &s.DayTime)
	}
	return s.key() != before
}

// fieldsOf reads a message's data fields by position; a position past the end leaves the target alone.
type fieldsOf []string

// str reads a string field. Fields are sent trimmed and a space stands for the empty string, so surrounding
// white space is removed.
func (f fieldsOf) str(i int, dst *string) {
	if i < len(f) {
		*dst = strings.TrimSpace(f[i])
	}
}

// ints reads consecutive int fields from position i. An empty field is 0; a field that does not parse is skipped.
func (f fieldsOf) ints(i int, dst ...*int) {
	for j, d := range dst {
		if i+j >= len(f) {
			return
		}
		if f[i+j] == "" {
			*d = 0
		} else if n, err := strconv.Atoi(f[i+j]); err == nil {
			*d = n
		}
	}
}

// flag reads a 0/1 field: "1" is true, anything else false.
func (f fieldsOf) flag(i int, dst *bool) {
	if i < len(f) {
		*dst = f[i] == "1"
	}
}

func (f fieldsOf) value(i int, dst *Value) {
	if i < len(f) {
		*dst = ParseValue(f[i])
	}
}

// ParseValue reads a time field: "m:ss" (game and break times, "12:00"), "s.t" (the last minute of the game time
// and the last five seconds of the shot clock, "58.8") or "s" ("23") to milliseconds. A blank or unreadable field
// is not Valid.
func ParseValue(text string) Value {
	v := Value{Text: text}
	if text == "" {
		return v
	}
	if i := strings.IndexByte(text, ':'); i >= 0 {
		mins, err1 := strconv.Atoi(text[:i])
		secs, err2 := strconv.Atoi(text[i+1:])
		if err1 != nil || err2 != nil || mins < 0 || secs < 0 || secs > 59 {
			return v
		}
		v.Ms, v.Valid = (mins*60+secs)*1000, true
		return v
	}
	secs, err := strconv.ParseFloat(text, 64)
	if err != nil || secs < 0 {
		return v
	}
	v.Ms, v.Valid = int(secs*1000+0.5), true
	return v
}

// String is one line of the state: teams, score, period, game and shot clock, then the flags and clocks that are
// set.
func (s *State) String() string {
	out := fmt.Sprintf("%s %s %d-%d %s clock %s shot %s", s.Abbrevs[Home], s.Abbrevs[Visitor], s.Score[Home],
		s.Score[Visitor], s.PeriodName, s.Clock, s.ShotClock)
	if s.BreakTime.Valid {
		out += " break " + s.BreakTime.Text
	}
	if s.TimeoutTime.Valid {
		out += " timeout " + s.TimeoutTime.Text
	}
	if s.Stopped {
		out += " STOPPED"
	}
	if s.Horn {
		out += " HORN"
	}
	if s.EndOfQuarter {
		out += " EOQ"
	}
	if s.ShotExpired {
		out += " SHOT-EXPIRED"
	}
	switch s.Possession {
	case PossessionHome:
		out += " poss " + s.Abbrevs[Home]
	case PossessionVisitor:
		out += " poss " + s.Abbrevs[Visitor]
	case PossessionBoth:
		out += " poss both"
	}
	if s.Fouls != [2]int{} || s.Bonus != [2]int{} {
		out += fmt.Sprintf(" fouls %d-%d bonus %d-%d", s.Fouls[Home], s.Fouls[Visitor], s.Bonus[Home],
			s.Bonus[Visitor])
	}
	if s.Timeouts.Running != "" {
		out += " TO " + s.Timeouts.Running
	}
	return out
}
