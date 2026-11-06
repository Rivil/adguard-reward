package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HistoryPageSize is the number of grants one history page holds (locked
// retention: newest 50, then load more).
const HistoryPageSize = 50

// visibleGrants is the FROM clause of every history read, aliasing grants g
// and children c. A grant is visible only while its child exists and its id is
// above that child's watermark, so a deleted child's grants drop out and a
// reused child id never inherits them. Grants outlive their child (locked
// client_set), so this join is the only thing that hides them.
const visibleGrants = `grants g
	JOIN children c ON c.id = g.child_id AND g.id > c.history_after_grant_id`

// How a grant ended, as a parent filters on it. Each stored (status,
// end_reason) pair falls under exactly one of these.
const (
	HistoryOngoing        = "ongoing"
	HistoryExpired        = "expired"
	HistoryEndedEarly     = "ended_early"
	HistoryPermanentBlock = "permanent_block"
	HistoryEndedUnknown   = "ended_unknown"
)

// historyStatusConds maps each filter status to its predicate on grants g. Only
// an ended grant's end_reason decides anything: status alone settles the rest.
var historyStatusConds = map[string]string{
	HistoryOngoing:        `g.status = 'active'`,
	HistoryExpired:        `(g.status = 'expired' OR (g.status = 'ended' AND g.end_reason = 'expired'))`,
	HistoryEndedEarly:     `(g.status = 'ended' AND g.end_reason = 'ended_early')`,
	HistoryPermanentBlock: `(g.status = 'ended' AND g.end_reason = 'permanent_block')`,
	HistoryEndedUnknown:   `(g.status = 'ended' AND g.end_reason IS NULL)`,
}

// historyServiceCond matches a grant that holds the service or had it trimmed
// off. EXISTS, not a join, so a grant lists once and keeps all its services.
const historyServiceCond = `(EXISTS (SELECT 1 FROM grant_services s WHERE s.grant_id = g.id AND s.service_id = ?)
	OR EXISTS (SELECT 1 FROM grant_service_removals r WHERE r.grant_id = g.id AND r.service_id = ?))`

// HistoryFilter narrows ListHistory; filters combine with AND. Every field's
// zero value means no filter.
type HistoryFilter struct {
	// ChildID picks one child; 0 means every child.
	ChildID int64
	// ServiceID is an exact service id, live on the grant or trimmed off it.
	ServiceID string
	// Status is one of the History* statuses.
	Status string
	// From is inclusive and Until exclusive, both on when the grant started
	// (locked date_range).
	From, Until time.Time
	// Before is the keyset cursor: only grants with a lower id are read.
	Before int64
}

// GrantHistory is one page of grant history, newest first. ChildID and
// ChildName name the child the page was filtered to, zero on a page across
// all children.
type GrantHistory struct {
	ChildID   int64
	ChildName string
	// Grants is newest first by id, never nil.
	Grants []HistoryGrant
	// HasMore says an older page exists; its cursor is the last grant's id.
	HasMore bool
}

// HistoryGrant is a grant as history shows it. EndReason is "" for a live
// grant and for every grant that ended before reasons were recorded — never
// inferred (locked legacy_end_reason). EndedAt is nil while active. ChildID
// and ChildName are the grant's own child on ListHistory and zero on
// ListGrantHistory, whose page already names the child.
type HistoryGrant struct {
	ID         int64
	ChildID    int64
	ChildName  string
	Status     string
	EndReason  string
	StartedAt  time.Time
	EndsAt     time.Time
	EndedAt    *time.Time
	Services   []HistoryService
	Extensions []GrantExtension
}

// HistoryService is one service a grant covered, whether it stayed on the
// grant or a permanent block trimmed it off. Name is the snapshot taken at
// grant start, "" when none was stored (pre-upgrade grants); callers resolve
// it further. RemovedAt is nil and RemovedReason "" while the service stayed.
type HistoryService struct {
	ID            string
	Name          string
	RemovedAt     *time.Time
	RemovedReason string
}

// GrantExtension is one recorded extension of a grant.
type GrantExtension struct {
	At        time.Time
	OldEndsAt time.Time
	NewEndsAt time.Time
}

// ListHistory returns up to limit grants matching f across every child,
// newest first by id, each naming its child and carrying the same details as
// ListGrantHistory. Hidden grants are excluded in SQL, before the limit, so a
// page is always full while visible grants remain. The cursor is a plain id:
// it need not belong to a grant that is still visible. A ChildID no child row
// carries, negative ids included, is ErrChildNotFound; limit <= 0 means
// HistoryPageSize.
func (st *Store) ListHistory(ctx context.Context, f HistoryFilter, limit int) (GrantHistory, error) {
	var out GrantHistory
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if f.ChildID != 0 {
			if err := historyChild(ctx, tx, f.ChildID, &out); err != nil {
				return err
			}
		}
		return historyPage(ctx, tx, f, limit, &out)
	})
	if err != nil {
		return GrantHistory{}, wrapChildErr("list history", err)
	}
	return out, nil
}

// ListGrantHistory returns up to limit of childID's grants with id below
// before (0: from the newest), newest first, each with its live and removed
// services, their snapshot names and its extensions in ascending order. Only
// grants with id above the child's history_after_grant_id are listed, so a
// reused child id never inherits an earlier child's history. Everything is
// read in one transaction. An unknown child is ErrChildNotFound, id 0 and
// below included: unlike ListHistory, 0 here never means every child. limit
// <= 0 means HistoryPageSize.
func (st *Store) ListGrantHistory(ctx context.Context, childID, before int64, limit int) (GrantHistory, error) {
	var out GrantHistory
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if err := historyChild(ctx, tx, childID, &out); err != nil {
			return err
		}
		return historyPage(ctx, tx, HistoryFilter{ChildID: childID, Before: before}, limit, &out)
	})
	if err != nil {
		return GrantHistory{}, wrapChildErr("list grant history", err)
	}
	for i := range out.Grants {
		out.Grants[i].ChildID, out.Grants[i].ChildName = 0, ""
	}
	return out, nil
}

// historyChild names the page after childID, or fails with ErrChildNotFound.
func historyChild(ctx context.Context, tx *sql.Tx, childID int64, out *GrantHistory) error {
	err := tx.QueryRowContext(ctx, `SELECT id, name FROM children WHERE id = ?`, childID).
		Scan(&out.ChildID, &out.ChildName)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrChildNotFound
	}
	return err
}

// historyPage fills out with one page of grants and their details, reading
// one row past the limit to learn whether an older page exists.
func historyPage(ctx context.Context, tx *sql.Tx, f HistoryFilter, limit int, out *GrantHistory) error {
	if limit <= 0 {
		limit = HistoryPageSize
	}
	grants, err := historyGrants(ctx, tx, f, limit+1)
	if err != nil {
		return err
	}
	if len(grants) > limit {
		grants = grants[:limit]
		out.HasMore = true
	}
	if err := historyDetails(ctx, tx, grants); err != nil {
		return err
	}
	out.Grants = grants
	return nil
}

// historyWhere builds the WHERE clause for f, "" when nothing narrows the read.
// Every filter sits here, inside the keyset query, so the limit and HasMore
// count filtered rows. A Status outside the History* set is an error, never an
// unfiltered read.
func historyWhere(f HistoryFilter) (string, []any, error) {
	var (
		conds []string
		args  []any
	)
	if f.ChildID != 0 {
		conds, args = append(conds, `g.child_id = ?`), append(args, f.ChildID)
	}
	if f.ServiceID != "" {
		conds, args = append(conds, historyServiceCond), append(args, f.ServiceID, f.ServiceID)
	}
	if f.Status != "" {
		cond, ok := historyStatusConds[f.Status]
		if !ok {
			return "", nil, fmt.Errorf("unknown history status %q", f.Status)
		}
		conds = append(conds, cond)
	}
	if !f.From.IsZero() {
		conds, args = append(conds, `g.started_at >= ?`), append(args, f.From.Unix())
	}
	if !f.Until.IsZero() {
		conds, args = append(conds, `g.started_at < ?`), append(args, f.Until.Unix())
	}
	if f.Before != 0 {
		conds, args = append(conds, `g.id < ?`), append(args, f.Before)
	}
	if len(conds) == 0 {
		return "", nil, nil
	}
	return ` WHERE ` + strings.Join(conds, ` AND `), args, nil
}

// historyGrants reads up to n visible grant rows matching f, newest first.
func historyGrants(ctx context.Context, tx *sql.Tx, f HistoryFilter, n int) ([]HistoryGrant, error) {
	where, args, err := historyWhere(f)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT g.id, g.child_id, c.name, g.status, g.end_reason, g.started_at, g.ends_at, g.ended_at
		  FROM `+visibleGrants+where+`
		 ORDER BY g.id DESC
		 LIMIT ?`, append(args, n)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryGrant{}
	for rows.Next() {
		var (
			g                 HistoryGrant
			reason            sql.NullString
			startedAt, endsAt int64
			endedAt           sql.NullInt64
		)
		if err := rows.Scan(&g.ID, &g.ChildID, &g.ChildName, &g.Status, &reason, &startedAt, &endsAt, &endedAt); err != nil {
			return nil, err
		}
		g.EndReason = reason.String
		g.StartedAt = time.Unix(startedAt, 0).UTC()
		g.EndsAt = time.Unix(endsAt, 0).UTC()
		g.EndedAt = unixPtr(endedAt)
		g.Services = []HistoryService{}
		g.Extensions = []GrantExtension{}
		out = append(out, g)
	}
	return out, rows.Err()
}

// historyDetails fills each grant's services and extensions. Live and
// removed services are unioned before names are joined, so a trimmed
// service keeps its snapshot name though its grant_services row is gone.
// Each query is scanned to completion before the next (single connection).
func historyDetails(ctx context.Context, tx *sql.Tx, grants []HistoryGrant) error {
	if len(grants) == 0 {
		return nil
	}
	index := make(map[int64]int, len(grants))
	ids := make([]any, len(grants))
	for i, g := range grants {
		index[g.ID] = i
		ids[i] = g.ID
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"

	rows, err := tx.QueryContext(ctx, `
		SELECT s.grant_id, s.service_id, COALESCE(n.name, ''), s.removed_at, s.reason
		  FROM (SELECT grant_id, service_id, NULL AS removed_at, NULL AS reason
		          FROM grant_services WHERE grant_id IN `+in+`
		        UNION ALL
		        SELECT grant_id, service_id, removed_at, reason
		          FROM grant_service_removals WHERE grant_id IN `+in+`) s
		  LEFT JOIN grant_service_names n ON n.grant_id = s.grant_id AND n.service_id = s.service_id
		 ORDER BY s.grant_id, s.service_id`, append(append([]any{}, ids...), ids...)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			grantID   int64
			svc       HistoryService
			removedAt sql.NullInt64
			reason    sql.NullString
		)
		if err := rows.Scan(&grantID, &svc.ID, &svc.Name, &removedAt, &reason); err != nil {
			rows.Close()
			return err
		}
		svc.RemovedAt = unixPtr(removedAt)
		svc.RemovedReason = reason.String
		g := &grants[index[grantID]]
		g.Services = append(g.Services, svc)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT grant_id, extended_at, old_ends_at, new_ends_at
		  FROM grant_extensions WHERE grant_id IN `+in+`
		 ORDER BY grant_id, id`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var grantID, at, oldEnds, newEnds int64
		if err := rows.Scan(&grantID, &at, &oldEnds, &newEnds); err != nil {
			return err
		}
		g := &grants[index[grantID]]
		g.Extensions = append(g.Extensions, GrantExtension{
			At:        time.Unix(at, 0).UTC(),
			OldEndsAt: time.Unix(oldEnds, 0).UTC(),
			NewEndsAt: time.Unix(newEnds, 0).UTC(),
		})
	}
	return rows.Err()
}

func unixPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}
