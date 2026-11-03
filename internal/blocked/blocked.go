// Package blocked folds AdGuard's per-client blocked-services lists into a
// per-child view, and computes the one-time offer that moves a child's
// clients off the global list. It is pure: no I/O, no store, no clock.
package blocked

import (
	"slices"
	"sort"

	"github.com/Rivil/adguard-reward/internal/adguard"
)

// State of one service across a child's present clients.
const (
	StateBlocked   = "blocked"   // every present client blocks it
	StatePartial   = "partial"   // some do, some do not
	StateUnblocked = "unblocked" // none does (or there are no present clients)
)

// Input is everything Compute needs, read from AdGuard and the store by the
// caller: the live persistent clients, the global list, the catalogue, and
// the child's mapped client names in stored order.
//
// Self is the viewing child's id. Owners maps a client name to every child
// that holds it; Grants are the active grants of every child, each with the
// clients stored on it. Both may be nil — the view then carries no
// shared/unblocked-by annotations.
//
// Custom are the stored custom services, folded after the catalogue. Their
// state comes from intent, not from AdGuard's user_rules: on each present
// client a custom service is unblocked iff an active grant in Grants (of any
// child) covers it there, else blocked. So Custom needs Grants to be the
// complete set of active grants; a caller that could not read them passes no
// Custom rather than report a false "blocked".
type Input struct {
	Clients  []adguard.PersistentClient
	Global   []string
	Services []adguard.Service
	Custom   []CustomService
	Mapped   []string
	Self     int64
	Owners   map[string][]ChildRef
	Grants   []GrantCover
}

// CustomService is a stored custom service as the fold needs it: its
// catalogue id ("custom:<n>") and name. It has no icon.
type CustomService struct {
	ID   string
	Name string
}

// ChildRef names a child.
type ChildRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// GrantCover is an active grant as the fold needs it: who it is for, and
// which services it opened on which (stored) clients.
type GrantCover struct {
	ID       int64
	Child    ChildRef
	Services []string
	Clients  []string
}

// Unblock attributes an open service on a client to another child's grant.
type Unblock struct {
	Client  string   `json:"client"`
	GrantID int64    `json:"grant_id"`
	Child   ChildRef `json:"child"`
}

// ClientState is one mapped client's presence in AdGuard. A missing client
// is reported, never dropped (locked: dangling_client); a client on the
// global list is flagged rather than hidden (locked: global_list_clients).
// SharedWith lists the client's other owners, id ascending, never nil.
type ClientState struct {
	Name       string
	Missing    bool
	UsesGlobal bool
	SharedWith []ChildRef
}

// ServiceState is one catalogue service's fold. Differs lists the present
// clients on which the service is NOT blocked, sorted; it is empty (never
// nil) unless State is partial. UnblockedBy names, per present client that
// does not block the service, each active grant of ANOTHER child covering
// the service on that client — sorted by (client, grant id), never nil.
type ServiceState struct {
	ID          string
	Name        string
	Icon        string
	Custom      bool
	State       string
	Differs     []string
	UnblockedBy []Unblock
}

// View is Compute's result; both slices are never nil.
type View struct {
	Clients  []ClientState
	Services []ServiceState
}

// Compute folds the input. A client whose use_global_blocked_services is set
// has its effective set taken from Global; every other client from its own
// list. Ids blocked on a client but absent from the catalogue are ignored —
// the catalogue is the view.
func Compute(in Input) View {
	byName := make(map[string]*adguard.PersistentClient, len(in.Clients))
	for i := range in.Clients {
		byName[in.Clients[i].Name] = &in.Clients[i]
	}
	globalSet := toSet(in.Global)

	// present holds each present mapped client's name and effective set.
	type present struct {
		name string
		set  map[string]bool
	}
	view := View{Clients: make([]ClientState, 0, len(in.Mapped)), Services: []ServiceState{}}
	var presents []present
	for _, name := range in.Mapped {
		pc, ok := byName[name]
		if !ok {
			view.Clients = append(view.Clients, ClientState{Name: name, Missing: true, SharedWith: sharedWith(in, name)})
			continue
		}
		view.Clients = append(view.Clients, ClientState{
			Name: name, UsesGlobal: pc.UseGlobalBlockedServices, SharedWith: sharedWith(in, name),
		})
		set := globalSet
		if !pc.UseGlobalBlockedServices {
			set = toSet(pc.BlockedServices)
		}
		presents = append(presents, present{name: name, set: set})
	}

	// fold computes one service's state over the present clients;
	// blocked reports whether p blocks it.
	fold := func(st ServiceState, blocked func(p present) bool) ServiceState {
		st.Differs, st.UnblockedBy = []string{}, []Unblock{}
		blockedOn := 0
		for _, p := range presents {
			if blocked(p) {
				blockedOn++
				continue
			}
			st.Differs = append(st.Differs, p.name)
			st.UnblockedBy = append(st.UnblockedBy, unblockedBy(in, p.name, st.ID)...)
		}
		sort.Slice(st.UnblockedBy, func(i, j int) bool {
			a, b := st.UnblockedBy[i], st.UnblockedBy[j]
			if a.Client != b.Client {
				return a.Client < b.Client
			}
			return a.GrantID < b.GrantID
		})
		switch {
		case len(presents) > 0 && blockedOn == len(presents):
			st.State = StateBlocked
			st.Differs = []string{}
		case blockedOn == 0:
			st.State = StateUnblocked
			st.Differs = []string{}
		default:
			st.State = StatePartial
			sort.Strings(st.Differs)
		}
		return st
	}

	for _, svc := range in.Services {
		view.Services = append(view.Services, fold(
			ServiceState{ID: svc.ID, Name: svc.Name, Icon: svc.Icon},
			func(p present) bool { return p.set[svc.ID] },
		))
	}
	for _, svc := range in.Custom {
		view.Services = append(view.Services, fold(
			ServiceState{ID: svc.ID, Name: svc.Name, Custom: true},
			func(p present) bool { return !covered(in.Grants, svc.ID, p.name) },
		))
	}
	return view
}

// Step is one client's migration off the global list: Gains are the global
// ids its own list lacks, Target the list to write (own ∪ global, sorted).
// A client with nothing to gain is still a step — its flag must flip.
type Step struct {
	Client string
	Gains  []string
	Target []string
}

// Offer lists, in mapped order, every mapped and present client that still
// uses the global list. It is empty when the global list is empty: there is
// nothing to migrate away from.
func Offer(clients []adguard.PersistentClient, global, mapped []string) []Step {
	steps := []Step{}
	if len(global) == 0 {
		return steps
	}
	byName := make(map[string]*adguard.PersistentClient, len(clients))
	for i := range clients {
		byName[clients[i].Name] = &clients[i]
	}
	for _, name := range mapped {
		pc, ok := byName[name]
		if !ok || !pc.UseGlobalBlockedServices {
			continue
		}
		own := toSet(pc.BlockedServices)
		target := toSet(pc.BlockedServices)
		gains := []string{}
		for _, id := range global {
			if !own[id] {
				gains = append(gains, id)
			}
			target[id] = true
		}
		steps = append(steps, Step{Client: name, Gains: sortedUnique(gains), Target: keys(target)})
	}
	return steps
}

// sharedWith returns client's owners other than in.Self, id ascending.
func sharedWith(in Input, client string) []ChildRef {
	out := []ChildRef{}
	for _, o := range in.Owners[client] {
		if o.ID != in.Self {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// unblockedBy returns the active grants of other children that cover
// service on client (per the grant's stored clients).
func unblockedBy(in Input, client, service string) []Unblock {
	var out []Unblock
	for _, g := range in.Grants {
		if g.Child.ID == in.Self || !slices.Contains(g.Services, service) || !slices.Contains(g.Clients, client) {
			continue
		}
		out = append(out, Unblock{Client: client, GrantID: g.ID, Child: g.Child})
	}
	return out
}

// covered reports whether any active grant (of any child) covers service on
// client, per the grant's stored clients.
func covered(grants []GrantCover, service, client string) bool {
	for _, g := range grants {
		if slices.Contains(g.Services, service) && slices.Contains(g.Clients, client) {
			return true
		}
	}
	return false
}

func toSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func keys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedUnique(ids []string) []string {
	return keys(toSet(ids))
}
