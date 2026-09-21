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

// handleChildCreate is POST /api/v1/children.
func (a *API) handleChildCreate(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeChild(w, r)
	if !ok {
		return
	}
	c, err := a.deps.Children.CreateChild(r.Context(), in.Name, in.Clients)
	if err != nil {
		a.writeStoreError(w, "create child", in.Name, err)
		return
	}
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
func (a *API) handleChildUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := childID(w, r)
	if !ok {
		return
	}
	in, ok := decodeChild(w, r)
	if !ok {
		return
	}
	c, err := a.deps.Children.UpdateChild(r.Context(), id, in.Name, in.Clients)
	if err != nil {
		a.writeStoreError(w, "update child", in.Name, err)
		return
	}
	writeJSON(w, http.StatusOK, viewOf(c))
}

// handleChildDelete is DELETE /api/v1/children/{id}.
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
	w.WriteHeader(http.StatusNoContent)
}
