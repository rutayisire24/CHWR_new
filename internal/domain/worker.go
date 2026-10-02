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

// PersonStatus is the `person_status` enum: the record's own state. It is not
// the workforce status — a health worker leaves the workforce (WorkerStatus);
// a person dies, or turns out to be another record and is merged into it.
type PersonStatus string

const (
	PersonActive   PersonStatus = "active"
	PersonDeceased PersonStatus = "deceased"
	PersonMerged   PersonStatus = "merged"
)

// Person is `persons`: who someone is, independent of the work they do. Their
// contacts, documents, education, languages and history hang off it.
type Person struct {
	ID        int64
	NIN       string // empty when not recorded; unique where present
	FirstName string
	LastName  string
	OtherName string
	Sex       Sex
	// DOB is the birth date, or the estimate an age implies when DOBEstimated
	// is set. The field forms collected an age, which is a snapshot; a date is
	// not, and the age is computed from it rather than stored going stale.
	DOB          *time.Time
	DOBEstimated bool
	Status       PersonStatus
}

// AgeOn is the person's age in whole years on a date, nil without a birth date.
func (p Person) AgeOn(on time.Time) *int {
	if p.DOB == nil {
		return nil
	}
	years := on.Year() - p.DOB.Year()
	if on.Month() < p.DOB.Month() || (on.Month() == p.DOB.Month() && on.Day() < p.DOB.Day()) {
		years-- // the birthday is still to come this year
	}
	return &years
}

// Age is the person's age today.
func (p Person) Age() *int { return p.AgeOn(time.Now()) }

// EstimateDOB is the birth date an age stated on a date implies: the middle of
// the year it points at, so the estimate is never more than six months out.
func EstimateDOB(years int, on time.Time) time.Time {
	return time.Date(on.Year()-years, time.July, 1, 0, 0, 0, 0, time.UTC)
}

// HealthWorker is a person's place in the workforce — `health_workers` and the
// `persons` row it points at — without the survey answers that live on a
// profile submission, and without the posting that lives on deployments. What
// a worker does and where they do it is a Deployment; who they are survives
// every transfer.
//
// Person is embedded, so a worker reads as the person it is (w.FirstName); the
// worker's own ID is the one everything in the register joins on, and the
// person's is w.Person.ID.
type HealthWorker struct {
	ID int64
	Person
	// Code is the human-legible permanent identifier, e.g. KYE00042: the
	// district's three-letter code and a serial. Issued by trigger with the
	// first deployment, never supplied and never changed, so no input type
	// carries it. Empty only inside the creating transaction.
	Code string

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

	CreatedBy     *int64
	LastUpdatedBy *int64
	CreatedOn     time.Time
	LastUpdatedOn time.Time
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

// Depth is the level's rung in the hierarchy, region 0 to village 5, so two
// levels can be compared: "placed too shallow" and "too deep" are different
// instructions to whoever is fixing the row.
func (l Level) Depth() int {
	switch l {
	case LevelRegion:
		return 0
	case LevelDistrict:
		return 1
	case LevelCounty:
		return 2
	case LevelSubcounty:
		return 3
	case LevelParish:
		return 4
	case LevelVillage:
		return 5
	}
	return -1
}

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
