package store

import (
	"context"
	"fmt"
)

// HistoryServiceOption is one service a parent can filter history by. Name is
// the newest snapshot taken of it on a visible grant, "" when none was stored
// (pre-upgrade grants); callers resolve it further.
type HistoryServiceOption struct {
	ID   string
	Name string
}

// historyServicesQuery lists every service on a visible grant, live or trimmed
// off, keeping one row each: the newest grant that has a snapshot name, or
// failing that any row, which then reads "". A newer grant with no snapshot
// never blanks a name an older one recorded.
const historyServicesQuery = `
	SELECT service_id, name FROM (
		SELECT u.service_id, COALESCE(n.name, '') AS name,
		       ROW_NUMBER() OVER (PARTITION BY u.service_id ORDER BY n.name IS NULL, u.grant_id DESC) AS pos
		  FROM (SELECT s.grant_id, s.service_id
		          FROM ` + visibleGrants + `
		          JOIN grant_services s ON s.grant_id = g.id
		        UNION
		        SELECT r.grant_id, r.service_id
		          FROM ` + visibleGrants + `
		          JOIN grant_service_removals r ON r.grant_id = g.id) u
		  LEFT JOIN grant_service_names n ON n.grant_id = u.grant_id AND n.service_id = u.service_id)
	 WHERE pos = 1
	 ORDER BY service_id`

// ListHistoryServices returns every service that appears in visible history,
// one row each by id, deleted custom services included (locked service_filter).
// The list is sorted by id and never nil.
func (st *Store) ListHistoryServices(ctx context.Context) ([]HistoryServiceOption, error) {
	rows, err := st.db.QueryContext(ctx, historyServicesQuery)
	if err != nil {
		return nil, fmt.Errorf("list history services: %w", err)
	}
	defer rows.Close()
	out := []HistoryServiceOption{}
	for rows.Next() {
		var o HistoryServiceOption
		if err := rows.Scan(&o.ID, &o.Name); err != nil {
			return nil, fmt.Errorf("list history services: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list history services: %w", err)
	}
	return out, nil
}
