package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Rivil/adguard-reward/internal/auth"
)

// Store is the auth.SessionStore; a signature drift fails to compile.
var _ auth.SessionStore = (*Store)(nil)

// Times are stored as Unix seconds UTC and come back truncated to the
// second, so callers must not expect sub-second round-trips.

const sessionCols = `id, username, token_hash, created_at, last_seen_at, expires_at`

func scanSession(row interface{ Scan(...any) error }) (auth.Session, error) {
	var (
		s                            auth.Session
		hash                         []byte
		created, lastSeen, expiresAt int64
	)
	if err := row.Scan(&s.ID, &s.Username, &hash, &created, &lastSeen, &expiresAt); err != nil {
		return auth.Session{}, err
	}
	copy(s.TokenHash[:], hash)
	s.CreatedAt = time.Unix(created, 0).UTC()
	s.LastSeenAt = time.Unix(lastSeen, 0).UTC()
	s.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	return s, nil
}

// Insert stores s and returns its rowid.
func (st *Store) Insert(ctx context.Context, s auth.Session) (int64, error) {
	res, err := st.db.ExecContext(ctx,
		`INSERT INTO sessions (username, token_hash, created_at, last_seen_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		s.Username, s.TokenHash[:], s.CreatedAt.Unix(), s.LastSeenAt.Unix(), s.ExpiresAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("insert session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert session: %w", err)
	}
	return id, nil
}

// ByTokenHash returns the session with this hash or auth.ErrNotFound.
func (st *Store) ByTokenHash(ctx context.Context, hash [32]byte) (auth.Session, error) {
	s, err := scanSession(st.db.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE token_hash = ?`, hash[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Session{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.Session{}, fmt.Errorf("session by hash: %w", err)
	}
	return s, nil
}

// Touch writes last_seen_at and expires_at.
func (st *Store) Touch(ctx context.Context, id int64, lastSeen, expires time.Time) error {
	_, err := st.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
		lastSeen.Unix(), expires.Unix(), id)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// Delete removes one session by id.
func (st *Store) Delete(ctx context.Context, id int64) error {
	if _, err := st.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// ListByUser lists username's sessions, oldest first.
func (st *Store) ListByUser(ctx context.Context, username string) ([]auth.Session, error) {
	rows, err := st.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE username = ? ORDER BY created_at ASC, id ASC`, username)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []auth.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return out, nil
}

// DeleteByUser removes id only when it belongs to username.
func (st *Store) DeleteByUser(ctx context.Context, username string, id int64) (bool, error) {
	res, err := st.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE id = ? AND username = ?`, id, username)
	if err != nil {
		return false, fmt.Errorf("delete session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete session: %w", err)
	}
	return n > 0, nil
}

// DeleteOthers removes every session of username except keepID.
func (st *Store) DeleteOthers(ctx context.Context, username string, keepID int64) (int, error) {
	res, err := st.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE username = ? AND id <> ?`, username, keepID)
	if err != nil {
		return 0, fmt.Errorf("delete other sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete other sessions: %w", err)
	}
	return int(n), nil
}

// DeleteExpired removes sessions whose expires_at <= now.
func (st *Store) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	res, err := st.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return int(n), nil
}
