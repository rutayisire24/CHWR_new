package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters. RFC 9106's second recommended profile: 64 MiB, three
// passes, four lanes — comfortable on the deployment target and well above the
// interactive minimum. Hashes carry their own parameters, so raising these
// later re-hashes on next login instead of invalidating stored passwords.
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// ErrMalformedHash means the stored password_hash is not a hash this package
// wrote. It is a data problem, not a wrong password, and must not be reported
// to the user as one.
var ErrMalformedHash = errors.New("malformed password hash")

// HashPassword returns a PHC-format argon2id hash:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>
func HashPassword(plaintext string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether plaintext produces the encoded hash. The
// comparison is constant-time.
func VerifyPassword(encoded, plaintext string) (bool, error) {
	salt, key, time, memory, threads, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	candidate := argon2.IDKey([]byte(plaintext), salt, time, memory, threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(key, candidate) == 1, nil
}

func decodeHash(encoded string) (salt, key []byte, time, memory uint32, threads uint8, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, ErrMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, nil, 0, 0, 0, ErrMalformedHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return nil, nil, 0, 0, 0, ErrMalformedHash
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return nil, nil, 0, 0, 0, ErrMalformedHash
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return nil, nil, 0, 0, 0, ErrMalformedHash
	}
	return salt, key, time, memory, threads, nil
}

// dummyHash is verified against when an email does not exist, so a failed
// login costs the same work whether or not the account is real. Generated once
// at startup from a random password nobody holds.
var dummyHash = mustDummyHash()

func mustDummyHash() string {
	filler := make([]byte, 32)
	if _, err := rand.Read(filler); err != nil {
		panic(fmt.Sprintf("auth: cannot seed dummy hash: %v", err))
	}
	h, err := HashPassword(base64.RawStdEncoding.EncodeToString(filler))
	if err != nil {
		panic(fmt.Sprintf("auth: cannot build dummy hash: %v", err))
	}
	return h
}

// BurnTime performs one argon2id verification against a throwaway hash. Call
// it on the "no such user" path so response timing does not disclose which
// emails are registered.
func BurnTime(plaintext string) {
	_, _ = VerifyPassword(dummyHash, plaintext)
}
