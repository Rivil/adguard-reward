package grants

import (
	"context"
	"slices"

	"github.com/Rivil/adguard-reward/internal/store"
)

// toggleCustom applies a stored custom-service toggle by converging the
// managed user_rules region through actx, so the child's clients gain (an
// unblock) or lose (a block) the @@ exception within the call. res carries
// the grant trim already done. Clients follows Toggle's governed rule: an
// unblock lists every mapped client; a block leaves out a client still held
// open by another current owner's permanent unblock of the service or by a
// live grant covering it there. A failed read, a set_rules failure, a
// timeout or a malformed region leaves the intent stored and fails every
// mapped client (Applied false); the reconciler re-converges the region.
// Caller holds mu.
func (e *Engine) toggleCustom(ctx, actx context.Context, child store.Child, serviceID, state string, res ToggleResult) ToggleResult {
	fail := func(err error) ToggleResult {
		e.log.Warn("toggle apply: managed rules not updated", "child", child.ID, "service", serviceID, "err", err)
		res.Applied = false
		res.Failed = sortedSet(child.Clients)
		return res
	}
	governed, err := e.customGoverned(ctx, child, serviceID, state)
	res.Clients = governed
	if err != nil {
		res.Clients = sortedSet(child.Clients)
		return fail(err)
	}
	if _, err := e.syncRules(ctx, actx, 0); err != nil {
		return fail(err)
	}
	res.Applied = true
	e.log.Info("service toggled", "child", child.ID, "service", serviceID, "state", state, "applied", true)
	return res
}

// customGoverned lists the child's mapped clients on which the requested
// state holds because of this toggle (see toggleCustom). Caller holds mu.
func (e *Engine) customGoverned(ctx context.Context, child store.Child, serviceID, state string) ([]string, error) {
	if state == store.ToggleUnblocked {
		return sortedSet(child.Clients), nil
	}
	children, toggles, err := e.readToggles(ctx)
	if err != nil {
		return nil, err
	}
	active, err := e.store.ListActiveGrants(ctx)
	if err != nil {
		return nil, err
	}
	open := permanentOpen(children, toggles)
	out := []string{}
	for _, c := range child.Clients {
		if slices.Contains(open[c], serviceID) {
			continue
		}
		if _, held := slices.BinarySearch(heldOpen(active, 0, c), serviceID); held {
			continue
		}
		out = append(out, c)
	}
	return sortedSet(out), nil
}
