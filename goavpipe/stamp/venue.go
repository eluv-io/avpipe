package stamp

import (
	"strings"

	"github.com/eluv-io/avpipe/goavpipe"
)

// VenueMapping is the short form of the venue board's six bindings, as a configuration stored them before a
// layout's slots carried their own (goavpipe.OverlaySlot.From): it names the slot that shows each value. A slot
// name left empty is not shown. Bindings gives the bindings it stands for.
type VenueMapping struct {
	Home      string `json:"home,omitempty"`       // home team's short name, a text slot
	Away      string `json:"away,omitempty"`       // visitor's short name, a text slot
	Score     string `json:"score,omitempty"`      // ScoreFormat with the home and visitor scores, a text slot
	Period    string `json:"period,omitempty"`     // the period name as sent ("1st", "OVT1"), a text slot
	Clock     string `json:"clock,omitempty"`      // the game clock, a clock slot
	ShotClock string `json:"shot_clock,omitempty"` // the shot clock, a clock slot of format shot

	ScoreFormat string `json:"score_format,omitempty"` // default "%d - %d"
	// ClockToleranceMs: a clock sample within this of what the running clock already predicts does not make a new
	// event; the compositor keeps extrapolating. Start, stop and a larger drift do. Default 50. It is the
	// Bindings.ToleranceMs of the stamper the mapping is used with.
	ClockToleranceMs int64 `json:"clock_tolerance_ms,omitempty"`
}

// DefaultVenueMapping uses the slot names home, away, score, period, clock and shot.
func DefaultVenueMapping() VenueMapping {
	return VenueMapping{Home: "home", Away: "away", Score: "score", Period: "period", Clock: "clock",
		ShotClock: "shot", ScoreFormat: "%d - %d", ClockToleranceMs: 50}
}

// Bindings is the mapping as bindings by slot name, for NewBindings: the keys home, away and period as text, the
// score as a Text binding made from ScoreFormat (its first %d is the home score, the second the visitor's), and
// the timers clock and shot.
func (m VenueMapping) Bindings() map[string]goavpipe.OverlayBinding {
	out := map[string]goavpipe.OverlayBinding{}
	bind := func(slot string, b goavpipe.OverlayBinding) {
		if slot != "" {
			out[slot] = b
		}
	}
	bind(m.Home, goavpipe.OverlayBinding{Key: "home"})
	bind(m.Away, goavpipe.OverlayBinding{Key: "away"})
	bind(m.Period, goavpipe.OverlayBinding{Key: "period"})
	bind(m.Score, goavpipe.OverlayBinding{Text: scoreText(m.ScoreFormat)})
	bind(m.Clock, goavpipe.OverlayBinding{Timer: "clock"})
	bind(m.ShotClock, goavpipe.OverlayBinding{Timer: "shot"})
	return out
}

// scoreText turns a printf score format into a Text binding, "%d - %d" when empty.
func scoreText(format string) string {
	if format == "" {
		format = "%d - %d"
	}
	format = strings.Replace(format, "%d", "{score_home}", 1)
	return strings.Replace(format, "%d", "{score_away}", 1)
}
