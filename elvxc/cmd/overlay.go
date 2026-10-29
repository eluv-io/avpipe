package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/eluv-io/avpipe/goavpipe"
)

// overlayTimescale is the unit of event pts in the spec and the stamper: milliseconds.
const overlayTimescale = 1000

// overlaySpec is the --overlay file: the layout with image and font paths relative to the file, and optional
// events with times in seconds on the input's own timeline.
type overlaySpec struct {
	DelayMs int64                  `json:"delay_ms"` // How far the stamper's through pts runs ahead of the frames
	Images  map[string]string      `json:"images"`   // Library name -> PNG path
	Slots   []goavpipe.OverlaySlot `json:"slots"`
	Events  []overlaySpecEvent     `json:"events"`
}

type overlaySpecEvent struct {
	Seq    int64          `json:"seq,omitempty"` // Default: position, from 1
	T      *float64       `json:"t,omitempty"`   // Seconds
	Pts    *int64         `json:"pts,omitempty"` // Milliseconds, instead of t
	Hidden bool           `json:"hidden,omitempty"`
	Data   map[string]any `json:"data"`
}

// loadOverlaySpec reads the spec, loads the images and resolves the font paths.
func loadOverlaySpec(path string) (*goavpipe.OverlayParams, *overlaySpec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var spec overlaySpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return nil, nil, fmt.Errorf("overlay spec %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}

	ov := &goavpipe.OverlayParams{Timescale: overlayTimescale, Images: map[string][]byte{}}
	for name, p := range spec.Images {
		png, err := os.ReadFile(resolve(p))
		if err != nil {
			return nil, nil, fmt.Errorf("overlay image %q: %w", name, err)
		}
		ov.Images[name] = png
	}
	for _, s := range spec.Slots {
		if s.Font != "" {
			s.Font = resolve(s.Font)
			if _, err := os.Stat(s.Font); err != nil {
				return nil, nil, fmt.Errorf("overlay slot %q font: %w", s.Name, err)
			}
		}
		ov.Slots = append(ov.Slots, s)
	}
	for i, e := range spec.Events {
		ev, err := overlaySpecEventToEvent(e, int64(i)+1)
		if err != nil {
			return nil, nil, fmt.Errorf("overlay event %d: %w", i, err)
		}
		if i > 0 {
			prev := ov.Events[i-1]
			if ev.Seq <= prev.Seq || ev.Pts < prev.Pts {
				return nil, nil, fmt.Errorf("overlay event %d: seq and pts must increase", i)
			}
		}
		ov.Events = append(ov.Events, ev)
	}
	return ov, &spec, nil
}

func overlaySpecEventToEvent(e overlaySpecEvent, defaultSeq int64) (goavpipe.OverlayEvent, error) {
	ev := goavpipe.OverlayEvent{Seq: e.Seq, Hidden: e.Hidden, Data: e.Data}
	if ev.Seq == 0 {
		ev.Seq = defaultSeq
	}
	switch {
	case e.T != nil && e.Pts != nil:
		return ev, fmt.Errorf("t and pts are mutually exclusive")
	case e.T != nil:
		ev.Pts = int64(math.Round(*e.T * overlayTimescale))
	case e.Pts != nil:
		ev.Pts = *e.Pts
	}
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	return ev, nil
}

// overlayPatch is one line of --overlay-events: a partial state merged into the last one.
type overlayPatch struct {
	Hidden *bool          `json:"hidden,omitempty"`
	Data   map[string]any `json:"data"`
}

// overlayStamper stands in for the recorder: it reads patches as they arrive, stamps each one just past the
// through pts, and serves every job that asks. The through pts advances with the job furthest ahead, so the jobs
// of one run all see the same event at the same frame however fast each one runs.
type overlayStamper struct {
	mu      sync.Mutex
	events  []goavpipe.OverlayEvent // every event stamped so far, sorted
	state   goavpipe.OverlayEvent   // the last full state, which patches merge into
	through int64                   // the events are final up to here
	delay   int64
	patches chan overlayPatch
	src     io.ReadCloser
	log     io.Writer
	readErr error
	done    chan struct{}
	refs    int // jobs holding a handle; the source closes with the last one
}

// newOverlayStamper starts reading JSON-line patches from src. The initial events are served as they are; the
// last one is the state patches merge into. Every stamped event is written to log as a JSON line, when set.
func newOverlayStamper(initial []goavpipe.OverlayEvent, delayMs int64, src io.ReadCloser, log io.Writer) *overlayStamper {
	s := &overlayStamper{
		events:  append([]goavpipe.OverlayEvent(nil), initial...),
		delay:   delayMs,
		patches: make(chan overlayPatch, 64),
		src:     src,
		log:     log,
		done:    make(chan struct{}),
	}
	if len(initial) > 0 {
		s.state = initial[len(initial)-1]
	}
	if s.state.Data == nil {
		s.state.Data = map[string]any{}
	}
	if src != nil {
		go s.read()
	} else {
		close(s.done)
	}
	return s
}

func (s *overlayStamper) read() {
	defer close(s.done)
	scanner := bufio.NewScanner(s.src)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var p overlayPatch
		if err := json.Unmarshal(line, &p); err != nil {
			s.mu.Lock()
			s.readErr = fmt.Errorf("bad overlay patch %q: %w", string(line), err)
			s.mu.Unlock()
			return
		}
		s.patches <- p
	}
	if err := scanner.Err(); err != nil {
		s.mu.Lock()
		s.readErr = err
		s.mu.Unlock()
	}
}

// apply merges a patch into the state and stamps it just past the through pts. Caller holds mu.
func (s *overlayStamper) apply(p overlayPatch) {
	data := make(map[string]any, len(s.state.Data)+len(p.Data))
	for k, v := range s.state.Data {
		data[k] = v
	}
	for k, v := range p.Data {
		data[k] = v
	}
	ev := goavpipe.OverlayEvent{Seq: s.state.Seq + 1, Pts: s.through + 1, Hidden: s.state.Hidden, Data: data}
	if p.Hidden != nil {
		ev.Hidden = *p.Hidden
	}
	if n := len(s.events); n > 0 && s.events[n-1].Pts >= ev.Pts {
		ev.Pts = s.events[n-1].Pts + 1
	}
	s.state = ev
	s.events = append(s.events, ev)
	if s.log != nil {
		if b, err := json.Marshal(ev); err == nil {
			_, _ = fmt.Fprintf(s.log, "%s\n", b)
		}
	}
}

// eventsThrough takes the patches that have arrived, advances the through pts and returns the events after
// afterSeq. It never blocks: in a single process the recorder is never behind the frames.
func (s *overlayStamper) eventsThrough(afterSeq, atLeast int64) ([]goavpipe.OverlayEvent, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for {
		select {
		case p := <-s.patches:
			s.apply(p)
			continue
		default:
		}
		break
	}
	if s.readErr != nil {
		return nil, 0, s.readErr
	}
	if atLeast+s.delay > s.through {
		s.through = atLeast + s.delay
	}
	var out []goavpipe.OverlayEvent
	for _, e := range s.events {
		if e.Seq > afterSeq {
			out = append(out, e)
		}
	}
	return out, s.through, nil
}

// handle gives one job its own OverlayEventSource. avpipe closes the source when the job ends; the stamper
// itself stops reading once every job has closed its handle.
func (s *overlayStamper) handle() goavpipe.OverlayEventSource {
	s.mu.Lock()
	s.refs++
	s.mu.Unlock()
	return &overlayStamperHandle{s: s}
}

func (s *overlayStamper) release() error {
	s.mu.Lock()
	s.refs--
	last := s.refs == 0
	s.mu.Unlock()
	if last && s.src != nil {
		return s.src.Close()
	}
	return nil
}

type overlayStamperHandle struct {
	s      *overlayStamper
	closed sync.Once
	err    error
}

func (h *overlayStamperHandle) EventsThrough(afterSeq, atLeast int64) ([]goavpipe.OverlayEvent, int64, error) {
	return h.s.eventsThrough(afterSeq, atLeast)
}

func (h *overlayStamperHandle) Close() error {
	h.closed.Do(func() { h.err = h.s.release() })
	return h.err
}

// openOverlayEvents opens the patch source. A FIFO is opened read-write so the open does not wait for a writer
// and the read does not end when one closes; a regular file is read to its end.
func openOverlayEvents(path string) (io.ReadCloser, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		f, err = os.Open(path)
	}
	return f, err
}
