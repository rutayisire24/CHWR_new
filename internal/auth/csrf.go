package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// CSRF: double-submit cookie. Every response carries a random token in an
// HttpOnly cookie, and every mutating form renders the same value in a hidden
// field. A cross-site form post can reach the endpoint but cannot read the
// cookie to populate the field, so the two never match.
const (
	CSRFCookie = "chwr_csrf"
	CSRFField  = "csrf_token"
)

// CSRF issues the token, puts it in the request context for templates, and
// rejects mutating requests whose form field does not match the cookie.
func CSRF(secure bool, pages ErrorPages) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ""
			if c, err := r.Cookie(CSRFCookie); err == nil {
				token = c.Value
			}
			if token == "" {
				var err error
				if token, err = newCSRFToken(); err != nil {
					http.Error(w, "csrf token", http.StatusInternalServerError)
					return
				}
				setCSRFCookie(w, token, secure)
			}

			if mutating(r.Method) {
				// ParseForm here so handlers can read r.PostForm without
				// re-parsing; a body that will not parse cannot be verified.
				if err := r.ParseForm(); err != nil {
					http.Error(w, "malformed form", http.StatusBadRequest)
					return
				}
				submitted := r.PostForm.Get(CSRFField)
				if submitted == "" {
					submitted = r.Header.Get("X-CSRF-Token")
				}
				if subtle.ConstantTimeCompare([]byte(submitted), []byte(token)) != 1 {
					pages.forbidden(w, r)
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(WithCSRFToken(r.Context(), token)))
		})
	}
}

func mutating(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return true
}

func newCSRFToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func setCSRFCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// RotateCSRF issues a fresh token, called on login and logout so a token
// captured before authentication cannot be replayed after it.
func RotateCSRF(w http.ResponseWriter, secure bool) {
	if token, err := newCSRFToken(); err == nil {
		setCSRFCookie(w, token, secure)
	}
}
