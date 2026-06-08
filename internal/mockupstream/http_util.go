package mockupstream

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// http_util.go holds small helpers shared by every handler: JSON encoding,
// error envelopes and body reading.

func itoa(n int) string { return strconv.Itoa(n) }

// writeJSON marshals v and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// openAIError writes an OpenAI-style error envelope (also used by the sync
// image/video handlers, doc §7.2).
func openAIError(w http.ResponseWriter, status int, errType, message, code string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"type":    errType,
			"message": message,
			"code":    code,
		},
	})
}

// readBody reads and returns the full request body, capped to avoid unbounded
// memory use from a misbehaving client. Empty body is not an error.
func readBody(r *http.Request) ([]byte, error) {
	const maxBody = 32 << 20 // 32 MiB
	return io.ReadAll(io.LimitReader(r.Body, maxBody))
}

// decodeJSON parses request body into a generic map. Returns an empty map (not
// nil) on empty/invalid body so handlers can read fields without nil checks.
func decodeJSON(body []byte) map[string]any {
	m := map[string]any{}
	if len(body) == 0 {
		return m
	}
	_ = json.Unmarshal(body, &m)
	return m
}

// strField extracts a string field from a decoded JSON map, with a default.
func strField(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

// intField extracts an integer field (JSON numbers decode as float64).
func intField(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

// boolField extracts a boolean field with a default.
func boolField(m map[string]any, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}
