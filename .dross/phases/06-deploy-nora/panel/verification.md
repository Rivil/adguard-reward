# Verification-lens plan — 06-deploy-nora

Method: for each criterion the ideal test contract was written first (which surface
breaks, what the test observes), then the smallest task that makes it satisfiable.
Everything testable runs on this machine under `make test`; nora and the phone are
observed by a human and recorded in verify.toml (locked `real_use_evidence`).

Test placement follows the precedent already in the tree: repo-artefact tests live in
`cmd/adguard-reward` (`TestDockerfileCMD` reads `../../deploy/Dockerfile`; `TestVersionVar`
pins the ldflags var), binary-level behaviour runs through `run()` with an injected
`lookupEnv` (`TestRun_TrustedProxies`, `TestRun_Healthz`), and the reconciler is tested
through `newEnv(t, mod)` against `adguardtest` (`TestReconcile_RunLoop`).

Contracts written first, per criterion:

```
c-1  parse deploy/adguard-reward.service -> [Service] has DynamicUser=yes,
     StateDirectory=adguard-reward, ExecStart ends "--config /etc/adguard-reward/config.yaml",
     LoadCredential=<id>:<file> (id adguard_password, file /etc/adguard-reward/adguard_password),
     NoNewPrivileges=yes, ProtectSystem=strict, PrivateTmp=yes.
     Cross-file: the credential id in LoadCredential == basename of password_file in the
     shipped systemd config (/run/credentials/adguard-reward.service/<id>).
c-2  `make build-linux VERSION=v` -> ELF64, EM_X86_64, no PT_INTERP, no DT_NEEDED;
     debug/buildinfo settings carry CGO_ENABLED=0 and -ldflags containing "-X main.version=v".
c-3  config.Load: data_dir unset + STATE_DIRECTORY=/x -> DataDir=/x; neither -> ./data;
     data_dir set wins over STATE_DIRECTORY; ADGUARD_REWARD_DATA_DIR wins over STATE_DIRECTORY;
     STATE_DIRECTORY="/a:/b" -> /a. run(): db file appears under STATE_DIRECTORY.
c-4  deploy/deploy-nora.sh under PATH shims for ssh/scp: exit 0 only when the post-restart
     /healthz body reports VERSION; wrong version or non-200 -> exit 1; first run installs
     config + credential file, second run does not; ordering scp -> install -> daemon-reload
     -> restart -> healthz. `make -n deploy-nora` prints the GOOS=linux build line first.
c-5  run() started on the shipped systemd config (ports/paths overridden by env): 6th failed
     login from peer 127.0.0.1 with X-Forwarded-For: 100.64.0.5 -> 429 and the log line says
     ip=100.64.0.5; the session cookie carries Secure. Manual: install prompt, standalone.
c-6  Run loop log lines: failing pass logs "reconcile failed"; failing pass after cancel
     does not; quiet pass logs nothing; repaired=1 and reverted=1 passes each log
     msg=reconcile with that count. Gremlins on ./internal/grants: 0 survivors at :133/:135.
c-7  verify.toml record with date, device, service, tap-to-unblock latency, expiry re-block.
```

Wire/shape decisions fixed here so wave-2 tasks can be written against them:

```
deploy/adguard-reward.service         [Unit] After=network-online.target adguardhome.service (Wants= the former)
                                      [Service] Type=simple, DynamicUser=yes, StateDirectory=adguard-reward,
                                        ExecStart=/usr/local/bin/adguard-reward --config /etc/adguard-reward/config.yaml
                                        LoadCredential=<id>:<file> (id adguard_password, file /etc/adguard-reward/adguard_password)
                                        NoNewPrivileges=yes ProtectSystem=strict ProtectHome=yes PrivateTmp=yes
                                        Restart=on-failure
                                      [Install] WantedBy=multi-user.target
deploy/config.systemd.yaml            listen "127.0.0.1:8080", base_url "https://nora.<tailnet>.ts.net",
                                      trusted_proxies ["127.0.0.1"], adguard.url http://127.0.0.1:80,
                                      adguard.username "" (fill in), adguard.password_file
                                      /run/credentials/adguard-reward.service/adguard_password,
                                      NO data_dir key (STATE_DIRECTORY takes over), no secrets (r-03)
Makefile                              LINUX_BIN ?= bin/$(BIN)-linux-amd64
                                      build-linux: build-web -> CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build
                                        $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(LINUX_BIN) ./cmd/$(BIN)
                                      deploy-nora: build-linux -> VERSION=$(VERSION) BIN=$(LINUX_BIN)
                                        NORA_HOST=$(NORA_HOST) deploy/deploy-nora.sh
                                      test-go: ADGUARD_REWARD_BUILD_TEST=1 go test -race ./...
deploy/deploy-nora.sh                 env: VERSION, BIN, NORA_HOST (default nora). Remote paths:
                                      /usr/local/bin/adguard-reward, /etc/systemd/system/adguard-reward.service,
                                      /etc/adguard-reward/{config.yaml,adguard_password}.
                                      Health: `ssh $NORA_HOST curl -fsS http://127.0.0.1:8080/healthz`
                                      retried up to 40 s; exit 1 unless "version":"$VERSION" in the body
                                      (prints `journalctl -u adguard-reward -n 20` on failure).
```

---

Phase 06-deploy-nora — 7 tasks across 3 waves

Wave 1
  t-1  Add systemd unit, systemd config, unit tests
       files:    deploy/adguard-reward.service, deploy/config.systemd.yaml, cmd/adguard-reward/deploy_test.go
       covers:   c-1 (+ the config half of c-5's testable clause)
       description: New unit per the shape above; new secret-free config example for the
                    systemd/tailscale layout; new test file that parses the unit into
                    section->key->values and cross-checks it against the config.
       depends_on: —
       contract: TestUnitFile: dropping or changing any of DynamicUser=yes,
                 StateDirectory=adguard-reward, NoNewPrivileges=yes, ProtectSystem=strict,
                 PrivateTmp=yes in [Service] fails with the key named
       contract: TestUnitFile: ExecStart not ending in "--config /etc/adguard-reward/config.yaml"
                 fails; LoadCredential not equal to
                 "<id>:<file> (id adguard_password, file /etc/adguard-reward/adguard_password)" fails
       contract: TestUnitCredentialMatchesConfig: renaming the LoadCredential id in the unit
                 without changing password_file in deploy/config.systemd.yaml (or vice versa)
                 fails — the test asserts password_file ==
                 "/run/credentials/adguard-reward.service/" + id
       contract: TestSystemdConfigLoads: config.Load on deploy/config.systemd.yaml with only
                 ADGUARD_REWARD_ADGUARD_USERNAME and ADGUARD_REWARD_ADGUARD_PASSWORD_FILE
                 (temp file) overridden yields Listen "127.0.0.1:8080", TrustedProxies
                 ["127.0.0.1"], BaseURL with prefix "https://", AdGuard.URL
                 "http://127.0.0.1:80"; a `password:` or `api_key:` value in the raw file
                 that is non-empty fails (r-03); a `data_dir` key present in the raw file fails
                 (STATE_DIRECTORY must win on nora)

  t-2  Add make build-linux with static ELF test
       files:    Makefile, cmd/adguard-reward/build_test.go
       covers:   c-2
       description: LINUX_BIN variable, build-linux target (CGO_ENABLED=0 GOOS=linux
                    GOARCH=amd64, same LDFLAGS as build), .PHONY/help entries; test-go
                    exports ADGUARD_REWARD_BUILD_TEST=1. New test runs the target into a
                    temp path and inspects the artefact with debug/elf + debug/buildinfo.
       depends_on: —
       contract: TestBuildLinux (skips unless ADGUARD_REWARD_BUILD_TEST=1 and `make` is on
                 PATH): after `make build-linux VERSION=test-0.0.0-gdeadbee LINUX_BIN=<tmp>`,
                 elf.Open reports Class ELFCLASS64 and Machine EM_X86_64 — a darwin or arm64
                 build fails
       contract: TestBuildLinux: the ELF has no PT_INTERP program header and
                 DynString(DT_NEEDED) is empty — CGO_ENABLED=1 (dynamic libc) fails
       contract: TestBuildLinux: buildinfo.ReadFile settings contain CGO_ENABLED=0 and an
                 -ldflags value containing "-X main.version=test-0.0.0-gdeadbee" — dropping
                 -X main.version from LDFLAGS fails
       contract: TestMakefileVersion: the Makefile's VERSION default line contains
                 "git describe --tags" — hard-coding VERSION fails
       contract: `make test` runs TestBuildLinux (test-go sets ADGUARD_REWARD_BUILD_TEST=1);
                 `go test ./...` without it skips — recorded so a skip is never mistaken for a pass

  t-3  Resolve data_dir from STATE_DIRECTORY
       files:    internal/config/config.go, internal/config/config_test.go, cmd/adguard-reward/main_test.go, deploy/config.example.yaml
       covers:   c-3
       description: Defaults().DataDir becomes ""; after env overrides Load fills an empty
                    DataDir from lookupEnv("STATE_DIRECTORY") (first ':'-separated entry)
                    else "./data". config.example.yaml comment documents the fallback.
       depends_on: —
       contract: TestLoad_StateDirectory: no data_dir in YAML + STATE_DIRECTORY=/var/lib/x ->
                 DataDir "/var/lib/x"; STATE_DIRECTORY unset -> "./data" (TestLoad_Defaults
                 keeps asserting "./data" unchanged)
       contract: TestLoad_StateDirectory: data_dir: "/y" in YAML + STATE_DIRECTORY=/x ->
                 "/y"; ADGUARD_REWARD_DATA_DIR=/z + STATE_DIRECTORY=/x -> "/z" — an
                 implementation that lets STATE_DIRECTORY override an explicit setting fails
       contract: TestLoad_StateDirectory: STATE_DIRECTORY="/a:/b" -> "/a"; STATE_DIRECTORY=""
                 (set but empty) -> "./data"
       contract: TestRun_StateDirectory: run() with a config lacking data_dir and env
                 {STATE_DIRECTORY: <tmp>} creates the sqlite file under <tmp> (os.Stat
                 succeeds) and the listening log line's data_dir is <tmp>; without
                 STATE_DIRECTORY the file lands under ./data relative to the test's cwd
                 (test chdirs into a temp dir first)
       contract: TestEnvTable still passes — STATE_DIRECTORY is a fallback, not a config
                 key, so it must NOT be added to envTable

  t-4  Kill Run-loop log survivors in reconcile.go
       files:    internal/grants/reconcile_test.go
       covers:   c-6
       description: New TestReconcile_RunLogs drives Engine.Run with a 20 ms interval and a
                    slog.TextHandler over a locked buffer injected via Options.Log; each
                    subtest pins one branch of the switch at reconcile.go:131-137.
       depends_on: —
       contract: subtest "failed": fake SetStatus("/control/clients", 500), one tick -> buffer
                 contains msg="reconcile failed" — the :133 mutant `err == nil && ...` fails
                 here (and again in "quiet" where it would log spuriously)
       contract: subtest "cancelled": fake Hang("/control/clients", 200 ms), cancel ctx at
                 50 ms, wait for Run to return -> buffer does NOT contain "reconcile failed"
                 — the :133 mutant `... && ctx.Err() != nil` fails
       contract: subtest "quiet": consistent state, three ticks -> buffer contains neither
                 "reconcile failed" nor msg=reconcile — the :135 BOUNDARY mutants
                 (`>= 0` on either operand) and both NEGATION mutants fail
       contract: subtest "repaired": parent re-blocks a granted service in the fake
                 (MutateClient), one tick -> buffer contains msg=reconcile with repaired=1
                 reverted=0; subtest "reverted": seedOverdue grant, one tick -> reverted=1
                 repaired=0 — pins each operand positively
       contract: `gremlins unleash` scoped to internal/grants (the dross verify mutation
                 leg) reports zero survivors at reconcile.go:133 and :135 — any of the three
                 routed mutants surviving fails c-6

Wave 2 (depends t-1, t-2)
  t-5  Add deploy-nora script, target and shim tests
       files:    deploy/deploy-nora.sh, Makefile, cmd/adguard-reward/deploy_test.go, docs/deploy-nora.md
       covers:   c-4
       description: bash script (set -euo pipefail) driven by VERSION/BIN/NORA_HOST: scp the
                    binary + unit to /tmp on nora, sudo install binary 0755 and unit 0644,
                    first-run-only install of config.systemd.yaml -> /etc/adguard-reward/
                    config.yaml (0644) and an empty 0600 adguard_password with a "fill these
                    in" hint, daemon-reload, enable --now/restart, then poll /healthz over
                    ssh for VERSION. Makefile gets NORA_HOST ?= nora and deploy-nora:
                    build-linux. Runbook documents the one-time `tailscale serve --bg
                    --https=443 http://127.0.0.1:8080` and the first-run edits.
       depends_on: t-1, t-2
       contract: TestDeployNora_Success: with PATH-prepended fake `ssh`/`scp` (shell shims
                 that append argv to a log and answer the healthz curl with
                 {"version":"v-test"}), `VERSION=v-test BIN=<tmp bin> NORA_HOST=fake
                 deploy/deploy-nora.sh` exits 0 and the log shows, in order: scp of BIN, scp
                 of the unit, install of the binary, install of the unit,
                 `systemctl daemon-reload`, `systemctl restart adguard-reward`, the healthz
                 curl — reordering restart after the health check fails
       contract: TestDeployNora_WrongVersion: healthz shim answers {"version":"v-old"} ->
                 exit 1 and stderr names both versions — a script that only checks HTTP
                 200 fails
       contract: TestDeployNora_Unhealthy: healthz shim exits 22 (curl -f on non-200) for
                 every attempt (retry window shortened via HEALTH_TIMEOUT=2 env) -> exit 1
                 and the log shows `journalctl -u adguard-reward` was requested
       contract: TestDeployNora_FirstRun: ssh shim answers `test -e /etc/adguard-reward/
                 config.yaml` with exit 1 -> log contains an install of config.systemd.yaml
                 to /etc/adguard-reward/config.yaml with -m 0644 and of adguard_password
                 with -m 0600; with exit 0 -> neither appears — a script that overwrites
                 the edited config on redeploy fails
       contract: TestDeployNora_NoSecretInArgv: in every logged ssh/scp argv the credential
                 path appears only as a destination path; no argv contains the contents of
                 a local password file (the shim log is grepped for a sentinel the test
                 writes into a decoy file the script must never read)
       contract: TestMakeDeployNoraChain: `make -n deploy-nora VERSION=v` output contains
                 "GOOS=linux GOARCH=amd64" before "deploy/deploy-nora.sh" — deploy-nora
                 losing its build-linux prerequisite fails

Wave 2 (depends t-1, t-3)
  t-6  Run shipped systemd config through run()
       files:    cmd/adguard-reward/main_test.go
       covers:   c-5 (testable clause), c-3 (integration)
       description: TestRun_SystemdConfig starts run() with --config ../../deploy/
                    config.systemd.yaml and env overrides for listen (127.0.0.1:0),
                    adguard.url (fake), username, password_file (temp), STATE_DIRECTORY
                    (temp); it simulates tailscale serve by sending from 127.0.0.1 with
                    X-Forwarded-For set to a CGNAT tailnet address.
       depends_on: t-1, t-3
       contract: 5 failed logins with X-Forwarded-For: 100.64.0.5 then a 6th -> 429, and
                 the login log lines carry ip=100.64.0.5 not ip=127.0.0.1 — removing
                 trusted_proxies from config.systemd.yaml, or ClientIP ignoring a bare-IP
                 entry, fails
       contract: a 6th failed login with X-Forwarded-For: 100.64.0.6 -> 401 (the limiter
                 keys on the forwarded address, so one phone cannot lock out another)
       contract: the Set-Cookie on a successful login carries Secure — base_url in
                 config.systemd.yaml losing its https:// prefix fails
       contract: the sqlite file is created under the STATE_DIRECTORY temp dir — a
                 data_dir key creeping back into config.systemd.yaml fails

Wave 3 (depends t-4, t-5, t-6)
  t-7  Deploy to nora and record real-use evidence
       files:    .dross/phases/06-deploy-nora/verify.toml
       covers:   c-4 (live run), c-5 (manual), c-7
       description: Run `make deploy-nora` against the real host (first run: edit
                    /etc/adguard-reward/config.yaml username/base_url and write the AdGuard
                    password to the credential file on nora file-to-file, then re-run);
                    enable tailscale serve once; from a phone on the tailnet observe the
                    install prompt, the standalone launch, a one-tap unblock on a child's
                    device and the automatic re-block; record each in verify.toml.
       depends_on: t-4, t-5, t-6
       contract: verify.toml c-4 notes record the observed `make deploy-nora` exit 0 and the
                 /healthz version string equal to `git describe --tags --always --dirty` of
                 the deployed commit, with date — a record of a run that was not observed
                 green in-session is forbidden (global rule 1)
       contract: verify.toml records for c-5: date, phone model/OS, "install prompt shown"
                 and "launched standalone (no browser chrome)" — a missing record fails c-5
       contract: verify.toml records for c-7: date, child's device name as known to AdGuard,
                 service tapped, seconds from tap to the service resolving on the device
                 (≤ 5), and "block returned at expiry without intervention" with the expiry
                 time — a missing record or latency > 5 s fails c-7
       contract: no secret value appears in verify.toml, docs or the commit (r-03; global
                 secret-handling rule 1) — `grep -q` for the credential file's sha256 prefix
                 is the only permitted check

## Coverage

| criterion | tasks |
|---|---|
| c-1 | t-1 |
| c-2 | t-2 |
| c-3 | t-3, t-6 |
| c-4 | t-5, t-7 |
| c-5 | t-1, t-6 (testable clause), t-7 (manual record) |
| c-6 | t-4 |
| c-7 | t-7 |

Every criterion accounted for: 7/7.

## Judgment calls

- c-2 is asserted on the artefact (debug/elf + debug/buildinfo), not on Makefile text; the
  Makefile text check is limited to the git-describe line. Rejected: grepping the recipe for
  `CGO_ENABLED=0` — it proves nothing about what was built.
- TestBuildLinux is gated on `ADGUARD_REWARD_BUILD_TEST=1`, exported by `make test-go`, not on
  `testing.Short()`. Rejected: always-on — gremlins re-runs the package tests per mutant and a
  cross-compile plus pnpm build per mutant would push cmd/adguard-reward into timeouts; and
  gremlins passes no -short. The project's test_command still runs it, so the gate is honest.
- Deploy logic lives in `deploy/deploy-nora.sh`, with the Makefile target a one-liner that
  wires VERSION/BIN/NORA_HOST. Rejected: inline Makefile recipe — untestable without a
  Makefile-shim harness, and `$$` escaping hides bugs.
- The deploy script is tested with PATH shims for `ssh`/`scp` from a Go test, in the same
  package as the other repo-artefact tests. Rejected: a bats suite — new toolchain for four
  assertions; rejected: no test — c-4's "fails unless ... version" is a real contract and the
  only thing standing between a stale binary and a green-looking deploy.
- The health gate goes over ssh (`curl http://127.0.0.1:8080/healthz` on nora), not via the
  tailnet HTTPS URL. Rejected: HTTPS URL — couples c-4 to tailscale serve being configured and
  to the dev box being on the tailnet; the ssh path proves the binary and unit alone.
- First run installs an *empty* 0600 credential file and the deploy then fails its health gate
  with a fill-these-in hint. Rejected: shipping the password from a local file via scp — moves
  a secret through the dev box's argv/log surface; the user writes it on nora file-to-file.
- STATE_DIRECTORY is resolved inside `config.Load` via the already-injected `lookupEnv`, with
  `Defaults().DataDir` becoming "". Rejected: reading os.Getenv in main.go — untestable
  without process-global env, and Load already owns precedence (defaults < YAML < env).
- STATE_DIRECTORY is *not* added to envTable. Rejected: making it a config key — TestEnvTable
  would then demand a yaml tag for a systemd-injected variable that is not configuration.
- A separate `deploy/config.systemd.yaml` rather than editing `config.example.yaml`. Rejected:
  one example for both layouts — the Docker/compose example must keep `data_dir` and a
  `password:` slot, the systemd one must have neither; two files with two tests beats one file
  with comments.
- t-6 is a single test function kept as its own task (small, but wave-2 by necessity): it is
  the only test that binds the *shipped* nora config to the c-5 limiter clause and the c-3
  STATE_DIRECTORY path end to end. Rejected: folding into t-1 (would drag t-1 into wave 2) or
  into t-5 (five files, two layers).
- c-6 is killed with a Run-loop log test rather than by refactoring the switch away. Rejected:
  restructuring reconcile.go:131-137 to make the mutants equivalent — the log lines are the
  operator's only signal that the reconciler is working, so they deserve a positive contract.
- c-7's tap-to-unblock latency and expiry re-block are human observations recorded in
  verify.toml (locked real_use_evidence). No task pretends to test them.
