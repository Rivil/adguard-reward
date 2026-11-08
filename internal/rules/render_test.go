package rules

import (
	"bytes"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

// decodeClients mirrors how AdGuard's urlfilter reads the $client values of a
// network rule, transcribed from github.com/AdguardTeam/urlfilter at
// 347285f3f046 (2026-08-19): rules/network.go splitPatternAndOptions,
// findOptionsDelimiter and loadOptions; rules/rule.go parseClients,
// parseClient and isQuoted; rules/rules.go splitWithEscapeCharacter. It is the
// oracle QuoteClient is checked against. urlfilter is not imported: r-01 keeps
// AdGuard code out of this module, so re-check this transcription when
// AdGuard changes its $client grammar.
func decodeClients(t *testing.T, line string) []string {
	t.Helper()
	line = strings.TrimPrefix(line, "@@")
	idx := -1
	for i := len(line) - 1; i >= 0; i-- {
		if line[i] == '$' && !(i > 0 && line[i-1] == '\\') {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("no options delimiter in %q", line)
	}
	opts := strings.ReplaceAll(line[idx+1:], `\$`, "$")
	var value string
	found := false
	for _, o := range splitEscaped(opts, ',') {
		if v, ok := strings.CutPrefix(o, "client="); ok {
			value, found = v, true
		}
	}
	if !found {
		t.Fatalf("no client option in %q", line)
	}
	var out []string
	for _, c := range splitEscaped(value, '|') {
		var q byte
		if len(c) >= 2 && (c[0] == '\'' || c[0] == '"') && c[0] == c[len(c)-1] {
			q, c = c[0], c[1:len(c)-1]
		}
		c = strings.ReplaceAll(c, `\,`, ",")
		if q != 0 {
			c = strings.ReplaceAll(c, `\`+string(q), string(q))
		}
		out = append(out, c)
	}
	return out
}

func splitEscaped(s string, sep byte) []string {
	var parts []string
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == sep:
			sb.WriteByte(sep)
			i++
		case s[i] == sep:
			if sb.Len() > 0 {
				parts = append(parts, sb.String())
				sb.Reset()
			}
		default:
			sb.WriteByte(s[i])
		}
	}
	if sb.Len() > 0 {
		parts = append(parts, sb.String())
	}
	return parts
}

func TestQuoteClient(t *testing.T) {
	exact := map[string]string{
		"kid-phone":          `'kid-phone'`,
		`Ann's "tab", |x`:    `'Ann\'s "tab"\, \|x'`,
		`a\b`:                `'a\b'`,
		`a$b`:                `'a\$b'`,
		`$`:                  `'\$'`,
		"Kid's iPad (ünï) ~": `'Kid\'s iPad (ünï) ~'`,
		`~Mom`:               `'~Mom'`,
	}
	for name, want := range exact {
		got, ok := QuoteClient(name)
		if !ok || got != want {
			t.Errorf("QuoteClient(%q) = %q, %v; want %q", name, got, ok, want)
			continue
		}
		for _, prefix := range []string{"||a.com^$client=", "@@||a.com^$client='other'|"} {
			dec := decodeClients(t, prefix+got)
			if dec[len(dec)-1] != name {
				t.Errorf("%q decodes to %q via urlfilter rules, want %q", prefix+got, dec[len(dec)-1], name)
			}
		}
	}

	for _, name := range []string{"", "a\nb", "a\rb", "tab\n", `a\'b`, `a\,b`, `a\|b`, `a\$b`, `trailing\`, `\`} {
		if got, ok := QuoteClient(name); ok {
			t.Errorf("QuoteClient(%q) = %q, true; want refused", name, got)
		}
	}
}

func TestRender_Block(t *testing.T) {
	got := Render([]Service{{ID: "custom:1", Domains: []string{"a.com", "b.com"}}},
		[]string{"kid-phone", "tv"}, nil, nil)
	want := []string{
		"||a.com^$client='kid-phone'|'tv'",
		"||b.com^$client='kid-phone'|'tv'",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if dec := decodeClients(t, got[0]); !reflect.DeepEqual(dec, []string{"kid-phone", "tv"}) {
		t.Errorf("decoded clients %q", dec)
	}
}

func TestRender_NoClients(t *testing.T) {
	svcs := []Service{{ID: "custom:1", Domains: []string{"a.com"}}}
	if got := Render(svcs, nil, nil, nil); len(got) != 0 {
		t.Fatalf("no child clients: got %q, want no lines", got)
	}
	if got := Render(svcs, []string{"bad\nname"}, nil, nil); len(got) != 0 {
		t.Fatalf("only unquotable clients: got %q, want no lines", got)
	}
	got := Render(svcs, nil, []Cover{{ServiceID: "custom:1", Clients: []string{"tv"}}}, nil)
	if want := []string{"@@||a.com^$client='tv'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cover without children: got %q want %q", got, want)
	}
	for _, l := range got {
		if !strings.Contains(l, "$client=") {
			t.Errorf("unscoped line %q", l)
		}
	}
}

func TestRender_DropsUnquotableClient(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	got := Render([]Service{{ID: "custom:1", Domains: []string{"a.com"}}},
		[]string{"kid\nphone", "tv"}, nil, log)
	if want := []string{"||a.com^$client='tv'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
	for _, l := range got {
		if strings.ContainsAny(l, "\r\n") {
			t.Fatalf("line split: %q", l)
		}
	}
	if !strings.Contains(buf.String(), "skipped") {
		t.Errorf("drop not logged: %q", buf.String())
	}
}

func TestRender_SkipsInvalidStoredDomain(t *testing.T) {
	got := Render([]Service{{ID: "custom:1", Domains: []string{"a.com", "||evil^$important", "B.com."}}},
		[]string{"kid"}, nil, nil)
	want := []string{"||a.com^$client='kid'", "||b.com^$client='kid'"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRender_Cover(t *testing.T) {
	svcs := []Service{{ID: "custom:1", Domains: []string{"a.com", "b.com"}}}
	got := Render(svcs, []string{"kid-phone", "tv"},
		[]Cover{{ServiceID: "custom:1", Clients: []string{"kid-phone", "tv"}}}, nil)
	want := []string{
		"||a.com^$client='kid-phone'|'tv'",
		"||b.com^$client='kid-phone'|'tv'",
		"@@||a.com^$client='kid-phone'|'tv'",
		"@@||b.com^$client='kid-phone'|'tv'",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	// A cover on another service adds nothing here.
	other := Render(svcs, []string{"kid-phone", "tv"},
		[]Cover{{ServiceID: "custom:2", Clients: []string{"tv"}}}, nil)
	if len(other) != 2 {
		t.Fatalf("foreign cover leaked: %q", other)
	}
}

func TestRender_SharedCover(t *testing.T) {
	svcs := []Service{{ID: "custom:1", Domains: []string{"a.com"}}}
	kids := []string{"a-phone", "b-phone", "tv"}
	a := Cover{ServiceID: "custom:1", Clients: []string{"a-phone", "tv"}}
	b := Cover{ServiceID: "custom:1", Clients: []string{"b-phone", "tv"}}

	exceptions := func(covers ...Cover) []string {
		var out []string
		for _, l := range Render(svcs, kids, covers, nil) {
			if strings.HasPrefix(l, "@@") {
				out = append(out, l)
			}
		}
		return out
	}
	if got, want := exceptions(a, b), []string{"@@||a.com^$client='a-phone'|'b-phone'|'tv'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("A+B: got %q want %q", got, want)
	}
	if got, want := exceptions(b), []string{"@@||a.com^$client='b-phone'|'tv'"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("only B: got %q want %q", got, want)
	}
	if got := exceptions(); len(got) != 0 {
		t.Fatalf("no covers: got %q", got)
	}
}

func TestRender_Deterministic(t *testing.T) {
	svcs := []Service{
		{ID: "custom:2", Domains: []string{"z.com", "m.com"}},
		{ID: "custom:1", Domains: []string{"b.com", "a.com"}},
		{ID: "custom:10", Domains: []string{"q.com"}},
	}
	kids := []string{"tv", "kid-phone", "a-tab", "tv"}
	covers := []Cover{
		{ServiceID: "custom:1", Clients: []string{"tv", "kid-phone"}},
		{ServiceID: "custom:2", Clients: []string{"a-tab"}},
		{ServiceID: "custom:1", Clients: []string{"a-tab", "tv"}},
	}
	want := Render(svcs, kids, covers, nil)
	if len(want) == 0 {
		t.Fatal("empty render")
	}
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 100 {
		s := cloneServices(svcs)
		for j := range s {
			r.Shuffle(len(s[j].Domains), func(a, b int) { s[j].Domains[a], s[j].Domains[b] = s[j].Domains[b], s[j].Domains[a] })
		}
		r.Shuffle(len(s), func(a, b int) { s[a], s[b] = s[b], s[a] })
		k := append([]string(nil), kids...)
		r.Shuffle(len(k), func(a, b int) { k[a], k[b] = k[b], k[a] })
		c := make([]Cover, len(covers))
		for j, cv := range covers {
			c[j] = Cover{ServiceID: cv.ServiceID, Clients: append([]string(nil), cv.Clients...)}
			r.Shuffle(len(c[j].Clients), func(a, b int) { c[j].Clients[a], c[j].Clients[b] = c[j].Clients[b], c[j].Clients[a] })
		}
		r.Shuffle(len(c), func(a, b int) { c[a], c[b] = c[b], c[a] })
		if got := Render(s, k, c, nil); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d differs:\n got  %q\n want %q", i, got, want)
		}
	}
	// Blocks precede exceptions.
	seenExcept := false
	for _, l := range want {
		if strings.HasPrefix(l, "@@") {
			seenExcept = true
		} else if seenExcept {
			t.Fatalf("block line after an exception: %q", want)
		}
	}
}

func cloneServices(in []Service) []Service {
	out := make([]Service, len(in))
	for i, s := range in {
		out[i] = Service{ID: s.ID, Domains: append([]string(nil), s.Domains...)}
	}
	return out
}
