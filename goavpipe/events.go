package goavpipe

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Timed data entering a transcode: the overlay's slot values and, later, the vertical crop centre. A job has one
// EventSource (XcParams.Events) that answers for every kind its consumers need. Each kind is a sequence of
// snapshots: an event is the complete state of that consumer from its Pts on, and on every decoded frame the
// consumer applies the latest event of its kind with Pts at or before the frame's pts. avpipe asks the source once
// per enabled consumer per frame and never blocks on it; waiting, cancel and the timeout policy are avpipe's.

// Event kinds.
const (
	KindOverlay      = "overlay"
	KindVerticalCrop = "vertical_crop"
)

// TimedEvent is the base of every event: when it applies and which kind it is. It is all the channel itself reads.
type TimedEvent struct {
	Seq  int64  `json:"seq"`  // per kind, 0 or more, increases with every event of that kind
	Pts  int64  `json:"pts"`  // in the consumer's timescale; the event applies from here until the next of its kind
	Kind string `json:"kind"` // KindOverlay, KindVerticalCrop
}

// Timing makes every type that embeds TimedEvent a Timed.
func (t TimedEvent) Timing() TimedEvent { return t }

// Timed is implemented by every event type.
type Timed interface{ Timing() TimedEvent }

// Status is the answer of a non-blocking lookup.
type Status int

const (
	StatusReady  Status = iota // the value is final and returned
	StatusNotYet               // not sealed or not fetched yet; ask again
	StatusEnded                // nothing more will come for this kind or hash
)

func (s Status) String() string {
	switch s {
	case StatusReady:
		return "ready"
	case StatusNotYet:
		return "not yet"
	case StatusEnded:
		return "ended"
	}
	return fmt.Sprintf("status(%d)", int(s))
}

// EventSource is the job's one source of timed events. Every method returns at once. avpipe calls it from the
// transcode's own thread, once per enabled consumer per decoded frame, so a call is a lookup, not a fetch: a
// source that has to wait for data answers StatusNotYet and avpipe asks again.
type EventSource interface {
	// Kinds lists the kinds this source answers for. Checked when the job starts against the enabled consumers;
	// a consumer whose kind is missing fails the job with EAV_PARAM before any output.
	Kinds() []string
	// At returns the latest event of kind with Pts <= pts, once it is sealed, as (event, StatusReady, nil); when
	// no event of the kind applies at pts yet and that is final, (nil, StatusReady, nil). A consumer that already
	// holds an event keeps it when it gets nil or the same Seq back. StatusNotYet means the source cannot yet say
	// what applies at pts. StatusEnded means no event of this kind will apply at or after pts: the consumer keeps
	// what it has and asks no more. An error fails the job with the consumer's error.
	At(kind string, pts int64) (Timed, Status, error)
	// Asset returns the bytes of an image an event refers to by hash (PNG), with the same statuses. avpipe asks
	// once per hash per job and keeps the decoded image.
	Asset(hash string) ([]byte, Status, error)
	// Close ends the source. avpipe calls it once when the job ends or is cancelled; a later At or Asset returns an
	// error.
	Close() error
}

// OverlayEvent is the complete state of the overlay from Pts on. A slot absent from Slots is blank, hidden or
// showing its layout default.
type OverlayEvent struct {
	TimedEvent
	Hidden bool                 `json:"hidden,omitempty"` // hides the whole overlay; the clocks keep running
	Slots  map[string]SlotValue `json:"slots"`
}

// SlotValue is one slot's value. Exactly one field is set, and it must match the slot's type in the layout: Text
// for a text slot, Image for an image slot, Clock for a clock slot; Hide for any slot. In JSON a plain string is
// accepted as {"text": ...} and null as {"hide": true}.
type SlotValue struct {
	Text  *string     `json:"text,omitempty"`
	Image *ImageRef   `json:"image,omitempty"`
	Clock *ClockState `json:"clock,omitempty"`
	Hide  bool        `json:"hide,omitempty"`
}

// TextValue, ImageValue, ClockValue and HideValue build a SlotValue.
func TextValue(s string) SlotValue         { return SlotValue{Text: &s} }
func ImageValue(name string) SlotValue     { return SlotValue{Image: &ImageRef{Name: name}} }
func ImageHashValue(hash string) SlotValue { return SlotValue{Image: &ImageRef{Hash: hash}} }
func ClockValue(ms int64, running bool) SlotValue {
	return SlotValue{Clock: &ClockState{Ms: ms, Running: running}}
}
func HideValue() SlotValue { return SlotValue{Hide: true} }

type slotValueJSON SlotValue

func (v *SlotValue) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	switch {
	case t == "null":
		*v = SlotValue{Hide: true}
		return nil
	case strings.HasPrefix(t, "\""):
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*v = SlotValue{Text: &s}
		return nil
	}
	var x slotValueJSON
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	*v = SlotValue(x)
	return v.check()
}

// check verifies that exactly one field is set.
func (v SlotValue) check() error {
	n := 0
	if v.Text != nil {
		n++
	}
	if v.Image != nil {
		n++
		if (v.Image.Name == "") == (v.Image.Hash == "") {
			return fmt.Errorf("an image names a library image or a hash, not both or neither")
		}
	}
	if v.Clock != nil {
		n++
		if v.Clock.Ms < 0 {
			return fmt.Errorf("clock ms must not be negative")
		}
	}
	if v.Hide {
		n++
	}
	if n != 1 {
		return fmt.Errorf("a slot value sets exactly one of text, image, clock or hide")
	}
	return nil
}

// ImageRef names an image: Name in OverlayParams.Images, the static library the job starts with, or Hash of a
// rendered image that the job fetches once through EventSource.Asset. Exactly one is set.
type ImageRef struct {
	Name string `json:"name,omitempty"`
	Hash string `json:"hash,omitempty"`
}

// ClockState is a clock at the event's Pts. The compositor draws it from media time: while Running the shown
// value is Ms plus the time since the event's Pts counting up, or minus it counting down, clamped at zero. The
// display format is the slot's (OverlaySlot.Format). A feed that samples a venue clock turns the samples into
// corrections, a new event only when the clock starts, stops or drifts.
type ClockState struct {
	Ms      int64 `json:"ms"`
	Running bool  `json:"running"`
	Up      bool  `json:"up,omitempty"` // counts up; the default counts down
}

// CropEvent is the vertical crop's state from Pts on (KindVerticalCrop; the consumer arrives with P2).
type CropEvent struct {
	TimedEvent
	Center int32 `json:"center"` // crop window centre as a fraction of the scaled width, in 1/10000
}

// Overlay timeout policies, OverlayParams.OnTimeout.
const (
	OverlayTimeoutFail = "fail" // the job fails with EAV_OVERLAY (default)
	OverlayTimeoutHold = "hold" // the previous event stays in effect
)

// Overlay defaults.
const (
	OverlayDefaultTimescale = 1000
	OverlayDefaultWaitMs    = 10000
)

// ValidateEvent checks e against the layout: every slot named exists, its value's type matches the slot's, a
// library image is in Images, and the event's kind is the overlay's. The stamper runs it before storing an event
// and avpipe before applying one, so a bad event is refused where it is made rather than failing a playout job.
func (p *OverlayParams) ValidateEvent(e *OverlayEvent) error {
	if e.Kind != "" && e.Kind != KindOverlay {
		return fmt.Errorf("event kind %q is not %q", e.Kind, KindOverlay)
	}
	for name, v := range e.Slots {
		slot := p.Slot(name)
		if slot == nil {
			return fmt.Errorf("unknown slot %q", name)
		}
		if err := v.check(); err != nil {
			return fmt.Errorf("slot %q: %w", name, err)
		}
		switch {
		case v.Hide:
		case v.Text != nil && slot.Type != OverlaySlotText:
			return fmt.Errorf("slot %q is not a text slot", name)
		case v.Image != nil && slot.Type != OverlaySlotImage:
			return fmt.Errorf("slot %q is not an image slot", name)
		case v.Clock != nil && slot.Type != OverlaySlotClock:
			return fmt.Errorf("slot %q is not a clock slot", name)
		case v.Image != nil && v.Image.Name != "":
			if _, ok := p.Images[v.Image.Name]; !ok {
				return fmt.Errorf("slot %q: image %q is not in the library", name, v.Image.Name)
			}
		}
	}
	return nil
}

// Slot returns the layout slot named name, or nil.
func (p *OverlayParams) Slot(name string) *OverlaySlot {
	for i := range p.Slots {
		if p.Slots[i].Name == name {
			return &p.Slots[i]
		}
	}
	return nil
}

// StaticSource answers At from events known up front, for VOD jobs, tests and spec files, and Asset from assets
// by hash. Events are sorted by kind, Pts and Seq; the latest with Pts <= pts applies, and the last event of a
// kind applies to the end of the job. Kinds lists the kinds of the events plus kinds, so a consumer with no
// events yet still passes the start-of-job check.
func StaticSource(events []Timed, assets map[string][]byte, kinds ...string) EventSource {
	s := &staticSource{byKind: map[string][]Timed{}, assets: assets}
	for _, e := range events {
		k := e.Timing().Kind
		s.byKind[k] = append(s.byKind[k], e)
	}
	for k := range s.byKind {
		ev := s.byKind[k]
		sort.SliceStable(ev, func(a, b int) bool {
			ta, tb := ev[a].Timing(), ev[b].Timing()
			return ta.Pts < tb.Pts || ta.Pts == tb.Pts && ta.Seq < tb.Seq
		})
	}
	s.kinds = make([]string, 0, len(s.byKind)+len(kinds))
	for k := range s.byKind {
		s.kinds = append(s.kinds, k)
	}
	for _, k := range kinds {
		if _, ok := s.byKind[k]; !ok {
			s.kinds = append(s.kinds, k)
		}
	}
	sort.Strings(s.kinds)
	return s
}

type staticSource struct {
	mu     sync.Mutex
	byKind map[string][]Timed
	kinds  []string
	assets map[string][]byte
	closed bool
}

func (s *staticSource) Kinds() []string { return s.kinds }

func (s *staticSource) At(kind string, pts int64) (Timed, Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, StatusEnded, fmt.Errorf("event source is closed")
	}
	ev := s.byKind[kind]
	// The last event with Pts <= pts: the first index whose Pts > pts, minus one
	i := sort.Search(len(ev), func(i int) bool { return ev[i].Timing().Pts > pts })
	if i == 0 {
		return nil, StatusReady, nil
	}
	return ev[i-1], StatusReady, nil
}

func (s *staticSource) Asset(hash string) ([]byte, Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, StatusEnded, fmt.Errorf("event source is closed")
	}
	b, ok := s.assets[hash]
	if !ok {
		return nil, StatusReady, fmt.Errorf("unknown asset %q", hash)
	}
	return b, StatusReady, nil
}

func (s *staticSource) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}
