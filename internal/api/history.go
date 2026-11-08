package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Rivil/adguard-reward/internal/store"
)

// Wire statuses of a history grant, the same values the overview filters on.
// "ended_unknown" is a pre-upgrade end whose reason was never recorded — never
// shown as ended early (locked legacy_end_reason).
const (
	historyOngoing        = store.HistoryOngoing
	historyExpired        = store.HistoryExpired
	historyEndedEarly     = store.HistoryEndedEarly
	historyPermanentBlock = store.HistoryPermanentBlock
	historyEndedUnknown   = store.HistoryEndedUnknown
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

// historyOverviewGrantView is a grant as the per-child page shows it, plus the
// child it belongs to.
type historyOverviewGrantView struct {
	historyGrantView
	Child historyChildView `json:"child"`
}

// historyOverviewPageView is one overview page. Child is set only under a
// child filter.
type historyOverviewPageView struct {
	Child      *historyChildView          `json:"child"`
	Grants     []historyOverviewGrantView `json:"grants"`
	NextBefore *int64                     `json:"next_before"`
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
	before, ok := historyBefore(w, r.URL.Query())
	if !ok {
		return
	}
	h, err := a.deps.Children.ListGrantHistory(r.Context(), id, before, store.HistoryPageSize)
	if err != nil {
		a.writeStoreError(w, "list grant history", "", err)
		return
	}

	names := a.liveNames(r, unnamedServices(h))
	out := historyPageView{
		Child:      historyChildView{ID: h.ChildID, Name: h.ChildName},
		Grants:     make([]historyGrantView, 0, len(h.Grants)),
		NextBefore: nextBefore(h),
	}
	for _, g := range h.Grants {
		out.Grants = append(out.Grants, historyGrantOf(g, names))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHistoryOverview is GET /api/v1/history: one page of every child's
// grants, newest first, narrowed by child, service, status, from and until,
// with before as the cursor. from is inclusive and until exclusive, both RFC
// 3339 instants the client computes, because the server knows no time zone.
// Names resolve as on the per-child page. An unknown, deleted, empty or
// non-numeric child is 404; any other malformed or empty parameter, or any
// repeated one, is 400, never an unfiltered page.
func (a *API) handleHistoryOverview(w http.ResponseWriter, r *http.Request) {
	f, ok := historyFilter(w, r)
	if !ok {
		return
	}
	h, err := a.deps.Children.ListHistory(r.Context(), f, store.HistoryPageSize)
	if err != nil {
		a.writeStoreError(w, "list history", "", err)
		return
	}

	names := a.liveNames(r, unnamedServices(h))
	out := historyOverviewPageView{
		Grants:     make([]historyOverviewGrantView, 0, len(h.Grants)),
		NextBefore: nextBefore(h),
	}
	if f.ChildID != 0 {
		out.Child = &historyChildView{ID: h.ChildID, Name: h.ChildName}
	}
	for _, g := range h.Grants {
		out.Grants = append(out.Grants, historyOverviewGrantView{
			historyGrantView: historyGrantOf(g, names),
			Child:            historyChildView{ID: g.ChildID, Name: g.ChildName},
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// historyFilter reads the overview's query strictly: a filter whose value is
// bad is refused rather than dropped into an unfiltered page. Keys it does not
// know are ignored. On failure it has written the response.
func historyFilter(w http.ResponseWriter, r *http.Request) (store.HistoryFilter, bool) {
	bad := func(message string) (store.HistoryFilter, bool) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, message)
		return store.HistoryFilter{}, false
	}
	// ParseQuery, not r.URL.Query(), which silently drops a pair it cannot decode.
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return bad("malformed query")
	}
	for _, key := range []string{"child", "service", "status", "from", "until"} {
		if len(q[key]) > 1 {
			return bad(key + " may be given only once")
		}
	}
	for _, key := range []string{"service", "status", "from", "until"} {
		if v, set := q[key]; set && v[0] == "" {
			return bad(key + " must not be empty")
		}
	}

	var f store.HistoryFilter
	if v, set := q["child"]; set {
		n, err := strconv.ParseInt(v[0], 10, 64)
		if err != nil || n <= 0 {
			writeError(w, http.StatusNotFound, CodeNotFound, noSuchChild)
			return store.HistoryFilter{}, false
		}
		f.ChildID = n
	}
	f.ServiceID = q.Get("service")
	f.Status = q.Get("status")
	switch f.Status {
	case "", historyOngoing, historyExpired, historyEndedEarly, historyPermanentBlock, historyEndedUnknown:
	default:
		return bad("unknown status")
	}
	if f.From, err = historyInstant(q, "from"); err != nil {
		return bad("from must be an RFC 3339 time")
	}
	if f.Until, err = historyInstant(q, "until"); err != nil {
		return bad("until must be an RFC 3339 time")
	}
	before, ok := historyBefore(w, q)
	if !ok {
		return store.HistoryFilter{}, false
	}
	f.Before = before
	return f, true
}

// historyInstant parses q's key as RFC 3339; the zero time when it is unset.
// The store reads the zero time as no filter, so that instant is refused.
func historyInstant(q url.Values, key string) (time.Time, error) {
	if !q.Has(key) {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, q.Get(key))
	if err == nil && t.IsZero() {
		return time.Time{}, errors.New("zero instant")
	}
	return t, err
}

// historyBefore reads the paging cursor, 0 when unset. Anything but one
// positive integer is 400, which it has then written.
func historyBefore(w http.ResponseWriter, q url.Values) (int64, bool) {
	raw, set := q["before"]
	if !set {
		return 0, true
	}
	n, err := strconv.ParseInt(raw[0], 10, 64)
	if err != nil || n <= 0 || len(raw) != 1 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "before must be a positive grant id")
		return 0, false
	}
	return n, true
}

// nextBefore is the cursor of the page after h, nil on the last one.
func nextBefore(h store.GrantHistory) *int64 {
	if h.HasMore && len(h.Grants) > 0 {
		return ptr(h.Grants[len(h.Grants)-1].ID)
	}
	return nil
}

// historyGrantOf is a grant on the wire. names holds the live names of services
// with no snapshot.
func historyGrantOf(g store.HistoryGrant, names map[string]string) historyGrantView {
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
		sv := historyServiceView{ID: s.ID, Name: serviceName(s.ID, s.Name, names)}
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
	return v
}

// serviceName resolves a service's display name: the snapshot, else the live
// name, else the bare id (locked service_names).
func serviceName(id, snapshot string, live map[string]string) string {
	if snapshot != "" {
		return snapshot
	}
	if n := live[id]; n != "" {
		return n
	}
	return id
}

// unnamedServices lists the ids on the page that have no snapshot.
func unnamedServices(h store.GrantHistory) []string {
	var out []string
	for _, g := range h.Grants {
		for _, s := range g.Services {
			if s.Name == "" {
				out = append(out, s.ID)
			}
		}
	}
	return out
}

// liveNames is the live catalogue's names for ids, read only when ids is not
// empty. A failed read is logged and yields no names, so those services fall
// back to their bare ids.
func (a *API) liveNames(r *http.Request, ids []string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	services, err := a.catalogue(r.Context())
	if err != nil {
		a.log.Warn("history: catalogue unavailable, unsnapshotted services show their ids", "err", err)
		return nil
	}
	byID := make(map[string]string, len(services))
	for _, s := range services {
		byID[s.ID] = s.Name
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		out[id] = byID[id]
	}
	return out
}

func ptr[T any](v T) *T { return &v }
