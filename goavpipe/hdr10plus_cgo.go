package goavpipe

// #cgo LDFLAGS: -L${SRCDIR}/../lib -lavpipe
// #include <stdint.h>
// #include <stdlib.h>
// /* Forward declare to avoid pulling FFmpeg headers via avpipe.h */
// int avpipe_set_hdr10plus(long long pts, const char *json, int json_len);
import "C"
import (
	"unsafe"
)

// SetHdr10Plus sends per-frame HDR10+ JSON to the native avpipe store.
func SetHdr10Plus(pts int64, json []byte) error {
	if len(json) == 0 {
		return nil
	}
	cstr := C.CString(string(json))
	defer C.free(unsafe.Pointer(cstr))
	rc := C.avpipe_set_hdr10plus(C.longlong(pts), cstr, C.int(len(json)))
	if rc != 0 {
		return fmtError("avpipe_set_hdr10plus failed")
	}
	return nil
}

// fmtError avoids importing fmt across package; simple helper
func fmtError(s string) error { return &simpleError{s} }

type simpleError struct{ s string }

func (e *simpleError) Error() string { return e.s }
