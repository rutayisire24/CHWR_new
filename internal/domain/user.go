// Package domain holds the registry's entities. Nothing here does I/O: no
// database handles, no HTTP, no templates.
package domain

import "time"

// Role is the `user_role` enum. The four roles are a 2x2 of capability
// (manage / view) by scope (national / one district).
type Role string

const (
	RoleNationalAdmin   Role = "national_admin"
	RoleNationalViewer  Role = "national_viewer"
	RoleDistrictManager Role = "district_manager"
	RoleDistrictViewer  Role = "district_viewer"
)

// Roles lists every role in matrix order; used to validate form input and to
// populate the role select.
var Roles = []Role{RoleNationalAdmin, RoleNationalViewer, RoleDistrictManager, RoleDistrictViewer}

// Valid reports whether r is one of the four enum members.
func (r Role) Valid() bool {
	for _, known := range Roles {
		if r == known {
			return true
		}
	}
	return false
}

// District reports whether the role is scoped to a single district. The schema
// enforces the matching half of this: users_scope_matches_role makes a district
// role without a district — or a national role with one — unrepresentable.
func (r Role) District() bool {
	return r == RoleDistrictManager || r == RoleDistrictViewer
}

// Label is the human-readable role name for templates.
func (r Role) Label() string {
	switch r {
	case RoleNationalAdmin:
		return "National administrator"
	case RoleNationalViewer:
		return "National viewer"
	case RoleDistrictManager:
		return "District manager"
	case RoleDistrictViewer:
		return "District viewer"
	}
	return string(r)
}

// UserStatus is the `user_status` enum. Users are disabled, never deleted: the
// audit trail references them.
type UserStatus string

const (
	UserActive   UserStatus = "active"
	UserDisabled UserStatus = "disabled"
)

// User is a row of `users` minus the password hash, which never leaves the
// store layer.
type User struct {
	ID           int64
	Email        string
	FullName     string
	MustReset    bool
	Role         Role
	DistrictID   *int64
	DistrictName string // joined from locations; empty for national roles
	Status       UserStatus
	LastLoginAt  *time.Time
	CreatedBy    *int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Active reports whether the account may still authenticate.
func (u User) Active() bool { return u.Status == UserActive }
