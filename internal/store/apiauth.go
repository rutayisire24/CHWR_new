package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// APIClients is the machine-consumer credential store for the read-only
// interoperability API: provisioning, credential verification, bearer-token
// issue and lookup, and the read-access log.
//
// Two of its methods take no Scope, both by the same argument as
// Sessions.Authenticate and Users.Credentials: they run before there is a
// principal to scope by. IssueToken authenticates a client, and Authenticate
// resolves a token to the client a Scope is then derived from.
type APIClients struct {
	pool *pgxpool.Pool
}

// NewAPIClient is a client as the provisioning command supplies it.
type NewAPIClient struct {
	Name       string
	ClientID   string
	Secret     string
	Scope      domain.APIClientScope
	DistrictID *int64
}

const apiClientColumns = `
    ac.id, ac.name, ac.client_id, ac.scope::text, ac.district_id,
    ac.status::text, ac.created_at, ac.last_used_at`

func scanAPIClient(row pgx.Row) (domain.APIClient, error) {
	var c domain.APIClient
	var scope, status string
	if err := row.Scan(&c.ID, &c.Name, &c.ClientID, &scope, &c.DistrictID,
		&status, &c.CreatedAt, &c.LastUsedAt); err != nil {
		return domain.APIClient{}, err
	}
	c.Scope = domain.APIClientScope(scope)
	c.Status = status
	return c, nil
}

// Create provisions a client and returns it. The secret is argon2id-hashed
// with the same scheme as a user password; the plaintext is never stored. It
// takes no Scope: provisioning is an administrative act performed from the
// command line, like -create-admin.
func (s *APIClients) Create(ctx context.Context, in NewAPIClient) (domain.APIClient, error) {
	if in.Scope == "" {
		in.Scope = domain.APIScopeNational
	}
	if !in.Scope.Valid() {
		return domain.APIClient{}, fmt.Errorf("create api client: invalid scope %q", in.Scope)
	}
	hash, err := auth.HashPassword(in.Secret)
	if err != nil {
		return domain.APIClient{}, fmt.Errorf("create api client: %w", err)
	}

	// RETURNING straight off the INSERT: a data-modifying CTE's new row is not
	// visible to a self-join elsewhere in the same statement, so there is no CTE
	// here — the api_clients row needs no post-insert re-read the way a CHW does
	// for its trigger-derived district.
	const q = `
	    INSERT INTO api_clients (name, client_id, secret_hash, scope, district_id)
	    VALUES ($1, $2, $3, $4::api_client_scope, $5)
	    RETURNING id, name, client_id, scope::text, district_id, status::text, created_at, last_used_at`

	c, err := scanAPIClient(s.pool.QueryRow(ctx, q,
		in.Name, in.ClientID, hash, string(in.Scope), in.DistrictID))
	if err != nil {
		return domain.APIClient{}, fmt.Errorf("create api client: %w", translate(err))
	}
	return c, nil
}

// IssueToken verifies a client's credentials and, on success, mints a bearer
// token. The raw token is returned once and never stored; only its SHA-256
// reaches api_tokens.
//
// A wrong secret, an unknown client_id and a disabled client are one outcome to
// the caller — ErrInvalidCredentials — and an unknown client_id still pays for
// one argon2id verification, so response timing does not disclose which
// client_ids are registered.
func (s *APIClients) IssueToken(ctx context.Context, clientID, secret string, ttl time.Duration, ip netip.Addr) (string, domain.APIClient, error) {
	var c domain.APIClient
	var scope, status, hash string
	err := s.pool.QueryRow(ctx, `
	    SELECT ac.id, ac.name, ac.client_id, ac.scope::text, ac.district_id,
	           ac.status::text, ac.created_at, ac.last_used_at, ac.secret_hash
	      FROM api_clients ac WHERE ac.client_id = $1`, clientID).Scan(
		&c.ID, &c.Name, &c.ClientID, &scope, &c.DistrictID,
		&status, &c.CreatedAt, &c.LastUsedAt, &hash)
	if err != nil {
		if errors.Is(translate(err), domain.ErrNotFound) {
			auth.BurnTime(secret) // keep the unknown-client path as costly as the real one
			return "", domain.APIClient{}, domain.ErrInvalidCredentials
		}
		return "", domain.APIClient{}, fmt.Errorf("issue token: %w", err)
	}
	c.Scope = domain.APIClientScope(scope)
	c.Status = status

	ok, verr := auth.VerifyPassword(hash, secret)
	if verr != nil || !ok || !c.Active() {
		return "", domain.APIClient{}, domain.ErrInvalidCredentials
	}

	token, tokenHash, err := auth.NewAPIToken()
	if err != nil {
		return "", domain.APIClient{}, err
	}
	var ipArg any
	if ip.IsValid() {
		ipArg = ip.String()
	}
	if _, err := s.pool.Exec(ctx, `
	    INSERT INTO api_tokens (token_hash, client_id, expires_at, ip)
	    VALUES ($1, $2, now() + $3::interval, $4::inet)`,
		tokenHash, c.ID, ttl.String(), ipArg); err != nil {
		return "", domain.APIClient{}, fmt.Errorf("issue token: %w", translate(err))
	}
	_, _ = s.pool.Exec(ctx, `UPDATE api_clients SET last_used_at = now() WHERE id = $1`, c.ID)
	return token, c, nil
}

// Authenticate resolves a bearer token's hash to its client, enforcing the
// expiry and refreshing last_used_at in the same statement, and joining an
// active client so a disabled client's token stops working at once. Scope-free
// by the same argument as Sessions.Authenticate: this is the call that produces
// the principal a Scope is derived from.
func (s *APIClients) Authenticate(ctx context.Context, tokenHash []byte) (domain.APIClient, error) {
	const q = `
	    WITH live AS (
	        UPDATE api_tokens
	           SET last_used_at = now()
	         WHERE token_hash = $1
	           AND expires_at > now()
	        RETURNING client_id
	    )
	    SELECT ` + apiClientColumns + `
	    FROM live
	    JOIN api_clients ac ON ac.id = live.client_id
	    WHERE ac.status = 'active'`

	c, err := scanAPIClient(s.pool.QueryRow(ctx, q, tokenHash))
	if err != nil {
		if errors.Is(translate(err), domain.ErrNotFound) {
			return domain.APIClient{}, domain.ErrSessionExpired
		}
		return domain.APIClient{}, fmt.Errorf("authenticate api token: %w", err)
	}
	return c, nil
}

// DeleteExpiredTokens clears tokens past their expiry. The expiry check in
// Authenticate is what enforces the deadline; this is housekeeping.
func (s *APIClients) DeleteExpiredTokens(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM api_tokens WHERE expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("purge expired api tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AccessEntry is one row of the read-access log.
type AccessEntry struct {
	ClientID   *int64
	ClientName string
	Method     string
	Path       string
	Query      string
	Status     int
	RowCount   *int
	IP         netip.Addr
}

// LogAccess records an API request. It is best-effort: a failure to log must
// not fail the request, so callers ignore the error or log it to slog.
func (s *APIClients) LogAccess(ctx context.Context, e AccessEntry) error {
	var ipArg any
	if e.IP.IsValid() {
		ipArg = e.IP.String()
	}
	_, err := s.pool.Exec(ctx, `
	    INSERT INTO api_access_log
	        (client_id, client_name, method, path, query, status, row_count, ip)
	    VALUES ($1, $2, $3, $4, nullif($5,''), $6, $7, $8::inet)`,
		e.ClientID, nullifStr(e.ClientName), e.Method, e.Path, e.Query,
		e.Status, e.RowCount, ipArg)
	if err != nil {
		return fmt.Errorf("log api access: %w", err)
	}
	return nil
}

func nullifStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
