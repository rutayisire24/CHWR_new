package store

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Sessions is the server-side session table. The cookie carries a random
// token; only its SHA-256 is stored, so a database leak yields no usable
// cookies. Sessions are rows rather than JWTs because staff departures require
// instant revocation.
type Sessions struct {
	pool *pgxpool.Pool
}

// Create issues a session for the user and returns the raw cookie value. The
// raw token is returned once, here, and never persisted.
func (s *Sessions) Create(ctx context.Context, userID int64, ip netip.Addr, userAgent string) (string, error) {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}

	var ipArg any
	if ip.IsValid() {
		ipArg = ip.String()
	}

	_, err = s.pool.Exec(ctx, `
	    INSERT INTO sessions (token_hash, user_id, expires_at, ip, user_agent)
	    VALUES ($1, $2, now() + $3::interval, $4::inet, $5)`,
		hash, userID, auth.SessionLifetime.String(), ipArg, truncate(userAgent, 400))
	if err != nil {
		return "", fmt.Errorf("create session for user %d: %w", userID, translate(err))
	}
	return token, nil
}

// Authenticate resolves a cookie's token hash to its user, enforcing both the
// absolute expiry and the idle timeout, and refreshing last_seen_at in the
// same statement. It satisfies auth.Authenticator.
//
// Scope-free by the same argument as Users.Credentials: this call is what
// produces the principal a Scope is derived from.
func (s *Sessions) Authenticate(ctx context.Context, tokenHash []byte) (domain.User, error) {
	const q = `
	    WITH live AS (
	        UPDATE sessions
	           SET last_seen_at = now()
	         WHERE token_hash = $1
	           AND expires_at > now()
	           AND last_seen_at > now() - $2::interval
	        RETURNING user_id
	    )
	    SELECT ` + userColumns + `
	    FROM live
	    JOIN users u ON u.id = live.user_id
	    LEFT JOIN locations d ON d.id = u.district_id
	    WHERE u.status = 'active'`

	u, err := scanUser(s.pool.QueryRow(ctx, q, tokenHash, auth.SessionIdle.String()))
	if err != nil {
		if translated := translate(err); translated == domain.ErrNotFound {
			// Unknown, expired, idled out, or belonging to a disabled account:
			// all four are one outcome to the caller, which clears the cookie.
			return domain.User{}, domain.ErrSessionExpired
		}
		return domain.User{}, fmt.Errorf("authenticate session: %w", err)
	}
	return u, nil
}

// Delete revokes one session — the logout path.
func (s *Sessions) Delete(ctx context.Context, tokenHash []byte) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpired clears sessions past their absolute expiry. Called on a timer
// by the server; the expiry check in Authenticate is what actually enforces
// the deadline, so this is housekeeping, not security.
func (s *Sessions) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("purge expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// TouchedWithin reports the cutoff an idle session is measured against, for
// tests and for the account page.
func TouchedWithin() time.Duration { return auth.SessionIdle }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
