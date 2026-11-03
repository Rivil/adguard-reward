package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/store"
)

// toggleCatalogueTimeout bounds the built-in catalogue read of a toggle. The
// engine's ApplyTimeout (4 s) covers every AdGuard write after it, so the
// AdGuard I/O of one toggle fits the 5 s promise (c-1, c-2). Time spent
// waiting for the engine's mutex is outside that budget, as for grants.
const toggleCatalogueTimeout = time.Second

// customIDPrefix is the namespace of custom service ids; those are checked
// by the engine against the store, never against AdGuard's catalogue.
const customIDPrefix = "custom:"

type toggleRequest struct {
	ServiceID string `json:"service_id"`
	State     string `json:"state"`
}

// toggleGrantView is the active grant a permanent block trimmed; ended says
// the trim left it with no services, so it was ended.
type toggleGrantView struct {
	ID    int64 `json:"id"`
	Ended bool  `json:"ended"`
}

// toggleView is the 200 body of a toggle. failed and shared are never
// null; grant is null when no grant was trimmed.
type toggleView struct {
	ServiceID string           `json:"service_id"`
	State     string           `json:"state"`
	Applied   bool             `json:"applied"`
	Failed    []string         `json:"failed"`
	Shared    []sharedRef      `json:"shared"`
	Grant     *toggleGrantView `json:"grant"`
}

// handleToggle is POST /api/v1/children/{id}/toggles: store the child's
// permanent intent for one service and apply it to the child's clients.
// Validation runs cheapest first: the body is bounded and strict (400), an
// unknown child is 404 before any AdGuard call, and a built-in id must be in
// AdGuard's live catalogue (422), read under its own 1 s deadline — a failed
// or slow read is 502 and nothing is stored. A custom id is checked by the
// engine under its mutex (422 when no such service exists). A write that
// failed on some clients is still 200 with applied false and the failed
// names: the intent is stored and the reconciler converges them (locked
// toggle_partial_apply). shared names every device the toggle governs that
// another child also holds (locked shared_client_scope); grant reports the
// active grant a block trimmed.
func (a *API) handleToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := childID(w, r)
	if !ok {
		return
	}
	var in toggleRequest
	if !decodeGrantBody(w, r, &in, "{service_id, state}") {
		return
	}
	in.ServiceID = strings.TrimSpace(in.ServiceID)
	if in.ServiceID == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "service_id is required")
		return
	}
	if in.State != store.ToggleBlocked && in.State != store.ToggleUnblocked {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			fmt.Sprintf("state must be %q or %q", store.ToggleBlocked, store.ToggleUnblocked))
		return
	}

	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.writeStoreError(w, "list children", "", err)
		return
	}
	i := slices.IndexFunc(children, func(c store.Child) bool { return c.ID == id })
	if i < 0 {
		a.writeStoreError(w, "get child", "", store.ErrChildNotFound)
		return
	}
	child := children[i]

	if !strings.HasPrefix(in.ServiceID, customIDPrefix) {
		known, err := a.builtinKnown(r.Context(), in.ServiceID)
		if err != nil {
			a.writeAdGuardError(w, "toggle service", err)
			return
		}
		if !known {
			writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable, fmt.Sprintf("unknown service %q", in.ServiceID))
			return
		}
	}

	res, err := a.deps.Grants.Toggle(r.Context(), child.ID, in.ServiceID, in.State)
	if err != nil {
		var unknown *grants.ErrUnknownService
		switch {
		case errors.As(err, &unknown):
			writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable, fmt.Sprintf("unknown service %q", unknown.ID))
		case errors.Is(err, store.ErrChildNotFound):
			// Deleted between the children read and the engine's own read.
			a.writeStoreError(w, "toggle service", "", err)
		default:
			a.writeGrantError(w, "toggle service", err)
		}
		return
	}

	failed := res.Failed
	if failed == nil {
		failed = []string{}
	}
	var grant *toggleGrantView
	if res.Grant != nil {
		grant = &toggleGrantView{ID: res.Grant.ID, Ended: res.Grant.Ended}
	}
	writeJSON(w, http.StatusOK, toggleView{
		ServiceID: in.ServiceID,
		State:     in.State,
		Applied:   res.Applied,
		Failed:    failed,
		Shared:    sharedOf(res.Clients, child.ID, children),
		Grant:     grant,
	})
}

// builtinKnown reports whether serviceID is in AdGuard's live catalogue,
// read under toggleCatalogueTimeout.
func (a *API) builtinKnown(ctx context.Context, serviceID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, toggleCatalogueTimeout)
	defer cancel()
	services, err := a.deps.AdGuard.Services(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(services, func(s adguard.Service) bool { return s.ID == serviceID }), nil
}
