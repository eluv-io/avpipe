package transport

import (
	"time"

	"github.com/eluv-io/errors-go"
)

// ErrNotRtp is the cause of the error an RTP source returns once its datagrams have not been RTP for too long. A plain
// MPEG-TS sender on an rtp:// input produces it. Reconnecting does not help, so a reader treats it as terminal.
var ErrNotRtp = errors.Str("source does not send RTP")

// maxConsecutiveNonRtp is how many datagrams in a row may fail to parse as RTP version 2 before the source is taken to
// not send RTP at all. At 7 TS packets per datagram, it is about a second of a 10 Mbps stream.
const maxConsecutiveNonRtp = 1000

// tsSyncByte is the first byte of an MPEG-TS packet. As the first byte of an RTP header it reads as version 1.
const tsSyncByte = 0x47

// rtpGuard counts the consecutive datagrams of an RTP source that are not RTP. A single one is dropped, so a stray
// datagram does not end the connection. A run of them fails the source. Its zero value is ready to use.
type rtpGuard struct {
	max         int // the number of consecutive datagrams that fails the source, maxConsecutiveNonRtp if 0
	consecutive int
}

// accept records a datagram that is RTP.
func (g *rtpGuard) accept() {
	g.consecutive = 0
}

// reject records a datagram that is not RTP for cause. It returns nil while the datagram is to be dropped, and an error
// with ErrNotRtp as its cause once the run of such datagrams is long enough to fail the source.
func (g *rtpGuard) reject(datagram []byte, cause error) error {
	g.consecutive++
	plainTs := len(datagram) > 0 && datagram[0] == tsSyncByte
	limit := g.max
	if limit == 0 {
		limit = maxConsecutiveNonRtp
	}
	if g.consecutive < limit {
		log.Throttle("not-rtp", 10*time.Second).Warn("dropping datagram that is not RTP", cause,
			"consecutive", g.consecutive, "plain_mpegts", plainTs)
		return nil
	}
	return errors.E("rtpGuard", errors.K.Invalid, ErrNotRtp,
		"reason", "consecutive datagrams are not RTP", "consecutive", g.consecutive, "plain_mpegts", plainTs,
		"last_error", cause)
}
