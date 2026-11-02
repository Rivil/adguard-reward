package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

// sessionView is one row of GET /api/v1/sessions.
type sessionView struct {
	ID         int64  `json:"id"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	Current    bool   `json:"current"`
}

// handleSessionsList is GET /api/v1/sessions: the caller's sessions, oldest
// first, with the one behind this request flagged.
func (a *API) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	rows, err := a.deps.Sessions.ListByUser(r.Context(), sess.Username)
	if err != nil {
		a.log.Error("list sessions failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not list sessions")
		return
	}
	out := make([]sessionView, 0, len(rows))
	for _, s := range rows {
		out = append(out, sessionView{
			ID:         s.ID,
			CreatedAt:  s.CreatedAt.UTC().Format(time.RFC3339),
			LastSeenAt: s.LastSeenAt.UTC().Format(time.RFC3339),
			Current:    s.ID == sess.ID,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Sessions []sessionView `json:"sessions"`
	}{out})
}

// handleSessionDelete is DELETE /api/v1/sessions/{id}. A foreign or unknown
// id is 404 either way, so ids cannot be enumerated. Deleting the current
// session also clears the cookie, replacing RequireSession's refresh.
func (a *API) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such session")
		return
	}
	ok, err := a.deps.Sessions.DeleteByUser(r.Context(), sess.Username, id)
	if err != nil {
		a.log.Error("delete session failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not revoke session")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such session")
		return
	}
	if id == sess.ID {
		a.deps.Auth.Clear(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionsRevokeAll is POST /api/v1/sessions/revoke-all: every
// session of the caller except the current one.
func (a *API) handleSessionsRevokeAll(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.FromContext(r.Context())
	n, err := a.deps.Sessions.DeleteOthers(r.Context(), sess.Username, sess.ID)
	if err != nil {
		a.log.Error("revoke sessions failed", "err", err, "session", sess)
		writeError(w, http.StatusInternalServerError, "internal", "could not revoke sessions")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Revoked int `json:"revoked"`
	}{n})
}
