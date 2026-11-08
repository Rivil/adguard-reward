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
// each other child's active grant or permanent unblock holding it open on a
// mapped client, never null. toggle is the child's own permanent intent
// ("blocked" / "unblocked"), null when never toggled.
type blockedServiceView struct {
	serviceView
	Custom      bool              `json:"custom"`
	State       string            `json:"state"`
	Toggle      *string           `json:"toggle"`
	Differs     []string          `json:"differs"`
	UnblockedBy []blocked.Unblock `json:"unblocked_by"`
}

// handleBlocked is GET /api/v1/children/{id}/blocked: the child's mapped
// clients read live from AdGuard and folded over the live catalogue. An
// unknown child is 404 before any AdGuard call. The shared annotations come
// from the active grants and the current mapping; a failed read degrades
// them to [] with a warn, never an error status. Custom services follow the
// catalogue with a state derived from intent (stored services + active
// grants); when either read fails they are left out with a warn — never
// reported as blocked on a guess, never a 500. Permanent toggles feed each
// service's toggle and the permanent unblocked_by entries; when they cannot
// be read every toggle is null and custom services are left out (their
// state depends on toggles), with a warn.
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
	owners, covers, coversOK := a.sharedInputs(r)
	toggles, togglesOK := a.toggleInputs(r)
	var custom []blocked.CustomService
	if togglesOK {
		custom = a.customInputs(r, coversOK)
	}
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
		Custom:   custom,
		Mapped:   child.Clients,
		Self:     child.ID,
		Owners:   owners,
		Grants:   covers,
		Toggles:  toggles,
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
			Custom:      s.Custom,
			State:       s.State,
			Toggle:      nullIfEmpty(s.Toggle),
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
// still covers, named {child_id, ""}. ok reports whether covers is the
// complete set of active grants.
func (a *API) sharedInputs(r *http.Request) (owners map[string][]blocked.ChildRef, covers []blocked.GrantCover, ok bool) {
	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.log.Warn("blocked view: children unavailable, shared annotations left empty", "err", err)
		return nil, nil, false
	}
	owners = map[string][]blocked.ChildRef{}
	for name, refs := range ownerIndex(children) {
		for _, ref := range refs {
			owners[name] = append(owners[name], blocked.ChildRef{ID: ref.ID, Name: ref.Name})
		}
	}
	active, err := a.deps.Grants.List(r.Context())
	if err != nil {
		a.log.Warn("blocked view: grants unavailable, unblocked_by left empty", "err", err)
		return owners, nil, false
	}
	names := make(map[int64]string, len(children))
	for _, c := range children {
		names[c.ID] = c.Name
	}
	covers = make([]blocked.GrantCover, 0, len(active))
	for _, g := range active {
		covers = append(covers, blocked.GrantCover{
			ID:       g.ID,
			Child:    blocked.ChildRef{ID: g.ChildID, Name: names[g.ChildID]},
			Services: g.Services,
			Clients:  g.Clients,
		})
	}
	return owners, covers, true
}

// toggleInputs reads every child's permanent toggles for the fold. ok is
// false when the read failed; the view then carries no toggle state.
func (a *API) toggleInputs(r *http.Request) (out []blocked.Toggle, ok bool) {
	list, err := a.deps.Children.ListServiceToggles(r.Context())
	if err != nil {
		a.log.Warn("blocked view: toggles unavailable, toggle state and custom services left out", "err", err)
		return nil, false
	}
	out = make([]blocked.Toggle, 0, len(list))
	for _, t := range list {
		out = append(out, blocked.Toggle{ChildID: t.ChildID, ServiceID: t.ServiceID, State: t.State})
	}
	return out, true
}

// nullIfEmpty marshals "" as null.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// customInputs reads the stored custom services for the fold. Their state
// needs the complete active grants (coversOK); without them, or when the
// read fails, it returns none and the view carries built-ins only.
func (a *API) customInputs(r *http.Request, coversOK bool) []blocked.CustomService {
	if a.deps.CustomServices == nil {
		return nil
	}
	if !coversOK {
		a.log.Warn("blocked view: grants unavailable, custom services left out")
		return nil
	}
	list, err := a.deps.CustomServices.ListCustomServices(r.Context())
	if err != nil {
		a.log.Warn("blocked view: custom services unavailable, left out", "err", err)
		return nil
	}
	out := make([]blocked.CustomService, 0, len(list))
	for _, c := range list {
		out = append(out, blocked.CustomService{ID: c.ServiceID(), Name: c.Name})
	}
	return out
}

// nonNil returns s, or an empty slice for nil, so it marshals as [].
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
