package stamp

import (
	"fmt"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/anc/venueclock"
)

// Mapper turns the venue clock state into the overlay's slot values. ANC calls it on every state change.
type Mapper interface {
	Slots(st *venueclock.State) map[string]goavpipe.SlotValue
	// ClockSlots lists the slots whose value is a clock state. They get the sample tolerance.
	ClockSlots() []string
	// ShotClockSlot names the clock slot that shows the shot clock, "" when none does. ANC replaces the value Slots
	// gives it with the clock it follows from the samples.
	ShotClockSlot() string
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

// VenueMapping says which overlay slots show which parts of the venue clock state, as text and two clocks. A slot
// name left empty is not shown. The game clock is GameClock; the shot clock is the one ANC follows, for a clock
// slot of format shot. The JSON form is how a configuration stores it.
type VenueMapping struct {
	Home      string `json:"home,omitempty"`       // home team's short name, a text slot
	Away      string `json:"away,omitempty"`       // visitor's short name, a text slot
	Score     string `json:"score,omitempty"`      // ScoreFormat with the home and visitor scores, a text slot
	Period    string `json:"period,omitempty"`     // the period name as sent ("1st", "OVT1"), a text slot
	Clock     string `json:"clock,omitempty"`      // the game clock, a clock slot
	ShotClock string `json:"shot_clock,omitempty"` // the shot clock, a clock slot of format shot

	ScoreFormat string `json:"score_format,omitempty"` // default "%d - %d"
	// ClockToleranceMs: a clock sample within this of what the running clock already predicts does not make a new
	// event; the compositor keeps extrapolating. Start, stop and a larger drift do. Default 50.
	ClockToleranceMs int64 `json:"clock_tolerance_ms,omitempty"`
}

// DefaultVenueMapping uses the slot names home, away, score, period, clock and shot.
func DefaultVenueMapping() VenueMapping {
	return VenueMapping{Home: "home", Away: "away", Score: "score", Period: "period", Clock: "clock",
		ShotClock: "shot", ScoreFormat: "%d - %d", ClockToleranceMs: 50}
}

// ForLayout returns m with every slot the layout does not have left out. An event may only name slots of the
// layout it is drawn with, so this is what makes a mapping that covers more than a layout draws usable with it:
// the default mapping with a layout of only a score and a clock, for example. Only the slot's name is looked at.
// A slot of the wrong type, such as the game clock mapped to a text slot, stays in and is rejected when the
// event is validated. A nil layout leaves m unchanged.
func (m VenueMapping) ForLayout(layout *goavpipe.OverlayParams) VenueMapping {
	if layout == nil {
		return m
	}
	for _, name := range []*string{&m.Home, &m.Away, &m.Score, &m.Period, &m.Clock, &m.ShotClock} {
		if *name != "" && layout.Slot(*name) == nil {
			*name = ""
		}
	}
	return m
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
		if c, ok := ShotClock(st); ok {
			out[m.ShotClock] = goavpipe.SlotValue{Clock: &c}
		} else {
			out[m.ShotClock] = goavpipe.HideValue()
		}
	}
	return out
}

// ClockSlots lists the slots whose value is a clock state, for the tolerance rule.
func (m VenueMapping) ClockSlots() []string {
	var slots []string
	for _, name := range []string{m.Clock, m.ShotClock} {
		if name != "" {
			slots = append(slots, name)
		}
	}
	return slots
}

func (m VenueMapping) ShotClockSlot() string { return m.ShotClock }
