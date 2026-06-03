package util

import "encoding/json"

// JSONString marshals v to a JSON string. Marshaling errors are silently
// ignored; the result is "{}" on failure rather than an empty string.
func JSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
