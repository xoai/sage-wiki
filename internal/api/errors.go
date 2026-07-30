// Package api implements the /v1 REST facade: a thin transport over the
// existing MCP tool handlers. MCP is the canonical contract — handlers here
// translate HTTP to tool argument maps and tool results back to JSON, and
// contain no business logic of their own.
package api

import (
	"encoding/json"
	"net/http"
)

// Fixed error code vocabulary (04 §Error model). Clients branch on code,
// never on message.
const (
	CodeInvalidArgument = "invalid_argument"
	CodeUnauthenticated = "unauthenticated"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeConflict        = "conflict"
	CodeFeatureDisabled = "feature_disabled"
	CodePayloadTooLarge = "payload_too_large"
	CodeRateLimited     = "rate_limited"
	CodeInternal        = "internal"
	CodeUnavailable     = "unavailable"
)

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// writeJSON emits a JSON response with the facade's single content type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Encode errors on a response already committed are unrecoverable; the
	// status line is out. Encoding a marshaled map never fails in practice.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emits the single error envelope for all non-2xx /v1 responses.
func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, errorEnvelope{Error: apiError{Code: code, Message: message, Details: details}})
}

// WriteError is the exported envelope writer, used by the web server's
// security middleware so its 401/403 on /v1 paths share this envelope.
func WriteError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeError(w, status, code, message, details)
}
