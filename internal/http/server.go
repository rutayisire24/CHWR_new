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
	CHWs    bool
	Imports bool
	Users   bool
	Audit   bool
	Section string // first path segment, so /chws/42 still marks Register
}

// section reduces a path to its first segment: a CHW detail page marks the
// same rail entry as the listing it was reached from.
func section(path string) string {
	if i := strings.Index(strings.TrimPrefix(path, "/"), "/"); i >= 0 {
		return path[:i+1]
	}
	return path
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
			CHWs:    auth.Can(u.Role, auth.CapCHWView),
			Imports: auth.Can(u.Role, auth.CapImport),
			Users:   auth.Can(u.Role, auth.CapUserManage),
			Audit:   auth.Can(u.Role, auth.CapAuditView),
			Section: section(r.URL.Path),
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

// clientIP is the address the session row and the audit trail record.
//
// By default it is the peer's own, and no header is believed: anyone can send
// X-Forwarded-For, and audit_log doubles as the CHW change history, so a
// forged one would be a lie in the register's own provenance.
//
// Behind a reverse proxy the peer is the proxy, and every row would say so.
// TRUSTED_PROXY names the proxies whose header may be read — a list of
// addresses, not a boolean, because "trust the header when someone sends one"
// is trusting the client.
func (s *Server) clientIP(r *http.Request) netip.Addr {
	peer := peerAddr(r)
	if !peer.IsValid() || !s.cfg.TrustsProxy(peer) {
		return peer
	}

	// X-Forwarded-For is client, proxy1, proxy2 …, each hop appending the peer
	// it saw. Walking from the right and stopping at the first address we do
	// not trust gives the client: a value the client itself put there sits to
	// the left of the real hops and can never be reached.
	forwarded := r.Header.Values("X-Forwarded-For")
	var hops []string
	for _, value := range forwarded {
		for _, hop := range strings.Split(value, ",") {
			if hop = strings.TrimSpace(hop); hop != "" {
				hops = append(hops, hop)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(hops[i])
		if err != nil {
			// An unparseable hop is where the chain stops being evidence.
			return peer
		}
		if !s.cfg.TrustsProxy(addr) {
			return addr.Unmap()
		}
	}
	// Every hop was a proxy we trust, or there was no header at all.
	return peer
}

// peerAddr is the address the connection actually came from.
func peerAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
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
