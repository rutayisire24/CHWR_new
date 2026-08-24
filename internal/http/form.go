package http

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"chwr/internal/store"
)

// trimmed reads a posted field with surrounding whitespace removed. The CSRF
// middleware has already called ParseForm, so PostForm is populated by the
// time any handler runs.
func trimmed(r *http.Request, key string) string {
	return strings.TrimSpace(r.PostForm.Get(key))
}

// optionalID reads a posted numeric id that may be blank — a district select
// left on "national role, no district", for instance. It reports whether the
// value was present and well-formed.
func optionalID(r *http.Request, key string) (*int64, bool) {
	raw := trimmed(r, key)
	if raw == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, false
	}
	return &id, true
}

// pathID reads a numeric path segment declared in the route pattern.
func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// audit appends an entry for an action that has already happened.
//
// A failure here is logged, not returned: the mutation is committed, and
// refusing the response would tell the user their change failed when it did
// not. Mutations that can share a transaction with their audit row use
// store.Audit.RecordTx instead, which is the stronger form invariant 6 asks
// for; the authentication events here have no such transaction to join.
func (s *Server) audit(r *http.Request, e store.Entry) {
	if err := s.store.Audit.Record(r.Context(), e); err != nil {
		slog.Error("audit write failed", "action", e.Action, "entity", e.Entity, "err", err)
	}
}
