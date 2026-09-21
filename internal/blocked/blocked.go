// Package blocked folds AdGuard's per-client blocked-services lists into a
// per-child view, and computes the one-time offer that moves a child's
// clients off the global list. It is pure: no I/O, no store, no clock.
package blocked

import (
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
type Input struct {
	Clients  []adguard.PersistentClient
	Global   []string
	Services []adguard.Service
	Mapped   []string
}

// ClientState is one mapped client's presence in AdGuard. A missing client
// is reported, never dropped (locked: dangling_client); a client on the
// global list is flagged rather than hidden (locked: global_list_clients).
type ClientState struct {
	Name       string
	Missing    bool
	UsesGlobal bool
}

// ServiceState is one catalogue service's fold. Differs lists the present
// clients on which the service is NOT blocked, sorted; it is empty (never
// nil) unless State is partial.
type ServiceState struct {
	ID      string
	Name    string
	Icon    string
	State   string
	Differs []string
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
			view.Clients = append(view.Clients, ClientState{Name: name, Missing: true})
			continue
		}
		view.Clients = append(view.Clients, ClientState{Name: name, UsesGlobal: pc.UseGlobalBlockedServices})
		set := globalSet
		if !pc.UseGlobalBlockedServices {
			set = toSet(pc.BlockedServices)
		}
		presents = append(presents, present{name: name, set: set})
	}

	for _, svc := range in.Services {
		st := ServiceState{ID: svc.ID, Name: svc.Name, Icon: svc.Icon, Differs: []string{}}
		blockedOn := 0
		for _, p := range presents {
			if p.set[svc.ID] {
				blockedOn++
			} else {
				st.Differs = append(st.Differs, p.name)
			}
		}
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
		view.Services = append(view.Services, st)
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
