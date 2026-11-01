package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// dumpClientTables renders child_clients and grant_clients in a fixed order
// so a test can assert a rejected write left both byte-identical.
func dumpClientTables(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT client_name || '|' || child_id FROM child_clients ORDER BY client_name`,
		`SELECT grant_id || '|' || client_name FROM grant_clients ORDER BY grant_id, client_name`,
	} {
		rows, err := s.DB().Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			b.WriteString(line + "\n")
		}
		_ = rows.Close()
		b.WriteString("--\n")
	}
	return b.String()
}

func activeClients(t *testing.T, s *Store, grantID int64) []string {
	t.Helper()
	gs, err := s.ListActiveGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.ID == grantID {
			return g.Clients
		}
	}
	t.Fatalf("grant %d is not active", grantID)
	return nil
}

func mustRename(t *testing.T, s *Store, from, to string) {
	t.Helper()
	if err := s.RenameClient(ctx, from, to); err != nil {
		t.Fatalf("RenameClient(%q, %q): %v", from, to, err)
	}
}

func TestRenameClient_Mapping(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone", "Kid tablet")
	g := mustGrant(t, s, ada.ID, []string{"youtube"}, ada.Clients)

	mustRename(t, s, "Kid phone", "Ada phone")

	if got := mustGet(t, s, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Ada phone", "Kid tablet"}) {
		t.Errorf("Ada clients = %v, want [Ada phone Kid tablet]", got)
	}
	if got := activeClients(t, s, g.ID); !reflect.DeepEqual(got, []string{"Ada phone", "Kid tablet"}) {
		t.Errorf("active grant clients = %v, want [Ada phone Kid tablet]", got)
	}
	all, _ := s.ListChildren(ctx)
	for _, c := range all {
		for _, n := range c.Clients {
			if n == "Kid phone" {
				t.Errorf("child %s still maps Kid phone", c.Name)
			}
		}
	}
}

func TestRenameClient_ExpiredUntouched(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	old := mustGrant(t, s, ada.ID, []string{"youtube"}, ada.Clients)
	mustSetStatus(t, s, old.ID, StatusActive, StatusExpired)
	ended := mustGrant(t, s, ada.ID, []string{"youtube"}, ada.Clients)
	mustSetStatus(t, s, ended.ID, StatusActive, StatusEnded)
	live := mustGrant(t, s, ada.ID, []string{"tiktok"}, ada.Clients)

	mustRename(t, s, "Kid phone", "Ada phone")

	for _, id := range []int64{old.ID, ended.ID} {
		if got := mustGetGrant(t, s, id).Clients; !reflect.DeepEqual(got, []string{"Kid phone"}) {
			t.Errorf("finished grant %d clients = %v, want [Kid phone]", id, got)
		}
	}
	if got := mustGetGrant(t, s, live.ID).Clients; !reflect.DeepEqual(got, []string{"Ada phone"}) {
		t.Errorf("active grant clients = %v, want [Ada phone]", got)
	}
}

func TestRenameClient_Dedupe(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	g := mustGrant(t, s, ada.ID, []string{"youtube"}, []string{"Kid phone", "Old tab"})

	mustRename(t, s, "Kid phone", "Old tab")

	if got := activeClients(t, s, g.ID); !reflect.DeepEqual(got, []string{"Old tab"}) {
		t.Errorf("active grant clients = %v, want one [Old tab]", got)
	}
	if got := mustGet(t, s, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Old tab"}) {
		t.Errorf("Ada clients = %v, want [Old tab]", got)
	}
}

func TestRenameClient_Taken(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	ben := mustCreate(t, s, "Ben", "Old tab")
	mustGrant(t, s, ada.ID, []string{"youtube"}, ada.Clients)
	mustGrant(t, s, ben.ID, []string{"youtube"}, ben.Clients)
	before := dumpClientTables(t, s)

	err := s.RenameClient(ctx, "Kid phone", "Old tab")
	var taken *ErrClientTaken
	if !errors.As(err, &taken) {
		t.Fatalf("err = %v, want *ErrClientTaken", err)
	}
	if taken.ChildID != ben.ID || taken.ChildName != "Ben" || taken.Client != "Old tab" {
		t.Errorf("ErrClientTaken = %+v, want Ben holding Old tab", taken)
	}
	if after := dumpClientTables(t, s); after != before {
		t.Errorf("tables changed on a rejected rename:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// An unmapped old name onto a name another child holds is still a conflict.
	if err := s.RenameClient(ctx, "Nobody", "Old tab"); !errors.As(err, &taken) {
		t.Errorf("unmapped rename onto Ben's client: err = %v, want *ErrClientTaken", err)
	}
	if after := dumpClientTables(t, s); after != before {
		t.Error("tables changed on a rejected unmapped rename")
	}
}

func TestRenameClient_Unmapped(t *testing.T) {
	s, _ := openTemp(t)
	mustCreate(t, s, "Ada", "Kid phone")
	before, _ := s.ListChildren(ctx)
	tables := dumpClientTables(t, s)

	if err := s.RenameClient(ctx, "Printer", "Office printer"); err != nil {
		t.Fatalf("unmapped rename: %v", err)
	}
	after, _ := s.ListChildren(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("ListChildren changed: %v -> %v", before, after)
	}
	if got := dumpClientTables(t, s); got != tables {
		t.Error("an unmapped rename wrote to the client tables")
	}
	if err := s.RenameClient(ctx, "Kid phone", "Kid phone"); err != nil {
		t.Errorf("same-name rename: %v", err)
	}
}

func TestRenameClient_SelfStale(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone", "TV")
	g := mustGrant(t, s, ada.ID, []string{"youtube"}, ada.Clients)

	mustRename(t, s, "Kid phone", "TV")

	if got := mustGet(t, s, ada.ID).Clients; !reflect.DeepEqual(got, []string{"TV"}) {
		t.Errorf("Ada clients = %v, want exactly [TV]", got)
	}
	if got := activeClients(t, s, g.ID); !reflect.DeepEqual(got, []string{"TV"}) {
		t.Errorf("active grant clients = %v, want [TV]", got)
	}
}

func TestAddChildClient_Appends(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	if err := s.AddChildClient(ctx, ada.ID, "New tv"); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, s, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Kid phone", "New tv"}) {
		t.Errorf("Ada clients = %v, want [Kid phone New tv]", got)
	}
}

func TestAddChildClient_Errors(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	ben := mustCreate(t, s, "Ben", "Kid tablet")

	err := s.AddChildClient(ctx, ada.ID, "Kid tablet")
	var taken *ErrClientTaken
	if !errors.As(err, &taken) || taken.ChildID != ben.ID || taken.Client != "Kid tablet" {
		t.Errorf("client Ben owns: err = %v, want *ErrClientTaken naming Ben", err)
	}
	if got := mustGet(t, s, ada.ID).Clients; !reflect.DeepEqual(got, []string{"Kid phone"}) {
		t.Errorf("Ada clients after conflict = %v, want [Kid phone]", got)
	}
	if err := s.AddChildClient(ctx, 999, "TV"); !errors.Is(err, ErrChildNotFound) {
		t.Errorf("unknown child: err = %v, want ErrChildNotFound", err)
	}
	if err := s.AddChildClient(ctx, ada.ID, "  "); err == nil {
		t.Error("blank name: want an error")
	}
}

func TestAddChildClient_SelfStale(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "TV")
	if err := s.AddChildClient(ctx, ada.ID, "TV"); err != nil {
		t.Fatalf("re-adding a held client: %v", err)
	}
	if n := countClients(t, s, ada.ID); n != 1 {
		t.Errorf("Ada has %d client rows, want 1", n)
	}
}
