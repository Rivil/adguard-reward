package api

import (
	"net/http"
	"strconv"

	"github.com/Rivil/adguard-reward/internal/store"
)

// Wire statuses of a history grant. "ended_unknown" is a pre-upgrade end
// whose reason was never recorded — never shown as ended early (locked
// legacy_end_reason).
const (
	historyOngoing        = "ongoing"
	historyExpired        = "expired"
	historyEndedEarly     = "ended_early"
	historyPermanentBlock = "permanent_block"
	historyEndedUnknown   = "ended_unknown"
)

// endStatus maps a grant's stored (status, end_reason) to its wire status.
func endStatus(status, reason string) string {
	switch status {
	case store.StatusActive:
		return historyOngoing
	case store.StatusExpired:
		return historyExpired
	}
	switch reason {
	case store.EndReasonEndedEarly:
		return historyEndedEarly
	case store.EndReasonPermanentBlock:
		return historyPermanentBlock
	case store.EndReasonExpired:
		return historyExpired
	}
	return historyEndedUnknown
}

type historyChildView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type historyServiceView struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	RemovedAt     *string `json:"removed_at"`
	RemovedReason *string `json:"removed_reason"`
}

type historyExtensionView struct {
	At        string `json:"at"`
	OldEndsAt string `json:"old_ends_at"`
	NewEndsAt string `json:"new_ends_at"`
}

type historyGrantView struct {
	ID         int64                  `json:"id"`
	Status     string                 `json:"status"`
	StartedAt  string                 `json:"started_at"`
	EndsAt     string                 `json:"ends_at"`
	EndedAt    *string                `json:"ended_at"`
	Services   []historyServiceView   `json:"services"`
	Extensions []historyExtensionView `json:"extensions"`
}

type historyPageView struct {
	Child      historyChildView   `json:"child"`
	Grants     []historyGrantView `json:"grants"`
	NextBefore *int64             `json:"next_before"`
}

// handleHistory is GET /api/v1/children/{id}/history[?before=<grant id>]:
// one page of the child's grants, newest first. Each service shows the name
// snapshotted at grant start; a service with no snapshot (a pre-upgrade
// grant) resolves to the live catalogue name, else its bare id. The
// catalogue is read only when a snapshot is missing, and a failed read is
// not an error — the bare id stands in (locked service_names). An unknown,
// deleted or non-numeric child is 404; a before that is not a positive
// integer is 400.
func (a *API) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := childID(w, r)
	if !ok {
		return
	}
	var before int64
	if raw, set := r.URL.Query()["before"]; set {
		n, err := strconv.ParseInt(raw[0], 10, 64)
		if err != nil || n <= 0 || len(raw) != 1 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "before must be a positive grant id")
			return
		}
		before = n
	}
	h, err := a.deps.Children.ListGrantHistory(r.Context(), id, before, store.HistoryPageSize)
	if err != nil {
		a.writeStoreError(w, "list grant history", "", err)
		return
	}

	names := a.liveNames(r, h)
	out := historyPageView{
		Child:  historyChildView{ID: h.ChildID, Name: h.ChildName},
		Grants: make([]historyGrantView, 0, len(h.Grants)),
	}
	for _, g := range h.Grants {
		v := historyGrantView{
			ID:         g.ID,
			Status:     endStatus(g.Status, g.EndReason),
			StartedAt:  rfc3339(g.StartedAt),
			EndsAt:     rfc3339(g.EndsAt),
			Services:   make([]historyServiceView, 0, len(g.Services)),
			Extensions: make([]historyExtensionView, 0, len(g.Extensions)),
		}
		if g.EndedAt != nil && v.Status != historyOngoing {
			v.EndedAt = ptr(rfc3339(*g.EndedAt))
		}
		for _, s := range g.Services {
			sv := historyServiceView{ID: s.ID, Name: s.Name}
			if sv.Name == "" {
				sv.Name = s.ID
				if n, ok := names[s.ID]; ok && n != "" {
					sv.Name = n
				}
			}
			if s.RemovedAt != nil {
				sv.RemovedAt = ptr(rfc3339(*s.RemovedAt))
				sv.RemovedReason = ptr(s.RemovedReason)
			}
			v.Services = append(v.Services, sv)
		}
		for _, x := range g.Extensions {
			v.Extensions = append(v.Extensions, historyExtensionView{
				At: rfc3339(x.At), OldEndsAt: rfc3339(x.OldEndsAt), NewEndsAt: rfc3339(x.NewEndsAt),
			})
		}
		out.Grants = append(out.Grants, v)
	}
	if h.HasMore && len(h.Grants) > 0 {
		out.NextBefore = ptr(h.Grants[len(h.Grants)-1].ID)
	}
	writeJSON(w, http.StatusOK, out)
}

// liveNames is the live catalogue by service id, read only when some
// service on the page has no snapshot. A failed read is logged and yields
// no names, so those services fall back to their bare ids.
func (a *API) liveNames(r *http.Request, h store.GrantHistory) map[string]string {
	missing := false
	for _, g := range h.Grants {
		for _, s := range g.Services {
			if s.Name == "" {
				missing = true
			}
		}
	}
	if !missing {
		return nil
	}
	services, err := a.catalogue(r.Context())
	if err != nil {
		a.log.Warn("history: catalogue unavailable, unsnapshotted services show their ids", "err", err)
		return nil
	}
	out := make(map[string]string, len(services))
	for _, s := range services {
		out[s.ID] = s.Name
	}
	return out
}

func ptr[T any](v T) *T { return &v }
