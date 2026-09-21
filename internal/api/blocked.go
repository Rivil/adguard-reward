package api

import (
	"net/http"

	"github.com/Rivil/adguard-reward/internal/blocked"
)

type blockedClientView struct {
	Name       string `json:"name"`
	Missing    bool   `json:"missing"`
	UsesGlobal bool   `json:"uses_global"`
}

type blockedServiceView struct {
	serviceView
	State   string   `json:"state"`
	Differs []string `json:"differs"`
}

// handleBlocked is GET /api/v1/children/{id}/blocked: the child's mapped
// clients read live from AdGuard and folded over the live catalogue. An
// unknown child is 404 before any AdGuard call.
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
	})

	clients := make([]blockedClientView, 0, len(view.Clients))
	for _, c := range view.Clients {
		clients = append(clients, blockedClientView{Name: c.Name, Missing: c.Missing, UsesGlobal: c.UsesGlobal})
	}
	svcs := make([]blockedServiceView, 0, len(view.Services))
	for _, s := range view.Services {
		svcs = append(svcs, blockedServiceView{
			serviceView: serviceView{ID: s.ID, Name: s.Name, Icon: s.Icon},
			State:       s.State,
			Differs:     s.Differs,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Child    childRef             `json:"child"`
		Clients  []blockedClientView  `json:"clients"`
		Services []blockedServiceView `json:"services"`
	}{childRef{ID: child.ID, Name: child.Name}, clients, svcs})
}
