package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// env builds a lookupEnv over a fixed map so tests never touch the process env.
func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func noEnv(string) (string, bool) { return "", false }

// requiredEnv is the smallest env that passes validation on its own.
var requiredEnv = map[string]string{
	"ADGUARD_REWARD_ADGUARD_URL":      "http://127.0.0.1:3000",
	"ADGUARD_REWARD_ADGUARD_USERNAME": "svc",
	"ADGUARD_REWARD_ADGUARD_PASSWORD": "envpw",
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalYAML = `adguard:
  url: "http://127.0.0.1:3000"
  username: "svc"
  password: "yamlpw"
`

func TestLoad_Full(t *testing.T) {
	cfg, err := Load("testdata/full.yaml", true, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := &Config{
		Listen:   "127.0.0.1:9090",
		BaseURL:  "https://reward.example",
		TLS:      TLS{Cert: "/etc/ssl/reward.crt", Key: "/etc/ssl/reward.key"},
		AdGuard:  AdGuard{URL: "http://127.0.0.1:3000", Username: "svc", Password: "yamlpw"},
		DataDir:  "/var/lib/reward",
		AI:       AI{Provider: "anthropic", APIKey: "sk-yaml", BaseURL: "https://api.example", Model: "claude-sonnet-5"},
		LogLevel: "debug",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("Load(full.yaml) mismatch\n got: %#v\nwant: %#v", cfg, want)
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(requiredEnv))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8080" || cfg.DataDir != "./data" || cfg.LogLevel != "info" {
		t.Fatalf("defaults not applied: listen=%q data_dir=%q log_level=%q", cfg.Listen, cfg.DataDir, cfg.LogLevel)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	for _, key := range []string{"adguard.url", "adguard.username", "adguard.password"} {
		t.Run(key, func(t *testing.T) {
			leaf := strings.TrimPrefix(key, "adguard.")
			var b strings.Builder
			b.WriteString("adguard:\n")
			for _, line := range strings.Split(strings.TrimSpace(minimalYAML), "\n")[1:] {
				if !strings.Contains(line, leaf+":") {
					b.WriteString(line + "\n")
				}
			}
			p := writeFile(t, t.TempDir(), "config.yaml", b.String())
			_, err := Load(p, true, noEnv)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("missing %s: err = %v, want error naming %q", key, err, key)
			}
		})
	}
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	p := writeFile(t, t.TempDir(), "config.yaml", minimalYAML)

	cfg, err := Load(p, true, env(map[string]string{"ADGUARD_REWARD_ADGUARD_PASSWORD": "envpw"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.AdGuard.Password.Reveal(); got != "envpw" {
		t.Fatalf("env override: password = %q, want envpw", got)
	}

	// A set-but-empty variable overrides to empty and must not fall through.
	_, err = Load(p, true, env(map[string]string{"ADGUARD_REWARD_ADGUARD_PASSWORD": ""}))
	if err == nil || !strings.Contains(err.Error(), "adguard.password") {
		t.Fatalf("blank env: err = %v, want error naming adguard.password", err)
	}
}

func TestEnvTable(t *testing.T) {
	want := map[string]string{
		"listen":                "ADGUARD_REWARD_LISTEN",
		"base_url":              "ADGUARD_REWARD_BASE_URL",
		"tls.cert":              "ADGUARD_REWARD_TLS_CERT",
		"tls.key":               "ADGUARD_REWARD_TLS_KEY",
		"adguard.url":           "ADGUARD_REWARD_ADGUARD_URL",
		"adguard.username":      "ADGUARD_REWARD_ADGUARD_USERNAME",
		"adguard.password":      "ADGUARD_REWARD_ADGUARD_PASSWORD",
		"adguard.password_file": "ADGUARD_REWARD_ADGUARD_PASSWORD_FILE",
		"data_dir":              "ADGUARD_REWARD_DATA_DIR",
		"ai.provider":           "ADGUARD_REWARD_AI_PROVIDER",
		"ai.api_key":            "ADGUARD_REWARD_AI_API_KEY",
		"ai.api_key_file":       "ADGUARD_REWARD_AI_API_KEY_FILE",
		"ai.base_url":           "ADGUARD_REWARD_AI_BASE_URL",
		"ai.model":              "ADGUARD_REWARD_AI_MODEL",
		"log_level":             "ADGUARD_REWARD_LOG_LEVEL",
	}
	got := map[string]string{}
	for _, e := range envTable {
		if _, dup := got[e.key]; dup {
			t.Errorf("envTable: duplicate row for %s", e.key)
		}
		got[e.key] = e.env
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("envTable names differ\n got: %v\nwant: %v", got, want)
	}

	// Every yaml-tagged leaf of Config must have a row, and every row must
	// name a real leaf — a renamed or added key fails here.
	leaves := map[string]bool{}
	walkLeaves(reflect.TypeOf(Config{}), "", leaves)
	for leaf := range leaves {
		if _, ok := got[leaf]; !ok {
			t.Errorf("config leaf %q has no envTable row", leaf)
		}
	}
	for key := range got {
		if !leaves[key] {
			t.Errorf("envTable row %q names no config leaf", key)
		}
	}

	// Each setter actually writes its leaf: set every var to its own env
	// name and read it back through the table's key.
	all := map[string]string{}
	for _, e := range envTable {
		all[e.env] = "v:" + e.key
	}
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(mergeMaps(all, map[string]string{
		"ADGUARD_REWARD_ADGUARD_URL":           "http://h",
		"ADGUARD_REWARD_ADGUARD_PASSWORD_FILE": "",
		"ADGUARD_REWARD_AI_API_KEY_FILE":       "",
		"ADGUARD_REWARD_LOG_LEVEL":             "warn",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "v:listen" || cfg.TLS.Key != "v:tls.key" || cfg.AI.Model != "v:ai.model" ||
		cfg.AI.APIKey.Reveal() != "v:ai.api_key" || cfg.AdGuard.Username != "v:adguard.username" {
		t.Errorf("setters did not land: %#v", cfg)
	}
}

func walkLeaves(t reflect.Type, prefix string, out map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if prefix != "" {
			name = prefix + "." + name
		}
		if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeOf(Secret("")) {
			walkLeaves(f.Type, name, out)
			continue
		}
		out[name] = true
	}
}

func mergeMaps(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestLoad_SecretFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeFile(t, dir, "config.yaml", "adguard:\n  url: \"http://h\"\n  username: \"svc\"\n")

	t.Run("password_file via env", func(t *testing.T) {
		pf := writeFile(t, dir, "pw.txt", "s3cret\n")
		cfg, err := Load(cfgPath, true, env(map[string]string{"ADGUARD_REWARD_ADGUARD_PASSWORD_FILE": pf}))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.AdGuard.Password.Reveal(); got != "s3cret" {
			t.Fatalf("password = %q, want s3cret (trailing newline trimmed)", got)
		}
	})

	t.Run("inner whitespace kept", func(t *testing.T) {
		pf := writeFile(t, dir, "pw-space.txt", " a b \r\n")
		cfg, err := Load(cfgPath, true, env(map[string]string{"ADGUARD_REWARD_ADGUARD_PASSWORD_FILE": pf}))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.AdGuard.Password.Reveal(); got != " a b " {
			t.Fatalf("password = %q, want %q", got, " a b ")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		pf := writeFile(t, dir, "empty.txt", "\n")
		_, err := Load(cfgPath, true, env(map[string]string{"ADGUARD_REWARD_ADGUARD_PASSWORD_FILE": pf}))
		if err == nil || !strings.Contains(err.Error(), "adguard.password_file") {
			t.Fatalf("empty file: err = %v, want error naming adguard.password_file", err)
		}
	})

	t.Run("unreadable file", func(t *testing.T) {
		_, err := Load(cfgPath, true, env(map[string]string{"ADGUARD_REWARD_ADGUARD_PASSWORD_FILE": filepath.Join(dir, "nope.txt")}))
		if err == nil || !strings.Contains(err.Error(), "adguard.password_file") {
			t.Fatalf("unreadable file: err = %v, want error naming adguard.password_file", err)
		}
	})

	t.Run("api_key_file identical", func(t *testing.T) {
		kf := writeFile(t, dir, "key.txt", "sk-file\n")
		cfg, err := Load(cfgPath, true, env(mergeMaps(requiredEnv, map[string]string{"ADGUARD_REWARD_AI_API_KEY_FILE": kf})))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.AI.APIKey.Reveal(); got != "sk-file" {
			t.Fatalf("api_key = %q, want sk-file", got)
		}
		_, err = Load(cfgPath, true, env(mergeMaps(requiredEnv, map[string]string{"ADGUARD_REWARD_AI_API_KEY_FILE": filepath.Join(dir, "nope.txt")})))
		if err == nil || !strings.Contains(err.Error(), "ai.api_key_file") {
			t.Fatalf("missing api_key_file: err = %v, want error naming ai.api_key_file", err)
		}
	})

	t.Run("relative to config dir", func(t *testing.T) {
		rel := t.TempDir() // CWD is the package dir, not this
		writeFile(t, rel, "secret.txt", "from-config-dir\n")
		p := writeFile(t, rel, "config.yaml", "adguard:\n  url: \"http://h\"\n  username: \"svc\"\n  password_file: secret.txt\n")
		cfg, err := Load(p, true, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.AdGuard.Password.Reveal(); got != "from-config-dir" {
			t.Fatalf("password = %q, want from-config-dir", got)
		}
	})
}

func TestLoad_PasswordAndFileConflict(t *testing.T) {
	dir := t.TempDir()
	pf := writeFile(t, dir, "pw.txt", "x\n")
	p := writeFile(t, dir, "config.yaml", minimalYAML)
	_, err := Load(p, true, env(map[string]string{"ADGUARD_REWARD_ADGUARD_PASSWORD_FILE": pf}))
	if err == nil {
		t.Fatal("both set: want error")
	}
	for _, key := range []string{"adguard.password", "adguard.password_file"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("both set: err %q does not name %s", err, key)
		}
	}
}

func TestLoad_UnknownKey(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "typo.yaml", "adguard:\n  url: \"http://h\"\n  username: \"svc\"\n  passwrod: \"x\"\n")
	_, err := Load(p, true, noEnv)
	if err == nil || !strings.Contains(err.Error(), "passwrod") {
		t.Fatalf("unknown key: err = %v, want error mentioning passwrod", err)
	}

	p = writeFile(t, dir, "broken.yaml", "adguard:\n  url: [\n")
	_, err = Load(p, true, noEnv)
	if err == nil || !strings.Contains(err.Error(), "line ") {
		t.Fatalf("syntax error: err = %v, want error with a line number", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "config.yaml")

	_, err := Load(missing, true, env(requiredEnv))
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("explicit missing: err = %v, want error containing %q", err, missing)
	}

	cfg, err := Load(missing, false, env(requiredEnv))
	if err != nil {
		t.Fatalf("default missing with env: err = %v, want env-only load", err)
	}
	if cfg.AdGuard.Username != "svc" {
		t.Fatalf("env-only load: username = %q", cfg.AdGuard.Username)
	}
}

func TestLoad_BadURL(t *testing.T) {
	for _, raw := range []string{"://bad", "127.0.0.1:80", "ftp://h", "http://u:p@h"} {
		_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(mergeMaps(requiredEnv,
			map[string]string{"ADGUARD_REWARD_ADGUARD_URL": raw})))
		if err == nil || !strings.Contains(err.Error(), "adguard.url") {
			t.Errorf("url %q: err = %v, want error naming adguard.url", raw, err)
		}
	}
}

func TestSecret_Redacted(t *testing.T) {
	const pw, key = "pw-CANARY-9f3a", "sk-CANARY-77b1"
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(mergeMaps(requiredEnv, map[string]string{
		"ADGUARD_REWARD_ADGUARD_PASSWORD": pw,
		"ADGUARD_REWARD_AI_API_KEY":       key,
	})))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdGuard.Password.Reveal() != pw || cfg.AI.APIKey.Reveal() != key {
		t.Fatal("Reveal() must return the original")
	}

	outputs := map[string]string{}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		outputs["cfg "+verb] = fmt.Sprintf(verb, cfg)
		outputs["*cfg "+verb] = fmt.Sprintf(verb, *cfg)
		outputs["secret "+verb] = fmt.Sprintf(verb, cfg.AdGuard.Password)
	}
	outputs["Sprint"] = fmt.Sprint(cfg.AdGuard.Password)
	j, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	outputs["json"] = string(j)

	var text, jsonl bytes.Buffer
	slog.New(slog.NewTextHandler(&text, &slog.HandlerOptions{Level: slog.LevelDebug})).
		Debug("cfg", "cfg", cfg, "pw", cfg.AdGuard.Password)
	slog.New(slog.NewJSONHandler(&jsonl, &slog.HandlerOptions{Level: slog.LevelDebug})).
		Debug("cfg", "cfg", cfg, "pw", cfg.AdGuard.Password)
	outputs["slog text"] = text.String()
	outputs["slog json"] = jsonl.String()

	for name, out := range outputs {
		if strings.Contains(out, pw) || strings.Contains(out, key) {
			t.Errorf("%s leaks a secret: %s", name, out)
		}
		if !strings.Contains(out, redacted) {
			t.Errorf("%s lacks %q (positive control): %s", name, redacted, out)
		}
	}
}

func TestSlogLevel(t *testing.T) {
	base := filepath.Join(t.TempDir(), "absent.yaml")
	cfg, err := Load(base, false, env(mergeMaps(requiredEnv, map[string]string{"ADGUARD_REWARD_LOG_LEVEL": "debug"})))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SlogLevel() != slog.LevelDebug {
		t.Fatalf("SlogLevel() = %v, want debug", cfg.SlogLevel())
	}
	_, err = Load(base, false, env(mergeMaps(requiredEnv, map[string]string{"ADGUARD_REWARD_LOG_LEVEL": "bogus"})))
	if err == nil || !strings.Contains(err.Error(), "log_level") {
		t.Fatalf("bogus level: err = %v, want error naming log_level", err)
	}
}
