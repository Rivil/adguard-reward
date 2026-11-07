package adguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	pathStatus   = "/control/filtering/status"
	pathSetRules = "/control/filtering/set_rules"
)

func appendLine(l string) func([]string) ([]string, error) {
	return func(cur []string) ([]string, error) { return append(cur, l), nil }
}

func TestEditUserRules_NoOp(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.SetUserRules([]string{"||a^", "", "@@||b^  "})

	changed, err := c.EditUserRules(bg, func(cur []string) ([]string, error) {
		return slices.Clone(cur), nil
	})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want false nil", changed, err)
	}
	if n := s.CountRequests("POST", pathSetRules); n != 0 {
		t.Fatalf("%d set_rules POSTs on a no-op edit", n)
	}

	// An empty list read as null is still a no-op when fn returns nil.
	s.SetUserRules(nil)
	if changed, err := c.EditUserRules(bg, func([]string) ([]string, error) { return nil, nil }); err != nil || changed {
		t.Fatalf("null -> nil: changed=%v err=%v", changed, err)
	}
	if n := s.CountRequests("POST", pathSetRules); n != 0 {
		t.Fatalf("%d set_rules POSTs on a null no-op", n)
	}
}

func TestEditUserRules_FailClosed(t *testing.T) {
	t.Run("read fails", func(t *testing.T) {
		s := newFake(t)
		c := newClient(t, s)
		s.SetStatus(pathStatus, 502)
		called := false
		_, err := c.EditUserRules(bg, func(cur []string) ([]string, error) {
			called = true
			return append(cur, "x"), nil
		})
		var se *StatusError
		if !errors.As(err, &se) || se.Code != 502 {
			t.Fatalf("err = %v, want *StatusError 502", err)
		}
		if called {
			t.Error("fn called after a failed read")
		}
		if n := s.CountRequests("POST", pathSetRules); n != 0 {
			t.Errorf("%d POSTs after a failed read", n)
		}
	})
	t.Run("fn fails", func(t *testing.T) {
		s := newFake(t)
		c := newClient(t, s)
		boom := errors.New("boom")
		changed, err := c.EditUserRules(bg, func(cur []string) ([]string, error) {
			return append(cur, "x"), boom
		})
		if !errors.Is(err, boom) || changed {
			t.Fatalf("changed=%v err=%v, want false boom", changed, err)
		}
		if n := s.CountRequests("POST", pathSetRules); n != 0 {
			t.Errorf("%d POSTs after fn failed", n)
		}
	})
}

func TestEditUserRules_Null(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.SetUserRules(nil)

	var seen []string
	changed, err := c.EditUserRules(bg, func(cur []string) ([]string, error) {
		seen = cur
		return append(cur, "! adguard-reward begin", "! adguard-reward end"), nil
	})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if seen == nil || len(seen) != 0 {
		t.Fatalf("fn saw %#v, want empty non-nil []string", seen)
	}
	if got, want := string(s.LastSetRules()), `{"rules":["! adguard-reward begin","! adguard-reward end"]}`; got != want {
		t.Fatalf("body %s, want %s", got, want)
	}

	// Clearing every rule posts [], never null.
	if _, err := c.EditUserRules(bg, func([]string) ([]string, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if got := string(s.LastSetRules()); got != `{"rules":[]}` {
		t.Fatalf("clearing body %s, want {\"rules\":[]}", got)
	}
}

func TestEditUserRules_Concurrent(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.EditUserRules(bg, appendLine(fmt.Sprintf("||line%d^", i))); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got := s.UserRules()
	if len(got) != n {
		t.Fatalf("%d lines survived, want %d: %q", len(got), n, got)
	}
	for i := range n {
		if !slices.Contains(got, fmt.Sprintf("||line%d^", i)) {
			t.Errorf("line %d lost", i)
		}
	}
	if m := s.MaxInFlightFiltering(); m != 1 {
		t.Fatalf("max in-flight on filtering paths = %d, want 1", m)
	}
}

func TestEditUserRules_Verbatim(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	orig := []string{"", "||a.com^   ", "\t# tab comment", "! ünïcödé ☃", "", "@@||b.com^$important", "||a.com^   "}
	s.SetUserRules(orig)

	if _, err := c.EditUserRules(bg, appendLine("||new^")); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Rules []string `json:"rules"`
	}
	raw := s.LastSetRules()
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if want := append(slices.Clone(orig), "||new^"); !reflect.DeepEqual(body.Rules, want) {
		t.Fatalf("posted %q\nwant   %q", body.Rules, want)
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("body carries null: %s", raw)
	}
}

func TestEditUserRules_WriteError(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.SetStatus(pathSetRules, 500)
	changed, err := c.EditUserRules(bg, appendLine("||x^"))
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 500 || se.Path != pathSetRules {
		t.Fatalf("err = %v, want *StatusError 500 on set_rules", err)
	}
	if changed {
		t.Error("changed=true on a failed write")
	}
}

func TestEditUserRules_WriteRejected(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	s.SetResponse(pathSetRules, 400, []byte("bad rule"))
	_, err := c.EditUserRules(bg, appendLine("||x^"))
	var ir *InputRejectedError
	if !errors.As(err, &ir) || ir.Message != "bad rule" {
		t.Fatalf("err = %v, want *InputRejectedError", err)
	}
}
