package domain

import "time"

// Sex is the `sex` enum. The source form offers exactly these two.
type Sex string

const (
	SexMale   Sex = "male"
	SexFemale Sex = "female"
)

// Sexes lists both members.
var Sexes = []Sex{SexFemale, SexMale}

// Valid reports whether s is one of the two enum members.
func (s Sex) Valid() bool { return s == SexMale || s == SexFemale }

// Label is the human-readable value.
func (s Sex) Label() string {
	switch s {
	case SexMale:
		return "Male"
	case SexFemale:
		return "Female"
	}
	return string(s)
}

// WorkerStatus is the `worker_status` enum. Health workers are never deleted:
// deactivation sets the status, the timestamp and a reason together, and
// health_workers_deactivation_complete keeps status and timestamp consistent.
// A worker with an active deployment cannot be deactivated — the posting ends
// first, in the same transaction.
type WorkerStatus string

const (
	WorkerActive   WorkerStatus = "active"
	WorkerInactive WorkerStatus = "inactive"
)

// HealthWorker is the person — `health_workers`, without the optional survey
// attributes that live on a category profile, and without the posting that
// lives on deployments. What a worker does and where they do it is a
// Deployment; who they are survives every transfer.
type HealthWorker struct {
	ID        int64
	NIN       string // empty when not recorded; unique where present
	FirstName string
	LastName  string
	Sex       Sex
	AgeYears  *int16
	// AgeCapturedOn is when the age was true. Age is a snapshot, not a fact.
	AgeCapturedOn time.Time

	// DistrictID is the district that owns this record for scoping. It is
	// derived by trigger from the worker's deployments and never supplied; it
	// tracks the latest posting and survives deactivation. Nil only inside the
	// transaction that creates the worker, before their first deployment.
	DistrictID *int64

	// Deployment is the worker's current posting, joined for display. The
	// register's listing, scoping and placement all read through it.
	Deployment *Deployment

	Status             WorkerStatus
	DeactivatedAt      *time.Time
	DeactivationReason string

	CreatedBy *int64
	UpdatedBy *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// FullName is "first last", the form the register displays and searches.
func (w HealthWorker) FullName() string { return w.FirstName + " " + w.LastName }

// Active reports whether the worker is currently in the workforce.
func (w HealthWorker) Active() bool { return w.Status == WorkerActive }

// Level is the `location_level` enum.
type Level string

const (
	LevelRegion    Level = "region"
	LevelDistrict  Level = "district"
	LevelCounty    Level = "county"
	LevelSubcounty Level = "subcounty"
	LevelParish    Level = "parish"
	LevelVillage   Level = "village"
)

// Label is the human-readable level name.
func (l Level) Label() string {
	switch l {
	case LevelSubcounty:
		return "Subcounty"
	case LevelParish:
		return "Parish"
	case LevelVillage:
		return "Village"
	case LevelCounty:
		return "County"
	case LevelDistrict:
		return "District"
	case LevelRegion:
		return "Region"
	}
	return string(l)
}

// Place is one rung of a location's ancestor chain.
type Place struct {
	ID    int64
	Level Level
	Name  string
	// Code is the official segment code, empty for a region. Identity in
	// locations is (parent_id, code) and never name, so this is what an
	// operator puts in an import's location_code column to settle a name two
	// siblings share.
	Code string
}
