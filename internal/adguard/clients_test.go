package adguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard/adguardtest"
)

// fixtureClient returns the raw fixture object for the named client.
func fixtureClient(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	var doc struct {
		Clients []map[string]json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal(adguardtest.Fixture("clients.json"), &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Clients {
		if string(c["name"]) == `"`+name+`"` {
			return c
		}
	}
	t.Fatalf("fixture has no client %q", name)
	return nil
}

// decodeUpdate splits a recorded /control/clients/update body.
func decodeUpdate(t *testing.T, body []byte) (name string, data map[string]json.RawMessage) {
	t.Helper()
	var u struct {
		Name string                     `json:"name"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("update body %s: %v", body, err)
	}
	return u.Name, u.Data
}

// asAny decodes raw JSON so compaction differences do not matter.
func asAny(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func find(cs []PersistentClient, name string) *PersistentClient {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

func TestClients_Fixture(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	res, err := c.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Persistent) != 3 {
		t.Fatalf("got %d clients, want 3", len(res.Persistent))
	}
	kid := find(res.Persistent, "Kid phone")
	if kid == nil {
		t.Fatal("Kid phone missing")
	}
	if !reflect.DeepEqual(kid.IDs, []string{"aa:bb:cc:dd:ee:01", "192.168.1.50"}) {
		t.Errorf("IDs = %v", kid.IDs)
	}
	if !reflect.DeepEqual(kid.BlockedServices, []string{"youtube", "tiktok"}) {
		t.Errorf("BlockedServices = %v", kid.BlockedServices)
	}
	if kid.UseGlobalBlockedServices {
		t.Error("Kid phone UseGlobalBlockedServices should be false")
	}
	if tab := find(res.Persistent, "Kid tablet"); tab == nil || !tab.UseGlobalBlockedServices {
		t.Error("Kid tablet UseGlobalBlockedServices should be true")
	}
	if !reflect.DeepEqual(res.GlobalBlockedServices, []string{"tiktok", "roblox"}) {
		t.Errorf("GlobalBlockedServices = %v, want fixture ids", res.GlobalBlockedServices)
	}
}

func TestClients_Null(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	res, err := c.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	old := find(res.Persistent, "Old laptop")
	if old == nil || old.BlockedServices == nil || len(old.BlockedServices) != 0 {
		t.Errorf("null blocked_services should decode to an empty non-nil slice, got %#v", old)
	}

	s.SetResponse("/control/clients", 200, []byte(`{"clients":null,"auto_clients":null}`))
	res, err = c.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Persistent == nil || len(res.Persistent) != 0 {
		t.Errorf("clients:null should give an empty non-nil slice, got %#v", res.Persistent)
	}
}

func TestSetBlockedServices_Preserves(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	if err := c.SetBlockedServices(context.Background(), "Kid phone", []string{"youtube", "tiktok"}); err != nil {
		t.Fatal(err)
	}
	name, data := decodeUpdate(t, s.LastUpdate())
	if name != "Kid phone" {
		t.Errorf("update name = %q", name)
	}
	if got := asAny(t, data["blocked_services"]); !reflect.DeepEqual(got, []any{"tiktok", "youtube"}) {
		t.Errorf("data.blocked_services = %v, want [tiktok youtube] (sorted)", got)
	}

	orig := fixtureClient(t, "Kid phone")
	delete(orig, "blocked_services")
	delete(data, "blocked_services")
	if len(orig) != len(data) {
		t.Errorf("field count: sent %d, read %d", len(data), len(orig))
	}
	for k, v := range orig {
		got, ok := data[k]
		if !ok {
			t.Errorf("field %q dropped from the update", k)
			continue
		}
		if !reflect.DeepEqual(asAny(t, got), asAny(t, v)) {
			t.Errorf("field %q changed: sent %s, read %s", k, got, v)
		}
	}
	for _, k := range []string{"future_field", "blocked_services_schedule", "safe_search", "upstreams_cache_size", "tags", "upstreams"} {
		if _, ok := data[k]; !ok {
			t.Errorf("field %q must survive the round-trip", k)
		}
	}
	if string(data["upstreams_cache_size"]) != "null" {
		t.Errorf("upstreams_cache_size = %s, want null preserved (not zeroed)", data["upstreams_cache_size"])
	}
}

func TestSetBlockedServices_Normalise(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()

	if err := c.SetBlockedServices(ctx, "Kid phone", []string{"b", "a", "a"}); err != nil {
		t.Fatal(err)
	}
	_, data := decodeUpdate(t, s.LastUpdate())
	if got := asAny(t, data["blocked_services"]); !reflect.DeepEqual(got, []any{"a", "b"}) {
		t.Errorf("[b a a] written as %v, want [a b]", got)
	}

	if err := c.SetBlockedServices(ctx, "Kid phone", nil); err != nil {
		t.Fatal(err)
	}
	_, data = decodeUpdate(t, s.LastUpdate())
	if string(data["blocked_services"]) != "[]" {
		t.Errorf("nil ids written as %s, want []", data["blocked_services"])
	}
}

func TestSetBlockedServices_FreshRead(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()
	if _, err := c.Clients(ctx); err != nil {
		t.Fatal(err)
	}
	s.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["ids"] = json.RawMessage(`["10.0.0.99"]`)
	})
	if err := c.SetBlockedServices(ctx, "Kid phone", []string{"roblox"}); err != nil {
		t.Fatal(err)
	}
	_, data := decodeUpdate(t, s.LastUpdate())
	if got := asAny(t, data["ids"]); !reflect.DeepEqual(got, []any{"10.0.0.99"}) {
		t.Errorf("update carried ids %v; the write must re-read, not reuse the earlier Clients() result", got)
	}
}

func TestSetBlockedServices_NotFound(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	err := c.SetBlockedServices(context.Background(), "nobody", []string{"youtube"})
	if !errors.Is(err, ErrClientNotFound) {
		t.Errorf("err = %v, want ErrClientNotFound", err)
	}
	for _, r := range s.Requests() {
		if r.Path == "/control/clients/update" {
			t.Fatal("unknown client must not produce a write")
		}
	}
}

func TestSetBlockedServices_Serialised(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.Hang("/control/clients", 20*time.Millisecond)
	s.Hang("/control/clients/update", 30*time.Millisecond)

	// Every writer shares the one RMW lock: mixing them must still never
	// overlap on the fake. Add/Remove may legitimately skip their POST when
	// the list is already as wanted, so the sequence is GET then at most one
	// POST, never two GETs or two POSTs in a row.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids := []string{"svc", string(rune('a' + i))}
			var err error
			switch i % 4 {
			case 0:
				err = c.SetBlockedServices(context.Background(), "Kid phone", ids)
			case 1:
				err = c.MigrateFromGlobal(context.Background(), "Kid tablet", ids)
			case 2:
				err = c.AddBlockedServices(context.Background(), "Kid phone", ids)
			default:
				err = c.RemoveBlockedServices(context.Background(), "Kid tablet", ids)
			}
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	if n := s.MaxInFlightUpdates(); n != 1 {
		t.Errorf("MaxInFlightUpdates = %d, want 1 (writes must serialise)", n)
	}
	var seq []string
	for _, r := range s.Requests() {
		if r.Path == "/control/clients" || r.Path == "/control/clients/update" {
			seq = append(seq, r.Method)
		}
	}
	if n := s.CountRequests("GET", "/control/clients"); n != 10 {
		t.Fatalf("expected 10 rmw reads, got %d: %v", n, seq)
	}
	for i, m := range seq {
		if m == "POST" && (i == 0 || seq[i-1] != "GET") {
			t.Fatalf("rmw sequence interleaved: %v", seq)
		}
	}
}

func TestSetBlockedServices_Errors(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()

	s.SetStatus("/control/clients/update", 400)
	err := c.SetBlockedServices(ctx, "Kid phone", []string{"youtube"})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 400 || se.Snippet == "" {
		t.Errorf("400: err = %v, want *StatusError 400 with a snippet", err)
	}
	if errors.Is(err, ErrBadCredentials) {
		t.Errorf("400 on a service call must not be ErrBadCredentials: %v", err)
	}

	s.SetResponse("/control/clients/update", 0, nil)
	s.SetAuth(false)
	if err := c.SetBlockedServices(ctx, "Kid phone", []string{"youtube"}); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("SetAuth(false): err = %v, want ErrBadCredentials", err)
	}
}

func TestSetBlockedServices_GlobalWarn(t *testing.T) {
	s := newFake(t)
	var buf bytes.Buffer
	c := newClient(t, s, WithLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	if err := c.SetBlockedServices(context.Background(), "Kid tablet", []string{"youtube"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "use_global_blocked_services") {
		t.Errorf("expected a warn line naming use_global_blocked_services, got:\n%s", buf.String())
	}
	if s.LastUpdate() == nil {
		t.Error("the write must still happen for a use_global client")
	}
}

func TestClients_PartialFailure(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.SetStatus("/control/blocked_services/get", 401)
	res, err := c.Clients(context.Background())
	if !errors.Is(err, ErrBadCredentials) {
		t.Errorf("err = %v, want ErrBadCredentials from the second call", err)
	}
	if res.Persistent != nil || res.GlobalBlockedServices != nil {
		t.Errorf("partial result returned alongside the error: %+v", res)
	}
}

func TestMigrateFromGlobal_Body(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	if err := c.MigrateFromGlobal(context.Background(), "Kid tablet", []string{"tiktok", "roblox", "tiktok"}); err != nil {
		t.Fatal(err)
	}
	name, data := decodeUpdate(t, s.LastUpdate())
	if name != "Kid tablet" {
		t.Errorf("update name = %q, want Kid tablet", name)
	}
	if got := asAny(t, data["blocked_services"]); !reflect.DeepEqual(got, []any{"roblox", "tiktok"}) {
		t.Errorf("data.blocked_services = %v, want [roblox tiktok] (sorted, deduped)", got)
	}
	if string(data["use_global_blocked_services"]) != "false" {
		t.Errorf("data.use_global_blocked_services = %s, want false", data["use_global_blocked_services"])
	}

	orig := fixtureClient(t, "Kid tablet")
	for _, k := range []string{"blocked_services", "use_global_blocked_services"} {
		delete(orig, k)
		delete(data, k)
	}
	if len(orig) != len(data) {
		t.Errorf("field count: sent %d, read %d", len(data), len(orig))
	}
	for k, v := range orig {
		got, ok := data[k]
		if !ok {
			t.Errorf("field %q dropped from the update", k)
			continue
		}
		if !reflect.DeepEqual(asAny(t, got), asAny(t, v)) {
			t.Errorf("field %q changed: sent %s, read %s", k, got, v)
		}
	}
	if string(data["upstreams_cache_size"]) != "0" {
		t.Errorf("upstreams_cache_size = %s, want 0 preserved", data["upstreams_cache_size"])
	}
}

func TestMigrateFromGlobal_GlobalUntouched(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()
	if err := c.MigrateFromGlobal(ctx, "Kid tablet", []string{"tiktok", "roblox"}); err != nil {
		t.Fatal(err)
	}
	var gets, posts int
	for _, r := range s.Requests() {
		switch {
		case r.Method == "GET" && r.Path == "/control/clients":
			gets++
		case r.Method == "POST" && r.Path == "/control/clients/update":
			posts++
		case strings.HasPrefix(r.Path, "/control/blocked_services/"):
			t.Errorf("migration touched %s %s — the global list must never be written", r.Method, r.Path)
		}
	}
	if gets != 1 || posts != 1 {
		t.Errorf("requests: %d GET /control/clients, %d POST update; want 1 and 1", gets, posts)
	}
	res, err := c.Clients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.GlobalBlockedServices, []string{"tiktok", "roblox"}) {
		t.Errorf("global list after migration = %v, want the fixture [tiktok roblox]", res.GlobalBlockedServices)
	}
	if tab := find(res.Persistent, "Kid tablet"); tab == nil || tab.UseGlobalBlockedServices {
		t.Errorf("Kid tablet still uses the global list after migration: %+v", tab)
	}
}

func TestMigrateFromGlobal_NotFound(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()
	if err := c.MigrateFromGlobal(ctx, "Nobody", []string{"tiktok"}); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("Nobody: err = %v, want ErrClientNotFound", err)
	}
	if s.LastUpdate() != nil {
		t.Error("unknown client must not produce a write")
	}
	s.SetResponse("/control/clients", 200, []byte(`{"clients":[]}`))
	if err := c.MigrateFromGlobal(ctx, "Kid tablet", []string{"tiktok"}); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("empty client list: err = %v, want ErrClientNotFound", err)
	}
	if s.LastUpdate() != nil {
		t.Error("a client absent from the fresh read must not be written")
	}
}

func TestAddBlockedServices_Union(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.MutateClient("Kid phone", func(m map[string]json.RawMessage) {
		m["blocked_services"] = json.RawMessage(`["youtube","roblox"]`)
	})
	if err := c.AddBlockedServices(context.Background(), "Kid phone", []string{"tiktok", "youtube"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.BlockedServices("Kid phone")
	if want := []string{"roblox", "tiktok", "youtube"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after Add = %v, want %v (sorted union, not a replace)", got, want)
	}
	name, data := decodeUpdate(t, s.LastUpdate())
	if name != "Kid phone" || string(data["future_field"]) != "42" {
		t.Errorf("update name=%q future_field=%s; every other field must go back verbatim", name, data["future_field"])
	}
}

func TestAddBlockedServices_Idempotent(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()
	ids := []string{"roblox", "tiktok"}

	if err := c.AddBlockedServices(ctx, "Kid phone", ids); err != nil {
		t.Fatal(err)
	}
	first, _ := s.BlockedServices("Kid phone")
	if n := s.CountRequests("POST", "/control/clients/update"); n != 1 {
		t.Fatalf("first Add issued %d update POSTs, want 1", n)
	}
	gets := s.CountRequests("GET", "/control/clients")

	if err := c.AddBlockedServices(ctx, "Kid phone", ids); err != nil {
		t.Fatal(err)
	}
	second, _ := s.BlockedServices("Kid phone")
	if !reflect.DeepEqual(first, second) {
		t.Errorf("second Add changed the list: %v -> %v", first, second)
	}
	if n := s.CountRequests("POST", "/control/clients/update"); n != 1 {
		t.Errorf("second identical Add issued a POST (total %d, want 1) — the no-op skip is lost", n)
	}
	if n := s.CountRequests("GET", "/control/clients"); n != gets+1 {
		t.Errorf("second Add issued %d GETs, want exactly 1", n-gets)
	}

	if err := c.RemoveBlockedServices(ctx, "Kid phone", []string{"nonexistent", "another"}); err != nil {
		t.Fatal(err)
	}
	if n := s.CountRequests("POST", "/control/clients/update"); n != 1 {
		t.Errorf("Remove of absent ids issued a POST (total %d, want 1)", n)
	}
	if n := s.CountRequests("GET", "/control/clients"); n != gets+2 {
		t.Errorf("Remove issued %d GETs, want exactly 1", n-gets-1)
	}
}

func TestRemoveBlockedServices_Diff(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()

	if err := c.RemoveBlockedServices(ctx, "Kid phone", []string{"tiktok", "nonexistent"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.BlockedServices("Kid phone"); !reflect.DeepEqual(got, []string{"youtube"}) {
		t.Errorf("Kid phone after Remove = %v, want [youtube]", got)
	}
	_, data := decodeUpdate(t, s.LastUpdate())
	if string(data["future_field"]) != "42" {
		t.Errorf("future_field = %s, want 42 preserved", data["future_field"])
	}

	posts := s.CountRequests("POST", "/control/clients/update")
	if err := c.RemoveBlockedServices(ctx, "Old laptop", []string{"youtube"}); err != nil {
		t.Fatalf("Remove on a null-list client: %v", err)
	}
	if n := s.CountRequests("POST", "/control/clients/update"); n != posts {
		t.Errorf("Remove on a null list issued a POST; null and [] are the same empty set")
	}
	if got, ok := s.BlockedServices("Old laptop"); !ok || len(got) != 0 {
		t.Errorf("Old laptop = %v, %v; want [] untouched", got, ok)
	}
}

func TestRemoveAdd_NotFound(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()
	if err := c.RemoveBlockedServices(ctx, "Nobody", []string{"youtube"}); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("Remove err = %v, want ErrClientNotFound", err)
	}
	if err := c.AddBlockedServices(ctx, "Nobody", []string{"youtube"}); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("Add err = %v, want ErrClientNotFound", err)
	}
	if n := s.CountRequests("POST", "/control/clients/update"); n != 0 {
		t.Errorf("unknown client produced %d update POSTs, want 0", n)
	}
}
