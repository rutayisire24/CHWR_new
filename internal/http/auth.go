package http

import (
	"errors"
	"net/http"
	"time"

	"chwr/internal/auth"
	"chwr/internal/domain"
	"chwr/internal/store"
)

type loginPage struct {
	Email string
	Next  string
	Error string
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFrom(r.Context()); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", loginPage{Next: safeNext(r.URL.Query().Get("next"))})
}

// login verifies credentials and issues a session. Every failure — unknown
// email, wrong password, disabled account — produces the same message and
// roughly the same timing, so the form cannot be used to enumerate staff.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	email := trimmed(r, "email")
	password := r.PostForm.Get("password")
	next := safeNext(r.PostForm.Get("next"))

	reject := func() {
		s.render(w, r, http.StatusUnauthorized, "login", loginPage{
			Email: email,
			Next:  next,
			Error: "That email and password do not match an active account.",
		})
	}

	user, hash, err := s.store.Users.Credentials(r.Context(), email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			auth.BurnTime(password) // equal cost whether or not the account exists
			s.recordLoginFailure(r, email, nil)
			reject()
			return
		}
		s.fail(w, r, err)
		return
	}

	ok, err := auth.VerifyPassword(hash, password)
	if err != nil {
		// A hash this package did not write is a data problem, not a wrong
		// password. Log it as a failure and refuse; do not tell the user.
		s.fail(w, r, err)
		return
	}
	if !ok || !user.Active() {
		s.recordLoginFailure(r, email, &user.ID)
		reject()
		return
	}

	token, err := s.store.Sessions.Create(r.Context(), user.ID, s.clientIP(r), r.UserAgent())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	auth.SetSessionCookie(w, r, token, s.secure())
	auth.RotateCSRF(w, s.secure()) // a token captured pre-login must not survive it

	if err := s.store.Users.MarkLoggedIn(r.Context(), user.ID, time.Now()); err != nil {
		s.fail(w, r, err)
		return
	}
	entry := store.ActorFrom(user)
	entry.Action = store.ActionLogin
	entry.Entity = "user"
	entry.EntityID = &user.ID
	entry.IP = s.clientIP(r)
	s.audit(r, entry)

	if user.MustReset {
		http.Redirect(w, r, "/account/password", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil && c.Value != "" {
		if err := s.store.Sessions.Delete(r.Context(), auth.HashToken(c.Value)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if u, ok := auth.UserFrom(r.Context()); ok {
		entry := store.ActorFrom(u)
		entry.Action = store.ActionLogout
		entry.Entity = "user"
		entry.EntityID = &u.ID
		entry.IP = s.clientIP(r)
		s.audit(r, entry)
	}

	auth.ClearSessionCookie(w, r)
	auth.RotateCSRF(w, s.secure())
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

type passwordPage struct {
	Forced bool
	Errors map[string]string
}

func (s *Server) passwordForm(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	s.render(w, r, http.StatusOK, "password", passwordPage{
		Forced: u.MustReset,
		Errors: map[string]string{},
	})
}

// changePassword is the self-service path, and the only way past a forced
// first-login reset.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.MustUser(r.Context())
	current := r.PostForm.Get("current")
	next := r.PostForm.Get("new")
	confirm := r.PostForm.Get("confirm")

	v := domain.NewValidationError()

	// Re-verify the current password against a fresh read: the session alone
	// must not be enough to set a new password on a borrowed browser.
	_, hash, err := s.store.Users.Credentials(r.Context(), u.Email)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ok, err := auth.VerifyPassword(hash, current)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		v.Add("current", "That is not your current password.")
	}
	if reason := auth.CheckPassword(next); reason != "" {
		v.Add("new", reason)
	}
	if next != confirm {
		v.Add("confirm", "The two passwords do not match.")
	}
	if ok && next == current {
		v.Add("new", "Choose a password different from the current one.")
	}

	if v.Any() {
		s.render(w, r, http.StatusUnprocessableEntity, "password", passwordPage{
			Forced: u.MustReset,
			Errors: v.Fields,
		})
		return
	}

	var keep []byte
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		keep = auth.HashToken(c.Value)
	}
	if err := s.store.Users.SetPassword(r.Context(), auth.ScopeFrom(r.Context()), u.ID, next, keep); err != nil {
		s.fail(w, r, err)
		return
	}

	entry := store.ActorFrom(u)
	entry.Action = store.ActionPasswordChange
	entry.Entity = "user"
	entry.EntityID = &u.ID
	entry.IP = s.clientIP(r)
	s.audit(r, entry)

	setFlash(w, s.secure(), "ok", "Your password has been changed. Other sessions were signed out.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// recordLoginFailure notes a rejected attempt. userID is nil when the email
// matched no account; the email is still recorded, because a run of failures
// against a non-existent address is itself worth seeing.
func (s *Server) recordLoginFailure(r *http.Request, email string, userID *int64) {
	s.audit(r, store.Entry{
		ActorID:    userID,
		ActorEmail: email,
		Action:     store.ActionLoginFailed,
		Entity:     "user",
		EntityID:   userID,
		IP:         s.clientIP(r),
	})
}
