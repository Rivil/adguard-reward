# 06-deploy-nora — panel synthesis (cold judge)

## Scores

Scale 1–5. Facts checked against the tree before scoring (Makefile, config.go,
main.go, main_test.go, reconcile.go, adguardtest, phase-04 verify.toml, and an
actual `GOOS=linux CGO_ENABLED=0 go build -trimpath` inspected with `go version -m`).

| draft        | criteria coverage | test-contract specificity | granularity | wave correctness |
|--------------|-------------------|---------------------------|-------------|------------------|
| risk         | 5 — 7/7, c-5 split into four testable/manual clauses each owned by a task | 4 — sharpest c-3/c-4/c-5 contracts, every line names the failing mutant/regression; one false claim (buildinfo `-ldflags` under `-trimpath`), and the `-short` gate on the cross-build test runs it under gremlins | 4 — t-6 is heavy (script + Makefile + 7 lines) but each task is one artefact cluster | 5 — t-5←{t-1,t-3}, t-6←{t-3,t-4}, t-7←{t-5,t-6}; t-2 correctly free |
| mvp          | 4 — 7/7 mapped, but c-4's "first run only" and "fails unless" have no local test, only an mtime observation in verify.toml | 3 — correct on the one fact the others got wrong (`-ldflags` is not in buildinfo under `-trimpath`), but c-2 is a Makefile self-check and c-4 is unproven until the real host | 2 — t-4 bundles c-2, c-4, c-5 (manual) and c-7 (manual) into one Makefile commit | 4 — deps are right, but a manual phone observation gates a code commit |
| verification | 5 — 7/7, contract-first per criterion, c-3 asserted at both Load and run() level | 4 — cleanest c-6 subtests and the best failure-path contracts (journalctl on failure, no-secret-in-argv, `make -n` chain order); two false claims (buildinfo `-ldflags`; a `data_dir` key on the `listening` log line that does not exist) and ssh/scp shims that prove command order but never execute the remote install logic | 5 — t-5/t-6 split cleanly by layer; t-6 as a one-test task is small but justified | 5 — correct; t-7←t-4 is unnecessary but harmless |

**Skeleton: risk.** It is the only draft whose c-4 harness actually executes the
install logic under test (DESTDIR + stubbed systemctl/curl/tailscale), it is the
only one that stops a placeholder credential from crash-looping the service, and
its failure-mode list (F1–F10) maps one-to-one onto contract lines. Its two
weaknesses — the impossible buildinfo assertion and the `-short` gate — are fixed
by grafting mvp's fact and verification's env gate.

Note on spec reading (not a divergence — all three drafts agree): c-4 says
"the example config"; every draft ships a second committed template
(`deploy/config.nora.yaml` / `config.systemd.yaml`) rather than installing
`config.example.yaml`, because the example carries `data_dir: "./data"` and a
`password:` slot that the systemd layout must not have. The merged plan keeps that
reading.

## Merged plan

Phase 06-deploy-nora — 7 tasks across 3 waves

Wave 1
  t-1  Resolve data_dir from $STATE_DIRECTORY                    [risk+mvp+verification]
       files:    internal/config/config.go, internal/config/config_test.go, deploy/config.example.yaml
       covers:   c-3
       depends:  —
       description:
         Defaults().DataDir becomes "" and Load resolves it last, after the env
         pass: explicit (yaml or ADGUARD_REWARD_DATA_DIR) wins; else the first
         ":"-separated entry of lookupEnv("STATE_DIRECTORY"); else "./data".
         Empty string from any source means "unset". STATE_DIRECTORY is a
         fallback, not a config key — it is NOT added to envTable.
         config.example.yaml drops the literal `data_dir: "./data"` for a
         commented-out key explaining the systemd fallback (risk F1: a literal
         ./data in the installed config beats STATE_DIRECTORY and, with cwd=/
         under ProtectSystem=strict, crash-loops the unit).
       test_contract:
         - [risk] yaml with no data_dir + STATE_DIRECTORY=/var/lib/x → cfg.DataDir == "/var/lib/x"; if the fallback is dropped, TestLoad_StateDirectory fails
         - [risk+mvp] no data_dir, no STATE_DIRECTORY → "./data" (TestLoad_Defaults keeps asserting "./data" post-Load unchanged; if Defaults() is emptied without the resolver, it fails)
         - [risk+verification] yaml data_dir "/var/lib/reward" + STATE_DIRECTORY=/other → "/var/lib/reward" (explicit beats systemd; inverted precedence fails)
         - [risk] ADGUARD_REWARD_DATA_DIR="" (set-but-empty) + STATE_DIRECTORY=/s → "/s"; ADGUARD_REWARD_DATA_DIR="/e" + STATE_DIRECTORY=/s → "/e"
         - [risk+verification] STATE_DIRECTORY="/a:/b" → "/a" (colon list); STATE_DIRECTORY="" (set but empty) → "./data"; if the split is missing, DataDir contains ":" and the test fails
         - [risk] TestExampleConfigLoads additionally asserts cfg.DataDir == "./data" with no STATE_DIRECTORY and == STATE_DIRECTORY with it set — a re-introduced literal `data_dir: "./data"` in the example config fails the second half
         - [verification] TestEnvTable still passes — STATE_DIRECTORY must NOT appear in envTable

  t-2  Kill reconcile Run log-line mutants                        [verification contract, risk+mvp agree]
       files:    internal/grants/reconcile_test.go
       covers:   c-6
       depends:  —
       description:
         Add TestReconcile_RunLogs driving Engine.Run at a 20 ms interval
         against e.logs (newEnv already wires a TextHandler over a locked
         buffer) with one subtest per branch of the switch at
         reconcile.go:131-137. No production change: the survivors only select
         which log line Run emits (phase-04 verify.toml c-5 note). Phase-04
         verify.toml lists five survivor entries at :133/:135 (both operands
         of each condition); the subtests below pin every operand, so all of
         them die, not only the three the spec names. Verify by hand with
         gremlins on internal/grants before committing and note the result in
         the commit body (risk).
       test_contract:
         - [verification+risk] subtest "failed": fake SetStatus("/control/clients", 500), one tick → buffer contains msg="reconcile failed" within 300 ms; the :133 NEGATION `err == nil` mutant makes a failing pass silent and the test fails (and logs spuriously in "quiet")
         - [verification+risk] subtest "cancelled": fake Hang("/control/clients", 200 ms), cancel ctx at 50 ms, wait for Run to return → buffer does NOT contain "reconcile failed"; the :133 NEGATION on `ctx.Err() == nil` fails here
         - [verification+risk] subtest "quiet": consistent state, three ticks → neither "reconcile failed" nor a msg="reconcile" line (asserted by exact msg so "startup reconcile" cannot mask it); the :135 BOUNDARY `>= 0` mutants on either operand log on every quiet pass and fail
         - [verification+risk] subtest "reverted": seedOverdue grant with withNoTimers, one tick → exactly one msg="reconcile" line with reverted=1 repaired=0; the :135 NEGATION `<= 0` mutant suppresses it and fails
         - [verification+risk] subtest "repaired": parent re-blocks a granted service via MutateClient, one tick → msg="reconcile" with repaired=1 reverted=0 (kills the mutants on the second operand of the ||)
         - [all] the phase-06 verify mutation run (gremlins over internal/grants) reports none of reconcile.go:133/135 CONDITIONALS_* surviving

  t-3  Add systemd unit, nora config template, parse tests       [risk+verification, mvp cross-check]
       files:    deploy/adguard-reward.service, deploy/config.nora.yaml, cmd/adguard-reward/deploy_test.go
       covers:   c-1, c-5 (config half: trusted_proxies + loopback listen)
       depends:  —
       description:
         Unit: [Unit] After=network-online.target AdGuardHome.service,
         Wants=network-online.target; [Service] Type=simple, DynamicUser=yes,
         StateDirectory=adguard-reward,
         LoadCredential=<id>:<file> (id adguard_password, file /etc/adguard-reward/adguard_password),
         ExecStart=/usr/local/bin/adguard-reward --config /etc/adguard-reward/config.yaml,
         NoNewPrivileges=yes, ProtectSystem=strict, ProtectHome=yes,
         PrivateTmp=yes, Restart=on-failure, RestartSec=5s;
         [Install] WantedBy=multi-user.target.
         Template (no secrets, no data_dir key): listen "127.0.0.1:8080",
         base_url "https://nora.TAILNET.ts.net", trusted_proxies ["127.0.0.1"],
         adguard.url http://127.0.0.1:80, adguard.username "CHANGE-ME",
         adguard.password_file /run/credentials/adguard-reward.service/adguard_password.
         deploy_test.go (new, beside main_test.go's TestDockerfileCMD) carries a
         ~30-line INI reader (sections, repeated keys) shared with t-6.
       test_contract:
         - [risk+verification+mvp] TestServiceUnit: DynamicUser=yes, StateDirectory=adguard-reward, NoNewPrivileges=yes, ProtectSystem=strict, PrivateTmp=yes, Restart=on-failure each asserted by exact section+key+value and named on failure; removing or misspelling any one fails
         - [risk+verification] ExecStart starts with an absolute binary path and contains `--config /etc/adguard-reward/config.yaml`; LoadCredential equals exactly "<id>:<file> (id adguard_password, file /etc/adguard-reward/adguard_password)"; a relative path, a dropped flag or a changed credential source fails
         - [risk] no `User=` and no `WorkingDirectory=` key may appear (either defeats DynamicUser / reintroduces a cwd-relative data dir); presence fails
         - [risk+verification+mvp] TestUnitConfigAgree: expected password_file is built as "/run/credentials/" + <unit filename> + "/" + <LoadCredential id>; the raw template's adguard.password_file must equal it. Renaming the credential id in one file only fails
         - [risk+verification] config.Load over deploy/config.nora.yaml (env-overriding only username and password_file to a temp file) asserts Listen == "127.0.0.1:8080", TrustedProxyNets() == [127.0.0.1/32], BaseURL has prefix "https://", AdGuard.URL == "http://127.0.0.1:80", and no `data_dir` key in the raw yaml; any drift fails
         - [mvp] api.ClientIP(cfg.TrustedProxyNets()) on the loaded template: RemoteAddr 127.0.0.1:x + X-Forwarded-For 100.64.0.7 → 100.64.0.7; RemoteAddr 100.64.0.9:x + same header → 100.64.0.9 (untrusted peer keeps the peer)
         - [risk+verification] template contains no non-empty `password:` and no non-empty `api_key:` value (rule r-03); a value there fails

  t-4  Add make build-linux with static ELF test                  [risk+verification; mvp's -trimpath fact]
       files:    Makefile, cmd/adguard-reward/build_test.go
       covers:   c-2
       depends:  —
       description:
         Makefile: `LINUX_BIN ?= bin/$(BIN)-linux-amd64`; `build-linux: build-web`
         runs `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS)
         -ldflags '$(LDFLAGS)' -o $(LINUX_BIN) ./cmd/$(BIN)`; .PHONY/help
         entries; VERSION pinned once per make invocation (risk F9);
         `test-go` exports ADGUARD_REWARD_BUILD_TEST=1. The Go test runs
         `make build-linux VERSION=test-0.0.0-gdeadbee LINUX_BIN=<tmp>` and
         inspects the artefact with debug/elf + debug/buildinfo without
         executing it. FACT (mvp, verified in-session with `go version -m`):
         under the Makefile's GOFLAGS=-trimpath the `-ldflags` setting is NOT
         recorded in buildinfo, so the version stamp cannot be read from
         buildinfo; it is proven by recipe text here and end-to-end by
         TestRun_Healthz (already pins version → /healthz body) and t-6's
         deploy gate.
       test_contract:
         - [risk] TestMakeBuildLinuxRecipe (always on): `make -n build-linux` output contains `CGO_ENABLED=0`, `GOOS=linux`, `GOARCH=amd64` and `-X main.version=` on the go build line; dropping any one fails
         - [verification] TestMakefileVersion: the Makefile's VERSION default line contains "git describe --tags"; hard-coding VERSION fails
         - [verification] TestBuildLinux (skips unless ADGUARD_REWARD_BUILD_TEST=1 and `make` is on PATH): elf.Open reports Class ELFCLASS64 and Machine EM_X86_64; a darwin or arm64 build fails
         - [risk+verification] TestBuildLinux: no PT_INTERP program header and DynString(DT_NEEDED) is empty (static, risk F8); a cgo-linked build fails
         - [risk+verification, corrected per mvp] TestBuildLinux: buildinfo.ReadFile settings contain CGO_ENABLED=0, GOOS=linux, GOARCH=amd64 — and NOT an `-ldflags` setting (it is absent under -trimpath; asserting it would never pass)
         - [verification] `make test` runs TestBuildLinux (test-go sets ADGUARD_REWARD_BUILD_TEST=1); `go test ./...` without it skips — recorded so a skip is never mistaken for a pass
         - [risk+mvp] /healthz reports the stamped version: already pinned by TestRun_Healthz (version="test-1" → body.version); this task adds no duplicate

Wave 2
  t-5  Nora config end-to-end through run(): state dir and phone limiter   [risk t-5 + verification t-6]
       files:    cmd/adguard-reward/main_test.go
       covers:   c-3 (integration), c-5 (testable clause)
       depends:  t-1, t-3
       description:
         TestRun_NoraConfig starts run() with `--config ../../deploy/config.nora.yaml`
         and env {STATE_DIRECTORY: tmp, ADGUARD_REWARD_LISTEN: 127.0.0.1:0,
         ADGUARD_REWARD_ADGUARD_URL: fake, ADGUARD_REWARD_ADGUARD_USERNAME,
         ADGUARD_REWARD_ADGUARD_PASSWORD_FILE: tmp file} — the real shipped
         template, the real ClientIP wiring, the real limiter, simulating
         tailscale serve by sending from 127.0.0.1 with X-Forwarded-For set to
         a CGNAT tailnet address.
       test_contract:
         - [risk+verification] after onListen, <STATE_DIRECTORY>/<store.FileName> exists and ./data was not created (F1 end-to-end); a data_dir key creeping back into the template, or data_dir resolution regressing, fails
         - [risk+verification] 5 bad logins with X-Forwarded-For: 100.64.0.5 from peer 127.0.0.1 → 6th is 429 and stderr has `ip=100.64.0.5` not `ip=127.0.0.1`; dropping trusted_proxies from the template, or ClientIP ignoring a bare-IP entry, yields 401 and fails
         - [risk+verification] immediately after, 1 bad login with X-Forwarded-For: 100.64.0.6 → 401 not 429 (limiter keyed per phone, not per proxy — risk F7 household lockout); a limiter keyed on the peer returns 429 and fails
         - [risk] X-Forwarded-For: fd7a:115c:a1e0::1 (tailnet IPv6) is logged as that address, not as 127.0.0.1
         - [verification+risk] the Set-Cookie on a successful login carries Secure and the listening line reports secure_cookie=true (base_url is https://); a template with an http:// base_url fails

  t-6  Install script and make deploy-nora with health gate       [risk skeleton; verification failure-path grafts; mvp end-to-end]
       files:    deploy/install-nora.sh, Makefile, cmd/adguard-reward/deploy_test.go
       covers:   c-4, c-5 (tailscale serve enablement)
       depends:  t-3, t-4
       description:
         `deploy-nora: build-linux` (Makefile, NORA_HOST ?= nora): `ssh $(NORA_HOST)
         uname -m` must print x86_64 (wrong-host guard); scp $(LINUX_BIN) to
         nora:/tmp/adguard-reward.new; scp unit + template to /tmp; then
         `ssh $(NORA_HOST) sudo -n env VERSION=$(VERSION) bash -s < deploy/install-nora.sh`.
         The script (set -euo pipefail; honours DESTDIR and HEALTH_TIMEOUT /
         HEALTH_INTERVAL; finds systemctl/curl/tailscale/journalctl on PATH so
         tests can stub them): install -m 0755 binary → /usr/local/bin/adguard-reward
         (unlink-then-create, F4); install unit 0644 → /etc/systemd/system; if
         /etc/adguard-reward/config.yaml is absent, install template 0644 with
         TAILNET replaced by the MagicDNS suffix from `tailscale status --json`;
         if the credential file is absent, create it 0600 containing CHANGE-ME;
         if config still holds CHANGE-ME or TAILNET, or the credential is
         CHANGE-ME → print exactly what to edit and exit 3 before touching the
         service (F6); daemon-reload; enable --now / restart; `tailscale serve
         --bg --https=443 http://127.0.0.1:8080` (literal 127.0.0.1, F7); poll
         curl http://127.0.0.1:8080/healthz until 200 or HEALTH_TIMEOUT (default
         45 s, F5), then `grep -F "\"version\":\"$VERSION\""` once (F9) —
         mismatch exits 4 naming both versions; any health failure prints
         `journalctl -u adguard-reward -n 20` before exiting. The script never
         reads or prints the credential file's contents.
       test_contract:
         - [risk] TestInstallNora_FirstRun (DESTDIR=tmp, stub systemctl/curl/tailscale/journalctl on PATH): config.yaml 0644 with TAILNET replaced by the stub's suffix, adguard_password 0600 containing CHANGE-ME, binary 0755 at usr/local/bin, unit under etc/systemd/system; exit code 3 and the systemctl stub log is empty (no restart with a placeholder credential); any of these differing fails
         - [risk] TestInstallNora_SecondRunPreservesEdits (F3): pre-seed config.yaml with a marker line and a real-looking credential, run the script → both files byte-identical to the seed (sha256 compared), binary and unit replaced; an unconditional install of either file fails
         - [risk+verification] TestInstallNora_HealthGate: with a valid config, the curl stub returns `{"status":"ok","version":"other"}` → exit 4, stderr names both versions, and the systemctl log shows daemon-reload then restart (in that order) before the curl; returns `{"version":"<VERSION>"}` → exit 0. A script that skips the compare, only checks HTTP 200, or compares before restart, fails
         - [risk+verification] TestInstallNora_PollsUntilUp (F5): with HEALTH_TIMEOUT=2 HEALTH_INTERVAL=0.2, the curl stub fails its first 2 invocations (counter file) then answers 200 with the right version → exit 0; a one-shot check fails
         - [verification] TestInstallNora_Unhealthy: curl stub exits 22 for every attempt under HEALTH_TIMEOUT=1 → non-zero exit and the journalctl stub log shows `-u adguard-reward` was requested
         - [verification, adapted] TestInstallNora_NoSecretOutput: the seeded credential file contains a sentinel; the sentinel appears in no stub log and in neither captured stdout nor stderr of the script (checked with a boolean contains, never printed)
         - [risk] the tailscale stub's recorded argv contains `serve`, `--bg`, and the literal `http://127.0.0.1:8080`; `localhost` in the target fails (F7)
         - [risk+verification] TestMakeDeployNoraChain: `make -n deploy-nora VERSION=v` output contains `GOOS=linux GOARCH=amd64` before `install-nora.sh`, and contains `uname -m`, `sudo -n`, `bash -s` and `VERSION=`; the binary path scp'd equals $(LINUX_BIN) from the same -n output; losing the build-linux prerequisite or renaming the build output in one place only fails
         - [risk] deploy_test.go asserts the script's `install` target path equals the unit's ExecStart binary path parsed by t-3's INI reader (F4)
         - [mvp] end-to-end: if -X main.version stops reaching the binary, the real deploy's gate fails — /healthz reports "dev" while $(VERSION) is the git-describe string (observed in t-7, not asserted here)

Wave 3
  t-7  Deploy to nora, runbook, record real-use observations     [risk+verification]
       files:    docs/deploy-nora.md, .dross/phases/06-deploy-nora/verify.toml
       covers:   c-4 (live run), c-5 (install prompt, manual), c-7
       depends:  t-5, t-6
       description:
         Runbook: prerequisites (ssh alias nora, passwordless `sudo -n`, systemd
         ≥ 247 for LoadCredential, tailscale on nora), first run (`make
         deploy-nora` exits 3 → on nora write the AdGuard password into the
         0600 file with `umask 077; cat > …`, set adguard.username, rerun),
         phone install steps, exit codes 3 and 4 and what a failed health gate
         means. Then perform the deploy and the phone observations and record
         them (locked real_use_evidence: manual records, not tests).
       test_contract:
         - [risk+verification] verify.toml c-4 records the observed `make deploy-nora` exit 0 and the /healthz version string equal to `git describe --tags --always --dirty` of the deployed commit, with date — a run not observed green in-session must not be recorded (global rule 1)
         - [risk+verification] verify.toml records for c-5: date, phone model/OS, "https://nora.<tailnet>.ts.net/ showed the install prompt" and "launched standalone (no browser chrome)"; a missing record fails c-5
         - [risk+verification] verify.toml records for c-7: date, child's device name as known to AdGuard, service tapped, grant length, seconds from tap to the service resolving on the device (≤ 5), and "block returned at expiry without intervention" with the expiry time; a missing record or latency > 5 s fails c-7
         - [risk] docs/deploy-nora.md names exit codes 3 and 4 with their meaning, matching the script (a Go test in deploy_test.go greps the doc for both codes; if the script's codes change without the doc, it fails)
         - [verification] no secret value appears in verify.toml, the doc or the commit (r-03; global secret rule 1) — a `grep -q` boolean is the only permitted check

Coverage
- c-1 → t-3
- c-2 → t-4
- c-3 → t-1, t-5
- c-4 → t-6, t-7 (live)
- c-5 → t-3 (config + ClientIP), t-5 (XFF/limiter through run()), t-6 (tailscale serve), t-7 (manual install-prompt record)
- c-6 → t-2
- c-7 → t-7

## Disagreements

1. **Where the deploy logic lives and how c-4 is tested.**
   risk: a remote install script (`deploy/install-nora.sh`) piped over
   `ssh sudo -n bash -s`, run locally under DESTDIR with PATH stubs for
   systemctl/curl/tailscale. verification: a local orchestration script
   (`deploy/deploy-nora.sh`) that issues ssh/scp, tested with ssh/scp shims that
   log argv. mvp: inline Makefile recipe, proven only by the real deploy plus a
   config mtime observation in verify.toml.
   Default: risk. Only risk's harness executes the first-run guard, the
   permission bits and the version compare under `go test`; verification's shims
   prove ssh command order but the remote logic is an untested string; mvp leaves
   both "first run only" and "fails unless … version" without a local test.
   Cost of the default: the Makefile-side ssh/scp lines are covered only by
   `make -n` text.

2. **First-run behaviour on a virgin host.**
   risk: placeholder guard — if config still says CHANGE-ME/TAILNET or the
   credential is CHANGE-ME, exit 3 before daemon-reload/restart. verification and
   mvp: install an empty 0600 credential, restart, let the health gate fail with
   a hint. Default: risk. With Restart=on-failure an empty/placeholder
   credential means the unit loops: startup probe → bad credentials → exit 1 →
   restart every 5 s, each loop hitting AdGuard's /control/login and risking
   AdGuard's own failed-login throttling on the service account. Exit 3 is a
   clean, actionable stop. Tied to this: username placeholder "CHANGE-ME" (risk)
   over "" (mvp/verification) — empty fails config validation anyway, CHANGE-ME
   is greppable by the guard.

3. **Who enables `tailscale serve`.**
   risk: inside install-nora.sh every run (idempotent, stubbed in test, literal
   127.0.0.1 target asserted). mvp: inside deploy-nora every run. verification:
   a one-time manual step in the runbook. Default: in the script (2 of 3, and
   c-5's testable half — the literal loopback target that keeps trusted_proxies
   matching — only gets a contract if the invocation is in a tested file).
   Why it matters: manual enablement means a rebuilt host silently loses c-5;
   automated enablement requires `tailscale` on root's PATH under `sudo -n` and
   the `serve --bg --https=443` syntax, which has changed across tailscale
   releases — t-7 must confirm the installed version accepts it.

4. **How the version stamp is proven for c-2.**
   risk and verification: assert an `-ldflags` setting containing
   `-X main.version=` in debug/buildinfo. mvp: it is not there under -trimpath;
   prove it end-to-end via /healthz. Checked: mvp is right (Makefile has
   `GOFLAGS ?= -trimpath`; `go version -m` on an in-session cross-build shows
   `-trimpath=true`, `CGO_ENABLED=0`, `GOOS`, `GOARCH` and no `-ldflags`).
   Default: buildinfo for CGO/GOOS/GOARCH + `make -n` recipe text for the `-X`
   flag + TestRun_Healthz and the t-6 gate end-to-end. No draft proposes the one
   artefact-level check that would work (scan the built binary's bytes for a
   sentinel version string, which survives `-s -w`); left out per the
   no-invention rule but noted for the executor. Why it matters: as drafted the
   contract can never pass and would block t-4 at execution.

5. **Gating and shape of the cross-build test.**
   risk: skip under `-short`, and the test does its own `go build` into a temp
   dir. verification: skip unless ADGUARD_REWARD_BUILD_TEST=1 (exported by
   `make test-go`), and the test runs the real `make build-linux LINUX_BIN=<tmp>`.
   Default: verification for both. gremlins passes no `-short` and re-runs
   cmd/adguard-reward's tests for every internal/grants mutant, so a `-short`
   gate runs a cross-compile per mutant; the env gate does not. Running the real
   make target tests the recipe that ships, not a re-typed copy of it; the cost
   (a pnpm build) is paid only under `make test`. risk's always-on `make -n`
   text check is kept so a plain `go test ./...` still sees recipe drift.

6. **Test file placement.** mvp: unit/config tests appended to main_test.go
   (already ~1370 lines). risk/verification: new deploy_test.go and
   build_test.go in the same package. Default: new files. Low stakes.

7. **Template file name.** risk/mvp: `deploy/config.nora.yaml`. verification:
   `deploy/config.systemd.yaml` (argument: it is layout-specific, not
   host-specific). Default: config.nora.yaml — base_url carries nora's hostname
   and adguard.url is nora's view of AdGuard, so the file is host-specific in
   fact. Rename is trivial if the M4 Docker/proxy work wants a generic one.

8. **Granularity: 4 tasks vs 7.** mvp bundles build-linux, deploy-nora, the
   c-5 install-prompt observation and the c-7 real-use observation into one
   Makefile task. Default: 7 tasks. A failed phone observation would block a
   Makefile commit, and in pair mode each approval should gate one artefact
   cluster; the manual observations belong in a wave-3 task with no code.

9. **t-7's dependency on the mutation task.** verification makes the final
   deploy task depend on t-2 (c-6); risk does not. Default: not a dependency —
   the deploy needs nothing from the reconcile test, and the mutation run is
   part of verify, not of t-7. Harmless either way; recorded because it changes
   whether t-2 can slip without blocking the deploy.

10. **Run-level c-3 assertion in wave 1.** verification adds
    TestRun_StateDirectory to t-3 (its data_dir task) with a chdir into a temp
    dir to observe ./data, and claims the `listening` line reports data_dir (it
    does not — main.go:170 logs addr/version/tls/secure_cookie). risk asserts the
    run-level path only in wave 2 through the nora template. Default: risk —
    the Load-level tests already assert both halves of c-3, and the run-level
    check through the shipped template in t-5 is the one that matters on nora;
    a chdir-based test forfeits t.Parallel in the heaviest test package for no
    extra coverage.
