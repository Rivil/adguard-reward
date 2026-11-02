// Package adguardtest is an in-process fake of the AdGuard Home /control API,
// shaped after v0.107.x, for tests of the adguard client and of main.
//
// The fixtures under testdata/ were checked against the upstream
// openapi/openapi.yaml (AdGuardHome master, 2026-09-19, v0.107.x line): a
// persistent client carries a flat `blocked_services` string array plus a
// `blocked_services_schedule`; the global GET /control/blocked_services/get
// is `{schedule, ids}`; GET /control/blocked_services/all is
// `{blocked_services: [{icon_svg, id, name, rules, group_id}], groups}`.
// Every icon_svg is the placeholder base64("<svg/>") — AdGuard's real icons
// are GPL assets and are never copied into this repository (rule r-01).
package adguardtest

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

//go:embed testdata/*.json
var fixtures embed.FS

// Fixture returns the embedded bytes of one testdata file, e.g. "clients.json".
// It panics on an unknown name so a typo fails loudly in the test that made it.
func Fixture(name string) []byte {
	b, err := fixtures.ReadFile("testdata/" + name)
	if err != nil {
		panic(fmt.Sprintf("adguardtest: no fixture %q: %v", name, err))
	}
	return b
}

// Options configures a fake server.
type Options struct {
	User string // service credential the fake accepts on basic auth and login
	Pass string
}

// Request is one recorded call. Body is the raw request body, unparsed, so a
// test can assert exactly what the client sent.
type Request struct {
	Method  string
	Path    string
	HasAuth bool // an Authorization header was present
	Body    []byte
}

type forced struct {
	code int
	body []byte
}

// Server is the fake. Every method is safe for concurrent use.
type Server struct {
	srv  *httptest.Server
	user string
	pass string

	mu          sync.Mutex
	authOK      bool
	forced      map[string]forced
	hang        map[string]time.Duration
	updateFault map[string]int             // client name -> forced status on /control/clients/update
	clientsDoc  map[string]json.RawMessage // top-level of clients.json
	clients     []map[string]json.RawMessage
	dirty       bool                       // clients modified since the fixture was loaded
	dhcpDoc     map[string]json.RawMessage // top-level of dhcp_status.json
	dhcpDirty   bool                       // leases replaced since the fixture was loaded
	requests    []Request
	lastUpdate  []byte
	added       [][]byte // raw bodies of accepted POST /control/clients/add
	inFlight    int
	maxInFlight int

	userRules       []string // served as user_rules; nil serves null
	lastSetRules    []byte   // raw body of the most recent accepted set_rules
	filterInFlight  int
	filterMaxFlight int
}

// New starts a fake AdGuard Home. It is closed when t ends.
func New(t testing.TB, opts Options) *Server {
	t.Helper()
	s := &Server{
		user:        opts.User,
		pass:        opts.Pass,
		authOK:      true,
		forced:      map[string]forced{},
		hang:        map[string]time.Duration{},
		updateFault: map[string]int{},
	}
	s.loadClients()
	s.dhcpDoc = decodeDoc("dhcp_status.json")
	if err := json.Unmarshal(decodeDoc("filtering_status.json")["user_rules"], &s.userRules); err != nil {
		panic("adguardtest: filtering_status.json user_rules: " + err.Error())
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func decodeDoc(name string) map[string]json.RawMessage {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(Fixture(name), &doc); err != nil {
		panic("adguardtest: " + name + ": " + err.Error())
	}
	return doc
}

func (s *Server) loadClients() {
	doc := decodeDoc("clients.json")
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(doc["clients"], &list); err != nil {
		panic("adguardtest: clients.json clients: " + err.Error())
	}
	s.clientsDoc = doc
	s.clients = list
}

// URL is the base URL of the fake, without a trailing slash.
func (s *Server) URL() string { return s.srv.URL }

// Close shuts the server down. Safe to call twice.
func (s *Server) Close() { s.srv.Close() }

// SetStatus forces every request to path to answer with code and a short
// plain-text body, bypassing auth and the normal handler. A 3xx carries a
// Location header pointing back at /control/status so a following client
// would have somewhere to go.
func (s *Server) SetStatus(path string, code int) {
	s.SetResponse(path, code, []byte("forced status "+strconv.Itoa(code)))
}

// SetResponse is SetStatus with an explicit body. A nil body clears the
// override.
func (s *Server) SetResponse(path string, code int, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if body == nil {
		delete(s.forced, path)
		return
	}
	s.forced[path] = forced{code: code, body: body}
}

// Hang delays every response on path by d (or until the request context is
// cancelled). Zero clears it.
func (s *Server) Hang(path string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d == 0 {
		delete(s.hang, path)
		return
	}
	s.hang[path] = d
}

// SetUpdateStatus makes POST /control/clients/update answer code for the
// named client only, after the body has been parsed and before anything is
// stored, so a multi-client write can fail on its second client while the
// first went through. 0 clears it. Other clients are unaffected.
func (s *Server) SetUpdateStatus(name string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if code == 0 {
		delete(s.updateFault, name)
		return
	}
	s.updateFault[name] = code
}

// SetAuth controls whether the configured credential is accepted. When false
// every authenticated endpoint answers 401 even with the right password —
// the "credential revoked in AdGuard" case.
func (s *Server) SetAuth(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authOK = ok
}

// MutateClient edits the stored client named name in place, simulating an
// edit made in AdGuard's own UI between a client's read and its write.
// It panics if no such client exists.
func (s *Server) MutateClient(name string, fn func(c map[string]json.RawMessage)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(name)
	if i < 0 {
		panic("adguardtest: MutateClient: no client " + name)
	}
	fn(s.clients[i])
	s.dirty = true
}

// BlockedServices returns the stored blocked_services of the named client as
// it would be served — a null or missing list reads as []. ok is false for a
// client the fake does not have.
func (s *Server) BlockedServices(name string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(name)
	if i < 0 {
		return nil, false
	}
	var ids []string
	if raw, ok := s.clients[i]["blocked_services"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &ids); err != nil {
			panic("adguardtest: BlockedServices: " + err.Error())
		}
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, true
}

// RemoveClient deletes the named client, simulating a device removed in
// AdGuard's UI: later GETs omit it and a POST /control/clients/update for it
// answers 400. It panics if no such client exists.
func (s *Server) RemoveClient(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(name)
	if i < 0 {
		panic("adguardtest: RemoveClient: no client " + name)
	}
	s.clients = append(s.clients[:i:i], s.clients[i+1:]...)
	s.dirty = true
}

// AutoClient is one runtime client as GET /control/clients lists it under
// auto_clients. Source is AdGuard's label: "ARP", "rDNS", "DHCP", "WHOIS",
// "etc/hosts".
type AutoClient struct {
	Name   string
	IP     string
	Source string
}

// SetAutoClients replaces the auto_clients served by GET /control/clients.
// A nil or empty list serves "auto_clients": [], never null. Persistent
// clients are untouched.
func (s *Server) SetAutoClients(list []AutoClient) {
	type wire struct {
		Name      string            `json:"name"`
		IP        string            `json:"ip"`
		Source    string            `json:"source"`
		WhoisInfo map[string]string `json:"whois_info"`
	}
	out := make([]wire, 0, len(list))
	for _, a := range list {
		out = append(out, wire{Name: a.Name, IP: a.IP, Source: a.Source, WhoisInfo: map[string]string{}})
	}
	b := mustJSON(out)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clientsDoc["auto_clients"] = b
	s.dirty = true
}

// Lease is one DHCP lease as GET /control/dhcp/status lists it.
type Lease struct {
	MAC      string
	IP       string
	Hostname string
}

// SetLeases replaces the dynamic leases and static_leases served by GET
// /control/dhcp/status. nil serves []. Dynamic leases carry a fixed
// expires timestamp; static ones carry none, as in AdGuard.
func (s *Server) SetLeases(dynamic, static []Lease) {
	type dyn struct {
		MAC      string `json:"mac"`
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
		Expires  string `json:"expires"`
	}
	type stat struct {
		MAC      string `json:"mac"`
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
	}
	d := make([]dyn, 0, len(dynamic))
	for _, l := range dynamic {
		d = append(d, dyn{MAC: l.MAC, IP: l.IP, Hostname: l.Hostname, Expires: "2026-11-02T00:00:00Z"})
	}
	st := make([]stat, 0, len(static))
	for _, l := range static {
		st = append(st, stat(l))
	}
	db, sb := mustJSON(d), mustJSON(st)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dhcpDoc["leases"] = db
	s.dhcpDoc["static_leases"] = sb
	s.dhcpDirty = true
}

// AddedClients returns the raw body of every accepted POST
// /control/clients/add, in arrival order. Rejected adds are not listed.
func (s *Server) AddedClients() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.added))
	for i, b := range s.added {
		out[i] = bytes.Clone(b)
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("adguardtest: encode: " + err.Error())
	}
	return b
}

// CountRequests returns how many recorded requests match method and path
// exactly.
func (s *Server) CountRequests(method, path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// Requests returns a copy of every recorded request in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// LastUpdate returns the raw body of the most recent POST
// /control/clients/update, or nil.
func (s *Server) LastUpdate() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.lastUpdate)
}

// MaxInFlightUpdates reports the highest number of concurrent requests seen
// on the read-modify-write set (GET /control/clients, POST
// /control/clients/update and POST /control/clients/add). A client that
// serialises its writes never exceeds 1.
func (s *Server) MaxInFlightUpdates() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInFlight
}

// SetUserRules replaces the custom filtering rules served as user_rules by
// GET /control/filtering/status. nil serves null, as an AdGuard that never
// had a rule may.
func (s *Server) SetUserRules(rules []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userRules = slices.Clone(rules)
}

// UserRules returns the stored custom filtering rules, nil when null.
func (s *Server) UserRules() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.userRules)
}

// LastSetRules returns the raw body of the most recent accepted POST
// /control/filtering/set_rules, or nil.
func (s *Server) LastSetRules() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.lastSetRules)
}

// MaxInFlightFiltering reports the highest number of concurrent requests
// seen on GET /control/filtering/status and POST /control/filtering/set_rules.
// A client that serialises its user_rules edits never exceeds 1.
func (s *Server) MaxInFlightFiltering() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filterMaxFlight
}

func (s *Server) indexOf(name string) int {
	for i, c := range s.clients {
		var n string
		if json.Unmarshal(c["name"], &n) == nil && n == name {
			return i
		}
	}
	return -1
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_, hasAuth := r.Header["Authorization"]

	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, HasAuth: hasAuth, Body: body})
	delay := s.hang[r.URL.Path]
	f, isForced := s.forced[r.URL.Path]
	rmw := r.URL.Path == "/control/clients" || r.URL.Path == "/control/clients/update" ||
		r.URL.Path == "/control/clients/add"
	if rmw {
		s.inFlight++
		if s.inFlight > s.maxInFlight {
			s.maxInFlight = s.inFlight
		}
	}
	filtering := r.URL.Path == "/control/filtering/status" || r.URL.Path == "/control/filtering/set_rules"
	if filtering {
		s.filterInFlight++
		if s.filterInFlight > s.filterMaxFlight {
			s.filterMaxFlight = s.filterInFlight
		}
	}
	s.mu.Unlock()
	if rmw {
		defer func() {
			s.mu.Lock()
			s.inFlight--
			s.mu.Unlock()
		}()
	}
	if filtering {
		defer func() {
			s.mu.Lock()
			s.filterInFlight--
			s.mu.Unlock()
		}()
		// Widen the window a lost mutex would race through.
		time.Sleep(time.Millisecond)
	}

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}

	if isForced {
		if f.code >= 300 && f.code < 400 {
			w.Header().Set("Location", "/control/status")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(f.code)
		_, _ = w.Write(f.body)
		return
	}

	if r.URL.Path == "/control/login" {
		s.handleLogin(w, r, body)
		return
	}

	user, pass, ok := r.BasicAuth()
	s.mu.Lock()
	authOK := s.authOK
	s.mu.Unlock()
	if !ok || !authOK || user != s.user || pass != s.pass {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method + " " + r.URL.Path {
	case "GET /control/status":
		writeJSON(w, Fixture("status.json"))
	case "GET /control/clients":
		writeJSON(w, s.clientsJSON())
	case "GET /control/blocked_services/get":
		writeJSON(w, Fixture("blocked_services_get.json"))
	case "GET /control/blocked_services/all":
		writeJSON(w, Fixture("blocked_services_all.json"))
	case "GET /control/dhcp/status":
		writeJSON(w, s.dhcpJSON())
	case "POST /control/clients/update":
		s.handleUpdate(w, body)
	case "POST /control/clients/add":
		s.handleAdd(w, body)
	case "GET /control/filtering/status":
		writeJSON(w, s.filteringJSON())
	case "POST /control/filtering/set_rules":
		s.handleSetRules(w, body)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request, body []byte) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	if in.Name != s.user || in.Password != s.pass {
		http.Error(w, "invalid username or password", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "agh_session", Value: "fake-session", Path: "/", HttpOnly: true})
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUpdate(w http.ResponseWriter, body []byte) {
	var in struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "invalid client update: "+err.Error(), http.StatusBadRequest)
		return
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(in.Data, &data); err != nil || data == nil {
		http.Error(w, "invalid client data", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if code, ok := s.updateFault[in.Name]; ok {
		http.Error(w, "forced status "+strconv.Itoa(code)+" for "+in.Name, code)
		return
	}
	i := s.indexOf(in.Name)
	if i < 0 {
		http.Error(w, "client not found: "+in.Name, http.StatusBadRequest)
		return
	}
	var newName string
	if json.Unmarshal(data["name"], &newName) == nil && newName != in.Name && s.indexOf(newName) >= 0 {
		http.Error(w, fmt.Sprintf("another client uses the same name %q", newName), http.StatusBadRequest)
		return
	}
	s.clients[i] = data
	s.dirty = true
	s.lastUpdate = bytes.Clone(body)
	w.WriteHeader(http.StatusOK)
}

// handleAdd stores a new persistent client the way AdGuard's
// /control/clients/add does: the body is the client object itself. A blank
// name, no ids, a name another client has, or an id another client holds
// answers 400 and stores nothing.
func (s *Server) handleAdd(w http.ResponseWriter, body []byte) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(body, &data); err != nil || data == nil {
		http.Error(w, "invalid client: bad json", http.StatusBadRequest)
		return
	}
	var name string
	var ids []string
	_ = json.Unmarshal(data["name"], &name)
	_ = json.Unmarshal(data["ids"], &ids)
	if name == "" {
		http.Error(w, "invalid client: empty name", http.StatusBadRequest)
		return
	}
	if len(ids) == 0 {
		http.Error(w, "invalid client: no ids", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexOf(name) >= 0 {
		http.Error(w, fmt.Sprintf("another client uses the same name %q", name), http.StatusBadRequest)
		return
	}
	for _, c := range s.clients {
		var owner string
		var held []string
		_ = json.Unmarshal(c["name"], &owner)
		_ = json.Unmarshal(c["ids"], &held)
		for _, h := range held {
			for _, id := range ids {
				if strings.EqualFold(h, id) {
					http.Error(w, fmt.Sprintf("another client %q uses the same id %q", owner, id), http.StatusBadRequest)
					return
				}
			}
		}
	}
	s.clients = append(s.clients, data)
	s.dirty = true
	s.added = append(s.added, bytes.Clone(body))
	w.WriteHeader(http.StatusOK)
}

// filteringJSON serves filtering_status.json with the stored user_rules.
func (s *Server) filteringJSON() []byte {
	doc := decodeDoc("filtering_status.json")
	s.mu.Lock()
	doc["user_rules"] = mustJSON(s.userRules)
	s.mu.Unlock()
	return mustJSON(doc)
}

// handleSetRules stores the rules of POST /control/filtering/set_rules
// verbatim. An unknown field or a body that is not {"rules": [...]} answers
// 400; a null rules list is stored as null, as AdGuard would clear them.
func (s *Server) handleSetRules(w http.ResponseWriter, body []byte) {
	var in struct {
		Rules []string `json:"rules"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		http.Error(w, "invalid set_rules request: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userRules = in.Rules
	s.lastSetRules = bytes.Clone(body)
	w.WriteHeader(http.StatusOK)
}

// dhcpJSON serves dhcp_status.json verbatim until SetLeases changed it.
func (s *Server) dhcpJSON() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dhcpDirty {
		return Fixture("dhcp_status.json")
	}
	return mustJSON(s.dhcpDoc)
}

// clientsJSON serves the fixture bytes verbatim until something changed,
// then a re-encoded document carrying the stored client objects as-is.
func (s *Server) clientsJSON() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return Fixture("clients.json")
	}
	list, err := json.Marshal(s.clients)
	if err != nil {
		panic("adguardtest: encode clients: " + err.Error())
	}
	doc := make(map[string]json.RawMessage, len(s.clientsDoc))
	for k, v := range s.clientsDoc {
		doc[k] = v
	}
	doc["clients"] = list
	out, err := json.Marshal(doc)
	if err != nil {
		panic("adguardtest: encode clients doc: " + err.Error())
	}
	return out
}

func writeJSON(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}
