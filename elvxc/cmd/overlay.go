package cmd

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/goavpipe/stamp"
)

// overlayTimescale is the unit of event pts in the spec and the stamper: milliseconds.
const overlayTimescale = 1000

// overlaySpec is the --overlay file: the layout with image and font paths relative to the file, and optional
// events with times in seconds on the input's own timeline.
type overlaySpec struct {
	DelayMs int64                  `json:"delay_ms"` // Added when a patch is stamped: how far past the through pts it lands
	Images  map[string]string      `json:"images"`   // Library name -> PNG path
	Slots   []goavpipe.OverlaySlot `json:"slots"`
	Events  []overlaySpecEvent     `json:"events"`
}

// overlaySpecEvent is one event of the spec. Slot values take the forms overlaySlotValue accepts.
type overlaySpecEvent struct {
	Seq    int64                      `json:"seq,omitempty"` // Default: position, from 1
	T      *float64                   `json:"t,omitempty"`   // Seconds
	Pts    *int64                     `json:"pts,omitempty"` // Milliseconds, instead of t
	Hidden bool                       `json:"hidden,omitempty"`
	Slots  map[string]json.RawMessage `json:"slots"`
	Data   map[string]json.RawMessage `json:"data"` // The older spelling of slots
}

// overlaySlotValue decodes one spec or patch value for the slot called name. Besides the canonical SlotValue
// forms it accepts the short ones hand-written specs and the demo helpers use: null hides; a plain string is text
// for a text slot and a library name for an image slot; a bare {"ms", "running", "dir"} object is a clock;
// {"png": base64} is a picture, stored in assets under its hash and named by it. Pictures need assets; the spec
// loader passes nil, since its images come from files.
func overlaySlotValue(layout *goavpipe.OverlayParams, name string, raw json.RawMessage,
	assets map[string][]byte) (goavpipe.SlotValue, error) {
	slot := layout.Slot(name)
	if slot == nil {
		return goavpipe.SlotValue{}, fmt.Errorf("unknown slot %q", name)
	}
	text := strings.TrimSpace(string(raw))
	switch {
	case text == "null":
		return goavpipe.HideValue(), nil
	case strings.HasPrefix(text, `"`):
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return goavpipe.SlotValue{}, err
		}
		if slot.Type == goavpipe.OverlaySlotImage {
			return goavpipe.ImageValue(s), nil
		}
		return goavpipe.TextValue(s), nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return goavpipe.SlotValue{}, err
	}
	if pic, ok := probe["png"]; ok {
		if assets == nil {
			return goavpipe.SlotValue{}, fmt.Errorf("a picture can only come with a patch")
		}
		var b64 string
		if err := json.Unmarshal(pic, &b64); err != nil {
			return goavpipe.SlotValue{}, fmt.Errorf("png must be a base64 string")
		}
		b, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return goavpipe.SlotValue{}, fmt.Errorf("png is not base64: %w", err)
		}
		sum := sha256.Sum256(b)
		hash := hex.EncodeToString(sum[:])
		assets[hash] = b
		return goavpipe.ImageHashValue(hash), nil
	}
	if _, canonical := probe["clock"]; slot.Type == goavpipe.OverlaySlotClock && !canonical {
		var c struct {
			Ms      int64  `json:"ms"`
			Running bool   `json:"running"`
			Dir     string `json:"dir"`
			Up      bool   `json:"up"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return goavpipe.SlotValue{}, err
		}
		if c.Ms < 0 {
			return goavpipe.SlotValue{}, fmt.Errorf("clock ms must not be negative")
		}
		return goavpipe.SlotValue{Clock: &goavpipe.ClockState{Ms: c.Ms, Running: c.Running, Up: c.Up || c.Dir == "up"}}, nil
	}
	var v goavpipe.SlotValue
	if err := json.Unmarshal(raw, &v); err != nil {
		return goavpipe.SlotValue{}, err
	}
	return v, nil
}

// overlaySlotValues decodes every value of a spec event or patch against the layout.
func overlaySlotValues(layout *goavpipe.OverlayParams, raw map[string]json.RawMessage,
	assets map[string][]byte) (map[string]goavpipe.SlotValue, error) {
	out := make(map[string]goavpipe.SlotValue, len(raw))
	for name, r := range raw {
		v, err := overlaySlotValue(layout, name, r, assets)
		if err != nil {
			return nil, fmt.Errorf("slot %q: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}

// loadOverlaySpec reads the spec, loads the images and resolves the font paths. The events come back separately
// from the layout: the job's EventSource serves them.
func loadOverlaySpec(path string) (*goavpipe.OverlayParams, []goavpipe.OverlayEvent, *overlaySpec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}
	var spec overlaySpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return nil, nil, nil, fmt.Errorf("overlay spec %s: %w", path, err)
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
			return nil, nil, nil, fmt.Errorf("overlay image %q: %w", name, err)
		}
		ov.Images[name] = png
	}
	for _, s := range spec.Slots {
		if s.Font != "" {
			s.Font = resolve(s.Font)
			if _, err := os.Stat(s.Font); err != nil {
				return nil, nil, nil, fmt.Errorf("overlay slot %q font: %w", s.Name, err)
			}
		}
		ov.Slots = append(ov.Slots, s)
	}
	var events []goavpipe.OverlayEvent
	for i, e := range spec.Events {
		ev, err := overlaySpecEventToEvent(ov, e, int64(i)+1)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("overlay event %d: %w", i, err)
		}
		if err := ov.ValidateEvent(&ev); err != nil {
			return nil, nil, nil, fmt.Errorf("overlay event %d: %w", i, err)
		}
		if i > 0 {
			prev := events[i-1]
			if ev.Seq <= prev.Seq || ev.Pts < prev.Pts {
				return nil, nil, nil, fmt.Errorf("overlay event %d: seq and pts must increase", i)
			}
		}
		events = append(events, ev)
	}
	return ov, events, &spec, nil
}

func overlaySpecEventToEvent(layout *goavpipe.OverlayParams, e overlaySpecEvent, defaultSeq int64) (goavpipe.OverlayEvent, error) {
	ev := goavpipe.OverlayEvent{TimedEvent: goavpipe.TimedEvent{Seq: e.Seq, Kind: goavpipe.KindOverlay}, Hidden: e.Hidden}
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
	raw := e.Slots
	if raw == nil {
		raw = e.Data
	}
	slots, err := overlaySlotValues(layout, raw, nil)
	if err != nil {
		return ev, err
	}
	ev.Slots = slots
	return ev, nil
}

// overlayTimed is the events as the EventSource hands them out.
func overlayTimed(events []goavpipe.OverlayEvent) []goavpipe.Timed {
	out := make([]goavpipe.Timed, len(events))
	for i := range events {
		out[i] = events[i]
	}
	return out
}

// overlayPatch is one line of --overlay-events: a partial state merged into the last one. A slot value takes the
// SlotValue forms, or {"png": "<base64>"} for a picture delivered with the patch, which the stamper stores as an
// asset and names by hash.
type overlayPatch struct {
	Hidden *bool                      `json:"hidden,omitempty"`
	Slots  map[string]json.RawMessage `json:"slots"`
	Data   map[string]json.RawMessage `json:"data"` // The older spelling of slots
}

func (p overlayPatch) values() map[string]json.RawMessage {
	if p.Slots != nil {
		return p.Slots
	}
	return p.Data
}

// overlayStamper stands in for the recorder: it reads patches as they arrive, stamps each one delay past the
// through pts, and serves every job that asks. The recorder's through pts is the last written frame; here it is
// the last frame any job asked for, so the jobs of one run all see the same event at the same frame however fast
// each one runs. Each job gets its own EventSource from handle().
type overlayStamper struct {
	mu      sync.Mutex
	layout  *goavpipe.OverlayParams
	events  []goavpipe.OverlayEvent // every event stamped so far, sorted by pts then seq
	state   goavpipe.OverlayEvent   // the last full state, which patches merge into
	through int64                   // the events are final up to here
	delay   int64                   // ms, which is the overlay timescale
	assets  map[string][]byte       // pictures delivered with patches, by hash
	patches chan overlayPatch
	src     io.ReadCloser
	log     io.Writer
	readErr error
	done    chan struct{}
	refs    int // jobs holding a handle; the source closes with the last one
}

// newOverlayStamper starts reading JSON-line patches from src. The initial events are served as they are; the
// last one is the state patches merge into. Every stamped event is written to log as a JSON line, when set.
func newOverlayStamper(layout *goavpipe.OverlayParams, initial []goavpipe.OverlayEvent, delayMs int64,
	src io.ReadCloser, log io.Writer) *overlayStamper {
	s := &overlayStamper{
		layout:  layout,
		events:  append([]goavpipe.OverlayEvent(nil), initial...),
		delay:   delayMs,
		assets:  map[string][]byte{},
		patches: make(chan overlayPatch, 64),
		src:     src,
		log:     log,
		done:    make(chan struct{}),
	}
	if len(initial) > 0 {
		s.state = initial[len(initial)-1]
	}
	s.state.Kind = goavpipe.KindOverlay
	if s.state.Slots == nil {
		s.state.Slots = map[string]goavpipe.SlotValue{}
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
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
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
		if bad := s.unknownSlot(p); bad != "" {
			fmt.Fprintf(os.Stderr, "overlay patch refused: %q is not a slot of this layout: %s\n", bad, line)
			continue
		}
		s.patches <- p
	}
	if err := scanner.Err(); err != nil {
		s.mu.Lock()
		s.readErr = err
		s.mu.Unlock()
	}
}

// overlayCarryClock returns c advanced by elapsed ms when it is running, else c unchanged. A countdown stops at 0.
func overlayCarryClock(c goavpipe.ClockState, elapsedMs int64) goavpipe.ClockState {
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

// unknownSlot returns the first key of the patch that is not a slot, or "".
func (s *overlayStamper) unknownSlot(p overlayPatch) string {
	keys := make([]string, 0, len(p.values()))
	for k := range p.values() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s.layout.Slot(k) == nil {
			return k
		}
	}
	return ""
}

// apply merges a patch into the state and stamps it delay past the through pts. A patch that does not fit the
// layout is refused and reported, as the recorder would answer 400. Caller holds mu.
func (s *overlayStamper) apply(p overlayPatch) {
	// Patches stamped within one frame share a pts and apply in seq order: the later one is in effect from there
	ev := goavpipe.OverlayEvent{
		TimedEvent: goavpipe.TimedEvent{Seq: s.state.Seq + 1, Pts: s.through + 1 + s.delay, Kind: goavpipe.KindOverlay},
		Hidden:     s.state.Hidden,
	}
	if p.Hidden != nil {
		ev.Hidden = *p.Hidden
	}
	// A clock value means "ms at the event's pts", so a running clock carried into the new snapshot is moved
	// forward by the time between the two events; one the patch sets is taken as it is
	values := p.values()
	slots := make(map[string]goavpipe.SlotValue, len(s.state.Slots)+len(values))
	for k, v := range s.state.Slots {
		if _, patched := values[k]; !patched && v.Clock != nil {
			c := overlayCarryClock(*v.Clock, (ev.Pts-s.state.Pts)*1000/overlayTimescale)
			v = goavpipe.SlotValue{Clock: &c}
		}
		slots[k] = v
	}
	patched, err := overlaySlotValues(s.layout, values, s.assets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "overlay patch refused: %v\n", err)
		return
	}
	for k, v := range patched {
		slots[k] = v
	}
	ev.Slots = slots
	if err := s.layout.ValidateEvent(&ev); err != nil {
		fmt.Fprintf(os.Stderr, "overlay patch refused: %v\n", err)
		return
	}
	s.state = ev
	s.events = append(s.events, ev)
	if s.log != nil {
		if b, err := json.Marshal(ev); err == nil {
			_, _ = fmt.Fprintf(s.log, "%s\n", b)
		}
	}
}

// at stamps the patches that have arrived against the current through pts, advances it to pts and returns the
// latest event at or before pts. An event stamped above the through pts stays here until a job asks for a frame
// past it, as the recorder's do. It never answers not yet: in a single process the recorder is never behind the
// frames.
func (s *overlayStamper) at(pts int64) (goavpipe.Timed, goavpipe.Status, error) {
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
		return nil, goavpipe.StatusReady, s.readErr
	}
	if pts > s.through {
		s.through = pts
	}
	var latest *goavpipe.OverlayEvent
	for i := range s.events {
		if s.events[i].Pts <= pts {
			latest = &s.events[i]
		}
	}
	if latest == nil {
		return nil, goavpipe.StatusReady, nil
	}
	return *latest, goavpipe.StatusReady, nil
}

func (s *overlayStamper) asset(hash string) ([]byte, goavpipe.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.assets[hash]
	if !ok {
		return nil, goavpipe.StatusReady, fmt.Errorf("unknown asset %q", hash)
	}
	return b, goavpipe.StatusReady, nil
}

// handle gives one job its own EventSource. avpipe closes the source when the job ends; the stamper itself stops
// reading once every job has closed its handle.
func (s *overlayStamper) handle() goavpipe.EventSource {
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

func (h *overlayStamperHandle) Kinds() []string { return []string{goavpipe.KindOverlay} }

func (h *overlayStamperHandle) At(kind string, pts int64) (goavpipe.Timed, goavpipe.Status, error) {
	if kind != goavpipe.KindOverlay {
		return nil, goavpipe.StatusEnded, nil
	}
	return h.s.at(pts)
}

func (h *overlayStamperHandle) Asset(hash string) ([]byte, goavpipe.Status, error) {
	return h.s.asset(hash)
}

func (h *overlayStamperHandle) Close() error {
	h.closed.Do(func() { h.err = h.s.release() })
	return h.err
}

// venueMappingFor is the default venue mapping with the slots the layout does not have left out, so a spec with
// only a score and a clock still works with --overlay-anc.
func venueMappingFor(layout *goavpipe.OverlayParams) stamp.VenueMapping {
	m := stamp.DefaultVenueMapping()
	keep := func(name *string) {
		if layout.Slot(*name) == nil {
			*name = ""
		}
	}
	keep(&m.Home)
	keep(&m.Away)
	keep(&m.Score)
	keep(&m.Period)
	keep(&m.Clock)
	keep(&m.ShotClock)
	return m
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
