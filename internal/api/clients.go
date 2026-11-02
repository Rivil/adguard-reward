package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/store"
)

// maxClientBody bounds a POST /clients body: a name and a few ids.
const maxClientBody = 16 << 10

const adguardDownMsg = "AdGuard Home is unreachable — try again shortly"

// childRef is the {id, name} pair views use to point at a child.
type childRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// clientView is one row of GET /api/v1/clients: a live AdGuard persistent
// client plus every child holding it, id ascending. children is never null:
// an unassigned client has [].
type clientView struct {
	Name                     string     `json:"name"`
	IDs                      []string   `json:"ids"`
	UseGlobalBlockedServices bool       `json:"use_global_blocked_services"`
	Children                 []childRef `json:"children"`
}

// unlabelledView is one row of GET /api/v1/clients' unlabelled list: an
// AdGuard auto client no persistent client covers. suggested_ids is never
// null.
type unlabelledView struct {
	IP           string   `json:"ip"`
	Hostname     string   `json:"hostname"`
	Source       string   `json:"source"`
	SuggestedIDs []string `json:"suggested_ids"`
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

// ownerIndex maps client name → every child holding it, id ascending. A
// shared client lists all its holders; none is dropped.
func ownerIndex(children []store.Child) map[string][]childRef {
	owners := map[string][]childRef{}
	for _, c := range children {
		for _, name := range c.Clients {
			owners[name] = append(owners[name], childRef{ID: c.ID, Name: c.Name})
		}
	}
	for _, refs := range owners {
		slices.SortFunc(refs, func(a, b childRef) int { return cmp.Compare(a.ID, b.ID) })
	}
	return owners
}

// holders is owners' entry for name, never nil.
func holders(owners map[string][]childRef, name string) []childRef {
	if refs := owners[name]; refs != nil {
		return refs
	}
	return []childRef{}
}

// handleClients is GET /api/v1/clients: the persistent clients with every
// child holding them, plus every auto client none of them covers (locked:
// unlabelled_listing). Every call reads AdGuard — one /control/clients
// read and one lease read; nothing is memoised. A failed lease read only
// costs the MAC suggestions: devices are still listed, IP-only.
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
	leases, err := a.deps.AdGuard.Leases(r.Context())
	if err != nil {
		a.log.Warn("list clients: dhcp leases unavailable, suggesting ip only", "err", err)
		leases = nil
	}
	owners := ownerIndex(children)
	out := make([]clientView, 0, len(res.Persistent))
	for _, pc := range res.Persistent {
		v := clientView{Name: pc.Name, IDs: pc.IDs, UseGlobalBlockedServices: pc.UseGlobalBlockedServices,
			Children: holders(owners, pc.Name)}
		if v.IDs == nil {
			v.IDs = []string{}
		}
		out = append(out, v)
	}
	devices := adguard.Unlabelled(res.Persistent, res.AutoClients, leases)
	unlabelled := make([]unlabelledView, 0, len(devices))
	for _, d := range devices {
		ids := d.SuggestedIDs
		if ids == nil {
			ids = []string{}
		}
		unlabelled = append(unlabelled, unlabelledView{IP: d.IP, Hostname: d.Hostname, Source: d.Source, SuggestedIDs: ids})
	}
	writeJSON(w, http.StatusOK, struct {
		Clients    []clientView     `json:"clients"`
		Unlabelled []unlabelledView `json:"unlabelled"`
	}{out, unlabelled})
}

type createClientRequest struct {
	Name    string   `json:"name"`
	IDs     []string `json:"ids"`
	ChildID *int64   `json:"child_id"`
}

// decodeCreateClient reads a POST /clients body: bounded, no unknown fields,
// one object. The name is trimmed and must be 1..256 bytes; ids are trimmed,
// empties dropped, deduped, each at most 256 bytes, and at least one must
// remain. child_id null or absent means no child. A false return means the
// error was written.
func decodeCreateClient(w http.ResponseWriter, r *http.Request) (createClientRequest, bool) {
	var in createClientRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClientBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON {name, ids, child_id}")
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
	if in.Name == "" || len(in.Name) > maxClientName {
		writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("name must be 1 to %d bytes", maxClientName))
		return in, false
	}
	ids := make([]string, 0, len(in.IDs))
	seen := map[string]bool{}
	for _, id := range in.IDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if len(id) > maxClientName {
			writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("id longer than %d bytes", maxClientName))
			return in, false
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "at least one id (IP, CIDR or MAC) is required")
		return in, false
	}
	in.IDs = ids
	return in, true
}

// handleClientCreate is POST /api/v1/clients: create an AdGuard persistent
// client and, with child_id, assign it to that child in the same action.
// An unknown child (404) and a name any other child's mapping already holds
// (409, naming that child) are refused before AdGuard is written; only when
// every holder is child_id itself — a stale self-mapping — is it allowed,
// and it collapses in the store.
func (a *API) handleClientCreate(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeCreateClient(w, r)
	if !ok {
		return
	}
	var child *childRef
	if in.ChildID != nil {
		c, err := a.deps.Children.GetChild(r.Context(), *in.ChildID)
		if err != nil {
			a.writeStoreError(w, "get child", "", err)
			return
		}
		child = &childRef{ID: c.ID, Name: c.Name}
	}
	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.writeStoreError(w, "list children", "", err)
		return
	}
	for _, ref := range ownerIndex(children)[in.Name] {
		if child == nil || ref.ID != child.ID {
			writeError(w, http.StatusConflict, CodeConflict,
				fmt.Sprintf("client %s is already assigned to %s", in.Name, ref.Name))
			return
		}
	}
	if err := a.deps.Grants.CreateClient(r.Context(), in.Name, in.IDs, in.ChildID); err != nil {
		a.writeCreateClientError(w, in.Name, err)
		return
	}
	owners := []childRef{}
	if child != nil {
		owners = append(owners, *child)
	}
	writeJSON(w, http.StatusCreated, clientView{
		Name:                     in.Name,
		IDs:                      in.IDs,
		UseGlobalBlockedServices: child == nil,
		Children:                 owners,
	})
}

// writeCreateClientError maps a CreateClient failure: AdGuard's own
// refusals are 409 (name or id taken) or 400 (input rejected, AdGuard's
// message passed through); store conflicts and an unknown child go through
// writeStoreError; AdGuard down, slow or refusing the service credential is
// 502; anything else is 500.
func (a *API) writeCreateClientError(w http.ResponseWriter, name string, err error) {
	var idTaken *adguard.ErrIDTaken
	var rejected *adguard.InputRejectedError
	var clientTaken *store.ErrClientTaken
	switch {
	case errors.Is(err, adguard.ErrClientExists):
		writeError(w, http.StatusConflict, CodeConflict, fmt.Sprintf("a client named %s already exists in AdGuard Home", name))
	case errors.As(err, &idTaken):
		writeError(w, http.StatusConflict, CodeConflict,
			fmt.Sprintf("%s is already used by client %s", idTaken.ID, idTaken.Owner))
	case errors.As(err, &rejected):
		writeError(w, http.StatusBadRequest, CodeBadRequest, "AdGuard Home refused the client: "+rejected.Message)
	case errors.Is(err, store.ErrChildNotFound), errors.As(err, &clientTaken):
		a.writeStoreError(w, "create client", "", err)
	case adguardDown(err):
		a.writeAdGuardError(w, "create client", err)
	default:
		a.log.Error("create client failed", "client", name, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create client")
	}
}

// adguardDown reports whether err is AdGuard failing rather than the
// store: a non-2xx answer, the service credential refused, a transport
// error or the apply deadline.
func adguardDown(err error) bool {
	var status *adguard.StatusError
	var transport *url.Error
	return errors.As(err, &status) || errors.As(err, &transport) ||
		errors.Is(err, adguard.ErrBadCredentials) || errors.Is(err, context.DeadlineExceeded)
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
