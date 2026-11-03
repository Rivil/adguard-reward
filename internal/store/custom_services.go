package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// customPrefix marks a custom service id in the shared service-id space that
// grants and buttons use alongside AdGuard's built-in ids.
const customPrefix = "custom:"

// CustomService is a parent-defined service: a name and the domains it
// blocks, in the order the parent gave them. Domains is never nil.
type CustomService struct {
	ID      int64
	Name    string
	Domains []string
}

// ServiceID is the catalogue id grants and buttons use for s.
func (s CustomService) ServiceID() string { return CustomServiceID(s.ID) }

// CustomServiceID renders the catalogue id of custom service id.
func CustomServiceID(id int64) string { return customPrefix + strconv.FormatInt(id, 10) }

// ParseCustomID returns the row id of a "custom:<n>" catalogue id. Only the
// canonical form CustomServiceID produces is accepted: n is a positive
// decimal without sign or leading zeros.
func ParseCustomID(s string) (int64, bool) {
	rest, ok := strings.CutPrefix(s, customPrefix)
	if !ok || rest == "" || rest[0] < '1' || rest[0] > '9' {
		return 0, false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// IsCustomServiceID reports whether s is a well-formed custom catalogue id.
func IsCustomServiceID(s string) bool {
	_, ok := ParseCustomID(s)
	return ok
}

// ErrCustomServiceNotFound is returned for an id with no row.
var ErrCustomServiceNotFound = errors.New("custom service not found")

// ErrServiceNameTaken is returned when another custom service already has
// the name, compared case-insensitively. errors.Is also matches it against
// ErrNameTaken.
var ErrServiceNameTaken error = serviceNameTaken{}

type serviceNameTaken struct{}

func (serviceNameTaken) Error() string        { return "custom service name already taken" }
func (serviceNameTaken) Is(target error) bool { return target == ErrNameTaken }

// ErrDomainTaken says which custom service already owns a domain. Nothing
// is written.
type ErrDomainTaken struct {
	Domain  string
	OwnerID int64
	Owner   string // the owning service's name
}

func (e *ErrDomainTaken) Error() string {
	return fmt.Sprintf("domain %q already belongs to custom service %q", e.Domain, e.Owner)
}

// ErrServiceInUse refuses a delete while an active grant covers the service
// (locked delete_refs): deleting it would silently re-block mid-reward.
type ErrServiceInUse struct {
	ServiceID string
	GrantID   int64
}

func (e *ErrServiceInUse) Error() string {
	return fmt.Sprintf("custom service %s is in use by active grant %d", e.ServiceID, e.GrantID)
}

// CreateCustomService stores a new service with its domains in one
// transaction. Domains are stored as given: callers validate and normalise
// them first. A name already used (any case) is ErrServiceNameTaken, a domain
// another service owns is *ErrDomainTaken and a domain repeated in the call
// is an error; in each case nothing is inserted.
func (st *Store) CreateCustomService(ctx context.Context, name string, domains []string) (CustomService, error) {
	var out CustomService
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkServiceConflicts(ctx, tx, 0, name, domains); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO custom_services (name, name_key) VALUES (?, ?)`, name, nameKey(name))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := insertDomains(ctx, tx, id, domains); err != nil {
			return err
		}
		out, err = getCustomService(ctx, tx, id)
		return err
	})
	if err != nil {
		return CustomService{}, wrapServiceErr("create custom service", err)
	}
	return out, nil
}

// GetCustomService returns one service, or ErrCustomServiceNotFound.
func (st *Store) GetCustomService(ctx context.Context, id int64) (CustomService, error) {
	var out CustomService
	err := st.inTx(ctx, func(tx *sql.Tx) (err error) {
		out, err = getCustomService(ctx, tx, id)
		return err
	})
	if err != nil {
		return CustomService{}, wrapServiceErr("get custom service", err)
	}
	return out, nil
}

// ListCustomServices returns every service by id, each with its domains in
// stored order. The result is never nil.
func (st *Store) ListCustomServices(ctx context.Context) ([]CustomService, error) {
	var out []CustomService
	err := st.inTx(ctx, func(tx *sql.Tx) (err error) {
		out, err = loadCustomServices(ctx, tx, 0)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list custom services: %w", err)
	}
	return out, nil
}

// UpdateCustomService replaces the name and the whole domain list of id in
// one transaction, with the same conflict rules as create (the service's own
// name and domains never conflict with themselves). Any failure leaves the
// previous name and domains intact.
func (st *Store) UpdateCustomService(ctx context.Context, id int64, name string, domains []string) (CustomService, error) {
	var out CustomService
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := getCustomService(ctx, tx, id); err != nil {
			return err
		}
		if err := checkServiceConflicts(ctx, tx, id, name, domains); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE custom_services SET name = ?, name_key = ? WHERE id = ?`, name, nameKey(name), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM custom_service_domains WHERE service_id = ?`, id); err != nil {
			return err
		}
		if err := insertDomains(ctx, tx, id, domains); err != nil {
			return err
		}
		var err error
		out, err = getCustomService(ctx, tx, id)
		return err
	})
	if err != nil {
		return CustomService{}, wrapServiceErr("update custom service", err)
	}
	return out, nil
}

// DeleteCustomService removes id in one transaction. While an active grant
// lists the service it returns *ErrServiceInUse naming the lowest such grant
// and changes nothing. Otherwise the service id is stripped from every
// button's services (the rest keep their order), a button left with no
// services is deleted, a button icon equal to the id is cleared, and the
// service and its domains go. An unknown id is ErrCustomServiceNotFound.
func (st *Store) DeleteCustomService(ctx context.Context, id int64) error {
	svcID := CustomServiceID(id)
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := getCustomService(ctx, tx, id); err != nil {
			return err
		}
		var grantID int64
		err := tx.QueryRowContext(ctx, `
			SELECT g.id FROM grants g JOIN grant_services gs ON gs.grant_id = g.id
			WHERE g.status = ? AND gs.service_id = ? ORDER BY g.id ASC LIMIT 1`,
			StatusActive, svcID).Scan(&grantID)
		if err == nil {
			return &ErrServiceInUse{ServiceID: svcID, GrantID: grantID}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		affected, err := scanIDs(ctx, tx,
			`SELECT DISTINCT button_id FROM button_services WHERE service_id = ?`, svcID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM button_services WHERE service_id = ?`, svcID); err != nil {
			return err
		}
		for _, b := range affected {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM buttons WHERE id = ?
				AND NOT EXISTS (SELECT 1 FROM button_services WHERE button_id = ?)`, b, b); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE buttons SET icon = NULL WHERE icon = ?`, svcID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM custom_service_domains WHERE service_id = ?`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM custom_services WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return wrapServiceErr("delete custom service", err)
	}
	return nil
}

func nameKey(name string) string { return strings.ToLower(name) }

// checkServiceConflicts enforces the locked uniqueness rules against every
// service other than selfID (0 for a create), before anything is written.
func checkServiceConflicts(ctx context.Context, tx *sql.Tx, selfID int64, name string, domains []string) error {
	seen := make(map[string]bool, len(domains))
	for _, d := range domains {
		if seen[d] {
			return fmt.Errorf("domain %q is listed twice", d)
		}
		seen[d] = true
	}
	var other int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM custom_services WHERE name_key = ? AND id <> ?`, nameKey(name), selfID).Scan(&other)
	if err == nil {
		return ErrServiceNameTaken
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, d := range domains {
		var (
			ownerID int64
			owner   string
		)
		err := tx.QueryRowContext(ctx, `
			SELECT s.id, s.name FROM custom_service_domains d JOIN custom_services s ON s.id = d.service_id
			WHERE d.domain = ? AND d.service_id <> ?`, d, selfID).Scan(&ownerID, &owner)
		if err == nil {
			return &ErrDomainTaken{Domain: d, OwnerID: ownerID, Owner: owner}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func insertDomains(ctx context.Context, tx *sql.Tx, id int64, domains []string) error {
	for i, d := range domains {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO custom_service_domains (service_id, position, domain) VALUES (?, ?, ?)`,
			id, i, d); err != nil {
			return err
		}
	}
	return nil
}

func getCustomService(ctx context.Context, q querier, id int64) (CustomService, error) {
	list, err := loadCustomServices(ctx, q, id)
	if err != nil {
		return CustomService{}, err
	}
	if len(list) == 0 {
		return CustomService{}, ErrCustomServiceNotFound
	}
	return list[0], nil
}

// loadCustomServices reads services (only id when non-zero), then their
// domains, each scanned to completion before the next query (single
// connection rule).
func loadCustomServices(ctx context.Context, q querier, only int64) ([]CustomService, error) {
	where, args := "", []any{}
	if only != 0 {
		where, args = ` WHERE id = ?`, []any{only}
	}
	rows, err := q.QueryContext(ctx, `SELECT id, name FROM custom_services`+where+` ORDER BY id ASC`, args...)
	if err != nil {
		return nil, err
	}
	out := []CustomService{}
	index := map[int64]int{}
	for rows.Next() {
		var s CustomService
		if err := rows.Scan(&s.ID, &s.Name); err != nil {
			rows.Close()
			return nil, err
		}
		s.Domains = []string{}
		index[s.ID] = len(out)
		out = append(out, s)
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

	where = ""
	if only != 0 {
		where = ` WHERE service_id = ?`
	}
	rows, err = q.QueryContext(ctx,
		`SELECT service_id, domain FROM custom_service_domains`+where+` ORDER BY service_id ASC, position ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id int64
			d  string
		)
		if err := rows.Scan(&id, &d); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Domains = append(out[i].Domains, d)
		}
	}
	return out, rows.Err()
}

func scanIDs(ctx context.Context, q querier, query string, args ...any) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// wrapServiceErr passes this file's sentinels and typed errors through
// untouched — callers match on them — and wraps everything else.
func wrapServiceErr(op string, err error) error {
	var (
		taken *ErrDomainTaken
		inUse *ErrServiceInUse
	)
	if errors.Is(err, ErrServiceNameTaken) || errors.Is(err, ErrCustomServiceNotFound) ||
		errors.As(err, &taken) || errors.As(err, &inUse) {
		return err
	}
	return fmt.Errorf("%s: %w", op, err)
}
