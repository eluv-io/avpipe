package stamp

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/anc"
	"github.com/eluv-io/common-go/media/anc/venueclock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// venueMessage frames one venue clock message, CRC included. It is anctest.VenueMessage again: anctest links
// libavformat, and this package's tests stay free of cgo.
func venueMessage(seq int, msgID string, fields ...string) []byte {
	body := "4|6|" + msgID + "|" + fmt.Sprint(seq) + "|"
	for _, f := range fields {
		body += f + "|"
	}
	return append(append([]byte{0x01}, []byte(fmt.Sprintf("%s%02X", body, venueclock.CRC([]byte(body))))...), 0x04)
}

// ancFrame is the ST 2038 payload of one frame: a time code packet and the clock feed's packet carrying serial.
func ancFrame(serial ...byte) []byte {
	return anc.EncodePES([]anc.Packet{
		{Line: 9, DID: 0x60, SDID: 0x60, UDW: anc.Timecode{Hours: 1}.UDW()},
		{Line: 14, DID: 0x50, SDID: 0x01, UDW: append(append([]byte{}, venueclock.Header...), serial...)},
	})
}

// feeder feeds frames at 59.94 fps in 90 kHz, starting at pts0.
type feeder struct {
	a   *ANC
	pts int64
	n   int
}

func (f *feeder) frame(payload []byte) int64 {
	pts := f.pts + int64(f.n)*1001*90/60 // 1501.5 ticks per frame, floored per frame
	f.n++
	f.a.Feed(&goavpipe.AncPacket{Pts: pts, TimeBaseNum: 1, TimeBaseDen: 90000, Payload: payload})
	return pts
}

func TestANCStamper(t *testing.T) {
	log := NewLog()
	a := NewANC(log, 1000, DefaultVenueMapping())
	assert.Equal(t, []string{goavpipe.KindOverlay}, log.Kinds(), "the kind is known before any event")
	f := &feeder{a: a, pts: 1800000000}
	src := log.Source(0)

	// Frames with no serial bytes seal the log without events
	p := f.frame(ancFrame())
	_, status, err := src.At(goavpipe.KindOverlay, p*1000/90000)
	require.NoError(t, err)
	assert.Equal(t, goavpipe.StatusReady, status)
	_, status, _ = src.At(goavpipe.KindOverlay, p*1000/90000+17)
	assert.Equal(t, goavpipe.StatusNotYet, status, "the next frame's data is not in yet")
	assert.Empty(t, log.Events(goavpipe.KindOverlay))

	// Teams, period, score and a held clock: one event each, at the frame that completed the message
	seq := 100
	next := func(id string, fields ...string) []byte { seq++; return venueMessage(seq, id, fields...) }
	p = f.frame(ancFrame(next("17", "Home", "Visitor", "HOM", "VIS")...))
	events := log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 1)
	ev := events[0].(goavpipe.OverlayEvent)
	assert.Equal(t, p*1000/90000, ev.Pts)
	assert.Equal(t, "HOM", *ev.Slots["home"].Text)
	assert.Equal(t, "VIS", *ev.Slots["away"].Text)
	assert.Equal(t, "0 - 0", *ev.Slots["score"].Text)
	assert.True(t, ev.Slots["period"].Hide, "no period yet")
	assert.True(t, ev.Slots["clock"].Hide, "no clock yet")
	assert.True(t, ev.Slots["shot"].Hide)

	f.frame(ancFrame(next("4", "3", "1", "1st")...))
	f.frame(ancFrame(next("30", "12:00", "", "", "0", "0", "1")...))
	f.frame(ancFrame(next("31", "24", "0", "0", "0")...))
	events = log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 4)
	ev = events[3].(goavpipe.OverlayEvent)
	assert.Equal(t, "1st", *ev.Slots["period"].Text)
	assert.Equal(t, goavpipe.ClockState{Ms: 720000, Running: false}, *ev.Slots["clock"].Clock)
	assert.Equal(t, goavpipe.ClockState{Ms: 24000}, *ev.Slots["shot"].Clock)

	// A message split over two frames lands on the frame that completed it
	msg := next("1", "2", "0")
	f.frame(ancFrame(msg[:6]...))
	assert.Len(t, log.Events(goavpipe.KindOverlay), 4)
	p = f.frame(ancFrame(msg[6:]...))
	events = log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 5)
	ev = events[4].(goavpipe.OverlayEvent)
	assert.Equal(t, p*1000/90000, ev.Pts)
	assert.Equal(t, "2 - 0", *ev.Slots["score"].Text)

	// The clock starts: an event. Then samples every 1.001 s that match the running clock make none.
	p = f.frame(ancFrame(next("30", "12:00", "", "", "0", "0", "0")...))
	events = log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 6)
	start := events[5].(goavpipe.OverlayEvent)
	assert.Equal(t, goavpipe.ClockState{Ms: 720000, Running: true}, *start.Slots["clock"].Clock)
	for i := 1; i <= 5; i++ {
		for k := 0; k < 59; k++ {
			f.frame(ancFrame())
		}
		f.frame(ancFrame(next("30", fmt.Sprintf("11:%02d", 60-i), "", "", "0", "0", "0")...))
	}
	assert.Len(t, log.Events(goavpipe.KindOverlay), 6, "samples within tolerance of the running clock make no event")

	// A score change while the clock runs: the new snapshot carries the predicted clock, not a nudged one
	p = f.frame(ancFrame(next("1", "4", "0")...))
	events = log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 7)
	ev = events[6].(goavpipe.OverlayEvent)
	assert.Equal(t, "4 - 0", *ev.Slots["score"].Text)
	predicted := carryClock(*start.Slots["clock"].Clock, ev.Pts-start.Pts)
	assert.Equal(t, predicted, *ev.Slots["clock"].Clock)
	assert.True(t, ev.Slots["clock"].Clock.Running)

	// A score message half a second after the last clock sample: the snapshot carries the clock forward from the
	// sample's frame, so the stamped value is the prediction, not the stale sample
	for k := 0; k < 30; k++ {
		f.frame(ancFrame())
	}
	p = f.frame(ancFrame(next("1", "5", "0")...))
	events = log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 8)
	ev = events[7].(goavpipe.OverlayEvent)
	assert.Equal(t, "5 - 0", *ev.Slots["score"].Text)
	predicted = carryClock(*start.Slots["clock"].Clock, ev.Pts-start.Pts)
	assert.Equal(t, predicted, *ev.Slots["clock"].Clock, "carried from the sample's frame, not stamped as current")
	assert.Less(t, predicted.Ms, int64(715000), "the carry moved it by the half second")

	// A sample well off the prediction is a correction; a stop is an event
	f.frame(ancFrame(next("30", "11:50", "", "", "0", "0", "0")...))
	events = log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 9)
	assert.Equal(t, int64(710000), events[8].(goavpipe.OverlayEvent).Slots["clock"].Clock.Ms)
	f.frame(ancFrame(next("30", "11:50", "", "", "0", "0", "1")...))
	events = log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 10)
	assert.False(t, events[9].(goavpipe.OverlayEvent).Slots["clock"].Clock.Running)

	// Repeats change nothing
	f.frame(ancFrame(next("17", "Home", "Visitor", "HOM", "VIS")...))
	f.frame(ancFrame(next("31", "24", "0", "0", "0")...))
	assert.Len(t, log.Events(goavpipe.KindOverlay), 10)

	// A bad CRC is counted and ignored; a dropped-packet count is carried
	bad := next("1", "9", "9")
	bad[len(bad)-2] ^= 0x01
	f.frame(ancFrame(bad...))
	a.Feed(&goavpipe.AncPacket{Pts: f.pts + 10*1501, TimeBaseNum: 1, TimeBaseDen: 90000, Payload: ancFrame(), Dropped: 3})
	assert.Len(t, log.Events(goavpipe.KindOverlay), 10)
	assert.Equal(t, 1, a.Stats.BadCRC)
	assert.Equal(t, 3, a.Stats.Dropped)
	assert.Equal(t, 10, a.Stats.Events)
	assert.Equal(t, 0, a.Stats.Errors)
	assert.Equal(t, "5 - 0", fmt.Sprintf("%d - %d", a.State().Score[0], a.State().Score[1]))
	assert.Equal(t, int64(10), a.Last().Seq)

	// Every event validates against a layout with these slots
	layout := &goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{
		{Name: "home", Type: goavpipe.OverlaySlotText}, {Name: "away", Type: goavpipe.OverlaySlotText},
		{Name: "score", Type: goavpipe.OverlaySlotText}, {Name: "period", Type: goavpipe.OverlaySlotText},
		{Name: "clock", Type: goavpipe.OverlaySlotClock}, {Name: "shot", Type: goavpipe.OverlaySlotClock}}}
	for _, e := range log.Events(goavpipe.KindOverlay) {
		oe := e.(goavpipe.OverlayEvent)
		assert.NoError(t, layout.ValidateEvent(&oe))
	}
}

// TestANCStamperShotClock: the shot clock is followed from its readings, since the venue's running flag stays on
// while the clock sits. A reading is the time left cut off, so a step down means just under the next step up.
// Readings that follow the running clock make no event, in whole seconds, across the change to tenths at five
// seconds, and in tenths.
func TestANCStamperShotClock(t *testing.T) {
	log := NewLog()
	a := NewANC(log, 1000, DefaultVenueMapping())
	f := &feeder{a: a, pts: 1800000000}
	seq := 0
	next := func(id string, fields ...string) []byte { seq++; return venueMessage(seq, id, fields...) }
	latest := func() goavpipe.OverlayEvent { return log.Latest(goavpipe.KindOverlay).(goavpipe.OverlayEvent) }
	shot := func() goavpipe.ClockState { return *latest().Slots["shot"].Clock }
	count := func() int { return len(log.Events(goavpipe.KindOverlay)) }
	idle := func(frames int) {
		for i := 0; i < frames; i++ {
			f.frame(ancFrame())
		}
	}
	// shotAt is the shot clock of the event in effect, carried to the frame at pts (90 kHz)
	shotAt := func(pts int64) goavpipe.ClockState {
		ev := latest()
		return carryClock(*ev.Slots["shot"].Clock, pts*1000/90000-ev.Pts)
	}

	// The game clock runs; the shot clock is set to 24 and sits there with the running flag on
	f.frame(ancFrame(next("30", "12:00", "", "", "0", "0", "1")...))
	f.frame(ancFrame(next("30", "12:00", "", "", "0", "0", "0")...))
	f.frame(ancFrame(next("31", "24", "0", "0", "1")...))
	assert.Equal(t, goavpipe.ClockState{Ms: 24000}, shot(), "set: held at the value")
	idle(120)
	f.frame(ancFrame(next("31", "24", "0", "0", "1")...))
	n := count()
	assert.Equal(t, goavpipe.ClockState{Ms: 24000}, shot(), "a repeat two seconds on: still held")

	// It starts: the first step down is the start, with just under 24 s left
	p := f.frame(ancFrame(next("31", "23", "0", "0", "1")...))
	require.Equal(t, n+1, count())
	assert.Equal(t, goavpipe.ClockState{Ms: 23999, Running: true}, shot())
	assert.Equal(t, p*1000/90000, latest().Pts)

	// Whole seconds down to 5, a second apart: no event
	n = count()
	for s := 22; s >= 5; s-- {
		idle(59)
		f.frame(ancFrame(next("31", fmt.Sprint(s), "0", "0", "1")...))
	}
	assert.Equal(t, n, count(), "readings that follow the running clock make no event")
	// 5 stays up for a second, then tenths: 4.9 a second after 5, then every tenth
	idle(59)
	p = f.frame(ancFrame(next("31", "4.9", "0", "0", "1")...))
	assert.Equal(t, n, count(), "the change to tenths follows the running clock")
	assert.InDelta(t, 4999, shotAt(p).Ms, 50)
	for d := 48; d >= 30; d-- {
		idle(5)
		f.frame(ancFrame(next("31", fmt.Sprintf("%d.%d", d/10, d%10), "0", "0", "1")...))
	}
	assert.Equal(t, n, count(), "tenths follow the running clock")

	// A reset: held at 24 again, then started by its first step down
	f.frame(ancFrame(next("31", "24", "0", "0", "1")...))
	require.Equal(t, n+1, count())
	assert.Equal(t, goavpipe.ClockState{Ms: 24000}, shot())
	idle(40)
	f.frame(ancFrame(next("31", "23", "0", "0", "1")...))
	idle(59)
	f.frame(ancFrame(next("31", "22", "0", "0", "1")...))
	require.Equal(t, n+2, count())
	assert.True(t, shot().Running)

	// The game clock stops half a second after a reading: the shot clock stops with it, still reading 22
	idle(29)
	p = f.frame(ancFrame(next("30", "11:30", "", "", "0", "0", "1")...))
	require.Equal(t, n+3, count())
	assert.False(t, shot().Running)
	assert.InDelta(t, 22500, shot().Ms, 50)
	assert.Equal(t, p*1000/90000, latest().Pts)
	// Play goes on: the game clock starts, the shot clock with its next step down
	idle(120)
	f.frame(ancFrame(next("30", "11:30", "", "", "0", "0", "0")...))
	assert.False(t, shot().Running, "held until a reading moves")
	idle(29)
	f.frame(ancFrame(next("31", "21", "0", "0", "1")...))
	assert.Equal(t, goavpipe.ClockState{Ms: 21999, Running: true}, shot())

	// The readings stop coming with the game clock running: stopped on the reading it shows, a quarter second late
	n = count()
	idle(74)
	assert.Equal(t, n, count(), "not overdue yet")
	idle(2)
	require.Equal(t, n+1, count())
	assert.Equal(t, goavpipe.ClockState{Ms: 21000}, shot())

	// The operator sets it lower: not a step and not the time that passed, so held there
	idle(60)
	f.frame(ancFrame(next("31", "14", "0", "0", "1")...))
	assert.Equal(t, goavpipe.ClockState{Ms: 14000}, shot())
	// A step down while the game clock is stopped is a new held value
	f.frame(ancFrame(next("30", "11:28", "", "", "0", "0", "1")...))
	f.frame(ancFrame(next("31", "13", "0", "0", "1")...))
	assert.Equal(t, goavpipe.ClockState{Ms: 13000}, shot())
	// Blank: hidden
	f.frame(ancFrame(next("31", "", "0", "0", "1")...))
	assert.True(t, latest().Slots["shot"].Hide)
	assert.Equal(t, 0, a.Stats.Errors)
}

// TestANCStamperBreakClock: in a break the venue sends the game clock as 12:00 with the stripe off while the break
// clock runs; the game clock must not start. And a clock whose samples do not move is held whatever the flags
// say, until a sample moves again.
func TestANCStamperBreakClock(t *testing.T) {
	log := NewLog()
	a := NewANC(log, 1000, DefaultVenueMapping())
	f := &feeder{a: a, pts: 1800000000}
	seq := 0
	next := func(id string, fields ...string) []byte { seq++; return venueMessage(seq, id, fields...) }
	clockOf := func() goavpipe.ClockState {
		ev := log.Latest(goavpipe.KindOverlay).(goavpipe.OverlayEvent)
		return *ev.Slots["clock"].Clock
	}

	// Intermission: break clock in the timeout field, stripe off
	f.frame(ancFrame(next("30", "12:00", "", "1:30", "0", "0", "0")...))
	assert.Equal(t, goavpipe.ClockState{Ms: 720000, Running: false}, clockOf(), "a break clock showing: held")
	// The break clock is gone, stripe still off, but 12:00 again: the sample did not move, so still held
	for i := 0; i < 60; i++ {
		f.frame(ancFrame())
	}
	f.frame(ancFrame(next("30", "12:00", "", "", "0", "0", "0")...))
	assert.Equal(t, goavpipe.ClockState{Ms: 720000, Running: false}, clockOf(), "repeated sample: held")
	// A score message meanwhile: the snapshot keeps the clock held
	f.frame(ancFrame(next("1", "0", "0")...))
	f.frame(ancFrame(next("17", "Home", "Visitor", "HOM", "VIS")...))
	assert.False(t, clockOf().Running, "held across other messages")
	// The stripe goes on, then off again: the operator started the clock, running from the start message
	for i := 0; i < 60; i++ {
		f.frame(ancFrame())
	}
	f.frame(ancFrame(next("30", "12:00", "", "", "0", "0", "1")...))
	assert.False(t, clockOf().Running)
	for i := 0; i < 60; i++ {
		f.frame(ancFrame())
	}
	f.frame(ancFrame(next("30", "12:00", "", "", "0", "0", "0")...))
	assert.Equal(t, goavpipe.ClockState{Ms: 720000, Running: true}, clockOf(), "stripe off after on: running at once")
	events := len(log.Events(goavpipe.KindOverlay))
	// The first moving sample agrees with the prediction: no event
	for i := 0; i < 60; i++ {
		f.frame(ancFrame())
	}
	f.frame(ancFrame(next("30", "11:59", "", "", "0", "0", "0")...))
	assert.Equal(t, events, len(log.Events(goavpipe.KindOverlay)), "within tolerance")
	// Running samples within tolerance make no event
	for i := 0; i < 60; i++ {
		f.frame(ancFrame())
	}
	f.frame(ancFrame(next("30", "11:58", "", "", "0", "0", "0")...))
	assert.Equal(t, events, len(log.Events(goavpipe.KindOverlay)))
	// A timeout: game clock held with the timeout clock showing, then the stripe
	for i := 0; i < 60; i++ {
		f.frame(ancFrame())
	}
	f.frame(ancFrame(next("30", "11:57", "", "0:45", "0", "0", "1")...))
	assert.False(t, clockOf().Running)
}

func TestVenueMappingPartial(t *testing.T) {
	m := VenueMapping{Score: "s", Clock: "c", ScoreFormat: "%d:%d"}
	st := venueclock.State{Score: [2]int{3, 7}, Clock: venueclock.Value{Text: "0.5", Ms: 500, Valid: true}, Stopped: true}
	slots := m.Slots(&st)
	assert.Len(t, slots, 2)
	assert.Equal(t, "3:7", *slots["s"].Text)
	assert.Equal(t, goavpipe.ClockState{Ms: 500}, *slots["c"].Clock)
}

// TestVenueMappingForLayout: a mapping fitted to a layout names only that layout's slots, so every event it makes
// passes the layout's own validation.
func TestVenueMappingForLayout(t *testing.T) {
	layout := &goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{
		{Name: "score", Type: goavpipe.OverlaySlotText},
		{Name: "clock", Type: goavpipe.OverlaySlotClock},
	}}
	m := DefaultVenueMapping().ForLayout(layout)
	assert.Equal(t, VenueMapping{Score: "score", Clock: "clock", ScoreFormat: "%d - %d", ClockToleranceMs: 50}, m)
	assert.Equal(t, DefaultVenueMapping(), DefaultVenueMapping().ForLayout(nil), "no layout, nothing to leave out")

	st := venueclock.State{PeriodName: "1st", Score: [2]int{3, 7}, Clock: venueclock.Value{Ms: 500, Valid: true}}
	st.Abbrevs[venueclock.Home], st.Abbrevs[venueclock.Visitor] = "HOM", "VIS"
	fitted := &goavpipe.OverlayEvent{TimedEvent: goavpipe.TimedEvent{Kind: goavpipe.KindOverlay}, Slots: m.Slots(&st)}
	assert.Len(t, fitted.Slots, 2)
	require.NoError(t, layout.ValidateEvent(fitted))

	// the default mapping as it is names slots this layout lacks, which the layout rejects
	all := &goavpipe.OverlayEvent{TimedEvent: goavpipe.TimedEvent{Kind: goavpipe.KindOverlay},
		Slots: DefaultVenueMapping().Slots(&st)}
	assert.Error(t, layout.ValidateEvent(all))

	// a slot of the wrong type stays in: the fit goes by name only
	wrong := VenueMapping{Clock: "score"}.ForLayout(layout)
	assert.Equal(t, "score", wrong.Clock)
}

// TestVenueMappingJSON: the form a configuration stores.
func TestVenueMappingJSON(t *testing.T) {
	var m VenueMapping
	require.NoError(t, json.Unmarshal([]byte(
		`{"home":"h","away":"a","score":"s","period":"p","clock":"c","shot_clock":"sc","score_format":"%d:%d","clock_tolerance_ms":80}`), &m))
	assert.Equal(t, VenueMapping{Home: "h", Away: "a", Score: "s", Period: "p", Clock: "c", ShotClock: "sc",
		ScoreFormat: "%d:%d", ClockToleranceMs: 80}, m)
	b, err := json.Marshal(VenueMapping{Score: "s"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"score":"s"}`, string(b), "a slot not named is left out")
}
