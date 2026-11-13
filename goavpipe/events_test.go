package goavpipe

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlotValueJSON(t *testing.T) {
	var ev OverlayEvent
	err := json.Unmarshal([]byte(`{"seq": 3, "pts": 1500, "kind": "overlay", "slots": {
		"score": "1 - 0",
		"home": {"text": "HOME"},
		"poss": {"image": {"name": "red"}},
		"logo": {"image": {"hash": "abc"}},
		"clock": {"clock": {"ms": 120000, "running": true}},
		"gone": null,
		"hidden": {"hide": true}
	}}`), &ev)
	require.NoError(t, err)
	assert.Equal(t, TimedEvent{Seq: 3, Pts: 1500, Kind: KindOverlay}, ev.Timing())
	assert.Equal(t, "1 - 0", *ev.Slots["score"].Text, "a plain string is text")
	assert.Equal(t, "HOME", *ev.Slots["home"].Text)
	assert.Equal(t, "red", ev.Slots["poss"].Image.Name)
	assert.Equal(t, "abc", ev.Slots["logo"].Image.Hash)
	assert.Equal(t, ClockState{Ms: 120000, Running: true}, *ev.Slots["clock"].Clock)
	assert.True(t, ev.Slots["gone"].Hide, "null hides")
	assert.True(t, ev.Slots["hidden"].Hide)

	// The canonical form reads back the same
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	var again OverlayEvent
	require.NoError(t, json.Unmarshal(b, &again))
	assert.Equal(t, ev, again)

	for name, bad := range map[string]string{
		"two fields":    `{"text": "a", "hide": true}`,
		"none":          `{}`,
		"image neither": `{"image": {}}`,
		"image both":    `{"image": {"name": "a", "hash": "b"}}`,
		"negative ms":   `{"clock": {"ms": -1}}`,
	} {
		var v SlotValue
		assert.Error(t, json.Unmarshal([]byte(bad), &v), name)
	}
}

func TestValidateEvent(t *testing.T) {
	p := &OverlayParams{
		Slots: []OverlaySlot{
			{Name: "score", Type: OverlaySlotText},
			{Name: "poss", Type: OverlaySlotImage},
			{Name: "clock", Type: OverlaySlotClock},
		},
		Images: map[string][]byte{"red": {1}},
	}
	ok := OverlayEvent{Slots: map[string]SlotValue{
		"score": TextValue("1 - 0"), "poss": ImageValue("red"), "clock": ClockValue(1000, true)}}
	require.NoError(t, p.ValidateEvent(&ok))
	ok.Slots["poss"] = ImageHashValue("deadbeef")
	require.NoError(t, p.ValidateEvent(&ok), "a hash is not checked against the library")
	ok.Slots["score"] = HideValue()
	require.NoError(t, p.ValidateEvent(&ok), "any slot can be hidden")

	x := "x"
	for name, slots := range map[string]map[string]SlotValue{
		"unknown slot":   {"away": TextValue("x")},
		"text on image":  {"poss": TextValue("red")},
		"image on text":  {"score": ImageValue("red")},
		"clock on text":  {"score": ClockValue(0, false)},
		"missing image":  {"poss": ImageValue("blue")},
		"empty value":    {"score": {}},
		"two fields set": {"score": {Text: &x, Hide: true}},
	} {
		e := OverlayEvent{Slots: slots}
		assert.Error(t, p.ValidateEvent(&e), name)
	}
	wrongKind := OverlayEvent{TimedEvent: TimedEvent{Kind: KindVerticalCrop}}
	assert.Error(t, p.ValidateEvent(&wrongKind))
}

func TestStaticSource(t *testing.T) {
	ev := func(seq, pts int64, score string) OverlayEvent {
		return OverlayEvent{TimedEvent: TimedEvent{Seq: seq, Pts: pts, Kind: KindOverlay},
			Slots: map[string]SlotValue{"score": TextValue(score)}}
	}
	// Given out of order, with a tie at 2000 where the higher seq wins
	s := StaticSource([]Timed{ev(3, 2000, "c"), ev(1, 0, "a"), ev(2, 2000, "b"),
		CropEvent{TimedEvent: TimedEvent{Seq: 1, Pts: 500, Kind: KindVerticalCrop}, Center: 5000}},
		map[string][]byte{"h1": {1, 2, 3}}, KindOverlay, "other")
	assert.Equal(t, []string{"other", KindOverlay, KindVerticalCrop}, s.Kinds(), "sorted")

	at := func(pts int64) *OverlayEvent {
		e, status, err := s.At(KindOverlay, pts)
		require.NoError(t, err)
		require.Equal(t, StatusReady, status)
		if e == nil {
			return nil
		}
		oe := e.(OverlayEvent)
		return &oe
	}
	assert.Nil(t, at(-1), "nothing applies before the first event")
	assert.Equal(t, int64(1), at(0).Seq)
	assert.Equal(t, int64(1), at(1999).Seq)
	assert.Equal(t, int64(3), at(2000).Seq, "the tie goes to the later seq")
	assert.Equal(t, int64(3), at(1<<40).Seq, "the last event applies to the end")

	e, status, err := s.At(KindVerticalCrop, 600)
	require.NoError(t, err)
	assert.Equal(t, StatusReady, status)
	assert.Equal(t, int32(5000), e.(CropEvent).Center)
	e, _, err = s.At("other", 600)
	require.NoError(t, err)
	assert.Nil(t, e)

	b, status, err := s.Asset("h1")
	require.NoError(t, err)
	assert.Equal(t, StatusReady, status)
	assert.Equal(t, []byte{1, 2, 3}, b)
	_, _, err = s.Asset("h2")
	assert.Error(t, err)

	require.NoError(t, s.Close())
	_, _, err = s.At(KindOverlay, 0)
	assert.Error(t, err, "closed")
}
