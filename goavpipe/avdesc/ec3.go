package avdesc

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eluv-io/avpipe/goavpipe/util"
)

// EC3Info holds E-AC-3-specific fields (Dolby Digital Plus / Atmos) parsed
// from the MP4 dec3 box.
type EC3Info struct {
	// ChanMap is the custom channel map bitmask from the MP4 dec3 box
	// (ETSI TS 102 366 Table E.1.4).
	ChanMap uint16 `json:"chan_map"`

	// JOC indicates Joint Object Coding (Dolby Atmos). True when the
	// flag_ec3_extension_type_a bit is set in the MP4 dec3 box extension
	// (ETSI TS 103 420).
	JOC bool `json:"joc"`

	// ComplexityIndex is the Atmos object complexity index
	// (complexity_index_type_a from ETSI TS 103 420). Only meaningful when
	// JOC is true.
	ComplexityIndex int `json:"complexity_index,omitempty"`
}

// ec3ChanMapNames lists the channel names for each bit of the EC-3 custom
// channel map, ordered from MSB (bit 15) to LSB (bit 0), per ETSI TS 102 366
// Table E.1.4.
var ec3ChanMapNames = [16]string{
	"L", "C", "R", "Ls", "Rs", "Lc/Rc", "Lrs/Rrs", "Cs",
	"Ts", "Lsd/Rsd", "Lw/Rw", "Vhl/Vhr", "Vhc", "Lts/Rts", "LFE2", "LFE",
}

// ChanMapHex returns ChanMap as an uppercase hex string; e.g. "F801".
func (e EC3Info) ChanMapHex() string {
	return fmt.Sprintf("%04X", e.ChanMap)
}

// ChanMapString returns the channel names encoded in ChanMap as a
// space-separated string; e.g. "L C R Ls Rs LFE".
func (e EC3Info) ChanMapString() string {
	var names []string
	for i, name := range ec3ChanMapNames {
		if e.ChanMap&(1<<(15-i)) != 0 {
			names = append(names, name)
		}
	}
	return strings.Join(names, " ")
}

// MarshalJSON adds an additional chan_map_hex field alongside the numeric
// chan_map.
func (e EC3Info) MarshalJSON() ([]byte, error) {
	type alias EC3Info
	return json.Marshal(&struct {
		alias
		ChanMapHex string `json:"chan_map_hex,omitempty"`
	}{
		alias:      alias(e),
		ChanMapHex: e.ChanMapHex(),
	})
}

// String returns the EC3Info as a JSON string, satisfying fmt.Stringer.
func (e EC3Info) String() string { return util.JSONString(e) }
