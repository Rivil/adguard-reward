package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func btn(childID int64, label string, services ...string) Button {
	return Button{ChildID: childID, Label: label, Services: services, Duration: time.Hour}
}

func mustReplace(t *testing.T, s *Store, in []Button) []Button {
	t.Helper()
	out, err := s.ReplaceButtons(ctx, in)
	if err != nil {
		t.Fatalf("ReplaceButtons(%+v): %v", in, err)
	}
	return out
}

func mustList(t *testing.T, s *Store) []Button {
	t.Helper()
	out, err := s.ListButtons(ctx)
	if err != nil {
		t.Fatalf("ListButtons: %v", err)
	}
	return out
}

func labels(bs []Button) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Label)
	}
	return out
}

func TestButtons_ReplaceAtomic(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada")
	before := mustReplace(t, s, []Button{btn(ada.ID, "A", "youtube"), btn(ada.ID, "B", "tiktok")})

	_, err := s.ReplaceButtons(ctx, []Button{btn(ada.ID, "C", "x"), btn(999, "D", "y")})
	var unknown *ErrUnknownChild
	if !errors.As(err, &unknown) || unknown.ChildID != 999 {
		t.Fatalf("ReplaceButtons with child 999 err = %v, want *ErrUnknownChild{999}", err)
	}
	if got := mustList(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("after unknown-child replace list = %+v, want untouched %+v", got, before)
	}

	zero := btn(ada.ID, "E", "z")
	zero.Duration = 0
	_, err = s.ReplaceButtons(ctx, []Button{btn(ada.ID, "C", "x"), zero})
	if err == nil {
		t.Fatal("ReplaceButtons with Duration 0 succeeded, want CHECK failure")
	}
	if errors.As(err, &unknown) {
		t.Fatalf("Duration 0 err = %v, want a constraint error not *ErrUnknownChild", err)
	}
	if got := mustList(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("after CHECK-failed replace list = %+v, want untouched %+v", got, before)
	}
}

func TestButtons_OrderPersists(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ada := mustCreate(t, s, "Ada")
	ben := mustCreate(t, s, "Ben")
	in := []Button{
		{ChildID: ada.ID, Label: "C", Services: []string{"youtube"}, Duration: 30 * time.Minute},
		{ChildID: ben.ID, Label: "A", Services: []string{"tiktok", "youtube"}, Duration: time.Hour},
		{ChildID: ada.ID, Label: "B", Services: []string{"roblox"}, Duration: 24 * time.Hour},
	}
	stored := mustReplace(t, s, in)
	if got := labels(stored); !reflect.DeepEqual(got, []string{"C", "A", "B"}) {
		t.Fatalf("labels = %v, want [C A B]", got)
	}
	for i := 1; i < len(stored); i++ {
		if stored[i].ID <= stored[i-1].ID {
			t.Fatalf("ids not ascending: %v", stored)
		}
	}
	for i := range in {
		if stored[i].ChildID != in[i].ChildID || stored[i].Duration != in[i].Duration ||
			!reflect.DeepEqual(stored[i].Services, in[i].Services) {
			t.Fatalf("stored[%d] = %+v, want fields of %+v", i, stored[i], in[i])
		}
	}
	if got := mustList(t, s); !reflect.DeepEqual(got, stored) {
		t.Fatalf("List = %+v, want %+v", got, stored)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := mustList(t, s); !reflect.DeepEqual(got, stored) {
		t.Fatalf("after reopen = %+v, want %+v", got, stored)
	}
	applied := queryStrings(t, s.DB(), `SELECT name FROM schema_migrations WHERE name = ?`, "0004_buttons.sql")
	if len(applied) != 1 {
		t.Fatalf("schema_migrations lists 0004_buttons.sql %d times, want 1", len(applied))
	}
	names := queryStrings(t, s.DB(), `SELECT name FROM schema_migrations ORDER BY applied_at ASC, name ASC`)
	i3, i4 := -1, -1
	for i, n := range names {
		switch n {
		case "0003_grants.sql":
			i3 = i
		case "0004_buttons.sql":
			i4 = i
		}
	}
	if i3 < 0 || i4 < 0 || i4 <= i3 {
		t.Fatalf("migration order = %v, want 0004_buttons.sql after 0003_grants.sql", names)
	}
}

func TestButtons_ServicesOrderKept(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada")
	stored := mustReplace(t, s, []Button{
		btn(ada.ID, "dup", "youtube", "tiktok", "youtube"),
		btn(ada.ID, "rev", "tiktok", "youtube"),
	})
	if !reflect.DeepEqual(stored[0].Services, []string{"youtube", "tiktok"}) {
		t.Fatalf("deduped services = %v, want [youtube tiktok]", stored[0].Services)
	}
	if !reflect.DeepEqual(stored[1].Services, []string{"tiktok", "youtube"}) {
		t.Fatalf("services = %v, want given order [tiktok youtube]", stored[1].Services)
	}
	listed := mustList(t, s)
	if !reflect.DeepEqual(listed, stored) {
		t.Fatalf("List = %+v, want %+v", listed, stored)
	}
}

func TestButtons_ChildCascade(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada")
	ben := mustCreate(t, s, "Ben")
	mustReplace(t, s, []Button{
		btn(ada.ID, "A1", "youtube", "tiktok"),
		btn(ben.ID, "B1", "roblox"),
		btn(ada.ID, "A2", "netflix"),
	})
	ok, err := s.DeleteChild(ctx, ada.ID)
	if err != nil || !ok {
		t.Fatalf("DeleteChild(ada) = %v, %v; want true, nil", ok, err)
	}
	got := mustList(t, s)
	if len(got) != 1 || got[0].Label != "B1" || got[0].ChildID != ben.ID {
		t.Fatalf("after cascade List = %+v, want only ben's B1", got)
	}
	if n := countRows(t, s, "button_services"); n != 1 {
		t.Fatalf("button_services rows = %d, want 1 (ben's roblox only)", n)
	}
}

func TestButtons_ReplaceEmpty(t *testing.T) {
	s, _ := openTemp(t)
	if got := mustList(t, s); got == nil || len(got) != 0 {
		t.Fatalf("fresh List = %#v, want []Button{}", got)
	}
	ada := mustCreate(t, s, "Ada")
	mustReplace(t, s, []Button{btn(ada.ID, "A", "youtube")})
	out, err := s.ReplaceButtons(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || len(out) != 0 {
		t.Fatalf("Replace(nil) = %#v, want []Button{}", out)
	}
	if n := countRows(t, s, "button_services"); n != 0 {
		t.Fatalf("button_services rows after Replace(nil) = %d, want 0", n)
	}
	noSvc := Button{ChildID: ada.ID, Label: "bare", Services: []string{}, Duration: time.Minute}
	for _, got := range [][]Button{mustReplace(t, s, []Button{noSvc}), mustList(t, s)} {
		if len(got) != 1 || got[0].Services == nil || len(got[0].Services) != 0 {
			t.Fatalf("button with no services read back as %#v, want Services []string{}", got)
		}
	}
	nilSvc := Button{ChildID: ada.ID, Label: "nil", Services: nil, Duration: time.Minute}
	if got := mustReplace(t, s, []Button{nilSvc}); got[0].Services == nil {
		t.Fatalf("nil Services stored and read back nil, want []string{}")
	}
}

func TestButtons_ChildCheckInTx(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada")
	mustReplace(t, s, []Button{btn(ada.ID, "A", "youtube")})

	done := make(chan error, 1)
	go func() {
		_, err := s.DeleteChild(ctx, ada.ID)
		done <- err
	}()
	if err := <-done; err != nil {
		t.Fatalf("DeleteChild: %v", err)
	}

	_, err := s.ReplaceButtons(ctx, []Button{btn(ada.ID, "A", "youtube")})
	var unknown *ErrUnknownChild
	if !errors.As(err, &unknown) || unknown.ChildID != ada.ID {
		t.Fatalf("Replace after child deleted err = %v, want *ErrUnknownChild{%d}", err, ada.ID)
	}
	if strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("err leaked the constraint: %v", err)
	}
}

func TestButtons_FreshIDs(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada")
	first := mustReplace(t, s, []Button{btn(ada.ID, "A", "youtube")})
	if first[0].ID != 1 {
		t.Fatalf("first id = %d, want 1", first[0].ID)
	}
	second := mustReplace(t, s, []Button{btn(ada.ID, "B", "tiktok")})
	if second[0].ID <= first[0].ID {
		t.Fatalf("second id = %d, want > %d (ids never reused)", second[0].ID, first[0].ID)
	}
	// Incoming ids are ignored: a caller echoing back an old id gets a fresh one.
	echoed := second[0]
	third := mustReplace(t, s, []Button{echoed})
	if third[0].ID <= second[0].ID {
		t.Fatalf("echoed id = %d, want fresh > %d", third[0].ID, second[0].ID)
	}
}

func TestButtons_ReplaceRace(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada")

	const writers = 10
	lists := make([][]Button, writers)
	keys := map[string]bool{}
	for w := range lists {
		p := string(rune('a' + w))
		lists[w] = []Button{
			btn(ada.ID, p+"1", p+"-s1", p+"-s2"),
			btn(ada.ID, p+"2", p+"-s3"),
			btn(ada.ID, p+"3", p+"-s4", p+"-s5", p+"-s6"),
		}
		keys[listKey(lists[w])] = true
	}

	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 256)
	stop := make(chan struct{})

	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := s.ListButtons(deadline)
				if err != nil {
					errs <- err
					return
				}
				if len(got) == 0 {
					continue // before the first replace lands
				}
				if !keys[listKey(got)] {
					errs <- errors.New("torn read: " + listKey(got))
				}
			}
		}()
	}

	writeErrs := make(chan error, writers)
	var ww sync.WaitGroup
	for w := 0; w < writers; w++ {
		ww.Add(1)
		go func() {
			defer ww.Done()
			end := time.Now().Add(200 * time.Millisecond)
			for time.Now().Before(end) {
				if _, err := s.ReplaceButtons(deadline, lists[w]); err != nil {
					writeErrs <- err
					return
				}
			}
		}()
	}
	go func() { ww.Wait(); close(stop) }()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-deadline.Done():
		t.Fatal("readers did not finish within 5s — a nested query on the single connection deadlocks")
	}
	close(writeErrs)
	for err := range writeErrs {
		t.Errorf("writer: %v", err)
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// listKey renders labels and services so a torn read (labels from one list,
// services from another, or a button missing its services) never matches.
func listKey(bs []Button) string {
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		parts = append(parts, b.Label+join(b.Services))
	}
	return strings.Join(parts, ";")
}
