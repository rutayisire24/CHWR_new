package auth

import "chwr/internal/domain"

// Capability is a single permission checked by middleware before a handler
// runs. The set is closed and small; see docs/rbac.md for the matrix.
type Capability string

const (
	CapWorkerView       Capability = "health_worker.view"
	CapWorkerCreate     Capability = "health_worker.create"
	CapWorkerUpdate     Capability = "health_worker.update"
	CapWorkerDeactivate Capability = "health_worker.deactivate"
	CapImport           Capability = "health_worker.import"
	CapUserManage       Capability = "user.manage"
	CapAuditView        Capability = "audit.view"
	CapExport           Capability = "export"
)

// matrix mirrors docs/rbac.md exactly. Presence means the role holds the
// capability; how far it reaches is the Scope's job, not this table's — a
// district_manager holds health_worker.create, and Scope confines it to their
// district.
var matrix = map[domain.Role]map[Capability]bool{
	domain.RoleNationalAdmin: {
		CapWorkerView: true, CapWorkerCreate: true, CapWorkerUpdate: true,
		CapWorkerDeactivate: true, CapUserManage: true, CapAuditView: true,
		CapExport: true, CapImport: true,
	},
	domain.RoleNationalViewer: {
		CapWorkerView: true, CapExport: true,
	},
	domain.RoleDistrictManager: {
		CapWorkerView: true, CapWorkerCreate: true, CapWorkerUpdate: true,
		CapWorkerDeactivate: true, CapAuditView: true, CapExport: true,
		CapImport: true,
	},
	domain.RoleDistrictViewer: {
		CapWorkerView: true, CapExport: true,
	},
}

// Can reports whether the role holds the capability. An unknown role holds
// nothing.
func Can(r domain.Role, c Capability) bool {
	return matrix[r][c]
}
