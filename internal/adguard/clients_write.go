package adguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
)

// ErrClientExists is returned when another persistent client already has
// the name an add or rename asks for. Nothing is written.
var ErrClientExists = errors.New("adguard: a client with that name already exists")

// ErrIDTaken says which persistent client already holds an id an add asked
// for. Nothing is written.
type ErrIDTaken struct {
	ID    string
	Owner string
}

func (e *ErrIDTaken) Error() string {
	return fmt.Sprintf("adguard: id %q is already used by client %q", e.ID, e.Owner)
}

// InputRejectedError is AdGuard answering 400 to a write: the input was
// refused, AdGuard is not down. Message is AdGuard's own text, so the parent
// sees why. It deliberately does not unwrap to *StatusError, which callers
// treat as "AdGuard unavailable".
type InputRejectedError struct {
	Path    string
	Message string
}

func (e *InputRejectedError) Error() string {
	return fmt.Sprintf("adguard: %s rejected: %s", e.Path, e.Message)
}

// NewClient is a persistent client to create. Blocked nil leaves the client
// on AdGuard's global blocked-services list; non-nil gives it its own list
// (possibly empty) with use_global_blocked_services=false.
type NewClient struct {
	Name    string
	IDs     []string
	Blocked *[]string
}

// AddClient creates a persistent client via POST /control/clients/add,
// under the read-modify-write mutex with a fresh read: a name another client
// has is ErrClientExists and an id another client holds (compared
// canonically) is *ErrIDTaken, both before anything is posted. The client is
// created with use_global_settings=true and an explicit
// use_global_blocked_services; blocked_services is sorted and never null.
func (c *Client) AddClient(ctx context.Context, nc NewClient) error {
	blocked := []string{}
	if nc.Blocked != nil {
		blocked = normaliseIDs(*nc.Blocked)
	}
	body := struct {
		Name                     string   `json:"name"`
		IDs                      []string `json:"ids"`
		UseGlobalSettings        bool     `json:"use_global_settings"`
		UseGlobalBlockedServices bool     `json:"use_global_blocked_services"`
		BlockedServices          []string `json:"blocked_services"`
	}{nc.Name, nonNil(nc.IDs), true, nc.Blocked == nil, blocked}

	c.rmw.Lock()
	defer c.rmw.Unlock()
	clients, err := c.readClients(ctx)
	if err != nil {
		return err
	}
	for _, pc := range clients {
		if pc.Name == nc.Name {
			return fmt.Errorf("%w: %q", ErrClientExists, nc.Name)
		}
		for _, held := range pc.IDs {
			for _, id := range nc.IDs {
				if sameID(held, id) {
					return &ErrIDTaken{ID: id, Owner: pc.Name}
				}
			}
		}
	}
	return rejected(c.do(ctx, http.MethodPost, "/control/clients/add", body, nil))
}

// RenameClient renames a persistent client through the locked
// read-modify-write: every other field goes back exactly as read. A name
// another client has is ErrClientExists and an unknown oldName is
// ErrClientNotFound, both with nothing posted. A same-name rename is a
// no-op.
func (c *Client) RenameClient(ctx context.Context, oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	c.rmw.Lock()
	defer c.rmw.Unlock()
	clients, err := c.readClients(ctx)
	if err != nil {
		return err
	}
	var target *PersistentClient
	for i := range clients {
		switch clients[i].Name {
		case newName:
			return fmt.Errorf("%w: %q", ErrClientExists, newName)
		case oldName:
			target = &clients[i]
		}
	}
	if target == nil {
		return fmt.Errorf("%w: %q", ErrClientNotFound, oldName)
	}
	data := make(map[string]json.RawMessage, len(target.raw))
	for k, v := range target.raw {
		data[k] = v
	}
	name, err := json.Marshal(newName)
	if err != nil {
		return fmt.Errorf("adguard: encode name: %w", err)
	}
	data["name"] = name
	body := struct {
		Name string                     `json:"name"`
		Data map[string]json.RawMessage `json:"data"`
	}{oldName, data}
	return rejected(c.do(ctx, http.MethodPost, "/control/clients/update", body, nil))
}

// AdoptBlockedServices gives the named client its own blocked-services list
// equal to what it effectively blocks now — AdGuard's global list when it
// inherits it, else its own list — plus ids, and clears
// use_global_blocked_services. Nothing it blocks is removed. When the flag
// is already false and the union adds nothing, no write is issued.
func (c *Client) AdoptBlockedServices(ctx context.Context, clientName string, ids []string) error {
	return c.updateClient(ctx, clientName, func(data map[string]json.RawMessage) error {
		var inherits bool
		if err := decodeField(data, "use_global_blocked_services", &inherits); err != nil {
			return err
		}
		var current []string
		if inherits {
			var global struct {
				IDs []string `json:"ids"`
			}
			if err := c.do(ctx, http.MethodGet, "/control/blocked_services/get", nil, &global); err != nil {
				return err
			}
			current = global.IDs
		} else if err := decodeField(data, "blocked_services", &current); err != nil {
			return err
		}
		current = normaliseIDs(current)
		next := normaliseIDs(append(slices.Clone(current), ids...))
		if !inherits && slices.Equal(current, next) {
			return errSkipUpdate
		}
		list, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("adguard: encode blocked_services: %w", err)
		}
		data["blocked_services"] = list
		data["use_global_blocked_services"] = json.RawMessage("false")
		return nil
	})
}

// sameID compares two client ids the way AdGuard matches them: addresses
// canonically, MACs case- and separator-insensitively, anything else
// case-insensitively.
func sameID(a, b string) bool {
	if canonicalIP(a) == canonicalIP(b) {
		return true
	}
	return canonicalMAC(a) == canonicalMAC(b)
}

// rejected turns AdGuard's 400 on a write into *InputRejectedError carrying
// its message; every other error passes through.
func rejected(err error) error {
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusBadRequest {
		return &InputRejectedError{Path: se.Path, Message: se.Snippet}
	}
	return err
}
