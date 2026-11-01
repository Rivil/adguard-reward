package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/store"
)

type renameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// decodeRename reads a POST /clients/rename body: bounded, no unknown
// fields, one object; from and to are trimmed and must be 1..256 bytes.
// Names travel in the body, never the path, so "/" and "%" need no
// escaping. A false return means the error was written.
func decodeRename(w http.ResponseWriter, r *http.Request) (renameRequest, bool) {
	var in renameRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClientBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "expected JSON {from, to}")
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
	in.From = strings.TrimSpace(in.From)
	in.To = strings.TrimSpace(in.To)
	for _, n := range []string{in.From, in.To} {
		if n == "" || len(n) > maxClientName {
			writeError(w, http.StatusBadRequest, CodeBadRequest, fmt.Sprintf("names must be 1 to %d bytes", maxClientName))
			return in, false
		}
	}
	return in, true
}

// handleClientRename is POST /api/v1/clients/rename: rename any persistent
// client the app lists, assigned or not (locked: rename_scope). The engine
// renames it in AdGuard and then in the child mapping and active grants,
// so a running grant still reverts on the device. The 200 body is the
// renamed client as AdGuard now lists it.
func (a *API) handleClientRename(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeRename(w, r)
	if !ok {
		return
	}
	if err := a.deps.Grants.RenameClient(r.Context(), in.From, in.To); err != nil {
		a.writeRenameError(w, in, err)
		return
	}
	writeJSON(w, http.StatusOK, a.renamedView(r, in.To))
}

// renamedView reads the renamed client back for the response. The rename
// has already happened, so a failed read is logged and answered with what
// is known — the name and its child — rather than an error.
func (a *API) renamedView(r *http.Request, name string) clientView {
	v := clientView{Name: name, IDs: []string{}}
	if children, err := a.deps.Children.ListChildren(r.Context()); err == nil {
		if ref, ok := ownerIndex(children)[name]; ok {
			v.Child = &ref
		}
	} else {
		a.log.Warn("rename client: child read-back failed", "err", err)
	}
	res, err := a.deps.AdGuard.Clients(r.Context())
	if err != nil {
		a.log.Warn("rename client: adguard read-back failed", "err", err)
		return v
	}
	for _, pc := range res.Persistent {
		if pc.Name == name {
			if pc.IDs != nil {
				v.IDs = pc.IDs
			}
			v.UseGlobalBlockedServices = pc.UseGlobalBlockedServices
		}
	}
	return v
}

// writeRenameError maps a rename failure: AdGuard refusing the input is 400
// with its message; unknown from is 404; a to that AdGuard already has, or
// that another child's mapping holds, is 409;
// AdGuard failing is 502; anything else (a store write AdGuard was renamed
// back from) is 500.
func (a *API) writeRenameError(w http.ResponseWriter, in renameRequest, err error) {
	var taken *store.ErrClientTaken
	var rejected *adguard.InputRejectedError
	switch {
	case errors.As(err, &rejected):
		writeError(w, http.StatusBadRequest, CodeBadRequest, "AdGuard Home refused the name: "+rejected.Message)
	case errors.Is(err, adguard.ErrClientNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("no client named %s in AdGuard Home", in.From))
	case errors.Is(err, adguard.ErrClientExists):
		writeError(w, http.StatusConflict, CodeConflict, fmt.Sprintf("a client named %s already exists in AdGuard Home", in.To))
	case errors.As(err, &taken):
		writeError(w, http.StatusConflict, CodeConflict,
			fmt.Sprintf("client %s is already assigned to %s", taken.Client, taken.ChildName))
	case adguardDown(err):
		a.writeAdGuardError(w, "rename client", err)
	default:
		a.log.Error("rename client failed", "from", in.From, "to", in.To, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not rename client")
	}
}
