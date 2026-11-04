package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func mustHistory(t *testing.T, s *Store, childID, before int64, limit int) GrantHistory {
	t.Helper()
	h, err := s.ListGrantHistory(ctx, childID, before, limit)
	if err != nil {
		t.Fatalf("ListGrantHistory(%d, %d, %d): %v", childID, before, limit, err)
	}
	return h
}

func historyIDs(h GrantHistory) []int64 {
	out := []int64{}
	for _, g := range h.Grants {
		out = append(out, g.ID)
	}
	return out
}

// grantN creates a grant on its own service so many can be live at once.
func grantN(t *testing.T, s *Store, childID int64, n int) Grant {
	t.Helper()
	return mustGrant(t, s, childID, []string{fmt.Sprintf("svc-%d", n)}, []string{"Kid phone"})
}

func TestListGrantHistory_Paging(t *testing.T) {
	for _, tc := range []struct {
		grants   int
		wantMore bool
	}{{51, true}, {50, false}} {
		t.Run(fmt.Sprint(tc.grants), func(t *testing.T) {
			s, _ := openTemp(t)
			ada := mustCreate(t, s, "Ada", "Kid phone")
			var ids []int64
			for i := 0; i < tc.grants; i++ {
				ids = append(ids, grantN(t, s, ada.ID, i).ID)
			}
			h := mustHistory(t, s, ada.ID, 0, 0)
			if len(h.Grants) != 50 || h.HasMore != tc.wantMore {
				t.Fatalf("page = %d grants HasMore %v, want 50 HasMore %v", len(h.Grants), h.HasMore, tc.wantMore)
			}
			for i, g := range h.Grants {
				if want := ids[len(ids)-1-i]; g.ID != want {
					t.Fatalf("grant %d = id %d, want %d (newest first)", i, g.ID, want)
				}
			}
			if h.ChildID != ada.ID || h.ChildName != "Ada" {
				t.Fatalf("child = %d %q, want %d Ada", h.ChildID, h.ChildName, ada.ID)
			}
			if !tc.wantMore {
				return
			}
			rest := mustHistory(t, s, ada.ID, h.Grants[49].ID, 0)
			if got := historyIDs(rest); !reflect.DeepEqual(got, []int64{ids[0]}) || rest.HasMore {
				t.Fatalf("page 2 = %v HasMore %v, want [%d] and no more", got, rest.HasMore, ids[0])
			}
		})
	}
}

func TestListGrantHistory_StableCursor(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	g1, g2, g3 := grantN(t, s, ada.ID, 1), grantN(t, s, ada.ID, 2), grantN(t, s, ada.ID, 3)

	p1 := mustHistory(t, s, ada.ID, 0, 2)
	if got := historyIDs(p1); !reflect.DeepEqual(got, []int64{g3.ID, g2.ID}) || !p1.HasMore {
		t.Fatalf("page 1 = %v HasMore %v, want [%d %d] with more", got, p1.HasMore, g3.ID, g2.ID)
	}
	g4 := grantN(t, s, ada.ID, 4)
	p2 := mustHistory(t, s, ada.ID, p1.Grants[len(p1.Grants)-1].ID, 2)
	if got := historyIDs(p2); !reflect.DeepEqual(got, []int64{g1.ID}) || p2.HasMore {
		t.Fatalf("page 2 = %v HasMore %v, want only [%d] (new grant %d neither listed nor duplicated)", got, p2.HasMore, g1.ID, g4.ID)
	}
}

func TestListGrantHistory_ReusedChildID(t *testing.T) {
	s, _ := openTemp(t)
	ben := mustCreate(t, s, "Ben", "Ben phone")
	a := mustCreate(t, s, "Ada", "Kid phone") // the highest id, so SQLite reuses it
	mustGrant(t, s, a.ID, []string{"youtube"}, a.Clients)
	mustGrant(t, s, a.ID, []string{"tiktok"}, a.Clients)
	mustGrant(t, s, ben.ID, []string{"youtube"}, ben.Clients)
	if ok, err := s.DeleteChild(ctx, a.ID); err != nil || !ok {
		t.Fatalf("DeleteChild = %v, %v", ok, err)
	}

	c := mustCreate(t, s, "Cleo", "Cleo phone")
	if c.ID != a.ID {
		t.Fatalf("new child id = %d, want reused %d — the test needs the reuse", c.ID, a.ID)
	}
	if h := mustHistory(t, s, c.ID, 0, 0); len(h.Grants) != 0 || h.HasMore {
		t.Fatalf("reused-id history = %v, want empty", historyIDs(h))
	}
	g := mustGrant(t, s, c.ID, []string{"netflix"}, c.Clients)
	if got := historyIDs(mustHistory(t, s, c.ID, 0, 0)); !reflect.DeepEqual(got, []int64{g.ID}) {
		t.Fatalf("history after a new grant = %v, want only [%d]", got, g.ID)
	}
}

func TestListGrantHistory_RemovalsAndExtensions(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	two := mustGrant(t, s, ada.ID, []string{"youtube", "tiktok"}, ada.Clients)
	mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)
	x1, x2 := t0.Add(5*time.Minute), t0.Add(20*time.Minute)
	if _, err := s.ExtendGrant(ctx, two.ID, t0.Add(90*time.Minute), x1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExtendGrant(ctx, two.ID, t0.Add(2*time.Hour), x2); err != nil {
		t.Fatal(err)
	}
	one := mustGrant(t, s, ada.ID, []string{"roblox"}, ada.Clients)
	mustToggle(t, s, ada.ID, "roblox", ToggleBlocked)

	h := mustHistory(t, s, ada.ID, 0, 0)
	if got := historyIDs(h); !reflect.DeepEqual(got, []int64{one.ID, two.ID}) {
		t.Fatalf("history = %v, want [%d %d]", got, one.ID, two.ID)
	}
	removed := tToggle
	ended := h.Grants[0]
	if ended.Status != StatusEnded || ended.EndReason != EndReasonPermanentBlock || ended.EndedAt == nil || !ended.EndedAt.Equal(tToggle) {
		t.Fatalf("trim-ended grant = %+v, want ended by permanent_block at %v", ended, tToggle)
	}
	if want := []HistoryService{{ID: "roblox", RemovedAt: &removed, RemovedReason: EndReasonPermanentBlock}}; !reflect.DeepEqual(ended.Services, want) {
		t.Fatalf("trim-ended services = %+v, want its last service listed as removed", ended.Services)
	}

	live := h.Grants[1]
	wantSvcs := []HistoryService{
		{ID: "tiktok"},
		{ID: "youtube", RemovedAt: &removed, RemovedReason: EndReasonPermanentBlock},
	}
	if !reflect.DeepEqual(live.Services, wantSvcs) {
		t.Fatalf("live services = %+v, want %+v", live.Services, wantSvcs)
	}
	if live.Status != StatusActive || live.EndReason != "" || live.EndedAt != nil {
		t.Fatalf("live grant = %+v, want active with no reason or end", live)
	}
	wantExt := []GrantExtension{
		{At: x1, OldEndsAt: t0.Add(time.Hour), NewEndsAt: t0.Add(90 * time.Minute)},
		{At: x2, OldEndsAt: t0.Add(90 * time.Minute), NewEndsAt: t0.Add(2 * time.Hour)},
	}
	if !reflect.DeepEqual(live.Extensions, wantExt) {
		t.Fatalf("extensions = %+v, want ascending %+v", live.Extensions, wantExt)
	}
	if !reflect.DeepEqual(ended.Extensions, []GrantExtension{}) {
		t.Fatalf("unextended grant extensions = %#v, want []", ended.Extensions)
	}
}

func TestListGrantHistory_Legacy(t *testing.T) {
	dir := t.TempDir()
	openAt0008(t, dir) // child 1: grant 1 expired, 2 ended, 3 active
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ben := mustCreate(t, s, "Ben", "Ben phone")
	bg := mustGrant(t, s, ben.ID, []string{"youtube"}, ben.Clients)

	h := mustHistory(t, s, 1, 0, 0)
	if got := historyIDs(h); !reflect.DeepEqual(got, []int64{3, 2, 1}) {
		t.Fatalf("legacy history = %v, want [3 2 1] and none of Ben's", got)
	}
	g := h.Grants[1]
	endedAt := time.Unix(2100, 0).UTC()
	want := HistoryGrant{
		ID: 2, Status: StatusEnded, EndReason: "",
		StartedAt: time.Unix(2000, 0).UTC(), EndsAt: time.Unix(2600, 0).UTC(), EndedAt: &endedAt,
		Services:   []HistoryService{{ID: "tiktok", Name: ""}},
		Extensions: []GrantExtension{},
	}
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("legacy ended grant = %+v, want %+v (no inferred reason, empty snapshot name)", g, want)
	}
	if svcs := h.Grants[2].Services; len(svcs) != 2 || svcs[0].ID != "custom:2" || svcs[1].ID != "youtube" || h.Grants[2].EndReason != "" {
		t.Fatalf("legacy expired grant = %+v, want [custom:2 youtube] with no reason", h.Grants[2])
	}
	if got := historyIDs(mustHistory(t, s, ben.ID, 0, 0)); !reflect.DeepEqual(got, []int64{bg.ID}) {
		t.Fatalf("Ben's history = %v, want only [%d]", got, bg.ID)
	}
	if _, err := s.ListGrantHistory(ctx, 999, 0, 0); !errors.Is(err, ErrChildNotFound) {
		t.Fatalf("unknown child err = %v, want ErrChildNotFound", err)
	}
}

func TestListGrantHistory_Reopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ada := mustCreate(t, s, "Ada", "Kid phone")
	g, err := s.CreateGrantNamed(ctx, ada.ID, []string{"youtube", "tiktok"}, ada.Clients, t0, t0.Add(time.Hour),
		map[string]string{"youtube": "YouTube", "tiktok": "TikTok"})
	if err != nil {
		t.Fatal(err)
	}
	mustToggle(t, s, ada.ID, "tiktok", ToggleBlocked)
	if _, err := s.ExtendGrant(ctx, g.ID, t0.Add(2*time.Hour), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	mustSetStatus(t, s, g.ID, StatusActive, StatusEnded)
	before := mustHistory(t, s, ada.ID, 0, 0)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after := mustHistory(t, s, ada.ID, 0, 0)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("history after reopen = %+v, want %+v", after, before)
	}
	if len(after.Grants) != 1 || after.Grants[0].EndReason != EndReasonEndedEarly || len(after.Grants[0].Extensions) != 1 {
		t.Fatalf("reopened grant = %+v, want ended_early with one extension", after.Grants)
	}
}

func TestListGrantHistory_RemovedServiceKeepsName(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	if _, err := s.CreateGrantNamed(ctx, ada.ID, []string{"tiktok", "youtube"}, ada.Clients, t0, t0.Add(time.Hour),
		map[string]string{"tiktok": "TikTok", "youtube": "YouTube"}); err != nil {
		t.Fatal(err)
	}
	mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)

	h := mustHistory(t, s, ada.ID, 0, 0)
	removed := tToggle
	want := []HistoryService{
		{ID: "tiktok", Name: "TikTok"},
		{ID: "youtube", Name: "YouTube", RemovedAt: &removed, RemovedReason: EndReasonPermanentBlock},
	}
	if len(h.Grants) != 1 || !reflect.DeepEqual(h.Grants[0].Services, want) {
		t.Fatalf("services = %+v, want %+v", h.Grants[0].Services, want)
	}
}
