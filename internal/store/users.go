package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Users reads and writes system accounts. Provisioning is national_admin only,
// so the scope argument on these methods is national in practice; it is still
// required, and still applied, so no future caller can widen a district user's
// view of the account list by accident.
type Users struct {
	pool *pgxpool.Pool
}

// userColumns is the projection every user query shares. citext and the enums
// are cast to text so pgx needs no type registration for them.
const userColumns = `
    u.id, u.email::text, u.full_name, u.must_reset, u.role::text,
    u.district_id, coalesce(d.name, ''), u.status::text,
    u.last_login_at, u.created_by, u.created_at, u.updated_at`

func scanUser(row pgx.Row) (domain.User, error) {
	var u domain.User
	var role, status string
	err := row.Scan(&u.ID, &u.Email, &u.FullName, &u.MustReset, &role,
		&u.DistrictID, &u.DistrictName, &status,
		&u.LastLoginAt, &u.CreatedBy, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return domain.User{}, err
	}
	u.Role = domain.Role(role)
	u.Status = domain.UserStatus(status)
	return u, nil
}

// Credentials looks an account up by email and returns it with its password
// hash.
//
// This is the one store method that takes no Scope: it runs before the request
// has a principal, and a login attempt must be able to find any account. It is
// for the login handler only — it returns the hash, so nothing else should
// call it. Disabled accounts are returned rather than hidden; the caller still
// verifies the password before reporting failure, so a disabled account and a
// wrong password cost the same and look the same.
func (s *Users) Credentials(ctx context.Context, email string) (domain.User, string, error) {
	const q = `SELECT ` + userColumns + `, u.password_hash
	           FROM users u LEFT JOIN locations d ON d.id = u.district_id
	           WHERE u.email = $1::citext`

	row := s.pool.QueryRow(ctx, q, email)

	var u domain.User
	var role, status, hash string
	err := row.Scan(&u.ID, &u.Email, &u.FullName, &u.MustReset, &role,
		&u.DistrictID, &u.DistrictName, &status,
		&u.LastLoginAt, &u.CreatedBy, &u.CreatedAt, &u.UpdatedAt, &hash)
	if err != nil {
		return domain.User{}, "", fmt.Errorf("load credentials: %w", translate(err))
	}
	u.Role = domain.Role(role)
	u.Status = domain.UserStatus(status)
	return u, hash, nil
}

// Get returns one account inside the scope. A district user asking for an
// account outside their district gets ErrNotFound, not ErrForbidden: existence
// itself is scoped information.
func (s *Users) Get(ctx context.Context, sc auth.Scope, id int64) (domain.User, error) {
	q := `SELECT ` + userColumns + `
	      FROM users u LEFT JOIN locations d ON d.id = u.district_id
	      WHERE u.id = $1`
	args := []any{id}

	if frag, extra := sc.Filter("u.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	u, err := scanUser(s.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.User{}, fmt.Errorf("get user %d: %w", id, translate(err))
	}
	return u, nil
}

// List returns every account inside the scope, newest first.
func (s *Users) List(ctx context.Context, sc auth.Scope) ([]domain.User, error) {
	q := `SELECT ` + userColumns + `
	      FROM users u LEFT JOIN locations d ON d.id = u.district_id
	      WHERE true`
	var args []any

	if frag, extra := sc.Filter("u.district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += ` ORDER BY u.status, u.full_name`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", translate(err))
	}
	defer rows.Close()

	var out []domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// NewUser is the input to Create. district_id must be present for district
// roles and absent for national ones; users_scope_matches_role rejects
// anything else, so the check here is a courtesy, not the guarantee.
type NewUser struct {
	Email      string
	FullName   string
	Role       domain.Role
	DistrictID *int64
	Password   string // plaintext; hashed here, never stored or logged
	CreatedBy  *int64
}

// Create provisions an account with must_reset set: the password an admin
// types is a handover token, not the user's password.
func (s *Users) Create(ctx context.Context, sc auth.Scope, in NewUser) (domain.User, error) {
	if in.DistrictID != nil && !sc.Allows(*in.DistrictID) {
		return domain.User{}, fmt.Errorf("create user: %w", domain.ErrForbidden)
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}

	const q = `
	    WITH inserted AS (
	        INSERT INTO users (email, full_name, password_hash, role, district_id, created_by)
	        VALUES ($1::citext, $2, $3, $4::user_role, $5, $6)
	        RETURNING *
	    )
	    SELECT ` + userColumns + `
	    FROM inserted u LEFT JOIN locations d ON d.id = u.district_id`

	u, err := scanUser(s.pool.QueryRow(ctx, q,
		in.Email, in.FullName, hash, string(in.Role), in.DistrictID, in.CreatedBy))
	if err != nil {
		return domain.User{}, fmt.Errorf("create user %q: %w", in.Email, translate(err))
	}
	return u, nil
}

// Update changes the mutable account fields. Password and status have their
// own methods, because both carry consequences a generic update would hide.
func (s *Users) Update(ctx context.Context, sc auth.Scope, id int64, fullName string, role domain.Role, districtID *int64) (domain.User, error) {
	if districtID != nil && !sc.Allows(*districtID) {
		return domain.User{}, fmt.Errorf("update user %d: %w", id, domain.ErrForbidden)
	}

	q := `
	    WITH updated AS (
	        UPDATE users SET full_name = $2, role = $3::user_role, district_id = $4,
	                         updated_at = now()
	        WHERE id = $1`
	args := []any{id, fullName, string(role), districtID}

	if frag, extra := sc.Filter("district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += `
	        RETURNING *
	    )
	    SELECT ` + userColumns + `
	    FROM updated u LEFT JOIN locations d ON d.id = u.district_id`

	u, err := scanUser(s.pool.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.User{}, fmt.Errorf("update user %d: %w", id, translate(err))
	}
	return u, nil
}

// SetStatus enables or disables an account. Disabling also drops every session
// the user holds, in one transaction: an account disabled at 09:00 must not
// still be browsing at 09:01.
func (s *Users) SetStatus(ctx context.Context, sc auth.Scope, id int64, status domain.UserStatus) (domain.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("set status: %w", err)
	}
	defer tx.Rollback(ctx)

	q := `
	    WITH updated AS (
	        UPDATE users SET status = $2::user_status, updated_at = now()
	        WHERE id = $1`
	args := []any{id, string(status)}

	if frag, extra := sc.Filter("district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += `
	        RETURNING *
	    )
	    SELECT ` + userColumns + `
	    FROM updated u LEFT JOIN locations d ON d.id = u.district_id`

	u, err := scanUser(tx.QueryRow(ctx, q, args...))
	if err != nil {
		return domain.User{}, fmt.Errorf("set status on user %d: %w", id, translate(err))
	}

	if status == domain.UserDisabled {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			return domain.User{}, fmt.Errorf("revoke sessions for user %d: %w", id, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("set status: %w", err)
	}
	return u, nil
}

// SetPassword replaces the hash and clears must_reset. Every other session the
// user holds is dropped, keeping the current one: a password change is how a
// user responds to a suspected compromise, so it has to evict the intruder.
func (s *Users) SetPassword(ctx context.Context, sc auth.Scope, id int64, plaintext string, keepTokenHash []byte) error {
	hash, err := auth.HashPassword(plaintext)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	defer tx.Rollback(ctx)

	q := `UPDATE users SET password_hash = $2, must_reset = false, updated_at = now()
	      WHERE id = $1`
	args := []any{id, hash}
	if frag, extra := sc.Filter("district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	tag, err := tx.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("set password for user %d: %w", id, translate(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set password for user %d: %w", id, domain.ErrNotFound)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, id, keepTokenHash); err != nil {
		return fmt.Errorf("revoke other sessions for user %d: %w", id, err)
	}
	return tx.Commit(ctx)
}

// ResetPassword is the admin path: it sets a handover password and turns
// must_reset back on, so the user must choose their own at next login. Every
// session the user holds is dropped.
func (s *Users) ResetPassword(ctx context.Context, sc auth.Scope, id int64, plaintext string) error {
	hash, err := auth.HashPassword(plaintext)
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	defer tx.Rollback(ctx)

	q := `UPDATE users SET password_hash = $2, must_reset = true, updated_at = now()
	      WHERE id = $1`
	args := []any{id, hash}
	if frag, extra := sc.Filter("district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}

	tag, err := tx.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("reset password for user %d: %w", id, translate(err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("reset password for user %d: %w", id, domain.ErrNotFound)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("revoke sessions for user %d: %w", id, err)
	}
	return tx.Commit(ctx)
}

// MarkLoggedIn stamps last_login_at. Scope-free by the same argument as
// Credentials: it runs as part of authenticating the user it stamps.
func (s *Users) MarkLoggedIn(ctx context.Context, id int64, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1`, id, at)
	if err != nil {
		return fmt.Errorf("stamp login for user %d: %w", id, err)
	}
	return nil
}

// CountAdmins returns how many active national admins exist. The user handlers
// use it to refuse the last one's demotion or disablement, which would leave
// the registry with nobody able to provision accounts.
func (s *Users) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE role = 'national_admin' AND status = 'active'`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}
