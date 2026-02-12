package goavpipe

// #cgo LDFLAGS: -L${SRCDIR}/../lib -lavpipe
// #include <stdint.h>
// #include <stdlib.h>
// /* Forward declare to avoid pulling FFmpeg headers via avpipe.h */
// int avpipe_set_hdr10plus(long long pts, const char *json, int json_len);
// int avpipe_extract_hdr10plus_json(const char *url, char **out_json, int *out_json_len);
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

// GetHdr10PlusJson extracts HDR10+ metadata from a video file and returns as JSON.
// Decodes frames until HDR10+ dynamic metadata is found.
// Returns empty byte slice if no HDR10+ metadata found (not an error).
func GetHdr10PlusJson(url string) ([]byte, error) {
	if url == "" {
		return nil, fmtError("empty URL")
	}

	cUrl := C.CString(url)
	defer C.free(unsafe.Pointer(cUrl))

	var cJson *C.char
	var cJsonLen C.int

	rc := C.avpipe_extract_hdr10plus_json(cUrl, &cJson, &cJsonLen)
	if rc != 0 {
		// Not finding HDR10+ metadata is not necessarily an error
		// Could be a non-HDR10+ source
		return []byte{}, nil
	}

	if cJson == nil || cJsonLen <= 0 {
		return []byte{}, nil
	}

	// Copy the JSON data before freeing
	jsonData := C.GoBytes(unsafe.Pointer(cJson), cJsonLen)
	C.free(unsafe.Pointer(cJson))

	return jsonData, nil
}

// fmtError avoids importing fmt across package; simple helper
func fmtError(s string) error { return &simpleError{s} }

type simpleError struct{ s string }

func (e *simpleError) Error() string { return e.s }
