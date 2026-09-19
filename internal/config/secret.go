package config

import (
	"fmt"
	"log/slog"
)

// redacted is what every rendering of a Secret produces.
const redacted = "[redacted]"

// Secret is a string that refuses to print itself. fmt verbs, JSON, text
// marshalling and slog all render it as "[redacted]"; Reveal is the only way
// to get the value out, which keeps the real string off every log path.
type Secret string

// Reveal returns the underlying value. Callers should pass the result straight
// to the consumer (an Authorization header, a provider client) and never
// format or log it.
func (s Secret) Reveal() string { return string(s) }

// IsSet reports whether the secret is non-empty without exposing it.
func (s Secret) IsSet() bool { return s != "" }

func (Secret) String() string   { return redacted }
func (Secret) GoString() string { return redacted }

// Format implements fmt.Formatter so %v, %+v, %#v, %s and %q all redact.
func (Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }

func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }
