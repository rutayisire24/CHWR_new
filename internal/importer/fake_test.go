package importer

import (
	"context"
	"strings"

	"hwr/internal/auth"
	"hwr/internal/domain"
)

// A small hierarchy, enough to exercise every resolution path:
//
//	ACHOLI region
//	  ABIM district
//	    ABIM COUNTY > MORULEM subcounty            > ALEREK parish > BUHOBA A, BUHOBA A, KANU-EAST
//	                                               > OKUDI parish  > BUHOBA A
//	                > MORULEM TOWN COUNCIL subcounty > CENTRAL WARD parish > MARKET CELL
//	  GULU district
//	    OMORO COUNTY > BUNGATIRA subcounty > PAWEL parish > LAYIBI village
//
// Two villages called BUHOBA A in one parish is the real case from the source
// workbook, and the reason identity is (parent_id, code) rather than name.
//
// MORULEM beside MORULEM TOWN COUNCIL is the other real case, and the reason
// foldName expands a tier word rather than dropping it: the hierarchy holds 279
// such pairs, and a fold that merged them would place a CHW in the wrong one
// without anything to notice. CENTRAL WARD and MARKET CELL carry the tier words
// that end real parish and village names.
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

	morulemTC = 30
	centralWd = 31
	marketCel = 32

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
	morulemTC: {place: domain.Place{ID: morulemTC, Level: domain.LevelSubcounty, Name: "MORULEM TOWN COUNCIL", Code: "05"}, parent: abimCty},
	centralWd: {place: domain.Place{ID: centralWd, Level: domain.LevelParish, Name: "CENTRAL WARD", Code: "001"}, parent: morulemTC},
	marketCel: {place: domain.Place{ID: marketCel, Level: domain.LevelVillage, Name: "MARKET CELL", Code: "001"}, parent: centralWd},
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

// The survey, shaped as 0006 seeds the CHW baseline: the same codes, the same
// branches, a subset of the tool and service choices. Code and prompt both
// match a cell, because a district reading the template's help will write one
// or the other.
var fakeSurvey = func() Survey {
	f64 := func(v float64) *float64 { return &v }
	yesNo := []domain.Option{{ID: 1, Code: "yes", Prompt: "Yes"}, {ID: 2, Code: "no", Prompt: "No"}}
	tools := []domain.Option{{ID: 0, Code: "none", Prompt: "None"},
		{ID: 1, Code: "bicycle", Prompt: "Bicycle"}, {ID: 2, Code: "gumboots", Prompt: "Gumboots"},
		{ID: 3, Code: "thermometer", Prompt: "Thermometer"}, {ID: 7, Code: "register", Prompt: "VHT Reporting Tools"}}
	services := []domain.Option{{ID: 0, Code: "none", Prompt: "None"},
		{ID: 1, Code: "iccm", Prompt: "Management of Common Childhood Illnesses (ICCM)"},
		{ID: 2, Code: "maternal_newborn", Prompt: "Maternal and Newborn Health"},
		{ID: 7, Code: "nutrition", Prompt: "Nutrition Services"}}
	q := func(code string, dt domain.DataType, opts []domain.Option, multi bool) domain.Question {
		return domain.Question{Code: code, Prompt: code, DataType: dt, Options: opts,
			Closed: opts != nil, Multi: multi, Active: true}
	}
	year, households, amount := q("service_start_year", domain.DataInteger, nil, false),
		q("households_served", domain.DataInteger, nil, false), q("incentive_amount_ugx", domain.DataInteger, nil, false)
	year.Min, year.Max = f64(1960), f64(2100)
	households.Min, households.Max = f64(3), f64(100000)
	amount.Min, amount.Max = f64(1000), f64(500000)
	amount.DependsOn, amount.DependsOnOption = "receives_incentive", "yes"
	reporting := q("phone_for_reporting", domain.DataYesNo, yesNo, false)
	reporting.DependsOn, reporting.DependsOnOption = "owns_phone", "yes"
	frequency := q("incentive_frequency", domain.DataCharacter, []domain.Option{
		{ID: 1, Code: "monthly", Prompt: "Monthly", Aliases: []string{"month"}},
		{ID: 2, Code: "quarterly", Prompt: "Quarterly"},
		{ID: 3, Code: "annually", Prompt: "Annually", Aliases: []string{"yearly"}},
		{ID: 4, Code: "one_off", Prompt: "One-off", Aliases: []string{"once"}}}, false)
	frequency.DependsOn, frequency.DependsOnOption = "receives_incentive", "yes"
	supervised := q("last_supervised_on", domain.DataMonth, nil, false)
	supervised.DependsOn, supervised.DependsOnOption = "received_supervision", "yes"
	functional := q("tools_functional", domain.DataCharacter, tools, true)
	functional.SubsetOf = "tools_held"
	trained := q("services_trained", domain.DataCharacter, services, true)
	trained.SubsetOf = "services_provided"
	return Survey{
		Profile: domain.Profile{Code: "chw_baseline", Name: "CHW baseline survey", Questions: []domain.Question{
			q("owns_phone", domain.DataYesNo, yesNo, false), reporting, year, households,
			q("other_languages", domain.DataText, nil, false),
			q("receives_incentive", domain.DataYesNo, yesNo, false), frequency, amount,
			q("received_supervision", domain.DataYesNo, yesNo, false), supervised,
			q("tools_held", domain.DataCharacter, tools, true), functional,
			q("services_provided", domain.DataCharacter, services, true), trained,
		}},
		Cadres: []int16{1, 2}, // vht and chew; not the health assistant
	}
}()

// The cadre vocabulary, as 0003 seeds it — VHTs at village, CHEWs at parish —
// plus a cadre outside the CHW category placed at subcounty, the shape an
// administrator adds through /cadres.
var fakeCadres = []domain.Cadre{
	{ID: 1, CategoryID: 1, CategoryCode: domain.CategoryCHW, Code: "vht", Label: "Village Health Team member",
		PlacementLevel: domain.LevelVillage, ImportAliases: []string{"village health team"}, Active: true},
	{ID: 2, CategoryID: 1, CategoryCode: domain.CategoryCHW, Code: "chew", Label: "Community Health Extension Worker",
		PlacementLevel: domain.LevelParish, ImportAliases: []string{"chw", "community health extension worker"}, Active: true},
	{ID: 3, CategoryID: 2, CategoryCode: "ehs", Code: "health_assistant", Label: "Health Assistant",
		PlacementLevel: domain.LevelSubcounty, ImportAliases: []string{"ha"}, Active: true},
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
	nins  map[string]domain.HealthWorker
	names map[int64][]domain.HealthWorker
	// calls counts ChildrenAt, so a test can prove the resolver caches.
	calls int
	// facilityCalls counts FacilitiesIn, for the same reason.
	facilityCalls int
}

func (f *fakeLookup) Cadres(ctx context.Context) ([]domain.Cadre, error) {
	return fakeCadres, nil
}

func (f *fakeLookup) Survey(ctx context.Context) (Survey, error) {
	return fakeSurvey, nil
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

func (f *fakeLookup) WorkerWithNIN(ctx context.Context, nin string) (domain.HealthWorker, error) {
	if w, ok := f.nins[nin]; ok {
		return w, nil
	}
	return domain.HealthWorker{}, domain.ErrNotFound
}

func (f *fakeLookup) NamesAt(ctx context.Context, sc auth.Scope, locationID int64, first, last string) ([]domain.HealthWorker, error) {
	var out []domain.HealthWorker
	for _, w := range f.names[locationID] {
		if strings.EqualFold(w.FirstName, first) && strings.EqualFold(w.LastName, last) {
			out = append(out, w)
		}
	}
	return out, nil
}
