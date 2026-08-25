package importer

import (
	"context"
	"strings"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// A small hierarchy, enough to exercise every resolution path:
//
//	ACHOLI region
//	  ABIM district
//	    ABIM COUNTY > MORULEM subcounty > ALEREK parish > BUHOBA A, BUHOBA A, KANU
//	                                    > OKUDI parish  > BUHOBA A
//	  GULU district
//	    OMORO COUNTY > BUNGATIRA subcounty > PAWEL parish > LAYIBI village
//
// Two villages called BUHOBA A in one parish is the real case from the source
// workbook, and the reason identity is (parent_id, code) rather than name.
const (
	acholi   = 1
	abim     = 10
	abimCty  = 11
	morulem  = 12
	alerek   = 13
	buhobaA1 = 14
	buhobaA2 = 15
	kanu     = 16
	okudi    = 17
	buhobaA3 = 18

	gulu      = 20
	omoro     = 21
	bungatira = 22
	pawel     = 23
	layibi    = 24
)

type node struct {
	place  domain.Place
	parent int64
	code   string
}

var tree = map[int64]node{
	acholi:    {place: domain.Place{ID: acholi, Level: domain.LevelRegion, Name: "ACHOLI"}},
	abim:      {place: domain.Place{ID: abim, Level: domain.LevelDistrict, Name: "ABIM", Code: "095"}, parent: acholi},
	abimCty:   {place: domain.Place{ID: abimCty, Level: domain.LevelCounty, Name: "ABIM COUNTY", Code: "235"}, parent: abim},
	morulem:   {place: domain.Place{ID: morulem, Level: domain.LevelSubcounty, Name: "MORULEM", Code: "04"}, parent: abimCty},
	alerek:    {place: domain.Place{ID: alerek, Level: domain.LevelParish, Name: "ALEREK", Code: "038"}, parent: morulem},
	buhobaA1:  {place: domain.Place{ID: buhobaA1, Level: domain.LevelVillage, Name: "BUHOBA A", Code: "002"}, parent: alerek},
	buhobaA2:  {place: domain.Place{ID: buhobaA2, Level: domain.LevelVillage, Name: "BUHOBA A", Code: "003"}, parent: alerek},
	kanu:      {place: domain.Place{ID: kanu, Level: domain.LevelVillage, Name: "KANU-EAST", Code: "004"}, parent: alerek},
	okudi:     {place: domain.Place{ID: okudi, Level: domain.LevelParish, Name: "OKUDI", Code: "039"}, parent: morulem},
	buhobaA3:  {place: domain.Place{ID: buhobaA3, Level: domain.LevelVillage, Name: "BUHOBA A", Code: "001"}, parent: okudi},
	gulu:      {place: domain.Place{ID: gulu, Level: domain.LevelDistrict, Name: "GULU", Code: "102"}, parent: acholi},
	omoro:     {place: domain.Place{ID: omoro, Level: domain.LevelCounty, Name: "OMORO COUNTY", Code: "241"}, parent: gulu},
	bungatira: {place: domain.Place{ID: bungatira, Level: domain.LevelSubcounty, Name: "BUNGATIRA", Code: "01"}, parent: omoro},
	pawel:     {place: domain.Place{ID: pawel, Level: domain.LevelParish, Name: "PAWEL", Code: "010"}, parent: bungatira},
	layibi:    {place: domain.Place{ID: layibi, Level: domain.LevelVillage, Name: "LAYIBI", Code: "001"}, parent: pawel},
}

// codePaths are the concatenated official codes, as locations.code_path holds
// them.
var codePaths = map[string]int64{
	"09523504038002": buhobaA1,
	"09523504038003": buhobaA2,
	"09523504038":    alerek,
	"09523504":       morulem,
	"10224101010001": layibi,
}

// The vocabularies, as 0003 seeds them. Slug and label both match, because a
// district reading the template's help will write one or the other.
var fakeTools = []domain.Tool{
	{ID: 1, Slug: "bicycle", Label: "Bicycle"},
	{ID: 2, Slug: "gumboots", Label: "Gumboots"},
	{ID: 3, Slug: "thermometer", Label: "Thermometer"},
	{ID: 7, Slug: "register", Label: "VHT Reporting Tools"},
}

var fakeDomains = []domain.ServiceDomain{
	{ID: 1, Slug: "iccm", Label: "Management of Common Childhood Illnesses (ICCM)"},
	{ID: 2, Slug: "maternal_newborn", Label: "Maternal and Newborn Health"},
	{ID: 7, Slug: "nutrition", Label: "Nutrition Services"},
}

// Facilities, keyed by district. ABIM holds two of the same name, which is not
// possible in the real schema — facilities are unique on (district_id, name) —
// but the importer must not depend on that to avoid picking one arbitrarily.
var fakeFacilities = map[int64][]domain.Facility{
	abim: {
		{ID: 100, Name: "ABIM HOSPITAL", Ownership: "GOV"},
		{ID: 101, Name: "MORULEM HC III", Ownership: "GOV"},
		{ID: 102, Name: "TWIN CLINIC", Ownership: "PFP"},
		{ID: 103, Name: "TWIN CLINIC", Ownership: "PNFP"},
	},
	gulu: {{ID: 200, Name: "GULU REGIONAL REFERRAL", Ownership: "GOV"}},
}

// fakeLookup answers from the tree above. It is the whole reason the validation
// pass needs no database.
type fakeLookup struct {
	// nins and names are the register as far as duplicate checking is
	// concerned.
	nins  map[string]domain.CHW
	names map[int64][]domain.CHW
	// calls counts ChildrenAt, so a test can prove the resolver caches.
	calls int
	// facilityCalls counts FacilitiesIn, for the same reason.
	facilityCalls int
}

func (f *fakeLookup) Tools(ctx context.Context) ([]domain.Tool, error) {
	return fakeTools, nil
}

func (f *fakeLookup) ServiceDomains(ctx context.Context) ([]domain.ServiceDomain, error) {
	return fakeDomains, nil
}

func (f *fakeLookup) FacilitiesIn(ctx context.Context, sc auth.Scope, districtID int64) ([]domain.Facility, error) {
	f.facilityCalls++
	if !sc.Allows(districtID) {
		return nil, domain.ErrNotFound
	}
	return fakeFacilities[districtID], nil
}

func (f *fakeLookup) Districts(ctx context.Context, sc auth.Scope) ([]domain.Place, error) {
	var out []domain.Place
	for _, n := range tree {
		if n.place.Level == domain.LevelDistrict && sc.Allows(n.place.ID) {
			out = append(out, n.place)
		}
	}
	return out, nil
}

func (f *fakeLookup) ChildrenAt(ctx context.Context, sc auth.Scope, ancestorID int64, level domain.Level) ([]domain.Place, error) {
	f.calls++
	var out []domain.Place
	for id, n := range tree {
		if n.place.Level != level {
			continue
		}
		for cursor := id; cursor != 0; cursor = tree[cursor].parent {
			if tree[cursor].parent == ancestorID || cursor == ancestorID {
				if n.place.ID != ancestorID {
					out = append(out, n.place)
				}
				break
			}
		}
	}
	return out, nil
}

func (f *fakeLookup) ByCode(ctx context.Context, code string) (int64, error) {
	if id, ok := codePaths[code]; ok {
		return id, nil
	}
	return 0, domain.ErrNotFound
}

func (f *fakeLookup) Ancestors(ctx context.Context, sc auth.Scope, id int64) ([]domain.Place, error) {
	n, ok := tree[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	chain := []domain.Place{n.place}
	for cursor := n.parent; cursor != 0; cursor = tree[cursor].parent {
		chain = append([]domain.Place{tree[cursor].place}, chain...)
	}
	return chain, nil
}

func (f *fakeLookup) CHWWithNIN(ctx context.Context, nin string) (domain.CHW, error) {
	if chw, ok := f.nins[nin]; ok {
		return chw, nil
	}
	return domain.CHW{}, domain.ErrNotFound
}

func (f *fakeLookup) NamesAt(ctx context.Context, sc auth.Scope, locationID int64, first, last string) ([]domain.CHW, error) {
	var out []domain.CHW
	for _, chw := range f.names[locationID] {
		if strings.EqualFold(chw.FirstName, first) && strings.EqualFold(chw.LastName, last) {
			out = append(out, chw)
		}
	}
	return out, nil
}
