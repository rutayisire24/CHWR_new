package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"mime"
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

// MaxMultipartBytes bounds a multipart request body. Bulk import is the only
// route that posts one; its own limit is 10 MB of file (docs/import.md), and
// the headroom here is the part boundaries and the other fields, so that an
// oversized *file* is refused by the importer with a sentence about files
// rather than here with a bare 413.
const MaxMultipartBytes = 12 << 20

// multipartMemory is how much of a multipart body ReadForm holds in memory
// before spilling the rest to a temp file. A register export is megabytes, and
// megabytes per concurrent upload is not a budget worth spending when the disk
// will do.
const multipartMemory = 1 << 20

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
				// The body is parsed here so handlers can read r.PostForm
				// without re-parsing; a body that will not parse cannot be
				// verified.
				cleanup, ok := parseBody(w, r)
				if cleanup != nil {
					// Registered before the token check, so the temp files go
					// even when the request is refused below.
					defer cleanup()
				}
				if !ok {
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

// parseBody populates r.PostForm, for the token check here and for the handler
// that runs after it.
//
// ParseForm does not read a multipart/form-data body: PostForm comes back
// empty, so an upload's csrf_token would look missing and every file upload
// would be refused with a 403 before its handler ran. Multipart bodies need
// ParseMultipartForm, which fills PostForm from the value parts and spills the
// file part to a temp file rather than into memory.
//
// It returns a cleanup for those temp files, and whether the request may
// proceed; when it may not, it has already written the response.
func parseBody(w http.ResponseWriter, r *http.Request) (func(), bool) {
	mediatype, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediatype != "multipart/form-data" {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "malformed form", http.StatusBadRequest)
			return nil, false
		}
		return nil, true
	}

	// The cap belongs here rather than on the route: this is where the body is
	// read, and by the time a handler could impose a limit the middleware has
	// already consumed it. An unauthenticated POST can therefore spend this
	// much disk before the token check refuses it — bounded, and cleaned up by
	// the deferred cleanup either way.
	r.Body = http.MaxBytesReader(w, r.Body, MaxMultipartBytes)
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "that upload is too large", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "malformed form", http.StatusBadRequest)
		return nil, false
	}

	// Removing the temp files centrally means no handler can forget to.
	form := r.MultipartForm
	return func() {
		if form != nil {
			form.RemoveAll()
		}
	}, true
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
