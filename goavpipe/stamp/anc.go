package stamp

import (
	"sync"

	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/eluv-io/avpipe/goavpipe"
)

// ANC turns the ST 2038 packets a transcode reports through AV_IN_STAT_DATA_ANC into overlay events in a Log. Each
// packet's ancillary data is parsed and handed to the feed, which publishes what it decodes as keys. Every change
// of what the bindings show of the keys becomes an OverlayEvent snapshot at the pts of the frame that completed
// the change, in the overlay's timescale; a key no slot is bound to makes no event. After each packet the log is
// sealed at that frame, so a job asking for a later frame waits until its ancillary data has been seen. Feed is
// called from one goroutine, the stat's; the other methods may be called from any.
type ANC struct {
	log       *Log
	timescale int64
	feed      Feed
	bindings  *Bindings

	mu    sync.Mutex
	keys  Keys
	seq   int64
	last  *goavpipe.OverlayEvent // the last event appended
	Stats ANCStats
}

// ANCStats counts what went through.
type ANCStats struct {
	Packets  int // ancillary PES packets fed
	Dropped  int // packets the stat path dropped before delivery (AncPacket.Dropped)
	Messages int // messages the feed decoded
	BadCRC   int // of them, damaged ones, which are left out
	Events   int // overlay events appended
	Errors   int // packets whose ancillary data or feed did not parse
}

// NewANC makes a stamper writing KindOverlay events of timescale into log: what feed decodes, as bindings show it.
func NewANC(log *Log, timescale int64, feed Feed, bindings *Bindings) *ANC {
	if timescale <= 0 {
		timescale = goavpipe.OverlayDefaultTimescale
	}
	log.kindMake(goavpipe.KindOverlay)
	return &ANC{log: log, timescale: timescale, feed: feed, bindings: bindings, keys: Keys{}}
}

// kindMake registers a kind so Kinds() lists it before the first event.
func (l *Log) kindMake(kind string) {
	l.mu.Lock()
	l.kind(kind)
	l.mu.Unlock()
}

// ts converts a pts in the packet's time base to the overlay timescale, rounded down as the compositor rounds
// the frame pts, so a message completed on a frame applies on that frame.
func (a *ANC) ts(p *goavpipe.AncPacket, pts int64) int64 {
	num, den := int64(p.TimeBaseNum), int64(p.TimeBaseDen)
	if num <= 0 || den <= 0 {
		num, den = 1, 90000
	}
	return pts * num * a.timescale / den
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
	messages, damaged, err := a.feed.Decode(pkts, p.Pts, &FeedContext{
		Keys:      a.keys,
		Timescale: a.timescale,
		Pts:       func(srcPts int64) int64 { return a.ts(p, srcPts) },
		Emit:      a.emit,
	})
	a.Stats.Messages += messages
	a.Stats.BadCRC += damaged
	if err != nil {
		a.Stats.Errors++
	}
	a.log.Seal(goavpipe.KindOverlay, a.ts(p, p.Pts))
}

// emit appends a snapshot at pts when what the bindings show changed. A running clock within the tolerance of
// what the previous event already predicts for pts is replaced by that prediction, so a snapshot made for a score
// change does not also nudge the clock, and a sample alone makes no event. Caller holds mu.
func (a *ANC) emit(pts int64) {
	slots := a.bindings.Slots(a.keys, pts, a.timescale)
	clocks := a.bindings.ClockSlots()
	changed := a.last == nil
	if a.last != nil {
		dt := (pts - a.last.Pts) * 1000 / a.timescale
		for _, name := range clocks {
			nv, hasNew := slots[name]
			lv, hasLast := a.last.Slots[name]
			if !hasNew || !hasLast || nv.Clock == nil || lv.Clock == nil {
				// Shown, hidden or set for the first time: a plain comparison decides
				if hasNew != hasLast || !sameValue(nv, lv) {
					changed = true
				}
				continue
			}
			// The tolerance is for a running clock; a held one shows its value, so any difference counts
			predicted := carryClock(*lv.Clock, dt)
			if predicted == *nv.Clock || (predicted.Running && nv.Clock.Running && predicted.Up == nv.Clock.Up &&
				abs64(predicted.Ms-nv.Clock.Ms) <= a.bindings.tolerance()) {
				slots[name] = goavpipe.SlotValue{Clock: &predicted}
			} else {
				changed = true
			}
		}
		if !changed {
			changed = !sameSlots(slots, a.last.Slots, clocks)
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

// Keys returns a copy of the keys as last published.
func (a *ANC) Keys() Keys {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(Keys, len(a.keys))
	for k, v := range a.keys {
		out[k] = v
	}
	return out
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
