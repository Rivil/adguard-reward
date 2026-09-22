package main

// Tests over the shipped deployment artefacts: the systemd unit and the nora
// config template. They are data, not code, so nothing else would notice a
// typo in them until the service failed to start on the host.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Rivil/adguard-reward/internal/api"
	"github.com/Rivil/adguard-reward/internal/config"
)

const (
	unitPath       = "../../deploy/adguard-reward.service"
	noraConfigPath = "../../deploy/config.nora.yaml"

	// The credential id the unit hands to systemd. The config's
	// password_file must name the tmpfs path systemd derives from it —
	// TestUnitConfigAgree builds that path rather than repeating it.
	credentialID = "adguard_password"
)

// unitFile is a parsed systemd unit: section -> key -> values in file order.
// systemd lets a key repeat (LoadCredential=, Environment=), and a repeat is
// exactly the kind of mistake worth catching, so every key holds a slice.
type unitFile map[string]map[string][]string

// parseINI reads a systemd-style unit. It is deliberately small: sections in
// [brackets], key=value lines, # and ; comments, blank lines ignored.
func parseINI(t *testing.T, path string) unitFile {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := unitFile{}
	section := ""
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			if _, ok := out[section]; !ok {
				out[section] = map[string][]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("%s:%d: not a key=value line: %q", path, i+1, line)
		}
		if section == "" {
			t.Fatalf("%s:%d: key %q before any [Section]", path, i+1, k)
		}
		k = strings.TrimSpace(k)
		out[section][k] = append(out[section][k], strings.TrimSpace(v))
	}
	return out
}

// one returns the single value of section/key, failing if it is missing or
// repeated.
func (u unitFile) one(t *testing.T, section, key string) string {
	t.Helper()
	vs := u[section][key]
	switch len(vs) {
	case 1:
		return vs[0]
	case 0:
		t.Fatalf("unit has no %s in [%s]", key, section)
	}
	t.Fatalf("unit repeats %s in [%s] %d times: %v", key, section, len(vs), vs)
	return ""
}

// has reports whether key appears anywhere in the unit.
func (u unitFile) has(key string) bool {
	for _, keys := range u {
		if _, ok := keys[key]; ok {
			return true
		}
	}
	return false
}

func TestServiceUnit(t *testing.T) {
	u := parseINI(t, unitPath)

	// The sandbox. Every one of these is load-bearing: without DynamicUser
	// the service runs as root, without StateDirectory it has nowhere
	// writable at all under ProtectSystem=strict.
	for key, want := range map[string]string{
		"DynamicUser":     "yes",
		"StateDirectory":  "adguard-reward",
		"NoNewPrivileges": "yes",
		"ProtectSystem":   "strict",
		"ProtectHome":     "yes",
		"PrivateTmp":      "yes",
		"Restart":         "on-failure",
	} {
		if got := u.one(t, "Service", key); got != want {
			t.Errorf("[Service] %s = %q, want %q", key, got, want)
		}
	}

	exec := u.one(t, "Service", "ExecStart")
	if !strings.HasPrefix(exec, "/") {
		t.Errorf("ExecStart = %q, want an absolute binary path (systemd rejects a relative one)", exec)
	}
	if !strings.Contains(exec, "--config /etc/adguard-reward/config.yaml") {
		t.Errorf("ExecStart = %q, want --config /etc/adguard-reward/config.yaml", exec)
	}

	if got, want := u.one(t, "Service", "LoadCredential"),
		credentialID+":/etc/adguard-reward/adguard_password"; got != want {
		t.Errorf("LoadCredential = %q, want %q", got, want)
	}

	// User= would override DynamicUser; WorkingDirectory= would give the
	// process a cwd and let a relative data_dir look like it works.
	for _, key := range []string{"User", "Group", "WorkingDirectory"} {
		if u.has(key) {
			t.Errorf("unit sets %s=; it must not (DynamicUser owns the identity, $STATE_DIRECTORY the data dir)", key)
		}
	}

	if got, want := u.one(t, "Install", "WantedBy"), "multi-user.target"; got != want {
		t.Errorf("[Install] WantedBy = %q, want %q", got, want)
	}
}

// execBinary is ExecStart's binary path — the first field, before the flags.
func execBinary(t *testing.T, u unitFile) string {
	t.Helper()
	return strings.Fields(u.one(t, "Service", "ExecStart"))[0]
}

// rawNoraConfig is the template decoded as plain YAML, for the assertions
// that are about the file's text rather than the loaded Config (an absent key
// and a loaded zero value are the same thing after Load).
func rawNoraConfig(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(noraConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestUnitConfigAgree derives the credential path from the unit rather than
// repeating it, so renaming the credential in one file and not the other
// fails here instead of at the first login on the host.
func TestUnitConfigAgree(t *testing.T) {
	u := parseINI(t, unitPath)

	id, _, ok := strings.Cut(u.one(t, "Service", "LoadCredential"), ":")
	if !ok {
		t.Fatal("LoadCredential is not <id>:<file>")
	}
	want := "/run/credentials/" + filepath.Base(unitPath) + "/" + id

	adg, _ := rawNoraConfig(t)["adguard"].(map[string]any)
	if got, _ := adg["password_file"].(string); got != want {
		t.Errorf("config.nora.yaml adguard.password_file = %q, want %q (systemd's path for LoadCredential id %q)", got, want, id)
	}
}

// noraConfig loads the shipped template, overriding only what cannot be
// shipped: the placeholder username and the credential path, which exists
// only under systemd.
func noraConfig(t *testing.T) *config.Config {
	t.Helper()
	pw := filepath.Join(t.TempDir(), "adguard_password")
	if err := os.WriteFile(pw, []byte("pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(noraConfigPath, true, envOf(map[string]string{
		"ADGUARD_REWARD_ADGUARD_USERNAME":      "svc",
		"ADGUARD_REWARD_ADGUARD_PASSWORD_FILE": pw,
	}))
	if err != nil {
		t.Fatalf("shipped nora template does not load: %v", err)
	}
	return cfg
}

func TestNoraConfigLoads(t *testing.T) {
	cfg := noraConfig(t)

	// Locked decision tls_path: the binary is plain HTTP on the loopback and
	// tailscale serve owns 443, so it must not bind a public address or a cert.
	if got, want := cfg.Listen, "127.0.0.1:8080"; got != want {
		t.Errorf("listen = %q, want %q", got, want)
	}
	if cfg.TLS.Cert != "" || cfg.TLS.Key != "" {
		t.Errorf("tls = %+v, want empty (tailscale serve terminates HTTPS)", cfg.TLS)
	}
	if !strings.HasPrefix(cfg.BaseURL, "https://") {
		t.Errorf("base_url = %q, want an https:// origin (no secure context, no PWA install and no Secure cookie)", cfg.BaseURL)
	}

	nets := cfg.TrustedProxyNets()
	if len(nets) != 1 || nets[0].String() != "127.0.0.1/32" {
		t.Errorf("trusted_proxies = %v, want [127.0.0.1/32]", nets)
	}

	// Locked decision nora_target: AdGuard as nora itself sees it.
	if got, want := cfg.AdGuard.URL, "http://127.0.0.1:80"; got != want {
		t.Errorf("adguard.url = %q, want %q", got, want)
	}

	// A literal data_dir would beat $STATE_DIRECTORY, and a relative one is
	// unwritable under ProtectSystem=strict — the service would crash-loop.
	if _, ok := rawNoraConfig(t)["data_dir"]; ok {
		t.Error("config.nora.yaml sets data_dir; it must not (StateDirectory supplies it)")
	}
}

// TestNoraConfigClientIP wires the template's trusted_proxies through the
// real resolver. tailscale serve is the only hop, so a forwarded address is
// the phone; a peer that is not the proxy is believed over its own header.
func TestNoraConfigClientIP(t *testing.T) {
	clientIP := api.ClientIP(noraConfig(t).TrustedProxyNets())

	for _, tc := range []struct {
		name, remote, xff, want string
	}{
		{"through the proxy", "127.0.0.1:54321", "100.64.0.7", "100.64.0.7"},
		{"peer is not the proxy", "100.64.0.9:54321", "100.64.0.7", "100.64.0.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: tc.remote, Header: http.Header{}}
			r.Header.Set("X-Forwarded-For", tc.xff)
			if got := clientIP(r); got != netip.MustParseAddr(tc.want) {
				t.Errorf("ClientIP(remote %s, XFF %s) = %v, want %s", tc.remote, tc.xff, got, tc.want)
			}
		})
	}
}

// TestNoraConfigNoSecrets enforces rule r-03 on the one config file that is
// committed and then copied onto a host: secrets reach the process through
// the credential file, never through here.
func TestNoraConfigNoSecrets(t *testing.T) {
	var walk func(path string, v any)
	walk = func(path string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		for k, val := range m {
			at := k
			if path != "" {
				at = path + "." + k
			}
			if k == "password" || k == "api_key" {
				if s, _ := val.(string); s != "" {
					t.Errorf("config.nora.yaml sets %s to a non-empty value; secrets belong in the credential file (rule r-03)", at)
				}
			}
			walk(at, val)
		}
	}
	walk("", rawNoraConfig(t))
}

// ---- deploy/install-nora.sh --------------------------------------------
//
// The installer only ever runs as root on nora, so these drive it against a
// temp tree (DESTDIR) with systemctl, curl, tailscale and journalctl stubbed
// on PATH. Every stub appends to one ordered log, so the order of the calls is
// checkable as well as their arguments.

const (
	installScript = "../../deploy/install-nora.sh"
	// The version the deploy claims to have built. Not a plausible git
	// describe output, so a match can only come from this test.
	installVersion = "v9.9.9-test"
	// What the tailscale stub reports; the template's TAILNET becomes the
	// part before .ts.net.
	stubMagicDNS = "tail9abc.ts.net"
	stubTailnet  = "tail9abc"
)

type install struct {
	t     *testing.T
	dest  string // DESTDIR: the pretend filesystem root
	stage string // where `make deploy-nora` would have scp'd its three files
	bin   string // the stub directory, first on PATH
	calls string // one ordered log of every stub invocation
}

// stub writes an executable /bin/sh script that records its own name and
// arguments in the shared call log, then runs body.
func (in *install) stub(name, body string) {
	in.t.Helper()
	script := "#!/bin/sh\nprintf '%s %s\\n' " + shQuote(name) + " \"$*\" >> " + shQuote(in.calls) + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(in.bin, name), []byte(script), 0o755); err != nil { //nolint:gosec // a stub that must be executable
		in.t.Fatal(err)
	}
}

// shQuote single-quotes s for /bin/sh. Temp paths contain no quotes, but the
// script text is generated, so it is quoted rather than assumed safe.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func copyInto(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, mode); err != nil {
		t.Fatal(err)
	}
}

// newInstall stages the real unit and template beside a stand-in binary and
// installs the four stubs. curl carries the body the individual test needs.
func newInstall(t *testing.T, curl string) *install {
	t.Helper()
	root := t.TempDir()
	in := &install{
		t:     t,
		dest:  filepath.Join(root, "dest"),
		stage: filepath.Join(root, "stage"),
		bin:   filepath.Join(root, "bin"),
		calls: filepath.Join(root, "calls.log"),
	}
	for _, d := range []string{in.dest, in.stage, in.bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(in.stage, "adguard-reward.new"), []byte("#!/bin/sh\n# pretend binary\n"), 0o755); err != nil { //nolint:gosec // stands in for the deployed binary
		t.Fatal(err)
	}
	copyInto(t, unitPath, filepath.Join(in.stage, "adguard-reward.service"), 0o644)
	copyInto(t, noraConfigPath, filepath.Join(in.stage, "config.nora.yaml"), 0o644)

	in.stub("systemctl", "")
	in.stub("journalctl", "")
	in.stub("tailscale", `case "$1" in status) printf '{"MagicDNSSuffix": "`+stubMagicDNS+`"}\n' ;; esac`)
	in.stub("curl", curl)
	return in
}

// healthyCurl answers /healthz with the version that was deployed.
func healthyCurl(version string) string {
	return `printf '{"status":"ok","version":"` + version + `"}\n'`
}

func (in *install) path(parts ...string) string {
	return filepath.Join(append([]string{in.dest}, parts...)...)
}

func (in *install) confPath() string { return in.path("etc", "adguard-reward", "config.yaml") }
func (in *install) credPath() string { return in.path("etc", "adguard-reward", "adguard_password") }

// seedConfigured leaves the config and credential an operator has already
// filled in, so the run gets past the first-run guard.
func (in *install) seedConfigured(password string) {
	in.t.Helper()
	dir := filepath.Dir(in.confPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		in.t.Fatal(err)
	}
	b, err := os.ReadFile(noraConfigPath)
	if err != nil {
		in.t.Fatal(err)
	}
	s := strings.ReplaceAll(string(b), "TAILNET", stubTailnet)
	s = strings.ReplaceAll(s, "CHANGE-ME", "svc")
	if err := os.WriteFile(in.confPath(), []byte(s+"# operator marker\n"), 0o644); err != nil { //nolint:gosec // 0644 is what the installer ships
		in.t.Fatal(err)
	}
	if err := os.WriteFile(in.credPath(), []byte(password+"\n"), 0o600); err != nil {
		in.t.Fatal(err)
	}
}

// run feeds the script to `bash -s`, exactly as `make deploy-nora` does over
// ssh, and returns its exit code with both streams.
func (in *install) run(env ...string) (code int, stdout, stderr string) {
	in.t.Helper()
	f, err := os.Open(installScript)
	if err != nil {
		in.t.Fatal(err)
	}
	defer f.Close()

	var out, errb bytes.Buffer
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = f
	cmd.Stdout = &out
	cmd.Stderr = &errb
	cmd.Env = append(os.Environ(),
		"PATH="+in.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"VERSION="+installVersion,
		"STAGE="+in.stage,
		"DESTDIR="+in.dest,
		"HEALTH_TIMEOUT=5",
		"HEALTH_INTERVAL=0.1",
	)
	cmd.Env = append(cmd.Env, env...)

	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		in.t.Fatalf("running %s: %v", installScript, err)
	}
	return code, out.String(), errb.String()
}

// log is the ordered call log; missing means no stub ran.
func (in *install) log() string {
	b, err := os.ReadFile(in.calls)
	if err != nil {
		return ""
	}
	return string(b)
}

func (in *install) mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func sha256Of(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// TestInstallNora_FirstRun: a fresh host gets its files and is told what to
// fill in — and the service is not started, because starting it with a
// placeholder password would spray bad logins at AdGuard.
func TestInstallNora_FirstRun(t *testing.T) {
	in := newInstall(t, healthyCurl(installVersion))
	code, _, stderr := in.run()

	if code != 3 {
		t.Fatalf("first run exited %d, want 3\nstderr:\n%s", code, stderr)
	}
	if got := in.mode(t, in.confPath()); got != 0o644 {
		t.Errorf("config.yaml mode = %o, want 644", got)
	}
	conf, err := os.ReadFile(in.confPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conf), "TAILNET") {
		t.Error("config.yaml still contains TAILNET; the MagicDNS suffix was not substituted")
	}
	if !strings.Contains(string(conf), "nora."+stubTailnet+".ts.net") {
		t.Errorf("config.yaml base_url does not name the tailnet %q:\n%s", stubTailnet, conf)
	}

	if got := in.mode(t, in.credPath()); got != 0o600 {
		t.Errorf("credential mode = %o, want 600 (it will hold the AdGuard password)", got)
	}
	cred, err := os.ReadFile(in.credPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(cred)) != "CHANGE-ME" {
		t.Error("credential placeholder is not CHANGE-ME")
	}

	if got := in.mode(t, in.path("usr", "local", "bin", "adguard-reward")); got != 0o755 {
		t.Errorf("binary mode = %o, want 755", got)
	}
	if _, err := os.Stat(in.path("etc", "systemd", "system", "adguard-reward.service")); err != nil {
		t.Errorf("unit not installed: %v", err)
	}

	if strings.Contains(in.log(), "systemctl") {
		t.Errorf("first run touched the service:\n%s", in.log())
	}
}

// TestInstallNora_SecondRunPreservesEdits: a deploy replaces code, never the
// operator's config or their password.
func TestInstallNora_SecondRunPreservesEdits(t *testing.T) {
	in := newInstall(t, healthyCurl(installVersion))
	in.seedConfigured("real-password")
	confSum, credSum := sha256Of(t, in.confPath()), sha256Of(t, in.credPath())

	// Stale code from an earlier deploy.
	binPath := in.path("usr", "local", "bin", "adguard-reward")
	unitPathDest := in.path("etc", "systemd", "system", "adguard-reward.service")
	for _, p := range []string{binPath, unitPathDest} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("stale\n"), 0o644); err != nil { //nolint:gosec // replaced by the run under test
			t.Fatal(err)
		}
	}

	if code, _, stderr := in.run(); code != 0 {
		t.Fatalf("second run exited %d, want 0\nstderr:\n%s", code, stderr)
	}
	if sha256Of(t, in.confPath()) != confSum {
		t.Error("config.yaml was overwritten; the operator's edits are gone")
	}
	if sha256Of(t, in.credPath()) != credSum {
		t.Error("credential file was overwritten; the AdGuard password is gone")
	}
	for _, p := range []string{binPath, unitPathDest} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) == "stale\n" {
			t.Errorf("%s was not replaced", p)
		}
	}
}

// TestInstallNora_HealthGate: the deploy has not succeeded until the running
// service reports the version that was just built. A restart that silently
// kept the old binary looks identical from the outside.
func TestInstallNora_HealthGate(t *testing.T) {
	t.Run("version mismatch", func(t *testing.T) {
		in := newInstall(t, `printf '{"status":"ok","version":"other"}\n'`)
		in.seedConfigured("real-password")

		code, _, stderr := in.run()
		if code != 4 {
			t.Fatalf("mismatch exited %d, want 4\nstderr:\n%s", code, stderr)
		}
		for _, want := range []string{"other", installVersion} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr does not name %q:\n%s", want, stderr)
			}
		}

		// The gate is only meaningful after the new unit is actually running.
		log := in.log()
		reload := strings.Index(log, "systemctl daemon-reload")
		restart := strings.Index(log, "systemctl restart")
		curl := strings.Index(log, "curl")
		if reload < 0 || restart < 0 || curl < 0 {
			t.Fatalf("call log is missing daemon-reload, restart or curl:\n%s", log)
		}
		if !(reload < restart && restart < curl) {
			t.Errorf("want daemon-reload, then restart, then curl:\n%s", log)
		}
	})

	t.Run("version matches", func(t *testing.T) {
		in := newInstall(t, healthyCurl(installVersion))
		in.seedConfigured("real-password")
		if code, _, stderr := in.run(); code != 0 {
			t.Fatalf("matching version exited %d, want 0\nstderr:\n%s", code, stderr)
		}
	})
}

// TestInstallNora_PollsUntilUp: the service takes a moment to open its
// listener, so a one-shot check would fail every healthy deploy.
func TestInstallNora_PollsUntilUp(t *testing.T) {
	root := t.TempDir()
	counter := filepath.Join(root, "n")
	in := newInstall(t, `n=$(cat `+shQuote(counter)+` 2>/dev/null || echo 0)
n=$((n+1)); printf '%s' "$n" > `+shQuote(counter)+`
[ "$n" -le 2 ] && exit 22
`+healthyCurl(installVersion))
	in.seedConfigured("real-password")

	code, _, stderr := in.run("HEALTH_TIMEOUT=2", "HEALTH_INTERVAL=0.2")
	if code != 0 {
		t.Fatalf("exited %d, want 0 — a slow start must be waited out\nstderr:\n%s", code, stderr)
	}
	if n := strings.Count(in.log(), "curl"); n < 3 {
		t.Errorf("curl was called %d times, want at least 3 (two refusals then success)", n)
	}
}

// TestInstallNora_Unhealthy: a deploy that never came up must fail loudly and
// leave the logs that explain why.
func TestInstallNora_Unhealthy(t *testing.T) {
	in := newInstall(t, "exit 22")
	in.seedConfigured("real-password")

	code, _, stderr := in.run("HEALTH_TIMEOUT=1", "HEALTH_INTERVAL=0.1")
	if code == 0 {
		t.Fatalf("a service that never answered exited 0\nstderr:\n%s", stderr)
	}
	if !strings.Contains(in.log(), "journalctl -u adguard-reward") {
		t.Errorf("no journalctl -u adguard-reward in the call log; the failure is unexplained:\n%s", in.log())
	}
}

// TestInstallNora_NoSecretOutput enforces the secret-handling rule the hard
// way: the credential's value must not appear in any log or on either stream.
// The check is a boolean — the sentinel is never printed, even on failure.
func TestInstallNora_NoSecretOutput(t *testing.T) {
	const sentinel = "pw-INSTALL-CANARY-4e21"
	in := newInstall(t, healthyCurl(installVersion))
	in.seedConfigured(sentinel)

	code, stdout, stderr := in.run()
	if code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	for name, s := range map[string]string{"stdout": stdout, "stderr": stderr, "call log": in.log()} {
		if strings.Contains(s, sentinel) {
			t.Errorf("the credential's value reached %s", name)
		}
	}
}

// TestInstallNora_TailscaleServe: serve must target the literal loopback
// address the config trusts for X-Forwarded-For. "localhost" can resolve to
// ::1, which is not in trusted_proxies, and the login limiter would then see
// the proxy instead of the phone.
func TestInstallNora_TailscaleServe(t *testing.T) {
	in := newInstall(t, healthyCurl(installVersion))
	in.seedConfigured("real-password")
	if code, _, stderr := in.run(); code != 0 {
		t.Fatalf("exited %d, want 0\nstderr:\n%s", code, stderr)
	}

	var serve string
	for _, line := range strings.Split(in.log(), "\n") {
		if strings.HasPrefix(line, "tailscale serve") {
			serve = line
		}
	}
	if serve == "" {
		t.Fatalf("tailscale serve was never called:\n%s", in.log())
	}
	for _, want := range []string{"--bg", "--https=443", "http://127.0.0.1:8080"} {
		if !strings.Contains(serve, want) {
			t.Errorf("tailscale serve line %q lacks %q", serve, want)
		}
	}
}
