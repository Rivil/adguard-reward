package rules

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeDomain(t *testing.T) {
	rejects := []string{
		"https://a.com",
		"a.com/x",
		"a.com:443",
		"*.a.com",
		"a .com",
		" a.com",
		"a.com ",
		"a.com\n",
		"a.com\r",
		"a.com\t",
		"||a.com^",
		"a.com^",
		"a.com$important",
		`/a\.com/`,
		"@@a.com",
		"a.com|",
		"a.com,b.com",
		"com",
		"",
		".",
		"a.com..",
		".a.com",
		"a..com",
		"ä.com",
		"-a.com",
		"a-.com",
		"a_b.com",
		strings.Repeat("a", 64) + ".com",
		strings.Repeat("abcdefghi.", 25) + "abcd", // 254 chars
	}
	for _, d := range rejects {
		t.Run(d, func(t *testing.T) {
			got, err := NormalizeDomain(d)
			if err == nil {
				t.Fatalf("NormalizeDomain(%q) = %q, want error", d, got)
			}
			if !errors.Is(err, ErrInvalidDomain) {
				t.Errorf("err %v does not wrap ErrInvalidDomain", err)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", d)) {
				t.Errorf("err %q does not name the domain", err)
			}
		})
	}

	accepts := map[string]string{
		"Example.COM.":                           "example.com",
		"a.com":                                  "a.com",
		"sub.a-b.example.co.uk":                  "sub.a-b.example.co.uk",
		"xn--bcher-kva.example":                  "xn--bcher-kva.example",
		"1.2.example":                            "1.2.example",
		strings.Repeat("a", 63) + ".com":         strings.Repeat("a", 63) + ".com",
		strings.Repeat("abcdefghi.", 25) + "abc": strings.Repeat("abcdefghi.", 25) + "abc", // 253 chars
	}
	for in, want := range accepts {
		got, err := NormalizeDomain(in)
		if err != nil || got != want {
			t.Errorf("NormalizeDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
