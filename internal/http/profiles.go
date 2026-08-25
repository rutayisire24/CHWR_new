package http

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"chwr/internal/auth"
	"chwr/internal/domain"
	"chwr/internal/store"
)

// phonePattern is the source form's own regex: nine digits, no country code
// and no leading zero. The schema carries the same CHECK.
var phonePattern = regexp.MustCompile(`^[0-9]{9}$`)

type profileFormPage struct {
	CHW         domain.CHW
	Profile     domain.Profile
	Facilities  []facilityOption
	Educations  []educationOption
	Frequencies []frequencyOption
	Tools       []domain.CHWTool
	Domains     []domain.CHWServiceDomain
	Years       []int
	Months      []monthOption
	Errors      map[string]string
}

type facilityOption struct {
	ID       int64
	Label    string
	Selected bool
}

type educationOption struct {
	Value    domain.EducationLevel
	Label    string
	Selected bool
}

type frequencyOption struct {
	Value    domain.IncentiveFrequency
	Label    string
	Selected bool
}

type monthOption struct {
	Value    int
	Label    string
	Selected bool
}

func (s *Server) chwProfileForm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.profileForm(r, id, domain.Profile{}, false, nil)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "chw_profile", p)
}

// chwProfileSave writes the profile and both junction sets. The submitted set
// is the new state — the junctions are the answer to a multi-select, so an
// unticked box means "no", not "unchanged".
func (s *Server) chwProfileSave(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	chw, err := s.store.CHWs.Get(r.Context(), sc, id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	in, draft, v := decodeProfile(r, chw)

	// A CHW's facility must be in the CHW's own district: the attachment is a
	// reporting line, not a placement, and a CHW reporting across a district
	// boundary is invisible to whoever supervises them. The picker only offers
	// this district, so reaching here means the form was tampered with —
	// chw_profiles_facility_district_trg would refuse it either way, but as a
	// 500 rather than a message.
	if in.FacilityID != nil {
		districtID, err := s.store.Profiles.FacilityDistrict(r.Context(), *in.FacilityID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			v.Add("facility_id", "That facility no longer exists.")
		case err != nil:
			s.fail(w, r, err)
			return
		case districtID != chw.DistrictID:
			v.Add("facility_id", "Choose a facility in "+chw.DistrictName+" district.")
		}
	}

	if v.Any() {
		p, err := s.profileForm(r, id, draft, true, v.Fields)
		if err != nil {
			s.notFoundOrFail(w, r, err)
			return
		}
		p.Tools = mergeTools(p.Tools, in.Tools)
		p.Domains = mergeDomains(p.Domains, in.Domains)
		s.render(w, r, http.StatusUnprocessableEntity, "chw_profile", p)
		return
	}

	if _, err := s.store.Profiles.Save(r.Context(), sc, actor, id, in, s.clientIP(r)); err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	setFlash(w, s.secure(), "ok", "Saved the profile for "+chw.FullName()+".")
	http.Redirect(w, r, chwPath(id), http.StatusSeeOther)
}

// decodeProfile reads the profile form. Every CHECK in chw_profiles is
// mirrored here, because a CHECK violation is a 500 and a field message is not;
// the schema remains the guarantee.
func decodeProfile(r *http.Request, chw domain.CHW) (store.ProfileInput, domain.Profile, *domain.ValidationError) {
	v := domain.NewValidationError()
	in := store.ProfileInput{}

	// Phone. The form asks whether the CHW owns one and branches: an owner has
	// a primary number and may report on it; a non-owner may leave an
	// alternate number to be reached on. phone_branch_exclusive refuses any
	// crossing of the two.
	owns := triState(r, "owns_phone")
	in.OwnsPhone = owns
	switch {
	case isTrue(owns):
		in.PhonePrimary = digitsOnly(trimmed(r, "phone_primary"))
		in.PhoneForReporting = boolPtr(r.PostForm.Get("phone_for_reporting") == "yes")
		if in.PhonePrimary == "" {
			v.Add("phone_primary", "Give the number, or answer no to owning a phone.")
		} else if !phonePattern.MatchString(in.PhonePrimary) {
			v.Add("phone_primary", "Nine digits, without the country code — for example 772123456.")
		}
	case isFalse(owns):
		in.PhoneAlternate = digitsOnly(trimmed(r, "phone_alternate"))
		if in.PhoneAlternate != "" && !phonePattern.MatchString(in.PhoneAlternate) {
			v.Add("phone_alternate", "Nine digits, without the country code — for example 772123456.")
		}
	}

	// Supervising facility. It is an attachment, not a placement, and the
	// trigger refuses one in another district; the picker only offers this
	// CHW's district, so a mismatch means the form was tampered with.
	if id, wellFormed := optionalID(r, "facility_id"); !wellFormed {
		v.Add("facility_id", "Choose a facility from the list.")
	} else {
		in.FacilityID = id
	}

	if year, ok := optionalInt(r, "service_start_year", 1960, 2100, v, "service_start_year",
		"Give the year they started, between 1960 and 2100, or leave it blank."); ok && year != nil {
		y := int16(*year)
		in.ServiceStartYear = &y
	}
	if households, ok := optionalInt(r, "households_served", 3, 100000, v, "households_served",
		"Households served must be between 3 and 100,000, or blank."); ok && households != nil {
		h := int32(*households)
		in.HouseholdsServed = &h
	}

	if education := domain.EducationLevel(trimmed(r, "education")); education != "" {
		if !education.Valid() {
			v.Add("education", "Choose a level from the list.")
		} else {
			in.Education = education
		}
	}

	// English is a multi-select in the source form, not a yes/no, so the three
	// proficiencies are kept apart rather than collapsed.
	if r.PostForm.Get("english_asked") == "1" {
		in.EnglishSpeak = boolPtr(r.PostForm.Get("english_speak") == "yes")
		in.EnglishRead = boolPtr(r.PostForm.Get("english_read") == "yes")
		in.EnglishWrite = boolPtr(r.PostForm.Get("english_write") == "yes")
	}
	in.OtherLanguagesRaw = trimmed(r, "other_languages_raw")

	// Incentive. incentive_details_require_yes refuses a frequency or an amount
	// unless the answer was yes.
	receives := triState(r, "receives_incentive")
	in.ReceivesIncentive = receives
	if isTrue(receives) {
		frequency := domain.IncentiveFrequency(trimmed(r, "incentive_frequency"))
		if frequency != "" && !frequency.Valid() {
			v.Add("incentive_frequency", "Choose how often it is received.")
		} else {
			in.IncentiveFrequency = frequency
		}
		if amount, ok := optionalInt(r, "incentive_amount_ugx", 1000, 500000, v, "incentive_amount_ugx",
			"An incentive is between UGX 1,000 and 500,000, or leave it blank."); ok && amount != nil {
			a := int32(*amount)
			in.IncentiveAmountUGX = &a
		}
	}

	// Supervision as year + month by decision: the source form records it per
	// service domain and carries no date. The stored value is the first of the
	// month, which supervision_date_requires_yes and the day CHECK both police.
	supervised := triState(r, "received_supervision")
	in.ReceivedSupervision = supervised
	if isTrue(supervised) {
		year := trimmed(r, "supervised_year")
		month := trimmed(r, "supervised_month")
		switch {
		case year == "" && month == "":
			// Supervised, but when was not recorded. Allowed: the CHECK only
			// forbids a date without a yes, not a yes without a date.
		case year == "" || month == "":
			v.Add("last_supervised_on", "Give both the year and the month, or neither.")
		default:
			y, errY := strconv.Atoi(year)
			m, errM := strconv.Atoi(month)
			if errY != nil || errM != nil || m < 1 || m > 12 || y < 2000 || y > time.Now().Year() {
				v.Add("last_supervised_on", "Choose a month and a year no later than this one.")
			} else {
				when := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
				if when.After(time.Now()) {
					v.Add("last_supervised_on", "That month has not happened yet.")
				} else {
					in.LastSupervisedOn = &when
				}
			}
		}
	}

	in.Tools = decodeTools(r)
	in.Domains = decodeDomains(r)

	return in, draftProfile(in, chw), v
}

// decodeTools reads the tool checklist. Condition is only meaningful for a tool
// the CHW actually holds — the form choice-filters it the same way.
func decodeTools(r *http.Request) []store.ToolInput {
	var out []store.ToolInput
	for _, raw := range r.PostForm["tool"] {
		id, err := strconv.ParseInt(raw, 10, 16)
		if err != nil {
			continue
		}
		t := store.ToolInput{ToolID: int16(id)}
		switch r.PostForm.Get("tool_functional_" + raw) {
		case "yes":
			t.Functional = boolPtr(true)
		case "no":
			t.Functional = boolPtr(false)
		}
		out = append(out, t)
	}
	return out
}

// decodeDomains reads the service-domain grid. trained_implies_provides is the
// form's own choice_filter and the schema's CHECK; a submission that breaks it
// has been tampered with, so it is corrected to the safe reading rather than
// rejected with a message nobody would understand.
func decodeDomains(r *http.Request) []store.DomainInput {
	provides := map[string]bool{}
	for _, raw := range r.PostForm["provides"] {
		provides[raw] = true
	}

	var out []store.DomainInput
	for raw := range provides {
		id, err := strconv.ParseInt(raw, 10, 16)
		if err != nil {
			continue
		}
		out = append(out, store.DomainInput{
			DomainID: int16(id),
			Provides: true,
			Trained:  contains(r.PostForm["trained"], raw),
		})
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// profileForm assembles the vocabularies, the district's facilities and the
// supervision date selects.
func (s *Server) profileForm(r *http.Request, chwID int64, draft domain.Profile, useDraft bool, errs map[string]string) (profileFormPage, error) {
	sc := auth.ScopeFrom(r.Context())

	chw, err := s.store.CHWs.Get(r.Context(), sc, chwID)
	if err != nil {
		return profileFormPage{}, err
	}

	profile := draft
	if !useDraft {
		if profile, err = s.store.Profiles.Get(r.Context(), sc, chwID); err != nil {
			return profileFormPage{}, err
		}
	}

	tools, err := s.store.Profiles.Tools(r.Context(), chwID)
	if err != nil {
		return profileFormPage{}, err
	}
	domains, err := s.store.Profiles.ServiceDomains(r.Context(), chwID)
	if err != nil {
		return profileFormPage{}, err
	}
	facilities, err := s.store.Profiles.FacilitiesIn(r.Context(), sc, chw.DistrictID)
	if err != nil {
		return profileFormPage{}, err
	}

	options := make([]facilityOption, 0, len(facilities))
	for _, f := range facilities {
		label := f.Name
		if f.Level != "" {
			label += " · " + f.Level
		}
		if f.Ownership != "" && f.Ownership != "GOV" {
			label += " (" + f.Ownership + ")"
		}
		options = append(options, facilityOption{
			ID: f.ID, Label: label,
			Selected: profile.FacilityID != nil && *profile.FacilityID == f.ID,
		})
	}

	educations := make([]educationOption, 0, len(domain.EducationLevels))
	for _, e := range domain.EducationLevels {
		educations = append(educations, educationOption{Value: e, Label: e.Label(), Selected: e == profile.Education})
	}
	frequencies := make([]frequencyOption, 0, len(domain.IncentiveFrequencies))
	for _, f := range domain.IncentiveFrequencies {
		frequencies = append(frequencies, frequencyOption{Value: f, Label: f.Label(), Selected: f == profile.IncentiveFrequency})
	}

	// Supervision is captured as year + month, so the selects are built here
	// rather than a date input whose day would then have to be discarded.
	thisYear := time.Now().Year()
	years := make([]int, 0, 11)
	for y := thisYear; y >= thisYear-10; y-- {
		years = append(years, y)
	}
	months := make([]monthOption, 0, 12)
	for m := 1; m <= 12; m++ {
		months = append(months, monthOption{
			Value: m, Label: time.Month(m).String(),
			Selected: profile.LastSupervisedOn != nil && int(profile.LastSupervisedOn.Month()) == m,
		})
	}
	if errs == nil {
		errs = map[string]string{}
	}

	return profileFormPage{
		CHW:         chw,
		Profile:     profile,
		Facilities:  options,
		Educations:  educations,
		Frequencies: frequencies,
		Tools:       tools,
		Domains:     domains,
		Years:       years,
		Months:      months,
		Errors:      errs,
	}, nil
}

// mergeTools and mergeDomains put the submitted selections back onto the
// vocabulary lists, so a rejected form redisplays what was ticked.
func mergeTools(vocab []domain.CHWTool, chosen []store.ToolInput) []domain.CHWTool {
	held := map[int16]*bool{}
	for _, t := range chosen {
		held[t.ToolID] = t.Functional
	}
	for i := range vocab {
		functional, ok := held[vocab[i].ID]
		vocab[i].Held = ok
		vocab[i].Functional = functional
	}
	return vocab
}

func mergeDomains(vocab []domain.CHWServiceDomain, chosen []store.DomainInput) []domain.CHWServiceDomain {
	byID := map[int16]store.DomainInput{}
	for _, d := range chosen {
		byID[d.DomainID] = d
	}
	for i := range vocab {
		d, ok := byID[vocab[i].ID]
		vocab[i].Provides = ok && d.Provides
		vocab[i].Trained = ok && d.Trained
	}
	return vocab
}

// draftProfile turns rejected input back into a Profile so the form
// redisplays what was typed.
func draftProfile(in store.ProfileInput, chw domain.CHW) domain.Profile {
	return domain.Profile{
		CHWID:               chw.ID,
		Exists:              true,
		OwnsPhone:           in.OwnsPhone,
		PhonePrimary:        in.PhonePrimary,
		PhoneForReporting:   in.PhoneForReporting,
		PhoneAlternate:      in.PhoneAlternate,
		FacilityID:          in.FacilityID,
		ServiceStartYear:    in.ServiceStartYear,
		HouseholdsServed:    in.HouseholdsServed,
		Education:           in.Education,
		EnglishSpeak:        in.EnglishSpeak,
		EnglishRead:         in.EnglishRead,
		EnglishWrite:        in.EnglishWrite,
		OtherLanguagesRaw:   in.OtherLanguagesRaw,
		ReceivesIncentive:   in.ReceivesIncentive,
		IncentiveFrequency:  in.IncentiveFrequency,
		IncentiveAmountUGX:  in.IncentiveAmountUGX,
		ReceivedSupervision: in.ReceivedSupervision,
		LastSupervisedOn:    in.LastSupervisedOn,
	}
}

// triState reads a yes / no / not-asked radio group. "Not asked" is a real
// answer here: every profile column is nullable because an imported record may
// answer none of them.
func triState(r *http.Request, field string) *bool {
	switch r.PostForm.Get(field) {
	case "yes":
		return boolPtr(true)
	case "no":
		return boolPtr(false)
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }
func isTrue(b *bool) bool  { return b != nil && *b }
func isFalse(b *bool) bool { return b != nil && !*b }

// optionalInt reads a numeric field that may be blank, bounded by the same
// range the schema's CHECK uses.
func optionalInt(r *http.Request, field string, min, max int, v *domain.ValidationError, errField, message string) (*int, bool) {
	raw := trimmed(r, field)
	if raw == "" {
		return nil, true
	}
	n, err := strconv.Atoi(strings.ReplaceAll(raw, ",", ""))
	if err != nil || n < min || n > max {
		v.Add(errField, message)
		return nil, false
	}
	return &n, true
}

// digitsOnly strips the spaces, dashes and leading zero people type into a
// phone field, so a number that is right gets stored rather than rejected on
// punctuation.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	out = strings.TrimPrefix(out, "256")
	return strings.TrimPrefix(out, "0")
}
