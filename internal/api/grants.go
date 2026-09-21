package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/store"
)

// maxGrantBody bounds a grant body: a child id, a few service ids and a
// duration is well under a kilobyte.
const maxGrantBody = 16 << 10

const noSuchGrant = "no such grant"

// grantView is the wire shape of one active grant; both lists are never
// null and times are RFC 3339 UTC.
type grantView struct {
	ID        int64    `json:"id"`
	ChildID   int64    `json:"child_id"`
	Services  []string `json:"services"`
	Clients   []string `json:"clients"`
	StartedAt string   `json:"started_at"`
	EndsAt    string   `json:"ends_at"`
}

func grantViewOf(g store.Grant) grantView {
	if g.Services == nil {
		g.Services = []string{}
	}
	if g.Clients == nil {
		g.Clients = []string{}
	}
	return grantView{
		ID:        g.ID,
		ChildID:   g.ChildID,
		Services:  g.Services,
		Clients:   g.Clients,
		StartedAt: rfc3339(g.StartedAt),
		EndsAt:    rfc3339(g.EndsAt),
	}
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

type grantRequest struct {
	ChildID  int64    `json:"child_id"`
	Services []string `json:"services"`
	Duration int      `json:"duration"` // seconds
}

type durationRequest struct {
	Duration int `json:"duration"` // seconds
}

// decodeGrantBody reads a body the way decodeChild does: bounded, no
// unknown fields, a single object. A false return means the error was
// written.
func decodeGrantBody(w http.ResponseWriter, r *http.Request, dst any, shape string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGrantBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON "+shape)
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected a single JSON object")
		return false
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "request body too large")
		return false
	}
	return true
}

// checkDuration turns whole seconds into a duration inside the locked
// bounds, or writes the 422. Shared by create and extend.
func checkDuration(w http.ResponseWriter, seconds int) (time.Duration, bool) {
	d := time.Duration(seconds) * time.Second
	if seconds <= 0 || d < grants.MinDuration || d > grants.MaxDuration {
		writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
			fmt.Sprintf("duration must be between %d and %d seconds",
				int(grants.MinDuration/time.Second), int(grants.MaxDuration/time.Second)))
		return 0, false
	}
	return d, true
}

// grantID parses the {id} path value; anything non-numeric is 404 so the
// id space cannot be probed by shape.
func grantID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, noSuchGrant)
		return 0, false
	}
	return id, true
}

// writeGrantError maps the engine's sentinels onto the envelope.
func (a *API) writeGrantError(w http.ResponseWriter, op string, err error) {
	var revert *grants.ErrRevertFailed
	switch {
	case errors.Is(err, grants.ErrGrantNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, noSuchGrant)
	case errors.As(err, &revert):
		a.log.Warn(op+": revert failed", "clients", revert.Clients)
		writeError(w, http.StatusBadGateway, CodeAdGuardUnavailable,
			"could not re-block "+strings.Join(revert.Clients, ", ")+" — the grant stays active and will be retried")
	default:
		a.log.Error(op+" failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not "+op)
	}
}

// handleGrantsList is GET /api/v1/grants: every active grant, id ASC.
func (a *API) handleGrantsList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.deps.Grants.List(r.Context())
	if err != nil {
		a.writeGrantError(w, "list grants", err)
		return
	}
	out := make([]grantView, 0, len(rows))
	for _, g := range rows {
		out = append(out, grantViewOf(g))
	}
	writeJSON(w, http.StatusOK, struct {
		Grants []grantView `json:"grants"`
	}{out})
}

// handleGrantCreate is POST /api/v1/grants. Validation runs cheapest first
// and the child lookup precedes any AdGuard call, so an unknown child is a
// 404 that never touches the catalogue. The overlap lock (one active grant
// per child and service) surfaces as 409 naming the existing grant.
func (a *API) handleGrantCreate(w http.ResponseWriter, r *http.Request) {
	var in grantRequest
	if !decodeGrantBody(w, r, &in, "{child_id, services, duration}") {
		return
	}
	d, ok := checkDuration(w, in.Duration)
	if !ok {
		return
	}
	services := make([]string, 0, len(in.Services))
	seen := map[string]bool{}
	for _, s := range in.Services {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		services = append(services, s)
	}
	if len(services) == 0 {
		writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable, "services must name at least one service")
		return
	}

	child, err := a.deps.Children.GetChild(r.Context(), in.ChildID)
	if err != nil {
		a.writeStoreError(w, "get child", "", err)
		return
	}
	if len(child.Clients) == 0 {
		writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable, "child has no clients to unblock")
		return
	}
	catalogue, err := a.deps.AdGuard.Services(r.Context())
	if err != nil {
		a.writeAdGuardError(w, "create grant", err)
		return
	}
	known := make(map[string]bool, len(catalogue))
	for _, s := range catalogue {
		known[s.ID] = true
	}
	for _, s := range services {
		if !known[s] {
			writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable, fmt.Sprintf("unknown service %q", s))
			return
		}
	}

	res, err := a.deps.Grants.Create(r.Context(), child.ID, services, child.Clients, d)
	if err != nil {
		var overlap *store.ErrGrantOverlap
		if errors.As(err, &overlap) {
			writeJSON(w, http.StatusConflict, struct {
				errorBody
				GrantID int64 `json:"grant_id"`
			}{
				errorBody{Error: CodeConflict, Message: fmt.Sprintf("%s is already granted by grant %d", overlap.ServiceID, overlap.ExistingID)},
				overlap.ExistingID,
			})
			return
		}
		a.writeGrantError(w, "create grant", err)
		return
	}
	failed := res.Failed
	if failed == nil {
		failed = []string{}
	}
	writeJSON(w, http.StatusCreated, struct {
		ID      int64    `json:"id"`
		EndsAt  string   `json:"ends_at"`
		Applied bool     `json:"applied"`
		Failed  []string `json:"failed"`
	}{res.Grant.ID, rfc3339(res.Grant.EndsAt), res.Applied, failed})
}

// handleGrantExtend is POST /api/v1/grants/{id}/extend: ends_at moves by
// duration from where it was, not from now.
func (a *API) handleGrantExtend(w http.ResponseWriter, r *http.Request) {
	id, ok := grantID(w, r)
	if !ok {
		return
	}
	var in durationRequest
	if !decodeGrantBody(w, r, &in, "{duration}") {
		return
	}
	d, ok := checkDuration(w, in.Duration)
	if !ok {
		return
	}
	g, err := a.deps.Grants.Extend(r.Context(), id, d)
	if err != nil {
		a.writeGrantError(w, "extend grant", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ID     int64  `json:"id"`
		EndsAt string `json:"ends_at"`
	}{g.ID, rfc3339(g.EndsAt)})
}

// handleGrantEnd is POST /api/v1/grants/{id}/end: the block is restored
// before the response is written. A client that could not be re-blocked is
// 502 and the grant stays active for the reconciler.
func (a *API) handleGrantEnd(w http.ResponseWriter, r *http.Request) {
	id, ok := grantID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Grants.End(r.Context(), id); err != nil {
		a.writeGrantError(w, "end grant", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
