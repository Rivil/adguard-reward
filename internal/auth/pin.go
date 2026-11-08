package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// PIN length bounds, in ASCII digits.
const (
	pinMinLen = 4
	pinMaxLen = 6
)

// ErrBadPIN is returned by ValidatePIN for anything but 4-6 ASCII digits.
var ErrBadPIN = errors.New("pin must be 4-6 digits")

// ErrBadPINHash is returned by VerifyPIN when the stored string is not a
// well-formed Argon2id v19 PHC hash within the accepted parameter bounds.
var ErrBadPINHash = errors.New("malformed pin hash")

// argonParams are the Argon2id cost parameters carried in a PHC string.
type argonParams struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
}

// defaultArgon is the cost used for new hashes: the OWASP Argon2id floor
// (19 MiB, 2 passes, 1 lane).
var defaultArgon = argonParams{memory: 19456, time: 2, threads: 1}

// Hash sizes and the bounds VerifyPIN accepts from a stored string. The upper
// bounds stop a tampered row from turning one unlock into a memory or CPU
// exhaustion; the lower bounds reject degenerate parameters argon2 would
// panic on or that would be trivially brute-forced.
const (
	pinSaltBytes = 16
	pinKeyBytes  = 32

	minSaltBytes = 8
	maxSaltBytes = 64
	minKeyBytes  = 16
	maxKeyBytes  = 64
	maxMemoryKiB = 1 << 18 // 256 MiB
	maxTime      = 16
	maxThreads   = 16
)

// phcEncoding is unpadded standard base64, as the PHC string format uses.
// Strict so a string has exactly one decoding.
var phcEncoding = base64.RawStdEncoding.Strict()

// ValidatePIN accepts exactly 4-6 bytes, each '0'-'9'. No trimming and no
// Unicode digit classes: "１２３４" and " 1234" are both rejected.
func ValidatePIN(pin string) error {
	if len(pin) < pinMinLen || len(pin) > pinMaxLen {
		return ErrBadPIN
	}
	for i := 0; i < len(pin); i++ {
		if pin[i] < '0' || pin[i] > '9' {
			return ErrBadPIN
		}
	}
	return nil
}

// HashPIN returns a salted Argon2id PHC string for pin:
// $argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<hash>.
// It does not validate pin; callers run ValidatePIN first.
func HashPIN(pin string) (string, error) {
	return hashPINWith(pin, defaultArgon)
}

func hashPINWith(pin string, p argonParams) (string, error) {
	salt := make([]byte, pinSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("pin salt: %w", err)
	}
	key := argon2.IDKey([]byte(pin), salt, p.time, p.memory, p.threads, pinKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads,
		phcEncoding.EncodeToString(salt), phcEncoding.EncodeToString(key)), nil
}

// VerifyPIN reports whether pin matches the stored PHC string. The cost
// parameters are read from the string itself, so hashes made under older
// defaults still verify. A string that is not Argon2id v19 PHC returns
// ErrBadPINHash, never true. The final comparison is constant-time.
func VerifyPIN(pin, encoded string) (bool, error) {
	p, salt, want, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(pin), salt, p.time, p.memory, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// parsePHC splits and checks a stored hash. Every field must be present,
// canonical and in bounds.
func parsePHC(encoded string) (argonParams, []byte, []byte, error) {
	var p argonParams
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrBadPINHash
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, nil, nil, ErrBadPINHash
	}
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return p, nil, nil, ErrBadPINHash
	}
	m, okM := phcUint(fields[0], "m=", maxMemoryKiB)
	t, okT := phcUint(fields[1], "t=", maxTime)
	th, okP := phcUint(fields[2], "p=", maxThreads)
	if !okM || !okT || !okP || t < 1 || th < 1 || m < 8*th {
		return p, nil, nil, ErrBadPINHash
	}
	p = argonParams{memory: uint32(m), time: uint32(t), threads: uint8(th)}

	salt, err := phcEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < minSaltBytes || len(salt) > maxSaltBytes {
		return p, nil, nil, ErrBadPINHash
	}
	key, err := phcEncoding.DecodeString(parts[5])
	if err != nil || len(key) < minKeyBytes || len(key) > maxKeyBytes {
		return p, nil, nil, ErrBadPINHash
	}
	return p, salt, key, nil
}

// phcUint parses "<prefix><decimal>" with no sign, no leading zeros and a
// value no greater than max.
func phcUint(field, prefix string, max uint64) (uint64, bool) {
	digits, ok := strings.CutPrefix(field, prefix)
	if !ok || digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseUint(digits, 10, 32)
	if err != nil || v > max {
		return 0, false
	}
	return v, true
}
