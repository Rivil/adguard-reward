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

// migrationsThrough is the embedded migrations up to and including last, so a
// test can build a database as an older binary left it.
func migrationsThrough(t *testing.T, last string) fstest.MapFS {
	t.Helper()
	src := embeddedMigrations()
	names, err := migrationNames(src)
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, name := range names {
		if name > last {
			break
		}
		body, err := fs.ReadFile(src, name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = &fstest.MapFile{Data: body}
	}
	if _, ok := out[last]; !ok {
		t.Fatalf("no embedded migration %s", last)
	}
	return out
}

// clientPairs lists child_clients as "child:client", sorted.
func clientPairs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return queryStrings(t, db, `SELECT c.name || ':' || cc.client_name
	  FROM child_clients cc JOIN children c ON c.id = cc.child_id
	 ORDER BY c.name, cc.client_name`)
}

func TestMigrate_0005PreservesClients(t *testing.T) {
	dir := t.TempDir()
	old, err := openWith(dir, nil, migrationsThrough(t, "0004_buttons.sql"))
	if err != nil {
		t.Fatalf("open at 0004: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO children (id, name, created_at) VALUES (1, 'Ada', 0), (2, 'Ben', 0)`,
		`INSERT INTO child_clients (client_name, child_id) VALUES ('Kid phone', 1), ('Kid tablet', 2)`,
	} {
		if _, err := old.DB().Exec(q); err != nil {
			t.Fatalf("seed at 0004: %v", err)
		}
	}
	// Positive control: at 0004 a client still has one owner.
	if _, err := old.DB().Exec(`INSERT INTO child_clients (client_name, child_id) VALUES ('Kid phone', 2)`); err == nil {
		t.Fatal("0004 schema accepted a second owner — the seed is not the old schema")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(dir, nil)
	if err != nil {
		t.Fatalf("Open over 0004: %v", err)
	}
	defer s.Close()

	if got, want := clientPairs(t, s.DB()), []string{"Ada:Kid phone", "Ben:Kid tablet"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("child_clients after 0005 = %v, want exactly %v", got, want)
	}
	// Later migrations run after 0005 too, so check 0005 directly follows 0004
	// rather than being last.
	applied := queryStrings(t, s.DB(), `SELECT name FROM schema_migrations ORDER BY rowid`)
	i4 := -1
	for i, n := range applied {
		if n == "0004_buttons.sql" {
			i4 = i
		}
	}
	if i4 < 0 || i4+1 >= len(applied) || applied[i4+1] != "0005_shared_clients.sql" {
		t.Fatalf("schema_migrations = %v, want 0005_shared_clients.sql applied right after 0004_buttons.sql", applied)
	}
	// The migrated table takes a second owner.
	if _, err := s.DB().Exec(`INSERT INTO child_clients (client_name, child_id) VALUES ('Kid phone', 2)`); err != nil {
		t.Fatalf("migrated child_clients rejects a shared client: %v", err)
	}
}

func TestSchema_ChildClientsCascade(t *testing.T) {
	s, _ := openTemp(t)
	db := s.DB()
	for _, q := range []string{
		`INSERT INTO children (id, name, created_at) VALUES (1, 'Ada', 0), (2, 'Ben', 0)`,
		`INSERT INTO child_clients (client_name, child_id) VALUES ('Kid phone', 1), ('Kid phone', 2), ('Kid tablet', 2)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	const orphans = `SELECT client_name FROM child_clients WHERE child_id NOT IN (SELECT id FROM children)`

	if _, err := db.Exec(`DELETE FROM children WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if got := queryStrings(t, db, orphans); len(got) != 0 {
		t.Fatalf("orphan child_clients rows after deleting Ben: %v", got)
	}
	if got := clientPairs(t, db); fmt.Sprint(got) != "[Ada:Kid phone]" {
		t.Fatalf("child_clients after deleting Ben = %v, want [Ada:Kid phone]", got)
	}
	if _, err := db.Exec(`DELETE FROM children`); err != nil {
		t.Fatal(err)
	}
	if got := queryStrings(t, db, `SELECT client_name FROM child_clients`); len(got) != 0 {
		t.Fatalf("child_clients rows after deleting every child: %v", got)
	}

	fk := queryStrings(t, db, `SELECT "table" || '.' || "to" || ' ' || on_delete FROM pragma_foreign_key_list('child_clients')`)
	if fmt.Sprint(fk) != "[children.id CASCADE]" {
		t.Fatalf("child_clients foreign keys = %v, want [children.id CASCADE]", fk)
	}
	cols := queryStrings(t, db, `SELECT name FROM pragma_index_info('child_clients_child_id_idx')`)
	if fmt.Sprint(cols) != "[child_id]" {
		t.Fatalf("child_clients_child_id_idx columns = %v, want [child_id]", cols)
	}
}

func TestSchema_ChildClientsPK(t *testing.T) {
	s, _ := openTemp(t)
	db := s.DB()
	if _, err := db.Exec(`INSERT INTO children (id, name, created_at) VALUES (1, 'Ada', 0), (2, 'Ben', 0)`); err != nil {
		t.Fatal(err)
	}
	const ins = `INSERT INTO child_clients (client_name, child_id) VALUES (?, ?)`
	if _, err := db.Exec(ins, "Kid phone", 1); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(ins, "Kid phone", 1)
	if err == nil || !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("duplicate (Kid phone, 1) err = %v, want a constraint violation", err)
	}
	if _, err := db.Exec(ins, "Kid phone", 2); err != nil {
		t.Fatalf("(Kid phone, 2) beside (Kid phone, 1): %v, want a second owner accepted", err)
	}
	// The key leads with client_name so its index serves owner lookups.
	pk := queryStrings(t, db, `SELECT name FROM pragma_table_info('child_clients') WHERE pk > 0 ORDER BY pk`)
	if fmt.Sprint(pk) != "[client_name child_id]" {
		t.Fatalf("child_clients primary key = %v, want [client_name child_id]", pk)
	}
}
