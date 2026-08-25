package http

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"chwr/internal/auth"
	"chwr/internal/domain"
	"chwr/internal/store"
)

type chwsPage struct {
	CHWs     []domain.CHW
	Matching int64
	Filter   filterView
	// PrevURL and NextURL are empty when there is no page that way, so the
	// template asks a string rather than reassembling the query itself.
	PrevURL   string
	NextURL   string
	Cadres    []cadreOption
	Places    []domain.Place // the chosen location's chain, for the "filtered to" line
	Districts []districtOption
	Prefill   map[string]int64
	CanEdit   bool
}

// filterView is the filter as the form redisplays it.
type filterView struct {
	Query      string
	Cadre      string
	Status     string
	LocationID int64
	Active     bool // any filter set at all
}

type cadreOption struct {
	Value    domain.Cadre
	Label    string
	Level    string // the placement level this cadre requires
	Selected bool
}

type sexOption struct {
	Value    domain.Sex
	Label    string
	Selected bool
}

type chwFormPage struct {
	Action    string
	CHW       domain.CHW
	Age       string // kept as typed, so a rejected form redisplays it
	Cadres    []cadreOption
	Sexes     []sexOption
	Districts []districtOption
	// Prefill is the location chain of an existing placement, so the cascade
	// can be rebuilt on edit without the browser guessing.
	Prefill    map[string]int64
	Duplicates []domain.CHW
	Errors     map[string]string
}

type chwShowPage struct {
	CHW       domain.CHW
	Placement []domain.Place
	Profile   domain.Profile
	Tools     []domain.CHWTool
	Domains   []domain.CHWServiceDomain
	CanEdit   bool
}

func (s *Server) chwsList(w http.ResponseWriter, r *http.Request) {
	sc := auth.ScopeFrom(r.Context())
	f, view := decodeFilter(r)

	page, err := s.store.CHWs.List(r.Context(), sc, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	matching, err := s.store.CHWs.Matching(r.Context(), sc, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	districts, err := s.store.Locations.Districts(r.Context(), sc)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// A location filter redisplays as the chain it names, so the page can say
	// which parish, in which subcounty, in which district.
	var places []domain.Place
	prefill := map[string]int64{}
	if f.LocationID != 0 {
		places, err = s.store.Locations.Ancestors(r.Context(), sc, f.LocationID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.fail(w, r, err)
			return
		}
		for _, place := range places {
			prefill[string(place.Level)] = place.ID
		}
	}

	options := make([]districtOption, 0, len(districts))
	for _, d := range districts {
		options = append(options, districtOption{
			ID: d.ID, Name: d.Name,
			Selected: prefill[string(domain.LevelDistrict)] == d.ID,
		})
	}
	cadres := make([]cadreOption, 0, len(domain.Cadres))
	for _, cadre := range domain.Cadres {
		cadres = append(cadres, cadreOption{
			Value: cadre, Label: cadre.Label(), Level: string(cadre.PlacementLevel()),
			Selected: string(cadre) == view.Cadre,
		})
	}

	s.render(w, r, http.StatusOK, "chws", chwsPage{
		CHWs:      page.CHWs,
		Matching:  matching,
		Filter:    view,
		PrevURL:   pageURL(r, page.HasPrev, "before", page.First),
		NextURL:   pageURL(r, page.HasNext, "after", page.Last),
		Cadres:    cadres,
		Places:    places,
		Districts: options,
		Prefill:   prefill,
		CanEdit:   auth.Can(auth.MustUser(r.Context()).Role, auth.CapCHWCreate),
	})
}

// decodeFilter reads the filter from the query string. Everything is optional,
// and anything unrecognised is dropped rather than rejected: a filter arrives
// from a bookmarked or shared URL as often as from the form, and a stale one
// should show the register rather than an error.
func decodeFilter(r *http.Request) (store.Filter, filterView) {
	q := r.URL.Query()

	f := store.Filter{Query: strings.TrimSpace(q.Get("q"))}
	view := filterView{Query: f.Query}

	if cadre := domain.Cadre(q.Get("cadre")); cadre.Valid() {
		f.Cadre = cadre
		view.Cadre = string(cadre)
	}
	switch status := domain.CHWStatus(q.Get("status")); status {
	case domain.CHWActive, domain.CHWInactive:
		f.Status = status
		view.Status = string(status)
	}

	// The location cascade contributes one field per level; the deepest one
	// filled in is the filter, exactly as on the CHW form.
	for _, field := range []string{"village_id", "parish_id", "subcounty_id", "district_id"} {
		if id, err := strconv.ParseInt(q.Get(field), 10, 64); err == nil && id > 0 {
			f.LocationID = id
			view.LocationID = id
			break
		}
	}
	view.Active = f.Query != "" || f.Cadre != "" || f.Status != "" || f.LocationID != 0

	f.After = decodeCursor(q.Get("after"))
	f.Before = decodeCursor(q.Get("before"))

	return f, view
}

// cursorSep separates a cursor's three parts. A unit separator, because a
// surname can contain anything a keyboard produces except a control character.
const cursorSep = "\x1f"

// Cursors travel in the URL. They are encoded rather than sent as three
// parameters: it keeps the query string readable, and a position that looks
// opaque is less inviting to hand-edit into nonsense.
func encodeCursor(c *store.Cursor) string {
	if c == nil {
		return ""
	}
	raw := c.LastName + cursorSep + c.FirstName + cursorSep + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reads a cursor back, and returns nil for anything malformed —
// a bad cursor means "start at the beginning", not an error page.
//
// A cursor is a position, not a permission: it names a sort key, and the scope
// filter still decides which rows past it are visible.
func decodeCursor(encoded string) *store.Cursor {
	if encoded == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	parts := strings.Split(string(raw), cursorSep)
	if len(parts) != 3 {
		return nil
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &store.Cursor{LastName: parts[0], FirstName: parts[1], ID: id}
}

// pageURL rebuilds the current query string around a new cursor, so paging
// keeps the filters and changing a filter starts the paging over.
func pageURL(r *http.Request, exists bool, param string, cursor *store.Cursor) string {
	if !exists || cursor == nil {
		return ""
	}
	q := r.URL.Query()
	q.Del("after")
	q.Del("before")
	q.Set(param, encodeCursor(cursor))
	return "/chws?" + q.Encode()
}

func (s *Server) chwShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	sc := auth.ScopeFrom(r.Context())

	chw, err := s.store.CHWs.Get(r.Context(), sc, id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	placement, err := s.store.Locations.Ancestors(r.Context(), sc, chw.LocationID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	profile, err := s.store.Profiles.Get(r.Context(), sc, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tools, err := s.store.Profiles.Tools(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	domains, err := s.store.Profiles.ServiceDomains(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, http.StatusOK, "chw_show", chwShowPage{
		CHW:       chw,
		Placement: placement,
		Profile:   profile,
		Tools:     tools,
		Domains:   domains,
		CanEdit:   auth.Can(auth.MustUser(r.Context()).Role, auth.CapCHWUpdate),
	})
}

func (s *Server) chwNew(w http.ResponseWriter, r *http.Request) {
	// A district user's district is the only one on offer, and it is
	// preselected: there is nothing to choose.
	blank := domain.CHW{Cadre: domain.CadreVHT, Sex: domain.SexFemale}
	p, err := s.chwForm(r, blank, "/chws/new", nil, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "chw_form", p)
}

func (s *Server) chwCreate(w http.ResponseWriter, r *http.Request) {
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	in, age, v := s.decodeCHW(r, sc)
	if v.Any() {
		s.rerenderCHWForm(w, r, draftCHW(in), "/chws/new", v.Fields, age)
		return
	}

	// A name already on the register at the same location is a warning, not a
	// refusal — two people in one village genuinely can share a name — so the
	// admin is shown the matches and confirms.
	if trimmed(r, "confirm_duplicate") == "" {
		dups, err := s.store.CHWs.PossibleDuplicates(r.Context(), sc, in.LocationID, in.FirstName, in.LastName, 0)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if len(dups) > 0 {
			p, err := s.chwForm(r, draftCHW(in), "/chws/new", nil, age)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			p.Duplicates = dups
			s.render(w, r, http.StatusOK, "chw_form", p)
			return
		}
	}

	chw, err := s.store.CHWs.Create(r.Context(), sc, actor, in, s.clientIP(r))
	if err != nil {
		s.chwWriteFailed(w, r, err, draftCHW(in), "/chws/new", age)
		return
	}

	setFlash(w, s.secure(), "ok", chw.FullName()+" has been added to the register.")
	http.Redirect(w, r, chwPath(chw.ID), http.StatusSeeOther)
}

func (s *Server) chwEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	chw, err := s.store.CHWs.Get(r.Context(), auth.ScopeFrom(r.Context()), id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	age := ""
	if chw.AgeYears != nil {
		age = strconv.Itoa(int(*chw.AgeYears))
	}
	p, err := s.chwForm(r, chw, chwPath(id), nil, age)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "chw_form", p)
}

func (s *Server) chwUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	in, age, v := s.decodeCHW(r, sc)
	if !v.Any() {
		if err := s.checkTransfer(r, sc, id, in.LocationID, v); err != nil {
			s.notFoundOrFail(w, r, err)
			return
		}
	}
	if v.Any() {
		draft := draftCHW(in)
		draft.ID = id
		s.rerenderCHWForm(w, r, draft, chwPath(id), v.Fields, age)
		return
	}

	chw, err := s.store.CHWs.Update(r.Context(), sc, actor, id, in, s.clientIP(r))
	if err != nil {
		draft := draftCHW(in)
		draft.ID = id
		s.chwWriteFailed(w, r, err, draft, chwPath(id), age)
		return
	}

	setFlash(w, s.secure(), "ok", "Saved changes to "+chw.FullName()+".")
	http.Redirect(w, r, chwPath(chw.ID), http.StatusSeeOther)
}

// checkTransfer refuses a move to another district while the CHW is still
// attached to a facility in the old one. chws_check_facility_after_move()
// raises on exactly this, which is right — a transfer must reassign the
// facility in the same transaction rather than have the column silently
// nulled — but a raise reaches the operator as a 500. This turns it into an
// instruction they can act on.
func (s *Server) checkTransfer(r *http.Request, sc auth.Scope, id, newLocationID int64, v *domain.ValidationError) error {
	before, err := s.store.CHWs.Get(r.Context(), sc, id)
	if err != nil {
		return err
	}
	_, newDistrictID, err := s.store.Locations.LevelOf(r.Context(), newLocationID)
	if err != nil {
		return err
	}
	if newDistrictID == before.DistrictID {
		return nil
	}

	profile, err := s.store.Profiles.Get(r.Context(), sc, id)
	if err != nil {
		return err
	}
	if profile.FacilityID != nil {
		v.Add("location", "This CHW reports to "+profile.FacilityName+
			", which is in their current district. Clear or change the supervising facility on their profile before moving them.")
	}
	return nil
}

// chwDeactivate retires a CHW. The reason is required by the handler: the
// schema only insists that status and timestamp agree, and a deactivation
// without a stated reason is unauditable a year later.
func (s *Server) chwDeactivate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	reason := trimmed(r, "reason")
	if len(reason) < 5 {
		setFlash(w, s.secure(), "error", "Give a reason for the deactivation — it becomes part of the record.")
		http.Redirect(w, r, chwPath(id), http.StatusSeeOther)
		return
	}

	chw, err := s.store.CHWs.Deactivate(r.Context(), sc, actor, id, reason, s.clientIP(r))
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	setFlash(w, s.secure(), "ok", chw.FullName()+" is now inactive. The record stays on the register.")
	http.Redirect(w, r, chwPath(id), http.StatusSeeOther)
}

func (s *Server) chwReactivate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	chw, err := s.store.CHWs.Reactivate(r.Context(), auth.ScopeFrom(r.Context()),
		auth.MustUser(r.Context()), id, s.clientIP(r))
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	setFlash(w, s.secure(), "ok", chw.FullName()+" is active again.")
	http.Redirect(w, r, chwPath(id), http.StatusSeeOther)
}

// decodeCHW reads and validates the core record. The placement rule is checked
// here against the location's real level, so a mismatched cadre comes back as
// a field message; chws_set_placement is still what guarantees it.
func (s *Server) decodeCHW(r *http.Request, sc auth.Scope) (store.CHWInput, string, *domain.ValidationError) {
	v := domain.NewValidationError()

	in := store.CHWInput{
		NIN:       strings.ToUpper(trimmed(r, "nin")),
		FirstName: trimmed(r, "first_name"),
		LastName:  trimmed(r, "last_name"),
		Sex:       domain.Sex(trimmed(r, "sex")),
		Cadre:     domain.Cadre(trimmed(r, "cadre")),
	}

	if in.FirstName == "" {
		v.Add("first_name", "Enter the first name.")
	}
	if in.LastName == "" {
		v.Add("last_name", "Enter the last name.")
	}
	if !in.Sex.Valid() {
		v.Add("sex", "Choose one.")
	}
	if !in.Cadre.Valid() {
		v.Add("cadre", "Choose a cadre.")
	}
	if in.NIN != "" && !domain.ValidNIN(in.NIN) {
		v.Add("nin", "A NIN is 14 characters: two letters, eleven letters or digits, then a letter.")
	}

	age := trimmed(r, "age_years")
	if age != "" {
		n, err := strconv.Atoi(age)
		if err != nil || !domain.ValidAge(n) {
			v.Add("age_years", "Age must be a whole number between 18 and 99, or left blank.")
		} else {
			years := int16(n)
			in.AgeYears = &years
		}
	}

	// The cascade posts one field per level; the deepest one filled in is the
	// placement. Which one that must be is decided by the cadre.
	in.LocationID = deepestLocation(r)
	switch {
	case in.LocationID == 0:
		v.Add("location", "Choose the placement, down to the "+placementLabel(in.Cadre)+".")
	case in.Cadre.Valid():
		level, districtID, err := s.store.Locations.LevelOf(r.Context(), in.LocationID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			v.Add("location", "That location no longer exists.")
		case err != nil:
			v.Add("location", "The placement could not be checked. Try again.")
		case level != in.Cadre.PlacementLevel():
			v.Add("location", "A "+in.Cadre.Label()+" is placed at "+placementLabel(in.Cadre)+" level.")
		case !sc.Allows(districtID):
			// Same answer as a location that does not exist: a district user
			// must not map the country by probing ids.
			v.Add("location", "That location no longer exists.")
		}
	}

	return in, age, v
}

// deepestLocation returns the most specific level the cascade posted. Village
// wins over parish, parish over subcounty: the browser leaves the deeper
// selects empty until they are reachable.
func deepestLocation(r *http.Request) int64 {
	for _, field := range []string{"village_id", "parish_id", "subcounty_id", "district_id"} {
		if id, ok := optionalID(r, field); ok && id != nil {
			return *id
		}
	}
	return 0
}

func placementLabel(c domain.Cadre) string {
	if c == domain.CadreCHEW {
		return "parish"
	}
	return "village"
}

// chwForm assembles the selects and the prefilled placement chain.
func (s *Server) chwForm(r *http.Request, c domain.CHW, action string, errs map[string]string, age string) (chwFormPage, error) {
	sc := auth.ScopeFrom(r.Context())

	districts, err := s.store.Locations.Districts(r.Context(), sc)
	if err != nil {
		return chwFormPage{}, err
	}

	prefill := map[string]int64{}
	if c.LocationID != 0 {
		chain, err := s.store.Locations.Ancestors(r.Context(), sc, c.LocationID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return chwFormPage{}, err
		}
		for _, place := range chain {
			prefill[string(place.Level)] = place.ID
		}
	}

	cadres := make([]cadreOption, 0, len(domain.Cadres))
	for _, cadre := range domain.Cadres {
		cadres = append(cadres, cadreOption{
			Value:    cadre,
			Label:    cadre.Label(),
			Level:    string(cadre.PlacementLevel()),
			Selected: cadre == c.Cadre,
		})
	}
	sexes := make([]sexOption, 0, len(domain.Sexes))
	for _, sex := range domain.Sexes {
		sexes = append(sexes, sexOption{Value: sex, Label: sex.Label(), Selected: sex == c.Sex})
	}
	opts := make([]districtOption, 0, len(districts))
	for _, d := range districts {
		opts = append(opts, districtOption{
			ID: d.ID, Name: d.Name,
			Selected: prefill[string(domain.LevelDistrict)] == d.ID,
		})
	}
	// A district user has exactly one district; preselect it so the cascade
	// starts one step further along.
	if len(opts) == 1 && !sc.IsNational() {
		opts[0].Selected = true
	}
	if errs == nil {
		errs = map[string]string{}
	}

	return chwFormPage{
		Action:    action,
		CHW:       c,
		Age:       age,
		Cadres:    cadres,
		Sexes:     sexes,
		Districts: opts,
		Prefill:   prefill,
		Errors:    errs,
	}, nil
}

func (s *Server) rerenderCHWForm(w http.ResponseWriter, r *http.Request, c domain.CHW, action string, errs map[string]string, age string) {
	p, err := s.chwForm(r, c, action, errs, age)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusUnprocessableEntity, "chw_form", p)
}

// chwWriteFailed turns the write errors a form can provoke into field
// messages, and everything else into a 500.
func (s *Server) chwWriteFailed(w http.ResponseWriter, r *http.Request, err error, draft domain.CHW, action, age string) {
	switch {
	case errors.Is(err, domain.ErrConflict):
		s.rerenderCHWForm(w, r, draft, action, map[string]string{
			"nin": "That NIN is already on another record.",
		}, age)
	case errors.Is(err, domain.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, domain.ErrForbidden):
		s.forbidden(w, r)
	default:
		s.fail(w, r, err)
	}
}

// draftCHW turns rejected input back into a CHW so the form redisplays what
// was typed instead of clearing it.
func draftCHW(in store.CHWInput) domain.CHW {
	return domain.CHW{
		NIN:        in.NIN,
		FirstName:  in.FirstName,
		LastName:   in.LastName,
		Sex:        in.Sex,
		Cadre:      in.Cadre,
		AgeYears:   in.AgeYears,
		LocationID: in.LocationID,
		Status:     domain.CHWActive,
	}
}

func chwPath(id int64) string { return "/chws/" + strconv.FormatInt(id, 10) }
