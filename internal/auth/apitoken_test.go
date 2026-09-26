package auth

import (
	"testing"

	"chwr/internal/domain"
)

// An API client reads through the same Scope machinery as a user, so a national
// client sees the country and a district client is confined to its district. A
// district client with no district — which the schema forbids — must degrade to
// matching nothing rather than reading the whole register.
func TestScopeForClient(t *testing.T) {
	nat := ScopeForClient(domain.APIClient{Scope: domain.APIScopeNational})
	if !nat.IsNational() {
		t.Error("a national client should get a national scope")
	}

	d := int64(58)
	ds := ScopeForClient(domain.APIClient{Scope: domain.APIScopeDistrict, DistrictID: &d})
	if id, ok := ds.DistrictID(); !ok || id != 58 {
		t.Errorf("district client scope = (%d,%v), want (58,true)", id, ok)
	}

	broken := ScopeForClient(domain.APIClient{Scope: domain.APIScopeDistrict})
	if id, ok := broken.DistrictID(); !ok || id != 0 {
		t.Errorf("a district client with no district should be District(0), got (%d,%v)", id, ok)
	}
}
