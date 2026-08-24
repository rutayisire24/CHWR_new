package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"chwr/internal/domain"
)

// Authenticator resolves a session cookie to its user and refreshes the
// session's last_seen_at. internal/store implements it; the interface lives
// here so this package does not import the store — the dependency runs the
// other way, because every store method takes a Scope.
type Authenticator interface {
	Authenticate(ctx context.Context, tokenHash []byte) (domain.User, error)
}

// ErrorPages lets the caller render denials in the application's own layout
// instead of net/http's plain text.
type ErrorPages struct {
	Forbidden func(w http.ResponseWriter, r *http.Request)
}

func (e ErrorPages) forbidden(w http.ResponseWriter, r *http.Request) {
	if e.Forbidden != nil {
		e.Forbidden(w, r)
		return
	}
	http.Error(w, "forbidden", http.StatusForbidden)
}

// LoadUser attaches the authenticated user to the request context when the
// session cookie resolves. It never rejects: public routes (login, healthz)
// run through it too, and RequireAuth does the rejecting.
func LoadUser(a Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookie)
			if err != nil || cookie.Value == "" {
				next.ServeHTTP(w, r)
				return
			}
			user, err := a.Authenticate(r.Context(), HashToken(cookie.Value))
			if err != nil {
				// Expired, revoked or unknown: clear the cookie so the browser
				// stops presenting it on every subsequent request.
				if errors.Is(err, domain.ErrSessionExpired) || errors.Is(err, domain.ErrNotFound) {
					ClearSessionCookie(w, r)
					next.ServeHTTP(w, r)
					return
				}
				http.Error(w, "session lookup failed", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
		})
	}
}

// RequireAuth rejects requests without a session. Browsers get a redirect to
// the login form carrying the path they wanted.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFrom(r.Context())
		if !ok {
			redirectToLogin(w, r)
			return
		}
		// A forced first-login reset must complete before anything else is
		// reachable; otherwise the admin-issued password stays usable.
		if u.MustReset && r.URL.Path != "/account/password" {
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCapability rejects a user whose role does not hold the capability.
// This is the first of the three enforcement layers; the Scope argument on
// every store method is the one that actually prevents cross-district reads,
// because a missing middleware check fails open.
func RequireCapability(c Capability, pages ErrorPages) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := UserFrom(r.Context())
			if !ok {
				redirectToLogin(w, r)
				return
			}
			if !Can(u.Role, c) {
				pages.forbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	dest := "/login"
	if r.Method == http.MethodGet && r.URL.Path != "/" {
		dest += "?next=" + url.QueryEscape(r.URL.RequestURI())
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// SetSessionCookie writes the session cookie. Secure is set outside dev, where
// the service is reached over plain http on localhost.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(SessionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie expires the cookie in the browser.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
