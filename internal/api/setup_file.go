package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/rules"
	"github.com/Rivil/adguard-reward/internal/store"
)

const (
	setupFileFormat  = "adguard-reward-export"
	setupFileVersion = 1
)

// setupFile is the export file. It holds no row ids and no timestamps. A
// service ref is a built-in id or "custom:<name>": <name> is always a service
// name, so "custom:12" names a service called 12, and store.ParseCustomID and
// store.IsCustomServiceID must never be used on a ref.
type setupFile struct {
	Format         string        `json:"format"`
	Version        int           `json:"version"`
	Children       []fileChild   `json:"children"`
	CustomServices []fileService `json:"custom_services"`
	Buttons        []fileButton  `json:"buttons"`
	Toggles        []fileToggle  `json:"toggles"`
}

type fileChild struct {
	Name    string   `json:"name"`
	Clients []string `json:"clients"`
}

type fileService struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

// fileButton names its child. Duration is whole seconds, held as int64 and
// bounds-checked as seconds: a time.Duration conversion wraps before the check
// could see it.
type fileButton struct {
	Label    string   `json:"label"`
	Child    string   `json:"child"`
	Services []string `json:"services"`
	Icon     string   `json:"icon"`
	Duration int64    `json:"duration"`
}

type fileToggle struct {
	Child   string `json:"child"`
	Service string `json:"service"`
	State   string `json:"state"`
}

// fileProblem is why a file was refused, ready for writeError: 400 when it
// could not be read as the file's shape, 422 when it was read and its content
// is refused.
type fileProblem struct {
	Status  int
	Code    string
	Message string
}

func badFile(format string, args ...any) *fileProblem {
	return &fileProblem{http.StatusBadRequest, CodeBadRequest, fmt.Sprintf(format, args...)}
}

func refuseFile(format string, args ...any) *fileProblem {
	return &fileProblem{http.StatusUnprocessableEntity, CodeUnprocessable, fmt.Sprintf(format, args...)}
}

// parseSetupFile reads and validates a whole export file, stopping at the
// first problem. Everything it checks needs only the file; the catalogue and
// the store are the caller's. The returned file is normalised the way the
// handlers normalise their input: names, labels, clients and refs trimmed,
// domains per rules.NormalizeDomain.
//
// json.Valid covers the whole body, so trailing data is refused here: dec.More
// reports false on a stray closing brace, the very thing it would need to see.
// The header is read leniently before the strict decode so that a future
// version is reported as a version, not as its first unknown field.
func parseSetupFile(body []byte) (setupFile, *fileProblem) {
	if !json.Valid(body) {
		return setupFile{}, badFile("not valid JSON")
	}
	var head struct {
		Format  json.RawMessage `json:"format"`
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return setupFile{}, badFile("expected a single JSON object")
	}
	if p := checkHeader(head.Format, head.Version); p != nil {
		return setupFile{}, p
	}

	f, p := decodeFile(body)
	if p != nil {
		return setupFile{}, p
	}
	if p := f.validate(); p != nil {
		return setupFile{}, p
	}
	return f, nil
}

// rawFile is the file's top level with each list left undecoded. A decoder
// reports a bad field with no index, so records decode one at a time to name
// theirs. An absent or null list stays nil for validate's required-lists check.
type rawFile struct {
	Format         json.RawMessage   `json:"format"`
	Version        json.RawMessage   `json:"version"`
	Children       []json.RawMessage `json:"children"`
	CustomServices []json.RawMessage `json:"custom_services"`
	Buttons        []json.RawMessage `json:"buttons"`
	Toggles        []json.RawMessage `json:"toggles"`
}

// decodeFile strictly decodes a body whose header has passed checkHeader.
func decodeFile(body []byte) (setupFile, *fileProblem) {
	var raw rawFile
	if err := decodeStrict(body, &raw); err != nil {
		return setupFile{}, decodeProblem("", err)
	}
	f := setupFile{Format: setupFileFormat, Version: setupFileVersion}
	var p *fileProblem
	if f.Children, p = decodeRecords[fileChild]("children", raw.Children); p != nil {
		return setupFile{}, p
	}
	if f.CustomServices, p = decodeRecords[fileService]("custom_services", raw.CustomServices); p != nil {
		return setupFile{}, p
	}
	if f.Buttons, p = decodeRecords[fileButton]("buttons", raw.Buttons); p != nil {
		return setupFile{}, p
	}
	if f.Toggles, p = decodeRecords[fileToggle]("toggles", raw.Toggles); p != nil {
		return setupFile{}, p
	}
	return f, nil
}

// decodeRecords decodes one list, naming the record a problem sits in. A nil
// list stays nil; [] stays empty.
func decodeRecords[T any](list string, raw []json.RawMessage) ([]T, *fileProblem) {
	if raw == nil {
		return nil, nil
	}
	out := make([]T, len(raw))
	for i, record := range raw {
		if err := decodeStrict(record, &out[i]); err != nil {
			return nil, decodeProblem(fmt.Sprintf("%s[%d]", list, i), err)
		}
	}
	return out, nil
}

func decodeStrict(data []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}

// checkHeader checks the version first: body `null` has neither field, and is
// reported as having no version.
func checkHeader(format, version json.RawMessage) *fileProblem {
	var v *int
	if len(version) == 0 || json.Unmarshal(version, &v) != nil || v == nil {
		return refuseFile("version: missing or not a number; expected a version %d export", setupFileVersion)
	}
	if *v != setupFileVersion {
		return refuseFile("version %d is not supported; this app reads version %d", *v, setupFileVersion)
	}
	var name string
	if len(format) == 0 || json.Unmarshal(format, &name) != nil || name != setupFileFormat {
		return refuseFile("not an adguard-reward export: format must be %q", setupFileFormat)
	}
	return nil
}

// decodeProblem words a decode failure inside record, which is "" at the top
// level. A record of the wrong type has no field of its own to name.
func decodeProblem(record string, err error) *fileProblem {
	var wrongType *json.UnmarshalTypeError
	if errors.As(err, &wrongType) {
		path := strings.Trim(record+"."+wrongType.Field, ".")
		return badFile("%s: wrong type, found %s", path, wrongType.Value)
	}
	message := strings.TrimPrefix(err.Error(), "json: ")
	if record == "" {
		return badFile("%s", message)
	}
	return badFile("%s: %s", record, message)
}

// validate checks the file top to bottom, trimming as it goes so that every
// reference compares against the value that will be stored.
func (f *setupFile) validate() *fileProblem {
	for _, list := range []struct {
		name    string
		missing bool
	}{
		{"children", f.Children == nil},
		{"custom_services", f.CustomServices == nil},
		{"buttons", f.Buttons == nil},
		{"toggles", f.Toggles == nil},
	} {
		if list.missing {
			return refuseFile("%s: required; an empty list is []", list.name)
		}
	}
	children, p := f.validateChildren()
	if p != nil {
		return p
	}
	services, p := f.validateServices()
	if p != nil {
		return p
	}
	if p := f.validateButtons(children, services); p != nil {
		return p
	}
	return f.validateToggles(children, services)
}

// validateChildren returns the set of trimmed child names. Names collide the
// way the children table's NOCASE column does: ASCII letters only.
func (f *setupFile) validateChildren() (map[string]bool, *fileProblem) {
	names := make(map[string]bool, len(f.Children))
	folded := make(map[string]string, len(f.Children))
	for i := range f.Children {
		c := &f.Children[i]
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" || utf8.RuneCountInString(c.Name) > maxChildName {
			return nil, refuseFile("children[%d].name: must be 1 to %d characters", i, maxChildName)
		}
		key := foldASCII(c.Name)
		if first, taken := folded[key]; taken {
			return nil, refuseFile("children[%d].name: %q duplicates %q", i, c.Name, first)
		}
		folded[key] = c.Name
		names[c.Name] = true

		seen := make(map[string]bool, len(c.Clients))
		for j := range c.Clients {
			client := strings.TrimSpace(c.Clients[j])
			path := fmt.Sprintf("children[%d].clients[%d]", i, j)
			if client == "" {
				return nil, refuseFile("%s: must not be empty", path)
			}
			if len(client) > maxClientName {
				return nil, refuseFile("%s: longer than %d bytes", path, maxClientName)
			}
			if seen[client] {
				return nil, refuseFile("%s: %q is listed twice", path, client)
			}
			seen[client] = true
			c.Clients[j] = client
		}
	}
	return names, nil
}

// foldASCII lowers ASCII letters only, matching SQLite's NOCASE.
func foldASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// validateServices returns the set of valid "custom:<name>" refs. Names collide
// the way custom_services.name_key does: fully lowercased, so unlike children
// non-ASCII letters fold too. A domain may appear once in the whole file.
func (f *setupFile) validateServices() (map[string]bool, *fileProblem) {
	refs := make(map[string]bool, len(f.CustomServices))
	folded := make(map[string]string, len(f.CustomServices))
	domains := map[string]bool{}
	for i := range f.CustomServices {
		s := &f.CustomServices[i]
		s.Name = strings.TrimSpace(s.Name)
		if s.Name == "" || utf8.RuneCountInString(s.Name) > maxCustomServiceName {
			return nil, refuseFile("custom_services[%d].name: must be 1 to %d characters", i, maxCustomServiceName)
		}
		key := strings.ToLower(s.Name)
		if first, taken := folded[key]; taken {
			return nil, refuseFile("custom_services[%d].name: %q duplicates %q", i, s.Name, first)
		}
		folded[key] = s.Name
		refs[customIDPrefix+s.Name] = true

		if len(s.Domains) == 0 || len(s.Domains) > maxCustomDomains {
			return nil, refuseFile("custom_services[%d].domains: must list 1 to %d hostnames", i, maxCustomDomains)
		}
		for j, d := range s.Domains {
			n, err := rules.NormalizeDomain(d)
			if err != nil {
				return nil, refuseFile("custom_services[%d].domains[%d]: %v", i, j, err)
			}
			if domains[n] {
				return nil, refuseFile("custom_services[%d].domains[%d]: domain %q is listed twice", i, j, n)
			}
			domains[n] = true
			s.Domains[j] = n
		}
	}
	return refs, nil
}

func (f *setupFile) validateButtons(children, services map[string]bool) *fileProblem {
	minSeconds := int64(grants.MinDuration / time.Second)
	maxSeconds := int64(grants.MaxDuration / time.Second)
	for i := range f.Buttons {
		b := &f.Buttons[i]
		b.Label = strings.TrimSpace(b.Label)
		if n := utf8.RuneCountInString(b.Label); n == 0 || n > maxLabelRunes {
			return refuseFile("buttons[%d].label: must be 1 to %d characters", i, maxLabelRunes)
		}
		if b.Duration < minSeconds || b.Duration > maxSeconds {
			return refuseFile("buttons[%d].duration: must be between %d and %d seconds", i, minSeconds, maxSeconds)
		}
		b.Child = strings.TrimSpace(b.Child)
		if !children[b.Child] {
			return refuseFile("buttons[%d].child: %q is not in the file", i, b.Child)
		}
		if len(b.Services) == 0 {
			return refuseFile("buttons[%d].services: must name at least one service", i)
		}
		seen := make(map[string]bool, len(b.Services))
		for j := range b.Services {
			ref := strings.TrimSpace(b.Services[j])
			path := fmt.Sprintf("buttons[%d].services[%d]", i, j)
			if p := checkRef(path, ref, services); p != nil {
				return p
			}
			if seen[ref] {
				return refuseFile("%s: %q is listed twice", path, ref)
			}
			seen[ref] = true
			b.Services[j] = ref
		}
		b.Icon = strings.TrimSpace(b.Icon)
		if b.Icon != "" {
			if p := checkRef(fmt.Sprintf("buttons[%d].icon", i), b.Icon, services); p != nil {
				return p
			}
		}
	}
	return nil
}

func (f *setupFile) validateToggles(children, services map[string]bool) *fileProblem {
	seen := make(map[[2]string]bool, len(f.Toggles))
	for i := range f.Toggles {
		t := &f.Toggles[i]
		t.Child = strings.TrimSpace(t.Child)
		if !children[t.Child] {
			return refuseFile("toggles[%d].child: %q is not in the file", i, t.Child)
		}
		t.Service = strings.TrimSpace(t.Service)
		if p := checkRef(fmt.Sprintf("toggles[%d].service", i), t.Service, services); p != nil {
			return p
		}
		if t.State != store.ToggleBlocked && t.State != store.ToggleUnblocked {
			return refuseFile("toggles[%d].state: must be %q or %q", i, store.ToggleBlocked, store.ToggleUnblocked)
		}
		key := [2]string{t.Child, t.Service}
		if seen[key] {
			return refuseFile("toggles[%d]: %q already has a toggle for %q", i, t.Child, t.Service)
		}
		seen[key] = true
	}
	return nil
}

// checkRef refuses an empty ref and a custom ref naming no service in the
// file. A built-in id is only checked against the live catalogue, by the
// caller, through builtinRefs.
func checkRef(path, ref string, services map[string]bool) *fileProblem {
	if ref == "" {
		return refuseFile("%s: must name a service", path)
	}
	if strings.HasPrefix(ref, customIDPrefix) && !services[ref] {
		return refuseFile("%s: %q is not in the file", path, ref)
	}
	return nil
}

// builtinRefs lists every built-in service id f uses, in first-use order and
// without repeats: button services, button icons and toggles. Never nil.
func builtinRefs(f setupFile) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(ref string) {
		if ref == "" || strings.HasPrefix(ref, customIDPrefix) || seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	for _, b := range f.Buttons {
		for _, s := range b.Services {
			add(s)
		}
		add(b.Icon)
	}
	for _, t := range f.Toggles {
		add(t.Service)
	}
	return out
}
