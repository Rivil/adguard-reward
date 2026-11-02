package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
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

// grantView is the wire shape of one active grant; every list is never
// null and times are RFC 3339 UTC.
type grantView struct {
	ID        int64       `json:"id"`
	ChildID   int64       `json:"child_id"`
	Services  []string    `json:"services"`
	Clients   []string    `json:"clients"`
	Shared    []sharedRef `json:"shared"`
	StartedAt string      `json:"started_at"`
	EndsAt    string      `json:"ends_at"`
}

// grantViewOf renders g with shared as given; nil becomes [].
func grantViewOf(g store.Grant, shared []sharedRef) grantView {
	if g.Services == nil {
		g.Services = []string{}
	}
	if g.Clients == nil {
		g.Clients = []string{}
	}
	if shared == nil {
		shared = []sharedRef{}
	}
	return grantView{
		ID:        g.ID,
		ChildID:   g.ChildID,
		Services:  g.Services,
		Clients:   g.Clients,
		Shared:    shared,
		StartedAt: rfc3339(g.StartedAt),
		EndsAt:    rfc3339(g.EndsAt),
	}
}

// sharedRef is one shared device a grant touches: the client and its
// owners other than the grant's child, id ascending.
type sharedRef struct {
	Client string     `json:"client"`
	Others []childRef `json:"others"`
}

// sharedOf lists, client ascending, every client in clients that some child
// other than childID currently holds, with those other holders. Ownership
// is read from children (the current mapping), not from the grant. Never
// nil.
func sharedOf(clients []string, childID int64, children []store.Child) []sharedRef {
	owners := ownerIndex(children)
	sorted := slices.Clone(clients)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	out := []sharedRef{}
	for _, c := range sorted {
		var others []childRef
		for _, ref := range owners[c] {
			if ref.ID != childID {
				others = append(others, ref)
			}
		}
		if len(others) > 0 {
			out = append(out, sharedRef{Client: c, Others: others})
		}
	}
	return out
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

// handleGrantsList is GET /api/v1/grants: every active grant, id ASC, each
// with the shared devices it touches by the current mapping. A failed
// children read only costs the shared annotations: every grant is still
// listed, with shared [], so the polled list never blanks on a store hiccup.
func (a *API) handleGrantsList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.deps.Grants.List(r.Context())
	if err != nil {
		a.writeGrantError(w, "list grants", err)
		return
	}
	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.log.Warn("list grants: children unavailable, shared left empty", "err", err)
		children = nil
	}
	out := make([]grantView, 0, len(rows))
	for _, g := range rows {
		out = append(out, grantViewOf(g, sharedOf(g.Clients, g.ChildID, children)))
	}
	writeJSON(w, http.StatusOK, struct {
		Grants []grantView `json:"grants"`
	}{out})
}

// handleGrantCreate is POST /api/v1/grants. Validation runs cheapest first
// and the children read precedes any AdGuard call, so an unknown child is a
// 404 that never touches the catalogue and a failed read is a 500 with no
// AdGuard write. The overlap lock (one active grant per child and service)
// surfaces as 409 naming the existing grant; another child's grant on a
// shared client does not overlap. The 201 names every shared device the
// grant touches and its other owners.
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

	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.writeStoreError(w, "list children", "", err)
		return
	}
	i := slices.IndexFunc(children, func(c store.Child) bool { return c.ID == in.ChildID })
	if i < 0 {
		a.writeStoreError(w, "get child", "", store.ErrChildNotFound)
		return
	}
	child := children[i]
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
		ID      int64       `json:"id"`
		EndsAt  string      `json:"ends_at"`
		Applied bool        `json:"applied"`
		Failed  []string    `json:"failed"`
		Shared  []sharedRef `json:"shared"`
	}{res.Grant.ID, rfc3339(res.Grant.EndsAt), res.Applied, failed, sharedOf(child.Clients, child.ID, children)})
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
