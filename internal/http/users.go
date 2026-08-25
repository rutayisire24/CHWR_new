package http

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"

	"chwr/internal/auth"
	"chwr/internal/domain"
	"chwr/internal/store"
)

type usersPage struct {
	Users []domain.User
}

type roleOption struct {
	Value    domain.Role
	Label    string
	Selected bool
}

type districtOption struct {
	ID       int64
	Name     string
	Selected bool
}

type userFormPage struct {
	Action    string
	User      domain.User
	Roles     []roleOption
	Districts []districtOption
	Suggested string
	Errors    map[string]string
}

func (s *Server) usersList(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.Users.List(r.Context(), auth.ScopeFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "users", usersPage{Users: users})
}

func (s *Server) userNew(w http.ResponseWriter, r *http.Request) {
	p, err := s.userForm(r, domain.User{}, "/users/new", nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "user_form", p)
}

func (s *Server) userCreate(w http.ResponseWriter, r *http.Request) {
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	in, v := decodeUser(r)
	password := trimmed(r, "password")
	if reason := auth.CheckPassword(password); reason != "" {
		v.Add("password", reason)
	}

	if v.Any() {
		s.rerenderUserForm(w, r, draftUser(in), "/users/new", v.Fields, password)
		return
	}

	in.CreatedBy = &actor.ID
	in.Password = password

	created, err := s.store.Users.Create(r.Context(), sc, in)
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			v.Add("email", "An account with that email already exists.")
			s.rerenderUserForm(w, r, draftUser(in), "/users/new", v.Fields, password)
			return
		}
		s.fail(w, r, err)
		return
	}

	entry := store.ActorFrom(actor)
	entry.Action = store.ActionUserCreate
	entry.Entity = "user"
	entry.EntityID = &created.ID
	entry.After = auditUser(created)
	entry.IP = s.clientIP(r)
	s.audit(r, entry)

	setFlash(w, s.secure(), "ok", created.Email+" can now sign in with the temporary password. They must change it immediately.")
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) userEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	user, err := s.store.Users.Get(r.Context(), auth.ScopeFrom(r.Context()), id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	p, err := s.userForm(r, user, userPath(id), nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "user_form", p)
}

func (s *Server) userUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	before, err := s.store.Users.Get(r.Context(), sc, id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	in, v := decodeUser(r)
	// The last active national admin must keep the role, or nobody can
	// provision accounts and the registry needs a database console to recover.
	if before.Role == domain.RoleNationalAdmin && in.Role != domain.RoleNationalAdmin {
		if last, err := s.lastAdmin(r, before); err != nil {
			s.fail(w, r, err)
			return
		} else if last {
			v.Add("role", "This is the only active national administrator. Promote another account first.")
		}
	}
	if v.Any() {
		draft := draftUser(in)
		draft.ID = id
		draft.Email = before.Email
		s.rerenderUserForm(w, r, draft, userPath(id), v.Fields, "")
		return
	}

	after, err := s.store.Users.Update(r.Context(), sc, id, in.FullName, in.Role, in.DistrictID)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	entry := store.ActorFrom(actor)
	entry.Action = store.ActionUserUpdate
	entry.Entity = "user"
	entry.EntityID = &after.ID
	entry.Before = auditUser(before)
	entry.After = auditUser(after)
	entry.IP = s.clientIP(r)
	s.audit(r, entry)

	setFlash(w, s.secure(), "ok", "Saved changes to "+after.Email+".")
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) userStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	status := domain.UserStatus(trimmed(r, "status"))
	if status != domain.UserActive && status != domain.UserDisabled {
		s.notFound(w, r)
		return
	}

	before, err := s.store.Users.Get(r.Context(), sc, id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	if status == domain.UserDisabled {
		if before.ID == actor.ID {
			setFlash(w, s.secure(), "error", "You cannot disable your own account.")
			http.Redirect(w, r, userPath(id), http.StatusSeeOther)
			return
		}
		if last, err := s.lastAdmin(r, before); err != nil {
			s.fail(w, r, err)
			return
		} else if last {
			setFlash(w, s.secure(), "error", "This is the only active national administrator and cannot be disabled.")
			http.Redirect(w, r, userPath(id), http.StatusSeeOther)
			return
		}
	}

	after, err := s.store.Users.SetStatus(r.Context(), sc, id, status)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	entry := store.ActorFrom(actor)
	entry.Action = store.ActionUserStatus
	entry.Entity = "user"
	entry.EntityID = &after.ID
	entry.Before = auditUser(before)
	entry.After = auditUser(after)
	entry.IP = s.clientIP(r)
	s.audit(r, entry)

	msg := after.Email + " is active again."
	if status == domain.UserDisabled {
		msg = after.Email + " is disabled. Every session they held has been ended."
	}
	setFlash(w, s.secure(), "ok", msg)
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

func (s *Server) userResetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	password := trimmed(r, "password")
	if reason := auth.CheckPassword(password); reason != "" {
		setFlash(w, s.secure(), "error", reason)
		http.Redirect(w, r, userPath(id), http.StatusSeeOther)
		return
	}

	target, err := s.store.Users.Get(r.Context(), sc, id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	if err := s.store.Users.ResetPassword(r.Context(), sc, id, password); err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	entry := store.ActorFrom(actor)
	entry.Action = store.ActionUserReset
	entry.Entity = "user"
	entry.EntityID = &target.ID
	entry.IP = s.clientIP(r)
	s.audit(r, entry)

	setFlash(w, s.secure(), "ok", "Temporary password set for "+target.Email+". They must change it at next sign-in.")
	http.Redirect(w, r, "/users", http.StatusSeeOther)
}

// decodeUser reads the shared user fields and validates them. The schema
// enforces the role/district agreement too — users_scope_matches_role makes a
// district user without a district unrepresentable — but a CHECK violation is
// a 500, and this turns it into a field message.
func decodeUser(r *http.Request) (store.NewUser, *domain.ValidationError) {
	v := domain.NewValidationError()

	in := store.NewUser{
		Email:    trimmed(r, "email"),
		FullName: trimmed(r, "full_name"),
		Role:     domain.Role(trimmed(r, "role")),
	}

	if in.FullName == "" {
		v.Add("full_name", "Enter the person's full name.")
	}
	if in.Email == "" {
		v.Add("email", "Enter an email address.")
	} else if _, err := mail.ParseAddress(in.Email); err != nil {
		v.Add("email", "That does not look like an email address.")
	}
	if !in.Role.Valid() {
		v.Add("role", "Choose a role.")
	}

	districtID, wellFormed := optionalID(r, "district_id")
	switch {
	case !wellFormed:
		v.Add("district_id", "Choose a district from the list.")
	case in.Role.District() && districtID == nil:
		v.Add("district_id", "District roles must be tied to a district.")
	case in.Role.Valid() && !in.Role.District() && districtID != nil:
		v.Add("district_id", "National roles cannot be tied to a district.")
	}
	in.DistrictID = districtID

	return in, v
}

// userForm assembles the selects. Districts come back scoped, so a district
// admin — should the capability ever be granted — could not assign outward.
func (s *Server) userForm(r *http.Request, u domain.User, action string, errs map[string]string) (userFormPage, error) {
	districts, err := s.store.Locations.Districts(r.Context(), auth.ScopeFrom(r.Context()))
	if err != nil {
		return userFormPage{}, err
	}

	roles := make([]roleOption, 0, len(domain.Roles))
	for _, role := range domain.Roles {
		roles = append(roles, roleOption{Value: role, Label: role.Label(), Selected: role == u.Role})
	}
	opts := make([]districtOption, 0, len(districts))
	for _, d := range districts {
		opts = append(opts, districtOption{
			ID: d.ID, Name: d.Name,
			Selected: u.DistrictID != nil && *u.DistrictID == d.ID,
		})
	}
	if errs == nil {
		errs = map[string]string{}
	}

	suggested, err := suggestPassword()
	if err != nil {
		return userFormPage{}, err
	}

	return userFormPage{
		Action:    action,
		User:      u,
		Roles:     roles,
		Districts: opts,
		Suggested: suggested,
		Errors:    errs,
	}, nil
}

func (s *Server) rerenderUserForm(w http.ResponseWriter, r *http.Request, u domain.User, action string, errs map[string]string, password string) {
	p, err := s.userForm(r, u, action, errs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if password != "" {
		p.Suggested = password // keep what the admin typed rather than swapping it out
	}
	s.render(w, r, http.StatusUnprocessableEntity, "user_form", p)
}

// draftUser turns rejected form input back into a User so the form redisplays
// what was typed instead of clearing it.
func draftUser(in store.NewUser) domain.User {
	return domain.User{
		Email:      in.Email,
		FullName:   in.FullName,
		Role:       in.Role,
		DistrictID: in.DistrictID,
		Status:     domain.UserActive,
	}
}

// auditUser is the JSONB shape written to audit_log. The password hash is not
// in it, and never will be.
func auditUser(u domain.User) map[string]any {
	return map[string]any{
		"id":          u.ID,
		"email":       u.Email,
		"full_name":   u.FullName,
		"role":        string(u.Role),
		"district_id": u.DistrictID,
		"status":      string(u.Status),
		"must_reset":  u.MustReset,
	}
}

func (s *Server) lastAdmin(r *http.Request, u domain.User) (bool, error) {
	if u.Role != domain.RoleNationalAdmin || !u.Active() {
		return false, nil
	}
	n, err := s.store.Users.CountAdmins(r.Context())
	if err != nil {
		return false, err
	}
	return n <= 1, nil
}

func (s *Server) notFoundOrFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if errors.Is(err, domain.ErrForbidden) {
		s.forbidden(w, r)
		return
	}
	s.fail(w, r, err)
}

func userPath(id int64) string {
	return "/users/" + strconv.FormatInt(id, 10)
}

// suggestPassword offers a password the admin can hand over verbatim, so the
// path of least resistance is a strong one rather than a memorable one. It is
// re-drawn until it satisfies the same policy the form enforces — base64 of 12
// random bytes lands on letters only often enough to matter.
func suggestPassword() (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		raw := make([]byte, 12)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("suggest password: %w", err)
		}
		candidate := base64.RawURLEncoding.EncodeToString(raw)
		if auth.CheckPassword(candidate) == "" {
			return candidate, nil
		}
	}
	return "", errors.New("suggest password: no acceptable candidate in 8 draws")
}
