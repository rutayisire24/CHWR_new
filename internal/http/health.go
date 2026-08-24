package http

import (
	"context"
	"net/http"
	"time"
)

// health is the liveness probe. It reports unhealthy when the database is
// unreachable, because a registry that cannot read its own register is not
// serving, whatever the process is doing.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.pool.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok\n"))
}
