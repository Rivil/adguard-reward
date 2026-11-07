package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

// The interface is proven at compile time in devices.go; referencing it here
// too keeps `go vet ./...` on the hook if that line is ever dropped.
var _ auth.DeviceStore = (*Store)(nil)

// A syntactically PHC-shaped hash: the schema checks only the prefix.
const testPINHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"

func insertDevice(t *testing.T, s *Store, user string, h byte) int64 {
	t.Helper()
	id, err := s.InsertDevice(ctx, auth.Device{
		Username:  user,
		TokenHash: hash(h),
		PINHash:   testPINHash,
		CreatedAt: base,
		ExpiresAt: base.Add(90 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("InsertDevice: %v", err)
	}
	return id
}

// tagSession sets a session's device_id directly; Session.DeviceID lands
// with the session store change.
func tagSession(t *testing.T, s *Store, sessionID, deviceID int64) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE sessions SET device_id = ? WHERE id = ?`, deviceID, sessionID); err != nil {
		t.Fatalf("tag session: %v", err)
	}
}

func failedAttempts(t *testing.T, s *Store, id int64) int {
	t.Helper()
	d, err := s.DeviceByID(ctx, id)
	if err != nil {
		t.Fatalf("DeviceByID(%d): %v", id, err)
	}
	return d.FailedAttempts
}

func sessionDeviceID(t *testing.T, s *Store, sessionID int64) string {
	t.Helper()
	got := queryStrings(t, s.DB(), `SELECT quote(device_id) FROM sessions WHERE id = ?`, sessionID)
	if len(got) != 1 {
		t.Fatalf("session %d: %d rows, want 1", sessionID, len(got))
	}
	return got[0]
}

func deviceIDs(t *testing.T, s *Store) string {
	t.Helper()
	return fmt.Sprint(queryStrings(t, s.DB(), `SELECT username || ':' || id FROM devices ORDER BY id`))
}

func TestDevices_RoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	id := insertDevice(t, s, "mum", 9)

	for name, get := range map[string]func() (auth.Device, error){
		"by hash": func() (auth.Device, error) { return s.DeviceByTokenHash(ctx, hash(9)) },
		"by id":   func() (auth.Device, error) { return s.DeviceByID(ctx, id) },
	} {
		d, err := get()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := auth.Device{
			ID: id, Username: "mum", TokenHash: hash(9), PINHash: testPINHash,
			CreatedAt: base, ExpiresAt: base.Add(90 * 24 * time.Hour),
		}
		if d != want {
			t.Fatalf("%s = %+v, want %+v", name, d, want)
		}
	}
	if _, err := s.DeviceByTokenHash(ctx, hash(8)); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("unknown hash err = %v, want ErrDeviceNotFound", err)
	}
	if _, err := s.DeviceByID(ctx, id+100); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("unknown id err = %v, want ErrDeviceNotFound", err)
	}

	// A counter in the input is ignored: new devices start at 0.
	id2, err := s.InsertDevice(ctx, auth.Device{
		Username: "mum", TokenHash: hash(10), PINHash: testPINHash, FailedAttempts: 4,
		CreatedAt: base, ExpiresAt: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := failedAttempts(t, s, id2); n != 0 {
		t.Fatalf("new device failed_attempts = %d, want 0", n)
	}
}

func TestReserveAttempt_Concurrent(t *testing.T) {
	s, _ := openTemp(t)
	id := insertDevice(t, s, "mum", 1)

	const n = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		got     []int
		refused int
		other   []error
	)
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a, err := s.ReserveAttempt(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				got = append(got, a)
			case errors.Is(err, auth.ErrNoAttempts):
				refused++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	sort.Ints(got)
	if fmt.Sprint(got) != "[1 2 3 4 5]" || refused != 15 {
		t.Fatalf("reservations = %v, refused = %d; want [1 2 3 4 5] and 15", got, refused)
	}
	if a := failedAttempts(t, s, id); a != auth.MaxPINAttempts {
		t.Fatalf("failed_attempts = %d, want %d", a, auth.MaxPINAttempts)
	}
}

func TestReserveAttempt_Missing(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.ReserveAttempt(ctx, 42); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("ReserveAttempt(missing) err = %v, want ErrDeviceNotFound", err)
	}
}

func TestReserveAttempt_SurvivesReopen(t *testing.T) {
	s, dir := openTemp(t)
	id := insertDevice(t, s, "mum", 1)
	for want := 1; want <= 3; want++ {
		if a, err := s.ReserveAttempt(ctx, id); err != nil || a != want {
			t.Fatalf("ReserveAttempt #%d = %d, %v", want, a, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer s2.Close()
	if a, err := s2.ReserveAttempt(ctx, id); err != nil || a != 4 {
		t.Fatalf("ReserveAttempt after reopen = %d, %v; want 4", a, err)
	}
}

func TestRevokeDevice_DeletesTaggedSessions(t *testing.T) {
	s, _ := openTemp(t)
	dev := insertDevice(t, s, "mum", 1)
	other := insertDevice(t, s, "mum", 2)
	tagged1 := insert(t, s, "mum", 11, base)
	tagged2 := insert(t, s, "mum", 12, base)
	otherTagged := insert(t, s, "mum", 13, base)
	untagged := insert(t, s, "mum", 14, base)
	tagSession(t, s, tagged1, dev)
	tagSession(t, s, tagged2, dev)
	tagSession(t, s, otherTagged, other)

	if err := s.RevokeDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceByID(ctx, dev); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("revoked device still present (err %v)", err)
	}
	left := queryStrings(t, s.DB(), `SELECT id || ':' || quote(device_id) FROM sessions ORDER BY id`)
	want := fmt.Sprint([]string{fmt.Sprintf("%d:%d", otherTagged, other), fmt.Sprintf("%d:NULL", untagged)})
	if fmt.Sprint(left) != want {
		t.Fatalf("sessions after revoke = %v, want %v", left, want)
	}
	if _, err := s.DeviceByID(ctx, other); err != nil {
		t.Fatalf("other device: %v", err)
	}
}

func TestDeleteDevice_DetachesSession(t *testing.T) {
	s, _ := openTemp(t)
	dev := insertDevice(t, s, "mum", 1)
	sess := insert(t, s, "mum", 11, base)
	tagSession(t, s, sess, dev)
	if got := sessionDeviceID(t, s, sess); got != fmt.Sprint(dev) {
		t.Fatalf("precondition: session device_id = %s, want %d", got, dev)
	}

	if err := s.DeleteDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceByID(ctx, dev); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("deleted device still present (err %v)", err)
	}
	if got := sessionDeviceID(t, s, sess); got != "NULL" {
		t.Fatalf("session device_id after DeleteDevice = %s, want NULL", got)
	}
}

func TestResetAndReplacePIN(t *testing.T) {
	s, _ := openTemp(t)
	id := insertDevice(t, s, "mum", 1)
	reserve := func(k int) {
		t.Helper()
		for range k {
			if _, err := s.ReserveAttempt(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
	}

	reserve(3)
	if err := s.ResetAttempts(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := failedAttempts(t, s, id); n != 0 {
		t.Fatalf("after ResetAttempts failed_attempts = %d, want 0", n)
	}

	reserve(3)
	const newHash = "$argon2id$v=19$m=19456,t=2,p=1$bmV3c2FsdG5ld3NhbHQ$bmV3aGFzaG5ld2hhc2huZXc"
	if err := s.ReplacePIN(ctx, id, newHash); err != nil {
		t.Fatal(err)
	}
	d, err := s.DeviceByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.FailedAttempts != 0 || d.PINHash != newHash || d.TokenHash != hash(1) {
		t.Fatalf("after ReplacePIN = attempts %d, pin %q, token %x; want 0, new hash, token kept",
			d.FailedAttempts, d.PINHash, d.TokenHash[:1])
	}
	if err := s.ReplacePIN(ctx, id+100, newHash); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("ReplacePIN(missing) err = %v, want ErrDeviceNotFound", err)
	}
	if err := s.ReplacePIN(ctx, id, "583920"); err == nil {
		t.Fatal("ReplacePIN with a non-PHC hash succeeded")
	}
}

func TestTouchDevice(t *testing.T) {
	s, _ := openTemp(t)
	id := insertDevice(t, s, "mum", 1)
	later := base.Add(100 * 24 * time.Hour)
	if err := s.TouchDevice(ctx, id, later); err != nil {
		t.Fatal(err)
	}
	d, err := s.DeviceByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !d.ExpiresAt.Equal(later) || !d.CreatedAt.Equal(base) {
		t.Fatalf("after TouchDevice expires %v created %v; want %v, %v", d.ExpiresAt, d.CreatedAt, later, base)
	}
}

func TestMigrate0010_Checks(t *testing.T) {
	dir := t.TempDir()
	old, err := openWith(dir, nil, migrationsThrough(t, "0009_grant_history.sql"))
	if err != nil {
		t.Fatalf("open at 0009: %v", err)
	}
	if _, err := old.DB().Exec(`INSERT INTO sessions (id, username, token_hash, created_at, last_seen_at, expires_at)
	                            VALUES (1, 'mum', x'01', 0, 0, 99)`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("Open over 0009: %v", err)
	}
	defer s.Close()
	db := s.DB()

	applied := queryStrings(t, db, `SELECT name FROM schema_migrations ORDER BY rowid`)
	i9 := -1
	for i, n := range applied {
		if n == "0009_grant_history.sql" {
			i9 = i
		}
	}
	if i9 < 0 || i9+1 >= len(applied) || applied[i9+1] != "0010_devices.sql" {
		t.Fatalf("schema_migrations = %v, want 0010_devices.sql directly after 0009_grant_history.sql", applied)
	}
	if got := sessionDeviceID(t, s, 1); got != "NULL" {
		t.Fatalf("pre-0010 session device_id = %s, want NULL", got)
	}

	const ins = `INSERT INTO devices (username, token_hash, pin_hash, failed_attempts, created_at, expires_at)
	             VALUES ('mum', ?, ?, ?, 0, 0)`
	if _, err := db.Exec(ins, []byte{1}, testPINHash, 0); err != nil {
		t.Fatalf("valid device row: %v", err)
	}
	for _, c := range []struct {
		name  string
		token []byte
		pin   string
		fails int
	}{
		{"plaintext pin", []byte{2}, "583920", 0},
		{"uppercase prefix", []byte{3}, strings.ToUpper(testPINHash[:10]) + testPINHash[10:], 0},
		{"argon2i", []byte{4}, strings.Replace(testPINHash, "argon2id", "argon2i", 1), 0},
		{"failed_attempts 6", []byte{5}, testPINHash, 6},
		{"failed_attempts -1", []byte{6}, testPINHash, -1},
		{"duplicate token_hash", []byte{1}, testPINHash, 0},
	} {
		_, err := db.Exec(ins, c.token, c.pin, c.fails)
		if err == nil || !strings.Contains(err.Error(), "constraint") {
			t.Errorf("%s: err = %v, want a constraint violation", c.name, err)
		}
	}
	if _, err := db.Exec(ins, []byte{7}, testPINHash, 5); err != nil {
		t.Fatalf("failed_attempts 5 (the cap) rejected: %v", err)
	}
}

func TestDeleteOtherDevices(t *testing.T) {
	s, _ := openTemp(t)
	a := insertDevice(t, s, "mum", 1)
	b := insertDevice(t, s, "mum", 2)
	insertDevice(t, s, "mum", 3) // C
	d := insertDevice(t, s, "dad", 4)
	sessB := insert(t, s, "mum", 12, base)
	tagSession(t, s, sessB, b)
	sessA := insert(t, s, "mum", 11, base)
	tagSession(t, s, sessA, a)

	n, err := s.DeleteOtherDevices(ctx, "mum", a)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("DeleteOtherDevices = %d, want 2", n)
	}
	if got, want := deviceIDs(t, s), fmt.Sprintf("[mum:%d dad:%d]", a, d); got != want {
		t.Fatalf("devices left = %s, want %s", got, want)
	}
	// B's session went with B; A's own session is untouched.
	left := queryStrings(t, s.DB(), `SELECT id || ':' || quote(device_id) FROM sessions ORDER BY id`)
	if want := fmt.Sprint([]string{fmt.Sprintf("%d:%d", sessA, a)}); fmt.Sprint(left) != want {
		t.Fatalf("sessions left = %v, want %v", left, want)
	}

	n, err = s.DeleteOtherDevices(ctx, "mum", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("DeleteOtherDevices(keep 0) = %d, want 1", n)
	}
	if got, want := deviceIDs(t, s), fmt.Sprintf("[dad:%d]", d); got != want {
		t.Fatalf("devices left = %s, want %s", got, want)
	}
}

func TestDeleteExpiredDevices(t *testing.T) {
	s, _ := openTemp(t)
	now := base.Add(time.Hour)
	mk := func(h byte, expires time.Time) int64 {
		t.Helper()
		id, err := s.InsertDevice(ctx, auth.Device{
			Username: "mum", TokenHash: hash(h), PINHash: testPINHash, CreatedAt: base, ExpiresAt: expires,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	expired := mk(1, now)
	live := mk(2, now.Add(time.Second))
	sessExpired := insert(t, s, "mum", 11, base)
	tagSession(t, s, sessExpired, expired)
	sessLive := insert(t, s, "mum", 12, base)
	tagSession(t, s, sessLive, live)

	n, err := s.DeleteExpiredDevices(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("DeleteExpiredDevices = %d, want 1", n)
	}
	if got, want := deviceIDs(t, s), fmt.Sprintf("[mum:%d]", live); got != want {
		t.Fatalf("devices left = %s, want %s", got, want)
	}
	left := queryStrings(t, s.DB(), `SELECT id || ':' || quote(device_id) FROM sessions ORDER BY id`)
	if want := fmt.Sprint([]string{fmt.Sprintf("%d:%d", sessLive, live)}); fmt.Sprint(left) != want {
		t.Fatalf("sessions left = %v, want %v", left, want)
	}
}

func TestDevice_LogValue(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.MessageKey) {
				return slog.Attr{}
			}
			return a
		},
	}))
	d := auth.Device{
		ID: 7, Username: "mum", TokenHash: hash(0xAB), PINHash: testPINHash,
		FailedAttempts: 3, CreatedAt: base, ExpiresAt: base,
	}
	log.Info("x", "device", d)

	var got map[string]map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line %q: %v", buf.String(), err)
	}
	want := map[string]any{"id": float64(7), "username": "mum"}
	if fmt.Sprint(got["device"]) != fmt.Sprint(want) || len(got) != 1 {
		t.Fatalf("device logs as %s, want exactly {id, username}", buf.String())
	}
	if strings.Contains(buf.String(), "argon2id") || strings.Contains(buf.String(), "ab") {
		t.Fatalf("log line leaks a hash: %s", buf.String())
	}
}
