package stamp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/eluv-io/avpipe/goavpipe"
)

// Bindings turn Keys into the slot values of an overlay event: the bindings of a layout's slots (see
// goavpipe.OverlayBinding), checked against the feed that publishes the keys. A slot that is not bound is not in
// the events, so it keeps what the layout gives it, an image slot its default picture.
type Bindings struct {
	// Render is called once per distinct picture of a template binding, with the hash that names the picture and
	// the keys it shows (strings, numbers and bools; unset keys are left out), from the stamper's goroutine. It
	// must not block for long. The picture arrives later through Log.PutAsset(hash, png), and the job waits for
	// it: a log with an asset fetcher answers not yet for a hash it does not hold, so set one, a no-op will do.
	Render func(hash string, state map[string]any)
	// ToleranceMs: a running clock within this of what the previous event predicts makes no new event. 0 = 50.
	ToleranceMs int64

	slots     []boundSlot
	mu        sync.Mutex
	requested map[string]bool
}

type boundSlot struct {
	name string
	typ  string // goavpipe.OverlaySlotText, OverlaySlotClock or OverlaySlotImage
	b    goavpipe.OverlayBinding
	text []textPart // b.Text, parsed
}

// textPart is a piece of a Text binding: a literal, or a key to substitute.
type textPart struct {
	lit string
	key string
}

// NewBindings makes the bindings of layout for feed. A slot's binding is its From. Without one it is named's
// entry for the slot's name. named nil stands for the feed's defaults: a text or clock slot they do not cover
// then binds to the key of its own name when the feed publishes one, and an image slot is left unbound. An entry
// of named for a slot the layout does not have is left out. A binding that does not fit its slot's type, or
// names a key the feed does not publish, is an error, as is an image name the layout's library does not hold.
//
// With layout nil the slots are named's (or the feed's defaults): a text slot for a Key or Text binding and a
// clock slot for a Timer.
func NewBindings(feed Feed, layout *goavpipe.OverlayParams, named map[string]goavpipe.OverlayBinding) (*Bindings, error) {
	b := &Bindings{requested: map[string]bool{}}
	kinds := feed.KeyKinds()
	byName := named == nil
	if named == nil {
		named = feed.DefaultBindings()
	}
	add := func(name, typ string, bind goavpipe.OverlayBinding) error {
		s := boundSlot{name: name, typ: typ, b: bind}
		if err := s.check(kinds, layout); err != nil {
			return fmt.Errorf("stamp: slot %q: %w", name, err)
		}
		b.slots = append(b.slots, s)
		return nil
	}
	if layout == nil {
		names := make([]string, 0, len(named))
		for name := range named {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			typ := goavpipe.OverlaySlotText
			if named[name].Timer != "" {
				typ = goavpipe.OverlaySlotClock
			}
			if err := add(name, typ, named[name]); err != nil {
				return nil, err
			}
		}
		return b, nil
	}
	for _, s := range layout.Slots {
		bind, bound := named[s.Name]
		switch {
		case s.From != nil:
			bind = *s.From
		case byName && s.Type == goavpipe.OverlaySlotImage:
			continue // a picture is bound only when its slot says so: most are fixed parts of the graphic
		case bound:
		case byName && kinds[s.Name] == KeyTimer:
			bind = goavpipe.OverlayBinding{Timer: s.Name}
		case byName && kinds[s.Name] != 0:
			bind = goavpipe.OverlayBinding{Key: s.Name}
		default:
			continue
		}
		if err := add(s.Name, s.Type, bind); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// check validates the binding against the slot's type, the feed's keys and the layout's image library, and
// parses a Text binding.
func (s *boundSlot) check(kinds map[string]KeyKind, layout *goavpipe.OverlayParams) error {
	template := s.b.Renderer == goavpipe.OverlayRendererTemplate
	if s.b.Renderer != "" && !template {
		return fmt.Errorf("unknown renderer %q", s.b.Renderer)
	}
	forms := 0
	for _, set := range []bool{s.b.Key != "", s.b.Text != "", s.b.Timer != "", template} {
		if set {
			forms++
		}
	}
	if forms != 1 {
		return fmt.Errorf("a binding is one of key, text, timer or renderer %q", goavpipe.OverlayRendererTemplate)
	}
	value := func(key string) error {
		switch kinds[key] {
		case 0:
			return fmt.Errorf("the feed does not publish key %q", key)
		case KeyTimer:
			return fmt.Errorf("key %q is a timer: it goes to a clock slot", key)
		}
		return nil
	}
	switch {
	case s.typ == goavpipe.OverlaySlotText && s.b.Key != "":
		return value(s.b.Key)
	case s.typ == goavpipe.OverlaySlotText && s.b.Text != "":
		parts, err := parseText(s.b.Text)
		if err != nil {
			return err
		}
		for _, p := range parts {
			if p.key != "" {
				if err := value(p.key); err != nil {
					return err
				}
			}
		}
		s.text = parts
		return nil
	case s.typ == goavpipe.OverlaySlotClock && s.b.Timer != "":
		switch kinds[s.b.Timer] {
		case 0:
			return fmt.Errorf("the feed does not publish key %q", s.b.Timer)
		case KeyTimer:
			return nil
		}
		return fmt.Errorf("key %q is not a timer", s.b.Timer)
	case s.typ == goavpipe.OverlaySlotImage && s.b.Key != "":
		if kinds[s.b.Key] != KeyString {
			if kinds[s.b.Key] == 0 {
				return fmt.Errorf("the feed does not publish key %q", s.b.Key)
			}
			return fmt.Errorf("key %q does not name an image: it is not a string", s.b.Key)
		}
		for v, img := range s.b.Images {
			if layout != nil && len(layout.Images[img]) == 0 {
				return fmt.Errorf("image %q for %q=%q is not in the layout's library", img, s.b.Key, v)
			}
		}
		return nil
	case s.typ == goavpipe.OverlaySlotImage && template:
		if len(s.b.Keys) == 0 {
			return fmt.Errorf("a template binding lists the keys the picture shows")
		}
		for _, key := range s.b.Keys {
			if err := value(key); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("the binding does not fit a %s slot", s.typ)
}

// parseText splits a Text binding into literals and {key} placeholders. There is no escape for a brace.
func parseText(text string) ([]textPart, error) {
	var parts []textPart
	for text != "" {
		open := strings.IndexAny(text, "{}")
		if open < 0 {
			parts = append(parts, textPart{lit: text})
			break
		}
		if text[open] == '}' {
			return nil, fmt.Errorf("text %q: } without {", text)
		}
		if open > 0 {
			parts = append(parts, textPart{lit: text[:open]})
		}
		end := strings.IndexAny(text[open+1:], "{}")
		if end <= 0 || text[open+1+end] != '}' {
			return nil, fmt.Errorf("text %q: a placeholder is {key}", text)
		}
		parts = append(parts, textPart{key: text[open+1 : open+1+end]})
		text = text[open+end+2:]
	}
	return parts, nil
}

// keyText is a key's value as a text slot shows it.
func keyText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	}
	return fmt.Sprint(v)
}

// Slots is the value of every bound slot for keys at pts: a running timer is carried from its sample to pts, in
// timescale units. A slot with nothing to show is hidden.
func (b *Bindings) Slots(keys Keys, pts, timescale int64) map[string]goavpipe.SlotValue {
	out := make(map[string]goavpipe.SlotValue, len(b.slots))
	for i := range b.slots {
		s := &b.slots[i]
		out[s.name] = b.value(s, keys, pts, timescale)
	}
	return out
}

func (b *Bindings) value(s *boundSlot, keys Keys, pts, timescale int64) goavpipe.SlotValue {
	switch {
	case s.b.Timer != "":
		t, ok := keys[s.b.Timer].(Timer)
		if !ok {
			return goavpipe.HideValue()
		}
		c := carryClock(t.ClockState, (pts-t.Pts)*1000/timescale)
		return goavpipe.SlotValue{Clock: &c}
	case s.b.Renderer == goavpipe.OverlayRendererTemplate:
		state := map[string]any{}
		for _, key := range s.b.Keys {
			if v, ok := keys[key]; ok {
				state[key] = v
			}
		}
		if len(state) == 0 {
			return goavpipe.HideValue()
		}
		hash := StateHash(state)
		b.mu.Lock()
		first := !b.requested[hash]
		b.requested[hash] = true
		b.mu.Unlock()
		if first && b.Render != nil {
			b.Render(hash, state)
		}
		return goavpipe.ImageHashValue(hash)
	case s.typ == goavpipe.OverlaySlotImage:
		name, ok := keys[s.b.Key].(string)
		if ok && s.b.Images != nil {
			name, ok = s.b.Images[name]
		}
		if !ok || name == "" {
			return goavpipe.HideValue()
		}
		return goavpipe.ImageValue(name)
	case s.b.Key != "":
		v, ok := keys[s.b.Key]
		if !ok {
			return goavpipe.HideValue()
		}
		return goavpipe.TextValue(keyText(v))
	}
	var sb strings.Builder
	set := false
	for _, p := range s.text {
		if p.key == "" {
			sb.WriteString(p.lit)
		} else if v, ok := keys[p.key]; ok {
			set = true
			sb.WriteString(keyText(v))
		}
	}
	if !set {
		return goavpipe.HideValue()
	}
	return goavpipe.TextValue(sb.String())
}

// StateHash names the picture of a template binding: the hash of the keys it shows, as JSON, so the same picture
// is drawn and fetched once.
func StateHash(state map[string]any) string {
	j, _ := json.Marshal(state)
	sum := sha256.Sum256(j)
	return hex.EncodeToString(sum[:])
}

// ClockSlots lists the slots bound to a timer, in layout order. They get the tolerance rule.
func (b *Bindings) ClockSlots() []string {
	var slots []string
	for _, s := range b.slots {
		if s.b.Timer != "" {
			slots = append(slots, s.name)
		}
	}
	return slots
}

// Rendered reports whether a slot is bound to a template, so the stamper's owner needs a renderer.
func (b *Bindings) Rendered() bool {
	for _, s := range b.slots {
		if s.b.Renderer == goavpipe.OverlayRendererTemplate {
			return true
		}
	}
	return false
}

// SlotNames lists the bound slots, in layout order.
func (b *Bindings) SlotNames() []string {
	names := make([]string, 0, len(b.slots))
	for _, s := range b.slots {
		names = append(names, s.name)
	}
	return names
}

func (b *Bindings) tolerance() int64 {
	if b.ToleranceMs == 0 {
		return 50
	}
	return b.ToleranceMs
}
