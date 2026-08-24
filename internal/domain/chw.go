package domain

import "time"

// Cadre is the `cadre` enum. Single-valued by decision: the ODK form allowed a
// multi-select with "other", which produced values nothing could act on.
//
// Cadre determines placement. A CHEW serves a parish, a VHT a village; the
// level is enforced by trigger on insert and on cadre change alike.
type Cadre string

const (
	CadreVHT  Cadre = "vht"
	CadreCHEW Cadre = "chew"
)

// Cadres lists both members, for validation and for the form's radio group.
var Cadres = []Cadre{CadreVHT, CadreCHEW}

// Valid reports whether c is one of the two enum members.
func (c Cadre) Valid() bool { return c == CadreVHT || c == CadreCHEW }

// PlacementLevel is the hierarchy level a CHW of this cadre must sit at. It
// mirrors chws_set_placement; the trigger is the enforcement, this is what
// lets the form ask for the right thing in the first place.
func (c Cadre) PlacementLevel() Level {
	if c == CadreCHEW {
		return LevelParish
	}
	return LevelVillage
}

// Label is the human-readable cadre name.
func (c Cadre) Label() string {
	switch c {
	case CadreVHT:
		return "VHT"
	case CadreCHEW:
		return "CHEW"
	}
	return string(c)
}

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

// CHWStatus is the `chw_status` enum. CHWs are never deleted: deactivation
// sets the status, the timestamp and a reason together, and
// chws_deactivation_complete keeps status and timestamp consistent.
type CHWStatus string

const (
	CHWActive   CHWStatus = "active"
	CHWInactive CHWStatus = "inactive"
)

// CHW is the core register record — `chws`, without the optional survey
// attributes that live on chw_profiles.
type CHW struct {
	ID        int64
	NIN       string // empty when not recorded; unique where present
	FirstName string
	LastName  string
	Sex       Sex
	Cadre     Cadre
	AgeYears  *int16
	// AgeCapturedOn is when the age was true. Age is a snapshot, not a fact.
	AgeCapturedOn time.Time

	// LocationID is the single placement column; its level is determined by
	// cadre. DistrictID is derived from it by trigger and never supplied.
	LocationID   int64
	DistrictID   int64
	LocationName string // joined for display
	DistrictName string
	// Placement is the location's ancestor chain, district downward, for the
	// breadcrumb and for prefilling the cascading selects on edit.
	Placement []Place

	Status             CHWStatus
	DeactivatedAt      *time.Time
	DeactivationReason string

	CreatedBy *int64
	UpdatedBy *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// FullName is "first last", the form the register displays and searches.
func (c CHW) FullName() string { return c.FirstName + " " + c.LastName }

// Active reports whether the CHW is currently serving.
func (c CHW) Active() bool { return c.Status == CHWActive }

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
}
