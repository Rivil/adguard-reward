package api

import (
	"fmt"
	"net/http"

	"github.com/Rivil/adguard-reward/internal/blocked"
)

// migrationClient is one entry of the offer: a mapped client still on the
// global list, its child, and the ids it would gain.
type migrationClient struct {
	Name  string   `json:"name"`
	Child childRef `json:"child"`
	Gains []string `json:"gains"`
}

type migrationOffer struct {
	Global  []string          `json:"global"`
	Clients []migrationClient `json:"clients"`
}

// computeOffer reads the store and AdGuard fresh and folds every mapped
// client through blocked.Offer. On failure it writes the error and
// returns false.
func (a *API) computeOffer(w http.ResponseWriter, r *http.Request, op string) (migrationOffer, []blocked.Step, bool) {
	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.writeStoreError(w, "list children", "", err)
		return migrationOffer{}, nil, false
	}
	res, err := a.deps.AdGuard.Clients(r.Context())
	if err != nil {
		a.writeAdGuardError(w, op, err)
		return migrationOffer{}, nil, false
	}
	mapped := []string{}
	owner := map[string]childRef{}
	for _, c := range children {
		for _, name := range c.Clients {
			mapped = append(mapped, name)
			owner[name] = childRef{ID: c.ID, Name: c.Name}
		}
	}
	steps := blocked.Offer(res.Persistent, res.GlobalBlockedServices, mapped)
	out := migrationOffer{Global: res.GlobalBlockedServices, Clients: make([]migrationClient, 0, len(steps))}
	for _, s := range steps {
		out.Clients = append(out.Clients, migrationClient{Name: s.Client, Child: owner[s.Client], Gains: s.Gains})
	}
	return out, steps, true
}

// handleMigrationOffer is GET /api/v1/migration. It never writes.
func (a *API) handleMigrationOffer(w http.ResponseWriter, r *http.Request) {
	offer, _, ok := a.computeOffer(w, r, "migration offer")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, offer)
}

// handleMigrationApply is POST /api/v1/migration (no body): recompute the
// offer from a fresh read and write each step in order. The global list is
// never written (locked: global_list_clients). The first AdGuard failure
// is 502 naming the client; clients already written stay written and the
// next GET simply lists the rest.
func (a *API) handleMigrationApply(w http.ResponseWriter, r *http.Request) {
	_, steps, ok := a.computeOffer(w, r, "migration apply")
	if !ok {
		return
	}
	migrated := make([]string, 0, len(steps))
	for _, s := range steps {
		if err := a.deps.AdGuard.MigrateFromGlobal(r.Context(), s.Client, s.Target); err != nil {
			a.log.Warn("migration apply: adguard unavailable", "client", s.Client, "err", err)
			writeError(w, http.StatusBadGateway, CodeAdGuardUnavailable,
				fmt.Sprintf("could not migrate %s — AdGuard Home is unreachable; %d client(s) were migrated", s.Client, len(migrated)))
			return
		}
		migrated = append(migrated, s.Client)
	}
	writeJSON(w, http.StatusOK, struct {
		Migrated []string `json:"migrated"`
	}{migrated})
}
