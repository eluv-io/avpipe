package stamp

import (
	"fmt"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/anc"
	"github.com/eluv-io/common-go/media/anc/venueclock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// venueMessage frames one venue clock message, CRC included.
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
	assert.Equal(t, "24", *ev.Slots["shot"].Text)

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

	// A sample 200 ms off the prediction is a correction; a stop is an event
	f.frame(ancFrame(next("30", "11:50", "", "", "0", "0", "0")...))
	events = log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 8)
	assert.Equal(t, int64(710000), events[7].(goavpipe.OverlayEvent).Slots["clock"].Clock.Ms)
	f.frame(ancFrame(next("30", "11:50", "", "", "0", "0", "1")...))
	events = log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 9)
	assert.False(t, events[8].(goavpipe.OverlayEvent).Slots["clock"].Clock.Running)

	// Repeats change nothing
	f.frame(ancFrame(next("17", "Home", "Visitor", "HOM", "VIS")...))
	f.frame(ancFrame(next("31", "24", "0", "0", "0")...))
	assert.Len(t, log.Events(goavpipe.KindOverlay), 9)

	// A bad CRC is counted and ignored; a dropped-packet count is carried
	bad := next("1", "9", "9")
	bad[len(bad)-2] ^= 0x01
	f.frame(ancFrame(bad...))
	a.Feed(&goavpipe.AncPacket{Pts: f.pts + 10*1501, TimeBaseNum: 1, TimeBaseDen: 90000, Payload: ancFrame(), Dropped: 3})
	assert.Len(t, log.Events(goavpipe.KindOverlay), 9)
	assert.Equal(t, 1, a.Stats.BadCRC)
	assert.Equal(t, 3, a.Stats.Dropped)
	assert.Equal(t, 9, a.Stats.Events)
	assert.Equal(t, 0, a.Stats.Errors)
	assert.Equal(t, "4 - 0", fmt.Sprintf("%d - %d", a.State().Score[0], a.State().Score[1]))
	assert.Equal(t, int64(9), a.Last().Seq)

	// Every event validates against a layout with these slots
	layout := &goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{
		{Name: "home", Type: goavpipe.OverlaySlotText}, {Name: "away", Type: goavpipe.OverlaySlotText},
		{Name: "score", Type: goavpipe.OverlaySlotText}, {Name: "period", Type: goavpipe.OverlaySlotText},
		{Name: "clock", Type: goavpipe.OverlaySlotClock}, {Name: "shot", Type: goavpipe.OverlaySlotText}}}
	for _, e := range log.Events(goavpipe.KindOverlay) {
		oe := e.(goavpipe.OverlayEvent)
		assert.NoError(t, layout.ValidateEvent(&oe))
	}
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
