package http

import (
	"net/http"

	"chwr/internal/auth"
	"chwr/internal/store"
)

type dashboardPage struct {
	Locations  int64
	Facilities int64
	CHWs       int64
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	// The stdlib mux matches "/" as a prefix for anything unrouted, so the
	// catch-all lands here and has to be turned away explicitly.
	if r.URL.Path != "/" {
		s.notFound(w, r)
		return
	}

	counts, err := s.store.Locations.Counts(r.Context(), auth.ScopeFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard", dashboardPage{
		Locations:  counts.Locations,
		Facilities: counts.Facilities,
		CHWs:       counts.CHWs,
	})
}

type auditPage struct {
	Entries []store.LogEntry
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.Audit.List(r.Context(), auth.ScopeFrom(r.Context()), 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "audit", auditPage{Entries: entries})
}
