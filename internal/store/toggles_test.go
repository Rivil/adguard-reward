package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

var tToggle = t0.Add(10 * time.Minute)

func mustToggle(t *testing.T, s *Store, childID int64, serviceID, state string) ToggleChange {
	t.Helper()
	ch, err := s.SetServiceToggle(ctx, childID, serviceID, state, tToggle)
	if err != nil {
		t.Fatalf("SetServiceToggle(%d, %q, %q): %v", childID, serviceID, state, err)
	}
	return ch
}

func mustToggles(t *testing.T, s *Store) []ServiceToggle {
	t.Helper()
	list, err := s.ListServiceToggles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// toggleKeys renders the list as "child/service=state" for compact asserts.
func toggleKeys(list []ServiceToggle) []string {
	out := []string{}
	for _, tg := range list {
		out = append(out, fmt.Sprintf("%d/%s=%s", tg.ChildID, tg.ServiceID, tg.State))
	}
	return out
}

func TestSetToggle_TrimAtomic(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	g := mustGrant(t, s, ada.ID, []string{"youtube", "tiktok"}, ada.Clients)

	// The upsert runs first; a failure in the trim that follows must roll
	// the upsert back with it.
	if _, err := s.DB().Exec(`CREATE TRIGGER inject BEFORE DELETE ON grant_services
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetServiceToggle(ctx, ada.ID, "youtube", ToggleBlocked, tToggle); err == nil {
		t.Fatal("SetServiceToggle past an injected trim failure succeeded")
	}
	if list := mustToggles(t, s); len(list) != 0 {
		t.Fatalf("toggles after failed set = %v, want none", toggleKeys(list))
	}
	if got := mustGetGrant(t, s, g.ID); got.Status != StatusActive || !reflect.DeepEqual(got.Services, []string{"tiktok", "youtube"}) {
		t.Fatalf("grant after failed set = %+v, want active [tiktok youtube]", got)
	}

	_, err := s.SetServiceToggle(ctx, ada.ID, "youtube", "maybe", tToggle)
	if !errors.Is(err, ErrInvalidToggleState) {
		t.Fatalf("state maybe err = %v, want ErrInvalidToggleState", err)
	}
	if list := mustToggles(t, s); len(list) != 0 {
		t.Fatalf("toggles after bad state = %v, want none", toggleKeys(list))
	}
	if got := mustGetGrant(t, s, g.ID); !reflect.DeepEqual(got.Services, []string{"tiktok", "youtube"}) {
		t.Fatalf("grant after bad state = %+v", got)
	}

	// The CHECK constraint is the safety net under the Go-side check.
	if _, err := s.DB().Exec(`INSERT INTO service_toggles (child_id, service_id, state, updated_at)
		VALUES (?, 'youtube', 'maybe', 0)`, ada.ID); err == nil {
		t.Fatal("CHECK constraint accepted state maybe")
	}
}

func TestSetToggle_EndsEmptyGrant(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	g := mustGrant(t, s, ada.ID, []string{"youtube"}, ada.Clients)

	ch := mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)
	if !ch.Ended || ch.Trimmed == nil || ch.Trimmed.ID != g.ID {
		t.Fatalf("change = %+v, want Ended with Trimmed grant %d", ch, g.ID)
	}
	if !reflect.DeepEqual(ch.Trimmed.Services, []string{"youtube"}) {
		t.Fatalf("Trimmed.Services = %v, want pre-trim [youtube]", ch.Trimmed.Services)
	}
	got := mustGetGrant(t, s, g.ID)
	if got.Status != StatusEnded {
		t.Fatalf("grant status = %q, want ended", got.Status)
	}
	var endedAt int64
	if err := s.DB().QueryRow(`SELECT ended_at FROM grants WHERE id = ?`, g.ID).Scan(&endedAt); err != nil {
		t.Fatal(err)
	}
	if endedAt != tToggle.Unix() {
		t.Fatalf("ended_at = %d, want %d", endedAt, tToggle.Unix())
	}
	active, err := s.ListActiveGrants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active grants = %+v, want none", active)
	}
	if _, err := s.CreateGrant(ctx, ada.ID, []string{"youtube"}, ada.Clients, t0, t0.Add(time.Hour)); err != nil {
		t.Fatalf("CreateGrant(youtube) after the grant ended: %v", err)
	}
}

func TestSetToggle_TrimScope(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone", "Kid tablet")
	ben := mustCreate(t, s, "Ben", "Ben phone")

	old := mustGrant(t, s, ada.ID, []string{"youtube"}, ada.Clients)
	mustSetStatus(t, s, old.ID, StatusActive, StatusExpired)
	live := mustGrant(t, s, ada.ID, []string{"youtube", "tiktok"}, ada.Clients)
	other := mustGrant(t, s, ben.ID, []string{"youtube"}, ben.Clients)

	ch := mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)
	if ch.Ended {
		t.Fatal("Ended = true for a grant that keeps tiktok")
	}
	if ch.Trimmed == nil || ch.Trimmed.ID != live.ID ||
		!reflect.DeepEqual(ch.Trimmed.Services, []string{"tiktok", "youtube"}) ||
		!reflect.DeepEqual(ch.Trimmed.Clients, []string{"Kid phone", "Kid tablet"}) {
		t.Fatalf("Trimmed = %+v, want pre-trim grant %d [tiktok youtube] on both clients", ch.Trimmed, live.ID)
	}

	got := mustGetGrant(t, s, live.ID)
	if got.Status != StatusActive || !reflect.DeepEqual(got.Services, []string{"tiktok"}) || !got.EndsAt.Equal(live.EndsAt) {
		t.Fatalf("live grant = %+v, want active [tiktok] ending %v", got, live.EndsAt)
	}
	if got := mustGetGrant(t, s, other.ID); got.Status != StatusActive || !reflect.DeepEqual(got.Services, []string{"youtube"}) {
		t.Fatalf("Ben's grant = %+v, want untouched", got)
	}
	if got := mustGetGrant(t, s, old.ID); got.Status != StatusExpired || !reflect.DeepEqual(got.Services, []string{"youtube"}) {
		t.Fatalf("Ada's expired grant = %+v, want untouched", got)
	}

	ch = mustToggle(t, s, ada.ID, "tiktok", ToggleUnblocked)
	if ch.Trimmed != nil || ch.Ended {
		t.Fatalf("unblock change = %+v, want nothing trimmed", ch)
	}
	if got := mustGetGrant(t, s, live.ID); !reflect.DeepEqual(got.Services, []string{"tiktok"}) {
		t.Fatalf("live grant after unblock = %+v, want [tiktok]", got)
	}
}

func TestSetToggle_Upsert(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)
	later := tToggle.Add(time.Hour)
	if _, err := s.SetServiceToggle(ctx, ada.ID, "youtube", ToggleUnblocked, later); err != nil {
		t.Fatal(err)
	}
	want := []ServiceToggle{{ChildID: ada.ID, ServiceID: "youtube", State: ToggleUnblocked, UpdatedAt: later.UTC()}}
	if got := mustToggles(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("toggles = %+v, want %+v", got, want)
	}
}

func TestSetToggle_Invalid(t *testing.T) {
	s, _ := openTemp(t)
	mustCreate(t, s, "Ada", "Kid phone")
	_, err := s.SetServiceToggle(ctx, 99, "youtube", ToggleBlocked, tToggle)
	if !errors.Is(err, ErrChildNotFound) {
		t.Fatalf("unknown child err = %v, want ErrChildNotFound", err)
	}
	if list := mustToggles(t, s); len(list) != 0 {
		t.Fatalf("toggles = %v, want none", toggleKeys(list))
	}
}

func TestToggles_Cascade(t *testing.T) {
	s, _ := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	ben := mustCreate(t, s, "Ben", "Ben phone")
	for i := 1; i <= 10; i++ {
		mustService(t, s, fmt.Sprintf("Svc %d", i), fmt.Sprintf("d%d.example", i))
	}
	for _, id := range []int64{ada.ID, ben.ID} {
		mustToggle(t, s, id, "youtube", ToggleBlocked)
		mustToggle(t, s, id, "custom:1", ToggleUnblocked)
		mustToggle(t, s, id, "custom:10", ToggleUnblocked)
	}

	if err := s.DeleteCustomService(ctx, 1); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"1/custom:10=unblocked", "1/youtube=blocked",
		"2/custom:10=unblocked", "2/youtube=blocked",
	}
	if got := toggleKeys(mustToggles(t, s)); !reflect.DeepEqual(got, want) {
		t.Fatalf("after DeleteCustomService(1) = %v, want %v", got, want)
	}

	if _, err := s.DeleteChild(ctx, ada.ID); err != nil {
		t.Fatal(err)
	}
	want = []string{"2/custom:10=unblocked", "2/youtube=blocked"}
	if got := toggleKeys(mustToggles(t, s)); !reflect.DeepEqual(got, want) {
		t.Fatalf("after DeleteChild(Ada) = %v, want %v", got, want)
	}
}

func TestToggles_Persist(t *testing.T) {
	s, dir := openTemp(t)
	ada := mustCreate(t, s, "Ada", "Kid phone")
	ben := mustCreate(t, s, "Ben", "Ben phone")
	mustToggle(t, s, ben.ID, "tiktok", ToggleBlocked)
	mustToggle(t, s, ada.ID, "youtube", ToggleUnblocked)
	mustToggle(t, s, ada.ID, "custom:3", ToggleBlocked)
	before := mustToggles(t, s)
	wantKeys := []string{"1/custom:3=blocked", "1/youtube=unblocked", "2/tiktok=blocked"}
	if got := toggleKeys(before); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("order = %v, want %v", got, wantKeys)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if after := mustToggles(t, s2); !reflect.DeepEqual(after, before) {
		t.Fatalf("after reopen %+v, want %+v", after, before)
	}
}
