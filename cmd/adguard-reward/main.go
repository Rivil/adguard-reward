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
	"syscall"
	"time"

	"github.com/Rivil/adguard-reward/internal/adguard"
	"github.com/Rivil/adguard-reward/internal/config"
	"github.com/Rivil/adguard-reward/internal/health"
)

// version is bound at build time via -ldflags '-X main.version=...'.
var version = "dev"

// probeInterval is how often the health prober re-checks AdGuard after startup.
const probeInterval = 60 * time.Second

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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", prober.Handler())

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

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("listening", "addr", ln.Addr().String(), "version", version)
	if onListen != nil {
		onListen(ln.Addr().String())
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
