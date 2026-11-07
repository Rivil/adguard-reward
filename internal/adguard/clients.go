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

// AutoClient is a runtime client AdGuard discovered by itself (ARP, rDNS,
// DHCP, WHOIS, etc/hosts) — a device it has seen, not one a parent named.
// Hostname is AdGuard's "name" for it and may be blank.
type AutoClient struct {
	IP       string
	Hostname string
	Source   string
}

// ClientsResult is what Clients returns: the persistent clients, the auto
// clients from the same /control/clients read, and the global
// blocked-services list persistent clients inherit when
// use_global_blocked_services is set. AutoClients is never nil.
type ClientsResult struct {
	Persistent            []PersistentClient
	AutoClients           []AutoClient
	GlobalBlockedServices []string
}

// Clients reads GET /control/clients and GET /control/blocked_services/get.
// If either call fails the error is returned with no partial result.
func (c *Client) Clients(ctx context.Context) (ClientsResult, error) {
	persistent, auto, err := c.readDoc(ctx)
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
		AutoClients:           auto,
		GlobalBlockedServices: nonNil(global.IDs),
	}, nil
}

// readClients fetches and decodes the persistent clients, keeping each raw
// object for a later write. auto_clients are ignored.
func (c *Client) readClients(ctx context.Context) ([]PersistentClient, error) {
	persistent, _, err := c.readDoc(ctx)
	return persistent, err
}

// readDoc is one GET /control/clients decoded into persistent clients (raw
// objects kept) and auto clients. A null or missing auto_clients is [].
func (c *Client) readDoc(ctx context.Context) ([]PersistentClient, []AutoClient, error) {
	var doc struct {
		Clients     []map[string]json.RawMessage `json:"clients"`
		AutoClients []struct {
			Name   string `json:"name"`
			IP     string `json:"ip"`
			Source string `json:"source"`
		} `json:"auto_clients"`
	}
	if err := c.do(ctx, http.MethodGet, "/control/clients", nil, &doc); err != nil {
		return nil, nil, err
	}
	auto := make([]AutoClient, 0, len(doc.AutoClients))
	for _, a := range doc.AutoClients {
		auto = append(auto, AutoClient{IP: a.IP, Hostname: a.Name, Source: a.Source})
	}
	persistent, err := decodePersistent(doc.Clients)
	if err != nil {
		return nil, nil, err
	}
	return persistent, auto, nil
}

func decodePersistent(list []map[string]json.RawMessage) ([]PersistentClient, error) {
	out := make([]PersistentClient, 0, len(list))
	for _, raw := range list {
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
		return fmt.Errorf("%w: /control/clients: field %q: %w", ErrBadResponse, key, err)
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
		c.warnGlobal(data, clientName)
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

// errSkipUpdate is returned by a mutate closure when the edit would leave the
// client exactly as read; updateClient then returns nil without a POST.
var errSkipUpdate = errors.New("adguard: update is a no-op")

// RemoveBlockedServices takes ids out of the named client's blocked_services
// via the locked read-modify-write: the live list is re-read under the mutex
// and edited in place, so a parent's concurrent edit to an unrelated id
// survives. Ids already absent are ignored; when nothing would change no
// write is issued, so the call costs one GET and at most one POST.
func (c *Client) RemoveBlockedServices(ctx context.Context, clientName string, ids []string) error {
	return c.editBlockedServices(ctx, clientName, func(current []string) []string {
		return slices.DeleteFunc(current, func(id string) bool { return slices.Contains(ids, id) })
	})
}

// AddBlockedServices puts ids into the named client's blocked_services as a
// sorted set-union, under the same locked read-modify-write as Remove and
// with the same no-op skip — a second identical Add writes nothing.
func (c *Client) AddBlockedServices(ctx context.Context, clientName string, ids []string) error {
	return c.editBlockedServices(ctx, clientName, func(current []string) []string {
		return append(current, ids...)
	})
}

// editBlockedServices runs edit over the client's normalised live list and
// writes the normalised result only when it differs from what was read.
func (c *Client) editBlockedServices(ctx context.Context, clientName string, edit func(current []string) []string) error {
	return c.updateClient(ctx, clientName, func(data map[string]json.RawMessage) error {
		var current []string
		if err := decodeField(data, "blocked_services", &current); err != nil {
			return err
		}
		current = normaliseIDs(current)
		next := normaliseIDs(edit(slices.Clone(current)))
		if slices.Equal(current, next) {
			return errSkipUpdate
		}
		c.warnGlobal(data, clientName)
		list, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("adguard: encode blocked_services: %w", err)
		}
		data["blocked_services"] = list
		return nil
	})
}

// warnGlobal logs when a per-client blocked_services write targets a client
// that inherits the global list — AdGuard ignores the per-client list then.
func (c *Client) warnGlobal(data map[string]json.RawMessage, clientName string) {
	if string(data["use_global_blocked_services"]) == "true" {
		c.log.Warn("client uses the global blocked-services list; per-client write may be ignored by AdGuard",
			"client", clientName, "use_global_blocked_services", true)
	}
}

// normaliseIDs sorts and dedups a service-id list, [] for nil.
func normaliseIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return nonNil(slices.Compact(out))
}

// encodeIDs sorts, dedups and marshals a service-id list, [] for nil.
func encodeIDs(ids []string) (json.RawMessage, error) {
	list, err := json.Marshal(normaliseIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("adguard: encode blocked_services: %w", err)
	}
	return list, nil
}

// updateClient is the locked read-modify-write every per-client writer
// shares: re-read /control/clients under c.rmw, copy the named client's raw
// object verbatim, let mutate edit the copy, and POST it back. An unknown
// name is ErrClientNotFound and writes nothing; a mutate that returns
// errSkipUpdate writes nothing and succeeds.
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
		if errors.Is(err, errSkipUpdate) {
			return nil
		}
		return err
	}

	body := struct {
		Name string                     `json:"name"`
		Data map[string]json.RawMessage `json:"data"`
	}{clientName, data}
	return c.do(ctx, http.MethodPost, "/control/clients/update", body, nil)
}
