package api

import (
	"net/http"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/store"
)

const adguardDownMsg = "AdGuard Home is unreachable — try again shortly"

// childRef is the {id, name} pair views use to point at a child.
type childRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// clientView is one row of GET /api/v1/clients: a live AdGuard persistent
// client plus the child it is assigned to, or null.
type clientView struct {
	Name                     string    `json:"name"`
	IDs                      []string  `json:"ids"`
	UseGlobalBlockedServices bool      `json:"use_global_blocked_services"`
	Child                    *childRef `json:"child"`
}

type serviceView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// writeAdGuardError answers 502 for any AdGuard failure on a read. That
// includes ErrBadCredentials: it is the service credential that was
// rejected, not the parent's session, so 401 would be the wrong signal.
func (a *API) writeAdGuardError(w http.ResponseWriter, op string, err error) {
	a.log.Warn(op+": adguard unavailable", "err", err)
	writeError(w, http.StatusBadGateway, CodeAdGuardUnavailable, adguardDownMsg)
}

// ownerIndex maps client name → owning child.
func ownerIndex(children []store.Child) map[string]childRef {
	owner := map[string]childRef{}
	for _, c := range children {
		for _, name := range c.Clients {
			owner[name] = childRef{ID: c.ID, Name: c.Name}
		}
	}
	return owner
}

// handleClients is GET /api/v1/clients. Every call reads AdGuard; nothing
// is memoised.
func (a *API) handleClients(w http.ResponseWriter, r *http.Request) {
	res, err := a.deps.AdGuard.Clients(r.Context())
	if err != nil {
		a.writeAdGuardError(w, "list clients", err)
		return
	}
	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.writeStoreError(w, "list children", "", err)
		return
	}
	owner := ownerIndex(children)
	out := make([]clientView, 0, len(res.Persistent))
	for _, pc := range res.Persistent {
		v := clientView{Name: pc.Name, IDs: pc.IDs, UseGlobalBlockedServices: pc.UseGlobalBlockedServices}
		if v.IDs == nil {
			v.IDs = []string{}
		}
		if ref, ok := owner[pc.Name]; ok {
			v.Child = &ref
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, struct {
		Clients []clientView `json:"clients"`
	}{out})
}

// handleServices is GET /api/v1/services: the catalogue as AdGuard serves
// it right now, icon echoed verbatim.
func (a *API) handleServices(w http.ResponseWriter, r *http.Request) {
	services, err := a.deps.AdGuard.Services(r.Context())
	if err != nil {
		a.writeAdGuardError(w, "list services", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Services []serviceView `json:"services"`
	}{serviceViews(services)})
}

func serviceViews(services []adguard.Service) []serviceView {
	out := make([]serviceView, 0, len(services))
	for _, s := range services {
		out = append(out, serviceView{ID: s.ID, Name: s.Name, Icon: s.Icon})
	}
	return out
}
