package grants

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Rivil/adguard-reward/internal/store"
)

// RenameClient renames a persistent client in AdGuard and then follows it
// in the store — the child mapping and the clients stored on active grants
// — so a running grant still reverts on the renamed device (locked:
// rename_scope: any listed client, assigned or not). It runs under the
// engine mutex, so no grant apply, End or expiry reads the store between
// the two writes.
//
// A newName another child already maps (a stale mapping AdGuard no longer
// knows) is *store.ErrClientTaken before AdGuard is touched. AdGuard's own
// refusals — adguard.ErrClientExists, adguard.ErrClientNotFound — come back
// as they are, with nothing written. When the store write fails after
// AdGuard renamed, the client is renamed back in AdGuard and the returned
// error names both names. A same-name rename is a no-op.
func (e *Engine) RenameClient(ctx context.Context, oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.checkRename(ctx, oldName, newName); err != nil {
		return err
	}
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	if err := e.ag.RenameClient(actx, oldName, newName); err != nil {
		return err
	}
	// AdGuard has the new name; record it even if the request has gone.
	serr := e.store.RenameClient(context.WithoutCancel(ctx), oldName, newName)
	if serr == nil {
		return nil
	}
	if aerr := e.ag.RenameClient(actx, newName, oldName); aerr != nil {
		e.log.Error("client rename: store failed and AdGuard could not be renamed back",
			"from", oldName, "to", newName, "store_err", serr, "adguard_err", aerr)
		return fmt.Errorf("grants: rename %q to %q: store failed and AdGuard still has %q: %w",
			oldName, newName, newName, errors.Join(serr, aerr))
	}
	return fmt.Errorf("grants: rename %q to %q: store failed, AdGuard renamed back: %w", oldName, newName, serr)
}

// checkRename mirrors the store's ownership rule before any write: newName
// held by a child other than oldName's owner is *store.ErrClientTaken. The
// same child holding both (a stale self-mapping) is allowed — the store
// rename collapses it. Caller holds mu.
func (e *Engine) checkRename(ctx context.Context, oldName, newName string) error {
	children, err := e.store.ListChildren(ctx)
	if err != nil {
		return err
	}
	var oldOwner, newOwner *store.Child
	for i := range children {
		if slices.Contains(children[i].Clients, oldName) {
			oldOwner = &children[i]
		}
		if slices.Contains(children[i].Clients, newName) {
			newOwner = &children[i]
		}
	}
	if newOwner != nil && (oldOwner == nil || newOwner.ID != oldOwner.ID) {
		return &store.ErrClientTaken{Client: newName, ChildID: newOwner.ID, ChildName: newOwner.Name}
	}
	return nil
}
