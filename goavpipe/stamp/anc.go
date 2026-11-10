package stamp

import (
	"sync"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/anc"
	"github.com/eluv-io/common-go/media/anc/venueclock"
)

// ANC turns the ST 2038 packets a transcode reports through AV_IN_STAT_DATA_ANC into overlay events in a Log. Each
// packet's ancillary data is parsed, the venue clock feed in it reassembled and decoded, and every change of the
// venue state becomes an OverlayEvent snapshot at the pts of the frame that completed the message, in the
// overlay's timescale. After each packet the log is sealed at that frame, so a job asking for a later frame waits
// until its ancillary data has been seen. Feed is called from one goroutine, the stat's; the other methods may be
// called from any.
type ANC struct {
	log       *Log
	timescale int64
	mapping   Mapper
	tolerance int64 // ms: a game clock sample this close to the prediction is no correction

	mu     sync.Mutex
	stream venueclock.Stream
	state  venueclock.State
	seq    int64
	last   *goavpipe.OverlayEvent // the last event appended
	sample clockSample            // the previous game clock sample, to tell a held clock from a running one
	held   bool                   // the game clock is held although the flags say it runs
	raw    map[string]rawClock    // per clock slot, the venue's last sample and when it was taken
	Stats  ANCStats
}

// rawClock is a clock value as the venue sent it, with the pts of the frame it was sampled on. A snapshot made
// later for another message must carry it forward from there, not treat it as current.
type rawClock struct {
	c   goavpipe.ClockState
	pts int64
}

// clockSample is one game clock value as the venue sent it.
type clockSample struct {
	ms      int64
	pts     int64 // overlay timescale
	valid   bool
	stopped bool // the "time stopped" stripe was on
}

// ANCStats counts what went through.
type ANCStats struct {
	Packets  int // ancillary PES packets fed
	Dropped  int // packets the stat path dropped before delivery (AncPacket.Dropped)
	Messages int // venue clock messages decoded
	BadCRC   int
	Events   int // overlay events appended
	Errors   int // packets whose ancillary data or clock messages did not parse
}

// NewANC makes a stamper writing KindOverlay events of timescale into log, mapped by m. The clock tolerance is
// VenueMapping.ClockToleranceMs when m is one, 50 ms otherwise.
func NewANC(log *Log, timescale int64, m Mapper) *ANC {
	if timescale <= 0 {
		timescale = goavpipe.OverlayDefaultTimescale
	}
	log.kindMake(goavpipe.KindOverlay)
	tol := int64(50)
	if vm, ok := m.(VenueMapping); ok {
		tol = vm.tolerance()
	}
	return &ANC{log: log, timescale: timescale, mapping: m, tolerance: tol, raw: map[string]rawClock{}}
}

// kindMake registers a kind so Kinds() lists it before the first event.
func (l *Log) kindMake(kind string) {
	l.mu.Lock()
	l.kind(kind)
	l.mu.Unlock()
}

// ts converts a pts in the packet's time base to the overlay timescale, rounded down as the compositor rounds
// the frame pts, so a message completed on a frame applies on that frame.
func (a *ANC) ts(p *goavpipe.AncPacket) int64 {
	num, den := int64(p.TimeBaseNum), int64(p.TimeBaseDen)
	if num <= 0 || den <= 0 {
		num, den = 1, 90000
	}
	return p.Pts * num * a.timescale / den
}

// Feed takes one ancillary packet. Parse errors are counted, not returned: the feed goes on with the next packet.
func (a *ANC) Feed(p *goavpipe.AncPacket) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Stats.Packets++
	a.Stats.Dropped += p.Dropped
	if p.Pts < 0 {
		return // no pts, no frame to attach anything to
	}
	pkts, err := anc.ParsePES(p.Payload)
	if err != nil {
		a.Stats.Errors++
	}
	msgs, err := a.stream.FeedPackets(pkts, p.Pts)
	if err != nil {
		a.Stats.Errors++
	}
	for _, m := range msgs {
		a.Stats.Messages++
		if !m.ChecksumOK {
			a.Stats.BadCRC++
			continue
		}
		changed := a.state.Apply(m)
		pts := a.ts(&goavpipe.AncPacket{Pts: m.Pts, TimeBaseNum: p.TimeBaseNum, TimeBaseDen: p.TimeBaseDen})
		if m.MessageID == venueclock.MsgGameTime && a.noteSample(pts) {
			changed = true
		}
		if changed {
			a.emit(pts)
		}
	}
	a.log.Seal(goavpipe.KindOverlay, a.ts(p))
}

// noteSample records the game clock sample of a message at pts and keeps held up to date: the clock is held when
// a sample repeats the previous value although time passed, since a running clock reads differently every second,
// and runs again when a sample moves or the "time stopped" stripe goes from on to off, which is the operator
// starting it. The venue's flags alone do not say: a break shows 12:00 with the stripe off. Returns whether held
// changed. Caller holds mu.
func (a *ANC) noteSample(pts int64) bool {
	prev := a.sample
	a.sample = clockSample{ms: int64(a.state.Clock.Ms), pts: pts, valid: a.state.Clock.Valid, stopped: a.state.Stopped}
	was := a.held
	switch {
	case !prev.valid || !a.sample.valid:
		a.held = false
	case prev.stopped && !a.sample.stopped:
		a.held = false
	case a.sample.ms != prev.ms:
		a.held = false
	case (pts-prev.pts)*1000/a.timescale >= 500:
		a.held = true
	}
	return a.held != was
}

// emit appends a snapshot at pts when what the mapping shows changed. A running game clock sample within the
// tolerance of what the previous event already predicts for pts is replaced by that prediction, so a snapshot
// made for a score change does not also nudge the clock, and a sample alone makes no event. While held, the game
// clock is written stopped whatever the flags say. Caller holds mu.
func (a *ANC) emit(pts int64) {
	slots := a.mapping.Slots(&a.state)
	if a.held {
		for _, name := range a.mapping.ClockSlots() {
			if v, ok := slots[name]; ok && v.Clock != nil && v.Clock.Running {
				c := *v.Clock
				c.Running = false
				slots[name] = goavpipe.SlotValue{Clock: &c}
			}
		}
	}
	// A clock value in the mapping is the venue's last sample, taken on an earlier frame. A snapshot made now
	// (for a score, say) must carry it forward to pts, or it would stamp a clock up to a second behind.
	for _, name := range a.mapping.ClockSlots() {
		v, ok := slots[name]
		if !ok || v.Clock == nil {
			delete(a.raw, name)
			continue
		}
		r, seen := a.raw[name]
		if !seen || r.c != *v.Clock {
			r = rawClock{c: *v.Clock, pts: pts}
			a.raw[name] = r
		}
		carried := carryClock(r.c, (pts-r.pts)*1000/a.timescale)
		slots[name] = goavpipe.SlotValue{Clock: &carried}
	}
	changed := a.last == nil
	if a.last != nil {
		dt := (pts - a.last.Pts) * 1000 / a.timescale
		for _, name := range a.mapping.ClockSlots() {
			nv, hasNew := slots[name]
			lv, hasLast := a.last.Slots[name]
			if !hasNew || !hasLast || nv.Clock == nil || lv.Clock == nil {
				// Shown, hidden or set for the first time: a plain comparison decides
				if hasNew != hasLast || !sameValue(nv, lv) {
					changed = true
				}
				continue
			}
			predicted := carryClock(*lv.Clock, dt)
			if predicted.Running == nv.Clock.Running && predicted.Up == nv.Clock.Up &&
				abs64(predicted.Ms-nv.Clock.Ms) <= a.tolerance {
				slots[name] = goavpipe.SlotValue{Clock: &predicted}
			} else {
				changed = true
			}
		}
		if !changed {
			changed = !sameSlots(slots, a.last.Slots, a.mapping.ClockSlots())
		}
	}
	if !changed {
		return
	}
	a.seq++
	ev := goavpipe.OverlayEvent{
		TimedEvent: goavpipe.TimedEvent{Seq: a.seq, Pts: pts, Kind: goavpipe.KindOverlay},
		Slots:      slots,
	}
	if err := a.log.Append(ev); err != nil {
		a.Stats.Errors++
		return
	}
	a.Stats.Events++
	a.last = &ev
}

// SetSeq makes the next event's Seq n+1, for a log that already holds events of this kind up to Seq n.
func (a *ANC) SetSeq(n int64) {
	a.mu.Lock()
	a.seq = n
	a.mu.Unlock()
}

// State returns a copy of the venue state as last decoded.
func (a *ANC) State() venueclock.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// Last returns the last event appended, or nil.
func (a *ANC) Last() *goavpipe.OverlayEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last == nil {
		return nil
	}
	ev := *a.last
	return &ev
}

// carryClock is c advanced by elapsed ms when running; a countdown stops at 0.
func carryClock(c goavpipe.ClockState, elapsedMs int64) goavpipe.ClockState {
	if !c.Running {
		return c
	}
	if c.Up {
		c.Ms += elapsedMs
	} else if c.Ms -= elapsedMs; c.Ms < 0 {
		c.Ms = 0
	}
	return c
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// sameSlots compares two slot maps by value, ignoring the clock slots (handled by the tolerance rule).
func sameSlots(a, b map[string]goavpipe.SlotValue, skip []string) bool {
	if len(a) != len(b) {
		return false
	}
	isSkipped := func(name string) bool {
		for _, s := range skip {
			if s == name {
				return true
			}
		}
		return false
	}
	for name, va := range a {
		vb, ok := b[name]
		if !ok {
			return false
		}
		if isSkipped(name) {
			continue
		}
		if !sameValue(va, vb) {
			return false
		}
	}
	return true
}

func sameValue(a, b goavpipe.SlotValue) bool {
	switch {
	case a.Hide != b.Hide:
		return false
	case (a.Text == nil) != (b.Text == nil), a.Text != nil && *a.Text != *b.Text:
		return false
	case (a.Image == nil) != (b.Image == nil), a.Image != nil && *a.Image != *b.Image:
		return false
	case (a.Clock == nil) != (b.Clock == nil), a.Clock != nil && *a.Clock != *b.Clock:
		return false
	}
	return true
}
