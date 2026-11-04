package store

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func mustCreate(t *testing.T, s *Store, name string, clients ...string) Child {
	t.Helper()
	c, err := s.CreateChild(ctx, name, clients)
	if err != nil {
		t.Fatalf("CreateChild(%q, %v): %v", name, clients, err)
	}
	return c
}

func mustGet(t *testing.T, s *Store, id int64) Child {
	t.Helper()
	c, err := s.GetChild(ctx, id)
	if err != nil {
		t.Fatalf("GetChild(%d): %v", id, err)
	}
	return c
}

func countClients(t *testing.T, s *Store, childID int64) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM child_clients WHERE child_id = ?`, childID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChildren_NameUnique(t *testing.T) {
	s, _ := openTemp(t)
	mustCreate(t, s, "Ada")
	if _, err := s.CreateChild(ctx, "ada", nil); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("CreateChild(\"ada\") err = %v, want ErrNameTaken", err)
	}
	all, err := s.ListChildren(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("ListChildren = %d rows, want 1", len(all))
	}
	ben := mustCreate(t, s, "Ben")
	if _, err := s.UpdateChild(ctx, ben.ID, "Ada", nil); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("UpdateChild(ben, \"Ada\") err = %v, want ErrNameTaken", err)
	}
	if got := mustGet(t, s, ben.ID); got.Name != "Ben" {
		t.Fatalf("after rejected rename Ben is %q", got.Name)
	}
}

// listClients is ListChildren folded to name -> clients.
func listClients(t *testing.T, s *Store) map[string][]string {
	t.Helper()
	all, err := s.ListChildren(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, c := range all {
		out[c.Name] = c.Clients
	}
	return out
}

func TestCreateChild_SharedClient(t *testing.T) {
	s, _ := openTemp(t)
	mustCreate(t, s, "Ada", "Kid phone")
	cleo, err := s.CreateChild(ctx, "Cleo", []string{"Kid phone"})
	if err != nil {
		t.Fatalf("CreateChild(Cleo, [Kid phone]) while Ada holds it: %v", err)
	}
	if cleo.Name != "Cleo" || !reflect.DeepEqual(cleo.Clients, []string{"Kid phone"}) {
		t.Fatalf("CreateChild = %+v, want Cleo [Kid phone]", cleo)
	}
	want := map[string][]string{"Ada": {"Kid phone"}, "Cleo": {"Kid phone"}}
	if got := listClients(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListChildren = %v, want %v", got, want)
	}
}

func TestUpdateChild_SharedClient(t *testing.T) {
	s, _ := openTemp(t)
	mustCreate(t, s, "Ada", "Kid phone")
	ben := mustCreate(t, s, "Ben", "Kid tablet")
	if _, err := s.UpdateChild(ctx, ben.ID, "Ben", []string{"Kid phone"}); err != nil {
		t.Fatalf("UpdateChild(Ben, [Kid phone]) while Ada holds it: %v", err)
	}
	want := map[string][]string{"Ada": {"Kid phone"}, "Ben": {"Kid phone"}}
	if got := listClients(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListChildren = %v, want %v", got, want)
	}
}

func TestUpdateChild_RemoveKeepsOtherOwner(t *testing.T) {
	s, _ := openTemp(t)
	mustCreate(t, s, "Ada", "Kid phone")
	ben := mustCreate(t, s, "Ben", "Kid phone", "Kid tablet")
	if _, err := s.UpdateChild(ctx, ben.ID, "Ben", []string{"Kid tablet"}); err != nil {
		t.Fatalf("UpdateChild(Ben without Kid phone): %v", err)
	}
	want := map[string][]string{"Ada": {"Kid phone"}, "Ben": {"Kid tablet"}}
	if got := listClients(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListChildren = %v, want %v", got, want)
	}
}

func TestDeleteChild_KeepsOtherOwner(t *testing.T) {
	s, _ := openTemp(t)
	mustCreate(t, s, "Ada", "Kid phone")
	ben := mustCreate(t, s, "Ben", "Kid phone")
	ok, err := s.DeleteChild(ctx, ben.ID)
	if err != nil || !ok {
		t.Fatalf("DeleteChild(Ben) = (%v, %v), want (true, nil)", ok, err)
	}
	want := map[string][]string{"Ada": {"Kid phone"}}
	if got := listClients(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("ListChildren = %v, want %v", got, want)
	}
	if n := countClients(t, s, ben.ID); n != 0 {
		t.Fatalf("child_clients rows for Ben after delete = %d, want 0", n)
	}
}

func TestChildren_UpdateRollsBack(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	mustCreate(t, s, "Ben", "Old laptop")
	_, err := s.UpdateChild(ctx, ada.ID, "ben", []string{"Old laptop"})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("UpdateChild err = %v, want ErrNameTaken", err)
	}
	got := mustGet(t, s, ada.ID)
	if got.Name != "Ada" || !reflect.DeepEqual(got.Clients, []string{"Kid phone"}) {
		t.Fatalf("after rejected update Ada = %+v, want name Ada clients [Kid phone]", got)
	}
}

func TestChildren_UpdateSameName(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	if _, err := s.UpdateChild(ctx, ada.ID, "Ada", ada.Clients); err != nil {
		t.Fatalf("UpdateChild with own name/clients: %v", err)
	}
	got, err := s.UpdateChild(ctx, ada.ID, "Ada", []string{"Kid phone", "Kid phone"})
	if err != nil {
		t.Fatalf("UpdateChild re-assigning own client: %v", err)
	}
	if !reflect.DeepEqual(got.Clients, []string{"Kid phone"}) {
		t.Fatalf("Clients = %v, want [Kid phone]", got.Clients)
	}
}

func TestChildren_UpdateReplaces(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone", "Kid tablet")
	if _, err := s.UpdateChild(ctx, ada.ID, "Ada M", []string{"Old laptop"}); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, s, ada.ID)
	if got.Name != "Ada M" || !reflect.DeepEqual(got.Clients, []string{"Old laptop"}) {
		t.Fatalf("after update = %+v, want Ada M [Old laptop]", got)
	}
	if n := countClients(t, s, ada.ID); n != 1 {
		t.Fatalf("child_clients rows = %d, want 1", n)
	}
}

func TestChildren_DeleteFrees(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	ok, err := s.DeleteChild(ctx, ada.ID)
	if err != nil || !ok {
		t.Fatalf("DeleteChild = (%v, %v), want (true, nil)", ok, err)
	}
	if n := countClients(t, s, ada.ID); n != 0 {
		t.Fatalf("child_clients rows after delete = %d, want 0 (cascade)", n)
	}
	if _, err := s.GetChild(ctx, ada.ID); !errors.Is(err, ErrChildNotFound) {
		t.Fatalf("GetChild after delete err = %v, want ErrChildNotFound", err)
	}
	if _, err := s.CreateChild(ctx, "Cy", []string{"Kid phone"}); err != nil {
		t.Fatalf("client not freed by delete: %v", err)
	}
	ok, err = s.DeleteChild(ctx, 999)
	if err != nil || ok {
		t.Fatalf("DeleteChild(999) = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestChildren_ClientsNormalised(t *testing.T) {
	s, _ := openTemp(t)
	got := mustCreate(t, s, "A", " Kid phone", "Kid phone", "", "Kid tablet")
	want := []string{"Kid phone", "Kid tablet"}
	if !reflect.DeepEqual(got.Clients, want) {
		t.Fatalf("Clients = %v, want %v", got.Clients, want)
	}
	if again := mustGet(t, s, got.ID); !reflect.DeepEqual(again.Clients, want) {
		t.Fatalf("stored Clients = %v, want %v", again.Clients, want)
	}
}

func TestChildren_ListShape(t *testing.T) {
	s, _ := openTemp(t)
	all, err := s.ListChildren(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if all == nil || len(all) != 0 {
		t.Fatalf("empty store ListChildren = %#v, want non-nil empty slice", all)
	}
	mustCreate(t, s, "ben")
	mustCreate(t, s, "Ada", "Kid phone")
	mustCreate(t, s, "Cy")
	all, err = s.ListChildren(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, c := range all {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"ben", "Ada", "Cy"}) {
		t.Fatalf("order = %v, want creation order [ben Ada Cy]", names)
	}
	if all[0].Clients == nil || len(all[0].Clients) != 0 {
		t.Fatalf("child with no clients has Clients = %#v, want []string{}", all[0].Clients)
	}
}

func TestChildren_ListRace(t *testing.T) {
	s, _ := openTemp(t)
	mustCreate(t, s, "Ada", "p1", "p2")
	cyClients := []string{"c1", "c2"}

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
				all, err := s.ListChildren(deadline)
				if err != nil {
					errs <- err
					return
				}
				for _, c := range all {
					switch c.Name {
					case "Ada":
						if !reflect.DeepEqual(c.Clients, []string{"p1", "p2"}) {
							errs <- errors.New("Ada read with clients " + join(c.Clients))
						}
					case "Cy":
						// Cy exists only as a whole: pre-cycle it is absent,
						// post-create it owns both clients. Anything else is a torn read.
						if !reflect.DeepEqual(c.Clients, cyClients) {
							errs <- errors.New("Cy read mid-write with clients " + join(c.Clients))
						}
					default:
						errs <- errors.New("unexpected child " + c.Name)
					}
				}
			}
		}()
	}

	writeErr := make(chan error, 1)
	go func() {
		defer close(stop)
		for i := 0; i < 20; i++ {
			cy, err := s.CreateChild(deadline, "Cy", cyClients)
			if err != nil {
				writeErr <- err
				return
			}
			if _, err := s.DeleteChild(deadline, cy.ID); err != nil {
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

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return "[" + out + "]"
}

func TestChildren_Persist(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ada := mustCreate(t, s, "Ada", "Kid phone", "Kid tablet")
	ben := mustCreate(t, s, "Ben")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	all, err := s.ListChildren(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all, []Child{ada, ben}) {
		t.Fatalf("after reopen = %+v, want %+v", all, []Child{ada, ben})
	}
	applied := queryStrings(t, s.DB(), `SELECT name FROM schema_migrations WHERE name = ?`, "0002_children.sql")
	if len(applied) != 1 {
		t.Fatalf("schema_migrations lists 0002_children.sql %d times, want 1", len(applied))
	}
}

func TestChildren_NotFound(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.GetChild(ctx, 999); !errors.Is(err, ErrChildNotFound) {
		t.Fatalf("GetChild(999) err = %v, want ErrChildNotFound", err)
	}
	if _, err := s.UpdateChild(ctx, 999, "X", nil); !errors.Is(err, ErrChildNotFound) {
		t.Fatalf("UpdateChild(999) err = %v, want ErrChildNotFound", err)
	}
}

func historyWatermark(t *testing.T, s *Store, childID int64) int64 {
	t.Helper()
	var w int64
	if err := s.DB().QueryRow(`SELECT history_after_grant_id FROM children WHERE id = ?`, childID).Scan(&w); err != nil {
		t.Fatalf("history_after_grant_id of %d: %v", childID, err)
	}
	return w
}

func TestCreateChild_HistoryWatermark(t *testing.T) {
	t.Run("no grants", func(t *testing.T) {
		s, _ := openTemp(t)
		c := mustCreate(t, s, "Ada")
		if w := historyWatermark(t, s, c.ID); w != 0 {
			t.Fatalf("watermark with no grants = %d, want 0", w)
		}
	})

	t.Run("upgrade then create", func(t *testing.T) {
		dir := t.TempDir()
		openAt0008(t, dir) // child 1 with grants 1..3
		old, err := openWith(dir, nil, migrationsThrough(t, "0008_service_toggles.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.DB().Exec(`INSERT INTO grants (id, child_id, status, started_at, ends_at, ended_at)
		    VALUES (7, 1, 'expired', 7000, 7600, 7600)`); err != nil {
			t.Fatal(err)
		}
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}

		s, err := Open(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if w := historyWatermark(t, s, 1); w != 0 {
			t.Fatalf("pre-0009 child watermark = %d, want 0 (sees all its grants)", w)
		}
		ben := mustCreate(t, s, "Ben")
		if w := historyWatermark(t, s, ben.ID); w != 7 {
			t.Fatalf("new child watermark with grants up to id 7 = %d, want 7", w)
		}
		// A rejected create leaves no row and writes no watermark.
		if _, err := s.CreateChild(ctx, "ben", nil); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("duplicate name err = %v, want ErrNameTaken", err)
		}
	})
}
