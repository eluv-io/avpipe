package avpipe

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe/goavpipe"
)

// TestOverlayLayoutPlacement checks the layout rules of an image slot's aspect and of a text or clock slot placed
// in an image slot.
func TestOverlayLayoutPlacement(t *testing.T) {
	board := goavpipe.OverlaySlot{Name: "board", Type: goavpipe.OverlaySlotImage, X: 0.5, Y: 0.04, Size: 0.9,
		H: 0.2223, Aspect: 3.2, Align: "center"}
	clock := goavpipe.OverlaySlot{Name: "clock", Type: goavpipe.OverlaySlotClock, X: 0.5, Y: 0.7, Size: 0.22,
		Font: "Arial:bold", In: "board"}
	check := func(slots ...goavpipe.OverlaySlot) error {
		_, err := overlayCheckLayout(&goavpipe.OverlayParams{Slots: slots})
		return err
	}
	with := func(s goavpipe.OverlaySlot, change func(s *goavpipe.OverlaySlot)) goavpipe.OverlaySlot {
		change(&s)
		return s
	}

	require.NoError(t, check(board, clock))
	require.NoError(t, check(clock, board), "the image slot may come after the slot placed in it")
	require.NoError(t, check(with(board, func(s *goavpipe.OverlaySlot) { s.H = 0 })), "an aspect stands in for h")

	for name, slots := range map[string][]goavpipe.OverlaySlot{
		"neither h nor aspect": {with(board, func(s *goavpipe.OverlaySlot) { s.H, s.Aspect = 0, 0 })},
		"negative aspect":      {with(board, func(s *goavpipe.OverlaySlot) { s.Aspect = -1 })},
		"aspect out of range":  {with(board, func(s *goavpipe.OverlaySlot) { s.Aspect = 1000 })},
		"aspect on a clock":    {board, with(clock, func(s *goavpipe.OverlaySlot) { s.Aspect = 2 })},
		"in an unknown slot":   {board, with(clock, func(s *goavpipe.OverlaySlot) { s.In = "nothing" })},
		"in itself":            {board, with(clock, func(s *goavpipe.OverlaySlot) { s.In = "clock" })},
		"in a text slot": {board, clock, with(clock, func(s *goavpipe.OverlaySlot) {
			s.Name, s.In = "shot", "clock"
		})},
		"image in an image": {board, with(board, func(s *goavpipe.OverlaySlot) { s.Name, s.In = "logo", "board" })},
	} {
		require.Error(t, check(slots...), name)
	}
}
