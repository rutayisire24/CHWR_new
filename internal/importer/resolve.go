package importer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Lookup is the slice of the register the importer reads. internal/store
// satisfies it; a test supplies its own, which is what lets the whole
// validation pass run without a database.
//
// Every method takes the Scope, because every read here is a read of the
// register and the register has no unscoped reads.
type Lookup interface {
	// Districts lists the districts the scope allows — one, for a district
	// user, which is what confines a whole file to their district.
	Districts(ctx context.Context, sc auth.Scope) ([]domain.Place, error)
	// ChildrenAt lists the locations of one level under an ancestor, with
	// their codes. Every sibling, not a match: telling a match from an
	// ambiguity means seeing them all.
	ChildrenAt(ctx context.Context, sc auth.Scope, ancestorID int64, level domain.Level) ([]domain.Place, error)
	// ByCode resolves an official code path to a location id.
	ByCode(ctx context.Context, code string) (int64, error)
	// Ancestors returns a location's chain, region downward and inclusive.
	Ancestors(ctx context.Context, sc auth.Scope, id int64) ([]domain.Place, error)
	// CHWWithNIN returns the CHW already carrying a NIN. The importer asks
	// nationally: chws_nin_uniq is a national index, so a duplicate in another
	// district is still a duplicate.
	CHWWithNIN(ctx context.Context, nin string) (domain.CHW, error)
	// NamesAt returns CHWs of that name at that location, for the soft
	// duplicate probe.
	NamesAt(ctx context.Context, sc auth.Scope, locationID int64, first, last string) ([]domain.CHW, error)
}

// normalizeName folds a location name to the form names are matched by: upper
// case, with every separator removed — spaces, hyphens, apostrophes and stops.
// KANU-EAST, "Kanu East" and KANUEAST are one name, because the workbook, the
// district's spreadsheet and the clerk all write it differently.
//
// Dropping separators rather than collapsing them to a space is deliberate.
// Collapsing matches "Kanu East" to "KANU EAST" and still misses "KANU-EAST",
// which is the spelling the source workbook actually uses. The cost is that two
// genuinely different names could fold together — and that cost is bounded,
// because a fold that matches two siblings is an *ambiguity*, which is
// quarantined with both candidates rather than resolved. Over-matching here
// produces a question, never a silently wrong answer.
//
// Nothing fuzzier than this, ever. Identity in locations is (parent_id, code)
// and explicitly not name — one parish holds two villages both called BUHOBA A
// — so a trigram match across 71,207 villages would answer confidently and
// wrongly, and a CHW filed under the wrong village is not an error anyone
// notices.
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case ' ', '\t', '\n', '\r', '.', '\'', '’', '-', '–', '_', '/':
			// A separator is a typing habit, not part of the name.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Placement is where a row's CHW goes: the location itself and the district
// derived from its chain.
type Placement struct {
	LocationID int64
	Level      domain.Level
	DistrictID int64
	// Chain is the resolved location's ancestry, for the message a code
	// mismatch prints.
	Chain []domain.Place
}

// Resolver turns a row's location columns into a Placement.
//
// It caches: a file is usually one district's worth of CHWs, so the same
// parish's villages are asked for hundreds of times over. The cache lives for
// one file, which is also as long as it may live — the hierarchy is not
// immutable, and a resolver kept between uploads would answer from a register
// that has since changed.
type Resolver struct {
	lookup Lookup
	scope  auth.Scope

	districts map[string][]domain.Place
	children  map[childKey][]domain.Place
}

type childKey struct {
	parent int64
	level  domain.Level
}

// NewResolver preloads the districts the scope allows. For a district user
// that is one row, and it is the reason a file naming another district is
// refused row by row rather than resolved and then caught: the other district
// is not in the map at all.
func NewResolver(ctx context.Context, lookup Lookup, sc auth.Scope) (*Resolver, error) {
	districts, err := lookup.Districts(ctx, sc)
	if err != nil {
		return nil, fmt.Errorf("load districts: %w", err)
	}

	r := &Resolver{
		lookup:    lookup,
		scope:     sc,
		districts: make(map[string][]domain.Place, len(districts)),
		children:  make(map[childKey][]domain.Place),
	}
	for _, d := range districts {
		key := normalizeName(d.Name)
		r.districts[key] = append(r.districts[key], d)
	}
	return r, nil
}

// Resolve reads a row's placement. cadre decides which level the placement sits
// at, so a row whose cadre did not parse resolves as deep as it was given and
// leaves the level alone.
func (r *Resolver) Resolve(ctx context.Context, row Row, cadre domain.Cadre) (*Placement, []domain.Problem) {
	if code := row.Value(ColCode); code != "" {
		return r.byCode(ctx, row, code)
	}
	return r.byNames(ctx, row, cadre)
}

// byCode resolves the official code path, and checks the name columns against
// what it found.
//
// Migration 0001 settles which channel is authoritative — code is the identity,
// name is a label — so the code decides. That does not make a contradicting
// label ignorable: when the two disagree one of them is wrong and nothing in
// the file says which, and preferring the code quietly would file a CHW
// somewhere no human confirmed.
func (r *Resolver) byCode(ctx context.Context, row Row, code string) (*Placement, []domain.Problem) {
	id, err := r.lookup.ByCode(ctx, code)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, []domain.Problem{{Field: ColCode, Code: domain.ProblemCodeUnknown,
			Message: fmt.Sprintf("No location has the code %s.", code)}}
	}
	if err != nil {
		return nil, []domain.Problem{{Field: ColCode, Code: domain.ProblemCodeUnknown,
			Message: "That code could not be checked. Try again."}}
	}

	// Scope before anything descriptive. A code resolving outside the
	// uploader's district is answered as out of scope and nothing further,
	// because the mismatch message below would name another district's
	// locations — and a district user must not map the country by probing.
	chain, err := r.lookup.Ancestors(ctx, auth.National(), id)
	if err != nil {
		return nil, []domain.Problem{{Field: ColCode, Code: domain.ProblemCodeUnknown,
			Message: "That code could not be checked. Try again."}}
	}
	place := &Placement{LocationID: id, Chain: chain, Level: chain[len(chain)-1].Level}
	for _, p := range chain {
		if p.Level == domain.LevelDistrict {
			place.DistrictID = p.ID
		}
	}
	if !r.scope.Allows(place.DistrictID) {
		return nil, []domain.Problem{outsideScope(ColCode)}
	}

	if problem := r.checkNames(row, chain, code); problem != nil {
		return nil, []domain.Problem{*problem}
	}
	return place, nil
}

// checkNames compares the name columns the file filled in against the chain the
// code resolved to. The comparison is on normalized names, so a difference of
// punctuation or case is a match; only a genuine contradiction stops the row.
func (r *Resolver) checkNames(row Row, chain []domain.Place, code string) *domain.Problem {
	byLevel := make(map[domain.Level]domain.Place, len(chain))
	for _, p := range chain {
		byLevel[p.Level] = p
	}

	for _, pair := range []struct {
		column string
		level  domain.Level
	}{
		{ColDistrict, domain.LevelDistrict},
		{ColSubcounty, domain.LevelSubcounty},
		{ColParish, domain.LevelParish},
		{ColVillage, domain.LevelVillage},
	} {
		named := row.Value(pair.column)
		if named == "" {
			continue
		}
		actual, ok := byLevel[pair.level]
		if !ok {
			return &domain.Problem{Field: pair.column, Code: domain.ProblemCodeMismatch,
				Message: fmt.Sprintf("The code %s is a %s, so it has no %s. The row says %s.",
					code, chain[len(chain)-1].Level.Label(), pair.column, named)}
		}
		if normalizeName(named) != normalizeName(actual.Name) {
			return &domain.Problem{Field: pair.column, Code: domain.ProblemCodeMismatch,
				Message: fmt.Sprintf("The code %s is %s %s. The row says %s.",
					code, actual.Name, strings.ToLower(pair.level.Label()), named)}
		}
	}
	return nil
}

// byNames walks the cascade: district, then subcounty, parish and village, each
// matched among the siblings of the one above.
//
// County is never a column. It is mandatory in the data — subcounty codes are
// unique only within a county — and it is derived from the path, exactly as the
// cascading selects derive it. A district's spreadsheet will not have it.
func (r *Resolver) byNames(ctx context.Context, row Row, cadre domain.Cadre) (*Placement, []domain.Problem) {
	districtName := row.Value(ColDistrict)
	if districtName == "" {
		return nil, []domain.Problem{{Field: ColDistrict, Code: domain.ProblemRequired,
			Message: "Name the district."}}
	}
	matches := r.districts[normalizeName(districtName)]
	switch len(matches) {
	case 0:
		// A district outside the scope and a district that does not exist get
		// the same answer, because the scope's district list is all this
		// resolver was ever given.
		return nil, []domain.Problem{outsideScope(ColDistrict)}
	case 1:
	default:
		return nil, []domain.Problem{r.ambiguous(ctx, ColDistrict, districtName, matches)}
	}

	place := &Placement{
		LocationID: matches[0].ID,
		Level:      domain.LevelDistrict,
		DistrictID: matches[0].ID,
		Chain:      []domain.Place{matches[0]},
	}

	// The cascade below the district. A CHEW stops at parish; a VHT goes on to
	// the village.
	steps := []struct {
		column string
		level  domain.Level
	}{
		{ColSubcounty, domain.LevelSubcounty},
		{ColParish, domain.LevelParish},
		{ColVillage, domain.LevelVillage},
	}

	for _, step := range steps {
		name := row.Value(step.column)
		if name == "" {
			if step.level == domain.LevelVillage {
				break // whether that is allowed is the cadre's business
			}
			return nil, []domain.Problem{{Field: step.column, Code: domain.ProblemRequired,
				Message: "Name the " + step.column + "."}}
		}

		siblings, err := r.childrenAt(ctx, place.LocationID, step.level)
		if err != nil {
			return nil, []domain.Problem{{Field: step.column, Code: domain.ProblemLocationMissing,
				Message: "That location could not be checked. Try again."}}
		}

		var found []domain.Place
		wanted := normalizeName(name)
		for _, s := range siblings {
			if normalizeName(s.Name) == wanted {
				found = append(found, s)
			}
		}
		switch len(found) {
		case 0:
			return nil, []domain.Problem{{Field: step.column, Code: domain.ProblemLocationMissing,
				Message: fmt.Sprintf("No %s called %s in %s.",
					step.column, name, place.Chain[len(place.Chain)-1].Name)}}
		case 1:
		default:
			return nil, []domain.Problem{r.ambiguous(ctx, step.column, name, found)}
		}

		place.LocationID = found[0].ID
		place.Level = step.level
		place.Chain = append(place.Chain, found[0])
	}
	return place, nil
}

// childrenAt reads one rung of the cascade, through the per-file cache.
func (r *Resolver) childrenAt(ctx context.Context, parentID int64, level domain.Level) ([]domain.Place, error) {
	key := childKey{parent: parentID, level: level}
	if cached, ok := r.children[key]; ok {
		return cached, nil
	}
	found, err := r.lookup.ChildrenAt(ctx, r.scope, parentID, level)
	if err != nil {
		return nil, err
	}
	r.children[key] = found
	return found, nil
}

// outsideScope is the one answer a district user gets for a location that is
// not theirs, whether it exists or not. Naming the district it belongs to would
// let a district user map the country by probing names, which is the same
// reason /api/locations and the CHW form answer this way.
func outsideScope(column string) domain.Problem {
	return domain.Problem{Field: column, Code: domain.ProblemOutsideScope,
		Message: "That location is not in your district."}
}

// ambiguous carries every place the name could have meant, with the chain above
// it and the code that settles it. A human picks; nothing here guesses.
//
// The chain is fetched per candidate, which is a query the common path never
// makes: two siblings sharing a name is rare, and "which BUHOBA A" is
// unanswerable without the parish above it.
func (r *Resolver) ambiguous(ctx context.Context, column, name string, matches []domain.Place) domain.Problem {
	candidates := make([]domain.Candidate, 0, len(matches))
	for _, m := range matches {
		c := domain.Candidate{LocationID: m.ID, Name: m.Name, Code: m.Code}
		if chain, err := r.lookup.Ancestors(ctx, r.scope, m.ID); err == nil {
			var names []string
			for _, p := range chain {
				if p.Level != domain.LevelRegion && p.ID != m.ID {
					names = append(names, p.Name)
				}
			}
			c.Path = strings.Join(names, " > ")
		}
		candidates = append(candidates, c)
	}
	return domain.Problem{Field: column, Code: domain.ProblemLocationAmbig,
		Message: fmt.Sprintf("More than one %s here is called %s. Put its code in the %s column to say which.",
			column, name, ColCode),
		Candidates: candidates}
}
