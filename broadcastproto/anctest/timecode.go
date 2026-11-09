package anctest

import "github.com/eluv-io/common-go/media/anc"

// TimecodeUDW returns the 16 user data words of an ST 12-2 time code packet (DID 0x60 / SDID 0x60) for the
// given time, with payload type dbb1 (0 LTC, 1 VITC field 1, 2 VITC field 2), no binary groups and no flags
// other than drop frame.
func TimecodeUDW(hours, minutes, seconds, frames int, dropFrame bool, dbb1 uint8) []byte {
	return anc.Timecode{Hours: hours, Minutes: minutes, Seconds: seconds, Frames: frames, DropFrame: dropFrame,
		DBB1: dbb1}.UDW()
}

// TimecodeAt returns the time code of frame n at an integer frame rate, counting from zero, non-drop.
func TimecodeAt(n, fps int) (hours, minutes, seconds, frames int) {
	frames = n % fps
	s := n / fps
	return s / 3600, s / 60 % 60, s % 60, frames
}
