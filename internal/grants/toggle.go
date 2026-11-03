package grants

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/Rivil/adguard-reward/internal/store"
)

// ToggleGrant reports the active grant a permanent block trimmed: the
// service left its coverage, and Ended says the grant had nothing left and
// was ended (locked block_during_grant).
type ToggleGrant struct {
	ID    int64
	Ended bool
}

// ToggleResult is what Toggle returns. Failed names the clients whose
// AdGuard write did not land (sorted, never nil); the intent is stored
// either way and the reconciler converges them (locked toggle_partial_apply).
// Clients names the clients on which the requested state now holds because
// of this toggle — failed ones included, a client held open by another
// owner's permanent unblock or a live grant excluded (sorted, never nil).
// Grant is the trimmed grant, or nil.
type ToggleResult struct {
	Applied bool
	Failed  []string
	Clients []string
	Grant   *ToggleGrant
}

// Toggle stores the child's permanent intent for serviceID and applies it
// within one ApplyTimeout. A custom id with no stored service is
// *ErrUnknownService and nothing is stored (checked under the mutex, as in
// Create); an unknown child is store.ErrChildNotFound; a bad state is
// store.ErrInvalidToggleState.
//
// A block also removes the service from the child's active grant covering
// it, ending a grant left with none (its timer stops). Built-in services are
// written through applyPlan, so a client on the global list fails either
// way (accepted limitation: it converges once migrated off the global list):
//
//   - unblock: the service is removed on every mapped client;
//   - block: it is added on every mapped client not held open by another
//     owner's permanent unblock or a live grant, and re-added on the trimmed
//     grant's stored clients, best-effort for those no longer mapped (a
//     failure there is reported but never retried).
//
// A failed read of the children, toggles or active grants fails every
// target client without a write. Custom ids converge the managed user_rules
// region instead, inside the same ApplyTimeout (see toggleCustom).
func (e *Engine) Toggle(ctx context.Context, childID int64, serviceID, state string) (ToggleResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	custom := strings.HasPrefix(serviceID, customPrefix)
	if custom {
		n, ok := store.ParseCustomID(serviceID)
		if !ok {
			return ToggleResult{}, &ErrUnknownService{ID: serviceID}
		}
		if _, err := e.store.GetCustomService(ctx, n); err != nil {
			if errors.Is(err, store.ErrCustomServiceNotFound) {
				return ToggleResult{}, &ErrUnknownService{ID: serviceID}
			}
			return ToggleResult{}, err
		}
	}
	child, err := e.store.GetChild(ctx, childID)
	if err != nil {
		return ToggleResult{}, err
	}
	change, err := e.store.SetServiceToggle(ctx, childID, serviceID, state, e.now())
	if err != nil {
		return ToggleResult{}, err
	}

	res := ToggleResult{Failed: []string{}, Clients: []string{}}
	if change.Trimmed != nil {
		res.Grant = &ToggleGrant{ID: change.Trimmed.ID, Ended: change.Ended}
		if change.Ended {
			e.stopTimer(change.Trimmed.ID)
			e.log.Info("grant ended by permanent block", "grant", change.Trimmed.ID, "child", childID, "service", serviceID)
		}
	}
	actx, cancel := e.applyContext(ctx)
	defer cancel()
	if custom {
		return e.toggleCustom(ctx, actx, child, serviceID, state, res), nil
	}
	plan, governed, err := e.togglePlanFor(ctx, child, serviceID, state, change.Trimmed)
	if err != nil {
		targets := slices.Clone(child.Clients)
		if change.Trimmed != nil {
			targets = append(targets, change.Trimmed.Clients...)
		}
		e.log.Warn("toggle apply: read failed, nothing written", "child", childID, "service", serviceID, "err", err)
		// Without the reads nothing can be told apart: every target failed,
		// and failed clients count as governed.
		res.Failed = sortedSet(targets)
		res.Clients = slices.Clone(res.Failed)
		return res, nil
	}
	res.Failed, _ = e.applyPlan(actx, plan)
	res.Clients = governed
	res.Applied = len(res.Failed) == 0
	e.log.Info("service toggled", "child", childID, "service", serviceID, "state", state,
		"applied", res.Applied, "failed", res.Failed)
	return res, nil
}

// togglePlanFor builds the per-client write for one built-in toggle and the
// clients it governs (see ToggleResult.Clients). Only serviceID is touched:
// other toggled services converge in the reconciler. Caller holds mu.
func (e *Engine) togglePlanFor(ctx context.Context, child store.Child, serviceID, state string, trimmed *store.Grant) (map[string]clientPlan, []string, error) {
	children, toggles, err := e.readToggles(ctx)
	if err != nil {
		return nil, nil, err
	}
	active, err := e.store.ListActiveGrants(ctx)
	if err != nil {
		return nil, nil, err
	}
	full := togglePlan(children, toggles, active)
	plan := map[string]clientPlan{}
	governed := []string{}
	for _, c := range child.Clients {
		p := full[c]
		switch {
		case slices.Contains(p.remove, serviceID):
			plan[c] = clientPlan{remove: []string{serviceID}}
			if state == store.ToggleUnblocked {
				governed = append(governed, c)
			}
		case slices.Contains(p.add, serviceID):
			plan[c] = clientPlan{add: []string{serviceID}}
			if state == store.ToggleBlocked {
				governed = append(governed, c)
			}
		}
	}
	if state == store.ToggleBlocked && trimmed != nil {
		g := *trimmed
		g.Services = []string{serviceID}
		for c := range dropOpen(revertPlan(g, active), permanentOpen(children, toggles)) {
			if _, mapped := plan[c]; mapped {
				continue
			}
			plan[c] = clientPlan{add: []string{serviceID}}
			governed = append(governed, c)
		}
	}
	return plan, sortedSet(governed), nil
}

// sortedSet returns ids sorted and deduplicated, never nil.
func sortedSet(ids []string) []string {
	out := slices.Clone(ids)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
