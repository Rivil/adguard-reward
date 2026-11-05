package grants

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/store"
)

// ErrAdoptFailed names the client whose baseline could not be written.
// Clients before it in the call may already carry their baseline — a
// superset of what they blocked, so a retry is safe.
type ErrAdoptFailed struct {
	Client string
	Err    error
}

func (e *ErrAdoptFailed) Error() string {
	return fmt.Sprintf("grants: could not apply %q's blocked services: %v", e.Client, e.Err)
}

func (e *ErrAdoptFailed) Unwrap() error { return e.Err }

// ErrClientsRead marks AdoptClients' read of AdGuard failing, whatever the
// cause: an undecodable 200 body has no status or transport error to know it
// by, and only the mark tells it from a store read failing. No client has been
// written.
type ErrClientsRead struct {
	Err error
}

func (e *ErrClientsRead) Error() string {
	return fmt.Sprintf("grants: could not read AdGuard's clients: %v", e.Err)
}

func (e *ErrClientsRead) Unwrap() error { return e.Err }

// CreateClient creates an AdGuard persistent client and, when childID is
// non-nil, maps it to that child in the same call (locked:
// new_client_blocked_set_v2). An assigned client gets its own list — the
// child's baseline (baselineFrom), the child's permanent toggles included —
// with use_global_blocked_services=false, so a grant running for the child
// never leaves the new device open after expiry; an unassigned one keeps
// AdGuard's defaults. It runs under the engine mutex with one
// ApplyTimeout-bounded context, so no grant applies or reverts between the
// baseline read and the add.
//
// An unknown child is store.ErrChildNotFound before AdGuard is touched. If
// the store mapping fails after the add, the client stays in AdGuard with
// its baseline and the error is returned.
func (e *Engine) CreateClient(ctx context.Context, name string, ids []string, childID *int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	nc := adguard.NewClient{Name: name, IDs: ids}
	var child store.Child
	if childID != nil {
		var err error
		if child, err = e.store.GetChild(ctx, *childID); err != nil {
			return err
		}
	}
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	if childID != nil {
		base, err := e.baseline(ctx, actx, child.ID, child.Clients, name)
		if err != nil {
			return err
		}
		nc.Blocked = &base
	}
	if err := e.ag.AddClient(actx, nc); err != nil {
		return err
	}
	if childID == nil {
		return nil
	}
	if err := e.store.AddChildClient(context.WithoutCancel(ctx), child.ID, name); err != nil {
		return fmt.Errorf("client %q created in AdGuard but not assigned to %q: %w", name, child.Name, err)
	}
	// The new device is a child client now: block the custom services on it
	// before returning. A failure leaves it to the reconciler.
	e.resyncRules(context.WithoutCancel(ctx), actx, "client create", name)
	return nil
}

// AdoptClients gives each client being added to a child the child's
// baseline (baselineFrom), computed from siblings — the clients that stay
// with the child — and from the client's current owners, whose permanent
// unblocks stay open on it, as an addition to what the client already
// blocks: nothing is removed and use_global_blocked_services is cleared. A
// service another active grant still covers on that client (of any owner,
// on the grant's stored clients) is left out of that client's addition, so
// sharing a device never re-blocks what another owner's grant unblocked
// there (locked: shared_device_rule).
// A client whose list already holds everything is not written. A client
// AdGuard does not know is skipped. No added clients means no AdGuard
// request at all. A failed read of AdGuard is *ErrClientsRead; a failed store
// read is returned as it is. The first failed write is *ErrAdoptFailed naming
// the client.
func (e *Engine) AdoptClients(ctx context.Context, childID int64, siblings, added []string) error {
	if len(added) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	actx, cancel := e.applyContext(ctx)
	defer cancel()
	res, err := e.ag.Clients(actx)
	if err != nil {
		return &ErrClientsRead{Err: err}
	}
	jb, err := e.baselineFrom(ctx, res, childID, siblings)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, pc := range res.Persistent {
		known[pc.Name] = true
	}
	for _, name := range added {
		if !known[name] {
			continue
		}
		err := e.ag.AdoptBlockedServices(actx, name, without(jb.forClient(name), heldOpen(jb.active, 0, name)))
		switch {
		case err == nil, errors.Is(err, adguard.ErrClientNotFound):
		default:
			return &ErrAdoptFailed{Client: name, Err: err}
		}
	}
	return nil
}

// without returns the members of base not in held (sorted), never nil.
func without(base, held []string) []string {
	out := []string{}
	for _, s := range base {
		if _, ok := slices.BinarySearch(held, s); !ok {
			out = append(out, s)
		}
	}
	return out
}

// baseline reads AdGuard through actx and computes the baseline of the new
// client name joining childID. Caller holds mu.
func (e *Engine) baseline(ctx, actx context.Context, childID int64, siblings []string, name string) ([]string, error) {
	res, err := e.ag.Clients(actx)
	if err != nil {
		return nil, err
	}
	jb, err := e.baselineFrom(ctx, res, childID, siblings)
	if err != nil {
		return nil, err
	}
	return jb.forClient(name), nil
}

// joinBaseline is what baselineFrom read for devices joining one child
// (locked new_client_blocked_set_v2). common is the part every joining
// client shares; forClient adds the terms that depend on who owns it.
type joinBaseline struct {
	childID int64
	common  []string           // siblings' (or global) list ∪ grant clause ∪ the child's permanent blocks
	known   []string           // the siblings AdGuard knows
	owners  map[string][]int64 // client -> the children whose mapping lists it now
	open    map[int64][]string // child -> its permanently unblocked built-in services
	active  []store.Grant
}

// baselineFrom reads what a device joining childID must block, from the
// siblings — the child's clients AdGuard knows — and the store:
//
//   - the union of the siblings' effective blocked sets (the global list for
//     one that inherits it), or AdGuard's global list when there are none;
//   - every service under the child's active grants and under any active
//     grant (of any child) stored on a known sibling — those grants
//     unblocked the services on the siblings, and their revert will never
//     touch the new device;
//   - every service a sibling's owner has permanently unblocked, when that
//     owner does not own the new device (forClient) — the sibling is open
//     for that other child's reason, as with a sibling's grant;
//   - the child's permanent blocks;
//
// minus, last, every service permanently unblocked by any owner of the new
// device — the child or a co-owner of a shared one (forClient) — so a live
// grant cannot re-add it (open-wins, locked shared_client_scope). Only
// built-in ids: custom ones are enforced by the managed region. A failed
// read of the active grants, children or toggles is returned: a baseline
// computed without them could block a permanently unblocked service or
// miss a permanent block. Caller holds mu.
func (e *Engine) baselineFrom(ctx context.Context, res adguard.ClientsResult, childID int64, siblings []string) (joinBaseline, error) {
	byName := make(map[string]adguard.PersistentClient, len(res.Persistent))
	for _, pc := range res.Persistent {
		byName[pc.Name] = pc
	}
	jb := joinBaseline{childID: childID, open: map[int64][]string{}}
	for _, s := range siblings {
		pc, ok := byName[s]
		if !ok {
			continue
		}
		jb.known = append(jb.known, s)
		if pc.UseGlobalBlockedServices {
			jb.common = append(jb.common, res.GlobalBlockedServices...)
		} else {
			jb.common = append(jb.common, pc.BlockedServices...)
		}
	}
	if len(jb.known) == 0 {
		jb.common = append(jb.common, res.GlobalBlockedServices...)
	}
	var err error
	if jb.active, err = e.store.ListActiveGrants(ctx); err != nil {
		return joinBaseline{}, err
	}
	children, toggles, err := e.readToggles(ctx)
	if err != nil {
		return joinBaseline{}, err
	}
	for _, g := range jb.active {
		if g.ChildID == childID || slices.ContainsFunc(jb.known, func(s string) bool { return slices.Contains(g.Clients, s) }) {
			jb.common = append(jb.common, builtin(g.Services)...)
		}
	}
	for _, t := range toggles {
		if len(builtin([]string{t.serviceID})) == 0 {
			continue
		}
		switch {
		case t.state == store.ToggleUnblocked:
			jb.open[t.childID] = append(jb.open[t.childID], t.serviceID)
		case t.childID == childID:
			jb.common = append(jb.common, t.serviceID)
		}
	}
	jb.owners = owners(children)
	return jb, nil
}

// forClient returns the baseline of name: common, plus the permanent
// unblocks of each known sibling's owners who do not own name, minus the
// permanent unblocks of name's owners (the child and the children already
// mapping it). Sorted, never nil.
func (jb joinBaseline) forClient(name string) []string {
	own := append([]int64{jb.childID}, jb.owners[name]...)
	out := slices.Clone(jb.common)
	for _, s := range jb.known {
		for _, o := range jb.owners[s] {
			if !slices.Contains(own, o) {
				out = append(out, jb.open[o]...)
			}
		}
	}
	out = slices.DeleteFunc(out, func(svc string) bool {
		return slices.ContainsFunc(own, func(o int64) bool { return slices.Contains(jb.open[o], svc) })
	})
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// resyncRules converges the managed region after a committed client write,
// logging rather than returning a failure: the store already holds the
// intent and the reconciler converges it. Caller holds mu.
func (e *Engine) resyncRules(ctx, actx context.Context, op, client string) {
	if _, err := e.syncRules(ctx, actx, 0); err != nil {
		e.log.Warn(op+": managed rules not updated; the reconciler will retry", "client", client, "err", err)
	}
}
