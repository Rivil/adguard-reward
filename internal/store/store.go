// Package store owns the SQLite database under data_dir.
//
// One file, one writer: the pool is capped at a single connection so the
// pure-Go driver never sees two writers, and WAL plus a busy timeout keep
// readers from tripping over the sweeper. The corollary for query code: never
// issue a second query while a *sql.Rows is still open — it would wait for
// the one connection forever. Scan into memory, close, then query again.
// Migrations are embedded and run on every Open, so a fresh data_dir and an
// upgraded binary take the same path.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// FileName is the database file created inside data_dir.
const FileName = "adguard-reward.db"

// Store is an open, migrated database.
type Store struct {
	db   *sql.DB
	path string
	log  *slog.Logger
}

// Open creates dataDir if needed, opens (or creates) the database inside it,
// applies the embedded migrations and returns the store. Errors name data_dir
// so the operator can find the config key.
func Open(dataDir string, log *slog.Logger) (*Store, error) {
	return openWith(dataDir, log, embeddedMigrations())
}

// openWith is Open with an injectable migration source, for tests.
func openWith(dataDir string, log *slog.Logger, migrations migrationFS) (*Store, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data_dir %q: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, FileName)

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("data_dir %q: open %s: %w", dataDir, FileName, err)
	}
	// A single connection is the whole concurrency story: modernc's driver is
	// not safe with two writers, and busy_timeout covers whoever is queued.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("data_dir %q: open %s: %w", dataDir, FileName, err)
	}
	if err := restrictPerms(path); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("data_dir %q: %w", dataDir, err)
	}
	if err := migrate(ctx, db, migrations, log); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("data_dir %q: migrate: %w", dataDir, err)
	}
	return &Store{db: db, path: path, log: log}, nil
}

// dsn builds the driver URL: WAL for concurrent readers, a 5 s busy wait so a
// queued writer blocks instead of failing, and foreign keys on for later
// migrations that declare them.
func dsn(path string) string {
	q := url.Values{}
	q["_pragma"] = []string{
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"foreign_keys(ON)",
		"synchronous(NORMAL)",
	}
	return "file:" + path + "?" + q.Encode()
}

// restrictPerms chmods the database and its WAL siblings to 0600. The umask
// decides what SQLite creates them as, and a permissive umask must not leave
// session hashes world-readable.
func restrictPerms(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		err := os.Chmod(p, 0o600)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("chmod %s: %w", filepath.Base(p), err)
		}
	}
	return nil
}

// DB exposes the connection pool to the query files in this package.
func (s *Store) DB() *sql.DB { return s.db }

// Path is the database file's location.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
