package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationFS is a directory of *.sql files at its root, applied in name
// order. Names are the ordering, so files are prefixed 0001_, 0002_, ...
type migrationFS = fs.FS

func embeddedMigrations() migrationFS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic("store: embedded migrations dir missing: " + err.Error())
	}
	return sub
}

// Migrations lists the embedded migration file names in the order they are
// applied.
func (s *Store) Migrations() []string {
	names, err := migrationNames(embeddedMigrations())
	if err != nil {
		panic("store: list embedded migrations: " + err.Error())
	}
	return names
}

// migrationNames returns the *.sql files at the root of fsys, sorted. The
// sort is explicit rather than trusted from ReadDir so an fs that returns
// entries in any order still applies 0001 before 0002.
func migrationNames(fsys migrationFS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// migrate applies every file in fsys that schema_migrations does not yet
// list, each in its own transaction. A failing file is rolled back and the
// error names it; files already applied are left alone.
func migrate(ctx context.Context, db *sql.DB, fsys migrationFS, log *slog.Logger) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT    PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	names, err := migrationNames(fsys)
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := applyOne(ctx, db, name, string(body)); err != nil {
			return err
		}
		log.Info("applied migration", "migration", name)
	}
	return nil
}

func appliedMigrations(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read schema_migrations: %w", err)
		}
		applied[name] = true
	}
	return applied, rows.Err()
}

// applyOne runs one migration body and records it, atomically.
func applyOne(ctx context.Context, db *sql.DB, name, body string) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: begin: %w", name, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("migration %s: %w", name, err)
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
		name, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("migration %s: record: %w", name, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: commit: %w", name, err)
	}
	return nil
}
