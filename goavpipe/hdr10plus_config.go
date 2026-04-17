package goavpipe

// #cgo LDFLAGS: -L${SRCDIR}/../lib -lavpipe
// #include <stdint.h>
// #include <stdlib.h>
// int avpipe_hdr10plus_set_tolerance(long long tolerance_pts);
// int avpipe_hdr10plus_set_ttl(int ttl_seconds);
// int avpipe_hdr10plus_set_capacity(int max_entries);
import "C"

// SetHdr10PlusTolerance sets nearest-PTS tolerance in PTS units.
func SetHdr10PlusTolerance(tolerancePts int64) error {
	rc := C.avpipe_hdr10plus_set_tolerance(C.longlong(tolerancePts))
	if rc != 0 {
		return fmtError("avpipe_hdr10plus_set_tolerance failed")
	}
	return nil
}

// SetHdr10PlusTTL sets time-to-live in seconds for HDR10+ metadata entries.
func SetHdr10PlusTTL(ttlSeconds int) error {
	rc := C.avpipe_hdr10plus_set_ttl(C.int(ttlSeconds))
	if rc != 0 {
		return fmtError("avpipe_hdr10plus_set_ttl failed")
	}
	return nil
}

// SetHdr10PlusCapacity sets maximum number of entries to keep in store.
func SetHdr10PlusCapacity(maxEntries int) error {
	rc := C.avpipe_hdr10plus_set_capacity(C.int(maxEntries))
	if rc != 0 {
		return fmtError("avpipe_hdr10plus_set_capacity failed")
	}
	return nil
}
