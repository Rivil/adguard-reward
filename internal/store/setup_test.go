package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustExport(t *testing.T, s *Store) Setup {
	t.Helper()
	out, err := s.ExportSetup(ctx)
	if err != nil {
		t.Fatalf("ExportSetup: %v", err)
	}
	return out
}

func mustImport(t *testing.T, s *Store, in Setup) {
	t.Helper()
	if err := s.ImportSetup(ctx, in); err != nil {
		t.Fatalf("ImportSetup: %v", err)
	}
}

func mustDeleteService(t *testing.T, s *Store, id int64) {
	t.Helper()
	if err := s.DeleteCustomService(ctx, id); err != nil {
		t.Fatalf("DeleteCustomService(%d): %v", id, err)
	}
}

// setupTables is every table an import writes.
var setupTables = []string{
	"children", "child_clients", "custom_services", "custom_service_domains",
	"buttons", "button_services", "service_toggles",
}

func rowCounts(t *testing.T, s *Store) map[string]int {
	t.Helper()
	out := make(map[string]int, len(setupTables))
	for _, table := range setupTables {
		var n int
		if err := s.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

func assertNoRows(t *testing.T, s *Store) {
	t.Helper()
	for table, n := range rowCounts(t, s) {
		if n != 0 {
			t.Errorf("%s has %d rows, want 0", table, n)
		}
	}
}

// serviceByName finds a custom service's row by name.
func serviceByName(t *testing.T, s *Store, name string) CustomService {
	t.Helper()
	for _, svc := range mustServices(t, s) {
		if svc.Name == name {
			return svc
		}
	}
	t.Fatalf("no custom service named %q", name)
	return CustomService{}
}

// seedGapped builds a source whose custom ids start at 2, so ids that pass
// through unmapped land on the wrong service in a fresh target.
func seedGapped(t *testing.T, s *Store) (alpha, beta CustomService) {
	t.Helper()
	gone := mustService(t, s, "Gone", "gone.example")
	mustDeleteService(t, s, gone.ID)
	alpha = mustService(t, s, "Alpha", "z.example", "a.example")
	beta = mustService(t, s, "Beta", "m.example")
	ada := mustCreate(t, s, "Ada", "Kid phone", "Shared tv")
	ben := mustCreate(t, s, "Ben", "Shared tv")
	mustReplace(t, s, []Button{
		{ChildID: ben.ID, Label: "Later", Services: []string{beta.ServiceID(), "youtube"}, Icon: alpha.ServiceID(), Duration: 30 * time.Minute},
		{ChildID: ada.ID, Label: "First", Services: []string{"tiktok"}, Duration: time.Hour},
	})
	mustToggle(t, s, ben.ID, beta.ServiceID(), ToggleUnblocked)
	mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)
	return alpha, beta
}

func TestSetup_RoundTrip(t *testing.T) {
	src, _ := openTemp(t)
	alpha, beta := seedGapped(t, src)
	if alpha.ID != 2 || beta.ID != 3 {
		t.Fatalf("source ids = %d, %d, want gapped 2, 3", alpha.ID, beta.ID)
	}

	first := mustExport(t, src)
	want := Setup{
		Children: []SetupChild{
			{Name: "Ada", Clients: []string{"Kid phone", "Shared tv"}},
			{Name: "Ben", Clients: []string{"Shared tv"}},
		},
		CustomServices: []SetupService{
			{Name: "Alpha", Domains: []string{"z.example", "a.example"}},
			{Name: "Beta", Domains: []string{"m.example"}},
		},
		Buttons: []SetupButton{
			{Label: "Later", Child: "Ben", Services: []string{"custom:Beta", "youtube"}, Icon: "custom:Alpha", Duration: 30 * time.Minute},
			{Label: "First", Child: "Ada", Services: []string{"tiktok"}, Duration: time.Hour},
		},
		Toggles: []SetupToggle{
			{Child: "Ada", Service: "youtube", State: ToggleBlocked},
			{Child: "Ben", Service: "custom:Beta", State: ToggleUnblocked},
		},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("export =\n%+v\nwant\n%+v", first, want)
	}

	dst, _ := openTemp(t)
	mustImport(t, dst, first)
	if got := serviceByName(t, dst, "Beta"); got.ID != 2 {
		t.Fatalf("target Beta id = %d, want fresh id 2", got.ID)
	}
	if second := mustExport(t, dst); !reflect.DeepEqual(second, first) {
		t.Fatalf("second export =\n%+v\nwant\n%+v", second, first)
	}

	// Stored rows carry the target's ids, not the source's.
	buttons := mustList(t, dst)
	if got, want := buttons[0].Services, []string{"custom:2", "youtube"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target button services = %v, want %v", got, want)
	}
	toggles := mustToggles(t, dst)
	if got := toggleKeys(toggles); !reflect.DeepEqual(got, []string{"1/youtube=blocked", "2/custom:2=unblocked"}) {
		t.Fatalf("target toggles = %v", got)
	}
}

func TestSetup_EmptyInstall(t *testing.T) {
	s, _ := openTemp(t)
	got := mustExport(t, s)
	want := Setup{
		Children:       []SetupChild{},
		CustomServices: []SetupService{},
		Buttons:        []SetupButton{},
		Toggles:        []SetupToggle{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("empty export = %#v, want non-nil empty lists", got)
	}
}

func TestSetup_IconRemap(t *testing.T) {
	src, _ := openTemp(t)
	gone := mustService(t, src, "Gone", "gone.example")
	mustDeleteService(t, src, gone.ID)
	iconOnly := mustService(t, src, "Icon only", "icon.example")
	other := mustService(t, src, "Other", "other.example")
	ada := mustCreate(t, src, "Ada")
	mustReplace(t, src, []Button{
		{ChildID: ada.ID, Label: "B", Services: []string{other.ServiceID()}, Icon: iconOnly.ServiceID(), Duration: time.Hour},
	})

	dst, _ := openTemp(t)
	mustImport(t, dst, mustExport(t, src))
	got := mustList(t, dst)[0]
	if want := serviceByName(t, dst, "Icon only").ServiceID(); got.Icon != want {
		t.Fatalf("target icon = %q, want %q", got.Icon, want)
	}
	if want := serviceByName(t, dst, "Other").ServiceID(); !reflect.DeepEqual(got.Services, []string{want}) {
		t.Fatalf("target services = %v, want [%s]", got.Services, want)
	}
}

func TestSetup_ToggleOrder(t *testing.T) {
	src, _ := openTemp(t)
	for i := range 8 {
		gone := mustService(t, src, fmt.Sprintf("Gone %d", i))
		mustDeleteService(t, src, gone.ID)
	}
	a := mustService(t, src, "A", "a.example")
	b := mustService(t, src, "B", "b.example")
	if a.ID != 9 || b.ID != 10 {
		t.Fatalf("source ids = %d, %d, want 9, 10", a.ID, b.ID)
	}
	zed := mustCreate(t, src, "Zed")
	ada := mustCreate(t, src, "Ada")
	mustToggle(t, src, zed.ID, a.ServiceID(), ToggleUnblocked)
	mustToggle(t, src, ada.ID, b.ServiceID(), ToggleBlocked)
	mustToggle(t, src, ada.ID, a.ServiceID(), ToggleBlocked)

	first := mustExport(t, src)
	want := []SetupToggle{
		{Child: "Ada", Service: "custom:A", State: ToggleBlocked},
		{Child: "Ada", Service: "custom:B", State: ToggleBlocked},
		{Child: "Zed", Service: "custom:A", State: ToggleUnblocked},
	}
	if !reflect.DeepEqual(first.Toggles, want) {
		t.Fatalf("toggles = %+v, want %+v", first.Toggles, want)
	}

	dst, _ := openTemp(t)
	mustImport(t, dst, first)
	if second := mustExport(t, dst); !reflect.DeepEqual(second.Toggles, want) {
		t.Fatalf("toggles after import = %+v, want %+v", second.Toggles, want)
	}
}

func TestSetup_NameLikeID(t *testing.T) {
	services := make([]SetupService, 0, 13)
	for i := 1; i <= 12; i++ {
		services = append(services, SetupService{Name: fmt.Sprintf("Row %d", i), Domains: []string{}})
	}
	services = append(services, SetupService{Name: "12", Domains: []string{"twelve.example"}})

	s, _ := openTemp(t)
	mustImport(t, s, Setup{
		Children:       []SetupChild{{Name: "Ada"}},
		CustomServices: services,
		Buttons: []SetupButton{
			{Label: "B", Child: "Ada", Services: []string{"custom:12"}, Icon: "custom:12", Duration: time.Hour},
		},
		Toggles: []SetupToggle{{Child: "Ada", Service: "custom:12", State: ToggleBlocked}},
	})

	named := serviceByName(t, s, "12")
	if named.ID != 13 {
		t.Fatalf("service named 12 has row id %d, want 13", named.ID)
	}
	if row := serviceByName(t, s, "Row 12"); row.ID != 12 {
		t.Fatalf("service Row 12 has row id %d, want 12", row.ID)
	}
	b := mustList(t, s)[0]
	if !reflect.DeepEqual(b.Services, []string{"custom:13"}) || b.Icon != "custom:13" {
		t.Fatalf("button = %+v, want services and icon on custom:13", b)
	}
	if got := mustToggles(t, s)[0].ServiceID; got != "custom:13" {
		t.Fatalf("toggle service = %q, want custom:13", got)
	}
}

func TestSetup_NotEmpty(t *testing.T) {
	file := Setup{Children: []SetupChild{{Name: "Imported"}}}
	cases := []struct {
		name string
		seed func(t *testing.T, s *Store)
		want []string
	}{
		{"custom service alone", func(t *testing.T, s *Store) {
			mustService(t, s, "Alpha", "a.example")
		}, []string{"custom services"}},
		{"child alone", func(t *testing.T, s *Store) {
			mustCreate(t, s, "Ada")
		}, []string{"children"}},
		{"child with button and toggle", func(t *testing.T, s *Store) {
			ada := mustCreate(t, s, "Ada")
			mustReplace(t, s, []Button{btn(ada.ID, "B", "youtube")})
			mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)
		}, []string{"children", "buttons", "toggles"}},
		{"every kind", func(t *testing.T, s *Store) {
			ada := mustCreate(t, s, "Ada")
			mustReplace(t, s, []Button{btn(ada.ID, "B", "youtube")})
			mustToggle(t, s, ada.ID, "youtube", ToggleBlocked)
			mustService(t, s, "Alpha", "a.example")
		}, []string{"children", "buttons", "custom services", "toggles"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTemp(t)
			tc.seed(t, s)
			before := rowCounts(t, s)

			var notEmpty *ErrNotEmpty
			if err := s.EnsureEmpty(ctx); !errors.As(err, &notEmpty) {
				t.Fatalf("EnsureEmpty err = %v, want *ErrNotEmpty", err)
			}
			if !reflect.DeepEqual(notEmpty.Kinds, tc.want) {
				t.Fatalf("EnsureEmpty kinds = %v, want %v", notEmpty.Kinds, tc.want)
			}
			for _, kind := range tc.want {
				if !strings.Contains(notEmpty.Error(), kind) {
					t.Fatalf("message %q does not name %q", notEmpty.Error(), kind)
				}
			}

			notEmpty = nil
			if err := s.ImportSetup(ctx, file); !errors.As(err, &notEmpty) {
				t.Fatalf("ImportSetup err = %v, want *ErrNotEmpty", err)
			}
			if !reflect.DeepEqual(notEmpty.Kinds, tc.want) {
				t.Fatalf("ImportSetup kinds = %v, want %v", notEmpty.Kinds, tc.want)
			}
			if after := rowCounts(t, s); !reflect.DeepEqual(after, before) {
				t.Fatalf("row counts = %v, want unchanged %v", after, before)
			}
		})
	}
}

func TestSetup_EnsureEmptyOnFreshInstall(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.EnsureEmpty(ctx); err != nil {
		t.Fatalf("EnsureEmpty on a fresh install = %v", err)
	}
}

func TestSetup_ConcurrentImport(t *testing.T) {
	s, _ := openTemp(t)
	file := Setup{
		Children:       []SetupChild{{Name: "Ada", Clients: []string{"Kid phone"}}},
		CustomServices: []SetupService{{Name: "Alpha", Domains: []string{"a.example"}}},
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.ImportSetup(ctx, file)
		}()
	}
	wg.Wait()

	var ok, refused int
	for _, err := range errs {
		var notEmpty *ErrNotEmpty
		switch {
		case err == nil:
			ok++
		case errors.As(err, &notEmpty):
			refused++
		default:
			t.Fatalf("ImportSetup err = %v, want nil or *ErrNotEmpty", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("imports: %d ok, %d refused, want 1 and 1", ok, refused)
	}
}

// fullSetup touches every table an import writes.
func fullSetup() Setup {
	return Setup{
		Children:       []SetupChild{{Name: "Ada", Clients: []string{"Kid phone"}}},
		CustomServices: []SetupService{{Name: "Alpha", Domains: []string{"a.example"}}},
		Buttons: []SetupButton{
			{Label: "B", Child: "Ada", Services: []string{"custom:Alpha"}, Duration: time.Hour},
		},
		Toggles: []SetupToggle{{Child: "Ada", Service: "youtube", State: ToggleBlocked}},
	}
}

func TestSetup_Rollback(t *testing.T) {
	s, _ := openTemp(t)
	file := fullSetup()
	file.Toggles = append(file.Toggles, SetupToggle{Child: "Ada", Service: "tiktok", State: "open"})

	if err := s.ImportSetup(ctx, file); err == nil {
		t.Fatal("ImportSetup with an invalid toggle state succeeded")
	}
	assertNoRows(t, s)
}

func TestSetup_UnknownRefs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *Setup)
		want   string
	}{
		{"button child", func(f *Setup) { f.Buttons[0].Child = "Nobody" }, `"Nobody"`},
		{"button service", func(f *Setup) { f.Buttons[0].Services = []string{"custom:Nope"} }, `"custom:Nope"`},
		{"button icon", func(f *Setup) { f.Buttons[0].Icon = "custom:Nope" }, `"custom:Nope"`},
		{"toggle child", func(f *Setup) { f.Toggles[0].Child = "Nobody" }, `"Nobody"`},
		{"toggle service", func(f *Setup) { f.Toggles[0].Service = "custom:Nope" }, `"custom:Nope"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTemp(t)
			file := fullSetup()
			tc.mutate(&file)
			err := s.ImportSetup(ctx, file)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ImportSetup err = %v, want it to name %s", err, tc.want)
			}
			assertNoRows(t, s)
		})
	}
}

func TestSetup_HistoryWatermark(t *testing.T) {
	s, _ := openTemp(t)
	old := mustCreate(t, s, "Old")
	start := time.Unix(1_000, 0)
	if _, err := s.CreateGrant(ctx, old.ID, []string{"youtube"}, nil, start, start.Add(time.Hour)); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	history, err := s.ListGrantHistory(ctx, old.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListGrantHistory: %v", err)
	}
	if len(history.Grants) != 1 {
		t.Fatalf("before delete: %d grants, want 1", len(history.Grants))
	}
	if ok, err := s.DeleteChild(ctx, old.ID); err != nil || !ok {
		t.Fatalf("DeleteChild = %v, %v", ok, err)
	}

	mustImport(t, s, Setup{Children: []SetupChild{{Name: "New"}}})
	children, err := s.ListChildren(ctx)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(children) != 1 || children[0].ID != old.ID {
		t.Fatalf("children = %+v, want one reusing id %d", children, old.ID)
	}
	history, err = s.ListGrantHistory(ctx, old.ID, 0, 0)
	if err != nil {
		t.Fatalf("ListGrantHistory: %v", err)
	}
	if len(history.Grants) != 0 {
		t.Fatalf("imported child inherited %d grants", len(history.Grants))
	}
}

func TestSetup_DanglingRef(t *testing.T) {
	const dangling = "custom:99"
	cases := []struct {
		name string
		seed func(t *testing.T, s *Store, child int64)
	}{
		{"button service", func(t *testing.T, s *Store, child int64) {
			mustReplace(t, s, []Button{btn(child, "B", dangling)})
		}},
		{"button icon", func(t *testing.T, s *Store, child int64) {
			mustReplace(t, s, []Button{{ChildID: child, Label: "B", Services: []string{"youtube"}, Icon: dangling, Duration: time.Hour}})
		}},
		{"toggle", func(t *testing.T, s *Store, child int64) {
			mustToggle(t, s, child, dangling, ToggleBlocked)
		}},
		{"malformed ref", func(t *testing.T, s *Store, child int64) {
			mustToggle(t, s, child, "custom:not-an-id", ToggleBlocked)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTemp(t)
			tc.seed(t, s, mustCreate(t, s, "Ada").ID)
			out, err := s.ExportSetup(ctx)
			if err == nil {
				t.Fatalf("ExportSetup = %+v, want an error", out)
			}
			if !strings.Contains(err.Error(), "custom:") {
				t.Fatalf("ExportSetup err = %v, want it to name the ref", err)
			}
			if !reflect.DeepEqual(out, Setup{}) {
				t.Fatalf("ExportSetup returned a partial setup: %+v", out)
			}
		})
	}
}

func TestSetup_NoDeadlock(t *testing.T) {
	src, _ := openTemp(t)
	seedGapped(t, src)
	dst, _ := openTemp(t)

	// A nested st.db call inside the tx waits on the only connection until
	// this deadline, so it surfaces as an error rather than a hung test.
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	file, err := src.ExportSetup(deadline)
	if err != nil {
		t.Fatalf("ExportSetup: %v", err)
	}
	if err := dst.ImportSetup(deadline, file); err != nil {
		t.Fatalf("ImportSetup: %v", err)
	}
}
