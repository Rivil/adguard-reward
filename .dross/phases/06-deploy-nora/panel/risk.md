# 06-deploy-nora — risk-lens draft

Lens: every task is shaped around one cluster of things that break on a real
host that no test on this machine can see. The failure modes that drove the
graph:

- F1  `data_dir` explicitly `./data` in the installed config (config.example.yaml
      ships it that way) beats `$STATE_DIRECTORY`; under DynamicUser +
      ProtectSystem=strict cwd is `/`, `MkdirAll("./data")` fails, exit 1,
      systemd restart loop. `$STATE_DIRECTORY` can also be a colon-separated
      list when a unit names several directories.
- F2  Unit ↔ config drift: `LoadCredential=<id>:…` and
      `adguard.password_file: /run/credentials/<unit>.service/<id>` are two
      strings in two files that must agree or the service exits 1 on boot.
- F3  Re-deploy overwrites a hand-edited `/etc/adguard-reward/config.yaml` or
      the password file ("first run only" clause silently violated).
- F4  Restart "succeeds" but the old binary is still serving (copy hit
      ETXTBSY, wrong install path, unit not reloaded) — /healthz answers 200
      with the previous version.
- F5  Health check races the listener: startup reconcile holds the listener
      closed up to 30 s when AdGuard is slow; a one-shot curl fails spuriously.
- F6  Placeholder credential on first run → startup probe → ErrBadCredentials →
      exit 1 → restart loop, and the deploy reports a vague health failure.
- F7  Proxy peer is not 127.0.0.1 (tailscale serve pointed at `localhost` →
      ::1) → trusted_proxies [127.0.0.1] never matches → every phone shares one
      limiter key (`::1`/peer) → 5 bad logins lock the whole household out; or
      the app doesn't listen on ::1 at all → 502 from tailscale.
- F8  Non-static or wrong-arch binary (cgo leak, GOOS unset on macOS) — runs
      here, `Exec format error` on nora.
- F9  Version string mismatch between what Make stamped and what Make compares
      (`-dirty`, `+`, `/` characters in a grep pattern).
- F10 Reconciler log-line mutants (reconcile.go:133/135): survivors mean a
      failing pass can go unlogged on nora, which is the only place a human
      will ever look for it.

Phase 06-deploy-nora — 7 tasks across 3 waves

Wave 1
  t-1  Resolve data_dir from $STATE_DIRECTORY
       files:    internal/config/config.go, internal/config/config_test.go, deploy/config.example.yaml
       covers:   c-3
       depends:  —
       description:
         Defaults().DataDir becomes "" and Load resolves it last: explicit
         (yaml or ADGUARD_REWARD_DATA_DIR) wins; else first entry of
         lookupEnv("STATE_DIRECTORY") split on ":"; else "./data". Empty
         string from any source means "unset". config.example.yaml drops the
         literal `data_dir: "./data"` in favour of a commented-out key
         explaining the systemd fallback (F1).
       contract:
         - yaml with no data_dir + STATE_DIRECTORY=/var/lib/x → cfg.DataDir == "/var/lib/x"; if the fallback is dropped, TestLoad_StateDirectory fails
         - no data_dir, no STATE_DIRECTORY → "./data" (TestLoad_Defaults keeps asserting "./data" post-Load; if Defaults() is emptied without the resolver, it fails)
         - yaml data_dir "/var/lib/reward" + STATE_DIRECTORY=/other → "/var/lib/reward" (explicit beats systemd; if precedence is inverted the test fails)
         - ADGUARD_REWARD_DATA_DIR="" (set-but-empty) + STATE_DIRECTORY=/s → "/s"; ADGUARD_REWARD_DATA_DIR="/e" + STATE_DIRECTORY=/s → "/e"
         - STATE_DIRECTORY="/a:/b" → "/a" (colon list); if split is missing, DataDir contains ":" and the test fails
         - TestExampleConfigLoads additionally asserts cfg.DataDir == "./data" with no STATE_DIRECTORY and == STATE_DIRECTORY with it set — a re-introduced literal `data_dir: "./data"` in the example config fails the second half (F1)

  t-2  Kill reconcile Run log-line mutants
       files:    internal/grants/reconcile_test.go
       covers:   c-6
       depends:  —
       description:
         Add TestReconcile_RunLogs driving Engine.Run at a 20 ms interval
         against e.logs (newEnv already wires a TextHandler) and asserting
         exactly which of the two Run log lines appear per pass outcome
         (F10). Verify by hand with gremlins on internal/grants before
         committing; note the result in the commit body.
       contract:
         - AdGuard /control/clients → 500: stderr gains a line with msg="reconcile failed" within 300 ms; the :133 NEGATION `err == nil` mutant makes a failing pass silent and the test fails
         - clean pass with nothing to do: no msg="reconcile" line and no "reconcile failed" line (asserted by exact msg, so "startup reconcile" cannot mask it); the :135 BOUNDARY `>= 0` mutant logs on every quiet pass and fails here
         - one seeded overdue grant with suppressed timers (withNoTimers): a msg="reconcile" line with reverted=1 appears; the :135 NEGATION `<= 0` mutant suppresses it and fails
         - parent re-block drift (MutateClient) → msg="reconcile" line with repaired=1 (kills the mutant on the second operand of the ||)
         - fake hangs, ctx cancelled mid-pass: Run returns and no "reconcile failed" line is emitted; the :133 NEGATION on `ctx.Err() == nil` fails here
         - gremlins run over internal/grants reports none of reconcile.go:133/135 CONDITIONALS_* surviving

  t-3  Add systemd unit, nora config template, parse test
       files:    deploy/adguard-reward.service, deploy/config.nora.yaml, cmd/adguard-reward/deploy_test.go
       covers:   c-1, c-5 (config half: trusted_proxies + loopback listen)
       depends:  —
       description:
         Unit: [Unit] After=network-online.target AdGuardHome.service;
         [Service] DynamicUser=yes, StateDirectory=adguard-reward,
         LoadCredential=<id>:<file> (id adguard_password, file /etc/adguard-reward/adguard_password),
         ExecStart=/usr/local/bin/adguard-reward --config /etc/adguard-reward/config.yaml,
         NoNewPrivileges=yes, ProtectSystem=strict, PrivateTmp=yes,
         ProtectHome=yes, Restart=on-failure, RestartSec=5s;
         [Install] WantedBy=multi-user.target. Template config (no secrets,
         no data_dir key): listen "127.0.0.1:8080", base_url
         "https://nora.TAILNET.ts.net", trusted_proxies ["127.0.0.1"],
         adguard.url http://127.0.0.1:80, adguard.username "CHANGE-ME",
         adguard.password_file /run/credentials/adguard-reward.service/adguard_password.
         deploy_test.go carries a 30-line INI reader (sections, repeated keys).
       contract:
         - TestServiceUnit: DynamicUser=yes, StateDirectory=adguard-reward, NoNewPrivileges=yes, ProtectSystem=strict, PrivateTmp=yes, Restart=on-failure each asserted by exact section+key+value; removing or misspelling any one fails the test
         - ExecStart is asserted to contain `--config /etc/adguard-reward/config.yaml` and to start with an absolute binary path; a relative path or a dropped flag fails
         - no `User=` and no `WorkingDirectory=` key may appear (either one would defeat DynamicUser / reintroduce a cwd-relative data dir); presence fails
         - TestUnitConfigAgree (F2): the expected password_file is built as "/run/credentials/" + <unit filename> + "/" + <LoadCredential id>; config.Load over deploy/config.nora.yaml (env-overriding password_file to a temp file, and ADGUARD_URL untouched) must report cfg.AdGuard.PasswordFile before override == that built path. Renaming the credential id in one file only fails the test
         - config.Load of the template asserts Listen == "127.0.0.1:8080", TrustedProxyNets() == [127.0.0.1/32], AdGuard.URL == "http://127.0.0.1:80", and no `data_dir` key present in the raw yaml (F1/F7); any drift fails
         - template contains no `password:` inline value and no `api_key:` value (rule r-03); a value there fails

  t-4  Add make build-linux with static ELF test
       files:    Makefile, cmd/adguard-reward/build_test.go
       covers:   c-2
       depends:  —
       description:
         `build-linux: build-web` runs `CGO_ENABLED=0 GOOS=linux GOARCH=amd64
         go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/adguard-reward-linux-amd64
         ./cmd/adguard-reward`; VERSION is pinned once per make invocation
         (`VERSION := $(shell …)` semantics, F9). Test cross-builds to a temp
         dir with -X main.version=build-test and inspects the artefact without
         executing it; skipped under -short.
       contract:
         - `make -n build-linux` output (captured by the test, no execution) contains `CGO_ENABLED=0`, `GOOS=linux`, `GOARCH=amd64` and `-X main.version=` on the go build line; dropping any one fails TestMakeBuildLinuxRecipe
         - debug/elf on the test-built binary: Machine == EM_X86_64, Class == ELFCLASS64, and no PT_INTERP program header / no `.interp` section (static, F8); a cgo-linked or host-arch build fails TestBuildLinuxStatic
         - debug/buildinfo on the same file reports GOOS=linux, GOARCH=amd64, CGO_ENABLED=0 and an -ldflags setting containing `-X main.version=build-test`; a build that lost the stamp fails
         - /healthz reports the stamped version: already pinned by TestRun_Healthz (version="test-1" → body.version); this task adds no duplicate

Wave 2
  t-5  Nora config end-to-end: state dir and phone limiter
       files:    cmd/adguard-reward/main_test.go
       covers:   c-3, c-5 (testable clause)
       depends:  t-1, t-3
       description:
         TestRun_NoraConfig starts run() with `--config deploy/config.nora.yaml`
         and env {STATE_DIRECTORY: tmp, ADGUARD_REWARD_LISTEN: 127.0.0.1:0,
         ADGUARD_REWARD_ADGUARD_URL: fake, ADGUARD_REWARD_ADGUARD_USERNAME,
         ADGUARD_REWARD_ADGUARD_PASSWORD_FILE: tmp file} — the real shipped
         template, the real ClientIP wiring, the real limiter.
       contract:
         - after onListen, <STATE_DIRECTORY>/<store.FileName> exists and ./data was not created (F1 end-to-end); if data_dir resolution regresses the file lands elsewhere and the test fails
         - 5 bad logins with X-Forwarded-For: 100.101.102.103 from peer 127.0.0.1 → 6th is 429 and stderr has `ip=100.101.102.103`; if trusted_proxies is dropped from the template or ClientIP stops walking XFF, the 6th is 401 and the test fails
         - immediately after, 1 bad login with X-Forwarded-For: 100.101.102.104 → 401 not 429 (limiter keyed per phone, not per proxy — the household-lockout failure F7); a limiter keyed on the peer returns 429 and fails
         - X-Forwarded-For: fd7a:115c:a1e0::1 (tailnet IPv6) is logged as that address, not as 127.0.0.1
         - the listening log line reports secure_cookie=true (base_url is https://); a template with an http:// base_url fails

  t-6  Install script and make deploy-nora with health gate
       files:    deploy/install-nora.sh, Makefile, cmd/adguard-reward/deploy_test.go
       covers:   c-4, c-5 (tailscale serve enablement)
       depends:  t-3, t-4
       description:
         `deploy-nora: build-linux` does: `ssh nora uname -m` must print
         x86_64 (wrong-host guard); scp the binary to nora:/tmp/adguard-reward.new;
         scp unit + template to /tmp; then `ssh nora sudo -n env VERSION=$(VERSION) bash -s < deploy/install-nora.sh`.
         The script (set -euo pipefail; honours DESTDIR, and finds
         systemctl/curl/tailscale on PATH so tests can stub them): install
         -m 0755 binary → /usr/local/bin/adguard-reward (unlink-then-create,
         F4); install unit → /etc/systemd/system; if /etc/adguard-reward/config.yaml
         is absent, install template 0644 with TAILNET replaced by the
         MagicDNS suffix from `tailscale status --json`; if the credential file
         is absent, create it 0600 containing CHANGE-ME; if config still holds
         CHANGE-ME or TAILNET, or the credential is CHANGE-ME → print exactly
         what to edit and exit 3 before touching the service (F6);
         daemon-reload; enable --now / restart; `tailscale serve --bg --https=443
         http://127.0.0.1:8080` (literal 127.0.0.1, F7); poll curl
         http://127.0.0.1:8080/healthz up to 45 s until 200 (F5), then
         `grep -F "\"version\":\"$VERSION\""` once (F9) — mismatch exits 4.
       contract:
         - TestInstallNora_FirstRun (DESTDIR=tmp, stub systemctl/curl/tailscale on PATH): config.yaml 0644 with TAILNET replaced by the stub's suffix, adguard_password 0600 containing CHANGE-ME, binary 0755 at usr/local/bin, unit under etc/systemd/system; exit code 3 and the systemctl stub log is empty (no restart with a placeholder credential); any of these differing fails
         - TestInstallNora_SecondRunPreservesEdits (F3): pre-seed config.yaml with a marker line and a real-looking credential, run the script → both files byte-identical to the seed (sha256 compared in the test), binary and unit replaced; an unconditional install of either file fails the test
         - TestInstallNora_HealthGate: with a valid config, the curl stub returns `{"status":"ok","version":"other"}` → exit 4 and the systemctl log shows daemon-reload then restart (in that order); returns `{"version":"<VERSION>"}` → exit 0. A script that skips the compare, or compares before restart, fails
         - TestInstallNora_PollsUntilUp (F5): curl stub fails its first 2 invocations (counter file) then answers 200 with the right version → exit 0; a one-shot check fails
         - the tailscale stub's recorded argv contains `serve`, `--bg`, and the literal `http://127.0.0.1:8080`; `localhost` in the target fails (F7)
         - `make -n deploy-nora` contains `uname -m`, `sudo -n`, `bash -s` and `VERSION=`; the binary path scp'd equals the one build-linux writes (parsed from the same -n output); a rename of the build output in one place only fails
         - deploy_test.go asserts the script's `install` target path equals the unit's ExecStart binary path from t-3's parsed unit (F4)

Wave 3
  t-7  Deploy to nora and record real-use observations
       files:    docs/deploy-nora.md, .dross/phases/06-deploy-nora/verify.toml
       covers:   c-5 (install prompt, manual), c-7
       depends:  t-5, t-6
       description:
         Runbook: prerequisites (ssh alias nora, passwordless sudo, systemd ≥
         247 for LoadCredential, tailscale on nora), first run (`make deploy-nora`
         exits 3 → write the AdGuard password into the 0600 file with `umask 077;
         cat > …` on nora, set adguard.username, rerun), phone install steps,
         and what a failed health gate means. Then perform the deploy and the
         phone observations and record them.
       contract:
         - verify.toml records the observation "https://nora.<tailnet>.ts.net/ shows the browser install prompt and launches standalone" with date and device; a missing record fails c-5
         - verify.toml records "one tap unblocked <service> on <child device> within 5 s; block returned at expiry with no intervention" with date, device, service and grant length; a missing record fails c-7
         - verify.toml records the `make deploy-nora` run's final line showing /healthz version == `git describe` output of the deployed commit (F4/F9 observed on the real host)
         - docs/deploy-nora.md names the exit codes 3 and 4 with their meaning, matching the script (a Go test in deploy_test.go greps the doc for both codes; if the script's codes change without the doc, it fails)

## Coverage
- c-1 → t-3
- c-2 → t-4
- c-3 → t-1, t-5
- c-4 → t-6
- c-5 → t-3 (config), t-5 (XFF/limiter test), t-6 (tailscale serve), t-7 (manual install-prompt record)
- c-6 → t-2
- c-7 → t-7

## Judgment calls
- Empty `data_dir` means "unset" at every layer (yaml, env, defaults) and the example config loses its literal `./data`; rejected tracking a "was set" flag through Load — a boolean beside every source is more surface for the same result, and the example config shipping `./data` is the actual crash (F1).
- The nora config is a second committed template (deploy/config.nora.yaml) rather than sed over config.example.yaml at deploy time; rejected the sed route because a Go test can load a committed file but not the output of a Make recipe, and F2/F7 need the file pinned.
- The health gate, first-run guard and tailscale serve live in a bash script run over `ssh sudo bash -s`, not inline Makefile shell; rejected inline because only a file on disk can be run twice against DESTDIR with stubbed systemctl/curl to prove "first run only" (F3) and "fails unless 200 with the just-built version" (F4/F5).
- First deploy on a virgin host deliberately exits 3 before restarting; rejected reading the password into the deploy (argv/ssh env) — secrets never transit the deploy tooling (global secret rules), and a placeholder-credential restart is a guaranteed crash loop (F6).
- Tailnet name is derived on nora from `tailscale status --json`, not passed as a make variable; rejected the make var because the Mac's tailscale CLI is not on PATH by default and a wrong/absent value would leave base_url http-less → cookie not Secure without any test noticing.
- Static-binary proof uses debug/elf + debug/buildinfo on a test-driven cross build plus `make -n` recipe text; rejected running `make build-linux` inside a test (pulls pnpm build into every `go test`) and rejected `file(1)` (not portable between the Mac and CI).
- The c-6 test asserts on log lines by exact `msg=` value; rejected refactoring Run to return per-pass outcomes for testability — the survivors are in Run's logging switch and the fix must stay in test code so phase-04 behaviour is untouched.
- Per-phone limiter isolation (second XFF address gets 401 after the first is locked) is asserted explicitly in t-5; rejected relying on TestRun_TrustedProxies, which uses 127.0.0.0/8 and never checks that a second client stays unlocked — the household-lockout failure (F7) is the one that would actually be reported from nora.
- `sudo -n` (non-interactive) is required and its failure is a clear early error; rejected `ssh -t` so that the stdin-fed script and the health-gate output stay machine-readable.
