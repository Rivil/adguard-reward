package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Button is a preset grant a parent taps on Home. Services keeps the order it
// was given (the first supplies the icon), deduped on first occurrence, and is
// never nil. Duration is whole seconds in the database.
type Button struct {
	ID       int64
	ChildID  int64
	Label    string
	Services []string
	Duration time.Duration
}

// ErrUnknownChild says which child id in a replace has no row. The replace is
// rolled back whole, so the previous list is intact.
type ErrUnknownChild struct {
	ChildID int64
}

func (e *ErrUnknownChild) Error() string {
	return fmt.Sprintf("child %d not found", e.ChildID)
}

// dedupKeepOrder drops later duplicates without sorting. The result is never
// nil so it serialises as [] rather than null.
func dedupKeepOrder(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// ListButtons returns every button in stored order with its services.
func (st *Store) ListButtons(ctx context.Context) ([]Button, error) {
	var out []Button
	err := st.inTx(ctx, func(tx *sql.Tx) (err error) {
		out, err = loadButtons(ctx, tx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list buttons: %w", err)
	}
	return out, nil
}

// ReplaceButtons swaps the whole list for in, in one transaction: the old
// rows go (cascading their services), each item's child is checked so an
// unknown one is a typed *ErrUnknownChild rather than a constraint failure,
// and position follows the slice index. Incoming IDs are ignored; the stored
// list, with fresh ids, is returned.
func (st *Store) ReplaceButtons(ctx context.Context, in []Button) ([]Button, error) {
	var out []Button
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM buttons`); err != nil {
			return err
		}
		for i, b := range in {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM children WHERE id = ?`, b.ChildID).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return &ErrUnknownChild{ChildID: b.ChildID}
			}
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx,
				`INSERT INTO buttons (position, label, child_id, duration) VALUES (?, ?, ?, ?)`,
				i, b.Label, b.ChildID, int64(b.Duration/time.Second))
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			for j, svc := range dedupKeepOrder(b.Services) {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO button_services (button_id, position, service_id) VALUES (?, ?, ?)`,
					id, j, svc); err != nil {
					return err
				}
			}
		}
		var err error
		out, err = loadButtons(ctx, tx)
		return err
	})
	if err != nil {
		var unknown *ErrUnknownChild
		if errors.As(err, &unknown) {
			return nil, err
		}
		return nil, fmt.Errorf("replace buttons: %w", err)
	}
	return out, nil
}

// loadButtons issues two queries — buttons, then their services — each
// scanned to completion before the next starts (single-connection rule).
// Callers run it inside a transaction so the two reads see one snapshot and
// a concurrent replace cannot commit between them.
func loadButtons(ctx context.Context, q querier) ([]Button, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, child_id, label, duration FROM buttons ORDER BY position ASC`)
	if err != nil {
		return nil, err
	}
	out := []Button{}
	index := map[int64]int{}
	for rows.Next() {
		var (
			b    Button
			secs int64
		)
		if err := rows.Scan(&b.ID, &b.ChildID, &b.Label, &secs); err != nil {
			rows.Close()
			return nil, err
		}
		b.Duration = time.Duration(secs) * time.Second
		b.Services = []string{}
		index[b.ID] = len(out)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	rows, err = q.QueryContext(ctx,
		`SELECT button_id, service_id FROM button_services ORDER BY button_id ASC, position ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id  int64
			svc string
		)
		if err := rows.Scan(&id, &svc); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Services = append(out[i].Services, svc)
		}
	}
	return out, rows.Err()
}
