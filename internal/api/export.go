package api

import (
	"net/http"
	"time"

	"github.com/Rivil/adguard-reward/internal/store"
)

// setupFileOf maps a store snapshot to the file. Every list is built with make
// so an empty install writes [] rather than null, which parseSetupFile refuses.
func setupFileOf(s store.Setup) setupFile {
	f := setupFile{
		Format:         setupFileFormat,
		Version:        setupFileVersion,
		Children:       make([]fileChild, 0, len(s.Children)),
		CustomServices: make([]fileService, 0, len(s.CustomServices)),
		Buttons:        make([]fileButton, 0, len(s.Buttons)),
		Toggles:        make([]fileToggle, 0, len(s.Toggles)),
	}
	for _, c := range s.Children {
		f.Children = append(f.Children, fileChild{Name: c.Name, Clients: c.Clients})
	}
	for _, svc := range s.CustomServices {
		f.CustomServices = append(f.CustomServices, fileService{Name: svc.Name, Domains: svc.Domains})
	}
	for _, b := range s.Buttons {
		f.Buttons = append(f.Buttons, fileButton{
			Label:    b.Label,
			Child:    b.Child,
			Services: b.Services,
			Icon:     b.Icon,
			Duration: int64(b.Duration / time.Second),
		})
	}
	for _, t := range s.Toggles {
		f.Toggles = append(f.Toggles, fileToggle{Child: t.Child, Service: t.Service, State: t.State})
	}
	return f
}

// handleExport is GET /api/v1/export: the install's setup as one file. It reads
// the store and never AdGuard, so a backup works while AdGuard is down. There
// is no Content-Disposition: the SPA saves the body through fetch and a Blob.
func (a *API) handleExport(w http.ResponseWriter, r *http.Request) {
	s, err := a.deps.Children.ExportSetup(r.Context())
	if err != nil {
		a.log.Error("export setup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not export setup")
		return
	}
	writeJSON(w, http.StatusOK, setupFileOf(s))
}
