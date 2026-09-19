// Package health probes AdGuard Home and serves /healthz.
//
// /healthz is liveness for systemd and Docker, so it is always 200; AdGuard's
// reachability is reported in the JSON body and never through the status
// code. The handler reads stored state only and never waits on AdGuard.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
)

// StatusClient is the slice of *adguard.Client the prober needs.
type StatusClient interface {
	Status(ctx context.Context) (adguard.Status, error)
}

// State is the AdGuard reachability enum reported in /healthz. It is closed:
// before the first probe the state is unreachable, not "unknown".
type State string

const (
	StateOK           State = "ok"
	StateUnauthorized State = "unauthorized"
	StateUnreachable  State = "unreachable"
)

// Result is one probe's outcome.
type Result struct {
	State   State
	Version string
	Err     error
}

// Prober keeps the most recent probe result.
type Prober struct {
	client     StatusClient
	appVersion string
	log        *slog.Logger

	mu   sync.RWMutex
	last Result
}

// New returns a prober around sc. appVersion is reported as "version".
func New(sc StatusClient, appVersion string, log *slog.Logger) *Prober {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Prober{
		client:     sc,
		appVersion: appVersion,
		log:        log,
		last:       Result{State: StateUnreachable},
	}
}

// Probe calls AdGuard once, stores and returns the classified result.
func (p *Prober) Probe(ctx context.Context) Result {
	st, err := p.client.Status(ctx)
	r := classify(st, err)
	p.mu.Lock()
	p.last = r
	p.mu.Unlock()

	if err != nil {
		p.log.Error("adguard probe failed", "adguard", string(r.State), "err", err)
	} else {
		p.log.Info("adguard probe ok", "adguard_version", r.Version)
	}
	return r
}

func classify(st adguard.Status, err error) Result {
	switch {
	case err == nil:
		return Result{State: StateOK, Version: st.Version}
	case errors.Is(err, adguard.ErrBadCredentials):
		return Result{State: StateUnauthorized, Err: err}
	default:
		return Result{State: StateUnreachable, Err: err}
	}
}

// Last returns the most recent stored result.
func (p *Prober) Last() Result {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.last
}

// Handler serves GET /healthz from stored state only.
func (p *Prober) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		r := p.Last()
		body := struct {
			Status         string `json:"status"`
			AdGuard        State  `json:"adguard"`
			AdGuardVersion string `json:"adguard_version"`
			Version        string `json:"version"`
		}{"ok", r.State, r.Version, p.appVersion}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(body)
	}
}

// Run probes immediately and then every interval until ctx is done. A probe
// outcome never stops the loop: a credential revoked after startup only
// changes what /healthz reports.
func (p *Prober) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	p.Probe(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Probe(ctx)
		}
	}
}
