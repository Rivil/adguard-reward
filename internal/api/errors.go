package api

import (
	"encoding/json"
	"net/http"
)

// Error codes carried in the envelope's "error" field. The SPA keys its
// messages on these, so they are part of the contract.
const (
	CodeBadRequest         = "bad_request"
	CodeUnauthorized       = "unauthorized"
	CodeForbidden          = "forbidden"
	CodeBadCredentials     = "bad_credentials"
	CodeAdGuardUnavailable = "adguard_unavailable"
	CodeRateLimited        = "rate_limited"
	CodeNotFound           = "not_found"
	CodeConflict           = "conflict"
	CodeUnprocessable      = "unprocessable"
	// CodeReauthRequired: changing or removing a PIN needs the AdGuard
	// password and none was sent.
	CodeReauthRequired = "reauth_required"
	// CodeBadPIN: a wrong PIN on unlock; the envelope carries
	// attempts_remaining.
	CodeBadPIN = "bad_pin"
	// CodeDeviceRevoked: the device cookie names no live device (revoked,
	// expired, or the last attempt spent); only a full login helps.
	CodeDeviceRevoked = "device_revoked"
)

// errorBody is the JSON envelope every error response carries.
type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// writeError emits {"error": code, "message": message} with status.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: code, Message: message})
}

// UnauthorizedWriter is the auth.ErrorWriter main hands to auth.New so
// RequireSession's 401 uses the same envelope as every other error here.
func UnauthorizedWriter(w http.ResponseWriter, status int, code, message string) {
	writeError(w, status, code, message)
}

// writeJSON emits a 2xx body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
