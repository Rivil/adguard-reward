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
//   - the child_clients row moves to newName. When the same child already
//     holds newName (a stale self-mapping to a name AdGuard no longer knows)
//     the old row is dropped and the existing one kept;
//   - every active grant's stored client moves to newName, deduplicated when
//     that grant already holds newName, so a running grant still reverts on
//     the device. Expired and ended grants are left as recorded.
//
// *ErrClientTaken is returned, with nothing written, when a child other than
// oldName's owner holds newName. An oldName no child maps leaves
// child_clients untouched. A same-name rename is a no-op.
func (st *Store) RenameClient(ctx context.Context, oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		oldOwner, err := clientOwner(ctx, tx, oldName)
		if err != nil {
			return err
		}
		newOwner, err := clientOwner(ctx, tx, newName)
		if err != nil {
			return err
		}
		switch {
		case newOwner != nil && (oldOwner == nil || newOwner.ChildID != oldOwner.ChildID):
			newOwner.Client = newName
			return newOwner
		case newOwner != nil:
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM child_clients WHERE client_name = ?`, oldName); err != nil {
				return err
			}
		case oldOwner != nil:
			if _, err := tx.ExecContext(ctx,
				`UPDATE child_clients SET client_name = ? WHERE client_name = ?`, newName, oldName); err != nil {
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
// no-op; another child holding it is *ErrClientTaken; an unknown child is
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
		owner, err := clientOwner(ctx, tx, name)
		if err != nil {
			return err
		}
		if owner != nil {
			if owner.ChildID == childID {
				return nil
			}
			owner.Client = name
			return owner
		}
		return insertClients(ctx, tx, childID, []string{name})
	})
	if err != nil {
		return wrapChildErr("add child client", err)
	}
	return nil
}

// clientOwner returns the child that maps name, or nil when none does. The
// result is an *ErrClientTaken so a conflict can be returned as-is once the
// caller fills in Client.
func clientOwner(ctx context.Context, tx *sql.Tx, name string) (*ErrClientTaken, error) {
	var owner ErrClientTaken
	err := tx.QueryRowContext(ctx,
		`SELECT c.id, c.name FROM child_clients cc JOIN children c ON c.id = cc.child_id
		  WHERE cc.client_name = ?`, name).Scan(&owner.ChildID, &owner.ChildName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("client owner: %w", err)
	}
	return &owner, nil
}
