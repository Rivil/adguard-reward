package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/store"
)

const exportPath = "/api/v1/export"

// exportGolden is the seedExport install as a file: stored order everywhere
// but the toggles, which sort by child name then ref.
const exportGolden = `{
	"format": "adguard-reward-export",
	"version": 1,
	"children": [
		{"name": "Ben", "clients": ["Kid phone", "Shared tv"]},
		{"name": "Ada", "clients": ["Shared tv"]},
		{"name": "Cy", "clients": []}
	],
	"custom_services": [
		{"name": "Roblox Extras", "domains": ["z.roblox.example", "a.roblox.example"]},
		{"name": "Other", "domains": ["other.example"]}
	],
	"buttons": [
		{"label": "Zed time", "child": "Ben", "services": ["youtube", "custom:Roblox Extras"], "icon": "custom:Other", "duration": 1800},
		{"label": "Art time", "child": "Ada", "services": ["tiktok"], "icon": "", "duration": 3600}
	],
	"toggles": [
		{"child": "Ada", "service": "custom:Other", "state": "unblocked"},
		{"child": "Ada", "service": "tiktok", "state": "blocked"},
		{"child": "Ben", "service": "custom:Roblox Extras", "state": "blocked"},
		{"child": "Ben", "service": "youtube", "state": "unblocked"}
	]
}`

// seedExport fills every list straight in the store, with the children and
// buttons out of alphabetical order so a sort cannot pass for stored order.
func (h *harness) seedExport() {
	h.t.Helper()
	ctx := context.Background()
	ben := h.seedChild("Ben", "Kid phone", "Shared tv")
	ada := h.seedChild("Ada", "Shared tv")
	h.seedChild("Cy")

	roblox, err := h.store.CreateCustomService(ctx, "Roblox Extras", []string{"z.roblox.example", "a.roblox.example"})
	if err != nil {
		h.t.Fatal(err)
	}
	other, err := h.store.CreateCustomService(ctx, "Other", []string{"other.example"})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.store.ReplaceButtons(ctx, []store.Button{
		{ChildID: ben.ID, Label: "Zed time", Services: []string{"youtube", roblox.ServiceID()}, Icon: other.ServiceID(), Duration: 30 * time.Minute},
		{ChildID: ada.ID, Label: "Art time", Services: []string{"tiktok"}, Duration: time.Hour},
	}); err != nil {
		h.t.Fatal(err)
	}
	for _, tg := range []struct {
		child          int64
		service, state string
	}{
		{ben.ID, "youtube", store.ToggleUnblocked},
		{ben.ID, roblox.ServiceID(), store.ToggleBlocked},
		{ada.ID, "tiktok", store.ToggleBlocked},
		{ada.ID, other.ServiceID(), store.ToggleUnblocked},
	} {
		if _, err := h.store.SetServiceToggle(ctx, tg.child, tg.service, tg.state, t0); err != nil {
			h.t.Fatal(err)
		}
	}
}

// exportOK GETs the file and requires a 200.
func (h *harness) exportOK(cookie string) *httptest.ResponseRecorder {
	h.t.Helper()
	w := h.do(http.MethodGet, exportPath, nil, withCookie(cookie))
	if w.Code != http.StatusOK {
		h.t.Fatalf("GET export: %d %s, want 200", w.Code, w.Body.String())
	}
	return w
}

// keyPaths records every object key under v as a path, with [] for a list.
func keyPaths(v any, path string, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			out[p] = true
			keyPaths(e, p, out)
		}
	case []any:
		for _, e := range x {
			keyPaths(e, path+"[]", out)
		}
	}
}

// secretForms is b as raw bytes and as hex in both cases.
func secretForms(b []byte) []string {
	h := hex.EncodeToString(b)
	return []string{string(b), h, strings.ToUpper(h)}
}

func TestExport_Contents(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.seedExport()
	w := h.exportOK(h.loginOK())

	var want bytes.Buffer
	if err := json.Compact(&want, []byte(exportGolden)); err != nil {
		t.Fatal(err)
	}
	if got := bytes.TrimSpace(w.Body.Bytes()); !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("body =\n%s\nwant\n%s", got, want.Bytes())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("Content-Disposition = %q, want none: the SPA saves through a Blob", cd)
	}
}

func TestExport_Gated(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.seedExport()

	w := h.do(http.MethodGet, exportPath, nil)
	if w.Code != http.StatusUnauthorized || decodeErr(t, w).Error != CodeUnauthorized {
		t.Fatalf("no session: %d %s, want 401 unauthorized", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Ada") {
		t.Fatalf("401 body leaks the setup: %s", w.Body.String())
	}
	h.exportOK(h.loginOK())
}

func TestExport_AdGuardDown(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.seedExport()
	cookie := h.loginOK()
	for _, path := range []string{
		"/control/login",
		"/control/status",
		"/control/clients",
		"/control/clients/add",
		"/control/clients/update",
		"/control/blocked_services/all",
		"/control/blocked_services/get",
		"/control/dhcp/status",
		"/control/filtering/status",
		setRulesPath,
	} {
		h.fake.SetStatus(path, http.StatusInternalServerError)
	}
	mark := len(h.fake.Requests())

	w := h.exportOK(cookie)
	if !strings.Contains(w.Body.String(), "Roblox Extras") {
		t.Errorf("body lacks the seeded setup: %s", w.Body.String())
	}
	if got := h.fake.Requests()[mark:]; len(got) != 0 {
		t.Fatalf("export made %d AdGuard requests, want 0: %+v", len(got), got)
	}
}

func TestExport_DanglingRef(t *testing.T) {
	const dangling = "custom:99"
	ctx := context.Background()
	cases := []struct {
		name string
		seed func(h *harness, child int64) error
	}{
		{"button service", func(h *harness, child int64) error {
			_, err := h.store.ReplaceButtons(ctx, []store.Button{
				{ChildID: child, Label: "Games", Services: []string{dangling}, Duration: time.Hour},
			})
			return err
		}},
		{"button icon", func(h *harness, child int64) error {
			_, err := h.store.ReplaceButtons(ctx, []store.Button{
				{ChildID: child, Label: "Games", Services: []string{"youtube"}, Icon: dangling, Duration: time.Hour},
			})
			return err
		}},
		{"toggle", func(h *harness, child int64) error {
			_, err := h.store.SetServiceToggle(ctx, child, dangling, store.ToggleBlocked, t0)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, nil)
			if err := tc.seed(h, h.seedChild("Ada", "Kid phone").ID); err != nil {
				t.Fatal(err)
			}
			w := h.do(http.MethodGet, exportPath, nil, withCookie(h.loginOK()))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d %s, want 500", w.Code, w.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("body %q: %v", w.Body.String(), err)
			}
			if keys := keysOf(t, got); !reflect.DeepEqual(keys, []string{"error", "message"}) {
				t.Fatalf("body keys = %v, want only the error envelope: %s", keys, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "Ada") {
				t.Fatalf("body holds part of the file: %s", w.Body.String())
			}
			if !strings.Contains(h.logs.String(), "export setup failed") {
				t.Fatalf("the store error was not logged:\n%s", h.logs.String())
			}
		})
	}
}

func TestExport_ParsesBack(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.seedExport()
	// A CR or LF client name is stored as is and must still survive the file.
	h.seedChild("Dee", "Odd\r\nname")
	w := h.exportOK(h.loginOK())

	var exported setupFile
	if err := json.Unmarshal(w.Body.Bytes(), &exported); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	parsed, p := parseSetupFile(w.Body.Bytes())
	if p != nil {
		t.Fatalf("parseSetupFile refused an export: %d %s\n%s", p.Status, p.Message, w.Body.String())
	}
	if !reflect.DeepEqual(parsed, exported) {
		t.Fatalf("parsing changed the file:\n%+v\nwant\n%+v", parsed, exported)
	}
}

func TestExport_NoSecrets(t *testing.T) {
	h := newHarness(t, nil, nil)
	ctx := context.Background()
	h.seedExport()

	cookie := h.loginOK()
	if code := h.me(cookie); code != http.StatusOK {
		t.Fatalf("/me with the session = %d, want it live", code)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil {
		t.Fatalf("session cookie: %v", err)
	}
	sessionHash := sha256.Sum256(raw)
	if _, err := h.store.ByTokenHash(ctx, sessionHash); err != nil {
		t.Fatalf("session hash is not the stored one: %v", err)
	}

	tokenHash := sha256.Sum256([]byte("sentinel device token"))
	const pinHash = "$argon2id$v=19$m=65536,t=3,p=4$U0VOVElORUw$c2VudGluZWwtcGluLWhhc2g"
	id, err := h.store.InsertDevice(ctx, auth.Device{
		Username:  fakeUser,
		TokenHash: tokenHash,
		PINHash:   pinHash,
		CreatedAt: t0,
		ExpiresAt: t0.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := h.store.DeviceByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if dev.TokenHash != tokenHash || dev.PINHash != pinHash {
		t.Fatalf("device read back as %+v, not the sentinels", dev)
	}

	body := h.exportOK(cookie).Body.String()
	if !strings.Contains(body, "Roblox Extras") {
		t.Fatalf("body lacks the seeded setup (positive control): %s", body)
	}
	for _, value := range [][]byte{[]byte(cookie), sessionHash[:], tokenHash[:], []byte(pinHash)} {
		for _, form := range secretForms(value) {
			if strings.Contains(body, form) {
				t.Errorf("body contains secret %q:\n%s", form, body)
			}
		}
	}
}

func TestExport_KeyAllowlist(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.seedExport()
	w := h.exportOK(h.loginOK())

	var doc any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	found := map[string]bool{}
	keyPaths(doc, "", found)
	got := make([]string, 0, len(found))
	for p := range found {
		got = append(got, p)
	}
	slices.Sort(got)

	want := []string{
		"buttons",
		"buttons[].child",
		"buttons[].duration",
		"buttons[].icon",
		"buttons[].label",
		"buttons[].services",
		"children",
		"children[].clients",
		"children[].name",
		"custom_services",
		"custom_services[].domains",
		"custom_services[].name",
		"format",
		"toggles",
		"toggles[].child",
		"toggles[].service",
		"toggles[].state",
		"version",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys =\n%v\nwant\n%v", got, want)
	}
}

func TestExport_EmptyInstall(t *testing.T) {
	h := newHarness(t, nil, nil)
	w := h.exportOK(h.loginOK())

	var top map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &top); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	for _, list := range []string{"children", "custom_services", "buttons", "toggles"} {
		if got := string(top[list]); got != "[]" {
			t.Errorf("%s = %q, want []", list, got)
		}
	}
	if _, p := parseSetupFile(w.Body.Bytes()); p != nil {
		t.Fatalf("parseSetupFile refused an empty export: %d %s", p.Status, p.Message)
	}
}
