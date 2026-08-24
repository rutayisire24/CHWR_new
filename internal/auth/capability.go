package auth

import "chwr/internal/domain"

// Capability is a single permission checked by middleware before a handler
// runs. The set is closed and small; see docs/rbac.md for the matrix.
type Capability string

const (
	CapCHWView       Capability = "chw.view"
	CapCHWCreate     Capability = "chw.create"
	CapCHWUpdate     Capability = "chw.update"
	CapCHWDeactivate Capability = "chw.deactivate"
	CapUserManage    Capability = "user.manage"
	CapAuditView     Capability = "audit.view"
	CapExport        Capability = "export"
)

// matrix mirrors docs/rbac.md exactly. Presence means the role holds the
// capability; how far it reaches is the Scope's job, not this table's — a
// district_manager holds chw.create, and Scope confines it to their district.
var matrix = map[domain.Role]map[Capability]bool{
	domain.RoleNationalAdmin: {
		CapCHWView: true, CapCHWCreate: true, CapCHWUpdate: true,
		CapCHWDeactivate: true, CapUserManage: true, CapAuditView: true,
		CapExport: true,
	},
	domain.RoleNationalViewer: {
		CapCHWView: true, CapExport: true,
	},
	domain.RoleDistrictManager: {
		CapCHWView: true, CapCHWCreate: true, CapCHWUpdate: true,
		CapCHWDeactivate: true, CapAuditView: true, CapExport: true,
	},
	domain.RoleDistrictViewer: {
		CapCHWView: true, CapExport: true,
	},
}

// Can reports whether the role holds the capability. An unknown role holds
// nothing.
func Can(r domain.Role, c Capability) bool {
	return matrix[r][c]
}
