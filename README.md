# adguard-reward

A self-hosted companion for [AdGuard Home](https://github.com/AdguardTeam/AdGuardHome)
that lets parents temporarily unblock a service for a child's devices with one
tap — *"YouTube · 1 hour"*, *"TikTok · 30 min"* — and puts the block back
server-side when the time runs out. No AdGuard UI, no remembering to re-block,
no drift.

> **Status: pre-release (v0.1, "Reward loop").** It runs in one household on a
> single host. There are no published binaries or container images yet — build
> from source. Expect breaking changes.

## What it does

- **Timed grants.** A button unblocks one or more of AdGuard's built-in
  services for every device belonging to a child. The revert is scheduled on
  the server and survives restarts.
- **No drift.** A reconciler re-applies active grants every 60 seconds, so a
  parent re-blocking a service in AdGuard's own UI mid-grant doesn't silently
  cut it short — and expired grants are always reverted.
- **Children and devices.** Group AdGuard persistent clients into named
  children, see each child's blocked services read live from AdGuard, and
  label or rename unrecognised devices from the app.
- **Phone-first.** An installable PWA with a live countdown for every active
  grant, plus extend and end-early controls.
- **Log in with AdGuard.** Credentials are checked against AdGuard Home's own
  login — no separate user database. Login is rate-limited per IP.

### Planned

- Custom services AdGuard doesn't ship, via a managed region of `user_rules`;
  permanent block/unblock toggles; grant history.
- AI-assisted service discovery grounded in the child's real DNS query log
  (bring your own Anthropic or OpenAI key).
- Release builds: goreleaser binaries and a container image.

## How it works

adguard-reward is a single static Go binary with the Svelte frontend embedded.
It talks to AdGuard Home only through its `/control/*` HTTP API — no AdGuard
code or assets are vendored. It changes only per-client `blocked_services`
(and, once custom services land, a clearly-marked region of `user_rules`).
Grants and settings live in a local SQLite database (pure Go, no cgo).

It is not a replacement for AdGuard's UI: filters, upstreams, DHCP and
everything else stay in AdGuard. Every AdGuard user is an admin, and so is
every user of this app.

See [`PLAN.md`](PLAN.md) for the design and [`ARCHITECTURE.md`](ARCHITECTURE.md)
for a feature-by-feature map of the code.

## Requirements

- AdGuard Home v0.107.x, reachable over HTTP(S) from wherever this runs
- To build: Go 1.26+, Node.js 24 and pnpm

## Build and run

```sh
git clone https://github.com/Rivil/adguard-reward.git
cd adguard-reward
(cd web && pnpm install)
make build                                   # → bin/adguard-reward
cp deploy/config.example.yaml config.yaml    # then fill in the adguard: section
./bin/adguard-reward --config config.yaml
```

Open <http://localhost:8080> and log in with your AdGuard Home credentials.
`GET /healthz` reports health and the running version.

### systemd

[`deploy/adguard-reward.service`](deploy/adguard-reward.service) is a hardened
unit (`DynamicUser`, `StateDirectory=adguard-reward`, `ProtectSystem=strict`).
Under it the database goes to `$STATE_DIRECTORY` unless `data_dir` is set.

### Docker

[`deploy/Dockerfile`](deploy/Dockerfile) builds a distroless image, and
[`deploy/docker-compose.yml`](deploy/docker-compose.yml) runs it next to an
existing AdGuard Home. The container reads its configuration from
`ADGUARD_REWARD_*` environment variables (see below); to use a config file
instead, mount it and pass `--config /data/config.yaml`.

```sh
docker build -f deploy/Dockerfile -t adguard-reward .
```

## Configuration

[`deploy/config.example.yaml`](deploy/config.example.yaml) documents every key.
The essentials:

| Key | Purpose |
| --- | --- |
| `listen` | Address to serve on (default `:8080`) |
| `adguard.url`, `adguard.username`, `adguard.password` | The AdGuard Home instance and the service account the app uses |
| `tls.cert`, `tls.key` | Optional; serve HTTPS directly |
| `trusted_proxies` | CIDRs whose `X-Forwarded-For` is trusted (empty = ignored) |
| `data_dir` | Where the SQLite database lives |
| `log_level` | `debug`, `info`, `warn`, `error` |

Every key can be overridden by an environment variable: `ADGUARD_REWARD_` plus
the key path joined with `_`, upper-cased — `adguard.url` becomes
`ADGUARD_REWARD_ADGUARD_URL`.

Secrets can come from a file instead of being inline: set
`adguard.password_file` (or `ai.api_key_file`) to a path. Don't commit a
`config.yaml` with real credentials.

## Development

```sh
make dev        # Go API against ./config.yaml
make dev-web    # Vite dev server on :5173, proxying /api to the API
make test       # Go + web test suites
make typecheck  # go vet + svelte-check
make lint       # gofmt check
make help       # everything else
```

## License

[AGPL-3.0](LICENSE). adguard-reward is an independent project and is not
affiliated with or endorsed by AdGuard.
