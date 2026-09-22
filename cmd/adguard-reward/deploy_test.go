package main

// Tests over the shipped deployment artefacts: the systemd unit and the nora
// config template. They are data, not code, so nothing else would notice a
// typo in them until the service failed to start on the host.

import (
	"net/http"
	"net/netip"
	"os"
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
