package grants

import (
	"slices"

	"github.com/Rivil/adguard-reward/internal/store"
)

// Permanent toggles (locked drift_enforcement, shared_client_scope,
// revert_baseline_v2). A toggle is a parent's per-(child, service) intent.
// Its effect on a client is decided by the client's CURRENT owners — the
// children whose mapping lists it now — unlike grant coverage, which is keyed
// on a grant's stored clients. These helpers are pure: callers read children,
// toggles and active grants, and apply the result.

// toggle is one stored intent, decoupled from the store row type so the
// planners stay pure.
type toggle struct {
	childID   int64
	serviceID string
	state     string // store.ToggleBlocked or store.ToggleUnblocked
}

// clientPlan is the blocked_services change one client needs: remove are
// services to open, add are services to block. Both are sorted.
type clientPlan struct {
	remove []string
	add    []string
}

// owners maps each client to the ids of the children whose mapping lists it.
func owners(children []store.Child) map[string][]int64 {
	out := map[string][]int64{}
	for _, c := range children {
		for _, client := range c.Clients {
			if !slices.Contains(out[client], c.ID) {
				out[client] = append(out[client], c.ID)
			}
		}
	}
	return out
}

// byChild indexes toggles as child id -> service id -> state.
func byChild(toggles []toggle) map[int64]map[string]string {
	out := map[int64]map[string]string{}
	for _, t := range toggles {
		if out[t.childID] == nil {
			out[t.childID] = map[string]string{}
		}
		out[t.childID][t.serviceID] = t.state
	}
	return out
}

// permanentOpen returns, per client, the services any current owner of it
// has permanently unblocked — sorted and deduplicated. A client with none is
// absent. Built-in and custom ids alike; callers pick the namespace they
// enforce.
func permanentOpen(children []store.Child, toggles []toggle) map[string][]string {
	intent := byChild(toggles)
	out := map[string][]string{}
	for client, ids := range owners(children) {
		var open []string
		for _, id := range ids {
			for svc, state := range intent[id] {
				if state == store.ToggleUnblocked {
					open = append(open, svc)
				}
			}
		}
		if len(open) > 0 {
			slices.Sort(open)
			out[client] = slices.Compact(open)
		}
	}
	return out
}

// togglePlan returns, per client owned by any child, the blocked_services
// change that converges toggled built-in services to intent. For each
// built-in service an owner of the client has toggled:
//
//   - OPEN (remove) if any owner has it unblocked or any active grant covers
//     it on the client (open-wins, as for grants on shared devices);
//   - else BLOCK (add) if any owner has it blocked.
//
// Services no owner toggled are AdGuard-owned and never appear. A client
// with nothing to change is absent. The plan states the desired direction;
// the caller diffs it against AdGuard's current list.
func togglePlan(children []store.Child, toggles []toggle, active []store.Grant) map[string]clientPlan {
	intent := byChild(toggles)
	out := map[string]clientPlan{}
	for client, ids := range owners(children) {
		unblocked, blocked := map[string]bool{}, map[string]bool{}
		for _, id := range ids {
			for svc, state := range intent[id] {
				if len(builtin([]string{svc})) == 0 {
					continue
				}
				if state == store.ToggleUnblocked {
					unblocked[svc] = true
				} else {
					blocked[svc] = true
				}
			}
		}
		if len(unblocked) == 0 && len(blocked) == 0 {
			continue
		}
		held := heldOpen(active, 0, client)
		var p clientPlan
		for svc := range unblocked {
			p.remove = append(p.remove, svc)
		}
		for svc := range blocked {
			if unblocked[svc] {
				continue
			}
			if _, ok := slices.BinarySearch(held, svc); ok {
				p.remove = append(p.remove, svc)
				continue
			}
			p.add = append(p.add, svc)
		}
		slices.Sort(p.remove)
		slices.Sort(p.add)
		out[client] = p
	}
	return out
}
