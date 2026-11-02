package api

import (
	"net/http"

	"github.com/Rivil/adguard-reward/internal/blocked"
)

// blockedClientView is one mapped client; shared_with lists its other
// owners, id ascending, never null.
type blockedClientView struct {
	Name       string             `json:"name"`
	Missing    bool               `json:"missing"`
	UsesGlobal bool               `json:"uses_global"`
	SharedWith []blocked.ChildRef `json:"shared_with"`
}

// blockedServiceView is one catalogue service's fold; unblocked_by names
// each other child's active grant holding it open on a mapped client,
// never null.
type blockedServiceView struct {
	serviceView
	State       string            `json:"state"`
	Differs     []string          `json:"differs"`
	UnblockedBy []blocked.Unblock `json:"unblocked_by"`
}

// handleBlocked is GET /api/v1/children/{id}/blocked: the child's mapped
// clients read live from AdGuard and folded over the live catalogue. An
// unknown child is 404 before any AdGuard call. The shared annotations come
// from the active grants and the current mapping; a failed read degrades
// them to [] with a warn, never an error status.
func (a *API) handleBlocked(w http.ResponseWriter, r *http.Request) {
	id, ok := childID(w, r)
	if !ok {
		return
	}
	child, err := a.deps.Children.GetChild(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, "get child", "", err)
		return
	}
	owners, covers := a.sharedInputs(r)
	res, err := a.deps.AdGuard.Clients(r.Context())
	if err != nil {
		a.writeAdGuardError(w, "blocked view", err)
		return
	}
	services, err := a.deps.AdGuard.Services(r.Context())
	if err != nil {
		a.writeAdGuardError(w, "blocked view", err)
		return
	}
	view := blocked.Compute(blocked.Input{
		Clients:  res.Persistent,
		Global:   res.GlobalBlockedServices,
		Services: services,
		Mapped:   child.Clients,
		Self:     child.ID,
		Owners:   owners,
		Grants:   covers,
	})

	clients := make([]blockedClientView, 0, len(view.Clients))
	for _, c := range view.Clients {
		clients = append(clients, blockedClientView{
			Name: c.Name, Missing: c.Missing, UsesGlobal: c.UsesGlobal, SharedWith: nonNil(c.SharedWith),
		})
	}
	svcs := make([]blockedServiceView, 0, len(view.Services))
	for _, s := range view.Services {
		svcs = append(svcs, blockedServiceView{
			serviceView: serviceView{ID: s.ID, Name: s.Name, Icon: s.Icon},
			State:       s.State,
			Differs:     s.Differs,
			UnblockedBy: nonNil(s.UnblockedBy),
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Child    childRef             `json:"child"`
		Clients  []blockedClientView  `json:"clients"`
		Services []blockedServiceView `json:"services"`
	}{childRef{ID: child.ID, Name: child.Name}, clients, svcs})
}

// sharedInputs reads the current mapping and the active grants for the
// fold's shared annotations. A failed children read returns nil for both —
// unblocked_by names the granting child, so it needs the mapping too; a
// failed grants read costs only the grants. Either way the view carries []
// for what is missing. A grant whose child is no longer listed (deleted)
// still covers, named {child_id, ""}.
func (a *API) sharedInputs(r *http.Request) (map[string][]blocked.ChildRef, []blocked.GrantCover) {
	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.log.Warn("blocked view: children unavailable, shared annotations left empty", "err", err)
		return nil, nil
	}
	owners := map[string][]blocked.ChildRef{}
	for name, refs := range ownerIndex(children) {
		for _, ref := range refs {
			owners[name] = append(owners[name], blocked.ChildRef{ID: ref.ID, Name: ref.Name})
		}
	}
	active, err := a.deps.Grants.List(r.Context())
	if err != nil {
		a.log.Warn("blocked view: grants unavailable, unblocked_by left empty", "err", err)
		return owners, nil
	}
	names := make(map[int64]string, len(children))
	for _, c := range children {
		names[c.ID] = c.Name
	}
	covers := make([]blocked.GrantCover, 0, len(active))
	for _, g := range active {
		covers = append(covers, blocked.GrantCover{
			ID:       g.ID,
			Child:    blocked.ChildRef{ID: g.ChildID, Name: names[g.ChildID]},
			Services: g.Services,
			Clients:  g.Clients,
		})
	}
	return owners, covers
}

// nonNil returns s, or an empty slice for nil, so it marshals as [].
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
