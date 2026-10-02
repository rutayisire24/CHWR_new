package http

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"hwr/internal/auth"
	"hwr/internal/domain"
	"hwr/internal/store"
)

// The person's details are edited from the worker page, one small form per
// kind: add a row, or remove one. A correction is a removal and an addition,
// which is also what the audit trail then says. Every rule the schema holds is
// checked here first, so a mistake comes back as a message on the page rather
// than a constraint violation.

func (s *Server) workerDetailAdd(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	detail := store.Detail(r.PathValue("detail"))
	if !ok || !detail.Valid() {
		s.notFound(w, r)
		return
	}
	sc := auth.ScopeFrom(r.Context())
	actor := auth.MustUser(r.Context())
	ip := s.clientIP(r)
	ctx := r.Context()

	var err error
	var problem string
	switch detail {
	case store.DetailContact:
		var c domain.Contact
		if c, problem = decodeContact(r); problem == "" {
			err = s.store.Persons.AddContact(ctx, sc, actor, id, c, ip)
		}
	case store.DetailKin:
		k := domain.Kin{Name: trimmed(r, "name"), Relationship: trimmed(r, "relationship"),
			Phone: trimmed(r, "phone"), IsEmergency: r.PostForm.Get("is_emergency") == "1"}
		switch {
		case k.Name == "" || k.Relationship == "":
			problem = "Give the next of kin's name and how they are related."
		case k.Phone != "" && !validPhone(&k.Phone):
			problem = "A phone number is nine digits once the spaces, dashes and country code come off."
		default:
			err = s.store.Persons.AddKin(ctx, sc, actor, id, k, ip)
		}
	case store.DetailDocument:
		typeID, perr := strconv.ParseInt(trimmed(r, "identifier_type_id"), 10, 16)
		doc := domain.Document{Type: domain.IdentifierType{ID: int16(typeID)}, Number: strings.ToUpper(trimmed(r, "number"))}
		if perr != nil || doc.Number == "" {
			problem = "Choose the kind of document and give its number."
		} else {
			err = s.store.Persons.AddDocument(ctx, sc, actor, id, doc, ip)
		}
	case store.DetailEducation:
		e := domain.Education{Level: domain.EducationLevel(trimmed(r, "level")),
			Institution: trimmed(r, "institution"), Qualification: trimmed(r, "qualification")}
		var bad bool
		e.YearCompleted, bad = optionalYear(r, "year_completed")
		switch {
		case !e.Level.Valid():
			problem = "Choose the level of education."
		case bad:
			problem = "The year completed is a year between 1940 and 2100."
		default:
			err = s.store.Persons.AddEducation(ctx, sc, actor, id, e, ip)
		}
	case store.DetailCourse:
		c := domain.Course{Course: trimmed(r, "course"), Institution: trimmed(r, "institution"),
			Qualification: trimmed(r, "qualification")}
		var from, to bool
		c.StartedOn, from = optionalDate(r, "started_on")
		c.CompletedOn, to = optionalDate(r, "completed_on")
		switch {
		case c.Course == "":
			problem = "Name the course."
		case from || to || outOfOrder(c.StartedOn, c.CompletedOn):
			problem = "The dates are not in order, or not dates."
		default:
			err = s.store.Persons.AddCourse(ctx, sc, actor, id, c, ip)
		}
	case store.DetailTraining:
		t := domain.Training{Title: trimmed(r, "title"), Provider: trimmed(r, "provider"),
			Certified: triState(r.PostForm.Get("certified"))}
		var from, to bool
		t.StartedOn, from = optionalDate(r, "started_on")
		t.EndedOn, to = optionalDate(r, "ended_on")
		switch {
		case t.Title == "":
			problem = "Name the training."
		case from || to || outOfOrder(t.StartedOn, t.EndedOn):
			problem = "The dates are not in order, or not dates."
		default:
			err = s.store.Persons.AddTraining(ctx, sc, actor, id, t, ip)
		}
	case store.DetailWorkHistory:
		h := domain.WorkHistory{Employer: trimmed(r, "employer"), Position: trimmed(r, "position")}
		var from, to bool
		h.StartedOn, from = optionalDate(r, "started_on")
		h.EndedOn, to = optionalDate(r, "ended_on")
		switch {
		case h.Employer == "":
			problem = "Name the employer."
		case from || to || outOfOrder(h.StartedOn, h.EndedOn):
			problem = "The dates are not in order, or not dates."
		default:
			err = s.store.Persons.AddWorkHistory(ctx, sc, actor, id, h, ip)
		}
	case store.DetailLanguage:
		languageID, perr := strconv.ParseInt(trimmed(r, "language_id"), 10, 16)
		l := domain.LanguageSkill{Language: domain.Language{ID: int16(languageID)},
			Understanding: grade(r, "understanding"), Reading: grade(r, "reading"), Writing: grade(r, "writing")}
		if perr != nil {
			problem = "Choose a language."
		} else {
			err = s.store.Persons.SetLanguage(ctx, sc, actor, id, l, ip)
		}
	}

	switch {
	case problem != "":
		setFlash(w, s.secure(), "error", problem)
	case errors.Is(err, domain.ErrConflict):
		setFlash(w, s.secure(), "error", "That is already recorded.")
	case err != nil:
		s.notFoundOrFail(w, r, err)
		return
	default:
		setFlash(w, s.secure(), "ok", "Recorded.")
	}
	http.Redirect(w, r, workerPath(id)+"#details", http.StatusSeeOther)
}

func (s *Server) workerDetailRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	rowID, rowOK := pathID(r, "row")
	detail := store.Detail(r.PathValue("detail"))
	if !ok || !rowOK || !detail.Valid() {
		s.notFound(w, r)
		return
	}
	if err := s.store.Persons.Remove(r.Context(), auth.ScopeFrom(r.Context()), auth.MustUser(r.Context()),
		id, detail, rowID, s.clientIP(r)); err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	setFlash(w, s.secure(), "ok", "Removed.")
	http.Redirect(w, r, workerPath(id)+"#details", http.StatusSeeOther)
}

// decodeContact reads the contact form against the shapes the schema checks.
func decodeContact(r *http.Request) (domain.Contact, string) {
	c := domain.Contact{
		Kind:      domain.ContactKind(trimmed(r, "kind")),
		Value:     trimmed(r, "value"),
		IsPrimary: r.PostForm.Get("is_primary") == "1",
	}
	switch c.Kind {
	case domain.ContactPhone:
		if !validPhone(&c.Value) {
			return c, "A phone number is nine digits once the spaces, dashes and country code come off."
		}
		c.Owned = triState(r.PostForm.Get("owned"))
		c.ForReporting = triState(r.PostForm.Get("for_reporting"))
	case domain.ContactEmail:
		if at := strings.Index(c.Value, "@"); at < 1 || at == len(c.Value)-1 || strings.ContainsAny(c.Value, " \t") {
			return c, "That is not an email address."
		}
	case domain.ContactAddress:
		if c.Value == "" {
			return c, "Give the address."
		}
	default:
		return c, "Choose the kind of contact."
	}
	return c, ""
}

// validPhone tidies a number in place to the nine digits the schema stores,
// and reports whether that is what it is.
func validPhone(s *string) bool {
	*s = digitsOnly(*s)
	return len(*s) == 9
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
	out := strings.TrimPrefix(b.String(), "256")
	return strings.TrimPrefix(out, "0")
}

// triState reads a yes / no / not-asked control. "Not asked" is a real answer:
// it is what most imported records say.
func triState(value string) *bool {
	switch value {
	case "yes":
		yes := true
		return &yes
	case "no":
		no := false
		return &no
	}
	return nil
}

// grade reads a proficiency select, where blank is "not asked".
func grade(r *http.Request, field string) domain.Proficiency {
	if p := domain.Proficiency(trimmed(r, field)); p.Valid() {
		return p
	}
	return ""
}

// optionalDate reads a date field that may be blank, and reports a value that
// is not a date.
func optionalDate(r *http.Request, field string) (*time.Time, bool) {
	raw := trimmed(r, field)
	if raw == "" {
		return nil, false
	}
	t, ok := domain.ParseDate(raw)
	if !ok {
		return nil, true
	}
	return &t, false
}

// optionalYear reads a year field that may be blank.
func optionalYear(r *http.Request, field string) (*int16, bool) {
	raw := trimmed(r, field)
	if raw == "" {
		return nil, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1940 || n > 2100 {
		return nil, true
	}
	y := int16(n)
	return &y, false
}

func outOfOrder(from, to *time.Time) bool {
	return from != nil && to != nil && to.Before(*from)
}

// workerReportServices records the services a worker gave on a date.
func (s *Server) workerReportServices(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	on, bad := optionalDate(r, "reporting_date")
	if on == nil || bad || on.After(time.Now()) {
		setFlash(w, s.secure(), "error", "Give the date the report is for: today or earlier.")
		http.Redirect(w, r, workerPath(id)+"#services", http.StatusSeeOther)
		return
	}
	var serviceIDs []int16
	for _, raw := range r.PostForm["service_id"] {
		n, err := strconv.ParseInt(raw, 10, 16)
		if err != nil {
			s.notFound(w, r)
			return
		}
		serviceIDs = append(serviceIDs, int16(n))
	}

	_, err := s.store.Activities.ReportServices(r.Context(), auth.ScopeFrom(r.Context()), auth.MustUser(r.Context()),
		id, *on, serviceIDs, s.clientIP(r))
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrForbidden):
		s.notFoundOrFail(w, r, err)
		return
	case errors.Is(err, domain.ErrRefused):
		// The schema refuses a date the worker held no posting on, and a
		// service their cadre then did not give; neither is a server fault.
		setFlash(w, s.secure(), "error", "That report was refused: the worker held no posting on that date, "+
			"or a service chosen did not apply to their cadre then.")
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		setFlash(w, s.secure(), "ok", fmt.Sprintf("Recorded %d service(s) for %s.", len(serviceIDs), on.Format("2 January 2006")))
	}
	http.Redirect(w, r, workerPath(id)+"#services", http.StatusSeeOther)
}
