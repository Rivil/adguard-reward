package grants

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/store"
)

// Stats summarises one reconcile pass. Writes counts the Add/Remove calls
// issued; each costs at most one POST, and none when the list already
// matched.
type Stats struct {
	Grants   int // active grants examined
	Reverted int // overdue grants reverted and marked expired
	Repaired int // clients whose drifted blocked_services were re-unblocked
	Writes   int
}

// Reconcile converges AdGuard with the active grants in one pass, under the
// engine mutex: every grant past ends_at is reverted and marked expired
// (exactly as the timer would), and for every live grant each stored client
// is read live and any granted id that has reappeared in its blocked_services
// — a parent re-blocked it in AdGuard's UI — is removed again. Live grants
// without an armed timer get one (post-restart). The pass reads AdGuard once
// for itself plus one re-read inside each Add/Remove; a pass that finds
// nothing to do issues no writes. A failed read aborts the pass with no
// status change and no writes.
func (e *Engine) Reconcile(ctx context.Context) (Stats, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	grants, err := e.store.ListActiveGrants(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("reconcile: list grants: %w", err)
	}
	st := Stats{Grants: len(grants)}
	if len(grants) == 0 {
		return st, nil
	}

	// One ApplyTimeout budget for every AdGuard call of the pass. Unlike a
	// request-driven operation this one follows ctx's cancellation: an
	// aborted pass is safe, the next one redoes it.
	actx, cancel := context.WithTimeout(ctx, e.applyTimeout)
	defer cancel()
	res, err := e.ag.Clients(actx)
	if err != nil {
		return st, fmt.Errorf("reconcile: read clients: %w", err)
	}
	byName := make(map[string]adguard.PersistentClient, len(res.Persistent))
	for _, pc := range res.Persistent {
		byName[pc.Name] = pc
	}

	now := e.now()
	for _, g := range grants {
		if !now.Before(g.EndsAt) {
			st.Writes += len(g.Clients)
			if e.expireLocked(ctx, actx, g) {
				st.Reverted++
			}
			continue
		}
		for _, name := range g.Clients {
			pc, ok := byName[name]
			if !ok {
				e.log.Debug("reconcile: client not in AdGuard, skipped", "grant", g.ID, "client", name)
				continue
			}
			if pc.UseGlobalBlockedServices {
				// Its own list is irrelevant while the flag is set and the
				// global list is never written; Create already reported it.
				continue
			}
			drifted := intersect(g.Services, pc.BlockedServices)
			if len(drifted) == 0 {
				continue
			}
			st.Writes++
			if err := e.ag.RemoveBlockedServices(actx, name, drifted); err != nil {
				e.log.Warn("reconcile: drift repair failed", "grant", g.ID, "client", name, "err", err)
				continue
			}
			e.log.Info("reconcile: re-blocked service removed again", "grant", g.ID, "client", name, "services", drifted)
			st.Repaired++
		}
		if _, armed := e.timers[g.ID]; !armed {
			e.arm(g)
		}
	}
	return st, nil
}

// intersect returns the members of want present in have, in want's order.
func intersect(want, have []string) []string {
	var out []string
	for _, id := range want {
		if slices.Contains(have, id) {
			out = append(out, id)
		}
	}
	return out
}

// Start runs the startup pass. Main calls it before the listener opens so a
// grant that expired while the process was down is reverted before any
// request is served; the error is for main to log, never fatal.
func (e *Engine) Start(ctx context.Context) error {
	st, err := e.Reconcile(ctx)
	if err != nil {
		return err
	}
	e.log.Info("startup reconcile", "grants", st.Grants, "reverted", st.Reverted, "repaired", st.Repaired)
	return nil
}

// Run reconciles every interval until ctx is done. Passes never overlap —
// each holds the engine mutex — and a failing pass is logged, not fatal.
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st, err := e.Reconcile(ctx)
			switch {
			case err != nil && ctx.Err() == nil:
				e.log.Error("reconcile failed", "err", err)
			case st.Reverted > 0 || st.Repaired > 0:
				e.log.Info("reconcile", "grants", st.Grants, "reverted", st.Reverted, "repaired", st.Repaired)
			}
		}
	}
}

// compile-time check that the real store and client satisfy the interfaces.
var (
	_ Store   = (*store.Store)(nil)
	_ AdGuard = (*adguard.Client)(nil)
)
