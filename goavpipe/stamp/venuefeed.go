package stamp

import (
	"strings"
	"sync"

	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/eluv-io/avpipe/broadcastproto/anc/venueclock"
	"github.com/eluv-io/avpipe/goavpipe"
)

// venueKeys are the keys VenueFeed publishes, described at VenueFeed.
var venueKeys = map[string]KeyKind{
	"home": KeyString, "away": KeyString, // short names
	"home_name": KeyString, "away_name": KeyString,
	"score_home": KeyNumber, "score_away": KeyNumber,
	"period":        KeyString, // the period's name
	"period_type":   KeyString, // pregame, break, play or overtime
	"period_number": KeyNumber,
	"poss":          KeyString, // home, away or both
	"fouls_home":    KeyNumber, "fouls_away": KeyNumber,
	"bonus_home": KeyNumber, "bonus_away": KeyNumber,
	"to_home": KeyNumber, "to_away": KeyNumber, // full timeouts left
	"to_short_home": KeyNumber, "to_short_away": KeyNumber,
	"timeout": KeyString, // home or away while that team's timeout runs
	"horn":    KeyBool, "shot_horn": KeyBool, "shot_expired": KeyBool, "period_end": KeyBool,
	"time_of_day": KeyString,
	"clock":       KeyTimer, "shot": KeyTimer, "break_clock": KeyTimer, "timeout_clock": KeyTimer,
}

var venuePeriodTypes = map[int]string{
	venueclock.PeriodPreGame: "pregame", venueclock.PeriodPause: "break",
	venueclock.PeriodPlaying: "play", venueclock.PeriodOvertime: "overtime",
}

// VenueFeed is the Feed of the venue clock feed (package venueclock): the messages carried as private user data
// in the stream's ancillary data become keys, and the game clock and the shot clock are followed from their
// samples, since the venue's flags alone do not say whether they run. The keys:
//
//	home, away                      string  team short names
//	home_name, away_name            string  team names
//	score_home, score_away          number
//	period                          string  the period's name as sent ("1st", "OVT1")
//	period_type                     string  pregame, break, play or overtime
//	period_number                   number
//	poss                            string  home, away or both
//	fouls_home, fouls_away          number  team fouls this period
//	bonus_home, bonus_away          number  0, 1 or 2
//	to_home, to_away                number  full timeouts left
//	to_short_home, to_short_away    number  short timeouts left
//	timeout                         string  home or away while that team's timeout runs
//	horn, shot_horn                 bool    the game-time and shot clock horns
//	shot_expired, period_end        bool    the shot clock has expired; the end of the period
//	time_of_day                     string  hh:mm:ss
//	clock                           timer   the game clock: held while a break or timeout clock shows, and while
//	                                        its samples stand still whatever the venue's flags say
//	shot                            timer   the shot clock, running or held as its readings show
//	break_clock, timeout_clock      timer   the break and timeout countdowns, each held at its last reading
//
// A string is unset while the venue sends it empty, a bool while the venue does not signal it, and a timer while
// the venue shows no such clock. The feed's empty number is 0, so a number is published once the teams are known
// (a short name is set), or as soon as it or the other number of its pair is not 0.
type VenueFeed struct {
	mu     sync.Mutex // guards state for State
	stream venueclock.Stream
	state  venueclock.State

	timescale int64
	sample    clockSample // the previous game clock sample, to tell a held clock from a running one
	held      bool        // the game clock is held although the flags say it runs
	gameRan   bool        // the game clock ran as of the last message, to see it stop
	shot      shotTrack   // the shot clock as followed from its readings
}

// shotTrack follows the shot clock. The venue shows it cut off, not rounded up (5.9 s left reads 5, 4.99 s reads
// 4.9), and sends a reading when it changes. Its running flag stays on while the clock sits at 24, so whether
// the clock runs is told from the readings.
type shotTrack struct {
	valid     bool                // the venue shows a shot clock
	c         goavpipe.ClockState // the time left as of pts
	pts       int64               // overlay timescale
	reading   int64               // the last reading, ms
	unit      int64               // step of the last reading: 1000 in whole seconds, 100 in tenths
	changedAt int64               // pts at which the reading last changed
}

// shotOverdueMs: a running shot clock whose next reading is this late has stopped.
const shotOverdueMs = 250

// clockSample is one game clock value as the venue sent it.
type clockSample struct {
	ms      int64
	pts     int64 // overlay timescale
	valid   bool
	stopped bool // the "time stopped" stripe was on
}

// NewVenueFeed makes the feed of one stream. It keeps the stream's state: a new recording period or a new
// input gets a new one.
func NewVenueFeed() *VenueFeed { return &VenueFeed{} }

// KeyKinds lists the keys described at VenueFeed with their kinds.
func (f *VenueFeed) KeyKinds() map[string]KeyKind { return venueKeys }

// DefaultBindings are the six slots a venue board has always had: home, away and period show the keys of their
// names, score the two scores as "3 - 2", and clock and shot the two clocks.
func (f *VenueFeed) DefaultBindings() map[string]goavpipe.OverlayBinding {
	return DefaultVenueMapping().Bindings()
}

// State returns a copy of the venue state as last decoded. Safe from any goroutine.
func (f *VenueFeed) State() venueclock.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Decode reassembles the feed's messages from the private user data packets among pkts, applies each to the
// venue state and publishes the keys. A message is at the pts of the PES that completed it. A message whose
// checksum fails is counted as damaged and left out. See Feed.
func (f *VenueFeed) Decode(pkts []anc.Packet, srcPts int64, c *FeedContext) (messages, damaged int, err error) {
	f.timescale = c.Timescale
	msgs, err := f.stream.FeedPackets(pkts, srcPts)
	for _, m := range msgs {
		messages++
		if !m.ChecksumOK {
			damaged++
			continue
		}
		f.mu.Lock()
		changed := f.state.Apply(m)
		f.mu.Unlock()
		pts := c.Pts(m.Pts)
		switch m.MessageID {
		case venueclock.MsgGameTime:
			changed = f.noteSample(pts) || changed
		case venueclock.MsgShotClock:
			changed = f.noteShot(pts) || changed
		}
		changed = f.followGame(pts) || changed
		if changed {
			f.publish(c.Keys, pts)
			c.Emit(pts)
		}
	}
	if pts := c.Pts(srcPts); f.shotOverdue(pts) {
		f.publish(c.Keys, pts)
		c.Emit(pts)
	}
	return messages, damaged, err
}

// publish sets the keys from the state as of pts.
func (f *VenueFeed) publish(k Keys, pts int64) {
	st := &f.state
	str := func(name, v string) {
		if v == "" {
			delete(k, name)
		} else {
			k[name] = v
		}
	}
	home, away := st.Abbrevs[venueclock.Home], st.Abbrevs[venueclock.Visitor]
	str("home", home)
	str("away", away)
	str("home_name", st.Teams[venueclock.Home])
	str("away_name", st.Teams[venueclock.Visitor])
	str("period", st.PeriodName)
	str("period_type", venuePeriodTypes[st.PeriodType])
	str("time_of_day", st.DayTime)
	switch st.Possession {
	case venueclock.PossessionHome:
		k["poss"] = "home"
	case venueclock.PossessionVisitor:
		k["poss"] = "away"
	case venueclock.PossessionBoth:
		k["poss"] = "both"
	default:
		delete(k, "poss")
	}
	switch st.Timeouts.Running {
	case "A":
		k["timeout"] = "home"
	case "B":
		k["timeout"] = "away"
	default:
		delete(k, "timeout")
	}

	// The feed's empty number is 0, so a 0 says nothing before the teams are known
	known := home != "" || away != ""
	nums := func(names string, v ...int) {
		set := known
		for _, n := range v {
			set = set || n != 0
		}
		for i, name := range strings.Fields(names) {
			if set {
				k[name] = v[i]
			} else {
				delete(k, name)
			}
		}
	}
	nums("score_home score_away", st.Score[venueclock.Home], st.Score[venueclock.Visitor])
	nums("fouls_home fouls_away", st.Fouls[venueclock.Home], st.Fouls[venueclock.Visitor])
	nums("bonus_home bonus_away", st.Bonus[venueclock.Home], st.Bonus[venueclock.Visitor])
	nums("to_home to_away", st.Timeouts.FullLeft[venueclock.Home], st.Timeouts.FullLeft[venueclock.Visitor])
	nums("to_short_home to_short_away", st.Timeouts.ShortLeft[venueclock.Home], st.Timeouts.ShortLeft[venueclock.Visitor])
	nums("period_number", st.PeriodNumber)

	flag := func(name string, on bool) {
		if on {
			k[name] = true
		} else {
			delete(k, name)
		}
	}
	flag("horn", st.Horn)
	flag("shot_horn", st.ShotHorn)
	flag("shot_expired", st.ShotExpired)
	flag("period_end", st.EndOfQuarter)

	if g, ok := GameClock(st); ok {
		g.Running = g.Running && !f.held
		k.setTimer("clock", g, pts)
	} else {
		delete(k, "clock")
	}
	if f.shot.valid {
		k["shot"] = Timer{ClockState: f.shot.c, Pts: f.shot.pts}
	} else {
		delete(k, "shot")
	}
	reading := func(name string, v venueclock.Value) {
		if v.Valid {
			k.setTimer(name, goavpipe.ClockState{Ms: int64(v.Ms)}, pts)
		} else {
			delete(k, name)
		}
	}
	reading("break_clock", st.BreakTime)
	reading("timeout_clock", st.TimeoutTime)
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

// gameRunning is whether the game clock runs as the feed follows it; true when the venue shows no game clock.
func (f *VenueFeed) gameRunning() bool {
	g, ok := GameClock(&f.state)
	return !ok || (g.Running && !f.held)
}

// noteShot takes the shot clock reading of a message at pts. A reading one step below the last, or lower by the
// time that passed, is the clock running: the time left is then just under the next step up, since the venue cuts
// the value off. A higher reading or any other jump is the operator setting the clock, and it holds there until it
// steps down. With the game clock stopped a step down is taken as a new held value. A repeat says nothing.
// Returns whether the followed clock changed.
func (f *VenueFeed) noteShot(pts int64) bool {
	v, prev := f.state.ShotClock, f.shot
	if !v.Valid {
		f.shot = shotTrack{}
		return prev.valid
	}
	reading, unit := int64(v.Ms), int64(1000)
	if strings.Contains(v.Text, ".") {
		unit = 100
	}
	if prev.valid && reading == prev.reading {
		return false
	}
	t := shotTrack{valid: true, pts: pts, reading: reading, unit: unit, changedAt: pts,
		c: goavpipe.ClockState{Ms: reading}}
	step := prev.reading - reading
	elapsed := (pts - prev.changedAt) * 1000 / f.timescale
	if prev.valid && step > 0 && (step <= prev.unit || abs64(step-elapsed) <= prev.unit) && f.gameRunning() {
		t.c = goavpipe.ClockState{Ms: reading + unit - 1, Running: true}
	}
	f.shot = t
	return true
}

// followGame stops the followed shot clock when the game clock stops. The shot clock does not run while the game
// time is held, and the game clock's stop is in the feed at once, while a stopped shot clock just sends no more
// readings. Returns whether the shot clock changed.
func (f *VenueFeed) followGame(pts int64) bool {
	g, ok := GameClock(&f.state)
	running := ok && g.Running && !f.held
	stopped := f.gameRan && !running
	f.gameRan = running
	if !stopped || !f.shot.c.Running {
		return false
	}
	f.stopShot(pts, carryClock(f.shot.c, (pts-f.shot.pts)*1000/f.timescale).Ms)
	return true
}

// shotOverdue stops the followed shot clock when its next reading is overdue: a running clock reads differently
// every step, so it stopped on the reading it shows. Returns whether it did.
func (f *VenueFeed) shotOverdue(pts int64) bool {
	if !f.shot.c.Running || (pts-f.shot.changedAt)*1000/f.timescale <= f.shot.unit+shotOverdueMs {
		return false
	}
	f.stopShot(pts, f.shot.reading)
	return true
}

// stopShot holds the followed shot clock at ms from pts on.
func (f *VenueFeed) stopShot(pts, ms int64) {
	f.shot.c = goavpipe.ClockState{Ms: ms}
	f.shot.pts = pts
}

// noteSample records the game clock sample of a message at pts and keeps held up to date: the clock is held when
// a sample repeats the previous value although time passed, since a running clock reads differently every second,
// and runs again when a sample moves or the "time stopped" stripe goes from on to off, which is the operator
// starting it. The venue's flags alone do not say: a break shows 12:00 with the stripe off. Returns whether held
// changed.
func (f *VenueFeed) noteSample(pts int64) bool {
	prev := f.sample
	f.sample = clockSample{ms: int64(f.state.Clock.Ms), pts: pts, valid: f.state.Clock.Valid, stopped: f.state.Stopped}
	was := f.held
	switch {
	case !prev.valid || !f.sample.valid:
		f.held = false
	case prev.stopped && !f.sample.stopped:
		f.held = false
	case f.sample.ms != prev.ms:
		f.held = false
	case (pts-prev.pts)*1000/f.timescale >= 500:
		f.held = true
	}
	return f.held != was
}
