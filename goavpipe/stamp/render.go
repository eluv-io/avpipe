package stamp

import (
	"fmt"

	"github.com/eluv-io/avpipe/goavpipe"
)

// BoardKeys are the keys of the venue feed a board template shows when its binding is the short form (WithBoard):
// everything of the board that changes rarely. The clocks are not among them: they tick, so the compositor draws
// them in holes the template leaves.
var BoardKeys = []string{"home", "away", "score_home", "score_away", "period", "poss",
	"fouls_home", "fouls_away", "bonus_home", "bonus_away", "to_home", "to_away"}

// WithBoard returns layout with a template binding of BoardKeys on its board slot: the image slot named board, or
// else the first image slot that has neither a default picture nor a binding. A layout in which a slot is already
// bound to a template is returned as it is, and so is one whose board slot has another binding. The layout given
// is not modified. A layout with no image slot for the board is an error.
func WithBoard(layout *goavpipe.OverlayParams) (*goavpipe.OverlayParams, error) {
	board := -1
	for i, s := range layout.Slots {
		if s.From != nil && s.From.Renderer == goavpipe.OverlayRendererTemplate {
			return layout, nil
		}
		if s.Type != goavpipe.OverlaySlotImage {
			continue
		}
		if s.Name == "board" || board < 0 && s.Default == "" && s.From == nil {
			board = i
		}
	}
	if board < 0 {
		return nil, fmt.Errorf("stamp: the layout has no image slot for the board (named board, or without a default)")
	}
	if layout.Slots[board].From != nil {
		return layout, nil
	}
	out := *layout
	out.Slots = append([]goavpipe.OverlaySlot(nil), layout.Slots...)
	out.Slots[board].From = &goavpipe.OverlayBinding{Renderer: goavpipe.OverlayRendererTemplate, Keys: BoardKeys}
	return &out, nil
}
