package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Grant statuses. A grant leaves active exactly once, through SetGrantStatus.
const (
	StatusActive  = "active"
	StatusExpired = "expired"
	StatusEnded   = "ended"
)

// Grant is a timed unblock of Services for one child. Clients is the set of
// AdGuard client names unblocked at creation — revert targets exactly these,
// never the child's current mapping. Both lists are sorted and never nil.
type Grant struct {
	ID        int64
	ChildID   int64
	Status    string
	Services  []string
	Clients   []string
	StartedAt time.Time
	EndsAt    time.Time
}

// ErrGrantNotFound is returned for an id no grant row carries, or — from the
// active-only operations — for a grant that is no longer active.
var ErrGrantNotFound = errors.New("grant not found")

// ErrGrantOverlap says which active grant already covers the (child, service).
type ErrGrantOverlap struct {
	ExistingID int64
	ServiceID  string
}

func (e *ErrGrantOverlap) Error() string {
	return fmt.Sprintf("service %q is already granted by grant %d", e.ServiceID, e.ExistingID)
}

// CreateGrant inserts an active grant with its services and clients in one
// transaction. An active grant for the same (child, service) is detected by
// SELECT first so the caller gets a typed error naming it; the partial unique
// index remains the safety net. Lists are normalised like child clients.
func (st *Store) CreateGrant(ctx context.Context, childID int64, services, clients []string, startedAt, endsAt time.Time) (Grant, error) {
	services = normaliseClients(services)
	clients = normaliseClients(clients)
	var out Grant
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		for _, svc := range services {
			var existing int64
			err := tx.QueryRowContext(ctx,
				`SELECT grant_id FROM grant_services WHERE child_id = ? AND service_id = ? AND active = 1`,
				childID, svc).Scan(&existing)
			if err == nil {
				return &ErrGrantOverlap{ExistingID: existing, ServiceID: svc}
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO grants (child_id, status, started_at, ends_at) VALUES (?, ?, ?, ?)`,
			childID, StatusActive, startedAt.Unix(), endsAt.Unix())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, svc := range services {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO grant_services (grant_id, child_id, service_id, active) VALUES (?, ?, ?, 1)`,
				id, childID, svc); err != nil {
				return err
			}
		}
		for _, c := range clients {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO grant_clients (grant_id, client_name) VALUES (?, ?)`, id, c); err != nil {
				return err
			}
		}
		out, err = getGrant(ctx, tx, id)
		return err
	})
	if err != nil {
		return Grant{}, wrapGrantErr("create grant", err)
	}
	return out, nil
}

// ListActiveGrants returns every active grant, id ASC, with full lists.
func (st *Store) ListActiveGrants(ctx context.Context) ([]Grant, error) {
	var out []Grant
	err := st.inTx(ctx, func(tx *sql.Tx) (err error) {
		out, err = loadGrants(ctx, tx, `g.status = ?`, StatusActive)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list active grants: %w", err)
	}
	return out, nil
}

// GetGrant returns one grant of any status, or ErrGrantNotFound.
func (st *Store) GetGrant(ctx context.Context, id int64) (Grant, error) {
	var out Grant
	err := st.inTx(ctx, func(tx *sql.Tx) (err error) {
		out, err = getGrant(ctx, tx, id)
		return err
	})
	if err != nil {
		return Grant{}, wrapGrantErr("get grant", err)
	}
	return out, nil
}

// ExtendGrant sets ends_at on an active grant and returns the updated row.
// A missing or non-active grant is ErrGrantNotFound.
func (st *Store) ExtendGrant(ctx context.Context, id int64, newEndsAt time.Time) (Grant, error) {
	var out Grant
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE grants SET ends_at = ? WHERE id = ? AND status = ?`, newEndsAt.Unix(), id, StatusActive)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrGrantNotFound
		}
		out, err = getGrant(ctx, tx, id)
		return err
	})
	if err != nil {
		return Grant{}, wrapGrantErr("extend grant", err)
	}
	return out, nil
}

// SetGrantStatus is a compare-and-set: the row moves from → to only if it is
// still in from, and on success its services stop counting as live so the
// (child, service) slot frees. It reports whether a row changed; an unknown
// id or a stale from is (false, nil), not an error.
func (st *Store) SetGrantStatus(ctx context.Context, id int64, from, to string, at time.Time) (bool, error) {
	changed := false
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		var endedAt any
		if to != StatusActive {
			endedAt = at.Unix()
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE grants SET status = ?, ended_at = ? WHERE id = ? AND status = ?`, to, endedAt, id, from)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		changed = true
		active := 0
		if to == StatusActive {
			active = 1
		}
		_, err = tx.ExecContext(ctx, `UPDATE grant_services SET active = ? WHERE grant_id = ?`, active, id)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("set grant status: %w", err)
	}
	return changed, nil
}

func getGrant(ctx context.Context, q querier, id int64) (Grant, error) {
	out, err := loadGrants(ctx, q, `g.id = ?`, id)
	if err != nil {
		return Grant{}, err
	}
	if len(out) == 0 {
		return Grant{}, ErrGrantNotFound
	}
	return out[0], nil
}

// loadGrants folds grants matching where into structs. It issues two queries
// — grants joined to services, then their clients — each scanned to
// completion before the next starts (single-connection rule). Callers run it
// inside a transaction so the two reads see one snapshot.
func loadGrants(ctx context.Context, q querier, where string, args ...any) ([]Grant, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT g.id, g.child_id, g.status, g.started_at, g.ends_at, gs.service_id
		  FROM grants g
		  LEFT JOIN grant_services gs ON gs.grant_id = g.id
		 WHERE `+where+`
		 ORDER BY g.id ASC, gs.service_id ASC`, args...)
	if err != nil {
		return nil, err
	}
	out := []Grant{}
	index := map[int64]int{}
	for rows.Next() {
		var (
			g                 Grant
			startedAt, endsAt int64
			service           sql.NullString
		)
		if err := rows.Scan(&g.ID, &g.ChildID, &g.Status, &startedAt, &endsAt, &service); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := index[g.ID]
		if !ok {
			g.StartedAt = time.Unix(startedAt, 0).UTC()
			g.EndsAt = time.Unix(endsAt, 0).UTC()
			g.Services = []string{}
			g.Clients = []string{}
			index[g.ID] = len(out)
			out = append(out, g)
			i = len(out) - 1
		}
		if service.Valid {
			out[i].Services = append(out[i].Services, service.String)
		}
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

	rows, err = q.QueryContext(ctx, `
		SELECT gc.grant_id, gc.client_name
		  FROM grant_clients gc
		  JOIN grants g ON g.id = gc.grant_id
		 WHERE `+where+`
		 ORDER BY gc.grant_id ASC, gc.client_name ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id     int64
			client string
		)
		if err := rows.Scan(&id, &client); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Clients = append(out[i].Clients, client)
		}
	}
	return out, rows.Err()
}

// wrapGrantErr passes the package sentinels through untouched — callers
// match on them — and wraps everything else with the operation.
func wrapGrantErr(op string, err error) error {
	var overlap *ErrGrantOverlap
	if errors.Is(err, ErrGrantNotFound) || errors.As(err, &overlap) {
		return err
	}
	return fmt.Errorf("%s: %w", op, err)
}
