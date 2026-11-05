package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
	"github.com/Rivil/adguard-reward/internal/store"
)

const importPath = "/api/v1/import"

func (h *harness) postImport(cookie string, body []byte) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, importPath, string(body), withCookie(cookie))
}

// adguardPosts counts the POSTs the fake has seen, which login adds to.
func (h *harness) adguardPosts() int {
	n := 0
	for _, r := range h.fake.Requests() {
		if r.Method == http.MethodPost {
			n++
		}
	}
	return n
}

func (h *harness) wantNoSetup() {
	h.t.Helper()
	got, err := h.store.ExportSetup(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	if n := len(got.Children) + len(got.CustomServices) + len(got.Buttons) + len(got.Toggles); n != 0 {
		h.t.Fatalf("store holds setup after a refused import: %+v", got)
	}
}

func wantProblem(t *testing.T, w *httptest.ResponseRecorder, status int, code string, wants ...string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d %s, want %d", w.Code, w.Body, status)
	}
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	if body.Error != code {
		t.Fatalf("code = %q, want %q", body.Error, code)
	}
	for _, want := range wants {
		if !strings.Contains(body.Message, want) {
			t.Fatalf("message %q does not name %q", body.Message, want)
		}
	}
}

// pushIDsForward burns custom service ids, so a target's ids differ from the
// source's and an id passed through unmapped lands on the wrong service.
func (h *harness) pushIDsForward(n int) {
	h.t.Helper()
	ctx := context.Background()
	for i := range n {
		s, err := h.store.CreateCustomService(ctx, fmt.Sprintf("Burn %d", i), []string{fmt.Sprintf("burn%d.example", i)})
		if err != nil {
			h.t.Fatal(err)
		}
		if err := h.store.DeleteCustomService(ctx, s.ID); err != nil {
			h.t.Fatal(err)
		}
	}
}

func TestImport_RoundTrip(t *testing.T) {
	src := newCustomHarness(t)
	src.pushIDsForward(1)
	src.seedExport()
	first := src.exportOK(src.loginOK()).Body.Bytes()

	dst := newCustomHarness(t)
	dst.pushIDsForward(5)
	cookie := dst.loginOK()
	w := dst.postImport(cookie, first)
	if w.Code != http.StatusOK {
		t.Fatalf("import = %d %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"missing_clients":[]`) {
		t.Fatalf("body = %s, want an empty missing_clients list", w.Body)
	}

	second := dst.exportOK(cookie).Body.Bytes()
	if !bytes.Equal(first, second) {
		t.Fatalf("second export differs:\n%s\nwant\n%s", second, first)
	}

	names := map[string]string{}
	for _, s := range listCustom(t, dst, cookie) {
		names[s.ServiceID] = s.Name
		if s.ID <= 5 {
			t.Fatalf("service %q has id %d, want a fresh id above the burned ones", s.Name, s.ID)
		}
	}
	buttons, err := dst.store.ListButtons(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := names[buttons[0].Services[1]]; got != "Roblox Extras" {
		t.Fatalf("button service %q lists %q, want Roblox Extras", buttons[0].Services[1], got)
	}
	if got := names[buttons[0].Icon]; got != "Other" {
		t.Fatalf("button icon %q lists %q, want Other", buttons[0].Icon, got)
	}

	children, err := dst.store.ListChildren(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	holders := 0
	for _, c := range children {
		for _, client := range c.Clients {
			if client == "Shared tv" {
				holders++
			}
		}
	}
	if holders != 2 {
		t.Fatalf("Shared tv has %d holders, want 2: %+v", holders, children)
	}
}

func TestImport_RejectedWhole(t *testing.T) {
	cases := []struct {
		name   string
		body   func(t *testing.T) []byte
		status int
		code   string
		want   string
	}{
		{"invalid JSON", func(t *testing.T) []byte { return []byte(`{not json`) },
			http.StatusBadRequest, CodeBadRequest, "not valid JSON"},
		{"unknown version", func(t *testing.T) []byte {
			return mapBody(t, func(m map[string]any) { m["version"] = 2 })
		}, http.StatusUnprocessableEntity, CodeUnprocessable, "version 2"},
		{"duplicate name", func(t *testing.T) []byte {
			return fileBody(t, edited(func(f *setupFile) { f.Children[1].Name = "ada" }))
		}, http.StatusUnprocessableEntity, CodeUnprocessable, "children[1].name"},
		{"duplicate domain", func(t *testing.T) []byte {
			return fileBody(t, edited(func(f *setupFile) { f.CustomServices[1].Domains = []string{"rbx.example"} }))
		}, http.StatusUnprocessableEntity, CodeUnprocessable, `"rbx.example"`},
		{"dangling child", func(t *testing.T) []byte {
			return fileBody(t, edited(func(f *setupFile) { f.Buttons[0].Child = "Nobody" }))
		}, http.StatusUnprocessableEntity, CodeUnprocessable, "buttons[0].child"},
		{"dangling service", func(t *testing.T) []byte {
			return fileBody(t, edited(func(f *setupFile) { f.Toggles[1].Service = "custom:Nope" }))
		}, http.StatusUnprocessableEntity, CodeUnprocessable, "toggles[1].service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, nil)
			rules := []string{"! parent line", "||example.org^"}
			h.fake.SetUserRules(rules)
			cookie := h.loginOK()
			posts := h.adguardPosts()

			wantProblem(t, h.postImport(cookie, tc.body(t)), tc.status, tc.code, tc.want)

			h.wantNoSetup()
			if got := h.adguardPosts(); got != posts {
				t.Fatalf("AdGuard saw %d POSTs, want none", got-posts)
			}
			if got := h.fake.UserRules(); !reflect.DeepEqual(got, rules) {
				t.Fatalf("user rules = %q, want unchanged", got)
			}
		})
	}
}

func TestImport_UnknownBuiltin(t *testing.T) {
	const absent = "not-in-catalogue"
	cases := []struct {
		name   string
		mutate func(f *setupFile)
	}{
		{"button service", func(f *setupFile) { f.Buttons[0].Services[0] = absent }},
		{"button icon", func(f *setupFile) { f.Buttons[0].Icon = absent }},
		{"toggle", func(f *setupFile) { f.Toggles[0].Service = absent }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, nil)
			w := h.postImport(h.loginOK(), fileBody(t, edited(tc.mutate)))
			wantProblem(t, w, http.StatusUnprocessableEntity, CodeUnprocessable, absent)
			h.wantNoSetup()
		})
	}
}

func TestImport_BuiltinNameClash(t *testing.T) {
	for _, name := range []string{"YouTube", "YOUTUBE", "youtube"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil, nil)
			cookie := h.loginOK()
			posts := h.adguardPosts()
			f := edited(func(f *setupFile) {
				f.CustomServices[1].Name = name
				f.Buttons[0].Icon = customIDPrefix + name
				f.Toggles[1].Service = customIDPrefix + name
			})
			wantProblem(t, h.postImport(cookie, fileBody(t, f)),
				http.StatusUnprocessableEntity, CodeUnprocessable, "custom_services[1].name", "YouTube")
			h.wantNoSetup()
			if got := h.adguardPosts(); got != posts {
				t.Fatalf("AdGuard saw %d POSTs, want none", got-posts)
			}
		})
	}
}

// withChildren rebuilds h's API over children, keeping everything else.
func (h *harness) withChildren(children ChildStore) {
	h.t.Helper()
	log := slog.New(slog.DiscardHandler)
	client, err := adguard.New(h.fake.URL(), fakeUser, fakePass)
	if err != nil {
		h.t.Fatal(err)
	}
	eng := grants.New(h.store, client, grants.Options{Now: h.clock.Now, ApplyTimeout: harnessApplyTimeout, Log: log})
	h.t.Cleanup(eng.Close)
	cfg := ratelimit.Defaults()
	cfg.Now = h.clock.Now
	h.h = New(Deps{
		AdGuard:  client,
		ClientIP: ClientIP(nil),
		Log:      log,
		Auth:     auth.New(h.store, auth.Options{Now: h.clock.Now, Log: log, ErrorWriter: UnauthorizedWriter}),
		Limiter:  ratelimit.New(cfg),
		Sessions: h.store,
		Children: children,
		Grants:   eng,
		Buttons:  h.store,
	}).Handler()
}

// stubbedSetup passes EnsureEmpty and answers ImportSetup with err, standing in
// for a write that lands between the two checks.
type stubbedSetup struct {
	ChildStore
	ensure error
	err    error
}

func (s stubbedSetup) EnsureEmpty(context.Context) error { return s.ensure }

func (s stubbedSetup) ImportSetup(context.Context, store.Setup) error { return s.err }

func TestImport_NonEmpty(t *testing.T) {
	seeds := []struct {
		name string
		seed func(h *harness)
		want string
	}{
		{"child", func(h *harness) { h.seedChild("Ada") }, "children"},
		{"custom service", func(h *harness) {
			if _, err := h.store.CreateCustomService(context.Background(), "Alpha", []string{"a.example"}); err != nil {
				h.t.Fatal(err)
			}
		}, "custom services"},
	}
	for _, tc := range seeds {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, nil)
			tc.seed(h)
			before, err := h.store.ExportSetup(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			wantProblem(t, h.postImport(h.loginOK(), fileBody(t, baseFile())),
				http.StatusConflict, CodeConflict, tc.want)
			after, err := h.store.ExportSetup(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("setup = %+v, want unchanged %+v", after, before)
			}
		})
	}

	t.Run("lost the race inside the transaction", func(t *testing.T) {
		h := newHarness(t, nil, nil)
		h.withChildren(stubbedSetup{ChildStore: h.store, err: &store.ErrNotEmpty{Kinds: []string{"children"}}})
		wantProblem(t, h.postImport(h.loginOK(), fileBody(t, baseFile())),
			http.StatusConflict, CodeConflict, "children")
	})
}

func TestImport_StoreFailure(t *testing.T) {
	broken := errors.New("disk on fire")
	cases := []struct {
		name string
		stub stubbedSetup
	}{
		{"emptiness check", stubbedSetup{ensure: broken}},
		{"commit", stubbedSetup{err: broken}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, nil)
			tc.stub.ChildStore = h.store
			h.withChildren(tc.stub)
			w := h.postImport(h.loginOK(), fileBody(t, baseFile()))
			wantProblem(t, w, http.StatusInternalServerError, "internal", "could not import setup")
			if strings.Contains(w.Body.String(), broken.Error()) {
				t.Fatalf("body %s leaks the store error", w.Body)
			}
		})
	}
}

func TestImport_CatalogueDown(t *testing.T) {
	h := newHarness(t, nil, nil)
	cookie := h.loginOK()
	h.fake.SetStatus("/control/blocked_services/all", http.StatusBadGateway)
	wantProblem(t, h.postImport(cookie, fileBody(t, baseFile())),
		http.StatusBadGateway, CodeAdGuardUnavailable)
	h.wantNoSetup()
}

func TestImport_BodyBounds(t *testing.T) {
	padded := func(t *testing.T, size int) []byte {
		t.Helper()
		body := fileBody(t, baseFile())
		return append(body, bytes.Repeat([]byte{' '}, size-len(body))...)
	}

	t.Run("valid 100 KiB file", func(t *testing.T) {
		f := baseFile()
		for i := range 3000 {
			f.Children = append(f.Children, fileChild{Name: fmt.Sprintf("Child %d", i), Clients: []string{"Kid phone"}})
		}
		body := fileBody(t, f)
		if len(body) < 100<<10 {
			t.Fatalf("body is %d bytes, want at least 100 KiB", len(body))
		}
		h := newHarness(t, nil, nil)
		if w := h.postImport(h.loginOK(), body); w.Code != http.StatusOK {
			t.Fatalf("import = %d %s", w.Code, w.Body)
		}
	})
	t.Run("at the limit", func(t *testing.T) {
		h := newHarness(t, nil, nil)
		if w := h.postImport(h.loginOK(), padded(t, maxImportBody)); w.Code != http.StatusOK {
			t.Fatalf("import = %d %s", w.Code, w.Body)
		}
	})
	t.Run("one byte over", func(t *testing.T) {
		h := newHarness(t, nil, nil)
		w := h.postImport(h.loginOK(), padded(t, maxImportBody+1))
		wantProblem(t, w, http.StatusBadRequest, CodeBadRequest, "8 MiB")
		if strings.Contains(w.Body.String(), "JSON") {
			t.Fatalf("body %s reads as bad JSON", w.Body)
		}
		h.wantNoSetup()
	})
}

func TestImport_Gated(t *testing.T) {
	h := newHarness(t, nil, nil)
	body := string(fileBody(t, baseFile()))
	if w := h.do(http.MethodPost, importPath, body); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d %s, want 401", w.Code, w.Body)
	}
	cookie := h.loginOK()
	if w := h.do(http.MethodPost, importPath, body, withCookie(cookie), noCSRF()); w.Code != http.StatusForbidden {
		t.Fatalf("no CSRF header = %d %s, want 403", w.Code, w.Body)
	}
	h.wantNoSetup()
}
