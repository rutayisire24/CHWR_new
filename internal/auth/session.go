package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

// Session cookie and lifetime. Sessions are server-side rows in Postgres:
// staff departures require instant revocation, which a JWT cannot give without
// reintroducing the state it was meant to avoid.
const (
	SessionCookie   = "chwr_session"
	SessionLifetime = 12 * time.Hour // one working day; re-login next morning
	SessionIdle     = 2 * time.Hour  // no activity for this long ends it early
)

// NewSessionToken returns a fresh 256-bit token and its SHA-256. The raw token
// goes into the cookie and is never stored; only the digest reaches
// sessions.token_hash, so a database leak yields no usable cookies.
func NewSessionToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken returns the SHA-256 of a cookie value, for lookup in `sessions`.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
