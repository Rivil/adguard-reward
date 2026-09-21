package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Child is a named group of AdGuard persistent clients. Clients holds
// persistent-client names only, sorted, never nil. A name AdGuard no longer
// knows stays in the list until a parent removes it — nothing is auto-pruned.
type Child struct {
	ID      int64
	Name    string
	Clients []string
}

// ErrChildNotFound is returned for an id no child row carries.
var ErrChildNotFound = errors.New("child not found")

// ErrNameTaken is returned when another child already has the name
// (case-insensitively).
var ErrNameTaken = errors.New("child name already taken")

// ErrClientTaken says which other child owns the client.
type ErrClientTaken struct {
	Client    string
	ChildID   int64
	ChildName string
}

func (e *ErrClientTaken) Error() string {
	return fmt.Sprintf("client %q is already assigned to %q", e.Client, e.ChildName)
}

// normaliseClients trims, drops empties, sorts and dedups. The result is
// never nil so it serialises as [] rather than null.
func normaliseClients(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// listQuery joins children to their clients so one result set carries the
// whole picture: no consistency window between two reads and no nested query
// on the single connection. ORDER BY children.id then client_name gives ids
// ASC and sorted client lists without a second pass.
const listQuery = `
	SELECT c.id, c.name, cc.client_name
	  FROM children c
	  LEFT JOIN child_clients cc ON cc.child_id = c.id`

// scanChildren folds the joined rows into children, preserving row order.
func scanChildren(rows *sql.Rows) ([]Child, error) {
	out := []Child{}
	for rows.Next() {
		var (
			id     int64
			name   string
			client sql.NullString
		)
		if err := rows.Scan(&id, &name, &client); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != id {
			out = append(out, Child{ID: id, Name: name, Clients: []string{}})
		}
		if client.Valid {
			last := &out[len(out)-1]
			last.Clients = append(last.Clients, client.String)
		}
	}
	return out, rows.Err()
}

// ListChildren returns every child, id ASC, each with its sorted clients.
func (st *Store) ListChildren(ctx context.Context) ([]Child, error) {
	rows, err := st.db.QueryContext(ctx, listQuery+` ORDER BY c.id ASC, cc.client_name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list children: %w", err)
	}
	defer rows.Close()
	out, err := scanChildren(rows)
	if err != nil {
		return nil, fmt.Errorf("list children: %w", err)
	}
	return out, nil
}

// GetChild returns one child or ErrChildNotFound.
func (st *Store) GetChild(ctx context.Context, id int64) (Child, error) {
	return getChild(ctx, st.db, id)
}

// querier is the subset of *sql.DB and *sql.Tx the reads need.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func getChild(ctx context.Context, q querier, id int64) (Child, error) {
	rows, err := q.QueryContext(ctx, listQuery+` WHERE c.id = ? ORDER BY cc.client_name ASC`, id)
	if err != nil {
		return Child{}, fmt.Errorf("get child: %w", err)
	}
	defer rows.Close()
	out, err := scanChildren(rows)
	if err != nil {
		return Child{}, fmt.Errorf("get child: %w", err)
	}
	if len(out) == 0 {
		return Child{}, ErrChildNotFound
	}
	return out[0], nil
}

// CreateChild inserts a child with its clients in one transaction. Conflicts
// are detected by SELECT first so the caller gets a typed error naming the
// owner; the UNIQUE and PRIMARY KEY constraints remain the safety net.
func (st *Store) CreateChild(ctx context.Context, name string, clients []string) (Child, error) {
	clients = normaliseClients(clients)
	var out Child
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkConflicts(ctx, tx, 0, name, clients); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO children (name, created_at) VALUES (?, ?)`, name, time.Now().Unix())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := insertClients(ctx, tx, id, clients); err != nil {
			return err
		}
		out, err = getChild(ctx, tx, id)
		return err
	})
	if err != nil {
		return Child{}, wrapChildErr("create child", err)
	}
	return out, nil
}

// UpdateChild replaces name and the whole client set atomically. Its own row
// is excluded from the conflict checks so a same-name save or re-assigning a
// client it already owns succeeds.
func (st *Store) UpdateChild(ctx context.Context, id int64, name string, clients []string) (Child, error) {
	clients = normaliseClients(clients)
	var out Child
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := getChild(ctx, tx, id); err != nil {
			return err
		}
		if err := checkConflicts(ctx, tx, id, name, clients); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE children SET name = ? WHERE id = ?`, name, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM child_clients WHERE child_id = ?`, id); err != nil {
			return err
		}
		if err := insertClients(ctx, tx, id, clients); err != nil {
			return err
		}
		var err error
		out, err = getChild(ctx, tx, id)
		return err
	})
	if err != nil {
		return Child{}, wrapChildErr("update child", err)
	}
	return out, nil
}

// DeleteChild removes a child and, via ON DELETE CASCADE, its client rows.
// It reports whether a row existed.
func (st *Store) DeleteChild(ctx context.Context, id int64) (bool, error) {
	res, err := st.db.ExecContext(ctx, `DELETE FROM children WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete child: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete child: %w", err)
	}
	return n > 0, nil
}

// checkConflicts returns ErrNameTaken or *ErrClientTaken when another child
// (not selfID) already holds the name or one of the clients.
func checkConflicts(ctx context.Context, tx *sql.Tx, selfID int64, name string, clients []string) error {
	var other int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM children WHERE name = ? AND id <> ?`, name, selfID).Scan(&other)
	if err == nil {
		return ErrNameTaken
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, c := range clients {
		var taken ErrClientTaken
		err := tx.QueryRowContext(ctx,
			`SELECT c.id, c.name FROM child_clients cc JOIN children c ON c.id = cc.child_id
			  WHERE cc.client_name = ? AND cc.child_id <> ?`, c, selfID).
			Scan(&taken.ChildID, &taken.ChildName)
		if err == nil {
			taken.Client = c
			return &taken
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func insertClients(ctx context.Context, tx *sql.Tx, childID int64, clients []string) error {
	for _, c := range clients {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO child_clients (client_name, child_id) VALUES (?, ?)`, c, childID); err != nil {
			return err
		}
	}
	return nil
}

// inTx runs fn in a transaction, rolling back on any error or panic.
func (st *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// wrapChildErr passes the package sentinels through untouched — callers
// match on them — and wraps everything else with the operation.
func wrapChildErr(op string, err error) error {
	var taken *ErrClientTaken
	if errors.Is(err, ErrNameTaken) || errors.Is(err, ErrChildNotFound) || errors.As(err, &taken) {
		return err
	}
	return fmt.Errorf("%s: %w", op, err)
}
