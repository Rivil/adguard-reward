package rules

import (
	"log/slog"
	"slices"
	"strings"
)

// Service is a custom service as the region sees it: its catalogue id
// ("custom:<n>") and its domains.
type Service struct {
	ID      string
	Domains []string
}

// Cover says an active grant unblocks ServiceID on Clients (persistent
// client names).
type Cover struct {
	ServiceID string
	Clients   []string
}

// QuoteClient renders a persistent client name as one single-quoted value of
// AdGuard's $client modifier, or reports false when no quoting survives
// urlfilter's decoding.
//
// The escapes follow urlfilter's parser (github.com/AdguardTeam/urlfilter at
// 347285f3f046, rules/network.go and rules/rule.go), not intuition: the options delimiter is the last `$` not preceded by `\`,
// so `$` becomes `\$`; options split on unescaped `,` and the client list on
// unescaped `|`, so both are backslash-escaped; inside single quotes only `\'`
// is unescaped. `"` and `\` are therefore written bare — escaping them would
// leave a literal backslash in the decoded name and the rule would never match.
//
// Refused: an empty name, a newline or carriage return (it would split the
// rule across lines), a backslash directly before ' , | or $ (urlfilter
// would eat it as an escape) and a trailing backslash (it would sit against
// the closing quote and read as an escaped one).
func QuoteClient(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, "\r\n") || strings.HasSuffix(name, `\`) {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(name) + 4)
	b.WriteByte('\'')
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch c {
		case '\\':
			if i+1 < len(name) && strings.IndexByte(`',|$`, name[i+1]) >= 0 {
				return "", false
			}
		case '\'', ',', '|', '$':
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	b.WriteByte('\'')
	return b.String(), true
}

// Render returns the managed region's body (no markers), deterministic for
// any input order: one `||domain^$client=...` block line per domain over the
// union of mapped child clients, then one `@@||domain^$client=...` exception
// line per domain over the union of clients that active grants cover that
// service on. Each group is sorted by service id, then domain.
//
// A line is never emitted with an empty client list: an unscoped `||d^` would
// block adults too (decision block_scope). Client names QuoteClient refuses
// and stored domains NormalizeDomain rejects are skipped and logged to log
// (nil discards).
func Render(services []Service, childClients []string, covers []Cover, log *slog.Logger) []string {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	svcs := slices.Clone(services)
	slices.SortFunc(svcs, func(a, b Service) int { return strings.Compare(a.ID, b.ID) })

	covered := map[string][]string{}
	for _, c := range covers {
		covered[c.ServiceID] = append(covered[c.ServiceID], c.Clients...)
	}

	block := clientList(childClients, log)
	var blocks, excepts []string
	for _, s := range svcs {
		domains := domainList(s, log)
		except := clientList(covered[s.ID], log)
		for _, d := range domains {
			if block != "" {
				blocks = append(blocks, "||"+d+"^$client="+block)
			}
			if except != "" {
				excepts = append(excepts, "@@||"+d+"^$client="+except)
			}
		}
	}
	return append(append(make([]string, 0, len(blocks)+len(excepts)), blocks...), excepts...)
}

// clientList quotes the distinct names in sorted order and joins them with
// `|`; "" when none survive.
func clientList(names []string, log *slog.Logger) string {
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	quoted := make([]string, 0, len(sorted))
	for _, n := range sorted {
		q, ok := QuoteClient(n)
		if !ok {
			log.Warn("rules: client name cannot be targeted by a $client rule; skipped", "client", n)
			continue
		}
		quoted = append(quoted, q)
	}
	return strings.Join(quoted, "|")
}

// domainList returns s's valid domains, normalised, sorted and distinct.
func domainList(s Service, log *slog.Logger) []string {
	out := make([]string, 0, len(s.Domains))
	for _, d := range s.Domains {
		n, err := NormalizeDomain(d)
		if err != nil {
			log.Error("rules: stored domain is not a plain hostname; skipped", "service", s.ID, "err", err)
			continue
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
