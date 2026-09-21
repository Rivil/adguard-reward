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
	"strconv"
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
	dirty       bool // clients modified since the fixture was loaded
	requests    []Request
	lastUpdate  []byte
	inFlight    int
	maxInFlight int
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
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) loadClients() {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(Fixture("clients.json"), &doc); err != nil {
		panic("adguardtest: clients.json: " + err.Error())
	}
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
// on the read-modify-write pair (GET /control/clients and POST
// /control/clients/update). A client that serialises its writes never
// exceeds 1.
func (s *Server) MaxInFlightUpdates() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInFlight
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
	rmw := r.URL.Path == "/control/clients" || r.URL.Path == "/control/clients/update"
	if rmw {
		s.inFlight++
		if s.inFlight > s.maxInFlight {
			s.maxInFlight = s.inFlight
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
	case "POST /control/clients/update":
		s.handleUpdate(w, body)
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
	s.clients[i] = data
	s.dirty = true
	s.lastUpdate = bytes.Clone(body)
	w.WriteHeader(http.StatusOK)
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
