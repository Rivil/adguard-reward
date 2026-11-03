package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Rivil/adguard-reward/internal/rules"
	"github.com/Rivil/adguard-reward/internal/store"
)

const (
	maxCustomServiceBody = 32 << 10
	maxCustomServiceName = 64
	maxCustomDomains     = 50
	noSuchCustomService  = "no such custom service"
)

// customServiceView is one custom service on the wire. Applied is false when
// the service was stored but AdGuard's managed rules could not be updated;
// the reconciler converges them. A listing reports true: per-service region
// state is not tracked, the region is converged as a whole.
type customServiceView struct {
	ID        int64    `json:"id"`
	ServiceID string   `json:"service_id"`
	Name      string   `json:"name"`
	Domains   []string `json:"domains"`
	Applied   bool     `json:"applied"`
}

func customView(s store.CustomService, applied bool) customServiceView {
	domains := s.Domains
	if domains == nil {
		domains = []string{}
	}
	return customServiceView{ID: s.ID, ServiceID: s.ServiceID(), Name: s.Name, Domains: domains, Applied: applied}
}

type customServiceRequest struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

// decodeCustomService reads and validates a create/update body,
// cheapest checks first: bounded single JSON object with no unknown fields
// (400); name trimmed, 1..64 runes (422); 1..50 domains, each a plain
// hostname per rules.NormalizeDomain (400 naming domains[i]); then the
// normalised list deduped keeping first occurrence. A false return means the
// error was written.
func decodeCustomService(w http.ResponseWriter, r *http.Request) (customServiceRequest, bool) {
	var in customServiceRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCustomServiceBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON {name, domains}")
		return in, false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected a single JSON object")
		return in, false
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "request body too large")
		return in, false
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > maxCustomServiceName {
		writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
			fmt.Sprintf("name must be 1 to %d characters", maxCustomServiceName))
		return in, false
	}
	if len(in.Domains) == 0 || len(in.Domains) > maxCustomDomains {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			fmt.Sprintf("domains must list 1 to %d hostnames", maxCustomDomains))
		return in, false
	}
	domains := make([]string, 0, len(in.Domains))
	seen := make(map[string]bool, len(in.Domains))
	for i, d := range in.Domains {
		n, err := rules.NormalizeDomain(d)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("domains[%d]: %v", i, err))
			return in, false
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		domains = append(domains, n)
	}
	in.Domains = domains
	return in, true
}

// customServiceID parses {id}; anything non-numeric is 404.
func customServiceID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, CodeNotFound, noSuchCustomService)
		return 0, false
	}
	return id, true
}

// builtinClash reports whether name equals a built-in AdGuard service's name,
// case-insensitively (locked uniqueness). It reads the raw catalogue once; a
// failed read is written as 502 and reported as handled.
func (a *API) builtinClash(w http.ResponseWriter, r *http.Request, name string) (handled bool) {
	services, err := a.deps.AdGuard.Services(r.Context())
	if err != nil {
		a.writeAdGuardError(w, "check custom service name", err)
		return true
	}
	for _, s := range services {
		if strings.EqualFold(s.Name, name) {
			writeError(w, http.StatusConflict, CodeConflict,
				fmt.Sprintf("%s is already a built-in AdGuard service", s.Name))
			return true
		}
	}
	return false
}

// writeCustomServiceError maps engine and store errors onto the envelope.
func (a *API) writeCustomServiceError(w http.ResponseWriter, op, name string, err error) {
	var (
		taken *store.ErrDomainTaken
		inUse *store.ErrServiceInUse
	)
	switch {
	case errors.Is(err, store.ErrCustomServiceNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, noSuchCustomService)
	case errors.Is(err, store.ErrServiceNameTaken):
		writeError(w, http.StatusConflict, CodeConflict, fmt.Sprintf("a custom service named %s already exists", name))
	case errors.As(err, &taken):
		writeError(w, http.StatusConflict, CodeConflict,
			fmt.Sprintf("%s already belongs to custom service %s", taken.Domain, taken.Owner))
	case errors.As(err, &inUse):
		writeJSON(w, http.StatusConflict, struct {
			errorBody
			GrantID int64 `json:"grant_id"`
		}{
			errorBody{Error: CodeConflict, Message: fmt.Sprintf(
				"%s is in use by active grant %d — end that grant first", name, inUse.GrantID)},
			inUse.GrantID,
		})
	default:
		a.log.Error(op+" failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not "+op)
	}
}

// handleCustomServicesList is GET /api/v1/custom-services, by id.
func (a *API) handleCustomServicesList(w http.ResponseWriter, r *http.Request) {
	var list []store.CustomService
	var err error
	if a.deps.CustomServices != nil {
		list, err = a.deps.CustomServices.ListCustomServices(r.Context())
	}
	if err != nil {
		a.writeStoreError(w, "list custom services", "", err)
		return
	}
	out := make([]customServiceView, 0, len(list))
	for _, s := range list {
		out = append(out, customView(s, true))
	}
	writeJSON(w, http.StatusOK, struct {
		CustomServices []customServiceView `json:"custom_services"`
	}{out})
}

// handleCustomServiceCreate is POST /api/v1/custom-services: 201 with the
// stored service; applied:false when AdGuard's rules lag behind.
func (a *API) handleCustomServiceCreate(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeCustomService(w, r)
	if !ok || a.builtinClash(w, r, in.Name) {
		return
	}
	res, err := a.deps.Grants.CreateService(r.Context(), in.Name, in.Domains)
	if err != nil {
		a.writeCustomServiceError(w, "create custom service", in.Name, err)
		return
	}
	writeJSON(w, http.StatusCreated, customView(res.Service, res.Applied))
}

// handleCustomServiceUpdate is PUT /api/v1/custom-services/{id}: replaces
// the name and the whole domain list.
func (a *API) handleCustomServiceUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := customServiceID(w, r)
	if !ok {
		return
	}
	in, ok := decodeCustomService(w, r)
	if !ok || a.builtinClash(w, r, in.Name) {
		return
	}
	res, err := a.deps.Grants.UpdateService(r.Context(), id, in.Name, in.Domains)
	if err != nil {
		a.writeCustomServiceError(w, "update custom service", in.Name, err)
		return
	}
	writeJSON(w, http.StatusOK, customView(res.Service, res.Applied))
}

// handleCustomServiceDelete is DELETE /api/v1/custom-services/{id}: 204, or
// 409 with grant_id while an active grant covers it (locked delete_refs).
func (a *API) handleCustomServiceDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := customServiceID(w, r)
	if !ok {
		return
	}
	res, err := a.deps.Grants.DeleteService(r.Context(), id)
	if err != nil {
		name := store.CustomServiceID(id)
		if a.deps.CustomServices != nil {
			if cs, gerr := a.deps.CustomServices.ListCustomServices(r.Context()); gerr == nil {
				for _, s := range cs {
					if s.ID == id {
						name = s.Name
					}
				}
			}
		}
		a.writeCustomServiceError(w, "delete custom service", name, err)
		return
	}
	if !res.Applied {
		a.log.Warn("custom service deleted; AdGuard rules will catch up", "service", res.Service.ServiceID())
	}
	w.WriteHeader(http.StatusNoContent)
}
