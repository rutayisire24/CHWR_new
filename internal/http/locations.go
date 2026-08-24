package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// locationsJSON feeds the cascading selects on the CHW form: given an ancestor
// and a level, the locations of that level beneath it.
//
// The cascade skips county — the UI walks district > subcounty > parish >
// village — which is why this takes a level rather than returning children.
// County is still mandatory in the data (subcounty codes are unique only
// within a county); it is derived from the path for display.
//
// It is scoped like everything else: a district user asking for another
// district's subcounties gets an empty list, not a listing.
func (s *Server) locationsJSON(w http.ResponseWriter, r *http.Request) {
	level := domain.Level(r.URL.Query().Get("level"))
	switch level {
	case domain.LevelSubcounty, domain.LevelParish, domain.LevelVillage:
	default:
		http.Error(w, `{"error":"level must be subcounty, parish or village"}`, http.StatusBadRequest)
		return
	}

	under, err := strconv.ParseInt(r.URL.Query().Get("under"), 10, 64)
	if err != nil || under <= 0 {
		http.Error(w, `{"error":"under must be a location id"}`, http.StatusBadRequest)
		return
	}

	places, err := s.store.Locations.Descendants(r.Context(), auth.ScopeFrom(r.Context()), under, level)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.Error("locations lookup failed", "under", under, "level", level, "err", err)
		http.Error(w, `{"error":"lookup failed"}`, http.StatusInternalServerError)
		return
	}

	// Out of scope and genuinely childless are the same answer here: an empty
	// list. A district user must not learn that another district exists by the
	// shape of the error.
	out := make([]map[string]any, 0, len(places))
	for _, p := range places {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300") // the hierarchy does not move during a session
	if err := json.NewEncoder(w).Encode(out); err != nil {
		slog.Error("locations encode failed", "err", err)
	}
}
