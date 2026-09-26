package auth

import (
	"time"

	"chwr/internal/domain"
)

// DefaultAPITokenLifetime is how long a minted bearer token stays valid. It
// matches the 90-minute expectation the eCHIS user-management configuration
// caches a token for; the deployment can override it (config.APITokenTTL).
//
// Tokens are short-lived on purpose: a leaked one expires on its own, and there
// is no refresh — the consumer re-authenticates with its client credentials,
// which it holds anyway.
const DefaultAPITokenLifetime = 90 * time.Minute

// NewAPIToken returns a fresh 256-bit bearer token and its SHA-256. As with a
// session cookie, the raw token is handed to the consumer once and never
// stored; only the digest reaches api_tokens.token_hash, so a database leak
// yields no usable tokens.
func NewAPIToken() (token string, hash []byte, err error) {
	return NewSessionToken()
}

// ScopeForClient derives the data Scope an API client reads through. A national
// client reads the whole register; a district client is confined to its
// district by the same Scope filter that confines a district user. A district
// client with no district (which the schema forbids) degrades to matching
// nothing rather than reading the country.
func ScopeForClient(c domain.APIClient) Scope {
	if c.Scope == domain.APIScopeDistrict {
		if c.DistrictID == nil {
			return District(0)
		}
		return District(*c.DistrictID)
	}
	return National()
}
