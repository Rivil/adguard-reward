package store

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

func queryStrings(t *testing.T, db *sql.DB, q string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(q, args...)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestOpen_ConcurrentWriters(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.DB().Exec(`CREATE TABLE scratch (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	const goroutines, inserts = 20, 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*inserts)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < inserts; i++ {
				if _, err := s.DB().Exec(`INSERT INTO scratch (n) VALUES (?)`, g*inserts+i); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("insert: %v", err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM scratch`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != goroutines*inserts {
		t.Fatalf("rows = %d, want %d", n, goroutines*inserts)
	}
}

func TestOpen_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	want := s.Migrations()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, nil)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()

	got := queryStrings(t, s.DB(), `SELECT name FROM schema_migrations ORDER BY rowid`)
	if len(want) == 0 {
		t.Fatal("no embedded migrations")
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("schema_migrations = %v, want exactly %v", got, want)
	}
}

// reverseFS returns its entries in reverse name order so a runner that
// trusts ReadDir's order applies files backwards.
type reverseFS struct{ fstest.MapFS }

func (r reverseFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := r.MapFS.ReadDir(name)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

func TestMigrate_Order(t *testing.T) {
	stub := reverseFS{fstest.MapFS{
		"0002_second.sql": {Data: []byte(`INSERT INTO ordered (n) VALUES (2);`)},
		"0001_first.sql":  {Data: []byte(`CREATE TABLE ordered (n INTEGER); INSERT INTO ordered (n) VALUES (1);`)},
	}}
	// Positive control: the stub really does hand 0002 out first.
	entries, _ := fs.ReadDir(stub, ".")
	if entries[0].Name() != "0002_second.sql" {
		t.Fatalf("stub order = %s, want 0002 first", entries[0].Name())
	}

	s, err := openWith(t.TempDir(), nil, stub)
	if err != nil {
		t.Fatalf("openWith: %v", err)
	}
	defer s.Close()

	got := queryStrings(t, s.DB(), `SELECT name FROM schema_migrations ORDER BY rowid`)
	want := []string{"0001_first.sql", "0002_second.sql"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("applied order = %v, want %v", got, want)
	}
	if ns := queryStrings(t, s.DB(), `SELECT n FROM ordered ORDER BY rowid`); fmt.Sprint(ns) != "[1 2]" {
		t.Fatalf("ordered rows = %v, want [1 2]", ns)
	}
}

func TestMigrate_Rollback(t *testing.T) {
	dir := t.TempDir()
	bad := fstest.MapFS{
		"0001_broken.sql": {Data: []byte(`CREATE TABLE half (n INTEGER); THIS IS NOT SQL;`)},
	}
	_, err := openWith(dir, nil, bad)
	if err == nil {
		t.Fatal("openWith with a broken migration succeeded")
	}
	if !strings.Contains(err.Error(), "0001_broken.sql") {
		t.Fatalf("error %q does not name the migration file", err)
	}

	// Reopen with no migrations and inspect what the failed run left behind.
	s, err := openWith(dir, nil, fstest.MapFS{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	tables := queryStrings(t, s.DB(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'half'`)
	if len(tables) != 0 {
		t.Fatalf("table from the failed migration's first statement survived: %v", tables)
	}
	if applied := queryStrings(t, s.DB(), `SELECT name FROM schema_migrations`); len(applied) != 0 {
		t.Fatalf("failed migration was recorded as applied: %v", applied)
	}
}

func TestSchema_Sessions(t *testing.T) {
	s, _ := openTemp(t)

	type col struct {
		name, typ string
		notNull   bool
		pk        int
	}
	rows, err := s.DB().Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []col
	for rows.Next() {
		var (
			cid     int
			c       col
			notNull int
			dflt    sql.NullString
		)
		if err := rows.Scan(&cid, &c.name, &c.typ, &notNull, &dflt, &c.pk); err != nil {
			t.Fatal(err)
		}
		c.notNull = notNull == 1
		got = append(got, c)
	}
	want := []col{
		{"id", "INTEGER", false, 1},
		{"username", "TEXT", true, 0},
		{"token_hash", "BLOB", true, 0},
		{"created_at", "INTEGER", true, 0},
		{"last_seen_at", "INTEGER", true, 0},
		{"expires_at", "INTEGER", true, 0},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("table_info(sessions) =\n %v\nwant\n %v", got, want)
	}

	// token_hash must be UNIQUE: index_list reports an auto-index with unique=1
	// whose single column is token_hash.
	idx, err := s.DB().Query(`PRAGMA index_list(sessions)`)
	if err != nil {
		t.Fatal(err)
	}
	var uniqueIdx []string
	for idx.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := idx.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if unique == 1 {
			uniqueIdx = append(uniqueIdx, name)
		}
	}
	// Drain before the next query: the pool is one connection, so a nested
	// query while rows are open would wait forever.
	idx.Close()
	uniqueOnHash := false
	for _, name := range uniqueIdx {
		cols := queryStrings(t, s.DB(), fmt.Sprintf(`SELECT name FROM pragma_index_info(%q)`, name))
		if fmt.Sprint(cols) == "[token_hash]" {
			uniqueOnHash = true
		}
	}
	if !uniqueOnHash {
		t.Fatal("no UNIQUE index on sessions(token_hash)")
	}
}

func TestOpen_DataDir(t *testing.T) {
	t.Run("nested dir created 0700 and db 0600 under umask 0", func(t *testing.T) {
		old := syscall.Umask(0)
		defer syscall.Umask(old)

		dir := filepath.Join(t.TempDir(), "a", "b", "data")
		s, err := Open(dir, nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer s.Close()

		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("data_dir mode = %o, want 0700", got)
		}
		fi, err = os.Stat(filepath.Join(dir, FileName))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 0600", FileName, got)
		}
	})

	t.Run("regular file as data_dir errors naming data_dir", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "notadir")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Open(file, nil)
		if err == nil {
			t.Fatal("Open on a regular file succeeded")
		}
		if !strings.Contains(err.Error(), "data_dir") {
			t.Fatalf("error %q does not name data_dir", err)
		}
	})
}

func TestOpen_Pragmas(t *testing.T) {
	s, _ := openTemp(t)
	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var fk int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
	var busy int
	if err := s.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busy)
	}
}
