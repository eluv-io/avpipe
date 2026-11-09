package stamp

import (
	"sync"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/anc/venueclock"
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
	assert.Equal(t, goavpipe.ClockState{Ms: 4700, Running: true}, shot)
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

	// The shot clock is a clock slot of its own: a sample changes it, the board is not re-rendered
	f.frame(ancFrame(next("31", "24", "0", "0", "1")...))
	ev = log.Latest(goavpipe.KindOverlay).(goavpipe.OverlayEvent)
	assert.Equal(t, goavpipe.ClockState{Ms: 24000, Running: true}, *ev.Slots["shot"].Clock)
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
}
