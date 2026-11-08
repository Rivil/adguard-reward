package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func mustService(t *testing.T, s *Store, name string, domains ...string) CustomService {
	t.Helper()
	cs, err := s.CreateCustomService(ctx, name, domains)
	if err != nil {
		t.Fatalf("CreateCustomService(%q, %v): %v", name, domains, err)
	}
	return cs
}

func mustServices(t *testing.T, s *Store) []CustomService {
	t.Helper()
	list, err := s.ListCustomServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestCustomServices_Persist(t *testing.T) {
	s, dir := openTemp(t)
	created := mustService(t, s, "School Portal", "z.example", "a.example", "m.example")
	if created.ID == 0 || created.ServiceID() != CustomServiceID(created.ID) {
		t.Fatalf("created %+v", created)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, err := s2.GetCustomService(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := CustomService{ID: created.ID, Name: "School Portal", Domains: []string{"z.example", "a.example", "m.example"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen got %+v, want %+v", got, want)
	}
	if list := mustServices(t, s2); !reflect.DeepEqual(list, []CustomService{want}) {
		t.Fatalf("list after reopen %+v", list)
	}
}

func TestCustomServices_Unique(t *testing.T) {
	s, _ := openTemp(t)
	a := mustService(t, s, "TikTok Web", "tiktok.example")

	_, err := s.CreateCustomService(ctx, "tiktok web", []string{"other.example"})
	if !errors.Is(err, ErrServiceNameTaken) || !errors.Is(err, ErrNameTaken) {
		t.Fatalf("case-different name: err = %v, want ErrServiceNameTaken (and ErrNameTaken)", err)
	}
	if err.Error() != "custom service name already taken" {
		t.Errorf("message %q", err)
	}
	if n := len(mustServices(t, s)); n != 1 {
		t.Fatalf("%d rows after name clash, want 1", n)
	}

	_, err = s.CreateCustomService(ctx, "Other", []string{"fresh.example", "tiktok.example"})
	var taken *ErrDomainTaken
	if !errors.As(err, &taken) || taken.OwnerID != a.ID || taken.Owner != "TikTok Web" || taken.Domain != "tiktok.example" {
		t.Fatalf("domain clash: err = %#v, want *ErrDomainTaken owned by A", err)
	}
	if n := len(mustServices(t, s)); n != 1 {
		t.Fatalf("%d rows after domain clash, want 1", n)
	}
	if n := len(queryStrings(t, s.DB(), `SELECT domain FROM custom_service_domains WHERE domain = 'fresh.example'`)); n != 0 {
		t.Fatal("fresh.example inserted despite the clash")
	}

	b := mustService(t, s, "B", "b1.example", "b2.example")
	_, err = s.UpdateCustomService(ctx, b.ID, "B", []string{"b1.example", "tiktok.example"})
	if !errors.As(err, &taken) || taken.OwnerID != a.ID {
		t.Fatalf("update into A's domain: err = %v", err)
	}
	if got, _ := s.GetCustomService(ctx, b.ID); !reflect.DeepEqual(got.Domains, []string{"b1.example", "b2.example"}) {
		t.Fatalf("B's domains after refused update: %v", got.Domains)
	}
	_, err = s.UpdateCustomService(ctx, b.ID, "TIKTOK WEB", []string{"b1.example"})
	if !errors.Is(err, ErrServiceNameTaken) {
		t.Fatalf("rename onto A's name: err = %v", err)
	}
	// A service's own name (any case) and domains never conflict with itself.
	if _, err := s.UpdateCustomService(ctx, a.ID, "TIKTOK web", []string{"tiktok.example"}); err != nil {
		t.Fatalf("self case-change: %v", err)
	}
}

func TestCustomServices_Update(t *testing.T) {
	s, _ := openTemp(t)
	cs := mustService(t, s, "Portal", "a.example", "b.example")

	got, err := s.UpdateCustomService(ctx, cs.ID, "Portal 2", []string{"b.example", "c.example"})
	if err != nil {
		t.Fatal(err)
	}
	want := CustomService{ID: cs.ID, Name: "Portal 2", Domains: []string{"b.example", "c.example"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if all := queryStrings(t, s.DB(), `SELECT domain FROM custom_service_domains ORDER BY domain`); !reflect.DeepEqual(all, []string{"b.example", "c.example"}) {
		t.Fatalf("stored domains %v", all)
	}

	// Inject a failure after the domain delete step: a trigger aborts the
	// insert of d.example, so the transaction must roll back whole.
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_d BEFORE INSERT ON custom_service_domains
		WHEN NEW.domain = 'd.example' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateCustomService(ctx, cs.ID, "Portal 3", []string{"c.example", "d.example"}); err == nil {
		t.Fatal("update past an injected failure succeeded")
	}
	after, err := s.GetCustomService(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("after rollback %+v, want %+v", after, want)
	}

	if _, err := s.UpdateCustomService(ctx, 999, "x", []string{"x.example"}); !errors.Is(err, ErrCustomServiceNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestCustomServices_DeleteInUse(t *testing.T) {
	s, _ := openTemp(t)
	kid := mustCreate(t, s, "Kid", "kid-phone")
	cs := mustService(t, s, "Portal", "a.example")
	svc := cs.ServiceID()
	if _, err := s.ReplaceButtons(ctx, []Button{{ChildID: kid.ID, Label: "P", Services: []string{svc}, Icon: svc, Duration: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	// Lower ids that never block: an ended and an expired grant.
	for _, to := range []string{StatusEnded, StatusExpired} {
		g, err := s.CreateGrant(ctx, kid.ID, []string{svc}, []string{"kid-phone"}, now, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := s.SetGrantStatus(ctx, g.ID, StatusActive, to, now); err != nil || !ok {
			t.Fatalf("SetGrantStatus %s: %v %v", to, ok, err)
		}
	}
	active, err := s.CreateGrant(ctx, kid.ID, []string{"youtube", svc}, []string{"kid-phone"}, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	err = s.DeleteCustomService(ctx, cs.ID)
	var inUse *ErrServiceInUse
	if !errors.As(err, &inUse) || inUse.GrantID != active.ID || inUse.ServiceID != svc {
		t.Fatalf("err = %v, want *ErrServiceInUse{GrantID:%d}", err, active.ID)
	}
	if got, err := s.GetCustomService(ctx, cs.ID); err != nil || !reflect.DeepEqual(got.Domains, []string{"a.example"}) {
		t.Fatalf("service after refused delete: %+v %v", got, err)
	}
	if bs, _ := s.ListButtons(ctx); len(bs) != 1 || bs[0].Icon != svc || !reflect.DeepEqual(bs[0].Services, []string{svc}) {
		t.Fatalf("buttons after refused delete: %+v", bs)
	}

	if ok, err := s.SetGrantStatus(ctx, active.ID, StatusActive, StatusEnded, now); err != nil || !ok {
		t.Fatal(err)
	}
	if err := s.DeleteCustomService(ctx, cs.ID); err != nil {
		t.Fatalf("delete after the grant ended: %v", err)
	}
	if _, err := s.GetCustomService(ctx, cs.ID); !errors.Is(err, ErrCustomServiceNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if n := len(queryStrings(t, s.DB(), `SELECT domain FROM custom_service_domains`)); n != 0 {
		t.Fatalf("%d domains left", n)
	}
	if err := s.DeleteCustomService(ctx, cs.ID); !errors.Is(err, ErrCustomServiceNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestCustomServices_DeleteStripsButtons(t *testing.T) {
	s, _ := openTemp(t)
	kid := mustCreate(t, s, "Kid", "kid-phone")
	cs := mustService(t, s, "Portal", "a.example")
	other := mustService(t, s, "Other", "o.example")
	svc := cs.ServiceID()
	_, err := s.ReplaceButtons(ctx, []Button{
		{ChildID: kid.ID, Label: "mixed", Services: []string{svc, "youtube"}, Duration: time.Hour},
		{ChildID: kid.ID, Label: "only", Services: []string{svc}, Duration: time.Hour},
		{ChildID: kid.ID, Label: "icon", Services: []string{"tiktok", other.ServiceID()}, Icon: svc, Duration: time.Hour},
		{ChildID: kid.ID, Label: "untouched", Services: []string{other.ServiceID(), "youtube"}, Icon: "youtube", Duration: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteCustomService(ctx, cs.ID); err != nil {
		t.Fatal(err)
	}
	bs, err := s.ListButtons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type view struct {
		Label    string
		Services []string
		Icon     string
	}
	var got []view
	for _, b := range bs {
		got = append(got, view{b.Label, b.Services, b.Icon})
	}
	want := []view{
		{"mixed", []string{"youtube"}, ""},
		{"icon", []string{"tiktok", other.ServiceID()}, ""},
		{"untouched", []string{other.ServiceID(), "youtube"}, "youtube"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buttons after delete:\n got  %+v\n want %+v", got, want)
	}
}

func TestCustomServices_SelfDuplicate(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.CreateCustomService(ctx, "Dup", []string{"a.example", "a.example"}); err == nil {
		t.Fatal("duplicate domain in one create accepted")
	}
	if n := len(mustServices(t, s)); n != 0 {
		t.Fatalf("%d services after a refused create", n)
	}
	if n := len(queryStrings(t, s.DB(), `SELECT domain FROM custom_service_domains`)); n != 0 {
		t.Fatalf("%d domains after a refused create", n)
	}
	cs := mustService(t, s, "Ok", "a.example")
	if _, err := s.UpdateCustomService(ctx, cs.ID, "Ok", []string{"b.example", "b.example"}); err == nil {
		t.Fatal("duplicate domain in one update accepted")
	}
	if got, _ := s.GetCustomService(ctx, cs.ID); !reflect.DeepEqual(got.Domains, []string{"a.example"}) {
		t.Fatalf("domains after refused update: %v", got.Domains)
	}
}

func TestCustomServices_IDsNeverReused(t *testing.T) {
	s, _ := openTemp(t)
	a := mustService(t, s, "A", "a.example")
	if err := s.DeleteCustomService(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	b := mustService(t, s, "B", "b.example")
	if b.ID == a.ID {
		t.Fatalf("id %d reused after delete", b.ID)
	}
}

func TestIsCustomServiceID(t *testing.T) {
	for in, want := range map[string]bool{
		"youtube": false, "custom:7": true, "custom:": false, "custom:0": false,
		"custom:07": false, "custom:-1": false, "custom:+1": false, "custom:1a": false,
		"Custom:1": false, " custom:1": false, "custom:9223372036854775807": true,
		"custom:9223372036854775808": false,
	} {
		if got := IsCustomServiceID(in); got != want {
			t.Errorf("IsCustomServiceID(%q) = %v, want %v", in, got, want)
		}
	}
	if n, ok := ParseCustomID(CustomServiceID(42)); !ok || n != 42 {
		t.Errorf("round trip: %d %v", n, ok)
	}
}

// TestMigrate_ServiceTogglesAfterCustomServices pins 0008 directly after
// 0007, embedded and applied, without requiring it to stay the last one.
func TestMigrate_ServiceTogglesAfterCustomServices(t *testing.T) {
	s, _ := openTemp(t)
	const prev, want = "0007_custom_services.sql", "0008_service_toggles.sql"
	follows := func(names []string) bool {
		for i, n := range names {
			if n == prev {
				return i+1 < len(names) && names[i+1] == want
			}
		}
		return false
	}
	if names := s.Migrations(); !follows(names) {
		t.Fatalf("migrations = %v, want %s directly after %s", names, want, prev)
	}
	if applied := queryStrings(t, s.DB(), `SELECT name FROM schema_migrations ORDER BY rowid`); !follows(applied) {
		t.Fatalf("applied = %v, want %s directly after %s", applied, want, prev)
	}
}
