package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

// The interface is proven at compile time in sessions.go; referencing it
// here too keeps `go vet ./...` on the hook if that line is ever dropped.
var _ auth.SessionStore = (*Store)(nil)

var ctx = context.Background()

var base = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func hash(b byte) [32]byte {
	var h [32]byte
	h[0] = b
	return h
}

func insert(t *testing.T, s *Store, user string, h byte, created time.Time) int64 {
	t.Helper()
	id, err := s.Insert(ctx, auth.Session{
		Username:   user,
		TokenHash:  hash(h),
		CreatedAt:  created,
		LastSeenAt: created,
		ExpiresAt:  created.Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return id
}

func ids(ss []auth.Session) []int64 {
	out := make([]int64, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

func TestSessions_UniqueHash(t *testing.T) {
	s, _ := openTemp(t)
	insert(t, s, "alice", 1, base)
	_, err := s.Insert(ctx, auth.Session{Username: "alice", TokenHash: hash(1), CreatedAt: base, LastSeenAt: base, ExpiresAt: base})
	if err == nil {
		t.Fatal("second Insert with the same TokenHash succeeded")
	}
	rows, err := s.ListByUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListByUser = %d rows, want 1", len(rows))
	}
}

func TestSessions_DeleteByUser(t *testing.T) {
	s, _ := openTemp(t)
	aliceID := insert(t, s, "alice", 1, base)

	if ok, err := s.DeleteByUser(ctx, "bob", aliceID); err != nil || ok {
		t.Fatalf("DeleteByUser(bob, alice's id) = (%v, %v), want (false, nil)", ok, err)
	}
	if rows, _ := s.ListByUser(ctx, "alice"); len(rows) != 1 {
		t.Fatal("alice's row was deleted by bob")
	}
	if ok, err := s.DeleteByUser(ctx, "alice", aliceID); err != nil || !ok {
		t.Fatalf("DeleteByUser(alice, own id) = (%v, %v), want (true, nil)", ok, err)
	}
	if rows, _ := s.ListByUser(ctx, "alice"); len(rows) != 0 {
		t.Fatal("alice's row survived her own delete")
	}
}

func TestSessions_DeleteOthers(t *testing.T) {
	s, _ := openTemp(t)
	insert(t, s, "alice", 1, base)
	a2 := insert(t, s, "alice", 2, base.Add(time.Second))
	insert(t, s, "alice", 3, base.Add(2*time.Second))
	bobID := insert(t, s, "bob", 4, base)

	n, err := s.DeleteOthers(ctx, "alice", a2)
	if err != nil || n != 2 {
		t.Fatalf("DeleteOthers = (%d, %v), want (2, nil)", n, err)
	}
	rows, _ := s.ListByUser(ctx, "alice")
	if len(rows) != 1 || rows[0].ID != a2 {
		t.Fatalf("alice's sessions = %v, want [%d]", ids(rows), a2)
	}
	bob, _ := s.ListByUser(ctx, "bob")
	if len(bob) != 1 || bob[0].ID != bobID {
		t.Fatalf("bob's sessions = %v, want [%d]", ids(bob), bobID)
	}
}

func TestSessions_Touch(t *testing.T) {
	s, _ := openTemp(t)
	id := insert(t, s, "alice", 1, base)
	ls := base.Add(3 * time.Hour)
	ex := base.Add(40 * 24 * time.Hour)
	if err := s.Touch(ctx, id, ls, ex); err != nil {
		t.Fatal(err)
	}
	got, err := s.ByTokenHash(ctx, hash(1))
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastSeenAt.Equal(ls) || !got.ExpiresAt.Equal(ex) {
		t.Fatalf("after Touch: last_seen %v expires %v, want %v %v", got.LastSeenAt, got.ExpiresAt, ls, ex)
	}
	if !got.CreatedAt.Equal(base) {
		t.Fatalf("Touch changed created_at to %v", got.CreatedAt)
	}
}

func TestSessions_DeleteExpired(t *testing.T) {
	s, _ := openTemp(t)
	mk := func(h byte, exp time.Time) {
		if _, err := s.Insert(ctx, auth.Session{Username: "u", TokenHash: hash(h), CreatedAt: base, LastSeenAt: base, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	mk(1, base)
	mk(2, base.Add(time.Second))
	n, err := s.DeleteExpired(ctx, base)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpired = (%d, %v), want (1, nil)", n, err)
	}
	if _, err := s.ByTokenHash(ctx, hash(1)); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("row expiring at now survived: err = %v", err)
	}
	if _, err := s.ByTokenHash(ctx, hash(2)); err != nil {
		t.Fatalf("row expiring at now+1s was deleted: %v", err)
	}
}

func TestSessions_Persist(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := auth.Session{
		Username:   "alice",
		TokenHash:  hash(9),
		CreatedAt:  base,
		LastSeenAt: base.Add(time.Minute),
		ExpiresAt:  base.Add(time.Hour),
	}
	want.ID, err = s.Insert(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ByTokenHash(ctx, hash(9))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Username != want.Username || got.TokenHash != want.TokenHash ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.LastSeenAt.Equal(want.LastSeenAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("after reopen got %+v, want %+v", got, want)
	}
}

func TestSessions_NotFound(t *testing.T) {
	s, _ := openTemp(t)
	_, err := s.ByTokenHash(ctx, hash(42))
	if !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("err = %v, want auth.ErrNotFound", err)
	}
}

func TestSessions_ListOrder(t *testing.T) {
	s, _ := openTemp(t)
	later := insert(t, s, "alice", 1, base.Add(time.Hour))
	earlier := insert(t, s, "alice", 2, base)
	same := insert(t, s, "alice", 3, base)
	rows, err := s.ListByUser(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	got := ids(rows)
	if len(got) != 3 || got[0] != earlier || got[1] != same || got[2] != later {
		t.Fatalf("order = %v, want [%d %d %d] (created_at ASC, then id)", got, earlier, same, later)
	}
}
