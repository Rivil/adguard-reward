package api

import (
	"cmp"
	"net/http"
	"slices"
)

type historyServiceOptionView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type historyServicesView struct {
	Services []historyServiceOptionView `json:"services"`
}

// handleHistoryServices is GET /api/v1/history/services: every service that
// appears in visible history, deleted custom services included (locked
// service_filter), sorted by name then id. A name resolves as on a grant: the
// newest snapshot, else the live catalogue, else the bare id. The catalogue is
// read only when a snapshot is missing, and a failed read is not an error.
func (a *API) handleHistoryServices(w http.ResponseWriter, r *http.Request) {
	options, err := a.deps.Children.ListHistoryServices(r.Context())
	if err != nil {
		a.writeStoreError(w, "list history services", "", err)
		return
	}
	var unnamed []string
	for _, o := range options {
		if o.Name == "" {
			unnamed = append(unnamed, o.ID)
		}
	}
	names := a.liveNames(r, unnamed)

	out := make([]historyServiceOptionView, 0, len(options))
	for _, o := range options {
		out = append(out, historyServiceOptionView{ID: o.ID, Name: serviceName(o.ID, o.Name, names)})
	}
	slices.SortFunc(out, func(x, y historyServiceOptionView) int {
		return cmp.Or(cmp.Compare(x.Name, y.Name), cmp.Compare(x.ID, y.ID))
	})
	writeJSON(w, http.StatusOK, historyServicesView{Services: out})
}
