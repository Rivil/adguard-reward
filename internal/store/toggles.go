package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Toggle states: a parent's permanent intent for one (child, service).
const (
	ToggleBlocked   = "blocked"
	ToggleUnblocked = "unblocked"
)

// EndReasonPermanentBlock is the grants.end_reason of a grant a block
// toggle emptied, and the grant_service_removals.reason of every service a
// block toggle trimmed off a live grant.
const EndReasonPermanentBlock = "permanent_block"

// ServiceToggle is one stored permanent toggle.
type ServiceToggle struct {
	ChildID   int64
	ServiceID string
	State     string
	UpdatedAt time.Time
}

// ToggleChange reports what a toggle did to the child's live grants.
// Trimmed is the grant as it was before the toggle removed the service from
// it (nil when no grant was touched); Ended says the trim left it with no
// services, so it was ended.
type ToggleChange struct {
	Trimmed *Grant
	Ended   bool
}

// ErrInvalidToggleState rejects a state other than blocked / unblocked.
var ErrInvalidToggleState = errors.New("invalid toggle state")

// SetServiceToggle upserts the (childID, serviceID) toggle. For
// state=blocked the same transaction removes serviceID from the child's
// active grant covering it (at most one, by grant_services_live_idx) and
// ends that grant at `at` if it is left with no services (locked
// block_during_grant). Either everything is written or nothing is. An
// unknown child is ErrChildNotFound; serviceID is not checked here.
func (st *Store) SetServiceToggle(ctx context.Context, childID int64, serviceID, state string, at time.Time) (ToggleChange, error) {
	if state != ToggleBlocked && state != ToggleUnblocked {
		return ToggleChange{}, fmt.Errorf("%w: %q", ErrInvalidToggleState, state)
	}
	var out ToggleChange
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := getChild(ctx, tx, childID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO service_toggles (child_id, service_id, state, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (child_id, service_id) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
			childID, serviceID, state, at.Unix()); err != nil {
			return err
		}
		if state != ToggleBlocked {
			return nil
		}
		var err error
		out, err = trimGrant(ctx, tx, childID, serviceID, at)
		return err
	})
	if err != nil {
		return ToggleChange{}, wrapToggleErr("set service toggle", err)
	}
	return out, nil
}

// trimGrant removes serviceID from the child's live grant, if any, and ends
// the grant when no service is left on it. The removal is logged in
// grant_service_removals in the same tx so history still lists the service,
// and a grant the trim empties records end_reason 'permanent_block'.
func trimGrant(ctx context.Context, tx *sql.Tx, childID int64, serviceID string, at time.Time) (ToggleChange, error) {
	var grantID int64
	err := tx.QueryRowContext(ctx,
		`SELECT grant_id FROM grant_services WHERE child_id = ? AND service_id = ? AND active = 1`,
		childID, serviceID).Scan(&grantID)
	if errors.Is(err, sql.ErrNoRows) {
		return ToggleChange{}, nil
	}
	if err != nil {
		return ToggleChange{}, err
	}
	pre, err := getGrant(ctx, tx, grantID)
	if err != nil {
		return ToggleChange{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM grant_services WHERE grant_id = ? AND service_id = ?`, grantID, serviceID); err != nil {
		return ToggleChange{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO grant_service_removals (grant_id, service_id, removed_at, reason) VALUES (?, ?, ?, ?)`,
		grantID, serviceID, at.Unix(), EndReasonPermanentBlock); err != nil {
		return ToggleChange{}, err
	}
	change := ToggleChange{Trimmed: &pre}
	var left int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM grant_services WHERE grant_id = ?`, grantID).Scan(&left); err != nil {
		return ToggleChange{}, err
	}
	if left > 0 {
		return change, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE grants SET status = ?, ended_at = ?, end_reason = ? WHERE id = ? AND status = ?`,
		StatusEnded, at.Unix(), EndReasonPermanentBlock, grantID, StatusActive); err != nil {
		return ToggleChange{}, err
	}
	change.Ended = true
	return change, nil
}

// ListServiceToggles returns every stored toggle ordered by (child_id,
// service_id). The result is never nil.
func (st *Store) ListServiceToggles(ctx context.Context) ([]ServiceToggle, error) {
	rows, err := st.db.QueryContext(ctx,
		`SELECT child_id, service_id, state, updated_at FROM service_toggles ORDER BY child_id ASC, service_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list service toggles: %w", err)
	}
	defer rows.Close()
	out := []ServiceToggle{}
	for rows.Next() {
		var (
			t       ServiceToggle
			updated int64
		)
		if err := rows.Scan(&t.ChildID, &t.ServiceID, &t.State, &updated); err != nil {
			return nil, fmt.Errorf("list service toggles: %w", err)
		}
		t.UpdatedAt = time.Unix(updated, 0).UTC()
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list service toggles: %w", err)
	}
	return out, nil
}

// wrapToggleErr passes the package sentinels through untouched and wraps
// everything else with the operation.
func wrapToggleErr(op string, err error) error {
	if errors.Is(err, ErrChildNotFound) || errors.Is(err, ErrInvalidToggleState) {
		return err
	}
	return fmt.Errorf("%s: %w", op, err)
}
