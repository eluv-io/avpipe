// Package stamp holds the reusable parts of an event stamper: the pieces between a feed of timed data (an ST 2038
// venue clock, an API) and the EventSource a transcode asks once per frame. Log keeps the stamped events with a
// seal per kind and hands out per-job sources; ANC turns ancillary data packets into events through a venue
// mapping. elvxc wires them for demos and the fabric wires them into the live recorder, adding persistence through
// Log's event hook.
package stamp

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/eluv-io/avpipe/goavpipe"
)

// Log is the stamped event log of one stream: for every kind, the events sorted by pts and the pts up to which
// they are final (the seal). It is not an EventSource itself; Source gives each job its own view, so a job closing
// its source leaves the log and the other jobs alone. Appends, seals and asks may come from different goroutines.
type Log struct {
	mu       sync.Mutex
	kinds    map[string]*kindLog
	names    []string
	assets   map[string][]byte
	fetching map[string]bool
	fetch    func(hash string)
	onEvent  func(goavpipe.Timed)
}

type kindLog struct {
	events []goavpipe.Timed // sorted by Pts, then Seq
	seal   int64
	sealed bool
}

// NewLog makes a log that answers for kinds. A kind that gets events later is added then; naming it here lets
// Kinds() satisfy a job's start-of-job check before the first event arrives.
func NewLog(kinds ...string) *Log {
	l := &Log{kinds: map[string]*kindLog{}, assets: map[string][]byte{}, fetching: map[string]bool{}}
	for _, k := range kinds {
		l.kind(k)
	}
	return l
}

// kind returns the kind's log, making it. Caller holds mu or is the constructor.
func (l *Log) kind(name string) *kindLog {
	k, ok := l.kinds[name]
	if !ok {
		k = &kindLog{}
		l.kinds[name] = k
		l.names = append(l.names, name)
		sort.Strings(l.names)
	}
	return k
}

// OnEvent sets a function called, under the log's lock, with every event Append accepts: the place to persist
// events. Set it before the first Append.
func (l *Log) OnEvent(fn func(goavpipe.Timed)) {
	l.mu.Lock()
	l.onEvent = fn
	l.mu.Unlock()
}

// SetAssetFetcher sets what happens when a job asks for an image the log does not hold: fetch is called once per
// hash, on its own goroutine, and is expected to call PutAsset when the bytes are in. Until then the job is told
// not yet. Without a fetcher an unknown hash is an error.
func (l *Log) SetAssetFetcher(fetch func(hash string)) {
	l.mu.Lock()
	l.fetch = fetch
	l.mu.Unlock()
}

// Append adds an event of its own kind. Events may arrive out of pts order (an API event placed ahead of time) and
// are kept sorted by pts then seq; two events of one kind with the same seq are refused.
func (l *Log) Append(ev goavpipe.Timed) error {
	t := ev.Timing()
	if t.Kind == "" {
		return fmt.Errorf("stamp: event without a kind")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	k := l.kind(t.Kind)
	i := sort.Search(len(k.events), func(i int) bool {
		ti := k.events[i].Timing()
		return ti.Pts > t.Pts || ti.Pts == t.Pts && ti.Seq >= t.Seq
	})
	if i < len(k.events) && k.events[i].Timing().Seq == t.Seq && k.events[i].Timing().Pts == t.Pts {
		return fmt.Errorf("stamp: %s event seq %d already logged", t.Kind, t.Seq)
	}
	k.events = append(k.events, nil)
	copy(k.events[i+1:], k.events[i:])
	k.events[i] = ev
	if l.onEvent != nil {
		l.onEvent(ev)
	}
	return nil
}

// Seal marks the kind's events final up to and including pts: no event of the kind with a pts at or below it will
// be appended after this. The seal never moves backwards.
func (l *Log) Seal(kind string, pts int64) {
	l.mu.Lock()
	k := l.kind(kind)
	if !k.sealed || pts > k.seal {
		k.seal, k.sealed = pts, true
	}
	l.mu.Unlock()
}

// End seals the kind for good: nothing more will be appended, so every frame is answered.
func (l *Log) End(kind string) { l.Seal(kind, math.MaxInt64) }

// SealOf returns the kind's seal and whether one was set.
func (l *Log) SealOf(kind string) (int64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k, ok := l.kinds[kind]
	if !ok {
		return 0, false
	}
	return k.seal, k.sealed
}

// PutAsset stores an image by hash.
func (l *Log) PutAsset(hash string, b []byte) {
	l.mu.Lock()
	l.assets[hash] = b
	delete(l.fetching, hash)
	l.mu.Unlock()
}

// Kinds lists the kinds the log answers for.
func (l *Log) Kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.names...)
}

// Events returns a copy of the kind's events in order.
func (l *Log) Events(kind string) []goavpipe.Timed {
	l.mu.Lock()
	defer l.mu.Unlock()
	k, ok := l.kinds[kind]
	if !ok {
		return nil
	}
	return append([]goavpipe.Timed(nil), k.events...)
}

// Latest returns the last event of the kind by pts, sealed or not, or nil.
func (l *Log) Latest(kind string) goavpipe.Timed {
	l.mu.Lock()
	defer l.mu.Unlock()
	k, ok := l.kinds[kind]
	if !ok || len(k.events) == 0 {
		return nil
	}
	return k.events[len(k.events)-1]
}

// at answers for a job pts already translated to the log's timeline.
func (l *Log) at(kind string, pts int64) (goavpipe.Timed, goavpipe.Status, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k, ok := l.kinds[kind]
	if !ok {
		return nil, goavpipe.StatusEnded, nil
	}
	if !k.sealed || pts > k.seal {
		return nil, goavpipe.StatusNotYet, nil
	}
	i := sort.Search(len(k.events), func(i int) bool { return k.events[i].Timing().Pts > pts })
	if i == 0 {
		return nil, goavpipe.StatusReady, nil
	}
	return k.events[i-1], goavpipe.StatusReady, nil
}

func (l *Log) asset(hash string) ([]byte, goavpipe.Status, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.assets[hash]; ok {
		return b, goavpipe.StatusReady, nil
	}
	if l.fetch == nil {
		return nil, goavpipe.StatusReady, fmt.Errorf("stamp: unknown asset %q", hash)
	}
	if !l.fetching[hash] {
		l.fetching[hash] = true
		go l.fetch(hash)
	}
	return nil, goavpipe.StatusNotYet, nil
}

// Source gives a job its EventSource view of the log. offset is added to the job's pts to reach the log's
// timeline: a playout job on part n of a recording passes the part's start pts. The view hands events out on
// the job's timeline, their pts moved back by offset, so an event from before the part has a negative pts.
// Closing the view does not touch the log.
func (l *Log) Source(offset int64) goavpipe.EventSource {
	return &logView{log: l, offset: offset}
}

type logView struct {
	log    *Log
	offset int64
	mu     sync.Mutex
	closed bool
}

func (v *logView) Kinds() []string { return v.log.Kinds() }

func (v *logView) At(kind string, pts int64) (goavpipe.Timed, goavpipe.Status, error) {
	if v.isClosed() {
		return nil, goavpipe.StatusEnded, fmt.Errorf("stamp: source is closed")
	}
	ev, status, err := v.log.at(kind, pts+v.offset)
	if ev == nil || v.offset == 0 || err != nil {
		return ev, status, err
	}
	// The consumer measures a clock and a fade from the event's pts against its own frames
	moved, err := moveEvent(ev, -v.offset)
	return moved, status, err
}

// moveEvent returns a copy of ev with its pts moved by delta. The log's event is not changed.
func moveEvent(ev goavpipe.Timed, delta int64) (goavpipe.Timed, error) {
	switch e := ev.(type) {
	case goavpipe.OverlayEvent:
		e.Pts += delta
		return e, nil
	case *goavpipe.OverlayEvent:
		c := *e
		c.Pts += delta
		return &c, nil
	case goavpipe.CropEvent:
		e.Pts += delta
		return e, nil
	case *goavpipe.CropEvent:
		c := *e
		c.Pts += delta
		return &c, nil
	}
	return nil, fmt.Errorf("stamp: cannot move an event of type %T to the job's timeline", ev)
}

func (v *logView) Asset(hash string) ([]byte, goavpipe.Status, error) {
	if v.isClosed() {
		return nil, goavpipe.StatusEnded, fmt.Errorf("stamp: source is closed")
	}
	return v.log.asset(hash)
}

func (v *logView) Close() error {
	v.mu.Lock()
	v.closed = true
	v.mu.Unlock()
	return nil
}

func (v *logView) isClosed() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.closed
}
