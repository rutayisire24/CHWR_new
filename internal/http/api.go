package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chwr/internal/auth"
	"chwr/internal/domain"
	"chwr/internal/store"
)

// This file is the read-only interoperability API (decisions D1–D13). It is
// mounted under /api/v1/ outside the browser session and CSRF chain: machine
// consumers authenticate with a bearer token, not a cookie, so CSRF — which
// guards browser forms — does not apply. The register is never written here.

type apiCtxKey int

const (
	apiClientKey apiCtxKey = iota
	apiRowsKey
)

// apiHandler builds the /api/v1 sub-handler. The token endpoint is open (it IS
// the authentication); every data route sits behind requireAPIToken, and every
// route is wrapped in apiAccess so each request is logged.
func (s *Server) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/auth/token", s.apiAccess(http.HandlerFunc(s.apiToken)))

	authed := func(h http.HandlerFunc) http.Handler {
		return s.apiAccess(s.requireAPIToken(h))
	}
	mux.Handle("GET /api/v1/chws", authed(s.apiListCHWs))
	mux.Handle("GET /api/v1/chws/{hwid}", authed(s.apiGetCHW))
	mux.Handle("GET /api/v1/chws/{hwid}/supervisees", authed(s.apiSupervisees))
	mux.Handle("GET /api/v1/chws/{hwid}/supervisor", authed(s.apiSupervisor))

	// Unauthenticated liveness, no data: lets a consumer confirm reachability.
	mux.HandleFunc("GET /api/v1/health", s.apiHealth)
	mux.HandleFunc("/api/v1/", s.apiNotFound)
	return mux
}

// ---- middleware -----------------------------------------------------------

// requireAPIToken authenticates the bearer token and attaches the client and
// its Scope to the request context.
func (s *Server) requireAPIToken(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeAPIError(w, http.StatusUnauthorized, "missing or malformed Authorization bearer token")
			return
		}
		client, err := s.store.APIClients.Authenticate(r.Context(), auth.HashToken(token))
		if err != nil {
			if errors.Is(err, domain.ErrSessionExpired) || errors.Is(err, domain.ErrNotFound) {
				writeAPIError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			slog.Error("api token auth failed", "err", err)
			writeAPIError(w, http.StatusInternalServerError, "authentication failed")
			return
		}
		ctx := context.WithValue(r.Context(), apiClientKey, client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// apiAccess records every API request in api_access_log, best-effort, and
// captures the response status. It runs outermost so it sees the final status
// whether or not the request authenticated.
func (s *Server) apiAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows := new(int)
		*rows = -1 // -1 means "not a row-returning handler"
		rec := &apiRecorder{ResponseWriter: w, status: http.StatusOK}
		ctx := context.WithValue(r.Context(), apiRowsKey, rows)

		next.ServeHTTP(rec, r.WithContext(ctx))

		entry := store.AccessEntry{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Status: rec.status,
			IP:     s.clientIP(r),
		}
		if c, ok := apiClientFrom(ctx); ok {
			id := c.ID
			entry.ClientID = &id
			entry.ClientName = c.Name
		}
		if *rows >= 0 {
			n := *rows
			entry.RowCount = &n
		}
		if err := s.store.APIClients.LogAccess(context.Background(), entry); err != nil {
			slog.Error("api access log failed", "err", err)
		}
	})
}

type apiRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *apiRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *apiRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// ---- handlers -------------------------------------------------------------

type tokenRequest struct {
	ClientID     string `json:"client_id"`
	Email        string `json:"email"`
	ClientSecret string `json:"client_secret"`
	Password     string `json:"password"`
}

// apiToken mints a bearer token from client credentials. It accepts the
// client_id/client_secret pair, and also email/password, which is what the
// eCHIS user-management configuration sends.
func (s *Server) apiToken(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "body must be JSON with client_id and client_secret")
		return
	}
	clientID := firstNonEmpty(req.ClientID, req.Email)
	secret := firstNonEmpty(req.ClientSecret, req.Password)
	if clientID == "" || secret == "" {
		writeAPIError(w, http.StatusBadRequest, "client_id and client_secret are required")
		return
	}

	token, client, err := s.store.APIClients.IssueToken(
		r.Context(), clientID, secret, s.cfg.APITokenTTL, s.clientIP(r))
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			writeAPIError(w, http.StatusUnauthorized, "invalid client credentials")
			return
		}
		slog.Error("api token issue failed", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "could not issue token")
		return
	}

	ttl := int(s.cfg.APITokenTTL.Seconds())
	writeJSON(w, http.StatusOK, map[string]any{
		// Both spellings: the consumer reads access_token or token.
		"token":        token,
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   ttl,
		"expires_at":   time.Now().Add(s.cfg.APITokenTTL).UTC().Format(time.RFC3339),
		"scope":        string(client.Scope),
	})
}

// apiListCHWs is the query endpoint the consumers read. It honours the eCHIS
// filters (active, district, facility, village), a cadre filter, the
// warehouse's updated_since and keyset paging, and the targeted-supervision
// filter.
func (s *Server) apiListCHWs(w http.ResponseWriter, r *http.Request) {
	client, _ := apiClientFrom(r.Context())
	sc := auth.ScopeForClient(client)
	q := r.URL.Query()

	f := store.APIFilter{
		Cadre:                   strings.ToLower(q.Get("cadre")),
		DistrictName:            q.Get("district"),
		FacilityName:            q.Get("facility"),
		VillageName:             q.Get("village"),
		Supervision:             strings.ToLower(q.Get("supervision")),
		SupervisionIntervalDays: s.cfg.SupervisionIntervalDays,
	}
	if v := q.Get("active"); v != "" {
		b := v == "true" || v == "1"
		f.Active = &b
	}
	if v := q.Get("district_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.DistrictID = &id
		}
	}
	if v := q.Get("updated_since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "updated_since must be an RFC3339 timestamp")
			return
		}
		f.UpdatedSince = &t
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := q.Get("cursor"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.AfterID = id
		}
	}

	list, more, err := s.store.API.List(r.Context(), sc, f)
	if err != nil {
		slog.Error("api list chws failed", "err", err)
		writeAPIError(w, http.StatusInternalServerError, "could not read the register")
		return
	}
	setAPIRows(r.Context(), len(list))

	items := make([]map[string]any, 0, len(list))
	var nextCursor int64
	for _, c := range list {
		items = append(items, s.apiCHWJSON(c))
		nextCursor = c.ID
	}
	page := map[string]any{"limit": effectiveLimit(f.Limit), "count": len(list), "hasMore": more}
	if more {
		page["nextCursor"] = nextCursor
	}
	writeJSON(w, http.StatusOK, map[string]any{"chws": items, "pagination": page})
}

func (s *Server) apiGetCHW(w http.ResponseWriter, r *http.Request) {
	client, _ := apiClientFrom(r.Context())
	id, ok := hwidParam(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "hwid must be a positive integer")
		return
	}
	c, err := s.store.API.Get(r.Context(), auth.ScopeForClient(client), id)
	if err != nil {
		s.apiNotFoundOrFail(w, err, "read chw")
		return
	}
	setAPIRows(r.Context(), 1)
	writeJSON(w, http.StatusOK, s.apiCHWJSON(c))
}

func (s *Server) apiSupervisees(w http.ResponseWriter, r *http.Request) {
	client, _ := apiClientFrom(r.Context())
	id, ok := hwidParam(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "hwid must be a positive integer")
		return
	}
	list, err := s.store.API.Supervisees(r.Context(), auth.ScopeForClient(client), id)
	if err != nil {
		s.apiNotFoundOrFail(w, err, "read supervisees")
		return
	}
	setAPIRows(r.Context(), len(list))
	items := make([]map[string]any, 0, len(list))
	for _, c := range list {
		items = append(items, s.apiCHWJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"chws": items, "supervisorHwId": id, "count": len(list)})
}

func (s *Server) apiSupervisor(w http.ResponseWriter, r *http.Request) {
	client, _ := apiClientFrom(r.Context())
	sc := auth.ScopeForClient(client)
	id, ok := hwidParam(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "hwid must be a positive integer")
		return
	}
	vht, err := s.store.API.Get(r.Context(), sc, id)
	if err != nil {
		s.apiNotFoundOrFail(w, err, "read supervisor")
		return
	}
	if vht.SupervisorID == nil {
		writeAPIError(w, http.StatusNotFound, "no supervisor recorded for this CHW")
		return
	}
	sup, err := s.store.API.Get(r.Context(), sc, *vht.SupervisorID)
	if err != nil {
		s.apiNotFoundOrFail(w, err, "read supervisor")
		return
	}
	setAPIRows(r.Context(), 1)
	writeJSON(w, http.StatusOK, s.apiCHWJSON(sup))
}

func (s *Server) apiHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "api": "chwr-interop", "version": "v1"})
}

func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, http.StatusNotFound, "no such endpoint")
}

// ---- JSON projection ------------------------------------------------------

// apiCHWJSON is the per-CHW JSON body. The field names and nesting are exactly
// what the eCHIS user-management mapping reads (fullName, nationalId, id, phone,
// birthDate, gender, parish, district.name, subcounty.name,
// position.facility.name, villages[0]); the rest are extras the warehouse can
// use and other consumers ignore.
func (s *Server) apiCHWJSON(c store.APICHW) map[string]any {
	birth, approx := approxBirthDate(c)
	m := map[string]any{
		"id":         c.ID,
		"hwId":       c.ID,
		"fullName":   strings.TrimSpace(c.FirstName + " " + c.LastName),
		"firstName":  c.FirstName,
		"lastName":   c.LastName,
		"nationalId": c.NIN,
		"phone":      c.Phone,
		"gender":     c.Sex,
		"cadre":      c.Cadre,
		"status":     c.Status,
		"active":     c.Status == "active",
		"parish":     c.Parish,
		"district":   map[string]any{"name": c.District},
		"subcounty":  map[string]any{"name": c.Subcounty},
		"village":    c.Village,
		"villages":   c.Villages,
		"position":   map[string]any{"facility": map[string]any{"name": c.Facility}},
		"updatedAt":  c.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if birth != "" {
		m["birthDate"] = birth
		m["birthDateApproximate"] = approx
	}
	if c.SupervisorID != nil {
		m["supervisor"] = map[string]any{"hwId": *c.SupervisorID, "name": c.SupervisorName}
	} else {
		m["supervisor"] = nil
	}
	m["supervision"] = s.supervisionJSON(c)
	return m
}

// approxBirthDate returns the birth date to expose and whether it is
// approximated. A real captured date_of_birth is used as-is; otherwise, when an
// age and its capture date are known, the birth date is approximated as 1
// January of the estimated birth year (decision D4). An unknown age yields no
// birth date at all rather than a fabricated one.
func approxBirthDate(c store.APICHW) (date string, approximate bool) {
	if c.DateOfBirth != nil {
		return c.DateOfBirth.Format("2006-01-02"), false
	}
	if c.AgeYears != nil && !c.AgeCapturedOn.IsZero() {
		year := c.AgeCapturedOn.Year() - int(*c.AgeYears)
		return strconv.Itoa(year) + "-01-01", true
	}
	return "", false
}

func (s *Server) supervisionJSON(c store.APICHW) map[string]any {
	out := map[string]any{
		"received": c.ReceivedSupervision,
		"overdue":  nil,
	}
	if c.LastSupervisedOn != nil {
		out["lastSupervisedOn"] = c.LastSupervisedOn.Format("2006-01-02")
	} else {
		out["lastSupervisedOn"] = nil
	}
	// Overdue is only meaningful once the Ministry confirms the interval (D13).
	if s.cfg.SupervisionIntervalDays > 0 {
		if c.LastSupervisedOn == nil {
			out["overdue"] = true
		} else {
			cutoff := time.Now().AddDate(0, 0, -s.cfg.SupervisionIntervalDays)
			out["overdue"] = c.LastSupervisedOn.Before(cutoff)
		}
		out["intervalDays"] = s.cfg.SupervisionIntervalDays
	}
	return out
}

// ---- helpers --------------------------------------------------------------

func (s *Server) apiNotFoundOrFail(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, domain.ErrNotFound) {
		writeAPIError(w, http.StatusNotFound, "no such CHW in scope")
		return
	}
	slog.Error("api "+what+" failed", "err", err)
	writeAPIError(w, http.StatusInternalServerError, "request failed")
}

func hwidParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("hwid"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}

func apiClientFrom(ctx context.Context) (domain.APIClient, bool) {
	c, ok := ctx.Value(apiClientKey).(domain.APIClient)
	return c, ok
}

func setAPIRows(ctx context.Context, n int) {
	if p, ok := ctx.Value(apiRowsKey).(*int); ok && p != nil {
		*p = n
	}
}

func effectiveLimit(n int) int {
	if n <= 0 || n > 1000 {
		return 50
	}
	return n
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("api encode failed", "err", err)
	}
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg, "status": status})
}
