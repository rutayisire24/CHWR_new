// Package http wires routes to handlers. Handlers decode, authorize, delegate
// and render; no SQL lives here.
package http

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Server holds the dependencies every handler needs.
type Server struct {
	pool *pgxpool.Pool
}

// New returns the application's root handler.
func New(pool *pgxpool.Pool) http.Handler {
	s := &Server{pool: pool}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	return mux
}
