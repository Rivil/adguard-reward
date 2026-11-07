package store

import (
	"reflect"
	"testing"
	"time"
)

func mustHistoryServices(t *testing.T, s *Store) []HistoryServiceOption {
	t.Helper()
	out, err := s.ListHistoryServices(ctx)
	if err != nil {
		t.Fatalf("ListHistoryServices: %v", err)
	}
	return out
}

// namedGrant creates a grant snapshotting names, then ends it so the same
// service can be granted again.
func namedGrant(t *testing.T, s *Store, c Child, names map[string]string) Grant {
	t.Helper()
	services := make([]string, 0, len(names))
	for id := range names {
		services = append(services, id)
	}
	g, err := s.CreateGrantNamed(ctx, c.ID, services, c.Clients, t0, t0.Add(time.Hour), names)
	if err != nil {
		t.Fatal(err)
	}
	mustSetStatus(t, s, g.ID, StatusActive, StatusEnded)
	return g
}

func TestListHistoryServices_RemovedOnly(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	if _, err := s.CreateGrantNamed(ctx, ada.ID, []string{"tiktok", "youtube"}, ada.Clients, t0, t0.Add(time.Hour),
		map[string]string{"tiktok": "TikTok", "youtube": "YouTube"}); err != nil {
		t.Fatal(err)
	}
	mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)

	want := []HistoryServiceOption{{ID: "tiktok", Name: "TikTok"}, {ID: "youtube", Name: "YouTube"}}
	if got := mustHistoryServices(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %+v, want %+v with the trimmed one still listed", got, want)
	}
}

func TestListHistoryServices_DeletedCustom(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	cs := mustService(t, s, "Roblox", "roblox.example")
	id := CustomServiceID(cs.ID)
	namedGrant(t, s, ada, map[string]string{id: "Roblox"})
	if err := s.DeleteCustomService(ctx, cs.ID); err != nil {
		t.Fatal(err)
	}

	want := []HistoryServiceOption{{ID: id, Name: "Roblox"}}
	if got := mustHistoryServices(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %+v, want %+v", got, want)
	}
}

func TestListHistoryServices_NewestName(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	namedGrant(t, s, ada, map[string]string{"custom:1": "Roblox"})
	namedGrant(t, s, ada, map[string]string{"custom:1": "Games"})

	want := []HistoryServiceOption{{ID: "custom:1", Name: "Games"}}
	if got := mustHistoryServices(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %+v, want %+v", got, want)
	}

	// A later grant with no snapshot must not blank the name.
	seedGrant(t, s, rawGrant{child: ada.ID, status: StatusExpired, started: 1000, services: []string{"custom:1"}})
	if got := mustHistoryServices(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("services after an unnamed grant = %+v, want %+v", got, want)
	}
}

func TestListHistoryServices_Hidden(t *testing.T) {
	s, _ := openTemp(t)
	ben := mustCreate(t, s, "Ben", "Ben phone")
	gone := mustCreate(t, s, "Gone", "Gone phone")
	first := mustCreate(t, s, "First", "First phone") // the highest id, so SQLite reuses it
	namedGrant(t, s, ben, map[string]string{"tiktok": "TikTok"})
	namedGrant(t, s, gone, map[string]string{"netflix": "Netflix", "tiktok": "Stale"})
	mustGrant(t, s, first.ID, []string{"youtube"}, first.Clients)
	for _, id := range []int64{first.ID, gone.ID} {
		if ok, err := s.DeleteChild(ctx, id); err != nil || !ok {
			t.Fatalf("DeleteChild(%d) = %v, %v", id, ok, err)
		}
	}
	cleo := mustCreate(t, s, "Cleo", "Cleo phone")
	if cleo.ID != gone.ID {
		t.Fatalf("new child id = %d, want reused %d — the test needs the reuse", cleo.ID, gone.ID)
	}

	// Gone's grant is the newest to name tiktok, so a hidden grant's snapshot
	// must not win either.
	want := []HistoryServiceOption{{ID: "tiktok", Name: "TikTok"}}
	if got := mustHistoryServices(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %+v, want only %+v", got, want)
	}
}

func TestListHistoryServices_Legacy(t *testing.T) {
	s, _ := openTemp(t)
	if got := mustHistoryServices(t, s); got == nil || len(got) != 0 {
		t.Fatalf("no grants = %#v, want an empty non-nil list", got)
	}

	dir := t.TempDir()
	openAt0008(t, dir) // grants on youtube, custom:2 and tiktok, none with a snapshot
	legacy, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	want := []HistoryServiceOption{{ID: "custom:2"}, {ID: "tiktok"}, {ID: "youtube"}}
	if got := mustHistoryServices(t, legacy); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy services = %+v, want %+v with empty names", got, want)
	}
}
