package grants

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/rules"
	"github.com/Rivil/adguard-reward/internal/store"
)

// Stats summarises one reconcile pass. Writes counts the Add/Remove calls
// issued; each costs at most one POST, and none when the list already
// matched. RulesWritten is 1 when the pass rewrote the managed user_rules
// region, else 0.
type Stats struct {
	Grants       int // active grants examined
	Reverted     int // overdue grants reverted and marked expired
	Repaired     int // clients whose drifted blocked_services were converged, counted once per half
	Writes       int
	RulesWritten int
}

// Reconcile converges AdGuard with the active grants in one pass, under the
// engine mutex: every grant past ends_at is reverted and marked expired
// (exactly as the timer would, ref-counted against the other active grants),
// and for every live grant each stored client is read live and any granted
// id that has reappeared in its blocked_services — a parent re-blocked it in
// AdGuard's UI — is removed again. Live grants without an armed timer get
// one (post-restart). The grant half reads AdGuard's clients once plus one
// re-read inside each Add/Remove; a pass that finds nothing to do issues no
// writes. A failed clients read skips the grant half with no status change.
//
// The toggle half follows (locked drift_enforcement): every built-in
// service a child's owner toggled is converged on that child's clients to
// the stored intent — open-wins against live grants and co-owners, as in
// togglePlan — so a parent undoing a permanent toggle in AdGuard's UI, or a
// toggle write that failed part-way, is corrected in one pass. Services no
// one toggled stay AdGuard's. It re-reads the active grants, since the grant
// half may just have expired some; any failed read skips it with nothing
// written.
//
// Every pass — with or without grants, and even when the clients read
// failed — then converges the managed user_rules region to intent (stored
// custom services, children, toggles and active grants), undoing a restart
// gap, a lagging write or a manual edit inside the markers. A malformed
// region is logged at error and left untouched; the grant and toggle halves
// still ran.
func (e *Engine) Reconcile(ctx context.Context) (Stats, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	grants, err := e.store.ListActiveGrants(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("reconcile: list grants: %w", err)
	}
	st := Stats{Grants: len(grants)}

	// One ApplyTimeout budget for every AdGuard call of the pass. Unlike a
	// request-driven operation this one follows ctx's cancellation: an
	// aborted pass is safe, the next one redoes it.
	actx, cancel := context.WithTimeout(ctx, e.applyTimeout)
	defer cancel()

	var passErr error
	if len(grants) > 0 {
		if err := e.reconcileGrants(ctx, actx, grants, &st); err != nil {
			passErr = err
		}
	}
	if err := e.reconcileToggles(ctx, actx, &st); err != nil {
		e.log.Warn("reconcile: permanent toggles not converged", "err", err)
		if passErr == nil {
			passErr = err
		}
	}

	changed, err := e.syncRules(ctx, actx, 0)
	switch {
	case errors.Is(err, rules.ErrMalformedRegion):
		e.log.Error("reconcile: managed rules region is malformed; not rewritten — fix the adguard-reward markers in AdGuard's custom rules", "err", err)
	case err != nil:
		e.log.Warn("reconcile: managed rules not converged", "err", err)
		if passErr == nil {
			passErr = fmt.Errorf("reconcile: %w", err)
		}
	case changed:
		st.RulesWritten = 1
		e.log.Info("reconcile: managed rules region rewritten")
	}
	return st, passErr
}

// reconcileGrants is the grant half of a pass: overdue grants expire, live
// grants get drifted built-in ids removed again and a timer armed. A failed
// clients read returns the error with nothing done. Caller holds mu.
func (e *Engine) reconcileGrants(ctx, actx context.Context, grants []store.Grant, st *Stats) error {
	res, err := e.ag.Clients(actx)
	if err != nil {
		return fmt.Errorf("reconcile: read clients: %w", err)
	}
	byName := make(map[string]adguard.PersistentClient, len(res.Persistent))
	for _, pc := range res.Persistent {
		byName[pc.Name] = pc
	}

	now := e.now()
	for _, g := range grants {
		if !now.Before(g.EndsAt) {
			// Through the same ref-counted revert as the timer, which
			// re-reads the active grants itself: the snapshot above goes
			// stale as soon as an earlier overdue grant in this loop expires.
			expired, writes := e.expireLocked(ctx, actx, g)
			st.Writes += writes
			if expired {
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
	return nil
}

// reconcileToggles is the toggle half of a pass. It reads the children,
// toggles and — afresh, after the grant half — the active grants, plans
// with togglePlan, and diffs the plan against one read of AdGuard's clients
// so that only drifted clients reach applyPlan; a converged pass issues no
// write and, with nothing toggled, no AdGuard request at all. A pass with
// drift reads the clients twice — applyPlan re-reads just before writing,
// exactly as Toggle does — which is accepted: drift is rare and the second
// read only precedes a write. A client AdGuard does not know, or one
// inheriting the global blocked-services list (the toggle reported it
// failed; it converges once migrated), is skipped without a write. A failed
// read returns the error with nothing written; a failed client write is
// logged and retried next pass. Caller holds mu.
func (e *Engine) reconcileToggles(ctx, actx context.Context, st *Stats) error {
	children, toggles, err := e.readToggles(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: toggles: %w", err)
	}
	active, err := e.store.ListActiveGrants(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: toggles: read active grants: %w", err)
	}
	plan := togglePlan(children, toggles, active)
	if len(plan) == 0 {
		return nil
	}
	res, err := e.ag.Clients(actx)
	if err != nil {
		return fmt.Errorf("reconcile: toggles: read clients: %w", err)
	}
	byName := make(map[string]adguard.PersistentClient, len(res.Persistent))
	for _, pc := range res.Persistent {
		byName[pc.Name] = pc
	}
	drifted := map[string]clientPlan{}
	for name, p := range plan {
		pc, ok := byName[name]
		switch {
		case !ok:
			e.log.Debug("reconcile: toggled client not in AdGuard, skipped", "client", name)
			continue
		case pc.UseGlobalBlockedServices:
			e.log.Debug("reconcile: toggled client uses the global blocked-services list, skipped", "client", name)
			continue
		}
		d := clientPlan{
			remove: intersect(p.remove, pc.BlockedServices),
			add:    slices.DeleteFunc(slices.Clone(p.add), func(s string) bool { return slices.Contains(pc.BlockedServices, s) }),
		}
		if len(d.remove) > 0 || len(d.add) > 0 {
			drifted[name] = d
		}
	}
	failed, writes := e.applyPlan(actx, drifted)
	st.Writes += writes
	for name, d := range drifted {
		if slices.Contains(failed, name) {
			continue
		}
		st.Repaired++
		e.log.Info("reconcile: permanent toggles converged", "client", name, "unblocked", d.remove, "blocked", d.add)
	}
	if len(failed) > 0 {
		e.log.Warn("reconcile: permanent toggles not converged on some clients; retried next pass", "clients", failed)
	}
	return nil
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
	e.log.Info("startup reconcile", "grants", st.Grants, "reverted", st.Reverted, "repaired", st.Repaired,
		"rules_written", st.RulesWritten)
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
			// if/else rather than a two-arm switch: Go's coverage profile
			// starts a block *after* a `case` colon, so a switch condition
			// sits outside every block and a mutation tester can never reach
			// it. These two conditions decide which line a pass emits, and
			// the log is the only place that decision is observable.
			if err != nil && ctx.Err() == nil {
				e.log.Error("reconcile failed", "err", err)
			} else if st.Reverted > 0 || st.Repaired > 0 || st.RulesWritten > 0 {
				e.log.Info("reconcile", "grants", st.Grants, "reverted", st.Reverted, "repaired", st.Repaired,
					"rules_written", st.RulesWritten)
			}
		}
	}
}

// compile-time check that the real store and client satisfy the interfaces.
var (
	_ Store   = (*store.Store)(nil)
	_ AdGuard = (*adguard.Client)(nil)
)
