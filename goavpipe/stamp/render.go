package stamp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/common-go/media/anc/venueclock"
)

// BoardState is what a rendered scoreboard shows: the template's input, everything of the venue state that
// changes rarely. The clocks are not in it: they tick, so the compositor draws them in holes the template
// leaves. Its JSON keys are the ones the demo template's setState reads.
type BoardState struct {
	Home      string `json:"home,omitempty"`
	Away      string `json:"away,omitempty"`
	Score     string `json:"score,omitempty"`
	Period    string `json:"period,omitempty"`
	Poss      string `json:"poss,omitempty"`       // "home", "away" or ""
	FoulsHome int    `json:"fouls_home,omitempty"` // team fouls this period
	FoulsAway int    `json:"fouls_away,omitempty"`
	BonusHome int    `json:"bonus_home,omitempty"` // 0, 1 or 2 as the venue sends it
	BonusAway int    `json:"bonus_away,omitempty"`
	TOHome    int    `json:"to_home,omitempty"` // full timeouts left
	TOAway    int    `json:"to_away,omitempty"`
	Teams     bool   `json:"teams,omitempty"` // the teams message has been seen: the board has something to show
}

// BoardStateOf is the board for the venue state.
func BoardStateOf(st *venueclock.State, scoreFormat string) BoardState {
	b := BoardState{Home: st.Abbrevs[venueclock.Home], Away: st.Abbrevs[venueclock.Visitor], Period: st.PeriodName}
	b.Teams = b.Home != "" || b.Away != ""
	if b.Teams || st.Score != [2]int{} {
		b.Score = sprintScore(scoreFormat, st.Score[venueclock.Home], st.Score[venueclock.Visitor])
	}
	switch st.Possession {
	case venueclock.PossessionHome:
		b.Poss = "home"
	case venueclock.PossessionVisitor:
		b.Poss = "away"
	}
	b.FoulsHome, b.FoulsAway = st.Fouls[venueclock.Home], st.Fouls[venueclock.Visitor]
	b.BonusHome, b.BonusAway = st.Bonus[venueclock.Home], st.Bonus[venueclock.Visitor]
	b.TOHome, b.TOAway = st.Timeouts.FullLeft[venueclock.Home], st.Timeouts.FullLeft[venueclock.Visitor]
	return b
}

// ShotClock is the shot clock as a clock state, running as the venue's flag says. Invalid when blank.
func ShotClock(st *venueclock.State) (goavpipe.ClockState, bool) {
	if !st.ShotClock.Valid {
		return goavpipe.ClockState{}, false
	}
	return goavpipe.ClockState{Ms: int64(st.ShotClock.Ms), Running: st.ShotRunning}, true
}

// Hash names a board state: the hash of its JSON, so the same picture is rendered and fetched once.
func (b BoardState) Hash() string {
	j, _ := json.Marshal(b)
	sum := sha256.Sum256(j)
	return hex.EncodeToString(sum[:])
}

// RenderMapping shows the venue state as one rendered picture, the board, plus the game clock drawn by the
// compositor in a hole the template leaves. Every new board state is named by its hash and handed to Render
// once; the picture arrives later through Log.PutAsset, and the job waits for it (the log answers not yet for a
// hash it does not hold when it has an asset fetcher, so set one, a no-op will do). This is the template
// direction: the recorder renders, the playout jobs fetch by hash.
type RenderMapping struct {
	Board     string // the image slot the rendered board goes to
	Clock     string // the game clock slot, "" for none
	ShotClock string // the shot clock slot (format "shot"), "" for none
	// Render is called once per new board state, with its hash, from the stamper's goroutine; it must not block
	// for long. The renderer calls Log.PutAsset(hash, png) when the picture is ready.
	Render      func(hash string, state BoardState)
	ScoreFormat string // default "%d - %d"

	mu        sync.Mutex
	requested map[string]bool
}

// NewRenderMapping makes a mapping for the board and the two clock slots; either clock may be "".
func NewRenderMapping(board, clock, shot string, render func(hash string, state BoardState)) *RenderMapping {
	return &RenderMapping{Board: board, Clock: clock, ShotClock: shot, Render: render, requested: map[string]bool{}}
}

func (m *RenderMapping) Slots(st *venueclock.State) map[string]goavpipe.SlotValue {
	out := map[string]goavpipe.SlotValue{}
	board := BoardStateOf(st, m.ScoreFormat)
	if board == (BoardState{}) {
		out[m.Board] = goavpipe.HideValue()
	} else {
		hash := board.Hash()
		m.mu.Lock()
		first := !m.requested[hash]
		m.requested[hash] = true
		m.mu.Unlock()
		if first && m.Render != nil {
			m.Render(hash, board)
		}
		out[m.Board] = goavpipe.ImageHashValue(hash)
	}
	if m.Clock != "" {
		if c, ok := GameClock(st); ok {
			out[m.Clock] = goavpipe.SlotValue{Clock: &c}
		} else {
			out[m.Clock] = goavpipe.HideValue()
		}
	}
	if m.ShotClock != "" {
		if c, ok := ShotClock(st); ok {
			out[m.ShotClock] = goavpipe.SlotValue{Clock: &c}
		} else {
			out[m.ShotClock] = goavpipe.HideValue()
		}
	}
	return out
}

func (m *RenderMapping) ClockSlots() []string {
	var slots []string
	if m.Clock != "" {
		slots = append(slots, m.Clock)
	}
	if m.ShotClock != "" {
		slots = append(slots, m.ShotClock)
	}
	return slots
}
