package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

type sessionsBody struct {
	Sessions []sessionView `json:"sessions"`
}

func (h *harness) listSessions(cookie string) (*httptest.ResponseRecorder, sessionsBody) {
	h.t.Helper()
	w := h.do(http.MethodGet, "/api/v1/sessions", nil, withCookie(cookie))
	var body sessionsBody
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			h.t.Fatalf("sessions body %q: %v", w.Body.String(), err)
		}
	}
	return w, body
}

// idOf finds the caller's current session id via the listing.
func (h *harness) idOf(cookie string) int64 {
	h.t.Helper()
	w, body := h.listSessions(cookie)
	if w.Code != http.StatusOK {
		h.t.Fatalf("list: status %d body %s", w.Code, w.Body.String())
	}
	for _, s := range body.Sessions {
		if s.Current {
			return s.ID
		}
	}
	h.t.Fatal("no current session in listing")
	return 0
}

// insertOther creates a session for a second user straight through the
// store (the fake accepts one credential) and returns its id.
func (h *harness) insertOther(hashByte byte) int64 {
	h.t.Helper()
	var hash [32]byte
	hash[0] = hashByte
	id, err := h.store.Insert(context.Background(), auth.Session{
		Username:   "other",
		TokenHash:  hash,
		CreatedAt:  t0,
		LastSeenAt: t0,
		ExpiresAt:  t0.Add(time.Hour),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) me(cookie string) int {
	h.t.Helper()
	return h.do(http.MethodGet, "/api/v1/me", nil, withCookie(cookie)).Code
}

func TestSessions_List(t *testing.T) {
	h := newHarness(t, nil, nil)
	a := h.loginOK()
	h.clock.Advance(time.Second)
	b := h.loginOK()
	otherID := h.insertOther(1)

	w, body := h.listSessions(a)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("listed %d sessions, want 2: %+v", len(body.Sessions), body.Sessions)
	}
	aID, bID := h.idOf(a), h.idOf(b)
	current := 0
	for _, s := range body.Sessions {
		if s.ID == otherID {
			t.Fatalf("other user's session %d listed", otherID)
		}
		if s.Current {
			current++
			if s.ID != aID {
				t.Fatalf("current flagged on %d, want %d", s.ID, aID)
			}
		}
		if _, err := time.Parse(time.RFC3339, s.CreatedAt); err != nil {
			t.Fatalf("created_at %q: %v", s.CreatedAt, err)
		}
		if _, err := time.Parse(time.RFC3339, s.LastSeenAt); err != nil {
			t.Fatalf("last_seen_at %q: %v", s.LastSeenAt, err)
		}
	}
	if current != 1 {
		t.Fatalf("current flagged %d times, want 1", current)
	}
	if body.Sessions[0].ID != aID || body.Sessions[1].ID != bID {
		t.Fatalf("order = [%d %d], want oldest first [%d %d]", body.Sessions[0].ID, body.Sessions[1].ID, aID, bID)
	}
}

func TestSessions_EmptyListIsArray(t *testing.T) {
	h := newHarness(t, nil, nil)
	a := h.loginOK()
	aID := h.idOf(a)
	// Revoke everything else, then the list still holds exactly the caller;
	// the JSON shape check is on the raw body.
	h.do(http.MethodPost, "/api/v1/sessions/revoke-all", nil, withCookie(a))
	w, body := h.listSessions(a)
	if len(body.Sessions) != 1 || body.Sessions[0].ID != aID {
		t.Fatalf("sessions = %+v", body.Sessions)
	}
	if w.Body.String() == `{"sessions":null}`+"\n" {
		t.Fatal("empty-ish list encoded as null")
	}
}

func TestSessions_DeleteForeign(t *testing.T) {
	h := newHarness(t, nil, nil)
	a := h.loginOK()
	otherID := h.insertOther(2)

	for _, path := range []string{
		"/api/v1/sessions/" + itoa(otherID),
		"/api/v1/sessions/999999",
		"/api/v1/sessions/abc",
	} {
		w := h.do(http.MethodDelete, path, nil, withCookie(a))
		if w.Code != http.StatusNotFound || decodeErr(t, w).Error != CodeNotFound {
			t.Errorf("DELETE %s: status %d body %s, want 404 not_found", path, w.Code, w.Body.String())
		}
	}
	rows, err := h.store.ListByUser(context.Background(), "other")
	if err != nil || len(rows) != 1 || rows[0].ID != otherID {
		t.Fatalf("other's row after foreign delete: %v %v", rows, err)
	}
}

func TestSessions_DeleteOne(t *testing.T) {
	h := newHarness(t, nil, nil)
	a := h.loginOK()
	b := h.loginOK()
	bID := h.idOf(b)

	w := h.do(http.MethodDelete, "/api/v1/sessions/"+itoa(bID), nil, withCookie(a))
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE B: status %d body %s", w.Code, w.Body.String())
	}
	if c := sessionCookie(t, w); c == nil || c.MaxAge <= 0 {
		t.Fatalf("deleting another session should keep A's refresh, got %+v", c)
	}
	if code := h.me(b); code != http.StatusUnauthorized {
		t.Fatalf("/me with revoked B: %d, want 401", code)
	}
	if code := h.me(a); code != http.StatusOK {
		t.Fatalf("/me with A: %d, want 200", code)
	}
}

func TestSessions_DeleteSelf(t *testing.T) {
	h := newHarness(t, nil, nil)
	a := h.loginOK()
	aID := h.idOf(a)

	w := h.do(http.MethodDelete, "/api/v1/sessions/"+itoa(aID), nil, withCookie(a))
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE self: status %d body %s", w.Code, w.Body.String())
	}
	c := sessionCookie(t, w) // fatals on two headers
	if c == nil || c.MaxAge != -1 {
		t.Fatalf("DELETE self Set-Cookie = %+v, want exactly one with Max-Age=0", c)
	}
	if code := h.me(a); code != http.StatusUnauthorized {
		t.Fatalf("/me after self-delete: %d, want 401", code)
	}
}

func TestSessions_RevokeAll(t *testing.T) {
	h := newHarness(t, nil, nil)
	a := h.loginOK()
	b := h.loginOK()
	c := h.loginOK()

	revoke := func() int {
		t.Helper()
		w := h.do(http.MethodPost, "/api/v1/sessions/revoke-all", nil, withCookie(a))
		if w.Code != http.StatusOK {
			t.Fatalf("revoke-all: status %d body %s", w.Code, w.Body.String())
		}
		var body struct {
			Revoked int `json:"revoked"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Revoked
	}
	if n := revoke(); n != 2 {
		t.Fatalf("revoked = %d, want 2", n)
	}
	if h.me(b) != http.StatusUnauthorized || h.me(c) != http.StatusUnauthorized {
		t.Fatal("B or C still valid after revoke-all")
	}
	if h.me(a) != http.StatusOK {
		t.Fatal("A (the caller) was revoked")
	}
	if n := revoke(); n != 0 {
		t.Fatalf("second revoke-all = %d, want 0", n)
	}
}

func TestSessions_Chain(t *testing.T) {
	h := newHarness(t, nil, nil)
	a := h.loginOK()
	aID := h.idOf(a)

	w := h.do(http.MethodDelete, "/api/v1/sessions/"+itoa(aID), nil, withCookie(a), noCSRF())
	if w.Code != http.StatusForbidden {
		t.Fatalf("DELETE without CSRF header: status %d, want 403", w.Code)
	}
	if h.me(a) != http.StatusOK {
		t.Fatal("CSRF-rejected delete still revoked the session")
	}

	for name, w := range map[string]*httptest.ResponseRecorder{
		"csrf 403":   w,
		"list":       h.do(http.MethodGet, "/api/v1/sessions", nil, withCookie(a)),
		"revoke-all": h.do(http.MethodPost, "/api/v1/sessions/revoke-all", nil, withCookie(a)),
		"delete 404": h.do(http.MethodDelete, "/api/v1/sessions/abc", nil, withCookie(a)),
		"list 401":   h.do(http.MethodGet, "/api/v1/sessions", nil),
	} {
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, got)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
