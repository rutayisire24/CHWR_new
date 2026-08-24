package auth

import (
	"testing"

	"chwr/internal/domain"
)

func TestScopeForRole(t *testing.T) {
	district := int64(72)

	national := ScopeFor(domain.User{Role: domain.RoleNationalAdmin})
	if !national.IsNational() {
		t.Fatal("national admin should get a national scope")
	}

	scoped := ScopeFor(domain.User{Role: domain.RoleDistrictManager, DistrictID: &district})
	id, ok := scoped.DistrictID()
	if !ok || id != district {
		t.Fatalf("district manager scope = (%d, %v), want (72, true)", id, ok)
	}

	// A district role whose district_id is missing cannot happen in the
	// database — users_scope_matches_role forbids it — but if it ever did,
	// the scope must match nothing rather than the whole country.
	broken := ScopeFor(domain.User{Role: domain.RoleDistrictViewer})
	if broken.IsNational() {
		t.Fatal("a district role without a district must not widen to national")
	}
	if broken.Allows(district) {
		t.Fatal("a broken district scope must allow no district")
	}
}

func TestScopeFilter(t *testing.T) {
	frag, args := National().Filter("c.district_id", 3)
	if frag != "" || args != nil {
		t.Fatalf("national scope contributed %q %v, want nothing", frag, args)
	}

	frag, args = District(72).Filter("c.district_id", 3)
	if frag != " AND c.district_id = $3" {
		t.Fatalf("fragment = %q", frag)
	}
	if len(args) != 1 || args[0].(int64) != 72 {
		t.Fatalf("args = %v, want [72]", args)
	}

	// Placeholder numbers reach two digits once a query has ten arguments.
	if frag, _ := District(1).Filter("d", 12); frag != " AND d = $12" {
		t.Fatalf("two-digit placeholder = %q", frag)
	}
}

func TestScopeAllows(t *testing.T) {
	if !National().Allows(999) {
		t.Fatal("national scope should allow any district")
	}
	if District(72).Allows(73) {
		t.Fatal("district scope allowed a foreign district")
	}
	if !District(72).Allows(72) {
		t.Fatal("district scope refused its own district")
	}
}

// The matrix in docs/rbac.md is the contract; this pins it.
func TestCapabilityMatrix(t *testing.T) {
	cases := []struct {
		role domain.Role
		cap  Capability
		want bool
	}{
		{domain.RoleNationalAdmin, CapUserManage, true},
		{domain.RoleNationalViewer, CapUserManage, false},
		{domain.RoleDistrictManager, CapUserManage, false},
		{domain.RoleDistrictViewer, CapUserManage, false},

		{domain.RoleNationalAdmin, CapCHWCreate, true},
		{domain.RoleNationalViewer, CapCHWCreate, false},
		{domain.RoleDistrictManager, CapCHWCreate, true},
		{domain.RoleDistrictViewer, CapCHWCreate, false},

		{domain.RoleNationalViewer, CapCHWView, true},
		{domain.RoleDistrictViewer, CapCHWView, true},
		{domain.RoleDistrictViewer, CapExport, true},
		{domain.RoleDistrictViewer, CapCHWDeactivate, false},

		{domain.RoleNationalAdmin, CapAuditView, true},
		{domain.RoleNationalViewer, CapAuditView, false},
		{domain.RoleDistrictManager, CapAuditView, true},

		{domain.Role("root"), CapCHWView, false},
	}

	for _, c := range cases {
		if got := Can(c.role, c.cap); got != c.want {
			t.Errorf("Can(%s, %s) = %v, want %v", c.role, c.cap, got, c.want)
		}
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	const pw = "Kampala-Registry-2026"

	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	ok, err := VerifyPassword(hash, pw)
	if err != nil || !ok {
		t.Fatalf("verify correct password = (%v, %v), want (true, nil)", ok, err)
	}

	ok, err = VerifyPassword(hash, pw+"x")
	if err != nil || ok {
		t.Fatalf("verify wrong password = (%v, %v), want (false, nil)", ok, err)
	}

	// Two hashes of the same password differ: the salt is per-hash.
	other, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("hash again: %v", err)
	}
	if other == hash {
		t.Fatal("two hashes of the same password are identical — salt is not random")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-hash",
		"$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$a2V5",  // wrong variant
		"$argon2id$v=16$m=65536,t=3,p=4$c2FsdA$a2V5", // wrong version
		"$argon2id$v=19$m=65536,t=3,p=4$!!!!$a2V5",   // salt not base64
	} {
		if _, err := VerifyPassword(bad, "whatever"); err == nil {
			t.Errorf("VerifyPassword(%q) returned no error", bad)
		}
	}
}

func TestCheckPassword(t *testing.T) {
	cases := map[string]bool{
		"Kampala-Registry-2026": true,
		"correct horse 42":      true,
		"short1!":               false, // under twelve
		"allletterspassword":    false, // no digit or symbol
		"            ":          false, // whitespace only
	}
	for pw, wantOK := range cases {
		if gotOK := CheckPassword(pw) == ""; gotOK != wantOK {
			t.Errorf("CheckPassword(%q) accepted = %v, want %v", pw, gotOK, wantOK)
		}
	}
}

func TestSessionTokenIsHashedNotStored(t *testing.T) {
	token, hash, err := NewSessionToken()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	if len(hash) != 32 {
		t.Fatalf("hash length = %d, want 32", len(hash))
	}
	if token == string(hash) {
		t.Fatal("the stored hash is the raw token")
	}
	if got := HashToken(token); string(got) != string(hash) {
		t.Fatal("HashToken disagrees with NewSessionToken")
	}

	second, _, err := NewSessionToken()
	if err != nil {
		t.Fatalf("second token: %v", err)
	}
	if second == token {
		t.Fatal("two session tokens collided")
	}
}
