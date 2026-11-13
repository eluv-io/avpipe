package stamp

import (
	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/eluv-io/avpipe/goavpipe"
)

// Keys is a stream's overlay state: the values a feed has published, by key. A value is a string, an int, a bool
// or a Timer. An absent key is unset: the feed has not sent it, or sent it empty.
type Keys map[string]any

// Timer is a clock key: the clock's state as of Pts, in the stamper's timescale. A snapshot made later carries a
// running timer forward from Pts.
type Timer struct {
	goavpipe.ClockState
	Pts int64
}

// setTimer publishes a clock. An unchanged state keeps its Pts: a running clock is carried from the sample that
// first showed it, not from a repeat of that sample.
func (k Keys) setTimer(name string, c goavpipe.ClockState, pts int64) {
	if cur, ok := k[name].(Timer); ok && cur.ClockState == c {
		return
	}
	k[name] = Timer{ClockState: c, Pts: pts}
}

// KeyKind is the type of a key's value.
type KeyKind int

const (
	KeyString KeyKind = iota + 1
	KeyNumber
	KeyBool
	KeyTimer
)

// Feed decodes one in-band data format into keys. ANC hands it the ancillary packets of every PES and stamps an
// event whenever what the layout's bindings show of the keys has changed. A feed is used by one ANC.
type Feed interface {
	// KeyKinds lists every key the feed can publish, with its kind. Bindings are checked against it, so a binding
	// that names a key the feed does not have is refused when the stamper is set up. The map is not modified.
	KeyKinds() map[string]KeyKind
	// DefaultBindings gives the binding of a slot that has none, by slot name. It covers what a slot of that name
	// has always shown and what no single key holds, such as a score made of two numbers.
	DefaultBindings() map[string]goavpipe.OverlayBinding
	// Decode takes the ancillary packets of the PES at srcPts, in the source's time base. For every change it
	// sets or deletes keys in c.Keys and calls c.Emit with the pts the change is at. It returns the number of
	// messages it decoded, how many of them were damaged, and an error when the packets did not parse; decoding
	// goes on with the next PES either way. Called from one goroutine.
	Decode(pkts []anc.Packet, srcPts int64, c *FeedContext) (messages, damaged int, err error)
}

// FeedContext is what ANC hands a Feed for one PES.
type FeedContext struct {
	Keys      Keys
	Timescale int64                    // of the pts given to Emit and kept in a Timer
	Pts       func(srcPts int64) int64 // a pts of the source in Timescale
	Emit      func(pts int64)          // snapshot the keys at pts
}
