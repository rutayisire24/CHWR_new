package domain

import (
	"strings"
	"time"
)

// CadreCategory is a row of `cadre_categories` — the grouping the register is
// entered through. Community Health Workers is the first; each category owns
// its profile surface (chw_profiles for this one).
type CadreCategory struct {
	ID     int16
	Slug   string
	Label  string
	Active bool
}

// Cadre is a row of `cadres`: a type of health worker within a category —
// VHT and CHEW within Community Health Workers.
//
// Cadres are data, not an enum. The placement rule is the row's
// PlacementLevel, read by the deployments_set_placement trigger and by every
// form that asks for a placement, so adding a cadre is an INSERT, not a
// migration.
type Cadre struct {
	ID             int16
	CategoryID     int16
	Slug           string
	Label          string
	PlacementLevel Level
	// ImportAliases are the spellings an import may use for this cadre beside
	// the slug itself, matched case-folded with separators stripped — the ODK
	// export alone carries "VHT", "vht", "CHEW" and "CHW" (docs/odk-mapping.md).
	ImportAliases []string
	Active        bool
}

// MatchesImport reports whether a cell value names this cadre: its slug or one
// of its aliases, compared with case and separators folded away. "V.H.T" and
// "vht" are the same answer.
func (c Cadre) MatchesImport(s string) bool {
	folded := foldSeparators(s)
	if folded == foldSeparators(c.Slug) {
		return true
	}
	for _, alias := range c.ImportAliases {
		if folded == foldSeparators(alias) {
			return true
		}
	}
	return false
}

// foldSeparators lowercases and drops every space, dash, dot and underscore —
// the same rule the location resolver applies to names, and nothing fuzzier.
func foldSeparators(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case ' ', '-', '.', '_', '\t':
			continue
		}
		out = append(out, r)
	}
	return strings.ToLower(string(out))
}

// Deployment is a posting: one health worker serving in one cadre at one
// location over a period. A transfer or a promotion ends one row and opens
// another, so the register itself answers "who was deployed at X on date D".
//
// A worker holds at most one active deployment (EndedOn nil) —
// deployments_one_active_idx says so.
type Deployment struct {
	ID             int64
	HealthWorkerID int64

	CadreID int16
	// Cadre is the joined cadres row, for the label and the placement level.
	Cadre Cadre

	// LocationID is the placement; its level must equal the cadre's
	// PlacementLevel. DistrictID is derived from it by trigger and never
	// supplied.
	LocationID   int64
	DistrictID   int64
	LocationName string // joined for display
	DistrictName string
	// Placement is the location's ancestor chain, district downward, for the
	// breadcrumb and for prefilling the cascading selects on edit.
	Placement []Place

	// FacilityID is the supervising facility: an optional attachment, not a
	// placement, and always in the deployment's own district.
	FacilityID   *int64
	FacilityName string // joined for display

	StartedOn time.Time
	EndedOn   *time.Time
	EndReason string

	CreatedBy *int64
	UpdatedBy *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Active reports whether the posting is current.
func (d Deployment) Active() bool { return d.EndedOn == nil }
