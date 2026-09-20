// Package config loads the adguard-reward configuration from a YAML file and
// ADGUARD_REWARD_* environment variables.
//
// Precedence is defaults < YAML < env. Every leaf key has exactly one env
// name, listed in envTable; a set-but-empty variable overrides to empty rather
// than falling through to the file, so an unset compose substitution fails
// validation instead of silently using whatever the YAML says.
package config

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvPrefix is the prefix every environment override carries.
const EnvPrefix = "ADGUARD_REWARD_"

// Config is the fully merged configuration. Field names are exported so fmt
// and encoding/json reach the nested Secret values and redact them.
type Config struct {
	Listen  string `yaml:"listen"`
	BaseURL string `yaml:"base_url"`
	TLS     TLS    `yaml:"tls"`
	// TrustedProxies lists CIDRs (a bare IP means /32 or /128) whose
	// X-Forwarded-For is believed. Empty means the TCP peer is the client and
	// the header is ignored, so nothing can spoof the per-IP login limit.
	TrustedProxies []string `yaml:"trusted_proxies"`
	AdGuard        AdGuard  `yaml:"adguard"`
	DataDir        string   `yaml:"data_dir"`
	AI             AI       `yaml:"ai"`
	LogLevel       string   `yaml:"log_level"`
}

// TLS holds the listener certificate pair. Parsed now; the listener is wired
// in a later phase.
type TLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

// AdGuard is the service credential used for every /control/* call.
type AdGuard struct {
	URL          string `yaml:"url"`
	Username     string `yaml:"username"`
	Password     Secret `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
}

// AI is the optional bring-your-own-key provider configuration.
type AI struct {
	Provider   string `yaml:"provider"`
	APIKey     Secret `yaml:"api_key"`
	APIKeyFile string `yaml:"api_key_file"`
	BaseURL    string `yaml:"base_url"`
	Model      string `yaml:"model"`
}

// envEntry binds one dotted config key to its environment name and setter.
type envEntry struct {
	key string // dotted yaml path, e.g. "adguard.password_file"
	env string // full variable name, e.g. ADGUARD_REWARD_ADGUARD_PASSWORD_FILE
	set func(c *Config, v string)
}

// envTable is the single source of env names. TestEnvTable walks the Config
// struct by yaml tag and fails if a leaf is missing here, so a new key cannot
// ship without an env override.
var envTable = []envEntry{
	{"listen", EnvPrefix + "LISTEN", func(c *Config, v string) { c.Listen = v }},
	{"base_url", EnvPrefix + "BASE_URL", func(c *Config, v string) { c.BaseURL = v }},
	{"tls.cert", EnvPrefix + "TLS_CERT", func(c *Config, v string) { c.TLS.Cert = v }},
	{"tls.key", EnvPrefix + "TLS_KEY", func(c *Config, v string) { c.TLS.Key = v }},
	{"trusted_proxies", EnvPrefix + "TRUSTED_PROXIES", func(c *Config, v string) { c.TrustedProxies = splitList(v) }},
	{"adguard.url", EnvPrefix + "ADGUARD_URL", func(c *Config, v string) { c.AdGuard.URL = v }},
	{"adguard.username", EnvPrefix + "ADGUARD_USERNAME", func(c *Config, v string) { c.AdGuard.Username = v }},
	{"adguard.password", EnvPrefix + "ADGUARD_PASSWORD", func(c *Config, v string) { c.AdGuard.Password = Secret(v) }},
	{"adguard.password_file", EnvPrefix + "ADGUARD_PASSWORD_FILE", func(c *Config, v string) { c.AdGuard.PasswordFile = v }},
	{"data_dir", EnvPrefix + "DATA_DIR", func(c *Config, v string) { c.DataDir = v }},
	{"ai.provider", EnvPrefix + "AI_PROVIDER", func(c *Config, v string) { c.AI.Provider = v }},
	{"ai.api_key", EnvPrefix + "AI_API_KEY", func(c *Config, v string) { c.AI.APIKey = Secret(v) }},
	{"ai.api_key_file", EnvPrefix + "AI_API_KEY_FILE", func(c *Config, v string) { c.AI.APIKeyFile = v }},
	{"ai.base_url", EnvPrefix + "AI_BASE_URL", func(c *Config, v string) { c.AI.BaseURL = v }},
	{"ai.model", EnvPrefix + "AI_MODEL", func(c *Config, v string) { c.AI.Model = v }},
	{"log_level", EnvPrefix + "LOG_LEVEL", func(c *Config, v string) { c.LogLevel = v }},
}

// Defaults returns the configuration before any file or env is applied.
func Defaults() *Config {
	return &Config{
		Listen:   ":8080",
		DataDir:  "./data",
		LogLevel: "info",
	}
}

// Load reads the YAML at path (if any), applies env overrides via lookupEnv,
// resolves *_file indirections and validates the result.
//
// explicit reports whether the user passed the path themselves: a missing
// explicit file is an error, a missing default file means env-only.
// lookupEnv is normally os.LookupEnv; it is a parameter so tests can inject
// an isolated environment.
func Load(path string, explicit bool, lookupEnv func(string) (string, bool)) (*Config, error) {
	cfg := Defaults()

	baseDir := "" // *_file paths resolve against this; "" means CWD
	f, err := os.Open(path)
	switch {
	case err == nil:
		baseDir = filepath.Dir(path)
		decodeErr := decodeYAML(f, cfg)
		_ = f.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("config: %s: %w", path, decodeErr)
		}
	case errors.Is(err, os.ErrNotExist) && !explicit:
		// No default file: run from env alone.
	default:
		return nil, fmt.Errorf("config: open %s: %w", path, err)
	}

	for _, e := range envTable {
		if v, ok := lookupEnv(e.env); ok {
			e.set(cfg, v)
		}
	}

	if err := resolveSecretFile(baseDir, "adguard.password", "adguard.password_file",
		cfg.AdGuard.PasswordFile, &cfg.AdGuard.Password); err != nil {
		return nil, err
	}
	if err := resolveSecretFile(baseDir, "ai.api_key", "ai.api_key_file",
		cfg.AI.APIKeyFile, &cfg.AI.APIKey); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// splitList parses a comma-separated env value: entries are trimmed and
// empties dropped, so a set-but-empty variable yields an empty list that
// overrides the file.
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func decodeYAML(r io.Reader, cfg *Config) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// resolveSecretFile fills *dst from the file named by fileVal, if set. The
// inline value and the file are mutually exclusive. Only a trailing newline is
// trimmed so a password with leading or trailing spaces survives.
func resolveSecretFile(baseDir, key, fileKey, fileVal string, dst *Secret) error {
	if fileVal == "" {
		return nil
	}
	if dst.IsSet() {
		return fmt.Errorf("config: %s and %s are both set; use one", key, fileKey)
	}
	p := fileVal
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("config: %s: %w", fileKey, err)
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return fmt.Errorf("config: %s: %s is empty", fileKey, fileVal)
	}
	*dst = Secret(v)
	return nil
}

func (c *Config) validate() error {
	var missing []string
	if c.AdGuard.URL == "" {
		missing = append(missing, "adguard.url")
	}
	if c.AdGuard.Username == "" {
		missing = append(missing, "adguard.username")
	}
	if !c.AdGuard.Password.IsSet() {
		missing = append(missing, "adguard.password")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required %s", strings.Join(missing, ", "))
	}
	if err := validateURL(c.AdGuard.URL); err != nil {
		return fmt.Errorf("config: adguard.url: %w", err)
	}
	if (c.TLS.Cert == "") != (c.TLS.Key == "") {
		return errors.New("config: tls.cert and tls.key must both be set or both empty")
	}
	for _, e := range c.TrustedProxies {
		if _, err := parseCIDR(e); err != nil {
			return fmt.Errorf("config: trusted_proxies: %q: %w", e, err)
		}
	}
	if _, err := parseLevel(c.LogLevel); err != nil {
		return fmt.Errorf("config: log_level: %w", err)
	}
	return nil
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("missing host")
	}
	if u.User != nil {
		return errors.New("must not embed credentials; use adguard.username / adguard.password")
	}
	return nil
}

// parseCIDR accepts a CIDR or a bare IP (taken as a /32 or /128).
func parseCIDR(s string) (*net.IPNet, error) {
	if _, n, err := net.ParseCIDR(s); err == nil {
		return n, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, errors.New("not an IP or CIDR")
	}
	bits := 128
	if v4 := ip.To4(); v4 != nil {
		ip, bits = v4, 32
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

// TrustedProxyNets returns trusted_proxies parsed. Load validated every
// entry, so on a loaded Config nothing is skipped; a hand-built Config drops
// entries that do not parse.
func (c *Config) TrustedProxyNets() []*net.IPNet {
	var nets []*net.IPNet
	for _, e := range c.TrustedProxies {
		if n, err := parseCIDR(e); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown level %q (debug, info, warn, error)", s)
}

// SlogLevel maps log_level to a slog.Level. The value was validated by Load,
// so an unknown level (only possible on a hand-built Config) falls back to
// info.
func (c *Config) SlogLevel() slog.Level {
	l, err := parseLevel(c.LogLevel)
	if err != nil {
		return slog.LevelInfo
	}
	return l
}
