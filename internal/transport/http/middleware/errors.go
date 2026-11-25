package middleware

import (
	"encoding/json"
	"net/http"
)

// writeJSONError keeps middleware failures in the same envelope the handlers
// use, so a client parses one error shape everywhere.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	body := map[string]map[string]string{
		"error": {"code": code, "message": message},
	}
	_ = json.NewEncoder(w).Encode(body)
}
