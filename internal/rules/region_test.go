package rules

import (
	"errors"
	"reflect"
	"testing"
)

func cat(parts ...[]string) []string {
	out := []string{}
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// awkward holds lines a careless splice would trim, dedupe, sort or mistake
// for a marker.
var awkward = []string{
	"",
	"||ads.example^",
	"||ads.example^",
	"@@||ok.example^   ",
	"! a comment",
	"# hash comment",
	"",
	"||ünïcödé.example^ ! ☃",
	"! adguard-reward begin (old copy, not a marker)",
	"  ! adguard-reward begin",
	"! adguard-reward end ",
	"zzz",
	"aaa",
}

func TestSplice_PreservesOutside(t *testing.T) {
	managed := []string{"||new.example^$client='kid'", "@@||new.example^$client='kid'"}
	cases := []struct {
		name           string
		prefix, suffix []string
		old            []string
	}{
		{"region in middle", awkward[:6], awkward[6:], []string{"||stale^"}},
		{"region first", nil, awkward, []string{"||stale^", "||stale2^"}},
		{"region last", awkward, nil, nil},
		{"empty region", awkward[:3], awkward[3:], []string{}},
		{"blank-only outside", []string{"", ""}, []string{"", ""}, []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := cat(tc.prefix, []string{BeginMarker}, tc.old, []string{EndMarker}, tc.suffix)
			got, err := Splice(current, managed)
			if err != nil {
				t.Fatal(err)
			}
			want := cat(tc.prefix, []string{BeginMarker}, managed, []string{EndMarker}, tc.suffix)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got  %q\nwant %q", got, want)
			}
			np := len(tc.prefix)
			if !reflect.DeepEqual(got[:np], cat(tc.prefix)) {
				t.Errorf("prefix changed: %q", got[:np])
			}
			if !reflect.DeepEqual(got[len(got)-len(tc.suffix):], cat(tc.suffix)) {
				t.Errorf("suffix changed: %q", got[len(got)-len(tc.suffix):])
			}
		})
	}
}

func TestSplice_DoesNotAliasInput(t *testing.T) {
	current := []string{"a", BeginMarker, "old", EndMarker, "b"}
	keep := append([]string(nil), current...)
	got, err := Splice(current, []string{"x", "y", "z"})
	if err != nil {
		t.Fatal(err)
	}
	got[0] = "mutated"
	if !reflect.DeepEqual(current, keep) {
		t.Fatalf("input modified: %q", current)
	}
}

func TestSplice_AppendsWhenAbsent(t *testing.T) {
	managed := []string{"||a.com^$client='kid'"}
	for name, current := range map[string][]string{
		"awkward":        awkward,
		"comment first":  {"! header", "||x^"},
		"empty":          {},
		"nil":            nil,
		"trailing blank": {"||x^", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Splice(current, managed)
			if err != nil {
				t.Fatal(err)
			}
			want := cat(current, []string{BeginMarker}, managed, []string{EndMarker})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got  %q\nwant %q", got, want)
			}
		})
	}
	t.Run("empty managed", func(t *testing.T) {
		got, err := Splice([]string{"||x^"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"||x^", BeginMarker, EndMarker}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q want %q", got, want)
		}
	})
}

func TestSplice_Malformed(t *testing.T) {
	for name, current := range map[string][]string{
		"lone begin":       {"||x^", BeginMarker, "||y^"},
		"lone end":         {"||x^", EndMarker},
		"end before begin": {EndMarker, "||x^", BeginMarker},
		"two pairs":        {BeginMarker, "a", EndMarker, "b", BeginMarker, "c", EndMarker},
		"two begins":       {BeginMarker, BeginMarker, EndMarker},
		"two ends":         {BeginMarker, EndMarker, EndMarker},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Splice(current, []string{"||m^"})
			if !errors.Is(err, ErrMalformedRegion) {
				t.Fatalf("err = %v, want ErrMalformedRegion", err)
			}
			if got != nil {
				t.Fatalf("got %q, want nil", got)
			}
		})
	}
	if ErrMalformedRegion.Error() != "managed rules region is malformed" {
		t.Errorf("message = %q", ErrMalformedRegion.Error())
	}
}

func TestSplice_RejectsMarkerInManaged(t *testing.T) {
	for _, m := range []string{BeginMarker, EndMarker} {
		got, err := Splice([]string{"||x^"}, []string{"||a^", m})
		if err == nil || got != nil {
			t.Fatalf("managed %q: got %q, %v; want nil, error", m, got, err)
		}
	}
}

func TestSplice_Idempotent(t *testing.T) {
	managed := []string{"||a.com^$client='kid'", "@@||a.com^$client='kid'"}
	inputs := [][]string{
		awkward,
		nil,
		cat(awkward[:4], []string{BeginMarker, "||stale^", EndMarker}, awkward[4:]),
	}
	for i, x := range inputs {
		once, err := Splice(x, managed)
		if err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		twice, err := Splice(once, managed)
		if err != nil {
			t.Fatalf("input %d second: %v", i, err)
		}
		if !reflect.DeepEqual(once, twice) {
			t.Fatalf("input %d: not idempotent\nonce  %q\ntwice %q", i, once, twice)
		}
	}
}
