package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubAPI mounts a counting stub at /api/v1/x for the given methods.
func stubAPI(t *testing.T, methods ...string) (http.Handler, *int) {
	t.Helper()
	a := New(Deps{})
	calls := 0
	for _, m := range methods {
		a.mux.HandleFunc(m+" /api/v1/x", func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.WriteHeader(http.StatusOK)
		})
	}
	return a.Handler(), &calls
}

func send(h http.Handler, method, path string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var withHeader = map[string]string{CSRFHeader: CSRFValue}

func TestCSRF_Methods(t *testing.T) {
	all := []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	h, calls := stubAPI(t, all...)

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		*calls = 0
		w := send(h, m, "/api/v1/x", nil)
		if w.Code != http.StatusForbidden || *calls != 0 {
			t.Errorf("%s without header: status %d calls %d, want 403 and no call", m, w.Code, *calls)
		}
		var body errorBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error != CodeForbidden {
			t.Errorf("%s: body %s, want error forbidden", m, w.Body.String())
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		*calls = 0
		if w := send(h, m, "/api/v1/x", nil); w.Code != http.StatusOK || *calls != 1 {
			t.Errorf("%s without header: status %d calls %d, want 200 and one call", m, w.Code, *calls)
		}
	}
	*calls = 0
	if w := send(h, http.MethodPost, "/api/v1/x", withHeader); w.Code != http.StatusOK || *calls != 1 {
		t.Errorf("POST with header: status %d calls %d, want 200 and one call", w.Code, *calls)
	}
}

func TestCSRF_Value(t *testing.T) {
	h, calls := stubAPI(t, http.MethodPost)
	for _, v := range []string{"AdGuard-Reward", "adguard-reward ", "XMLHttpRequest", ""} {
		w := send(h, http.MethodPost, "/api/v1/x", map[string]string{CSRFHeader: v})
		if w.Code != http.StatusForbidden {
			t.Errorf("header %q: status %d, want 403", v, w.Code)
		}
	}
	if *calls != 0 {
		t.Fatalf("stub ran %d times", *calls)
	}
}

func TestCSRF_BeforeHandler(t *testing.T) {
	a := New(Deps{})
	a.mux.HandleFunc("POST /api/v1/boom", func(http.ResponseWriter, *http.Request) {
		panic("handler reached without the CSRF header")
	})
	h := a.Handler()

	if w := send(h, http.MethodPost, "/api/v1/nope", nil); w.Code != http.StatusForbidden {
		t.Errorf("unmounted path without header: status %d, want 403 (not 404)", w.Code)
	}
	if w := send(h, http.MethodPost, "/api/v1/boom", nil); w.Code != http.StatusForbidden {
		t.Errorf("panicking route without header: status %d, want 403", w.Code)
	}
}

func TestNoStore(t *testing.T) {
	h, _ := stubAPI(t, http.MethodGet)
	cases := []struct {
		name       string
		method     string
		path       string
		header     map[string]string
		wantStatus int // 0 = don't assert
	}{
		{"csrf 403", http.MethodPost, "/api/v1/x", nil, http.StatusForbidden},
		{"unmounted path", http.MethodGet, "/api/v1/unmounted", nil, 0},
		// The mux would answer 405 here, but the /api/v1/ catch-all (t-9)
		// claims every unmatched method+path and answers 401 anonymously, so
		// only the header is asserted.
		{"wrong method on a mounted path", http.MethodPost, "/api/v1/x", withHeader, 0},
		{"200 stub", http.MethodGet, "/api/v1/x", nil, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := send(h, c.method, c.path, c.header)
			if c.wantStatus != 0 && w.Code != c.wantStatus {
				t.Fatalf("status %d, want %d", w.Code, c.wantStatus)
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusUnauthorized, CodeUnauthorized, "m")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q, want application/json", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["error"] != "unauthorized" || got["message"] != "m" {
		t.Fatalf("body = %v, want exactly {error: unauthorized, message: m}", got)
	}

	// UnauthorizedWriter is the same envelope, for auth.RequireSession.
	w2 := httptest.NewRecorder()
	UnauthorizedWriter(w2, http.StatusUnauthorized, CodeUnauthorized, "m")
	if w2.Body.String() != w.Body.String() || w2.Code != w.Code {
		t.Fatalf("UnauthorizedWriter differs from writeError: %s", w2.Body.String())
	}
}
