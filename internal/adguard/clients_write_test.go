package adguard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

var bg = context.Background()

func addBody(t *testing.T, s interface{ AddedClients() [][]byte }) map[string]json.RawMessage {
	t.Helper()
	added := s.AddedClients()
	if len(added) == 0 {
		t.Fatal("no accepted add")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(added[len(added)-1], &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAddClient_Body(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)

	if err := c.AddClient(bg, NewClient{Name: "TV", IDs: []string{"192.168.1.30"}}); err != nil {
		t.Fatal(err)
	}
	b := addBody(t, s)
	for k, want := range map[string]string{
		"name": `"TV"`, "ids": `["192.168.1.30"]`, "use_global_settings": "true",
		"use_global_blocked_services": "true", "blocked_services": "[]",
	} {
		if string(b[k]) != want {
			t.Errorf("Blocked=nil: %s = %s, want %s", k, b[k], want)
		}
	}

	blocked := []string{"youtube", "tiktok", "youtube"}
	if err := c.AddClient(bg, NewClient{Name: "Den", IDs: []string{"192.168.1.31"}, Blocked: &blocked}); err != nil {
		t.Fatal(err)
	}
	b = addBody(t, s)
	if string(b["use_global_settings"]) != "true" || string(b["use_global_blocked_services"]) != "false" {
		t.Errorf("Blocked set: flags = %s / %s, want true / false", b["use_global_settings"], b["use_global_blocked_services"])
	}
	if string(b["blocked_services"]) != `["tiktok","youtube"]` {
		t.Errorf("Blocked set: blocked_services = %s, want sorted deduped", b["blocked_services"])
	}

	empty := []string{}
	if err := c.AddClient(bg, NewClient{Name: "Bare", IDs: []string{"192.168.1.32"}, Blocked: &empty}); err != nil {
		t.Fatal(err)
	}
	if b = addBody(t, s); string(b["blocked_services"]) != "[]" || string(b["use_global_blocked_services"]) != "false" {
		t.Errorf("Blocked=&[]: blocked_services %s flag %s, want [] false", b["blocked_services"], b["use_global_blocked_services"])
	}
	if got, ok := s.BlockedServices("Den"); !ok || !reflect.DeepEqual(got, []string{"tiktok", "youtube"}) {
		t.Errorf("fake's Den blocks %v, %v", got, ok)
	}
}

func TestAddClient_DuplicateName(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	err := c.AddClient(bg, NewClient{Name: "Kid phone", IDs: []string{"192.168.1.99"}})
	if !errors.Is(err, ErrClientExists) {
		t.Errorf("err = %v, want ErrClientExists", err)
	}
	if n := s.CountRequests("POST", "/control/clients/add"); n != 0 {
		t.Errorf("POST /control/clients/add = %d, want 0", n)
	}
}

func TestAddClient_IDTaken(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	for _, id := range []string{"192.168.1.50", "AA-BB-CC-DD-EE-01"} {
		err := c.AddClient(bg, NewClient{Name: "Spare", IDs: []string{"10.0.0.1", id}})
		var taken *ErrIDTaken
		if !errors.As(err, &taken) || taken.Owner != "Kid phone" || taken.ID != id {
			t.Errorf("id %s: err = %v, want *ErrIDTaken{Owner: Kid phone}", id, err)
		}
	}
	if n := s.CountRequests("POST", "/control/clients/add"); n != 0 {
		t.Errorf("POST /control/clients/add = %d, want 0", n)
	}
}

func TestAddClient_Rejected(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.SetResponse("/control/clients/add", 400, []byte("invalid client: bad id \"x\""))
	err := c.AddClient(bg, NewClient{Name: "TV", IDs: []string{"x"}})
	var rej *InputRejectedError
	if !errors.As(err, &rej) || rej.Message != `invalid client: bad id "x"` {
		t.Fatalf("err = %#v, want *InputRejectedError with AdGuard's message", err)
	}
	var se *StatusError
	if errors.As(err, &se) {
		t.Error("an input rejection must not read as *StatusError (unavailable)")
	}

	s.SetStatus("/control/clients/add", 500)
	err = c.AddClient(bg, NewClient{Name: "TV", IDs: []string{"192.168.1.30"}})
	if !errors.As(err, &se) || se.Code != 500 || errors.As(err, &rej) {
		t.Errorf("500: err = %v, want *StatusError 500", err)
	}
}

func TestRenameClient_PreservesRaw(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	if err := c.RenameClient(bg, "Kid phone", "Ada phone"); err != nil {
		t.Fatal(err)
	}
	name, data := decodeUpdate(t, s.LastUpdate())
	if name != "Kid phone" {
		t.Errorf("update name = %q, want the old name", name)
	}
	if string(data["name"]) != `"Ada phone"` {
		t.Errorf("data.name = %s, want \"Ada phone\"", data["name"])
	}
	if string(data["future_field"]) != "42" {
		t.Errorf("data.future_field = %s, want 42", data["future_field"])
	}
	orig := fixtureClient(t, "Kid phone")
	if len(data) != len(orig) {
		t.Errorf("data has %d keys, fixture %d", len(data), len(orig))
	}
	for k, v := range orig {
		if k == "name" {
			continue
		}
		if !reflect.DeepEqual(asAny(t, data[k]), asAny(t, v)) {
			t.Errorf("data.%s = %s, want the fixture's %s", k, data[k], v)
		}
	}
	res, err := c.Clients(bg)
	if err != nil {
		t.Fatal(err)
	}
	if find(res.Persistent, "Ada phone") == nil || find(res.Persistent, "Kid phone") != nil {
		t.Error("Clients() after rename should list Ada phone and not Kid phone")
	}
	if err := c.RenameClient(bg, "Ada phone", "Ada phone"); err != nil {
		t.Errorf("same-name rename: %v", err)
	}
	if n := s.CountRequests("POST", "/control/clients/update"); n != 1 {
		t.Errorf("updates = %d, want 1 (same-name rename writes nothing)", n)
	}
}

func TestRenameClient_Errors(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	if err := c.RenameClient(bg, "Kid phone", "Kid tablet"); !errors.Is(err, ErrClientExists) {
		t.Errorf("onto existing: err = %v, want ErrClientExists", err)
	}
	if err := c.RenameClient(bg, "Nobody", "Somebody"); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("unknown old: err = %v, want ErrClientNotFound", err)
	}
	if n := s.CountRequests("POST", "/control/clients/update"); n != 0 {
		t.Errorf("updates = %d, want 0", n)
	}
}

func TestAdopt_InheritsGlobal(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	if err := c.AdoptBlockedServices(bg, "Kid tablet", []string{"youtube"}); err != nil {
		t.Fatal(err)
	}
	_, data := decodeUpdate(t, s.LastUpdate())
	if string(data["blocked_services"]) != `["roblox","tiktok","youtube"]` {
		t.Errorf("blocked_services = %s, want global ∪ youtube sorted", data["blocked_services"])
	}
	if string(data["use_global_blocked_services"]) != "false" {
		t.Errorf("use_global_blocked_services = %s, want false", data["use_global_blocked_services"])
	}

	// Already on its own list, adding nothing new, still writes nothing more.
	before := s.CountRequests("POST", "/control/clients/update")
	if err := c.AdoptBlockedServices(bg, "Kid tablet", []string{"tiktok"}); err != nil {
		t.Fatal(err)
	}
	if n := s.CountRequests("POST", "/control/clients/update"); n != before {
		t.Errorf("second adopt wrote %d updates, want 0", n-before)
	}
}

func TestAdopt_Superset(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	if err := c.AdoptBlockedServices(bg, "Kid phone", []string{"tiktok"}); err != nil {
		t.Fatal(err)
	}
	if n := s.CountRequests("POST", "/control/clients/update"); n != 0 {
		t.Errorf("updates = %d, want 0", n)
	}
	if err := c.AdoptBlockedServices(bg, "Kid phone", []string{"roblox"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.BlockedServices("Kid phone"); !reflect.DeepEqual(got, []string{"roblox", "tiktok", "youtube"}) {
		t.Errorf("Kid phone blocks %v, want youtube kept and roblox added", got)
	}
	if err := c.AdoptBlockedServices(bg, "Ghost", []string{"roblox"}); !errors.Is(err, ErrClientNotFound) {
		t.Errorf("unknown client: err = %v, want ErrClientNotFound", err)
	}
}

func TestWrites_Serialised(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.Hang("/control/clients/add", 200*time.Millisecond)
	s.Hang("/control/clients/update", 200*time.Millisecond)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = c.AddClient(bg, NewClient{Name: "TV", IDs: []string{"192.168.1.30"}})
	}()
	go func() {
		defer wg.Done()
		errs[1] = c.AddBlockedServices(bg, "Kid phone", []string{"roblox"})
	}()
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := s.MaxInFlightUpdates(); got != 1 {
		t.Errorf("MaxInFlightUpdates = %d, want 1", got)
	}
	if n := s.CountRequests(http.MethodPost, "/control/clients/add"); n != 1 {
		t.Errorf("adds = %d, want 1", n)
	}
}
