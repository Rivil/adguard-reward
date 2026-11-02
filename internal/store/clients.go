package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// activeGrantIDs selects the grants whose stored clients still drive a
// revert. Expired and ended grants are history and keep the names they had.
const activeGrantIDs = `SELECT id FROM grants WHERE status = 'active'`

// RenameClient follows an AdGuard persistent-client rename from oldName to
// newName in one transaction:
//
//   - every owner's child_clients row moves to newName (a client may be
//     shared by several children). An owner that already holds newName (a
//     stale self-mapping to a name AdGuard no longer knows) drops its old row
//     and keeps the existing one;
//   - every active grant's stored client moves to newName, deduplicated when
//     that grant already holds newName, so a running grant still reverts on
//     the device. Expired and ended grants are left as recorded.
//
// *ErrClientTaken is returned, with nothing written, when a child holds
// newName but not oldName — the lowest such child id is named. An oldName no
// child maps leaves child_clients untouched. A same-name rename is a no-op.
func (st *Store) RenameClient(ctx context.Context, oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		oldOwners, err := clientOwners(ctx, tx, oldName)
		if err != nil {
			return err
		}
		newOwners, err := clientOwners(ctx, tx, newName)
		if err != nil {
			return err
		}
		if outsider := firstOutsider(newOwners, oldOwners); outsider != nil {
			outsider.Client = newName
			return outsider
		}
		for _, o := range oldOwners {
			q := `UPDATE child_clients SET client_name = ? WHERE child_id = ? AND client_name = ?`
			args := []any{newName, o.ChildID, oldName}
			if holds(newOwners, o.ChildID) {
				q = `DELETE FROM child_clients WHERE child_id = ? AND client_name = ?`
				args = args[1:]
			}
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM grant_clients
			  WHERE client_name = ?
			    AND grant_id IN (`+activeGrantIDs+`)
			    AND grant_id IN (SELECT grant_id FROM grant_clients WHERE client_name = ?)`,
			oldName, newName); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE grant_clients SET client_name = ?
			  WHERE client_name = ? AND grant_id IN (`+activeGrantIDs+`)`,
			newName, oldName)
		return err
	})
	if err != nil {
		return wrapChildErr("rename client", err)
	}
	return nil
}

// AddChildClient maps one client to a child with a single insert — never a
// replace of the child's list. A child that already holds the name is a
// no-op, whoever else shares it; otherwise another child holding it is
// *ErrClientTaken naming the lowest owner id; an unknown child is
// ErrChildNotFound.
func (st *Store) AddChildClient(ctx context.Context, childID int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("add child client: empty client name")
	}
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := getChild(ctx, tx, childID); err != nil {
			return err
		}
		owners, err := clientOwners(ctx, tx, name)
		if err != nil {
			return err
		}
		if holds(owners, childID) {
			return nil
		}
		if len(owners) > 0 {
			owners[0].Client = name
			return owners[0]
		}
		return insertClients(ctx, tx, childID, []string{name})
	})
	if err != nil {
		return wrapChildErr("add child client", err)
	}
	return nil
}

// clientOwners returns every child that maps name, id ascending, or none.
// Each is an *ErrClientTaken so a conflict can be returned as-is once the
// caller fills in Client.
func clientOwners(ctx context.Context, tx *sql.Tx, name string) ([]*ErrClientTaken, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT c.id, c.name FROM child_clients cc JOIN children c ON c.id = cc.child_id
		  WHERE cc.client_name = ? ORDER BY c.id`, name)
	if err != nil {
		return nil, fmt.Errorf("client owners: %w", err)
	}
	defer rows.Close()
	var owners []*ErrClientTaken
	for rows.Next() {
		var o ErrClientTaken
		if err := rows.Scan(&o.ChildID, &o.ChildName); err != nil {
			return nil, fmt.Errorf("client owners: %w", err)
		}
		owners = append(owners, &o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("client owners: %w", err)
	}
	return owners, nil
}

// holds reports whether childID is among owners.
func holds(owners []*ErrClientTaken, childID int64) bool {
	for _, o := range owners {
		if o.ChildID == childID {
			return true
		}
	}
	return false
}

// firstOutsider returns the first of owners not in among, or nil.
func firstOutsider(owners, among []*ErrClientTaken) *ErrClientTaken {
	for _, o := range owners {
		if !holds(among, o.ChildID) {
			return o
		}
	}
	return nil
}
