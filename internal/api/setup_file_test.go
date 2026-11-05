package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rivil/adguard-reward/internal/grants"
)

// baseFile is a valid file touching every list; each case changes one thing.
func baseFile() setupFile {
	return setupFile{
		Format:  setupFileFormat,
		Version: setupFileVersion,
		Children: []fileChild{
			{Name: "Ada", Clients: []string{"Kid phone", "Shared tv"}},
			{Name: "Ben", Clients: []string{"Shared tv"}},
		},
		CustomServices: []fileService{
			{Name: "Roblox Extras", Domains: []string{"roblox.example", "rbx.example"}},
			{Name: "Other", Domains: []string{"other.example"}},
		},
		Buttons: []fileButton{
			{Label: "Games", Child: "Ada", Services: []string{"youtube", "custom:Roblox Extras"}, Icon: "custom:Other", Duration: 1800},
		},
		Toggles: []fileToggle{
			{Child: "Ben", Service: "tiktok", State: "blocked"},
			{Child: "Ben", Service: "custom:Other", State: "unblocked"},
		},
	}
}

func fileBody(t *testing.T, f setupFile) []byte {
	t.Helper()
	body, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

// mapBody edits the file as a generic object, for shapes setupFile cannot hold.
func mapBody(t *testing.T, edit func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(fileBody(t, baseFile()), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	edit(m)
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

func assertRefused(t *testing.T, body []byte, status int, wants ...string) {
	t.Helper()
	_, p := parseSetupFile(body)
	if p == nil {
		t.Fatalf("parseSetupFile(%.200s) accepted it", body)
	}
	if p.Status != status {
		t.Fatalf("status = %d (%s), want %d", p.Status, p.Message, status)
	}
	wantCode := CodeUnprocessable
	if status == http.StatusBadRequest {
		wantCode = CodeBadRequest
	}
	if p.Code != wantCode {
		t.Fatalf("code = %q, want %q", p.Code, wantCode)
	}
	for _, want := range wants {
		if !strings.Contains(p.Message, want) {
			t.Fatalf("message %q does not name %q", p.Message, want)
		}
	}
}

func assertParses(t *testing.T, f setupFile) setupFile {
	t.Helper()
	got, p := parseSetupFile(fileBody(t, f))
	if p != nil {
		t.Fatalf("parseSetupFile refused: %d %s", p.Status, p.Message)
	}
	return got
}

// edited applies mutate to a fresh baseFile.
func edited(mutate func(f *setupFile)) setupFile {
	f := baseFile()
	mutate(&f)
	return f
}

func TestParseSetupFile_Valid(t *testing.T) {
	want := baseFile()
	if got := assertParses(t, want); !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseSetupFile_Unreadable(t *testing.T) {
	valid := fileBody(t, baseFile())
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"empty body", nil, "not valid JSON"},
		{"invalid JSON", []byte(`{not json`), "not valid JSON"},
		{"stray closing brace", append(append([]byte{}, valid...), '}'), "not valid JSON"},
		{"second object", append(append([]byte{}, valid...), []byte(` {}`)...), "not valid JSON"},
		{"array", []byte(`[]`), "JSON object"},
		{"number", []byte(`5`), "JSON object"},
		{"unknown top-level field", mapBody(t, func(m map[string]any) { m["extra"] = true }), `"extra"`},
		{"unknown nested field", mapBody(t, func(m map[string]any) {
			m["children"].([]any)[0].(map[string]any)["id"] = 7
		}), `"id"`},
		{"wrong list type", mapBody(t, func(m map[string]any) { m["children"] = 5 }), "children"},
		{"fractional duration", mapBody(t, func(m map[string]any) {
			m["buttons"].([]any)[0].(map[string]any)["duration"] = 90.5
		}), "duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, tc.body, http.StatusBadRequest, tc.want)
		})
	}
}

func TestParseSetupFile_FutureVersion(t *testing.T) {
	body := []byte(`{"format":"adguard-reward-export","version":2,"future":[]}`)
	assertRefused(t, body, http.StatusUnprocessableEntity, "version 2")
	if _, p := parseSetupFile(body); strings.Contains(p.Message, "future") {
		t.Fatalf("message %q names the unknown field, not the version", p.Message)
	}
}

func TestParseSetupFile_Version(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"missing", mapBody(t, func(m map[string]any) { delete(m, "version") })},
		{"string", mapBody(t, func(m map[string]any) { m["version"] = "1" })},
		{"null value", mapBody(t, func(m map[string]any) { m["version"] = nil })},
		{"fraction", mapBody(t, func(m map[string]any) { m["version"] = 1.5 })},
		{"zero", mapBody(t, func(m map[string]any) { m["version"] = 0 })},
		{"null body", []byte(`null`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, tc.body, http.StatusUnprocessableEntity, "version")
		})
	}
}

func TestParseSetupFile_Format(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"wrong", mapBody(t, func(m map[string]any) { m["format"] = "something-else" })},
		{"absent", mapBody(t, func(m map[string]any) { delete(m, "format") })},
		{"not a string", mapBody(t, func(m map[string]any) { m["format"] = 1 })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, tc.body, http.StatusUnprocessableEntity, "not an adguard-reward export")
		})
	}
}

func TestParseSetupFile_RequiredLists(t *testing.T) {
	refused := []string{"children", "custom_services", "buttons", "toggles"}
	bare := []byte(`{"format":"adguard-reward-export","version":1}`)
	assertRefused(t, bare, http.StatusUnprocessableEntity, "children")

	for _, field := range refused {
		t.Run(field+" absent", func(t *testing.T) {
			assertRefused(t, mapBody(t, func(m map[string]any) { delete(m, field) }),
				http.StatusUnprocessableEntity, field)
		})
		t.Run(field+" null", func(t *testing.T) {
			assertRefused(t, mapBody(t, func(m map[string]any) { m[field] = nil }),
				http.StatusUnprocessableEntity, field)
		})
	}

	empty := []byte(`{"format":"adguard-reward-export","version":1,"children":[],"custom_services":[],"buttons":[],"toggles":[]}`)
	if _, p := parseSetupFile(empty); p != nil {
		t.Fatalf("empty lists refused: %s", p.Message)
	}
}

func TestParseSetupFile_DuplicateNames(t *testing.T) {
	refused := []struct {
		name   string
		mutate func(f *setupFile)
		want   string
	}{
		{"child ASCII case", func(f *setupFile) { f.Children[1].Name = "ada" }, "children[1].name"},
		{"child after trim", func(f *setupFile) { f.Children[1].Name = " Ada " }, "children[1].name"},
		{"service ASCII case", func(f *setupFile) { f.CustomServices[1].Name = "ROBLOX extras" }, "custom_services[1].name"},
		{"service non-ASCII case", func(f *setupFile) {
			f.CustomServices[0].Name, f.CustomServices[1].Name = "Éa", "éa"
			f.Buttons[0].Services = []string{"youtube"}
			f.Buttons[0].Icon = ""
			f.Toggles = f.Toggles[:1]
		}, "custom_services[1].name"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, fileBody(t, edited(tc.mutate)), http.StatusUnprocessableEntity, tc.want, "duplicates")
		})
	}

	t.Run("child non-ASCII case is distinct", func(t *testing.T) {
		assertParses(t, edited(func(f *setupFile) {
			f.Children[0].Name, f.Children[1].Name = "Éa", "éa"
			f.Buttons[0].Child = "Éa"
			f.Toggles[0].Child, f.Toggles[1].Child = "éa", "éa"
		}))
	})
}

func TestParseSetupFile_DuplicateDomains(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *setupFile)
		path   string
	}{
		{"in one service", func(f *setupFile) {
			f.CustomServices[0].Domains = []string{"Example.COM", "example.com."}
		}, "custom_services[0].domains[1]"},
		{"across two services", func(f *setupFile) {
			f.CustomServices[0].Domains = []string{"Example.COM"}
			f.CustomServices[1].Domains = []string{"example.com."}
		}, "custom_services[1].domains[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, fileBody(t, edited(tc.mutate)), http.StatusUnprocessableEntity, tc.path, `"example.com"`)
		})
	}
}

func TestParseSetupFile_DomainsAreHostnames(t *testing.T) {
	for _, domain := range []string{`||evil.com^$important`, `/evil\.com/`, "", "*.evil.com"} {
		t.Run(domain, func(t *testing.T) {
			f := edited(func(f *setupFile) { f.CustomServices[0].Domains = []string{domain} })
			assertRefused(t, fileBody(t, f), http.StatusUnprocessableEntity, "custom_services[0].domains[0]")
		})
	}
}

func TestParseSetupFile_DanglingRefs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *setupFile)
		want   []string
	}{
		{"button child", func(f *setupFile) { f.Buttons[0].Child = "Nobody" }, []string{"buttons[0].child", `"Nobody"`}},
		{"button child wrong case", func(f *setupFile) { f.Buttons[0].Child = "ada" }, []string{"buttons[0].child", `"ada"`}},
		{"button service", func(f *setupFile) { f.Buttons[0].Services[1] = "custom:Nope" }, []string{"buttons[0].services[1]", `"custom:Nope"`}},
		{"button service wrong case", func(f *setupFile) { f.Buttons[0].Services[1] = "custom:roblox extras" }, []string{"buttons[0].services[1]"}},
		{"button service like a row id", func(f *setupFile) { f.Buttons[0].Services[1] = "custom:12" }, []string{"buttons[0].services[1]", `"custom:12"`}},
		{"button icon", func(f *setupFile) { f.Buttons[0].Icon = "custom:Nope" }, []string{"buttons[0].icon", `"custom:Nope"`}},
		{"toggle child", func(f *setupFile) { f.Toggles[1].Child = "Nobody" }, []string{"toggles[1].child", `"Nobody"`}},
		{"toggle service", func(f *setupFile) { f.Toggles[1].Service = "custom:Nope" }, []string{"toggles[1].service", `"custom:Nope"`}},
		{"blank button service", func(f *setupFile) { f.Buttons[0].Services[1] = "  " }, []string{"buttons[0].services[1]"}},
		{"blank toggle service", func(f *setupFile) { f.Toggles[0].Service = "" }, []string{"toggles[0].service"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, fileBody(t, edited(tc.mutate)), http.StatusUnprocessableEntity, tc.want...)
		})
	}
}

func TestParseSetupFile_LegalShapes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *setupFile)
	}{
		{"client under two children", func(f *setupFile) {}},
		{"two buttons with one label", func(f *setupFile) { f.Buttons = append(f.Buttons, f.Buttons[0]) }},
		{"child and service share a name", func(f *setupFile) {
			f.CustomServices[1].Name = "Ada"
			f.Buttons[0].Icon = "custom:Ada"
			f.Toggles[1].Service = "custom:Ada"
		}},
		{"service named like a row id", func(f *setupFile) {
			f.CustomServices[1].Name = "12"
			f.Buttons[0].Icon = "custom:12"
			f.Toggles[1].Service = "custom:12"
		}},
		{"no icon", func(f *setupFile) { f.Buttons[0].Icon = "" }},
		{"child with no clients", func(f *setupFile) { f.Children[1].Clients = []string{} }},
		{"same service toggled for two children", func(f *setupFile) {
			f.Toggles = append(f.Toggles, fileToggle{Child: "Ada", Service: "tiktok", State: "unblocked"})
		}},
		{"client name with a line break", func(f *setupFile) { f.Children[0].Clients[0] = "Kid\nphone" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertParses(t, edited(tc.mutate))
		})
	}
}

func TestParseSetupFile_Repeats(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *setupFile)
		path   string
	}{
		{"toggle", func(f *setupFile) {
			f.Toggles = append(f.Toggles, fileToggle{Child: "Ben", Service: "tiktok", State: "unblocked"})
		}, "toggles[2]"},
		{"toggle state", func(f *setupFile) { f.Toggles[0].State = "open" }, "toggles[0].state"},
		{"client", func(f *setupFile) { f.Children[0].Clients[1] = "Kid phone" }, "children[0].clients[1]"},
		{"client after trim", func(f *setupFile) { f.Children[0].Clients[1] = " Kid phone " }, "children[0].clients[1]"},
		{"blank client", func(f *setupFile) { f.Children[0].Clients[1] = "  " }, "children[0].clients[1]"},
		{"button service", func(f *setupFile) { f.Buttons[0].Services[1] = "youtube" }, "buttons[0].services[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, fileBody(t, edited(tc.mutate)), http.StatusUnprocessableEntity, tc.path)
		})
	}
}

func TestParseSetupFile_Bounds(t *testing.T) {
	const wide = "é" // two bytes, one rune: proves runes are counted, not bytes
	minSeconds := int64(grants.MinDuration / time.Second)
	maxSeconds := int64(grants.MaxDuration / time.Second)
	domains := func(n int) []string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, fmt.Sprintf("d%d.example", i))
		}
		return out
	}
	child := func(name string) func(f *setupFile) {
		return func(f *setupFile) { f.Children[0].Name = name; f.Buttons[0].Child = name }
	}
	service := func(name string) func(f *setupFile) {
		return func(f *setupFile) {
			f.CustomServices[1].Name = name
			f.Buttons[0].Icon = customIDPrefix + name
			f.Toggles[1].Service = customIDPrefix + name
		}
	}
	cases := []struct {
		name   string
		mutate func(f *setupFile)
		path   string // "" when the value is legal
	}{
		{"label at cap", func(f *setupFile) { f.Buttons[0].Label = strings.Repeat(wide, maxLabelRunes) }, ""},
		{"label over cap", func(f *setupFile) { f.Buttons[0].Label = strings.Repeat(wide, maxLabelRunes+1) }, "buttons[0].label"},
		{"blank label", func(f *setupFile) { f.Buttons[0].Label = "  " }, "buttons[0].label"},
		{"duration at min", func(f *setupFile) { f.Buttons[0].Duration = minSeconds }, ""},
		{"duration under min", func(f *setupFile) { f.Buttons[0].Duration = minSeconds - 1 }, "buttons[0].duration"},
		{"duration at max", func(f *setupFile) { f.Buttons[0].Duration = maxSeconds }, ""},
		{"duration over max", func(f *setupFile) { f.Buttons[0].Duration = maxSeconds + 1 }, "buttons[0].duration"},
		{"duration zero", func(f *setupFile) { f.Buttons[0].Duration = 0 }, "buttons[0].duration"},
		{"duration negative", func(f *setupFile) { f.Buttons[0].Duration = -60 }, "buttons[0].duration"},
		{"no services", func(f *setupFile) { f.Buttons[0].Services = []string{} }, "buttons[0].services"},
		{"null services", func(f *setupFile) { f.Buttons[0].Services = nil }, "buttons[0].services"},
		{"child name at cap", child(strings.Repeat(wide, maxChildName)), ""},
		{"child name over cap", child(strings.Repeat(wide, maxChildName+1)), "children[0].name"},
		{"blank child name", child("  "), "children[0].name"},
		{"service name at cap", service(strings.Repeat(wide, maxCustomServiceName)), ""},
		{"service name over cap", service(strings.Repeat(wide, maxCustomServiceName+1)), "custom_services[1].name"},
		{"blank service name", service("  "), "custom_services[1].name"},
		{"client at cap", func(f *setupFile) { f.Children[0].Clients[0] = strings.Repeat("x", maxClientName) }, ""},
		{"client over cap", func(f *setupFile) { f.Children[0].Clients[0] = strings.Repeat("x", maxClientName+1) }, "children[0].clients[0]"},
		{"client over cap in bytes", func(f *setupFile) { f.Children[0].Clients[0] = strings.Repeat(wide, maxClientName/2+1) }, "children[0].clients[0]"},
		{"domains at cap", func(f *setupFile) { f.CustomServices[0].Domains = domains(maxCustomDomains) }, ""},
		{"domains over cap", func(f *setupFile) { f.CustomServices[0].Domains = domains(maxCustomDomains + 1) }, "custom_services[0].domains"},
		{"no domains", func(f *setupFile) { f.CustomServices[0].Domains = []string{} }, "custom_services[0].domains"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := edited(tc.mutate)
			if tc.path == "" {
				assertParses(t, f)
				return
			}
			assertRefused(t, fileBody(t, f), http.StatusUnprocessableEntity, tc.path)
		})
	}
}

func TestParseSetupFile_DurationOverflow(t *testing.T) {
	wraps := int64(18446745874) // a variable: as a constant this would not compile
	if d := time.Duration(wraps) * time.Second; d < grants.MinDuration || d > grants.MaxDuration {
		t.Fatalf("%d seconds wraps to %v, outside the bounds: the case proves nothing", wraps, d)
	}
	body := mapBody(t, func(m map[string]any) {
		m["buttons"].([]any)[0].(map[string]any)["duration"] = json.Number(strconv.FormatInt(wraps, 10))
	})
	assertRefused(t, body, http.StatusUnprocessableEntity, "buttons[0].duration")
}

func TestParseSetupFile_Normalises(t *testing.T) {
	got := assertParses(t, edited(func(f *setupFile) {
		f.Children[0].Name = "  Ada "
		f.Children[0].Clients[0] = " Kid phone "
		f.CustomServices[1].Name = " Other "
		f.CustomServices[1].Domains[0] = "Other.Example."
		f.Buttons[0].Label = " Games "
		f.Buttons[0].Child = " Ada"
		f.Buttons[0].Services[0] = " youtube "
		f.Buttons[0].Icon = " custom:Other "
		f.Toggles[0].Child = "Ben "
		f.Toggles[1].Service = " custom:Other"
	}))
	if want := baseFile(); !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed =\n%+v\nwant\n%+v", got, want)
	}
}

func TestBuiltinRefs(t *testing.T) {
	cases := []struct {
		name  string
		file  setupFile
		wants []string
	}{
		{"none", setupFile{}, []string{}},
		{"base file", baseFile(), []string{"youtube", "tiktok"}},
		{"icon only", setupFile{Buttons: []fileButton{
			{Services: []string{"custom:A"}, Icon: "discord"},
		}}, []string{"discord"}},
		{"toggle only", setupFile{Toggles: []fileToggle{{Service: "steam"}}}, []string{"steam"}},
		{"repeats listed once", setupFile{
			Buttons: []fileButton{{Services: []string{"a", "b"}, Icon: "a"}, {Services: []string{"b"}}},
			Toggles: []fileToggle{{Service: "a"}},
		}, []string{"a", "b"}},
		{"custom refs are never built-in", setupFile{
			Buttons: []fileButton{{Services: []string{"custom:12"}, Icon: "custom:Roblox Extras"}},
			Toggles: []fileToggle{{Service: "custom:12"}},
		}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := builtinRefs(tc.file); !reflect.DeepEqual(got, tc.wants) {
				t.Fatalf("builtinRefs = %v, want %v", got, tc.wants)
			}
		})
	}
}
