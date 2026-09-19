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

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := c.SetBlockedServices(context.Background(), "Kid phone", []string{"svc", string(rune('a' + i))}); err != nil {
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
	if len(seq) != 10 {
		t.Fatalf("expected 10 rmw requests, got %d: %v", len(seq), seq)
	}
	for i := 0; i < len(seq); i += 2 {
		if seq[i] != "GET" || seq[i+1] != "POST" {
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
