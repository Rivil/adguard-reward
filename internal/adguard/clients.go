package adguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
)

// ErrClientNotFound is returned by SetBlockedServices when no persistent
// client has the given name. Nothing is written in that case.
var ErrClientNotFound = errors.New("adguard: client not found")

// PersistentClient is the subset of a /control/clients entry the app uses.
// The full object is kept verbatim so a write can carry every other field
// back exactly as read.
type PersistentClient struct {
	Name                     string
	IDs                      []string
	BlockedServices          []string
	UseGlobalBlockedServices bool

	raw map[string]json.RawMessage
}

// ClientsResult is what Clients returns: the persistent clients plus the
// global blocked-services list they inherit when use_global_blocked_services
// is set.
type ClientsResult struct {
	Persistent            []PersistentClient
	GlobalBlockedServices []string
}

// Clients reads GET /control/clients and GET /control/blocked_services/get.
// If either call fails the error is returned with no partial result.
func (c *Client) Clients(ctx context.Context) (ClientsResult, error) {
	persistent, err := c.readClients(ctx)
	if err != nil {
		return ClientsResult{}, err
	}
	var global struct {
		IDs []string `json:"ids"`
	}
	if err := c.do(ctx, http.MethodGet, "/control/blocked_services/get", nil, &global); err != nil {
		return ClientsResult{}, err
	}
	return ClientsResult{
		Persistent:            persistent,
		GlobalBlockedServices: nonNil(global.IDs),
	}, nil
}

// readClients fetches and decodes the persistent clients, keeping each raw
// object for a later write. auto_clients are ignored.
func (c *Client) readClients(ctx context.Context) ([]PersistentClient, error) {
	var doc struct {
		Clients []map[string]json.RawMessage `json:"clients"`
	}
	if err := c.do(ctx, http.MethodGet, "/control/clients", nil, &doc); err != nil {
		return nil, err
	}
	out := make([]PersistentClient, 0, len(doc.Clients))
	for _, raw := range doc.Clients {
		pc := PersistentClient{raw: raw}
		if err := decodeField(raw, "name", &pc.Name); err != nil {
			return nil, err
		}
		if err := decodeField(raw, "ids", &pc.IDs); err != nil {
			return nil, err
		}
		if err := decodeField(raw, "blocked_services", &pc.BlockedServices); err != nil {
			return nil, err
		}
		if err := decodeField(raw, "use_global_blocked_services", &pc.UseGlobalBlockedServices); err != nil {
			return nil, err
		}
		pc.IDs = nonNil(pc.IDs)
		pc.BlockedServices = nonNil(pc.BlockedServices)
		out = append(out, pc)
	}
	return out, nil
}

// decodeField unmarshals one key of a raw object; a missing or null key
// leaves dst at its zero value.
func decodeField(raw map[string]json.RawMessage, key string, dst any) error {
	v, ok := raw[key]
	if !ok || string(v) == "null" {
		return nil
	}
	if err := json.Unmarshal(v, dst); err != nil {
		return fmt.Errorf("adguard: /control/clients: field %q: %w", key, err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// SetBlockedServices replaces the blocked_services list of the named client
// via read-modify-write on POST /control/clients/update: the client is
// re-read under a mutex, every other field goes back exactly as read, and
// only blocked_services changes. ids are sorted and deduplicated; nil or
// empty is written as [] so AdGuard never sees null.
func (c *Client) SetBlockedServices(ctx context.Context, clientName string, ids []string) error {
	list, err := encodeIDs(ids)
	if err != nil {
		return err
	}
	return c.updateClient(ctx, clientName, func(data map[string]json.RawMessage) error {
		if string(data["use_global_blocked_services"]) == "true" {
			c.log.Warn("client uses the global blocked-services list; per-client write may be ignored by AdGuard",
				"client", clientName, "use_global_blocked_services", true)
		}
		data["blocked_services"] = list
		return nil
	})
}

// MigrateFromGlobal moves the named client off AdGuard's global
// blocked-services list in one write: blocked_services becomes ids (sorted,
// deduplicated, never null) and use_global_blocked_services is cleared.
// Nothing else on the client changes and the global list itself is never
// touched — /control/blocked_services/set is not called.
func (c *Client) MigrateFromGlobal(ctx context.Context, clientName string, ids []string) error {
	list, err := encodeIDs(ids)
	if err != nil {
		return err
	}
	return c.updateClient(ctx, clientName, func(data map[string]json.RawMessage) error {
		data["blocked_services"] = list
		data["use_global_blocked_services"] = json.RawMessage("false")
		return nil
	})
}

// encodeIDs sorts, dedups and marshals a service-id list, [] for nil.
func encodeIDs(ids []string) (json.RawMessage, error) {
	normalised := slices.Clone(ids)
	slices.Sort(normalised)
	normalised = slices.Compact(normalised)
	list, err := json.Marshal(nonNil(normalised))
	if err != nil {
		return nil, fmt.Errorf("adguard: encode blocked_services: %w", err)
	}
	return list, nil
}

// updateClient is the locked read-modify-write every per-client writer
// shares: re-read /control/clients under c.rmw, copy the named client's raw
// object verbatim, let mutate edit the copy, and POST it back. An unknown
// name is ErrClientNotFound and writes nothing.
func (c *Client) updateClient(ctx context.Context, clientName string, mutate func(data map[string]json.RawMessage) error) error {
	c.rmw.Lock()
	defer c.rmw.Unlock()

	clients, err := c.readClients(ctx)
	if err != nil {
		return err
	}
	var target *PersistentClient
	for i := range clients {
		if clients[i].Name == clientName {
			target = &clients[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("%w: %q", ErrClientNotFound, clientName)
	}

	data := make(map[string]json.RawMessage, len(target.raw)+1)
	for k, v := range target.raw {
		data[k] = v
	}
	if err := mutate(data); err != nil {
		return err
	}

	body := struct {
		Name string                     `json:"name"`
		Data map[string]json.RawMessage `json:"data"`
	}{clientName, data}
	return c.do(ctx, http.MethodPost, "/control/clients/update", body, nil)
}
