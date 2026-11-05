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

func mustOverview(t *testing.T, s *Store, f HistoryFilter, limit int) GrantHistory {
	t.Helper()
	h, err := s.ListHistory(ctx, f, limit)
	if err != nil {
		t.Fatalf("ListHistory(%+v, %d): %v", f, limit, err)
	}
	return h
}

// walkOverview follows the cursor to the end, returning each page's size and
// every id in the order served.
func walkOverview(t *testing.T, s *Store, f HistoryFilter, limit int) (sizes []int, ids []int64) {
	t.Helper()
	for {
		h := mustOverview(t, s, f, limit)
		sizes = append(sizes, len(h.Grants))
		ids = append(ids, historyIDs(h)...)
		if !h.HasMore {
			return sizes, ids
		}
		f.Before = h.Grants[len(h.Grants)-1].ID
	}
}

func TestListHistory_AllChildrenNewestFirst(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	ben := mustCreate(t, s, "Ben", "Ben phone")
	a1, err := s.CreateGrantNamed(ctx, ada.ID, []string{"youtube", "tiktok"}, ada.Clients, t0, t0.Add(time.Hour),
		map[string]string{"youtube": "YouTube", "tiktok": "TikTok"})
	if err != nil {
		t.Fatal(err)
	}
	b1 := mustGrant(t, s, ben.ID, []string{"netflix"}, ben.Clients)
	a2 := mustGrant(t, s, ada.ID, []string{"roblox"}, ada.Clients)
	b2 := mustGrant(t, s, ben.ID, []string{"disney"}, ben.Clients)
	mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)
	if _, err := s.ExtendGrant(ctx, a1.ID, t0.Add(2*time.Hour), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	mustSetStatus(t, s, b1.ID, StatusActive, StatusEnded)

	h := mustOverview(t, s, HistoryFilter{}, 0)
	if got, want := historyIDs(h), []int64{b2.ID, a2.ID, b1.ID, a1.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overview = %v, want %v (newest first across children)", got, want)
	}
	if h.ChildID != 0 || h.ChildName != "" || h.HasMore {
		t.Fatalf("page = child %d %q HasMore %v, want no child and no more", h.ChildID, h.ChildName, h.HasMore)
	}

	perChild := map[int64]HistoryGrant{}
	for _, c := range []Child{ada, ben} {
		for _, g := range mustHistory(t, s, c.ID, 0, 0).Grants {
			perChild[g.ID] = g
		}
	}
	names := map[int64]string{ada.ID: "Ada", ben.ID: "Ben"}
	owners := map[int64]int64{a1.ID: ada.ID, a2.ID: ada.ID, b1.ID: ben.ID, b2.ID: ben.ID}
	for _, g := range h.Grants {
		if g.ChildID != owners[g.ID] || g.ChildName != names[owners[g.ID]] {
			t.Fatalf("grant %d child = %d %q, want %d %q", g.ID, g.ChildID, g.ChildName, owners[g.ID], names[owners[g.ID]])
		}
		bare := g
		bare.ChildID, bare.ChildName = 0, ""
		if !reflect.DeepEqual(bare, perChild[g.ID]) {
			t.Fatalf("grant %d = %+v, want the per-child read %+v", g.ID, bare, perChild[g.ID])
		}
	}
	trimmed := h.Grants[3]
	if len(trimmed.Services) != 2 || trimmed.Services[1].RemovedAt == nil || len(trimmed.Extensions) != 1 {
		t.Fatalf("grant %d = %+v, want a removal and an extension to compare", trimmed.ID, trimmed)
	}
}

func TestListHistory_CrossChildPaging(t *testing.T) {
	s, _ := openTemp(t)
	kids := []Child{mustCreate(t, s, "Ada", "a"), mustCreate(t, s, "Ben", "b"), mustCreate(t, s, "Cleo", "c")}
	want := map[int64]bool{}
	for i := 0; i < 120; i++ {
		want[grantN(t, s, kids[i%3].ID, i).ID] = true
	}

	sizes, ids := walkOverview(t, s, HistoryFilter{}, 0)
	if !reflect.DeepEqual(sizes, []int{50, 50, 20}) {
		t.Fatalf("page sizes = %v, want [50 50 20]", sizes)
	}
	seen := map[int64]bool{}
	for i, id := range ids {
		if seen[id] || !want[id] {
			t.Fatalf("id %d repeated or unknown", id)
		}
		seen[id] = true
		if i > 0 && id >= ids[i-1] {
			t.Fatalf("ids not descending at %d: %d after %d", i, id, ids[i-1])
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("walk saw %d grants, want %d", len(seen), len(want))
	}

	for _, tc := range []struct {
		grants   int
		wantMore bool
	}{{51, true}, {50, false}} {
		t.Run(fmt.Sprint(tc.grants), func(t *testing.T) {
			s, _ := openTemp(t)
			ada := mustCreate(t, s, "Ada", "a")
			ben := mustCreate(t, s, "Ben", "b")
			for i := 0; i < tc.grants; i++ {
				grantN(t, s, []int64{ada.ID, ben.ID}[i%2], i)
			}
			h := mustOverview(t, s, HistoryFilter{}, 0)
			if len(h.Grants) != 50 || h.HasMore != tc.wantMore {
				t.Fatalf("page = %d grants HasMore %v, want 50 HasMore %v", len(h.Grants), h.HasMore, tc.wantMore)
			}
		})
	}
}

func TestListHistory_StableCursorAcrossChildren(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	ben := mustCreate(t, s, "Ben", "b")
	g1, g2 := grantN(t, s, ada.ID, 1), grantN(t, s, ada.ID, 2)
	g3, g4 := grantN(t, s, ben.ID, 3), grantN(t, s, ben.ID, 4)

	p1 := mustOverview(t, s, HistoryFilter{}, 2)
	if got := historyIDs(p1); !reflect.DeepEqual(got, []int64{g4.ID, g3.ID}) || !p1.HasMore {
		t.Fatalf("page 1 = %v HasMore %v, want [%d %d] with more", got, p1.HasMore, g4.ID, g3.ID)
	}
	cursor := p1.Grants[1].ID

	// Both changes land between the pages. The cursor grant's own child goes
	// away, so the cursor no longer points at anything visible.
	g5 := grantN(t, s, ada.ID, 5)
	if ok, err := s.DeleteChild(ctx, ben.ID); err != nil || !ok {
		t.Fatalf("DeleteChild = %v, %v", ok, err)
	}

	p2 := mustOverview(t, s, HistoryFilter{Before: cursor}, 2)
	if got := historyIDs(p2); !reflect.DeepEqual(got, []int64{g2.ID, g1.ID}) || p2.HasMore {
		t.Fatalf("page 2 = %v HasMore %v, want [%d %d] and no more (new grant %d stays out)", got, p2.HasMore, g2.ID, g1.ID, g5.ID)
	}
}

func TestListHistory_DeletedChildOmitted(t *testing.T) {
	t.Run("newest grants hidden", func(t *testing.T) {
		s, _ := openTemp(t)
		ada := mustCreate(t, s, "Ada", "a")
		ben := mustCreate(t, s, "Ben", "b")
		var visible []int64
		for i := 0; i < 50; i++ {
			visible = append(visible, grantN(t, s, ada.ID, i).ID)
		}
		for i := 0; i < 60; i++ {
			g := grantN(t, s, ben.ID, 100+i)
			if i > 0 {
				mustSetStatus(t, s, g.ID, StatusActive, StatusExpired)
			}
		}
		if ok, err := s.DeleteChild(ctx, ben.ID); err != nil || !ok {
			t.Fatalf("DeleteChild = %v, %v", ok, err)
		}
		h := mustOverview(t, s, HistoryFilter{}, 0)
		if len(h.Grants) != 50 || h.HasMore {
			t.Fatalf("page = %d grants HasMore %v, want a full 50 and no more", len(h.Grants), h.HasMore)
		}
		for i, g := range h.Grants {
			if want := visible[len(visible)-1-i]; g.ID != want || g.ChildName != "Ada" {
				t.Fatalf("grant %d = id %d of %q, want Ada's %d", i, g.ID, g.ChildName, want)
			}
		}
	})

	t.Run("interleaved", func(t *testing.T) {
		s, _ := openTemp(t)
		ada := mustCreate(t, s, "Ada", "a")
		ben := mustCreate(t, s, "Ben", "b")
		for i := 0; i < 120; i++ {
			grantN(t, s, []int64{ada.ID, ben.ID}[i%2], i)
		}
		if ok, err := s.DeleteChild(ctx, ben.ID); err != nil || !ok {
			t.Fatalf("DeleteChild = %v, %v", ok, err)
		}
		p1 := mustOverview(t, s, HistoryFilter{}, 0)
		if len(p1.Grants) != 50 || !p1.HasMore {
			t.Fatalf("page 1 = %d grants HasMore %v, want 50 with more", len(p1.Grants), p1.HasMore)
		}
		p2 := mustOverview(t, s, HistoryFilter{Before: p1.Grants[49].ID}, 0)
		if len(p2.Grants) != 10 || p2.HasMore {
			t.Fatalf("page 2 = %d grants HasMore %v, want 10 and no more", len(p2.Grants), p2.HasMore)
		}
		for _, g := range append(p1.Grants, p2.Grants...) {
			if g.ChildID != ada.ID {
				t.Fatalf("grant %d belongs to child %d, want only Ada's", g.ID, g.ChildID)
			}
		}
	})
}

func TestListHistory_ReusedIDOmitted(t *testing.T) {
	s, _ := openTemp(t)
	ben := mustCreate(t, s, "Ben", "Ben phone")
	old := mustCreate(t, s, "Ada", "Kid phone") // the highest id, so SQLite reuses it
	oldGrant := mustGrant(t, s, old.ID, []string{"youtube"}, old.Clients)
	benGrant := mustGrant(t, s, ben.ID, []string{"tiktok"}, ben.Clients)
	if ok, err := s.DeleteChild(ctx, old.ID); err != nil || !ok {
		t.Fatalf("DeleteChild = %v, %v", ok, err)
	}
	cleo := mustCreate(t, s, "Cleo", "Cleo phone")
	if cleo.ID != old.ID {
		t.Fatalf("new child id = %d, want reused %d — the test needs the reuse", cleo.ID, old.ID)
	}
	newGrant := mustGrant(t, s, cleo.ID, []string{"netflix"}, cleo.Clients)

	h := mustOverview(t, s, HistoryFilter{}, 0)
	if got, want := historyIDs(h), []int64{newGrant.ID, benGrant.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overview = %v, want %v without old grant %d", got, want, oldGrant.ID)
	}
	if h.Grants[0].ChildName != "Cleo" {
		t.Fatalf("new grant child = %q, want Cleo", h.Grants[0].ChildName)
	}
	for _, g := range h.Grants {
		if g.ChildName == "Cleo" && g.ID <= oldGrant.ID {
			t.Fatalf("grant %d carries Cleo's name from before the watermark", g.ID)
		}
	}

	filtered := mustOverview(t, s, HistoryFilter{ChildID: cleo.ID}, 0)
	if got := historyIDs(filtered); !reflect.DeepEqual(got, []int64{newGrant.ID}) {
		t.Fatalf("filtered = %v, want only [%d]", got, newGrant.ID)
	}
}

func TestListHistory_ChildFilter(t *testing.T) {
	s, _ := openTemp(t)
	if h := mustOverview(t, s, HistoryFilter{}, 0); h.Grants == nil || len(h.Grants) != 0 || h.HasMore {
		t.Fatalf("empty install = %#v, want an empty non-nil list", h.Grants)
	}

	ada := mustCreate(t, s, "Ada", "a")
	ben := mustCreate(t, s, "Ben", "b")
	gone := mustCreate(t, s, "Gone", "g")
	empty := mustCreate(t, s, "Empty", "e")
	ag, bg := grantN(t, s, ada.ID, 1), grantN(t, s, ben.ID, 2)
	if ok, err := s.DeleteChild(ctx, gone.ID); err != nil || !ok {
		t.Fatalf("DeleteChild = %v, %v", ok, err)
	}

	for _, id := range []int64{999, gone.ID} {
		if _, err := s.ListHistory(ctx, HistoryFilter{ChildID: id}, 0); !errors.Is(err, ErrChildNotFound) {
			t.Fatalf("ChildID %d err = %v, want ErrChildNotFound", id, err)
		}
	}

	h := mustOverview(t, s, HistoryFilter{ChildID: empty.ID}, 0)
	if h.Grants == nil || len(h.Grants) != 0 || h.ChildID != empty.ID || h.ChildName != "Empty" {
		t.Fatalf("empty child = %+v, want an empty page naming Empty", h)
	}

	h = mustOverview(t, s, HistoryFilter{ChildID: ada.ID}, 0)
	if got := historyIDs(h); !reflect.DeepEqual(got, []int64{ag.ID}) || h.ChildID != ada.ID || h.ChildName != "Ada" {
		t.Fatalf("Ada's page = %v child %d %q, want [%d] naming Ada", got, h.ChildID, h.ChildName, ag.ID)
	}

	h = mustOverview(t, s, HistoryFilter{ChildID: 0}, 0)
	if got := historyIDs(h); !reflect.DeepEqual(got, []int64{bg.ID, ag.ID}) || h.ChildID != 0 || h.ChildName != "" {
		t.Fatalf("all children = %v child %d %q, want [%d %d] and no page child", got, h.ChildID, h.ChildName, bg.ID, ag.ID)
	}
}

func TestListGrantHistory_NonPositiveID(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	grantN(t, s, ada.ID, 1)

	for _, id := range []int64{0, -1} {
		if h, err := s.ListGrantHistory(ctx, id, 0, 0); !errors.Is(err, ErrChildNotFound) {
			t.Fatalf("ListGrantHistory(%d) = %+v, %v; want ErrChildNotFound", id, h, err)
		}
	}
	if h, err := s.ListHistory(ctx, HistoryFilter{ChildID: -1}, 0); !errors.Is(err, ErrChildNotFound) {
		t.Fatalf("ListHistory(ChildID -1) = %+v, %v; want ErrChildNotFound", h, err)
	}
}

func TestListGrantHistory_LeavesGrantChildZero(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	grantN(t, s, ada.ID, 1)

	h := mustHistory(t, s, ada.ID, 0, 0)
	if len(h.Grants) != 1 || h.Grants[0].ChildID != 0 || h.Grants[0].ChildName != "" {
		t.Fatalf("per-child grants = %+v, want child fields left zero", h.Grants)
	}
}
