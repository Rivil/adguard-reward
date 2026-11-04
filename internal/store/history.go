package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// HistoryPageSize is the number of grants one history page holds (locked
// retention: newest 50, then load more).
const HistoryPageSize = 50

// GrantHistory is one page of a child's grant history, newest first.
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
// inferred (locked legacy_end_reason). EndedAt is nil while active.
type HistoryGrant struct {
	ID         int64
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

// ListGrantHistory returns up to limit of childID's grants with id below
// before (0: from the newest), newest first, each with its live and removed
// services, their snapshot names and its extensions in ascending order. Only
// grants with id above the child's history_after_grant_id are listed, so a
// reused child id never inherits an earlier child's history. Everything is
// read in one transaction. An unknown child is ErrChildNotFound; limit <= 0
// means HistoryPageSize.
func (st *Store) ListGrantHistory(ctx context.Context, childID, before int64, limit int) (GrantHistory, error) {
	if limit <= 0 {
		limit = HistoryPageSize
	}
	var out GrantHistory
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		var after int64
		err := tx.QueryRowContext(ctx,
			`SELECT id, name, history_after_grant_id FROM children WHERE id = ?`, childID).
			Scan(&out.ChildID, &out.ChildName, &after)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrChildNotFound
		}
		if err != nil {
			return err
		}
		grants, err := historyGrants(ctx, tx, childID, after, before, limit+1)
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
	})
	if err != nil {
		return GrantHistory{}, wrapChildErr("list grant history", err)
	}
	return out, nil
}

// historyGrants reads up to n grant rows, newest first.
func historyGrants(ctx context.Context, tx *sql.Tx, childID, after, before int64, n int) ([]HistoryGrant, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, status, end_reason, started_at, ends_at, ended_at
		  FROM grants
		 WHERE child_id = ? AND id > ? AND (? = 0 OR id < ?)
		 ORDER BY id DESC
		 LIMIT ?`, childID, after, before, before, n)
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
		if err := rows.Scan(&g.ID, &g.Status, &reason, &startedAt, &endsAt, &endedAt); err != nil {
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
