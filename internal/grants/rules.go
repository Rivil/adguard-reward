package grants

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Rivil/adguard-reward/internal/rules"
	"github.com/Rivil/adguard-reward/internal/store"
)

// ServiceResult is what the custom-service operations return: the stored
// service plus whether AdGuard's managed region was brought in line with it.
// Applied=false means the store write stands and the reconciler converges
// the region later.
type ServiceResult struct {
	Service store.CustomService
	Applied bool
}

// SyncRules converges the managed user_rules region to the desired rules
// for the stored custom services, children and active grants. It is the
// entry point for callers outside the engine (API handlers after a child
// write); it takes the engine mutex.
func (e *Engine) SyncRules(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	_, err := e.syncRules(ctx, actx, 0)
	return err
}

// syncRules rewrites the managed region from intent: every stored custom
// service blocked on every mapped child client, with an exception per
// domain over the clients an active grant (other than exclude; 0 excludes
// none) covers that service on. Store reads go through ctx and come first:
// any read error aborts before AdGuard is called, since a region rendered
// from a partial read could unblock every child. The AdGuard
// read-modify-write goes through actx and writes only when the region
// changes; a malformed region is refused (rules.ErrMalformedRegion) with no
// write. changed reports a write. Caller holds mu.
func (e *Engine) syncRules(ctx, actx context.Context, exclude int64) (changed bool, err error) {
	svcs, err := e.store.ListCustomServices(ctx)
	if err != nil {
		return false, fmt.Errorf("sync rules: read custom services: %w", err)
	}
	children, err := e.store.ListChildren(ctx)
	if err != nil {
		return false, fmt.Errorf("sync rules: read children: %w", err)
	}
	active, err := e.store.ListActiveGrants(ctx)
	if err != nil {
		return false, fmt.Errorf("sync rules: read active grants: %w", err)
	}
	desired := desiredRegion(svcs, children, active, exclude, e.log)
	changed, err = e.ag.EditUserRules(actx, func(current []string) ([]string, error) {
		return rules.Splice(current, desired)
	})
	if err != nil {
		return false, fmt.Errorf("sync rules: %w", err)
	}
	return changed, nil
}

// desiredRegion renders the region body from intent. Child clients are the
// union over every child; adults are never mapped, so never blocked (locked
// block_scope). Only custom ids in a grant become covers: built-in ids are
// the blocked_services path's business.
func desiredRegion(svcs []store.CustomService, children []store.Child, active []store.Grant, exclude int64, log *slog.Logger) []string {
	in := make([]rules.Service, 0, len(svcs))
	for _, s := range svcs {
		in = append(in, rules.Service{ID: s.ServiceID(), Domains: s.Domains})
	}
	var kids []string
	for _, c := range children {
		kids = append(kids, c.Clients...)
	}
	var covers []rules.Cover
	for _, g := range active {
		if g.ID == exclude {
			continue
		}
		for _, id := range g.Services {
			if store.IsCustomServiceID(id) {
				covers = append(covers, rules.Cover{ServiceID: id, Clients: g.Clients})
			}
		}
	}
	return rules.Render(in, kids, covers, log)
}

// CreateService stores a custom service and converges the region under the
// engine mutex. A store error (name or domain taken) is returned as is with
// nothing written to AdGuard. A failed region write is not an error: the
// service stays stored, Applied is false and the reconciler catches up.
func (e *Engine) CreateService(ctx context.Context, name string, domains []string) (ServiceResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs, err := e.store.CreateCustomService(ctx, name, domains)
	if err != nil {
		return ServiceResult{}, err
	}
	return ServiceResult{Service: cs, Applied: e.syncAfter(ctx, "create", cs.ID)}, nil
}

// UpdateService replaces a custom service's name and domains, then
// converges the region; failure semantics as CreateService. An unknown id is
// store.ErrCustomServiceNotFound.
func (e *Engine) UpdateService(ctx context.Context, id int64, name string, domains []string) (ServiceResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs, err := e.store.UpdateCustomService(ctx, id, name, domains)
	if err != nil {
		return ServiceResult{}, err
	}
	return ServiceResult{Service: cs, Applied: e.syncAfter(ctx, "update", cs.ID)}, nil
}

// DeleteService deletes a custom service (the store strips it from buttons)
// and converges the region so none of its domains keeps a rule. While an
// active grant covers it the store's *store.ErrServiceInUse comes back and
// AdGuard is not touched. The result carries the service as it was.
func (e *Engine) DeleteService(ctx context.Context, id int64) (ServiceResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cs, err := e.store.GetCustomService(ctx, id)
	if err != nil {
		return ServiceResult{}, err
	}
	if err := e.store.DeleteCustomService(ctx, id); err != nil {
		return ServiceResult{}, err
	}
	return ServiceResult{Service: cs, Applied: e.syncAfter(ctx, "delete", id)}, nil
}

// syncAfter converges the region after a committed custom-service write,
// logging rather than returning a failure. Caller holds mu.
func (e *Engine) syncAfter(ctx context.Context, op string, id int64) bool {
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	if _, err := e.syncRules(ctx, actx, 0); err != nil {
		e.log.Warn("custom service "+op+": stored, but AdGuard rules not updated; the reconciler will retry",
			"service", store.CustomServiceID(id), "err", err)
		return false
	}
	return true
}
