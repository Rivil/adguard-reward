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

func TestChildren_ClientOneOwner(t *testing.T) {
	s, _ := openTemp(t)
	mustCreate(t, s, "Ada", "Kid phone")
	_, err := s.CreateChild(ctx, "Ben", []string{"Kid phone", "Old laptop"})
	var taken *ErrClientTaken
	if !errors.As(err, &taken) {
		t.Fatalf("CreateChild(Ben) err = %v, want *ErrClientTaken", err)
	}
	if taken.Client != "Kid phone" || taken.ChildName != "Ada" {
		t.Fatalf("ErrClientTaken = %+v, want Client \"Kid phone\" ChildName \"Ada\"", taken)
	}
	all, _ := s.ListChildren(ctx)
	for _, c := range all {
		if c.Name == "Ben" {
			t.Fatal("Ben row exists after rejected create — tx did not roll back")
		}
	}
	if n := countClients(t, s, taken.ChildID); n != 1 {
		t.Fatalf("Ada has %d client rows, want 1", n)
	}
	// The PK on client_name is the safety net behind the SELECT check.
	rows, err := s.DB().Query(`PRAGMA index_list(child_clients)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	unique := false
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var origin string
		var isUnique int64
		for i, c := range cols {
			switch c {
			case "origin":
				origin, _ = vals[i].(string)
			case "unique":
				isUnique, _ = vals[i].(int64)
			}
		}
		if origin == "pk" && isUnique == 1 {
			unique = true
		}
	}
	if !unique {
		t.Fatal("child_clients has no unique primary-key index on client_name")
	}
}

func TestChildren_UpdateRollsBack(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	mustCreate(t, s, "Ben", "Old laptop")
	_, err := s.UpdateChild(ctx, ada.ID, "Renamed", []string{"Old laptop"})
	var taken *ErrClientTaken
	if !errors.As(err, &taken) {
		t.Fatalf("UpdateChild err = %v, want *ErrClientTaken", err)
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
