package auth

import (
	"context"

	"chwr/internal/domain"
)

type ctxKey int

const (
	userKey ctxKey = iota
	csrfKey
)

// WithUser returns a context carrying the authenticated user.
func WithUser(ctx context.Context, u domain.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// UserFrom returns the authenticated user, and whether the request has one.
func UserFrom(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(userKey).(domain.User)
	return u, ok
}

// MustUser returns the authenticated user. Handlers behind RequireAuth may
// call it; anything else must use UserFrom.
func MustUser(ctx context.Context) domain.User {
	u, ok := UserFrom(ctx)
	if !ok {
		panic("auth: no user in context — handler is not behind RequireAuth")
	}
	return u
}

// ScopeFrom derives the request's data scope from its user. An unauthenticated
// request gets a district scope matching nothing, so a store call on a code
// path that skipped RequireAuth returns empty rather than the country.
func ScopeFrom(ctx context.Context) Scope {
	u, ok := UserFrom(ctx)
	if !ok {
		return District(0)
	}
	return ScopeFor(u)
}

// WithCSRFToken returns a context carrying the request's CSRF token, which
// templates render into every mutating form.
func WithCSRFToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, csrfKey, token)
}

// CSRFTokenFrom returns the request's CSRF token, or "" if none was issued.
func CSRFTokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(csrfKey).(string)
	return t
}
