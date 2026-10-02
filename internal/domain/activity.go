package domain

import "time"

// Dated events against a health worker's posting (0005): the services they
// report having given, and the tools handed out to them. Each is checked
// against the posting held on its date, so a cadre's applicable services and
// tools are the ones in force then.

// ServiceUpdate is one worker's report, for one date, of the services given.
type ServiceUpdate struct {
	ID             int64
	HealthWorkerID int64
	ReportingDate  time.Time
	DistrictID     int64
	CadreLabel     string // the cadre held on the date
	Services       []Service
	CreatedBy      *int64
	CreatedOn      time.Time
}

// ToolDistribution is one hand-out in one district on one date.
type ToolDistribution struct {
	ID            int64
	DistrictID    int64
	DistrictName  string
	ReportingDate time.Time
	Note          string
	Items         []DistributionItem
	// Recipients and Units summarise Items for a listing that does not load them.
	Recipients int64
	Units      int64
	CreatedBy  *int64
	CreatedOn  time.Time
}

// DistributionItem is one tool given to one worker in a distribution.
type DistributionItem struct {
	HealthWorkerID int64
	WorkerCode     string
	WorkerName     string
	Tool           Tool
	Quantity       int16
}

// ToolReceipt is a tool a worker received, and when.
type ToolReceipt struct {
	DistributionID int64
	ReportingDate  time.Time
	Tool           Tool
	Quantity       int16
}
