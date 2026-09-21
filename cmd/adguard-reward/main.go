// Command adguard-reward serves the parental-reward companion for AdGuard Home.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/api"
	"github.com/Rivil/adguard-reward/internal/auth"
	"github.com/Rivil/adguard-reward/internal/config"
	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/health"
	"github.com/Rivil/adguard-reward/internal/ratelimit"
	"github.com/Rivil/adguard-reward/internal/store"
)

// version is bound at build time via -ldflags '-X main.version=...'.
var version = "dev"

// probeInterval is how often the health prober re-checks AdGuard after startup.
const probeInterval = 60 * time.Second

// sweepInterval is how often expired sessions are deleted. A var, not a
// const, so tests can lower it.
var sweepInterval = time.Hour

// reconcileInterval is how often the grant reconciler re-reads AdGuard
// (locked reconciler_cadence). A var so tests can lower it.
var reconcileInterval = 60 * time.Second

// startupReconcileTimeout bounds the pre-listen reconcile pass so an
// unreachable AdGuard cannot hold the listener closed.
const startupReconcileTimeout = 30 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.LookupEnv, os.Stderr, nil))
}

// run is main without the process-global edges so tests can drive it: args
// are the command-line flags, lookupEnv the environment, stderr where logs
// go, and onListen (optional) is called with the bound address once the
// listener is open. It returns the process exit code.
func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stderr io.Writer, onListen func(addr string)) int {
	fs := flag.NewFlagSet("adguard-reward", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", defaultConfigPath(), "path to config.yaml")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			explicit = true
		}
	})

	// Until the config is loaded the log level is unknown; boot errors go
	// through a default-level handler so they are never swallowed.
	log := slog.New(slog.NewTextHandler(stderr, nil))

	cfg, err := config.Load(*configPath, explicit, lookupEnv)
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}
	log = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.SlogLevel()}))

	client, err := adguard.New(cfg.AdGuard.URL, cfg.AdGuard.Username, cfg.AdGuard.Password.Reveal(), adguard.WithLogger(log))
	if err != nil {
		log.Error("adguard client", "err", err)
		return 1
	}

	prober := health.New(client, version, log)
	// The prober logs each probe's outcome itself; only the fatal case needs
	// a line here. An unreachable AdGuard is not fatal (startup_probe).
	if r := prober.Probe(ctx); errors.Is(r.Err, adguard.ErrBadCredentials) {
		log.Error("adguard rejected the service credential (adguard.username / adguard.password)", "url", cfg.AdGuard.URL)
		return 1
	}

	st, err := store.Open(cfg.DataDir, log)
	if err != nil {
		log.Error("store", "err", err) // err names data_dir
		return 1
	}
	defer func() { _ = st.Close() }()

	// The grant engine's startup pass runs before the listener opens: a grant
	// that expired while the process was down is reverted before any request
	// is served. Like the startup probe, an unreachable AdGuard is logged,
	// not fatal — the reconciler retries on its interval.
	eng := grants.New(st, client, grants.Options{Log: log})
	defer eng.Close()
	startupCtx, cancelStartup := context.WithTimeout(ctx, startupReconcileTimeout)
	if err := eng.Start(startupCtx); err != nil {
		log.Error("startup reconcile", "err", err)
	}
	cancelStartup()

	// The session cookie is Secure whenever the browser reaches us over
	// TLS: our own listener, or a proxy announced through base_url.
	tlsOn := cfg.TLS.Cert != "" // config.validate guarantees both-or-neither
	secure := tlsOn || strings.HasPrefix(cfg.BaseURL, "https://")
	sessions := auth.New(st, auth.Options{
		Secure:      secure,
		Log:         log,
		ErrorWriter: api.UnauthorizedWriter,
	})
	apiHandler := api.New(api.Deps{
		AdGuard:  client,
		ClientIP: api.ClientIP(cfg.TrustedProxyNets()),
		Log:      log,
		Auth:     sessions,
		Limiter:  ratelimit.New(ratelimit.Defaults()),
		Sessions: st,
		Children: st,
		Grants:   eng,
		Buttons:  st,
	}).Handler()

	// /healthz stays on the plain mux, outside the API chain: it is
	// liveness for systemd and Docker, so no cookie and no CSRF header.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", prober.Handler())
	mux.Handle("/api/v1/", apiHandler)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Error("listen", "addr", cfg.Listen, "err", err)
		return 1
	}

	go prober.Run(ctx, probeInterval)
	go sessions.RunSweeper(ctx, sweepInterval)
	go eng.Run(ctx, reconcileInterval)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("listening", "addr", ln.Addr().String(), "version", version, "tls", tlsOn, "secure_cookie", secure)
	if onListen != nil {
		onListen(ln.Addr().String())
	}
	if tlsOn {
		err = srv.ServeTLS(ln, cfg.TLS.Cert, cfg.TLS.Key)
	} else {
		err = srv.Serve(ln)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server exited", "err", err)
		return 1
	}
	return 0
}

// defaultConfigPath is config.yaml beside the binary (config_path decision).
// If the executable path cannot be resolved the bare name is used, which
// resolves against the working directory.
func defaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(filepath.Dir(exe), "config.yaml")
}
