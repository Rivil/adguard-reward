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

// rawGrant is a grant seeded straight into the tables, so a test can reach any
// (status, end_reason) pair. A blank reason stores NULL. Services go in
// grant_services, trimmed ones in grant_service_removals.
type rawGrant struct {
	child          int64
	status, reason string
	started        int64
	services       []string
	trimmed        []string
}

func seedGrant(t *testing.T, s *Store, g rawGrant) int64 {
	t.Helper()
	var reason any
	if g.reason != "" {
		reason = g.reason
	}
	res, err := s.DB().Exec(`INSERT INTO grants (child_id, status, end_reason, started_at, ends_at) VALUES (?, ?, ?, ?, ?)`,
		g.child, g.status, reason, g.started, g.started+3600)
	if err != nil {
		t.Fatalf("seed grant %+v: %v", g, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range g.services {
		if _, err := s.DB().Exec(`INSERT INTO grant_services (grant_id, child_id, service_id, active) VALUES (?, ?, ?, 0)`,
			id, g.child, svc); err != nil {
			t.Fatalf("seed service %s: %v", svc, err)
		}
	}
	for _, svc := range g.trimmed {
		if _, err := s.DB().Exec(`INSERT INTO grant_service_removals (grant_id, service_id, removed_at, reason) VALUES (?, ?, ?, 'permanent_block')`,
			id, svc, g.started+60); err != nil {
			t.Fatalf("seed removal %s: %v", svc, err)
		}
	}
	return id
}

func TestListHistory_ServiceIncludesTrimmed(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	two := mustGrant(t, s, ada.ID, []string{"youtube", "tiktok"}, ada.Clients)
	emptied := mustGrant(t, s, ada.ID, []string{"roblox"}, ada.Clients)
	other := mustGrant(t, s, ada.ID, []string{"netflix"}, ada.Clients)
	mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)
	mustToggle(t, s, ada.ID, "roblox", ToggleBlocked)

	h := mustOverview(t, s, HistoryFilter{ServiceID: "youtube"}, 0)
	if got := historyIDs(h); !reflect.DeepEqual(got, []int64{two.ID}) {
		t.Fatalf("service youtube = %v, want only [%d], not %d", got, two.ID, other.ID)
	}
	removed := tToggle
	want := []HistoryService{
		{ID: "tiktok"},
		{ID: "youtube", RemovedAt: &removed, RemovedReason: EndReasonPermanentBlock},
	}
	if !reflect.DeepEqual(h.Grants[0].Services, want) {
		t.Fatalf("services = %+v, want all of them with the trimmed one marked", h.Grants[0].Services)
	}

	var live int
	if err := s.DB().QueryRow(`SELECT count(*) FROM grant_services WHERE grant_id = ?`, emptied.ID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("emptied grant has %d grant_services rows, want 0 — the test needs a grant trimmed to nothing", live)
	}
	h = mustOverview(t, s, HistoryFilter{ServiceID: "roblox"}, 0)
	if got := historyIDs(h); !reflect.DeepEqual(got, []int64{emptied.ID}) || h.Grants[0].EndReason != EndReasonPermanentBlock {
		t.Fatalf("service roblox = %+v, want the permanent_block grant %d", h.Grants, emptied.ID)
	}
}

func TestListHistory_ServiceFilter(t *testing.T) {
	dir := t.TempDir()
	openAt0008(t, dir) // child 1: grant 1 youtube+custom:2, 2 tiktok, 3 youtube; no snapshots
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ben := mustCreate(t, s, "Ben", "Ben phone")
	cs := mustService(t, s, "Roblox", "roblox.example")
	id := CustomServiceID(cs.ID)
	custom, err := s.CreateGrantNamed(ctx, ben.ID, []string{id}, ben.Clients, t0, t0.Add(time.Hour), map[string]string{id: "Roblox"})
	if err != nil {
		t.Fatal(err)
	}
	mustSetStatus(t, s, custom.ID, StatusActive, StatusEnded)
	if err := s.DeleteCustomService(ctx, cs.ID); err != nil {
		t.Fatal(err)
	}
	// A snapshot that reads like another service's id must not match it.
	lookalike, err := s.CreateGrantNamed(ctx, ben.ID, []string{"disney"}, ben.Clients, t0, t0.Add(time.Hour), map[string]string{"disney": "tiktok"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		service string
		want    []int64
	}{
		{"tiktok", []int64{2}},
		{"custom:2", []int64{1}},
		{"youtube", []int64{3, 1}},
		{id, []int64{custom.ID}},
		{"disney", []int64{lookalike.ID}},
		{"Roblox", []int64{}},
	} {
		if got := historyIDs(mustOverview(t, s, HistoryFilter{ServiceID: tc.service}, 0)); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("service %q = %v, want %v", tc.service, got, tc.want)
		}
	}
}

func TestListHistory_ServiceNoDuplicates(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	id := seedGrant(t, s, rawGrant{child: ada.ID, status: StatusExpired, started: 1000,
		services: []string{"youtube", "tiktok"}, trimmed: []string{"youtube"}})

	h := mustOverview(t, s, HistoryFilter{ServiceID: "youtube"}, 0)
	if got := historyIDs(h); !reflect.DeepEqual(got, []int64{id}) || h.HasMore {
		t.Fatalf("service in both tables = %v HasMore %v, want [%d] once", got, h.HasMore, id)
	}
}

func TestListHistory_ServiceExactID(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	seedGrant(t, s, rawGrant{child: ada.ID, status: StatusExpired, started: 1000, services: []string{"youtube"}})
	twelve := seedGrant(t, s, rawGrant{child: ada.ID, status: StatusExpired, started: 1000, services: []string{"custom:12"}})
	seedGrant(t, s, rawGrant{child: ada.ID, status: StatusExpired, started: 1000, trimmed: []string{"youtube-kids"}})

	if got := historyIDs(mustOverview(t, s, HistoryFilter{ServiceID: "custom:12"}, 0)); !reflect.DeepEqual(got, []int64{twelve}) {
		t.Fatalf("custom:12 = %v, want [%d]", got, twelve)
	}
	for _, service := range []string{"custom:1", "you", "%", "youtube%", "_outube", `' OR 1=1 --`} {
		if got := historyIDs(mustOverview(t, s, HistoryFilter{ServiceID: service}, 0)); len(got) != 0 {
			t.Fatalf("service %q = %v, want no match", service, got)
		}
	}
}

func TestListHistory_DateBounds(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	const from, until = 10_000, 20_000
	at := func(sec int64) int64 {
		return seedGrant(t, s, rawGrant{child: ada.ID, status: StatusExpired, started: sec})
	}
	early, first, last, late := at(from-1), at(from), at(until-1), at(until)

	// Started before the range, then ended and was extended inside it.
	ranInto := at(from - 500)
	if _, err := s.DB().Exec(`UPDATE grants SET ends_at = ?, ended_at = ? WHERE id = ?`, from+500, from+400, ranInto); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO grant_extensions (grant_id, extended_at, old_ends_at, new_ends_at) VALUES (?, ?, ?, ?)`,
		ranInto, from+100, from-100, from+500); err != nil {
		t.Fatal(err)
	}

	unix := func(sec int64) time.Time { return time.Unix(sec, 0).UTC() }
	for _, tc := range []struct {
		name string
		f    HistoryFilter
		want []int64
	}{
		{"both", HistoryFilter{From: unix(from), Until: unix(until)}, []int64{last, first}},
		{"from only", HistoryFilter{From: unix(from)}, []int64{late, last, first}},
		{"until only", HistoryFilter{Until: unix(until)}, []int64{ranInto, last, first, early}},
		{"from after until", HistoryFilter{From: unix(until), Until: unix(from)}, []int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := historyIDs(mustOverview(t, s, tc.f, 0)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestListHistory_StatusMatrix(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	cells := []struct{ status, reason, want string }{
		{StatusActive, "", HistoryOngoing},
		{StatusActive, EndReasonExpired, HistoryOngoing},
		{StatusActive, EndReasonEndedEarly, HistoryOngoing},
		{StatusActive, EndReasonPermanentBlock, HistoryOngoing},
		{StatusExpired, "", HistoryExpired},
		{StatusExpired, EndReasonExpired, HistoryExpired},
		{StatusExpired, EndReasonEndedEarly, HistoryExpired},
		{StatusExpired, EndReasonPermanentBlock, HistoryExpired},
		{StatusEnded, "", HistoryEndedUnknown},
		{StatusEnded, EndReasonExpired, HistoryExpired},
		{StatusEnded, EndReasonEndedEarly, HistoryEndedEarly},
		{StatusEnded, EndReasonPermanentBlock, HistoryPermanentBlock},
	}
	want := map[string][]int64{}
	for _, c := range cells {
		// started 1000 puts every ends_at in the past, so a live row is overdue.
		id := seedGrant(t, s, rawGrant{child: ada.ID, status: c.status, reason: c.reason, started: 1000})
		want[c.want] = append([]int64{id}, want[c.want]...)
	}

	total := 0
	for _, status := range []string{HistoryOngoing, HistoryExpired, HistoryEndedEarly, HistoryPermanentBlock, HistoryEndedUnknown} {
		got := historyIDs(mustOverview(t, s, HistoryFilter{Status: status}, 0))
		if !reflect.DeepEqual(got, want[status]) {
			t.Fatalf("status %s = %v, want %v", status, got, want[status])
		}
		total += len(got)
	}
	if total != len(cells) {
		t.Fatalf("the five statuses hold %d grants, want each of the %d exactly once", total, len(cells))
	}
}

func TestListHistory_UnknownStatus(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	grantN(t, s, ada.ID, 1)

	h, err := s.ListHistory(ctx, HistoryFilter{Status: "bogus"}, 0)
	if err == nil || len(h.Grants) != 0 {
		t.Fatalf("status bogus = %+v, %v; want an error and no grants", h, err)
	}
}

func TestListHistory_FiltersCombine(t *testing.T) {
	type row struct {
		id      int64
		child   int64
		service string
		started int64
		status  string
	}
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	ben := mustCreate(t, s, "Ben", "b")
	statuses := []struct{ status, reason, wire string }{
		{StatusActive, "", HistoryOngoing},
		{StatusExpired, "", HistoryExpired},
		{StatusEnded, EndReasonEndedEarly, HistoryEndedEarly},
		{StatusEnded, "", HistoryEndedUnknown},
	}
	var grid []row
	for _, child := range []int64{ada.ID, ben.ID} {
		for _, service := range []string{"youtube", "tiktok"} {
			for day := int64(0); day < 3; day++ {
				for _, st := range statuses {
					g := rawGrant{child: child, status: st.status, reason: st.reason, started: 1000 * (day + 1)}
					if day == 1 { // the middle day's service was trimmed off
						g.trimmed = []string{service}
					} else {
						g.services = []string{service}
					}
					grid = append(grid, row{seedGrant(t, s, g), child, service, g.started, st.wire})
				}
			}
		}
	}

	full := HistoryFilter{
		ChildID:   ada.ID,
		ServiceID: "youtube",
		Status:    HistoryExpired,
		From:      time.Unix(2000, 0),
		Until:     time.Unix(3000, 0),
	}
	for bits := 0; bits < 32; bits++ {
		var f HistoryFilter
		if bits&1 != 0 {
			f.ChildID = full.ChildID
		}
		if bits&2 != 0 {
			f.ServiceID = full.ServiceID
		}
		if bits&4 != 0 {
			f.Status = full.Status
		}
		if bits&8 != 0 {
			f.From = full.From
		}
		if bits&16 != 0 {
			f.Until = full.Until
		}

		want := []int64{}
		for i := len(grid) - 1; i >= 0; i-- {
			g := grid[i]
			if (f.ChildID == 0 || g.child == f.ChildID) &&
				(f.ServiceID == "" || g.service == f.ServiceID) &&
				(f.Status == "" || g.status == f.Status) &&
				(f.From.IsZero() || g.started >= f.From.Unix()) &&
				(f.Until.IsZero() || g.started < f.Until.Unix()) {
				want = append(want, g.id)
			}
		}

		if got := historyIDs(mustOverview(t, s, f, 1000)); !reflect.DeepEqual(got, want) {
			t.Fatalf("subset %05b = %v, want %v", bits, got, want)
		}
		_, walked := walkOverview(t, s, f, 3)
		if len(want) > 0 && !reflect.DeepEqual(walked, want) {
			t.Fatalf("subset %05b walked at limit 3 = %v, want %v", bits, walked, want)
		}
	}
}

func TestListHistory_FilteredPaging(t *testing.T) {
	f := HistoryFilter{ServiceID: "youtube"}
	seed := func(t *testing.T, s *Store, child int64, service string, n int) []int64 {
		t.Helper()
		var ids []int64
		for i := 0; i < n; i++ {
			ids = append(ids, seedGrant(t, s, rawGrant{child: child, status: StatusExpired, started: 1000, services: []string{service}}))
		}
		return ids
	}

	t.Run("half of 120 match", func(t *testing.T) {
		s, _ := openTemp(t)
		ada := mustCreate(t, s, "Ada", "a")
		var want []int64
		for i := 0; i < 60; i++ {
			want = append(seed(t, s, ada.ID, "youtube", 1), want...)
			seed(t, s, ada.ID, "tiktok", 1)
		}
		sizes, ids := walkOverview(t, s, f, 0)
		if !reflect.DeepEqual(sizes, []int{50, 10}) || !reflect.DeepEqual(ids, want) {
			t.Fatalf("walk = sizes %v ids %v, want [50 10] over every match once", sizes, ids)
		}
	})

	t.Run("older non-match is not more", func(t *testing.T) {
		s, _ := openTemp(t)
		ada := mustCreate(t, s, "Ada", "a")
		seed(t, s, ada.ID, "tiktok", 1)
		seed(t, s, ada.ID, "youtube", 50)
		if h := mustOverview(t, s, f, 0); len(h.Grants) != 50 || h.HasMore {
			t.Fatalf("page = %d grants HasMore %v, want 50 and no more", len(h.Grants), h.HasMore)
		}
	})

	t.Run("51 matches", func(t *testing.T) {
		s, _ := openTemp(t)
		ada := mustCreate(t, s, "Ada", "a")
		ids := seed(t, s, ada.ID, "youtube", 51)
		h := mustOverview(t, s, f, 0)
		if len(h.Grants) != 50 || !h.HasMore || h.Grants[49].ID != ids[1] {
			t.Fatalf("page = %d grants HasMore %v, last %d; want 50 with more ending at %d", len(h.Grants), h.HasMore, h.Grants[49].ID, ids[1])
		}
		rest := mustOverview(t, s, HistoryFilter{ServiceID: "youtube", Before: h.Grants[49].ID}, 0)
		if got := historyIDs(rest); !reflect.DeepEqual(got, []int64{ids[0]}) || rest.HasMore {
			t.Fatalf("page 2 = %v HasMore %v, want only [%d]", got, rest.HasMore, ids[0])
		}
	})
}

func TestListHistory_StatusChangesBetweenPages(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "a")
	g1, g2, g3 := grantN(t, s, ada.ID, 1), grantN(t, s, ada.ID, 2), grantN(t, s, ada.ID, 3)
	g4, g5 := grantN(t, s, ada.ID, 4), grantN(t, s, ada.ID, 5)

	f := HistoryFilter{Status: HistoryOngoing}
	p1 := mustOverview(t, s, f, 2)
	if got := historyIDs(p1); !reflect.DeepEqual(got, []int64{g5.ID, g4.ID}) || !p1.HasMore {
		t.Fatalf("page 1 = %v HasMore %v, want [%d %d] with more", got, p1.HasMore, g5.ID, g4.ID)
	}
	// The cursor grant itself stops matching before page 2 is read.
	mustSetStatus(t, s, g4.ID, StatusActive, StatusExpired)

	f.Before = g4.ID
	if got := historyIDs(mustOverview(t, s, f, 2)); !reflect.DeepEqual(got, []int64{g3.ID, g2.ID}) {
		t.Fatalf("page 2 = %v, want [%d %d]", got, g3.ID, g2.ID)
	}
	f.Before = g2.ID
	if got := historyIDs(mustOverview(t, s, f, 2)); !reflect.DeepEqual(got, []int64{g1.ID}) {
		t.Fatalf("page 3 = %v, want [%d]", got, g1.ID)
	}
}

func TestListHistory_FiltersKeepVisibility(t *testing.T) {
	s, _ := openTemp(t)
	ben := mustCreate(t, s, "Ben", "Ben phone")
	gone := mustCreate(t, s, "Gone", "Gone phone")
	reused := mustCreate(t, s, "Ada", "Kid phone") // the highest id, so SQLite reuses it
	mustGrant(t, s, gone.ID, []string{"youtube"}, gone.Clients)
	mustGrant(t, s, reused.ID, []string{"youtube"}, reused.Clients)
	own := mustGrant(t, s, ben.ID, []string{"youtube"}, ben.Clients)
	for _, id := range []int64{gone.ID, reused.ID} {
		if ok, err := s.DeleteChild(ctx, id); err != nil || !ok {
			t.Fatalf("DeleteChild(%d) = %v, %v", id, ok, err)
		}
	}
	cleo := mustCreate(t, s, "Cleo", "Cleo phone")
	if cleo.ID != gone.ID {
		t.Fatalf("new child id = %d, want reused %d — the test needs the reuse", cleo.ID, gone.ID)
	}

	if got := historyIDs(mustOverview(t, s, HistoryFilter{ServiceID: "youtube"}, 0)); !reflect.DeepEqual(got, []int64{own.ID}) {
		t.Fatalf("service filter = %v, want only Ben's [%d]", got, own.ID)
	}
	if got := historyIDs(mustOverview(t, s, HistoryFilter{ChildID: cleo.ID, ServiceID: "youtube"}, 0)); len(got) != 0 {
		t.Fatalf("service filter on the reused id = %v, want nothing", got)
	}
	if got := historyIDs(mustOverview(t, s, HistoryFilter{Status: HistoryOngoing}, 0)); !reflect.DeepEqual(got, []int64{own.ID}) {
		t.Fatalf("status filter = %v, want only Ben's [%d]", got, own.ID)
	}
}
