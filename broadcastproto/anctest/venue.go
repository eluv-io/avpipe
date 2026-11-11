package anctest

import (
	"fmt"

	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/eluv-io/avpipe/broadcastproto/anc/venueclock"
)

// VenueMessage frames one message of the venue clock feed the way the feed sends it: SOH, then the version,
// the protocol id, msgID, the sequence number seq and the data fields, each followed by '|', then the CRC as two
// hex digits and EOT. The result is the serial bytes to spread over one or more VenuePackets.
func VenueMessage(seq int, msgID string, fields ...string) []byte {
	body := "4|6|" + msgID + "|" + fmt.Sprint(seq) + "|"
	for _, f := range fields {
		body += f + "|"
	}
	return append(append([]byte{0x01}, []byte(fmt.Sprintf("%s%02X", body, venueclock.CRC([]byte(body))))...), 0x04)
}

// VenuePacket returns the ancillary data packet that carries serial, the next bytes of the venue clock feed, in
// one frame: private user data (DID 0x50 / SDID 0x01) on line 14, with the feed's header in front. A frame in
// which the feed sends nothing still carries the packet with the header alone, which VenuePacket() gives.
func VenuePacket(serial ...byte) anc.Packet {
	return anc.Packet{Line: 14, DID: 0x50, SDID: 0x01, UDW: append(append([]byte{}, venueclock.Header...), serial...)}
}
