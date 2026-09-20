package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// CookieName is the session cookie.
const CookieName = "adguard_reward_session"

// tokenBytes is the raw token size: 256 random bits, 43 base64url chars.
const tokenBytes = 32

// tokenEncoding is unpadded base64url. Decoding is Strict so the final
// character's padding bits must be zero — otherwise two distinct strings
// would decode to the same bytes and name one session.
var tokenEncoding = base64.RawURLEncoding.Strict()

// newToken returns a fresh raw token and its hash. Only the hash is stored.
func newToken() (raw string, hash [32]byte, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", [32]byte{}, err
	}
	return tokenEncoding.EncodeToString(b), sha256.Sum256(b), nil
}

// hashToken decodes a presented token and hashes it. ok is false for
// anything that is not the canonical encoding of exactly tokenBytes bytes.
func hashToken(raw string) (hash [32]byte, ok bool) {
	if len(raw) != tokenEncoding.EncodedLen(tokenBytes) {
		return [32]byte{}, false
	}
	b, err := tokenEncoding.DecodeString(raw)
	if err != nil || len(b) != tokenBytes {
		return [32]byte{}, false
	}
	return sha256.Sum256(b), true
}
