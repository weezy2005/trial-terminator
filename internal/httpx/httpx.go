// Package httpx holds the small response helpers every HTTP handler shares,
// so all endpoints emit the same JSON and error envelope.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// JSON writes v as a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; all that's left is to record it.
		slog.Error("failed to encode JSON response", "error", err)
	}
}

// Error writes the standard error envelope: {"error": "message"}.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}
