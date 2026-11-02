package grants

import (
	"slices"

	"github.com/Rivil/adguard-reward/internal/store"
)

// Ref-counted unlock on shared clients (locked: shared_device_rule). A
// service a grant opened on a client is re-blocked there only when no other
// active grant still covers that service on that client. Coverage is keyed
// on each grant's STORED clients (locked: grant_coverage_basis), never on
// which children own the client now — so these helpers take no ownership
// input at all.

// revertPlan returns, per client in g.Clients, the services of g that may be
// re-blocked there: g.Services minus those another active grant (id != g.ID)
// covers on that client. A client whose every service is still held open is
// omitted, so the caller issues no write for it. Services keep g's order.
func revertPlan(g store.Grant, active []store.Grant) map[string][]string {
	plan := make(map[string][]string, len(g.Clients))
	for _, client := range g.Clients {
		held := heldOpen(active, g.ID, client)
		var svcs []string
		for _, s := range g.Services {
			if _, ok := slices.BinarySearch(held, s); !ok && !slices.Contains(svcs, s) {
				svcs = append(svcs, s)
			}
		}
		if len(svcs) > 0 {
			plan[client] = svcs
		}
	}
	return plan
}

// heldOpen returns the services that any active grant other than exclude
// covers on client — sorted, deduplicated, and [] (never nil) when none.
// Pass exclude 0 to count every grant (grant ids start at 1).
func heldOpen(active []store.Grant, exclude int64, client string) []string {
	held := []string{}
	for _, a := range active {
		if a.ID == exclude || !slices.Contains(a.Clients, client) {
			continue
		}
		held = append(held, a.Services...)
	}
	slices.Sort(held)
	return slices.Compact(held)
}
