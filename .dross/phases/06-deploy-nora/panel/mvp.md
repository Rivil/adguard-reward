# MVP lens — phase 06-deploy-nora

Bias applied: smallest task set that satisfies every criterion. Four tasks, two
waves. Nothing speculative (no goreleaser, no arm64, no deploy README, no
tailscale-serve abstraction). Every file below either exists today or is one of
the two new deploy/ files the spec itself names.

Phase 06-deploy-nora — 4 tasks across 2 waves

Wave 1
  t-1  Add systemd unit + nora config, parsed by test
       files:    deploy/adguard-reward.service (new), deploy/config.nora.yaml (new),
                 cmd/adguard-reward/main_test.go
       covers:   c-1, c-5 (testable clause: trusted_proxies honours the proxy's XFF)
       depends_on: —
       description:
         Write deploy/adguard-reward.service: [Service] DynamicUser=yes,
         StateDirectory=adguard-reward, ExecStart=/usr/local/bin/adguard-reward
         --config /etc/adguard-reward/config.yaml,
         LoadCredential=<id>:<file> (id adguard_password, file /etc/adguard-reward/adguard_password),
         NoNewPrivileges=yes, ProtectSystem=strict, PrivateTmp=yes, Restart=on-failure,
         [Install] WantedBy=multi-user.target.
         Write deploy/config.nora.yaml (locked tls_path + secret_placement, no data_dir
         key, no secrets): listen "127.0.0.1:8080", base_url
         "https://nora.TAILNET.ts.net" (TAILNET is edited on nora after the first
         install), trusted_proxies ["127.0.0.1"], adguard.url http://127.0.0.1:80,
         adguard.username "" (edited on nora), adguard.password_file
         /run/credentials/adguard-reward.service/adguard_password.
         Add to cmd/adguard-reward/main_test.go beside TestDockerfileCMD:
         TestSystemdUnit (minimal section/key=value parser over the unit file) and
         TestNoraConfig (config.Load of deploy/config.nora.yaml with env overrides for
         username/password and ADGUARD_REWARD_ADGUARD_PASSWORD_FILE="" to blank the
         credential path, then api.ClientIP(cfg.TrustedProxyNets())).
       contract:
         - if DynamicUser, StateDirectory, ExecStart's "--config /etc/adguard-reward/config.yaml",
           LoadCredential's "adguard_password:" id, NoNewPrivileges=yes, ProtectSystem=strict or
           PrivateTmp=yes is missing or changed in deploy/adguard-reward.service, TestSystemdUnit
           fails naming the key
         - if the LoadCredential id in the unit and the password_file path in
           deploy/config.nora.yaml (/run/credentials/adguard-reward.service/<id>) drift apart,
           TestSystemdUnit fails
         - if deploy/config.nora.yaml gains a data_dir key, sets listen to anything but
           127.0.0.1:8080, or loses the https:// base_url, TestNoraConfig fails
         - if trusted_proxies in deploy/config.nora.yaml stops covering 127.0.0.1, or
           api.ClientIP stops walking XFF for a trusted peer, TestNoraConfig fails: a request
           with RemoteAddr 127.0.0.1:x and X-Forwarded-For 100.64.0.7 must resolve to
           100.64.0.7 (the phone), and with RemoteAddr 100.64.0.9:x and the same header must
           resolve to 100.64.0.9 (untrusted peer keeps the peer)

  t-2  data_dir falls back to $STATE_DIRECTORY
       files:    internal/config/config.go, internal/config/config_test.go,
                 deploy/config.example.yaml
       covers:   c-3
       depends_on: —
       description:
         Defaults() leaves DataDir empty; after the env pass Load resolves an empty
         DataDir to lookupEnv("STATE_DIRECTORY") (first colon-separated entry) when
         set and non-empty, else "./data". Comment the data_dir key in
         config.example.yaml with the systemd fallback. Existing TestLoad_Defaults
         (expects "./data" with no env) keeps passing unchanged.
       contract:
         - if STATE_DIRECTORY is ignored: TestLoad_StateDirectory loads a YAML with no
           data_dir under lookupEnv{STATE_DIRECTORY:"/var/lib/adguard-reward"} and fails
           unless cfg.DataDir == "/var/lib/adguard-reward"
         - if the "./data" default is lost: TestLoad_Defaults fails (cfg.DataDir != "./data"
           with neither data_dir nor STATE_DIRECTORY)
         - if the fallback overrides an explicit value: TestLoad_StateDirectory's second case
           (data_dir: "/x" in YAML plus STATE_DIRECTORY set) fails unless cfg.DataDir == "/x";
           third case (ADGUARD_REWARD_DATA_DIR env plus STATE_DIRECTORY) fails unless the
           ADGUARD_REWARD_ value wins

  t-3  Kill reconcile Run log-branch survivors
       files:    internal/grants/reconcile_test.go
       covers:   c-6
       depends_on: —
       description:
         Add TestReconcile_RunLogs using the existing newEnv logs buffer and
         fake.Hang / fake failure hooks: drive Engine.Run at a 20 ms interval through
         (a) a quiet pass, (b) a pass that reverts an overdue grant seeded with
         seedOverdue, (c) a pass where /control/clients fails, (d) a failing pass after
         ctx is cancelled; assert on the emitted msg= lines. No production change —
         the three survivors are log-selection branches (phase 04 verify.toml c-5 note).
       contract:
         - reconcile.go:133 CONDITIONALS_NEGATION (err != nil → err == nil): a pass whose
           /control/clients GET returns 500 must produce a "reconcile failed" line; the mutant
           logs nothing on failure, so the assertion fails
         - reconcile.go:133 CONDITIONALS_NEGATION (ctx.Err() == nil → != nil): the failing pass
           with a live ctx must log "reconcile failed"; the mutant only logs after cancel, so
           the same assertion fails
         - reconcile.go:135 CONDITIONALS_BOUNDARY (> 0 → >= 0) and CONDITIONALS_NEGATION
           (> 0 → <= 0): a quiet pass (0 reverted, 0 repaired) must emit no "msg=reconcile "
           line, and a pass that reverts one overdue grant must emit exactly one carrying
           reverted=1; the mutants log on the quiet pass and/or stay silent on the revert, so
           one of the two assertions fails
         - the phase-06 verify mutation run over internal/grants/reconcile.go reports none of
           the three routed survivors surviving

Wave 2 (depends t-1, t-2)
  t-4  Makefile build-linux and deploy-nora targets
       files:    Makefile
       covers:   c-2, c-4, c-5 (manual: install prompt + standalone launch), c-7
       depends_on: t-1 (unit + config file names), t-2 (service needs the StateDirectory
                   fallback or store.Open fails under ProtectSystem=strict and healthz
                   never answers)
       description:
         build-linux: build-web, then GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build
         $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/adguard-reward-linux-amd64, then
         self-check `go version -m` for GOOS=linux, GOARCH=amd64, CGO_ENABLED=0 (fail
         otherwise; -ldflags is not recorded under -trimpath, so version is checked
         end-to-end by deploy-nora).
         deploy-nora: build-linux; scp binary + unit + config.nora.yaml to nora:/tmp;
         one ssh with sudo that installs the binary to /usr/local/bin (0755), the unit
         to /etc/systemd/system, creates /etc/adguard-reward and — only when absent —
         config.yaml (0644) and adguard_password (0600, empty, to be filled by hand),
         runs tailscale serve --bg 8080 (idempotent), systemctl daemon-reload,
         enable --now, restart; then polls http://127.0.0.1:8080/healthz on nora via
         ssh+curl for up to 15 s and exits non-zero unless it returns 200 with
         "version":"$(VERSION)". Prints the first-run to-do (username, TAILNET,
         password file) when it just created the config.
       contract:
         - if CGO_ENABLED=0 or the linux/amd64 target is dropped from build-linux, the target's
           own `go version -m` check exits non-zero (missing CGO_ENABLED=0 / GOOS=linux /
           GOARCH=amd64 build settings)
         - if -X main.version stops reaching the binary, deploy-nora's healthz check fails:
           /healthz reports "dev" while $(VERSION) is the git-describe string (TestRun_Healthz
           already fails if /healthz stops echoing the version var)
         - if the restart is skipped or the service fails to start, deploy-nora exits non-zero:
           healthz either times out or reports the previous version, not $(VERSION)
         - if the first-run guard is wrong, a second deploy-nora overwrites the edited
           /etc/adguard-reward/config.yaml — verify.toml records that config.yaml's mtime on
           nora is unchanged across two consecutive deploys
         - verify.toml records, with date and device, that https://nora.<tailnet>.ts.net/ from a
           phone on the tailnet showed the install prompt and launched standalone (c-5), and that
           one tap unblocked the service on a child's real device within 5 s and the block
           returned at expiry with no intervention (c-7); a missing record fails the criterion

## Coverage

| criterion | tasks |
|---|---|
| c-1 | t-1 |
| c-2 | t-4 |
| c-3 | t-2 |
| c-4 | t-4 |
| c-5 | t-1 (trusted_proxies/XFF test), t-4 (manual install-prompt observation) |
| c-6 | t-3 |
| c-7 | t-4 (manual real-use observation) |

## Judgment calls

- Merged the systemd unit and the nora config into one task with one test file: both are deploy/ artefacts parsed by Go tests in main_test.go (next to TestDockerfileCMD), and the cross-assert (LoadCredential id == password_file path) only exists if one test sees both files. Rejected a separate "nora config" task — it would be <10 min and lose the drift check.
- Merged build-linux and deploy-nora into one Makefile task in wave 2 rather than build-linux alone in wave 1: build-linux is a 6-line target, and the only thing that proves its version stamp (ldflags are not in buildinfo under -trimpath, confirmed with `go version -m` on a local linux build) is deploy-nora's healthz comparison.
- Committed deploy/config.nora.yaml with placeholders (TAILNET, empty username) and a first-run-only install, rather than a Makefile that seds TAILNET/USERNAME from variables: it keeps c-4's "example config on first run" literal, avoids new make variables, and the two hand-edits happen once on nora. Cost: the very first deploy-nora fails its healthz gate until the password file and config are filled — which is the criterion's failure mode, not a bug.
- tailscale serve --bg 8080 runs inside deploy-nora every time (idempotent) instead of a separate target or doc: c-5 needs it and nothing else does.
- No production change for c-6: the survivors select log lines only (phase 04 verify note), so a log-asserting test is the whole fix. Rejected refactoring Run to return stats for testability — more code, same kill.
- Left c-3's "first colon-separated entry" of STATE_DIRECTORY in the description rather than a task: systemd documents the list form, one strings.Cut covers it.
- Rejected a deploy README, a check-nora-config step in CI, arm64 builds and goreleaser: none traces to a criterion.
