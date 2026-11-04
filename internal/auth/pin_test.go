package auth

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestValidatePIN(t *testing.T) {
	reject := []string{
		"١٢٣٤",    // Arabic-Indic digits: unicode.IsDigit says yes
		"１２３４",    // fullwidth digits
		"١٢٣",     // 3 Arabic-Indic digits = 6 bytes: in byte-length range
		"１２",      // 2 fullwidth digits = 6 bytes: in byte-length range
		"12٣",     // mixed, 4 bytes
		" 1234",   // leading space
		"1234\n",  // trailing newline
		"1234 ",   // trailing space
		"123",     // too short
		"1234567", // too long
		"12a4",
		"",
		"-123",
		"+1234",
		"12.4",
	}
	for _, pin := range reject {
		if err := ValidatePIN(pin); !errors.Is(err, ErrBadPIN) {
			t.Errorf("ValidatePIN(%q) = %v, want ErrBadPIN", pin, err)
		}
	}
	accept := []string{"0000", "000000", "0123", "98765", "583920", "9999"}
	for _, pin := range accept {
		if err := ValidatePIN(pin); err != nil {
			t.Errorf("ValidatePIN(%q) = %v, want nil", pin, err)
		}
	}
}

var phcRE = regexp.MustCompile(`^\$argon2id\$v=19\$m=\d+,t=\d+,p=\d+\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$`)

func TestHashPIN_Format(t *testing.T) {
	const pin = "583920"
	a, err := HashPIN(pin)
	if err != nil {
		t.Fatal(err)
	}
	if !phcRE.MatchString(a) {
		t.Fatalf("HashPIN = %q, not Argon2id PHC", a)
	}
	if strings.Contains(a, pin) {
		t.Fatalf("hash %q contains the PIN", a)
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash %q does not use the default cost", a)
	}
	b, err := HashPIN(pin)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two hashes of one PIN are identical (%q): unsalted", a)
	}
	// Salt and key decode to the expected sizes.
	parts := strings.Split(a, "$")
	salt, err := phcEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != pinSaltBytes {
		t.Fatalf("salt %q: len %d err %v", parts[4], len(salt), err)
	}
	key, err := phcEncoding.DecodeString(parts[5])
	if err != nil || len(key) != pinKeyBytes {
		t.Fatalf("key %q: len %d err %v", parts[5], len(key), err)
	}
}

func TestVerifyPIN(t *testing.T) {
	h, err := HashPIN("123")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPIN("123", h); !ok || err != nil {
		t.Fatalf("VerifyPIN(correct) = %v, %v", ok, err)
	}
	for _, pin := range []string{"0123", "1230", "12", "", "124"} {
		if ok, err := VerifyPIN(pin, h); ok || err != nil {
			t.Errorf("VerifyPIN(%q) = %v, %v; want false, nil", pin, ok, err)
		}
	}

	// A tampered salt: change its first character (no padding bits there,
	// so the string still decodes) — the key no longer matches.
	parts := strings.Split(h, "$")
	s := []byte(parts[4])
	if s[0] == 'A' {
		s[0] = 'B'
	} else {
		s[0] = 'A'
	}
	parts[4] = string(s)
	if ok, err := VerifyPIN("123", strings.Join(parts, "$")); ok || err != nil {
		t.Fatalf("VerifyPIN(tampered salt) = %v, %v; want false, nil", ok, err)
	}

	bad := []string{
		strings.Replace(h, "$argon2id$", "$argon2i$", 1),
		strings.Replace(h, "$argon2id$", "$argon2d$", 1),
		strings.Replace(h, "$v=19$", "$v=16$", 1),
		strings.Replace(h, "$v=19$", "$", 1), // version omitted
		h[:len(h)/2],                         // truncated mid-salt
		h[:strings.LastIndex(h, "$")],        // hash field missing
		h + "$extra",
		h[:len(h)-1] + "!", // non-base64 hash
		strings.Replace(h, "m=19456", "m=0", 1),
		strings.Replace(h, "m=19456", "m=7", 1), // below 8*p
		strings.Replace(h, "m=19456", "m=999999999", 1),
		strings.Replace(h, "t=2", "t=0", 1),
		strings.Replace(h, "t=2", "t=99", 1),
		strings.Replace(h, "p=1", "p=0", 1),
		strings.Replace(h, "p=1", "p=300", 1),
		strings.Replace(h, "m=19456", "m=+19456", 1),
		strings.Replace(h, "m=19456", "m=019456", 1),
		strings.Replace(h, "m=19456,t=2,p=1", "t=2,m=19456,p=1", 1),
		strings.Replace(h, "m=19456,t=2,p=1", "m=19456,t=2", 1),
		"",
		"$",
		"$$$$$",
		"583920",
		"$argon2id$v=19$m=19456,t=2,p=1$$",
	}
	for _, enc := range bad {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("VerifyPIN(%q) panicked: %v", enc, r)
				}
			}()
			ok, err := VerifyPIN("123", enc)
			if ok {
				t.Errorf("VerifyPIN(%q) = true", enc)
			}
			if !errors.Is(err, ErrBadPINHash) {
				t.Errorf("VerifyPIN(%q) err = %v, want ErrBadPINHash", enc, err)
			}
		}()
	}
}

func TestVerifyPIN_StoredParams(t *testing.T) {
	h, err := hashPINWith("4321", argonParams{memory: 8192, time: 1, threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("hash %q does not carry the custom params", h)
	}
	if ok, err := VerifyPIN("4321", h); !ok || err != nil {
		t.Fatalf("VerifyPIN(custom params) = %v, %v; want true", ok, err)
	}
	if ok, err := VerifyPIN("4322", h); ok || err != nil {
		t.Fatalf("VerifyPIN(wrong, custom params) = %v, %v; want false", ok, err)
	}
}
