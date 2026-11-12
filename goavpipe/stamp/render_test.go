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

	"github.com/eluv-io/avpipe/broadcastproto/anc/venueclock"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoardState(t *testing.T) {
	st := venueclock.State{Abbrevs: [2]string{"HOM", "VIS"}, Score: [2]int{12, 9}, PeriodName: "2nd",
		Possession: venueclock.PossessionVisitor, Fouls: [2]int{3, 5}, Bonus: [2]int{0, 1},
		Timeouts: venueclock.Timeouts{FullLeft: [2]int{6, 5}}}
	b := BoardStateOf(&st, "")
	assert.Equal(t, BoardState{Home: "HOM", Away: "VIS", Score: "12 - 9", Period: "2nd", Poss: "away",
		FoulsHome: 3, FoulsAway: 5, BonusAway: 1, TOHome: 6, TOAway: 5, Teams: true}, b)
	shot, ok := ShotClock(&venueclock.State{ShotClock: venueclock.Value{Text: "4.7", Ms: 4700, Valid: true}, ShotRunning: true})
	assert.True(t, ok)
	assert.Equal(t, goavpipe.ClockState{Ms: 4700}, shot, "the reading as a held clock, whatever the running flag says")
	_, ok = ShotClock(&st)
	assert.False(t, ok, "blank shot clock")
	assert.Len(t, b.Hash(), 64)
	assert.Equal(t, b.Hash(), BoardStateOf(&st, "").Hash(), "the same board has the same hash")
	st.Score[0]++
	assert.NotEqual(t, b.Hash(), BoardStateOf(&st, "").Hash())
	assert.Equal(t, BoardState{}, BoardStateOf(&venueclock.State{}, ""), "nothing known: empty board")
}

// TestANCRenderMapping: the board is one image by hash, rendered once per state, with the clock beside it; a
// job waits for the picture until the renderer has put it in the log.
func TestANCRenderMapping(t *testing.T) {
	log := NewLog()
	log.SetAssetFetcher(func(string) {}) // pictures arrive from the renderer, not from a fetch
	var mu sync.Mutex
	rendered := map[string]BoardState{}
	m := NewRenderMapping("board", "clock", "shot", func(hash string, b BoardState) {
		mu.Lock()
		rendered[hash] = b
		mu.Unlock()
	})
	a := NewANC(log, 1000, m)
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
	assert.Equal(t, hash, events[1].(goavpipe.OverlayEvent).Slots["board"].Image.Hash, "the clock did not change the board")
	assert.Equal(t, goavpipe.ClockState{Ms: 720000}, *ev.Slots["clock"].Clock)
	mu.Lock()
	assert.Len(t, rendered, 2, "one render per board state: teams, then teams with the period")
	assert.Equal(t, BoardState{Home: "HOM", Away: "VIS", Score: "0 - 0", Period: "1st", Teams: true}, rendered[hash])
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

	// A score change is a new board; a repeated state is not rendered again
	f.frame(ancFrame(next("1", "2", "0")...))
	f.frame(ancFrame(next("1", "2", "0")...))
	mu.Lock()
	assert.Len(t, rendered, 3)
	mu.Unlock()
	last := log.Latest(goavpipe.KindOverlay).(goavpipe.OverlayEvent)
	assert.NotEqual(t, hash, last.Slots["board"].Image.Hash)
	assert.Equal(t, []string{"clock", "shot"}, m.ClockSlots())
	assert.Equal(t, "shot", m.ShotClockSlot())
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

	state := BoardState{Home: "HOM", Away: "VIS", Score: "2 - 0", Teams: true}
	require.NoError(t, c.Render(state.Hash(), state))
	line, err := bufio.NewReader(req).ReadBytes('\n')
	require.NoError(t, err)
	var got struct {
		Hash  string     `json:"hash"`
		State BoardState `json:"state"`
	}
	require.NoError(t, json.Unmarshal(line, &got))
	assert.Equal(t, state.Hash(), got.Hash)
	assert.Equal(t, state, got.State)

	fmt.Fprintf(res, "{\"hash\":%q,\"png\":%q}\n", got.Hash, base64.StdEncoding.EncodeToString([]byte("png bytes")))
	fmt.Fprintf(res, "{\"hash\":\"abc\",\"error\":\"template threw\"}\n")
	fmt.Fprintf(res, "not json\n")
	a := <-answers
	assert.Equal(t, answer{hash: state.Hash(), png: []byte("png bytes")}, a)
	a = <-answers
	assert.Equal(t, "abc", a.hash)
	assert.ErrorContains(t, a.err, "template threw")
	a = <-answers
	assert.Equal(t, "", a.hash)
	assert.Error(t, a.err)
}

func TestRenderMappingForLayout(t *testing.T) {
	layout := &goavpipe.OverlayParams{Slots: []goavpipe.OverlaySlot{
		{Name: "bg", Type: goavpipe.OverlaySlotImage, Default: "bg"},
		{Name: "panel", Type: goavpipe.OverlaySlotImage},
		{Name: "clock", Type: goavpipe.OverlaySlotClock},
		{Name: "shot", Type: goavpipe.OverlaySlotText},
	}}
	m, err := RenderMappingForLayout(layout, nil)
	require.NoError(t, err)
	assert.Equal(t, "panel", m.Board, "the first image slot without a default")
	assert.Equal(t, "clock", m.Clock)
	assert.Equal(t, "", m.ShotClock, "a text slot named shot is not the shot clock")

	layout.Slots = append(layout.Slots, goavpipe.OverlaySlot{Name: "board", Type: goavpipe.OverlaySlotImage})
	m, err = RenderMappingForLayout(layout, nil)
	require.NoError(t, err)
	assert.Equal(t, "board", m.Board, "a slot named board wins")

	_, err = RenderMappingForLayout(&goavpipe.OverlayParams{Slots: layout.Slots[:1]}, nil)
	assert.Error(t, err, "no image slot for the board")
}
