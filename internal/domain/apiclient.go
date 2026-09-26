package domain

import "time"

// APIClientScope is the reach an API consumer is provisioned with. It mirrors
// the user model's national/district split, so the same Scope machinery
// confines a district-scoped consumer exactly as it confines a district user.
type APIClientScope string

const (
	APIScopeNational APIClientScope = "national"
	APIScopeDistrict APIClientScope = "district"
)

// Valid reports whether s is one of the two members.
func (s APIClientScope) Valid() bool {
	return s == APIScopeNational || s == APIScopeDistrict
}

// APIClient is a machine consumer of the read-only interoperability API — the
// eCHIS user-management tool, or the National Data Warehouse. It authenticates
// with a client_id and secret to mint a short-lived bearer token; it reads the
// register and never writes it.
//
// It is deliberately separate from a system User: an API client holds no
// capability in the role matrix, cannot sign in to the web application, and its
// reads are logged in api_access_log rather than audit_log.
type APIClient struct {
	ID         int64
	Name       string
	ClientID   string
	Scope      APIClientScope
	DistrictID *int64
	Status     string // 'active' | 'disabled', the user_status enum
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Active reports whether the client may authenticate.
func (c APIClient) Active() bool { return c.Status == "active" }
