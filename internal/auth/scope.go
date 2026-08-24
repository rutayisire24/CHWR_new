// Package auth carries authentication and authorization: password hashing,
// session tokens, the capability matrix, and the middleware that enforces both.
//
// It deliberately does not import internal/store — the dependency runs the
// other way, because every store method takes a Scope.
package auth

import (
	"strconv"

	"chwr/internal/domain"
)

// Scope is the data boundary a request may touch. It is a required argument to
// every store method, so a handler cannot forget to apply it: the call does not
// compile without one. Middleware alone would not be enough, because a missing
// middleware check fails open while a missing argument fails to build.
//
// The zero Scope is national — but a zero Scope is never produced by this
// package: ScopeFor derives it from the user's role, and the schema guarantees
// role and district agree.
type Scope struct {
	districtID *int64
}

// National is the unrestricted scope, for national roles and for internal
// callers such as the importer and the seeder.
func National() Scope { return Scope{} }

// District restricts the scope to a single district.
func District(id int64) Scope { return Scope{districtID: &id} }

// ScopeFor derives the scope from a user's role. district roles always carry a
// district_id (users_scope_matches_role), so the nil case here is unreachable
// for a stored user; it degrades to the safest option rather than panicking.
func ScopeFor(u domain.User) Scope {
	if u.Role.District() {
		if u.DistrictID == nil {
			return District(0) // matches nothing; a broken row must not read the country
		}
		return District(*u.DistrictID)
	}
	return National()
}

// IsNational reports whether the scope spans the whole country.
func (s Scope) IsNational() bool { return s.districtID == nil }

// DistrictID returns the district the scope is pinned to, and whether it is
// pinned at all.
func (s Scope) DistrictID() (int64, bool) {
	if s.districtID == nil {
		return 0, false
	}
	return *s.districtID, true
}

// Allows reports whether a row belonging to districtID is inside the scope.
// Used for row-level checks after a fetch; the WHERE clause is still the
// primary defence.
func (s Scope) Allows(districtID int64) bool {
	if s.districtID == nil {
		return true
	}
	return *s.districtID == districtID
}

// Filter returns a SQL fragment and its argument for appending to a WHERE
// clause. col is the qualified district column, n the next placeholder number.
// A national scope contributes nothing.
//
//	frag, arg := sc.Filter("c.district_id", len(args)+1)
func (s Scope) Filter(col string, n int) (string, []any) {
	if s.districtID == nil {
		return "", nil
	}
	return " AND " + col + " = $" + strconv.Itoa(n), []any{*s.districtID}
}
