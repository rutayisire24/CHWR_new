// Package http wires routes to handlers. Handlers decode, authorize, delegate
// and render; no SQL lives here.
package http

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/config"
	"chwr/internal/store"
	"chwr/internal/web"
)

// New returns the application's root handler. Templates are parsed here, once,
// so a broken template stops startup instead of a request.
func New(pool *pgxpool.Pool, cfg config.Config) (http.Handler, error) {
	tmpl, err := web.Parse()
	if err != nil {
		return nil, err
	}

	s := &Server{
		pool:  pool,
		store: store.New(pool),
		tmpl:  tmpl,
		cfg:   cfg,
	}
	pages := auth.ErrorPages{Forbidden: s.forbidden}

	mux := http.NewServeMux()

	// Public.
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("GET /static/", http.StripPrefix("/static/",
		http.FileServer(http.FS(web.Static()))))
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)

	// Signed in. RequireAuth also holds a user with must_reset on the password
	// page until they have chosen their own.
	signedIn := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(h)
	}
	mux.Handle("GET /{$}", signedIn(s.dashboard))
	mux.Handle("POST /logout", signedIn(s.logout))
	mux.Handle("GET /account/password", signedIn(s.passwordForm))
	mux.Handle("POST /account/password", signedIn(s.changePassword))

	// Capability-gated. RequireCapability is the first of the three layers;
	// the Scope argument inside each store call is the one that confines a
	// district user's reads, because a missing middleware check fails open.
	manageUsers := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireCapability(auth.CapUserManage, pages)(h))
	}
	mux.Handle("GET /users", manageUsers(s.usersList))
	mux.Handle("GET /users/new", manageUsers(s.userNew))
	mux.Handle("POST /users/new", manageUsers(s.userCreate))
	mux.Handle("GET /users/{id}", manageUsers(s.userEdit))
	mux.Handle("POST /users/{id}", manageUsers(s.userUpdate))
	mux.Handle("POST /users/{id}/status", manageUsers(s.userStatus))
	mux.Handle("POST /users/{id}/reset", manageUsers(s.userResetPassword))

	// The register. Viewing is every role; writing needs the manage capability,
	// and the Scope inside each store call is what keeps a district manager to
	// their own district.
	viewCHWs := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireCapability(auth.CapCHWView, pages)(h))
	}
	editCHWs := func(c auth.Capability, h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireCapability(c, pages)(h))
	}
	mux.Handle("GET /chws", viewCHWs(s.chwsList))
	mux.Handle("GET /chws/new", editCHWs(auth.CapCHWCreate, s.chwNew))
	mux.Handle("POST /chws/new", editCHWs(auth.CapCHWCreate, s.chwCreate))
	mux.Handle("GET /chws/{id}", viewCHWs(s.chwShow))
	mux.Handle("GET /chws/{id}/edit", editCHWs(auth.CapCHWUpdate, s.chwEdit))
	mux.Handle("POST /chws/{id}", editCHWs(auth.CapCHWUpdate, s.chwUpdate))
	mux.Handle("GET /chws/{id}/profile", editCHWs(auth.CapCHWUpdate, s.chwProfileForm))
	mux.Handle("POST /chws/{id}/profile", editCHWs(auth.CapCHWUpdate, s.chwProfileSave))
	mux.Handle("POST /chws/{id}/deactivate", editCHWs(auth.CapCHWDeactivate, s.chwDeactivate))
	mux.Handle("POST /chws/{id}/reactivate", editCHWs(auth.CapCHWDeactivate, s.chwReactivate))

	// Bulk import. Its own capability rather than chw.create: an upload is a
	// different act from adding one CHW, and the Scope inside every store call
	// is what keeps a district manager's file to their own district.
	mayImport := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireCapability(auth.CapImport, pages)(h))
	}
	mux.Handle("GET /imports", mayImport(s.importsList))
	mux.Handle("GET /imports/template.csv", mayImport(s.importTemplate))
	mux.Handle("POST /imports", mayImport(s.importUpload))
	mux.Handle("GET /imports/{id}", mayImport(s.importShow))
	mux.Handle("GET /imports/{id}/errors.csv", mayImport(s.importErrors))
	mux.Handle("POST /imports/{id}/commit", mayImport(s.importCommit))
	mux.Handle("POST /imports/{id}/discard", mayImport(s.importDiscard))

	// Feeds the cascading selects. Read-only, and scoped like every other read.
	mux.Handle("GET /api/locations", viewCHWs(s.locationsJSON))

	viewAudit := func(h http.HandlerFunc) http.Handler {
		return auth.RequireAuth(auth.RequireCapability(auth.CapAuditView, pages)(h))
	}
	mux.Handle("GET /audit", viewAudit(s.auditList))

	// Anything the patterns above did not claim.
	mux.HandleFunc("GET /", s.notFound)

	// Outermost first: security headers, then CSRF (which parses the form and
	// issues the token), then the session lookup every handler reads from.
	return securityHeaders(
		auth.CSRF(s.secure(), pages)(
			auth.LoadUser(s.store.Sessions)(mux),
		),
	), nil
}

// securityHeaders sets the defaults every response carries. The CSP is strict
// because there is no JS build step and no CDN: scripts and styles are our own
// files, served from this origin.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
