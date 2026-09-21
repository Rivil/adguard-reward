package adguardtest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const user, pass = "svc", "pw"

func do(t *testing.T, s *Server, method, path, body string, auth bool) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, s.URL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		req.SetBasicAuth(user, pass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, b
}

func TestFake_RequiresBasicAuth(t *testing.T) {
	s := New(t, Options{User: user, Pass: pass})

	resp, _ := do(t, s, "GET", "/control/clients", "", false)
	if resp.StatusCode != 401 {
		t.Errorf("no auth: status %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", s.URL()+"/control/clients", nil)
	req.SetBasicAuth(user, "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("wrong password: status %d, want 401", resp.StatusCode)
	}

	resp, b := do(t, s, "GET", "/control/clients", "", true)
	if resp.StatusCode != 200 {
		t.Fatalf("right password: status %d, want 200", resp.StatusCode)
	}
	if !bytes.Equal(b, Fixture("clients.json")) {
		t.Error("right password: body is not the embedded fixture verbatim")
	}

	s.SetAuth(false)
	resp, _ = do(t, s, "GET", "/control/status", "", true)
	if resp.StatusCode != 401 {
		t.Errorf("SetAuth(false): status %d, want 401", resp.StatusCode)
	}
}

func TestFake_Login(t *testing.T) {
	s := New(t, Options{User: user, Pass: pass})

	resp, _ := do(t, s, "POST", "/control/login", `{"name":"svc","password":"pw"}`, false)
	if resp.StatusCode != 200 {
		t.Errorf("good login without basic auth: status %d, want 200", resp.StatusCode)
	}

	resp, b := do(t, s, "POST", "/control/login", `{"name":"svc","password":"nope"}`, false)
	if resp.StatusCode != 403 || !strings.Contains(string(b), "invalid username or password") {
		t.Errorf("bad login: status %d body %q, want 403 'invalid username or password'", resp.StatusCode, b)
	}

	s.SetStatus("/control/login", 400)
	resp, _ = do(t, s, "POST", "/control/login", `{"name":"svc","password":"nope"}`, false)
	if resp.StatusCode != 400 {
		t.Errorf("SetStatus(login, 400): status %d, want 400", resp.StatusCode)
	}
}

func TestFake_UpdateEcho(t *testing.T) {
	s := New(t, Options{User: user, Pass: pass})

	update := `{"name":"Kid phone","data":{"name":"Kid phone","ids":["1.2.3.4"],"blocked_services":["roblox"],"future_field":42,"brand_new":{"x":null}}}`
	resp, b := do(t, s, "POST", "/control/clients/update", update, true)
	if resp.StatusCode != 200 {
		t.Fatalf("update: status %d body %s", resp.StatusCode, b)
	}
	if !bytes.Equal(s.LastUpdate(), []byte(update)) {
		t.Error("LastUpdate() is not the raw body")
	}

	_, b = do(t, s, "GET", "/control/clients", "", true)
	var doc struct {
		Clients []map[string]json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	for _, c := range doc.Clients {
		if string(c["name"]) == `"Kid phone"` {
			got = c
		}
	}
	if got == nil {
		t.Fatal("Kid phone missing after update")
	}
	var want map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"name":"Kid phone","ids":["1.2.3.4"],"blocked_services":["roblox"],"future_field":42,"brand_new":{"x":null}}`), &want)
	if len(got) != len(want) {
		t.Errorf("echoed client has %d keys, want %d: %v", len(got), len(want), keys(got))
	}
	for k, v := range want {
		if !bytes.Equal(bytes.TrimSpace(got[k]), bytes.TrimSpace(v)) {
			t.Errorf("echoed %s = %s, want %s", k, got[k], v)
		}
	}
	if len(doc.Clients) != 3 {
		t.Errorf("client count after update = %d, want 3", len(doc.Clients))
	}

	resp, _ = do(t, s, "POST", "/control/clients/update", `{"name":"Kid phone","data":{"name":"x"},"extra":1}`, true)
	if resp.StatusCode != 400 {
		t.Errorf("unknown top-level key: status %d, want 400", resp.StatusCode)
	}
	resp, _ = do(t, s, "POST", "/control/clients/update", `{"name":"nobody","data":{"name":"nobody"}}`, true)
	if resp.StatusCode != 400 {
		t.Errorf("unknown client: status %d, want 400", resp.StatusCode)
	}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestFake_MutateClient(t *testing.T) {
	s := New(t, Options{User: user, Pass: pass})
	s.MutateClient("Kid tablet", func(c map[string]json.RawMessage) {
		c["blocked_services"] = json.RawMessage(`["youtube"]`)
	})
	_, b := do(t, s, "GET", "/control/clients", "", true)
	if !strings.Contains(string(b), `"blocked_services":["youtube"]`) {
		t.Errorf("MutateClient not reflected: %s", b)
	}
}

func TestFake_Hang(t *testing.T) {
	s := New(t, Options{User: user, Pass: pass})
	s.Hang("/control/status", time.Second)
	start := time.Now()
	resp, _ := do(t, s, "GET", "/control/status", "", true)
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if d := time.Since(start); d < time.Second {
		t.Errorf("Hang: response took %v, want >= 1s", d)
	}
}

func TestFake_SetResponse(t *testing.T) {
	s := New(t, Options{User: user, Pass: pass})
	s.SetResponse("/control/status", 503, bytes.Repeat([]byte("x"), 10<<10))
	resp, b := do(t, s, "GET", "/control/status", "", false) // bypasses auth
	if resp.StatusCode != 503 || len(b) != 10<<10 {
		t.Errorf("SetResponse: status %d len %d", resp.StatusCode, len(b))
	}
	s.SetStatus("/control/status", 302)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp2, err := c.Get(s.URL() + "/control/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != 302 || resp2.Header.Get("Location") == "" {
		t.Errorf("302 override: status %d location %q", resp2.StatusCode, resp2.Header.Get("Location"))
	}
}

func TestFixtures_Shape(t *testing.T) {
	var clients struct {
		Clients []map[string]json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal(Fixture("clients.json"), &clients); err != nil {
		t.Fatal(err)
	}
	if len(clients.Clients) == 0 {
		t.Fatal("clients.json has no clients")
	}
	sawNull, sawGlobal := false, false
	for _, c := range clients.Clients {
		for _, k := range []string{"name", "ids", "use_global_blocked_services", "blocked_services", "blocked_services_schedule"} {
			if _, ok := c[k]; !ok {
				t.Errorf("client %s lacks %s", c["name"], k)
			}
		}
		if string(c["blocked_services"]) == "null" {
			sawNull = true
		} else {
			var ids []string
			if err := json.Unmarshal(c["blocked_services"], &ids); err != nil {
				t.Errorf("client %s blocked_services is not a string array: %s", c["name"], c["blocked_services"])
			}
		}
		if string(c["use_global_blocked_services"]) == "true" {
			sawGlobal = true
		}
	}
	if !sawNull || !sawGlobal {
		t.Errorf("fixture must include a null blocked_services client (%v) and a use_global client (%v)", sawNull, sawGlobal)
	}

	var get struct {
		IDs      *[]string       `json:"ids"`
		Schedule json.RawMessage `json:"schedule"`
	}
	if err := json.Unmarshal(Fixture("blocked_services_get.json"), &get); err != nil {
		t.Fatal(err)
	}
	if get.IDs == nil || get.Schedule == nil {
		t.Error("blocked_services_get.json needs ids and schedule")
	}

	var all struct {
		Services []map[string]json.RawMessage `json:"blocked_services"`
	}
	if err := json.Unmarshal(Fixture("blocked_services_all.json"), &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Services) == 0 {
		t.Fatal("blocked_services_all.json has no services")
	}
	for _, k := range []string{"id", "name", "icon_svg"} {
		if _, ok := all.Services[0][k]; !ok {
			t.Errorf("blocked_services_all[0] lacks %s", k)
		}
	}
	for _, svc := range all.Services {
		if string(svc["icon_svg"]) != `"PHN2Zy8+"` {
			t.Errorf("service %s icon_svg must be the placeholder, not a real icon (r-01)", svc["id"])
		}
	}

	var status struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(Fixture("status.json"), &status); err != nil {
		t.Fatal(err)
	}
	if status.Version == "" {
		t.Error("status.json needs version")
	}
}

func TestFake_Requests(t *testing.T) {
	s := New(t, Options{User: user, Pass: pass})
	do(t, s, "GET", "/control/status", "", true)
	do(t, s, "POST", "/control/login", `{"name":"svc","password":"pw"}`, false)
	do(t, s, "POST", "/control/clients/update", `{"name":"Kid phone","data":{"name":"Kid phone"}}`, true)

	got := s.Requests()
	if len(got) != 3 {
		t.Fatalf("recorded %d requests, want 3", len(got))
	}
	if got[0].Path != "/control/status" || !got[0].HasAuth {
		t.Errorf("req 0 = %+v", got[0])
	}
	if got[1].Path != "/control/login" || got[1].HasAuth || string(got[1].Body) != `{"name":"svc","password":"pw"}` {
		t.Errorf("req 1 = %+v", got[1])
	}
	if got[2].Method != "POST" || string(got[2].Body) != `{"name":"Kid phone","data":{"name":"Kid phone"}}` {
		t.Errorf("req 2 = %+v", got[2])
	}
	if s.MaxInFlightUpdates() != 1 {
		t.Errorf("MaxInFlightUpdates = %d, want 1 for sequential calls", s.MaxInFlightUpdates())
	}
}

func TestFake_SetUpdateStatus(t *testing.T) {
	s := New(t, Options{User: user, Pass: pass})
	update := func(name string) int {
		resp, _ := do(t, s, "POST", "/control/clients/update",
			`{"name":"`+name+`","data":{"name":"`+name+`","ids":[]}}`, true)
		return resp.StatusCode
	}

	s.SetUpdateStatus("Kid tablet", 500)
	if code := update("Kid tablet"); code != 500 {
		t.Errorf("faulted client: status %d, want 500", code)
	}
	if s.LastUpdate() != nil {
		t.Error("a faulted update must not be stored")
	}
	if code := update("Kid phone"); code != 200 {
		t.Errorf("other client: status %d, want 200", code)
	}

	s.SetUpdateStatus("Kid tablet", 0)
	if code := update("Kid tablet"); code != 200 {
		t.Errorf("after clearing: status %d, want 200", code)
	}
}

func TestFake_GrantHelpers(t *testing.T) {
	s := New(t, Options{User: user, Pass: pass})

	if got, ok := s.BlockedServices("Kid phone"); !ok || strings.Join(got, ",") != "youtube,tiktok" {
		t.Errorf("BlockedServices(Kid phone) = %v, %v; want [youtube tiktok] true (fixture order)", got, ok)
	}
	if got, ok := s.BlockedServices("Old laptop"); !ok || got == nil || len(got) != 0 {
		t.Errorf("BlockedServices(Old laptop) = %#v, %v; want [] true (null reads as empty)", got, ok)
	}
	if got, ok := s.BlockedServices("Nobody"); ok || got != nil {
		t.Errorf("BlockedServices(Nobody) = %v, %v; want nil false", got, ok)
	}
	s.MutateClient("Kid phone", func(c map[string]json.RawMessage) {
		c["blocked_services"] = json.RawMessage(`["roblox"]`)
	})
	if got, _ := s.BlockedServices("Kid phone"); strings.Join(got, ",") != "roblox" {
		t.Errorf("BlockedServices after MutateClient = %v, want [roblox]", got)
	}

	s.RemoveClient("Kid tablet")
	_, b := do(t, s, "GET", "/control/clients", "", true)
	var doc struct {
		Clients []map[string]json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Clients) != 2 {
		t.Errorf("GET /control/clients lists %d clients after RemoveClient, want 2", len(doc.Clients))
	}
	if strings.Contains(string(b), `"Kid tablet"`) {
		t.Error("removed client still served")
	}
	resp, _ := do(t, s, "POST", "/control/clients/update",
		`{"name":"Kid tablet","data":{"name":"Kid tablet","ids":[]}}`, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("update of removed client: status %d, want 400", resp.StatusCode)
	}
	if _, ok := s.BlockedServices("Kid tablet"); ok {
		t.Error("BlockedServices still finds the removed client")
	}

	if n := s.CountRequests("POST", "/control/clients/update"); n != 1 {
		t.Errorf("CountRequests(POST, /control/clients/update) = %d, want 1", n)
	}
	if n := s.CountRequests("GET", "/control/clients"); n != 1 {
		t.Errorf("CountRequests(GET, /control/clients) = %d, want 1", n)
	}
	if n := s.CountRequests("GET", "/control/clients/update"); n != 0 {
		t.Errorf("CountRequests must match method exactly, got %d", n)
	}
	if n := s.CountRequests("POST", "/control/clients"); n != 0 {
		t.Errorf("CountRequests must match path exactly (no prefix match), got %d", n)
	}
}
