package util

import (
	"encoding/json"

	log "github.com/eluv-io/log-go"
)

// JSONString marshals v to a JSON string. Returns "{}" and logs an error on
// failure.
func JSONString(v any) string {
	const op = "util.JSONString"
	b, err := json.Marshal(v)
	if err != nil {
		log.Error("marshal failed", "error", err, "op", op)
		return "{}"
	}
	return string(b)
}
