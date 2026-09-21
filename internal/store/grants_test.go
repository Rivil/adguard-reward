package store

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)

func mustGrant(t *testing.T, s *Store, childID int64, services, clients []string) Grant {
	t.Helper()
	g, err := s.CreateGrant(ctx, childID, services, clients, t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatalf("CreateGrant(%d, %v, %v): %v", childID, services, clients, err)
	}
	return g
}

func mustGetGrant(t *testing.T, s *Store, id int64) Grant {
	t.Helper()
	g, err := s.GetGrant(ctx, id)
	if err != nil {
		t.Fatalf("GetGrant(%d): %v", id, err)
	}
	return g
}

func mustSetStatus(t *testing.T, s *Store, id int64, from, to string) {
	t.Helper()
	ok, err := s.SetGrantStatus(ctx, id, from, to, t0.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("SetGrantStatus(%d, %s, %s) = %v, %v; want true, nil", id, from, to, ok, err)
	}
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGrants_OverlapPerChildService(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	ben := mustCreate(t, s, "Ben", "Kid tablet")

	first := mustGrant(t, s, ada.ID, []string{"youtube", "tiktok"}, ada.Clients)
	_, err := s.CreateGrant(ctx, ada.ID, []string{"tiktok"}, ada.Clients, t0, t0.Add(time.Hour))
	var overlap *ErrGrantOverlap
	if !errors.As(err, &overlap) {
		t.Fatalf("second CreateGrant err = %v, want *ErrGrantOverlap", err)
	}
	if overlap.ExistingID != first.ID || overlap.ServiceID != "tiktok" {
		t.Fatalf("ErrGrantOverlap = %+v, want ExistingID %d ServiceID tiktok", overlap, first.ID)
	}
	if n := countRows(t, s, "grants"); n != 1 {
		t.Fatalf("grants rows = %d, want 1", n)
	}
	if _, err := s.CreateGrant(ctx, ben.ID, []string{"tiktok"}, ben.Clients, t0, t0.Add(time.Hour)); err != nil {
		t.Fatalf("CreateGrant(ben, tiktok): %v", err)
	}

	rows, err := s.DB().Query(`PRAGMA index_list('grant_services')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var (
			seq, unique, partial int
			name, origin         string
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if name == "grant_services_live_idx" {
			found = true
			if unique != 1 || partial != 1 {
				t.Fatalf("grant_services_live_idx unique=%d partial=%d, want 1/1", unique, partial)
			}
		}
	}
	if !found {
		t.Fatal("grant_services_live_idx missing from PRAGMA index_list")
	}
}

func TestGrants_OverlapClears(t *testing.T) {
	for _, to := range []string{StatusExpired, StatusEnded} {
		t.Run(to, func(t *testing.T) {
			s, _ := openTemp(t)
			ada := mustCreate(t, s, "Ada", "Kid phone")
			g := mustGrant(t, s, ada.ID, []string{"tiktok"}, ada.Clients)
			mustSetStatus(t, s, g.ID, StatusActive, to)
			if _, err := s.CreateGrant(ctx, ada.ID, []string{"tiktok"}, ada.Clients, t0, t0.Add(time.Hour)); err != nil {
				t.Fatalf("CreateGrant after %s: %v", to, err)
			}
		})
	}
}

func TestGrants_StatusCAS(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	g := mustGrant(t, s, ada.ID, []string{"tiktok"}, ada.Clients)

	ok, err := s.SetGrantStatus(ctx, g.ID, StatusActive, StatusExpired, t0)
	if err != nil || !ok {
		t.Fatalf("first CAS = %v, %v; want true, nil", ok, err)
	}
	ok, err = s.SetGrantStatus(ctx, g.ID, StatusActive, StatusExpired, t0)
	if err != nil || ok {
		t.Fatalf("repeat CAS = %v, %v; want false, nil", ok, err)
	}
	ok, err = s.SetGrantStatus(ctx, g.ID, StatusActive, StatusEnded, t0)
	if err != nil || ok {
		t.Fatalf("active→ended on expired = %v, %v; want false, nil", ok, err)
	}
	if got := mustGetGrant(t, s, g.ID).Status; got != StatusExpired {
		t.Fatalf("status = %q, want expired", got)
	}
	ok, err = s.SetGrantStatus(ctx, 999, StatusActive, StatusExpired, t0)
	if err != nil || ok {
		t.Fatalf("CAS on 999 = %v, %v; want false, nil", ok, err)
	}
}

func TestGrants_ListActiveOnly(t *testing.T) {
	s, _ := openTemp(t)
	empty, err := s.ListActiveGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty store ListActiveGrants = %#v, want non-nil empty slice", empty)
	}

	ada := mustCreate(t, s, "Ada", "Kid phone", "Kid tablet")
	expired := mustGrant(t, s, ada.ID, []string{"tiktok"}, ada.Clients)
	ended := mustGrant(t, s, ada.ID, []string{"roblox"}, ada.Clients)
	mustSetStatus(t, s, expired.ID, StatusActive, StatusExpired)
	mustSetStatus(t, s, ended.ID, StatusActive, StatusEnded)
	active := mustGrant(t, s, ada.ID, []string{"youtube", "tiktok"}, ada.Clients)

	got, err := s.ListActiveGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []Grant{active}) {
		t.Fatalf("ListActiveGrants = %+v, want [%+v]", got, active)
	}
	if !reflect.DeepEqual(active.Services, []string{"tiktok", "youtube"}) ||
		!reflect.DeepEqual(active.Clients, []string{"Kid phone", "Kid tablet"}) {
		t.Fatalf("active grant lists = %v / %v", active.Services, active.Clients)
	}

	ben := mustCreate(t, s, "Ben", "Ben laptop")
	second := mustGrant(t, s, ben.ID, []string{"tiktok"}, ben.Clients)
	got, _ = s.ListActiveGrants(ctx)
	if len(got) != 2 || got[0].ID != active.ID || got[1].ID != second.ID {
		t.Fatalf("ListActiveGrants ids = %v, want [%d %d]", ids2(got), active.ID, second.ID)
	}
}

func ids2(gs []Grant) []int64 {
	out := make([]int64, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.ID)
	}
	return out
}

func TestGrants_ExtendActiveOnly(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	active := mustGrant(t, s, ada.ID, []string{"tiktok"}, ada.Clients)
	ended := mustGrant(t, s, ada.ID, []string{"roblox"}, ada.Clients)
	mustSetStatus(t, s, ended.ID, StatusActive, StatusEnded)

	want := t0.Add(30 * time.Minute)
	got, err := s.ExtendGrant(ctx, active.ID, want)
	if err != nil {
		t.Fatalf("ExtendGrant: %v", err)
	}
	if !got.EndsAt.Equal(want) {
		t.Fatalf("ExtendGrant EndsAt = %v, want %v", got.EndsAt, want)
	}
	if again := mustGetGrant(t, s, active.ID); !again.EndsAt.Equal(want) {
		t.Fatalf("GetGrant EndsAt = %v, want %v", again.EndsAt, want)
	}
	if _, err := s.ExtendGrant(ctx, ended.ID, want); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("ExtendGrant(ended) err = %v, want ErrGrantNotFound", err)
	}
	if _, err := s.ExtendGrant(ctx, 999, want); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("ExtendGrant(999) err = %v, want ErrGrantNotFound", err)
	}
	if got := mustGetGrant(t, s, ended.ID); !got.EndsAt.Equal(ended.EndsAt) {
		t.Fatalf("ended EndsAt changed to %v", got.EndsAt)
	}
}

func TestGrants_ClientsFrozen(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone", "Kid tablet")
	g := mustGrant(t, s, ada.ID, []string{"tiktok"}, []string{"Kid phone", "Kid tablet"})

	ok, err := s.DeleteChild(ctx, ada.ID)
	if err != nil || !ok {
		t.Fatalf("DeleteChild = %v, %v", ok, err)
	}
	want := []string{"Kid phone", "Kid tablet"}
	if got := mustGetGrant(t, s, g.ID); !reflect.DeepEqual(got.Clients, want) || got.ChildID != ada.ID {
		t.Fatalf("GetGrant after DeleteChild = %+v, want clients %v child %d", got, want, ada.ID)
	}
	all, err := s.ListActiveGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !reflect.DeepEqual(all[0].Clients, want) || all[0].ChildID != ada.ID {
		t.Fatalf("ListActiveGrants after DeleteChild = %+v", all)
	}
}

func TestGrants_CreateRollsBack(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	mustGrant(t, s, ada.ID, []string{"tiktok"}, ada.Clients)
	before := [3]int{countRows(t, s, "grants"), countRows(t, s, "grant_services"), countRows(t, s, "grant_clients")}

	_, err := s.CreateGrant(ctx, ada.ID, []string{"roblox", "tiktok"}, []string{"Kid phone", "Kid tablet"}, t0, t0.Add(time.Hour))
	var overlap *ErrGrantOverlap
	if !errors.As(err, &overlap) || overlap.ServiceID != "tiktok" {
		t.Fatalf("CreateGrant err = %v, want overlap on tiktok", err)
	}
	after := [3]int{countRows(t, s, "grants"), countRows(t, s, "grant_services"), countRows(t, s, "grant_clients")}
	if before != after {
		t.Fatalf("rows before %v after %v — rejected create left rows behind", before, after)
	}
}

func TestGrants_CreatePersist(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ada := mustCreate(t, s, "Ada", "Kid phone")
	start := t0.Add(1500 * time.Millisecond)
	end := t0.Add(time.Hour + 700*time.Millisecond)
	g, err := s.CreateGrant(ctx, ada.ID, []string{"youtube", "tiktok", "youtube"}, []string{" Kid phone", "Kid phone"}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Services, []string{"tiktok", "youtube"}) || !reflect.DeepEqual(g.Clients, []string{"Kid phone"}) {
		t.Fatalf("normalised lists = %v / %v", g.Services, g.Clients)
	}
	if !g.StartedAt.Equal(start.Truncate(time.Second)) || !g.EndsAt.Equal(end.Truncate(time.Second)) {
		t.Fatalf("times = %v / %v, want second-truncated %v / %v", g.StartedAt, g.EndsAt, start, end)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	all, err := s.ListActiveGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all, []Grant{g}) {
		t.Fatalf("after reopen = %+v, want [%+v]", all, g)
	}
	applied := queryStrings(t, s.DB(), `SELECT name FROM schema_migrations WHERE name = ?`, "0003_grants.sql")
	if len(applied) != 1 {
		t.Fatalf("schema_migrations lists 0003_grants.sql %d times, want 1", len(applied))
	}
}

func TestGrants_EmptyLists(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada")
	g := mustGrant(t, s, ada.ID, []string{}, []string{})
	for _, got := range []Grant{g, mustGetGrant(t, s, g.ID)} {
		if got.Services == nil || len(got.Services) != 0 || got.Clients == nil || len(got.Clients) != 0 {
			t.Fatalf("lists = %#v / %#v, want []string{} each", got.Services, got.Clients)
		}
	}
	all, _ := s.ListActiveGrants(ctx)
	if len(all) != 1 || all[0].Services == nil || all[0].Clients == nil {
		t.Fatalf("ListActiveGrants = %#v, want one grant with non-nil empty lists", all)
	}
}

func TestGrants_ListRace(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "p1", "p2")
	cy := mustCreate(t, s, "Cy", "c1", "c2")
	adaGrant := mustGrant(t, s, ada.ID, []string{"a", "b"}, ada.Clients)
	cyServices := []string{"x", "y", "z"}

	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	stop := make(chan struct{})

	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				all, err := s.ListActiveGrants(deadline)
				if err != nil {
					errs <- err
					return
				}
				for _, g := range all {
					switch g.ChildID {
					case ada.ID:
						if !reflect.DeepEqual(g, adaGrant) {
							errs <- errors.New("Ada grant read as " + join(g.Services) + join(g.Clients))
						}
					case cy.ID:
						// Cy's grant exists only as a whole: absent, or with all
						// three services and both clients. Anything else is a torn read.
						if !reflect.DeepEqual(g.Services, cyServices) || !reflect.DeepEqual(g.Clients, cy.Clients) {
							errs <- errors.New("Cy grant read mid-write as " + join(g.Services) + join(g.Clients))
						}
					default:
						errs <- errors.New("unexpected grant child")
					}
				}
			}
		}()
	}

	writeErr := make(chan error, 1)
	go func() {
		defer close(stop)
		for i := 0; i < 20; i++ {
			g, err := s.CreateGrant(deadline, cy.ID, cyServices, cy.Clients, t0, t0.Add(time.Hour))
			if err != nil {
				writeErr <- err
				return
			}
			if _, err := s.SetGrantStatus(deadline, g.ID, StatusActive, StatusExpired, t0); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-deadline.Done():
		t.Fatal("readers did not finish within 5s — a nested query on the single connection deadlocks")
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("writer: %v", err)
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
