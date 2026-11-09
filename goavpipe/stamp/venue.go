package stamp

import (
	"fmt"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/anc/venueclock"
)

// Mapper turns the venue clock state into the overlay's slot values. ANC calls it on every state change; the
// clock slots it names get the sample tolerance and the held-clock rule.
type Mapper interface {
	Slots(st *venueclock.State) map[string]goavpipe.SlotValue
	ClockSlots() []string
}

// GameClock is the game clock as a clock state: the value the venue sent, running while the "time stopped" stripe
// is off and no break or timeout clock is showing. In a break the feed sends the game clock as 12:00 with the
// stripe off while the break clock runs, so the stripe alone would start it. Invalid when the venue shows none.
func GameClock(st *venueclock.State) (goavpipe.ClockState, bool) {
	if !st.Clock.Valid {
		return goavpipe.ClockState{}, false
	}
	running := !st.Stopped && !st.BreakTime.Valid && !st.TimeoutTime.Valid
	return goavpipe.ClockState{Ms: int64(st.Clock.Ms), Running: running}, true
}

// VenueMapping says which overlay slots show which parts of the venue clock state, as text and a clock. A slot
// name left empty is not shown. The game clock is GameClock; the shot clock is text as the venue sends it, since
// it reads in whole seconds until the last five.
type VenueMapping struct {
	Home      string // home team's short name
	Away      string // visitor's short name
	Score     string // ScoreFormat with the home and visitor scores
	Period    string // the period name as sent ("1st", "OVT1")
	Clock     string // the game clock, a clock slot
	ShotClock string // the shot clock, a text slot

	ScoreFormat string // default "%d - %d"
	// ClockToleranceMs: a game clock sample within this of what the running clock already predicts does not make a
	// new event; the compositor keeps extrapolating. Start, stop and a larger drift do. Default 50.
	ClockToleranceMs int64
}

// DefaultVenueMapping uses the slot names home, away, score, period, clock and shot.
func DefaultVenueMapping() VenueMapping {
	return VenueMapping{Home: "home", Away: "away", Score: "score", Period: "period", Clock: "clock",
		ShotClock: "shot", ScoreFormat: "%d - %d", ClockToleranceMs: 50}
}

// sprintScore formats a score with format, "%d - %d" when empty.
func sprintScore(format string, home, away int) string {
	if format == "" {
		format = "%d - %d"
	}
	return fmt.Sprintf(format, home, away)
}

func (m VenueMapping) tolerance() int64 {
	if m.ClockToleranceMs == 0 {
		return 50
	}
	return m.ClockToleranceMs
}

// Slots is the overlay's slot values for the venue state st. A slot whose data the feed has not sent yet, or that
// shows a blank clock, is hidden.
func (m VenueMapping) Slots(st *venueclock.State) map[string]goavpipe.SlotValue {
	out := map[string]goavpipe.SlotValue{}
	text := func(slot, s string) {
		if slot == "" {
			return
		}
		if s == "" {
			out[slot] = goavpipe.HideValue()
		} else {
			out[slot] = goavpipe.TextValue(s)
		}
	}
	text(m.Home, st.Abbrevs[venueclock.Home])
	text(m.Away, st.Abbrevs[venueclock.Visitor])
	text(m.Period, st.PeriodName)
	if m.Score != "" {
		if st.Abbrevs[venueclock.Home] == "" && st.Score == [2]int{} {
			out[m.Score] = goavpipe.HideValue()
		} else {
			out[m.Score] = goavpipe.TextValue(sprintScore(m.ScoreFormat, st.Score[venueclock.Home],
				st.Score[venueclock.Visitor]))
		}
	}
	if m.Clock != "" {
		if c, ok := GameClock(st); ok {
			out[m.Clock] = goavpipe.SlotValue{Clock: &c}
		} else {
			out[m.Clock] = goavpipe.HideValue()
		}
	}
	if m.ShotClock != "" {
		if st.ShotClock.Valid {
			out[m.ShotClock] = goavpipe.TextValue(st.ShotClock.Text)
		} else {
			out[m.ShotClock] = goavpipe.HideValue()
		}
	}
	return out
}

// ClockSlots lists the slots whose value is a clock state, for the tolerance rule.
func (m VenueMapping) ClockSlots() []string {
	if m.Clock == "" {
		return nil
	}
	return []string{m.Clock}
}
