package stamp

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/eluv-io/avpipe/broadcastproto/anc/venueclock"
	"github.com/eluv-io/avpipe/goavpipe"
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

// venueStamper is a stamper of the venue feed drawn with layout; nil is the six default slots. named replaces the
// feed's default bindings.
func venueStamper(t *testing.T, log *Log, layout *goavpipe.OverlayParams, named map[string]goavpipe.OverlayBinding) (*ANC, *VenueFeed) {
	t.Helper()
	feed := NewVenueFeed()
	b, err := NewBindings(feed, layout, named)
	require.NoError(t, err)
	return NewANC(log, 1000, feed, b), feed
}

func TestANCStamper(t *testing.T) {
	log := NewLog()
	a, feed := venueStamper(t, log, nil, nil)
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
	assert.Equal(t, "5 - 0", fmt.Sprintf("%d - %d", feed.State().Score[0], feed.State().Score[1]))
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
	a, _ := venueStamper(t, log, nil, nil)
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
	a, _ := venueStamper(t, log, nil, nil)
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

// TestVenueMappingBindings: the short form names a slot per value; what it stands for are bindings.
func TestVenueMappingBindings(t *testing.T) {
	m := VenueMapping{Score: "s", Clock: "c", ScoreFormat: "%d:%d"}
	assert.Equal(t, map[string]goavpipe.OverlayBinding{
		"s": {Text: "{score_home}:{score_away}"},
		"c": {Timer: "clock"},
	}, m.Bindings())
	assert.Equal(t, "{score_home} - {score_away}", VenueMapping{Score: "s"}.Bindings()["s"].Text, "the default format")
	assert.Len(t, DefaultVenueMapping().Bindings(), 6)

	b, err := NewBindings(NewVenueFeed(), nil, m.Bindings())
	require.NoError(t, err)
	assert.Equal(t, []string{"c", "s"}, b.SlotNames())
	keys := Keys{"score_home": 3, "score_away": 7, "clock": Timer{ClockState: goavpipe.ClockState{Ms: 500}, Pts: 10}}
	slots := b.Slots(keys, 400, 1000)
	assert.Len(t, slots, 2)
	assert.Equal(t, "3:7", *slots["s"].Text)
	assert.Equal(t, goavpipe.ClockState{Ms: 500}, *slots["c"].Clock)
}

// TestBindingsForLayout: only the layout's slots are bound, so every event passes the layout's own validation.
func TestBindingsForLayout(t *testing.T) {
	layout := &goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{
		{Name: "bg", Type: goavpipe.OverlaySlotImage, Default: "bg"},
		{Name: "home", Type: goavpipe.OverlaySlotImage},
		{Name: "score", Type: goavpipe.OverlaySlotText},
		{Name: "clock", Type: goavpipe.OverlaySlotClock},
		{Name: "fouls_home", Type: goavpipe.OverlaySlotText},
	}}
	feed := NewVenueFeed()
	b, err := NewBindings(feed, layout, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"score", "clock", "fouls_home"}, b.SlotNames(),
		"the feed's default for score, the keys named like the slots, and no image slot")
	assert.Equal(t, []string{"clock"}, b.ClockSlots())
	assert.False(t, b.Rendered())

	keys := Keys{"home": "HOM", "score_home": 3, "score_away": 7, "fouls_home": 4,
		"clock": Timer{ClockState: goavpipe.ClockState{Ms: 9000, Running: true}, Pts: 1000}}
	ev := &goavpipe.OverlayEvent{TimedEvent: goavpipe.TimedEvent{Kind: goavpipe.KindOverlay}, Slots: b.Slots(keys, 3000, 1000)}
	require.NoError(t, layout.ValidateEvent(ev))
	assert.Equal(t, "3 - 7", *ev.Slots["score"].Text)
	assert.Equal(t, "4", *ev.Slots["fouls_home"].Text)
	assert.Equal(t, goavpipe.ClockState{Ms: 7000, Running: true}, *ev.Slots["clock"].Clock, "carried from its sample")

	// The short form: its slots only, and those the layout lacks are left out
	short := VenueMapping{Score: "score", Home: "nosuch"}.Bindings()
	b, err = NewBindings(feed, layout, short)
	require.NoError(t, err)
	assert.Equal(t, []string{"score"}, b.SlotNames())

	// A binding that does not fit its slot is refused, by name as well as spelled out
	_, err = NewBindings(feed, layout, VenueMapping{Clock: "score"}.Bindings())
	assert.ErrorContains(t, err, `slot "score"`)
	_, err = NewBindings(feed, &goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{
		{Name: "shot", Type: goavpipe.OverlaySlotText}}}, nil)
	assert.ErrorContains(t, err, "does not fit a text slot", "the shot clock is a timer")
}

// TestBindingForms: each form of a binding, and what is refused.
func TestBindingForms(t *testing.T) {
	arrows := map[string]string{"home": "left", "away": "right"}
	layout := &goavpipe.OverlayParams{
		Images: map[string][]byte{"left": {1}, "right": {1}},
		Slots: []goavpipe.OverlaySlot{
			{Name: "name", Type: goavpipe.OverlaySlotText, From: &goavpipe.OverlayBinding{Key: "home_name"}},
			{Name: "fouls", Type: goavpipe.OverlaySlotText, From: &goavpipe.OverlayBinding{Text: "FOULS {fouls_home}/{fouls_away}"}},
			{Name: "arrow", Type: goavpipe.OverlaySlotImage, From: &goavpipe.OverlayBinding{Key: "poss", Images: arrows}},
			{Name: "who", Type: goavpipe.OverlaySlotImage, From: &goavpipe.OverlayBinding{Key: "timeout"}},
			{Name: "rest", Type: goavpipe.OverlaySlotClock, From: &goavpipe.OverlayBinding{Timer: "break_clock"}},
			{Name: "horn", Type: goavpipe.OverlaySlotText, From: &goavpipe.OverlayBinding{Key: "horn"}},
			{Name: "board", Type: goavpipe.OverlaySlotImage, From: &goavpipe.OverlayBinding{
				Renderer: goavpipe.OverlayRendererTemplate, Keys: []string{"home", "poss"}}},
		},
	}
	feed := NewVenueFeed()
	b, err := NewBindings(feed, layout, nil)
	require.NoError(t, err)
	assert.True(t, b.Rendered())
	var rendered []map[string]any
	b.Render = func(hash string, state map[string]any) {
		assert.Equal(t, StateHash(state), hash)
		rendered = append(rendered, state)
	}

	// Nothing published: every slot hidden, nothing rendered
	slots := b.Slots(Keys{}, 0, 1000)
	assert.Len(t, slots, 7)
	for name, v := range slots {
		assert.True(t, v.Hide, name)
	}
	assert.Empty(t, rendered)

	keys := Keys{"home_name": "Home", "fouls_home": 2, "poss": "away", "timeout": "home", "horn": true,
		"break_clock": Timer{ClockState: goavpipe.ClockState{Ms: 90000}, Pts: 5}}
	slots = b.Slots(keys, 100, 1000)
	assert.Equal(t, "Home", *slots["name"].Text)
	assert.Equal(t, "FOULS 2/", *slots["fouls"].Text, "an unset key among set ones reads as empty")
	assert.Equal(t, goavpipe.ImageRef{Name: "right"}, *slots["arrow"].Image)
	assert.Equal(t, goavpipe.ImageRef{Name: "home"}, *slots["who"].Image, "without a table the value names the image")
	assert.Equal(t, goavpipe.ClockState{Ms: 90000}, *slots["rest"].Clock)
	assert.Equal(t, "true", *slots["horn"].Text)
	require.Len(t, rendered, 1)
	assert.Equal(t, map[string]any{"poss": "away"}, rendered[0], "the keys of the binding that are set")
	assert.Equal(t, StateHash(rendered[0]), slots["board"].Image.Hash)
	b.Slots(keys, 200, 1000)
	assert.Len(t, rendered, 1, "the same picture is asked for once")

	keys["poss"] = "both"
	assert.True(t, b.Slots(keys, 300, 1000)["arrow"].Hide, "a value the table has no entry for")
	assert.Len(t, rendered, 2)

	bad := func(typ string, from goavpipe.OverlayBinding, want string) {
		t.Helper()
		_, err := NewBindings(feed, &goavpipe.OverlayParams{Images: layout.Images,
			Slots: []goavpipe.OverlaySlot{{Name: "x", Type: typ, From: &from}}}, nil)
		assert.ErrorContains(t, err, want)
	}
	bad(goavpipe.OverlaySlotText, goavpipe.OverlayBinding{Key: "fouls_hme"}, `does not publish key "fouls_hme"`)
	bad(goavpipe.OverlaySlotText, goavpipe.OverlayBinding{Text: "{score_home} {nosuch}"}, `does not publish key "nosuch"`)
	bad(goavpipe.OverlaySlotText, goavpipe.OverlayBinding{Text: "{score_home"}, "placeholder")
	bad(goavpipe.OverlaySlotText, goavpipe.OverlayBinding{Text: "score_home}"}, "} without {")
	bad(goavpipe.OverlaySlotText, goavpipe.OverlayBinding{Key: "clock"}, "is a timer")
	bad(goavpipe.OverlaySlotText, goavpipe.OverlayBinding{Key: "home", Text: "{away}"}, "one of")
	bad(goavpipe.OverlaySlotText, goavpipe.OverlayBinding{}, "one of")
	bad(goavpipe.OverlaySlotClock, goavpipe.OverlayBinding{Timer: "period"}, "is not a timer")
	bad(goavpipe.OverlaySlotClock, goavpipe.OverlayBinding{Key: "period"}, "does not fit a clock slot")
	bad(goavpipe.OverlaySlotImage, goavpipe.OverlayBinding{Key: "score_home"}, "not a string")
	bad(goavpipe.OverlaySlotImage, goavpipe.OverlayBinding{Key: "poss", Images: map[string]string{"home": "nosuch"}}, "library")
	bad(goavpipe.OverlaySlotImage, goavpipe.OverlayBinding{Renderer: goavpipe.OverlayRendererTemplate}, "lists the keys")
	bad(goavpipe.OverlaySlotImage, goavpipe.OverlayBinding{Renderer: goavpipe.OverlayRendererTemplate, Keys: []string{"shot"}}, "is a timer")
	bad(goavpipe.OverlaySlotImage, goavpipe.OverlayBinding{Renderer: "chrome", Keys: []string{"home"}}, "unknown renderer")
}

// TestVenueFeedKeys: what the venue feed publishes of each message.
func TestVenueFeedKeys(t *testing.T) {
	log := NewLog()
	a, _ := venueStamper(t, log, nil, nil)
	f := &feeder{a: a, pts: 1800000000}
	seq := 0
	send := func(id string, fields ...string) { seq++; f.frame(ancFrame(venueMessage(seq, id, fields...)...)) }

	// Before the teams a number of 0 says nothing; one that is not 0 publishes its pair
	send("2", "0", "0", "0", "0")
	send("1", "0", "0")
	assert.Empty(t, a.Keys())
	send("1", "0", "2")
	assert.Equal(t, Keys{"score_home": 0, "score_away": 2}, a.Keys())

	send("17", "Home", "Visitor", "HOM", "VIS")
	send("2", "3", "5", "0", "1")
	send("4", "3", "2", "2nd")
	send("5", "2")
	send("32", "A", "1", "0", "5", "6", "0", "0", "1", "2")
	send("33", "7:15 PM", "19:15:40")
	send("30", "8:55", "", "0:45", "1", "1", "1")
	send("31", "0.0", "1", "1", "0")
	keys := a.Keys()
	clock, shot, timeout := keys["clock"].(Timer), keys["shot"].(Timer), keys["timeout_clock"].(Timer)
	for _, name := range []string{"clock", "shot", "timeout_clock"} {
		delete(keys, name)
	}
	assert.Equal(t, Keys{
		"home": "HOM", "away": "VIS", "home_name": "Home", "away_name": "Visitor",
		"score_home": 0, "score_away": 2,
		"fouls_home": 3, "fouls_away": 5, "bonus_home": 0, "bonus_away": 1,
		"period": "2nd", "period_type": "play", "period_number": 2,
		"poss": "away", "timeout": "home",
		"to_home": 5, "to_away": 6, "to_short_home": 1, "to_short_away": 2,
		"time_of_day": "19:15:40",
		"horn":        true, "period_end": true, "shot_horn": true, "shot_expired": true,
	}, keys)
	assert.Equal(t, goavpipe.ClockState{Ms: 535000}, clock.ClockState, "held: a timeout clock shows")
	assert.Equal(t, goavpipe.ClockState{Ms: 45000}, timeout.ClockState)
	assert.Equal(t, goavpipe.ClockState{}, shot.ClockState)
	for name, kind := range NewVenueFeed().KeyKinds() {
		if v, ok := a.Keys()[name]; ok {
			switch v.(type) {
			case string:
				assert.Equal(t, KeyString, kind, name)
			case int:
				assert.Equal(t, KeyNumber, kind, name)
			case bool:
				assert.Equal(t, KeyBool, kind, name)
			case Timer:
				assert.Equal(t, KeyTimer, kind, name)
			}
		}
	}
	for name := range a.Keys() {
		assert.NotZero(t, NewVenueFeed().KeyKinds()[name], "published key %s is declared", name)
	}

	// Cleared again: empty strings and a blank clock unset their keys
	send("5", "")
	send("32", " ", "1", "0", "5", "6", "0", "0", "1", "2")
	send("30", "", "", "", "0", "0", "0")
	send("31", "", "0", "0", "0")
	keys = a.Keys()
	for _, name := range []string{"poss", "timeout", "clock", "shot", "timeout_clock", "horn", "period_end", "shot_horn"} {
		assert.NotContains(t, keys, name)
	}
}

// TestANCUnboundKeys: a key no slot is bound to makes no event, however often it changes.
func TestANCUnboundKeys(t *testing.T) {
	layout := &goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{{Name: "score", Type: goavpipe.OverlaySlotText}}}
	log := NewLog()
	a, _ := venueStamper(t, log, layout, nil)
	f := &feeder{a: a, pts: 1800000000}
	seq := 0
	send := func(id string, fields ...string) { seq++; f.frame(ancFrame(venueMessage(seq, id, fields...)...)) }

	send("17", "Home", "Visitor", "HOM", "VIS")
	require.Len(t, log.Events(goavpipe.KindOverlay), 1, "the score shows once the teams are known")
	for i := 0; i < 5; i++ {
		send("33", "7:15 PM", fmt.Sprintf("19:15:%02d", i))
		send("2", fmt.Sprint(i), "0", "0", "0")
		send("30", fmt.Sprintf("11:%02d", 59-i), "", "", "0", "0", "0")
	}
	assert.Len(t, log.Events(goavpipe.KindOverlay), 1)
	assert.Equal(t, 4, a.Keys()["fouls_home"], "published all the same")
	send("1", "2", "0")
	events := log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 2)
	ev := events[1].(goavpipe.OverlayEvent)
	assert.Equal(t, map[string]goavpipe.SlotValue{"score": goavpipe.TextValue("2 - 0")}, ev.Slots)
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

// TestOverlayBindingJSON: a binding as a layout stores it.
func TestOverlayBindingJSON(t *testing.T) {
	var slot goavpipe.OverlaySlot
	require.NoError(t, json.Unmarshal([]byte(
		`{"name":"poss","type":"image","from":{"key":"poss","images":{"home":"left"}}}`), &slot))
	assert.Equal(t, &goavpipe.OverlayBinding{Key: "poss", Images: map[string]string{"home": "left"}}, slot.From)
	b, err := json.Marshal(goavpipe.OverlaySlot{Name: "score", Type: goavpipe.OverlaySlotText})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "from", "a slot without a binding stores none")
}
