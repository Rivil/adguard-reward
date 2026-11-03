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
	"unicode/utf8"

	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/store"
)

// maxChildBody bounds a child create/update body: a name plus a handful of
// client names is well under a kilobyte.
const maxChildBody = 16 << 10

const (
	maxChildName  = 64  // runes
	maxClientName = 256 // bytes, as AdGuard stores it
	noSuchChild   = "no such child"
)

// childView is the wire shape of one child; clients is never null.
type childView struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Clients []string `json:"clients"`
}

func viewOf(c store.Child) childView {
	if c.Clients == nil {
		c.Clients = []string{}
	}
	return childView{ID: c.ID, Name: c.Name, Clients: c.Clients}
}

type childRequest struct {
	Name    string   `json:"name"`
	Clients []string `json:"clients"`
}

// decodeChild reads and validates a create/update body the way handleLogin
// does: bounded, no unknown fields, a single object. The name is trimmed
// and must be 1..64 runes; clients are trimmed, empties dropped, deduped,
// each at most 256 bytes. A false return means the error was written.
func decodeChild(w http.ResponseWriter, r *http.Request) (childRequest, bool) {
	var in childRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChildBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON {name, clients}")
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
	if in.Name == "" || utf8.RuneCountInString(in.Name) > maxChildName {
		writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("name must be 1 to %d characters", maxChildName))
		return in, false
	}
	clients := make([]string, 0, len(in.Clients))
	seen := map[string]bool{}
	for _, c := range in.Clients {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if len(c) > maxClientName {
			writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("client name longer than %d bytes", maxClientName))
			return in, false
		}
		seen[c] = true
		clients = append(clients, c)
	}
	in.Clients = clients
	return in, true
}

// childID parses the {id} path value; anything non-numeric is 404 so the
// id space cannot be probed by shape.
func childID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, noSuchChild)
		return 0, false
	}
	return id, true
}

// writeStoreError maps the store's sentinels onto the envelope: conflicts
// are 409 with a message naming the child (and client), an unknown id is
// 404, anything else is 500. name is the requested child name, for the
// name-taken message.
func (a *API) writeStoreError(w http.ResponseWriter, op, name string, err error) {
	var taken *store.ErrClientTaken
	switch {
	case errors.Is(err, store.ErrChildNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, noSuchChild)
	case errors.Is(err, store.ErrNameTaken):
		writeError(w, http.StatusConflict, CodeConflict, fmt.Sprintf("a child named %s already exists", name))
	case errors.As(err, &taken):
		writeError(w, http.StatusConflict, CodeConflict,
			fmt.Sprintf("client %s is already assigned to %s", taken.Client, taken.ChildName))
	default:
		a.log.Error(op+" failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not "+op)
	}
}

// handleChildrenList is GET /api/v1/children, oldest first.
func (a *API) handleChildrenList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.writeStoreError(w, "list children", "", err)
		return
	}
	out := make([]childView, 0, len(rows))
	for _, c := range rows {
		out = append(out, viewOf(c))
	}
	writeJSON(w, http.StatusOK, struct {
		Children []childView `json:"children"`
	}{out})
}

// handleChildCreate is POST /api/v1/children. A name another child has
// (case-insensitively) is 409 before AdGuard is touched; the store's
// ErrNameTaken stays the race backstop. Each listed client — shared with
// another child or not — first gets the new child's baseline in AdGuard: a
// new child has no grants and no siblings, so that is AdGuard's global list,
// added to the client's own list minus anything another owner's active grant
// holds open there (c-3). An adopt failure is 502 naming the client and no
// child is created. A POST without clients adopts nothing in AdGuard. After
// the store write the managed rules region is resynced (resyncRules).
func (a *API) handleChildCreate(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeChild(w, r)
	if !ok {
		return
	}
	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.writeStoreError(w, "create child", in.Name, err)
		return
	}
	if err := updateConflict(children, 0, in); err != nil {
		a.writeStoreError(w, "create child", in.Name, err)
		return
	}
	if len(in.Clients) > 0 {
		if err := a.deps.Grants.AdoptClients(r.Context(), 0, nil, in.Clients); err != nil {
			a.writeAdoptError(w, "create child", err)
			return
		}
	}
	c, err := a.deps.Children.CreateChild(r.Context(), in.Name, in.Clients)
	if err != nil {
		a.writeStoreError(w, "create child", in.Name, err)
		return
	}
	a.resyncRules(r, "create child")
	writeJSON(w, http.StatusCreated, viewOf(c))
}

// handleChildGet is GET /api/v1/children/{id}.
func (a *API) handleChildGet(w http.ResponseWriter, r *http.Request) {
	id, ok := childID(w, r)
	if !ok {
		return
	}
	c, err := a.deps.Children.GetChild(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, "get child", "", err)
		return
	}
	writeJSON(w, http.StatusOK, viewOf(c))
}

// handleChildUpdate is PUT /api/v1/children/{id}: a full replace of name
// and client set, atomic in the store. The name conflict is 409 for a
// different child's name; an unchanged name is not a conflict.
//
// A client another child already holds is shared, not a conflict (c-1).
// Every client the PUT adds — shared ones included — gets the child's
// baseline in AdGuard before the store is written: what the clients staying
// with the child effectively block — AdGuard's global list when none stay —
// plus the services under the child's active grants and any active grant on
// a staying client, added to the client's own list with
// use_global_blocked_services cleared, minus what another owner's active
// grant holds open on it (c-3). Nothing it blocks is removed. The 404 and
// the name 409 are decided before AdGuard is touched; an AdGuard failure is
// 502 naming the client and leaves the store as it was. A PUT that adds
// nothing — a rename or removals only, a shared client's other owners
// keeping it — writes no client in AdGuard (c-7). After the store write the
// managed rules region is resynced (resyncRules).
func (a *API) handleChildUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := childID(w, r)
	if !ok {
		return
	}
	in, ok := decodeChild(w, r)
	if !ok {
		return
	}
	cur, err := a.deps.Children.GetChild(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, "update child", in.Name, err)
		return
	}
	children, err := a.deps.Children.ListChildren(r.Context())
	if err != nil {
		a.writeStoreError(w, "update child", in.Name, err)
		return
	}
	if err := updateConflict(children, id, in); err != nil {
		a.writeStoreError(w, "update child", in.Name, err)
		return
	}
	var added, siblings []string
	for _, c := range in.Clients {
		if slices.Contains(cur.Clients, c) {
			siblings = append(siblings, c)
		} else {
			added = append(added, c)
		}
	}
	if len(added) > 0 {
		if err := a.deps.Grants.AdoptClients(r.Context(), id, siblings, added); err != nil {
			a.writeAdoptError(w, "update child", err)
			return
		}
	}
	c, err := a.deps.Children.UpdateChild(r.Context(), id, in.Name, in.Clients)
	if err != nil {
		a.writeStoreError(w, "update child", in.Name, err)
		return
	}
	a.resyncRules(r, "update child")
	writeJSON(w, http.StatusOK, viewOf(c))
}

// updateConflict mirrors the store's name rule so it is answered before
// any AdGuard write: another child (not selfID; 0 for a create) with the
// name, compared as the column's COLLATE NOCASE does — ASCII case-folded —
// is store.ErrNameTaken. Clients are never a conflict: a client may belong
// to several children. The store re-checks inside its transaction.
func updateConflict(children []store.Child, selfID int64, in childRequest) error {
	for _, c := range children {
		if c.ID != selfID && asciiFold(c.Name) == asciiFold(in.Name) {
			return store.ErrNameTaken
		}
	}
	return nil
}

// asciiFold lower-cases ASCII letters only, as SQLite's NOCASE does.
func asciiFold(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// writeAdoptError answers a failed baseline write for op: 502 naming the
// client for *grants.ErrAdoptFailed, 502 for an AdGuard read failure, 500
// for anything else (a store read inside the engine).
func (a *API) writeAdoptError(w http.ResponseWriter, op string, err error) {
	var af *grants.ErrAdoptFailed
	switch {
	case errors.As(err, &af):
		a.log.Warn(op+": adguard baseline write failed", "client", af.Client, "err", err)
		writeError(w, http.StatusBadGateway, CodeAdGuardUnavailable,
			fmt.Sprintf("could not apply the child's blocked services to %s in AdGuard Home — nothing was saved; try again shortly", af.Client))
	case adguardDown(err):
		a.writeAdGuardError(w, op, err)
	default:
		a.log.Error(op+": baseline failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not "+op)
	}
}

// handleChildDelete is DELETE /api/v1/children/{id}. It writes no client in
// AdGuard: a shared client stays with its other owners, as it is (c-7).
// After the delete the managed rules region is resynced (resyncRules).
func (a *API) handleChildDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := childID(w, r)
	if !ok {
		return
	}
	ok, err := a.deps.Children.DeleteChild(r.Context(), id)
	if err != nil {
		a.writeStoreError(w, "delete child", "", err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, noSuchChild)
		return
	}
	a.resyncRules(r, "delete child")
	w.WriteHeader(http.StatusNoContent)
}

// resyncRules converges the managed user_rules region after a committed
// child write, so custom-service block lines follow the child's devices
// before the response. AdoptClients runs before the store write and cannot
// do it. A failure is logged, never returned: the child write stands and the
// reconciler converges the region within one tick.
func (a *API) resyncRules(r *http.Request, op string) {
	if a.deps.Grants == nil {
		return
	}
	if err := a.deps.Grants.SyncRules(r.Context()); err != nil {
		a.log.Warn(op+": managed rules not updated; the reconciler will retry", "err", err)
	}
}
