package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
	"github.com/Rivil/adguard-reward/internal/rules"
)

const (
	clientUpdatePath = "/control/clients/update"
	clientAddPath    = "/control/clients/add"
)

// importHarness is a harness whose engine a test can reconcile directly, the
// way the ticker would, over the same store, fake and clock as the API.
type importHarness struct {
	*harness
	eng    *grants.Engine
	client *adguard.Client
}

func newImportHarness(t *testing.T) *importHarness {
	t.Helper()
	return newImportHarnessOver(t, func(c *adguard.Client) grants.AdGuard { return c })
}

// newImportHarnessOver is newImportHarness with the engine's AdGuard wrapped
// by wrap; the API keeps the plain client.
func newImportHarnessOver(t *testing.T, wrap func(*adguard.Client) grants.AdGuard) *importHarness {
	t.Helper()
	h := newHarness(t, nil, nil)
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client, err := adguard.New(h.fake.URL(), fakeUser, fakePass, adguard.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	eng := grants.New(h.store, wrap(client), grants.Options{Now: h.clock.Now, ApplyTimeout: harnessApplyTimeout, Log: log})
	t.Cleanup(eng.Close)
	cfg := ratelimit.Defaults()
	cfg.Now = h.clock.Now
	h.h = New(Deps{
		AdGuard:        client,
		ClientIP:       ClientIP(nil),
		Log:            log,
		Auth:           auth.New(h.store, auth.Options{Now: h.clock.Now, Log: log, ErrorWriter: UnauthorizedWriter}),
		Limiter:        ratelimit.New(cfg),
		Sessions:       h.store,
		Children:       h.store,
		Grants:         eng,
		Buttons:        h.store,
		CustomServices: h.store,
	}).Handler()
	return &importHarness{harness: h, eng: eng, client: client}
}

// clientsFile is a valid file holding only children.
func clientsFile(children ...fileChild) setupFile {
	return setupFile{
		Format:         setupFileFormat,
		Version:        setupFileVersion,
		Children:       children,
		CustomServices: []fileService{},
		Buttons:        []fileButton{},
		Toggles:        []fileToggle{},
	}
}

func (h *importHarness) importOK(cookie string, f setupFile) importResult {
	h.t.Helper()
	w := h.postImport(cookie, fileBody(h.t, f))
	if w.Code != http.StatusOK {
		h.t.Fatalf("import = %d %s, want 200", w.Code, w.Body)
	}
	var out importResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		h.t.Fatalf("decode %s: %v", w.Body, err)
	}
	return out
}

func (h *importHarness) reconcile() {
	h.t.Helper()
	if _, err := h.eng.Reconcile(context.Background()); err != nil {
		h.t.Fatalf("Reconcile: %v", err)
	}
}

func (h *importHarness) usesGlobal(name string) bool {
	h.t.Helper()
	res, err := h.client.Clients(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range res.Persistent {
		if c.Name == name {
			return c.UseGlobalBlockedServices
		}
	}
	h.t.Fatalf("fake has no client %q", name)
	return false
}

// updatedClients names each client update the fake saw, in order.
func (h *importHarness) updatedClients() []string {
	h.t.Helper()
	out := []string{}
	for _, r := range h.fake.Requests() {
		if r.Method != http.MethodPost || r.Path != clientUpdatePath {
			continue
		}
		var u struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(r.Body, &u); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, u.Name)
	}
	return out
}

func wantLine(t *testing.T, lines []string, prefix string, wants ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.HasPrefix(l, prefix) {
			continue
		}
		for _, want := range wants {
			if !strings.Contains(l, want) {
				t.Fatalf("line %q does not name %q", l, want)
			}
		}
		return
	}
	t.Fatalf("no line starts %q in %q", prefix, lines)
}

func noLine(t *testing.T, lines []string, prefix string) {
	t.Helper()
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			t.Fatalf("line %q is present in %q", l, lines)
		}
	}
}

// awkwardRules surround an empty region with everything a careless rewrite
// could disturb: blank lines, trailing spaces, non-ASCII and near-miss markers.
func awkwardRules() (before, after []string) {
	before = []string{"! parent comment", "", "||ads.example^   ", "||żółć.example^", "! adguard-reward begin ", ""}
	after = []string{"", "! adguard-reward-end", "@@||allowed.example^  ", "! trailing comment"}
	return before, after
}

func servicesFile() setupFile {
	f := clientsFile(fileChild{Name: "Ada", Clients: []string{"Kid phone", "Kid tablet"}})
	f.CustomServices = []fileService{{Name: "Extras", Domains: []string{"extras.example"}}}
	return f
}

func TestImport_NonEmptyNoAdoption(t *testing.T) {
	h := newImportHarness(t)
	h.seedChild("Existing")
	cookie := h.loginOK()
	posts := h.adguardPosts()
	f := clientsFile(fileChild{Name: "Ada", Clients: []string{"Kid tablet"}})
	wantProblem(t, h.postImport(cookie, fileBody(t, f)), http.StatusConflict, CodeConflict, "children")
	if got := h.adguardPosts(); got != posts {
		t.Fatalf("AdGuard saw %d POSTs, want none", got-posts)
	}
}

func TestImport_AdGuardFailure(t *testing.T) {
	h := newImportHarness(t)
	cookie := h.loginOK()
	if slices.Contains(h.blocked("Kid phone"), "roblox") {
		t.Fatal("Kid phone already blocks roblox: the addition would prove nothing")
	}
	h.fake.SetUpdateStatus("Kid tablet", http.StatusInternalServerError)
	f := clientsFile(fileChild{Name: "Ada", Clients: []string{"Kid phone", "Kid tablet"}})

	wantProblem(t, h.postImport(cookie, fileBody(t, f)),
		http.StatusBadGateway, CodeAdGuardUnavailable, "Kid tablet")
	h.wantNoSetup()
	if got := h.blocked("Kid phone"); !slices.Contains(got, "roblox") {
		t.Fatalf("Kid phone blocks %v, want it to keep its addition of roblox", got)
	}

	h.fake.SetUpdateStatus("Kid tablet", 0)
	h.importOK(cookie, f)
}

func TestImport_ClientsReadDown(t *testing.T) {
	// AdoptClients reads clients too, but returns early with none to adopt, so
	// only the client-less file proves the import's own read is checked.
	cases := []struct {
		name    string
		clients []string
	}{
		{"with clients", []string{"Kid phone"}},
		{"no clients", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newImportHarness(t)
			cookie := h.loginOK()
			h.fake.SetStatus("/control/clients", http.StatusBadGateway)
			wantProblem(t, h.postImport(cookie, fileBody(t, clientsFile(fileChild{Name: "Ada", Clients: tc.clients}))),
				http.StatusBadGateway, CodeAdGuardUnavailable)
			h.wantNoSetup()
		})
	}
}

// adoptReadFails fails the engine's clients read while fail is set, with an
// error adguardDown does not match. The import's own read goes through the
// API's client, so only the read inside AdoptClients fails.
type adoptReadFails struct {
	*adguard.Client
	fail bool
}

func (a *adoptReadFails) Clients(ctx context.Context) (adguard.ClientsResult, error) {
	if a.fail {
		return adguard.ClientsResult{}, fmt.Errorf("adguard: GET /control/clients: decode: %w", io.ErrUnexpectedEOF)
	}
	return a.Client.Clients(ctx)
}

func TestImport_AdoptClientsReadFails(t *testing.T) {
	ag := &adoptReadFails{fail: true}
	h := newImportHarnessOver(t, func(c *adguard.Client) grants.AdGuard {
		ag.Client = c
		return ag
	})
	cookie := h.loginOK()
	f := clientsFile(fileChild{Name: "Ada", Clients: []string{"Kid phone"}})

	wantProblem(t, h.postImport(cookie, fileBody(t, f)), http.StatusBadGateway, CodeAdGuardUnavailable)
	h.wantNoSetup()
	if got := h.updatedClients(); len(got) != 0 {
		t.Fatalf("updates = %v, want none", got)
	}

	ag.fail = false
	h.importOK(cookie, f)
}

func TestImport_MissingClient(t *testing.T) {
	h := newImportHarness(t)
	got := h.importOK(h.loginOK(), clientsFile(
		fileChild{Name: "Ada", Clients: []string{"Gone phone", "Kid tablet"}},
		fileChild{Name: "Ben", Clients: []string{"Gone phone"}},
	))
	if want := []string{"Gone phone"}; !reflect.DeepEqual(got.MissingClients, want) {
		t.Fatalf("missing_clients = %v, want %v", got.MissingClients, want)
	}
	children, err := h.store.ListChildren(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(children[0].Clients, "Gone phone") || !slices.Contains(children[1].Clients, "Gone phone") {
		t.Fatalf("children = %+v, want both keeping Gone phone", children)
	}
	if n := h.fake.CountRequests(http.MethodPost, clientAddPath); n != 0 {
		t.Fatalf("%d client adds, want none", n)
	}
}

func TestImport_SharedClientAdopted(t *testing.T) {
	h := newImportHarness(t)
	if !h.usesGlobal("Kid tablet") {
		t.Fatal("Kid tablet must start on the global list")
	}
	h.importOK(h.loginOK(), clientsFile(
		fileChild{Name: "Ada", Clients: []string{"Kid tablet"}},
		fileChild{Name: "Ben", Clients: []string{"Kid tablet"}},
	))
	if h.usesGlobal("Kid tablet") {
		t.Fatal("Kid tablet still inherits the global list")
	}
	if got, want := h.updatedClients(), []string{"Kid tablet"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("updates = %v, want %v", got, want)
	}
}

func TestImport_RulesOutsideMarkersUntouched(t *testing.T) {
	h := newImportHarness(t)
	before, after := awkwardRules()
	h.fake.SetUserRules(slices.Concat(before, []string{rules.BeginMarker, rules.EndMarker}, after))

	if got := h.importOK(h.loginOK(), servicesFile()); !got.RulesApplied {
		t.Fatal("rules_applied = false, want true")
	}

	all := h.fake.UserRules()
	begin, end := slices.Index(all, rules.BeginMarker), slices.Index(all, rules.EndMarker)
	if !reflect.DeepEqual(all[:begin], before) {
		t.Fatalf("lines before the markers = %q, want %q", all[:begin], before)
	}
	if !reflect.DeepEqual(all[end+1:], after) {
		t.Fatalf("lines after the markers = %q, want %q", all[end+1:], after)
	}
	wantLine(t, all[begin+1:end], "||extras.example^$client=", "Kid phone", "Kid tablet")
}

func TestImport_RulesNoMarkers(t *testing.T) {
	h := newImportHarness(t)
	parent, trailing := awkwardRules()
	lines := slices.Concat(parent, trailing)
	h.fake.SetUserRules(lines)

	h.importOK(h.loginOK(), servicesFile())

	all := h.fake.UserRules()
	if !reflect.DeepEqual(all[:len(lines)], lines) {
		t.Fatalf("parent lines = %q, want %q", all[:len(lines)], lines)
	}
	if all[len(lines)] != rules.BeginMarker {
		t.Fatalf("region starts %q, want it appended straight after the parent's lines", all[len(lines)])
	}
	wantLine(t, managedRegion(h.harness), "||extras.example^$client=", "Kid phone")
}

func TestImport_CustomServiceGrants(t *testing.T) {
	h := newImportHarness(t)
	h.pushIDsForward(4)
	before, after := awkwardRules()
	h.fake.SetUserRules(slices.Concat(before, []string{rules.BeginMarker, rules.EndMarker}, after))
	cookie := h.loginOK()
	h.importOK(cookie, servicesFile())

	services := listCustom(t, h.harness, cookie)
	if len(services) != 1 || services[0].ID != 5 {
		t.Fatalf("services = %+v, want Extras under the fresh id 5", services)
	}
	children, err := h.store.ListChildren(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	grant := h.grantOK(cookie, children[0].ID, []string{services[0].ServiceID}, 3600)
	wantLine(t, managedRegion(h.harness), "@@||extras.example^$client=", "Kid phone", "Kid tablet")

	h.endGrant(cookie, grant.ID)
	noLine(t, managedRegion(h.harness), "@@||extras.example^")
	wantLine(t, managedRegion(h.harness), "||extras.example^$client=", "Kid phone")

	all := h.fake.UserRules()
	begin, end := slices.Index(all, rules.BeginMarker), slices.Index(all, rules.EndMarker)
	if !reflect.DeepEqual(all[:begin], before) || !reflect.DeepEqual(all[end+1:], after) {
		t.Fatalf("outside lines changed: %q", all)
	}
}

func TestImport_RulesWriteFails(t *testing.T) {
	h := newImportHarness(t)
	cookie := h.loginOK()
	h.fake.SetStatus(setRulesPath, http.StatusInternalServerError)

	got := h.importOK(cookie, servicesFile())
	if got.RulesApplied {
		t.Fatal("rules_applied = true after a failed write")
	}
	if rows := listCustom(t, h.harness, cookie); len(rows) != 1 {
		t.Fatalf("services = %+v, want the stored row to stand", rows)
	}
	if region := managedRegion(h.harness); region != nil {
		t.Fatalf("region = %q, want none yet", region)
	}

	updates := h.fake.CountRequests(http.MethodPost, clientUpdatePath)
	h.fake.SetResponse(setRulesPath, 0, nil)
	h.reconcile()
	wantLine(t, managedRegion(h.harness), "||extras.example^$client=", "Kid phone", "Kid tablet")
	if n := h.fake.CountRequests(http.MethodPost, clientUpdatePath); n != updates {
		t.Fatalf("Reconcile made %d client writes, want no re-adoption", n-updates)
	}
}

func TestImport_MalformedRegion(t *testing.T) {
	h := newImportHarness(t)
	malformed := []string{rules.BeginMarker, "||keep.example^", rules.BeginMarker, rules.EndMarker}
	h.fake.SetUserRules(malformed)

	got := h.importOK(h.loginOK(), servicesFile())
	if got.RulesApplied {
		t.Fatal("rules_applied = true over a malformed region")
	}
	if all := h.fake.UserRules(); !reflect.DeepEqual(all, malformed) {
		t.Fatalf("user rules = %q, want %q untouched", all, malformed)
	}
}

func TestImport_TogglesConverge(t *testing.T) {
	h := newImportHarness(t)
	h.pushIDsForward(3)
	if !h.usesGlobal("Kid tablet") {
		t.Fatal("Kid tablet must start on the global list")
	}
	phoneBefore := h.blocked("Kid phone")

	f := clientsFile(
		fileChild{Name: "Ada", Clients: []string{"Kid phone", "Old laptop"}},
		fileChild{Name: "Ben", Clients: []string{"Kid tablet"}},
	)
	f.CustomServices = []fileService{{Name: "Extras", Domains: []string{"extras.example"}}}
	f.Toggles = []fileToggle{
		{Child: "Ben", Service: "custom:Extras", State: "unblocked"},
		{Child: "Ben", Service: "tiktok", State: "unblocked"},
		{Child: "Ben", Service: "youtube", State: "blocked"},
	}
	h.importOK(h.loginOK(), f)
	h.reconcile()

	tablet := h.blocked("Kid tablet")
	if !slices.Contains(tablet, "youtube") {
		t.Fatalf("Kid tablet blocks %v, want youtube blocked", tablet)
	}
	if slices.Contains(tablet, "tiktok") {
		t.Fatalf("Kid tablet blocks %v, want tiktok open", tablet)
	}
	phone := h.blocked("Kid phone")
	if !slices.Contains(phone, "tiktok") {
		t.Fatalf("Kid phone blocks %v, want Ben's unblock not to reach it (was %v)", phone, phoneBefore)
	}
	// Kid phone already blocks youtube, so only this client shows a leak of
	// Ben's block onto Ada.
	if laptop := h.blocked("Old laptop"); slices.Contains(laptop, "youtube") {
		t.Fatalf("Old laptop blocks %v, want Ben's block not to reach it", laptop)
	}
	region := managedRegion(h.harness)
	wantLine(t, region, "@@||extras.example^$client=", "Kid tablet")
	for _, l := range region {
		if strings.HasPrefix(l, "@@") && (strings.Contains(l, "Kid phone") || strings.Contains(l, "Old laptop")) {
			t.Fatalf("exception %q reaches Ada's clients", l)
		}
	}
}

func TestImport_ToggleEdges(t *testing.T) {
	t.Run("permanent unblock of a global service", func(t *testing.T) {
		h := newImportHarness(t)
		f := clientsFile(fileChild{Name: "Ada", Clients: []string{"Kid tablet"}})
		f.Toggles = []fileToggle{{Child: "Ada", Service: "tiktok", State: "unblocked"}}
		h.importOK(h.loginOK(), f)
		h.reconcile()
		// A client left on the global list reads as blocking nothing, so the
		// adoption has to be proved before the empty-looking list means open.
		if h.usesGlobal("Kid tablet") {
			t.Fatal("Kid tablet still inherits the global list")
		}
		got := h.blocked("Kid tablet")
		if slices.Contains(got, "tiktok") {
			t.Fatalf("Kid tablet blocks %v, want tiktok gone", got)
		}
		if !slices.Contains(got, "roblox") {
			t.Fatalf("Kid tablet blocks %v, want the rest of the global list kept", got)
		}
	})
	t.Run("opposite toggles on a shared client leave it open", func(t *testing.T) {
		h := newImportHarness(t)
		f := clientsFile(
			fileChild{Name: "Ada", Clients: []string{"Kid tablet"}},
			fileChild{Name: "Ben", Clients: []string{"Kid tablet"}},
		)
		f.Toggles = []fileToggle{
			{Child: "Ada", Service: "youtube", State: "blocked"},
			{Child: "Ben", Service: "youtube", State: "unblocked"},
		}
		h.importOK(h.loginOK(), f)
		h.reconcile()
		if h.usesGlobal("Kid tablet") {
			t.Fatal("Kid tablet still inherits the global list")
		}
		if got := h.blocked("Kid tablet"); slices.Contains(got, "youtube") {
			t.Fatalf("Kid tablet blocks %v, want youtube open", got)
		}
	})
}

func TestImport_RejectedNoAdoption(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *setupFile)
	}{
		{"unknown built-in", func(f *setupFile) {
			f.Toggles = []fileToggle{{Child: "Ada", Service: "not-in-catalogue", State: "blocked"}}
		}},
		{"built-in name clash", func(f *setupFile) {
			f.CustomServices = []fileService{{Name: "youtube", Domains: []string{"clash.example"}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newImportHarness(t)
			f := clientsFile(fileChild{Name: "Ada", Clients: []string{"Kid tablet"}})
			tc.mutate(&f)
			wantProblem(t, h.postImport(h.loginOK(), fileBody(t, f)), http.StatusUnprocessableEntity, CodeUnprocessable)
			if got := h.updatedClients(); len(got) != 0 {
				t.Fatalf("updates = %v, want none", got)
			}
		})
	}
}

func TestImport_ClientOrder(t *testing.T) {
	h := newImportHarness(t)
	got := h.importOK(h.loginOK(), clientsFile(
		fileChild{Name: "Ada", Clients: []string{"Zed phone", "Old laptop", "Alpha phone"}},
		fileChild{Name: "Ben", Clients: []string{"Kid tablet", "Old laptop"}},
	))
	if want := []string{"Zed phone", "Alpha phone"}; !reflect.DeepEqual(got.MissingClients, want) {
		t.Fatalf("missing_clients = %v, want %v", got.MissingClients, want)
	}
	if got, want := h.updatedClients(), []string{"Old laptop", "Kid tablet"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("updates = %v, want %v", got, want)
	}
}
