package http

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/config"
	"chwr/internal/domain"
	"chwr/internal/store"
	"chwr/internal/web"
)

// Server holds the dependencies every handler needs. Handlers decode,
// authorize, delegate and render; no SQL lives in this package.
type Server struct {
	pool  *pgxpool.Pool
	store *store.Store
	tmpl  *web.Templates
	cfg   config.Config
}

// page is the data every template receives. The chrome — user, nav, CSRF token,
// flash — is filled in by render; handlers supply only Page.
type page struct {
	User      *domain.User
	Nav       nav
	CSRFToken string
	Flash     *flash
	Page      any
}

// nav is which sections the signed-in user may reach, resolved once so
// templates ask a boolean rather than re-deriving the capability matrix.
type nav struct {
	Users bool
	Audit bool
}

// render fills in the chrome and writes the page. A render failure is logged
// and reported as a 500: the response is buffered, so nothing partial escapes.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	p := page{
		CSRFToken: auth.CSRFTokenFrom(r.Context()),
		Flash:     takeFlash(w, r),
		Page:      data,
	}
	if u, ok := auth.UserFrom(r.Context()); ok {
		p.User = &u
		p.Nav = nav{
			Users: auth.Can(u.Role, auth.CapUserManage),
			Audit: auth.Can(u.Role, auth.CapAuditView),
		}
	}

	if err := s.tmpl.Render(w, status, name, p); err != nil {
		slog.Error("render failed", "page", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// fail logs an unexpected error and shows the generic error page. The message
// on screen is deliberately vague; the detail goes to the log.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	s.render(w, r, http.StatusInternalServerError, "error", errorPage{
		Title:   "Something went wrong",
		Message: "The request could not be completed. The error has been logged.",
	})
}

type errorPage struct {
	Title   string
	Message string
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "error", errorPage{
		Title:   "Page not found",
		Message: "That address does not exist.",
	})
}

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusForbidden, "error", errorPage{
		Title:   "Not permitted",
		Message: "Your role does not allow that action. If you believe it should, ask a national administrator.",
	})
}

// secure reports whether cookies should carry the Secure attribute. Off in
// dev, where the service is reached over plain http on localhost.
func (s *Server) secure() bool { return s.cfg.Prod() }

// clientIP is the peer address, for the session row and the audit trail. No
// proxy header is trusted: the service is reached directly, and an
// attacker-set X-Forwarded-For would poison the audit log.
func clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr
}

// safeNext sanitises a ?next= destination. Only same-site absolute paths are
// honoured, so the login form cannot be used as an open redirect.
func safeNext(raw string) string {
	if raw == "" {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "/"
	}
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	return u.RequestURI()
}
