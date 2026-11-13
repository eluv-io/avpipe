package stamp

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boardLayout is a layout drawn as a rendered board: the board's picture and the two clocks in its holes.
func boardLayout(t *testing.T) *goavpipe.OverlayParams {
	t.Helper()
	layout, err := WithBoard(&goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{
		{Name: "board", Type: goavpipe.OverlaySlotImage},
		{Name: "clock", Type: goavpipe.OverlaySlotClock},
		{Name: "shot", Type: goavpipe.OverlaySlotClock},
	}})
	require.NoError(t, err)
	return layout
}

// TestANCRenderedBoard: the board is one image by hash, rendered once per state, with the clocks beside it; a
// job waits for the picture until the renderer has put it in the log.
func TestANCRenderedBoard(t *testing.T) {
	log := NewLog()
	log.SetAssetFetcher(func(string) {}) // pictures arrive from the renderer, not from a fetch
	var mu sync.Mutex
	rendered := map[string]map[string]any{}
	feed := NewVenueFeed()
	bindings, err := NewBindings(feed, boardLayout(t), nil)
	require.NoError(t, err)
	bindings.Render = func(hash string, state map[string]any) {
		mu.Lock()
		rendered[hash] = state
		mu.Unlock()
	}
	a := NewANC(log, 1000, feed, bindings)
	f := &feeder{a: a, pts: 1800000000}
	src := log.Source(0)
	seq := 0
	next := func(id string, fields ...string) []byte { seq++; return venueMessage(seq, id, fields...) }

	f.frame(ancFrame(next("17", "Home", "Visitor", "HOM", "VIS")...))
	f.frame(ancFrame(next("4", "3", "1", "1st")...))
	f.frame(ancFrame(next("30", "12:00", "", "", "0", "0", "1")...))
	events := log.Events(goavpipe.KindOverlay)
	require.Len(t, events, 3)
	ev := events[2].(goavpipe.OverlayEvent)
	require.NotNil(t, ev.Slots["board"].Image)
	hash := ev.Slots["board"].Image.Hash
	assert.Len(t, hash, 64)
	assert.Equal(t, hash, events[1].(goavpipe.OverlayEvent).Slots["board"].Image.Hash, "the clock did not change the board")
	assert.Equal(t, goavpipe.ClockState{Ms: 720000}, *ev.Slots["clock"].Clock)
	mu.Lock()
	assert.Len(t, rendered, 2, "one render per board state: teams, then teams with the period")
	assert.Equal(t, map[string]any{"home": "HOM", "away": "VIS", "score_home": 0, "score_away": 0, "period": "1st",
		"fouls_home": 0, "fouls_away": 0, "bonus_home": 0, "bonus_away": 0, "to_home": 0, "to_away": 0}, rendered[hash],
		"the board's keys that are set: no possession yet")
	mu.Unlock()
	assert.True(t, ev.Slots["shot"].Hide, "no shot clock yet")

	// The shot clock is a clock slot of its own: a sample changes it, the board is not re-rendered. It holds at
	// the value it was set to whatever the running flag says
	f.frame(ancFrame(next("31", "24", "0", "0", "1")...))
	ev = log.Latest(goavpipe.KindOverlay).(goavpipe.OverlayEvent)
	assert.Equal(t, goavpipe.ClockState{Ms: 24000}, *ev.Slots["shot"].Clock)
	assert.Equal(t, hash, ev.Slots["board"].Image.Hash)
	mu.Lock()
	assert.Len(t, rendered, 2)
	mu.Unlock()

	// The picture is not there yet: a job asking for it waits; then the renderer delivers it
	_, status, err := src.Asset(hash)
	require.NoError(t, err)
	assert.Equal(t, goavpipe.StatusNotYet, status)
	log.PutAsset(hash, []byte("png"))
	b, status, err := src.Asset(hash)
	require.NoError(t, err)
	assert.Equal(t, goavpipe.StatusReady, status)
	assert.Equal(t, []byte("png"), b)

	// A score change is a new board; a repeated state is not rendered again; a key the board does not show
	// (the time of day) changes nothing
	f.frame(ancFrame(next("1", "2", "0")...))
	f.frame(ancFrame(next("1", "2", "0")...))
	f.frame(ancFrame(next("33", "7:15 PM", "19:15:40")...))
	mu.Lock()
	assert.Len(t, rendered, 3)
	mu.Unlock()
	last := log.Latest(goavpipe.KindOverlay).(goavpipe.OverlayEvent)
	assert.NotEqual(t, hash, last.Slots["board"].Image.Hash)
	assert.Equal(t, []string{"clock", "shot"}, bindings.ClockSlots())
	assert.True(t, bindings.Rendered())

	// Nothing known: no board
	empty, err := NewBindings(NewVenueFeed(), boardLayout(t), nil)
	require.NoError(t, err)
	assert.True(t, empty.Slots(Keys{}, 0, 1000)["board"].Hide)
}

// TestRenderClient: a request is one JSON line with the hash and the state; the renderer's answers come back
// through deliver, a picture, an error it reported, and a line that is not an answer.
func TestRenderClient(t *testing.T) {
	dir := t.TempDir()
	reqPath, resPath := filepath.Join(dir, "req.fifo"), filepath.Join(dir, "res.fifo")
	require.NoError(t, syscall.Mkfifo(reqPath, 0o600))
	require.NoError(t, syscall.Mkfifo(resPath, 0o600))

	type answer struct {
		hash string
		png  []byte
		err  error
	}
	answers := make(chan answer, 4)
	c, err := OpenRenderClient(reqPath, resPath, func(hash string, png []byte, err error) {
		answers <- answer{hash, png, err}
	})
	require.NoError(t, err)
	defer c.Close()

	// The renderer's side: read the request, answer it
	req, err := os.OpenFile(reqPath, os.O_RDWR, 0)
	require.NoError(t, err)
	defer req.Close()
	res, err := os.OpenFile(resPath, os.O_RDWR, 0)
	require.NoError(t, err)
	defer res.Close()

	state := map[string]any{"home": "HOM", "away": "VIS", "score_home": 2, "score_away": 0}
	hash := StateHash(state)
	require.NoError(t, c.Render(hash, state))
	line, err := bufio.NewReader(req).ReadBytes('\n')
	require.NoError(t, err)
	var got struct {
		Hash  string          `json:"hash"`
		State json.RawMessage `json:"state"`
	}
	require.NoError(t, json.Unmarshal(line, &got))
	assert.Equal(t, hash, got.Hash)
	assert.JSONEq(t, `{"home":"HOM","away":"VIS","score_home":2,"score_away":0}`, string(got.State))

	fmt.Fprintf(res, "{\"hash\":%q,\"png\":%q}\n", got.Hash, base64.StdEncoding.EncodeToString([]byte("png bytes")))
	fmt.Fprintf(res, "{\"hash\":\"abc\",\"error\":\"template threw\"}\n")
	fmt.Fprintf(res, "not json\n")
	a := <-answers
	assert.Equal(t, answer{hash: hash, png: []byte("png bytes")}, a)
	a = <-answers
	assert.Equal(t, "abc", a.hash)
	assert.ErrorContains(t, a.err, "template threw")
	a = <-answers
	assert.Equal(t, "", a.hash)
	assert.Error(t, a.err)
}

// TestWithBoard: the short form of a rendered board puts the template binding on the board's slot.
func TestWithBoard(t *testing.T) {
	layout := &goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{
		{Name: "bg", Type: goavpipe.OverlaySlotImage, Default: "bg"},
		{Name: "panel", Type: goavpipe.OverlaySlotImage},
		{Name: "clock", Type: goavpipe.OverlaySlotClock},
	}}
	board := &goavpipe.OverlayBinding{Renderer: goavpipe.OverlayRendererTemplate, Keys: BoardKeys}
	got, err := WithBoard(layout)
	require.NoError(t, err)
	assert.Equal(t, board, got.Slots[1].From, "the first image slot without a default")
	assert.Nil(t, got.Slots[0].From)
	assert.Nil(t, layout.Slots[1].From, "the layout given is not modified")
	b, err := NewBindings(NewVenueFeed(), got, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"panel", "clock"}, b.SlotNames())

	layout.Slots = append(layout.Slots, goavpipe.OverlaySlot{Name: "board", Type: goavpipe.OverlaySlotImage})
	got, err = WithBoard(layout)
	require.NoError(t, err)
	assert.Equal(t, board, got.Slots[3].From, "a slot named board wins")
	assert.Nil(t, got.Slots[1].From)

	// A layout that binds a template itself is taken as it is
	own := &goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{
		{Name: "panel", Type: goavpipe.OverlaySlotImage},
		{Name: "lower", Type: goavpipe.OverlaySlotImage, From: &goavpipe.OverlayBinding{
			Renderer: goavpipe.OverlayRendererTemplate, Keys: []string{"home", "away"}}},
	}}
	got, err = WithBoard(own)
	require.NoError(t, err)
	assert.Same(t, own, got)

	_, err = WithBoard(&goavpipe.OverlayParams{Slots: layout.Slots[:1]})
	assert.Error(t, err, "no image slot for the board")
}
