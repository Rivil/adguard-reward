package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Rivil/adguard-reward/internal/store"
)

// maxImportBody bounds an import. A real file is a few kilobytes; this leaves
// room for a large household without letting a stray upload fill memory.
const maxImportBody = 8 << 20

// importResult is the 200 body of an import. MissingClients is never null.
type importResult struct {
	MissingClients []string `json:"missing_clients"`
	RulesApplied   bool     `json:"rules_applied"`
}

// setupOf maps a validated file to the store's snapshot. Duration is in bounds
// by now, so the conversion cannot wrap.
func setupOf(f setupFile) store.Setup {
	s := store.Setup{
		Children:       make([]store.SetupChild, 0, len(f.Children)),
		CustomServices: make([]store.SetupService, 0, len(f.CustomServices)),
		Buttons:        make([]store.SetupButton, 0, len(f.Buttons)),
		Toggles:        make([]store.SetupToggle, 0, len(f.Toggles)),
	}
	for _, c := range f.Children {
		s.Children = append(s.Children, store.SetupChild{Name: c.Name, Clients: c.Clients})
	}
	for _, svc := range f.CustomServices {
		s.CustomServices = append(s.CustomServices, store.SetupService{Name: svc.Name, Domains: svc.Domains})
	}
	for _, b := range f.Buttons {
		s.Buttons = append(s.Buttons, store.SetupButton{
			Label:    b.Label,
			Child:    b.Child,
			Services: b.Services,
			Icon:     b.Icon,
			Duration: time.Duration(b.Duration) * time.Second,
		})
	}
	for _, t := range f.Toggles {
		s.Toggles = append(s.Toggles, store.SetupToggle{Child: t.Child, Service: t.Service, State: t.State})
	}
	return s
}

// handleImport is POST /api/v1/import: restore a whole export file into an
// empty install. The body is the raw file. Checks run cheapest first and each
// refuses the file whole: read and parse (400, 422), emptiness (409), then one
// read of AdGuard's catalogue (502 when it fails, 422 for an unknown built-in
// or a name clash). Nothing is written until all of them pass.
func (a *API) handleImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportBody))
	if err != nil {
		writeImportReadError(w, err)
		return
	}
	file, problem := parseSetupFile(body)
	if problem != nil {
		writeError(w, problem.Status, problem.Code, problem.Message)
		return
	}
	if err := a.deps.Children.EnsureEmpty(r.Context()); err != nil {
		a.writeImportStoreError(w, err)
		return
	}
	if !a.checkImportCatalogue(w, r, file) {
		return
	}
	if err := a.deps.Children.ImportSetup(r.Context(), setupOf(file)); err != nil {
		a.writeImportStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, importResult{MissingClients: []string{}})
}

func writeImportReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			fmt.Sprintf("file is larger than the %d MiB limit", maxImportBody>>20))
		return
	}
	writeError(w, http.StatusBadRequest, CodeBadRequest, "could not read the file")
}

// writeImportStoreError answers both emptiness checks: the one before the
// catalogue read and the one inside the store's transaction, which a racing
// import or parent can trip after the first has passed.
func (a *API) writeImportStoreError(w http.ResponseWriter, err error) {
	var notEmpty *store.ErrNotEmpty
	if errors.As(err, &notEmpty) {
		writeError(w, http.StatusConflict, CodeConflict,
			notEmpty.Error()+"; import only runs on an empty install")
		return
	}
	a.log.Error("import setup failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "could not import setup")
}

// checkImportCatalogue reads AdGuard's built-ins once and refuses a file that
// uses an id this AdGuard lacks (locked unknown_builtin_service) or a custom
// service named like a built-in, in any case. A false return means the error
// was written.
func (a *API) checkImportCatalogue(w http.ResponseWriter, r *http.Request, f setupFile) bool {
	services, err := a.deps.AdGuard.Services(r.Context())
	if err != nil {
		a.writeAdGuardError(w, "import setup", err)
		return false
	}
	known := make(map[string]bool, len(services))
	for _, s := range services {
		known[s.ID] = true
	}
	for _, id := range builtinRefs(f) {
		if !known[id] {
			writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
				fmt.Sprintf("service %q is not in this AdGuard's catalogue", id))
			return false
		}
	}
	for i, c := range f.CustomServices {
		for _, s := range services {
			if strings.EqualFold(s.Name, c.Name) {
				writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
					fmt.Sprintf("custom_services[%d].name: %s is already a built-in AdGuard service", i, s.Name))
				return false
			}
		}
	}
	return true
}
