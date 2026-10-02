package http

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"hwr/internal/auth"
	"hwr/internal/domain"
)

// The survey form is built from the questionnaire, not written by hand: a
// question's type decides its input, its branch and subset decide what the
// script hides and disables, and a new question appears on the form the
// moment its row lands. The schema checks every answer; the form checks them
// first so a mistake comes back as a field message rather than a 500.

// facilityOption is one row of the supervising-facility picker on the worker
// page: an attachment on the open posting, not part of the survey.
type facilityOption struct {
	ID       int64
	Label    string
	Selected bool
}

type profileFormPage struct {
	Worker  domain.HealthWorker
	Profile domain.Profile
	Fields  []questionField
	Errors  map[string]string
	// Latest is the submission the form was prefilled from, empty for a
	// worker who has never answered.
	Latest domain.Submission
}

// questionField is one question as the form draws it.
type questionField struct {
	domain.Question
	// Input is the control: radio, checkbox, number, month, date or text.
	Input   string
	Value   string // an open answer, as it will be shown
	Choices []choiceField
	// Unanswered checks the "Not asked" radio of a single choice.
	Unanswered bool
	// Range is the bounds of a number, spelled out for the hint.
	Range string
	Error string
}

type choiceField struct {
	Code    string
	Prompt  string
	Checked bool
}

// inputFor is the control a question is drawn with.
func inputFor(q domain.Question) string {
	switch {
	case q.Closed && q.Multi:
		return "checkbox"
	case q.Closed:
		return "radio"
	}
	switch q.DataType {
	case domain.DataInteger, domain.DataNumeric:
		return "number"
	case domain.DataMonth:
		return "month"
	case domain.DataDate:
		return "date"
	}
	return "text"
}

// surveyFields lays the active questions out with their answers.
func surveyFields(p domain.Profile, answers domain.Answers, errs map[string]string) []questionField {
	var out []questionField
	for _, q := range p.Questions {
		if !q.Active {
			continue
		}
		f := questionField{Question: q, Input: inputFor(q), Error: errs[q.Code]}
		values := answers[q.Code]
		f.Unanswered = len(values) == 0
		if q.Min != nil && q.Max != nil {
			f.Range = fmt.Sprintf("%s to %s", groupFloat(*q.Min), groupFloat(*q.Max))
		}
		if q.Closed {
			for _, o := range q.Options {
				f.Choices = append(f.Choices, choiceField{Code: o.Code, Prompt: o.Prompt,
					Checked: slices.Contains(values, o.Code)})
			}
		} else if len(values) > 0 {
			f.Value = values[0]
			if q.DataType == domain.DataMonth && len(f.Value) >= 7 {
				f.Value = f.Value[:7] // <input type=month> speaks YYYY-MM
			}
		}
		out = append(out, f)
	}
	return out
}

// surveyFor resolves the worker and the profile the request names — ?profile=,
// else the first that applies to their cadre. A profile that does not apply to
// the worker's cadre has no form for them: the same answer the show page gives
// by not linking to it.
func (s *Server) surveyFor(r *http.Request, workerID int64) (domain.HealthWorker, domain.Profile, error) {
	sc := auth.ScopeFrom(r.Context())
	worker, err := s.store.Workers.Get(r.Context(), sc, workerID)
	if err != nil {
		return domain.HealthWorker{}, domain.Profile{}, err
	}
	if worker.Deployment == nil {
		return domain.HealthWorker{}, domain.Profile{}, fmt.Errorf("survey for worker %d: %w", workerID, domain.ErrNotFound)
	}
	profiles, err := s.store.Profiles.ForCadre(r.Context(), worker.Deployment.CadreID)
	if err != nil {
		return domain.HealthWorker{}, domain.Profile{}, err
	}
	want := r.URL.Query().Get("profile")
	for _, p := range profiles {
		if want == "" || p.Code == want {
			return worker, p, nil
		}
	}
	return domain.HealthWorker{}, domain.Profile{}, fmt.Errorf("survey %q for worker %d: %w", want, workerID, domain.ErrNotFound)
}

func (s *Server) workerProfileForm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	worker, profile, err := s.surveyFor(r, id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	latest, err := s.store.Profiles.Latest(r.Context(), auth.ScopeFrom(r.Context()), id, profile)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "worker_profile", profileFormPage{
		Worker: worker, Profile: profile, Latest: latest,
		Fields: surveyFields(profile, latest.Answers, nil), Errors: map[string]string{},
	})
}

// workerProfileSave records the form as a new submission. A multi-select's
// ticked boxes are the whole answer — an unticked box means "not chosen", not
// "unchanged" — and a branch the answers close is dropped, because a form posts
// every field it draws whether the reader could see it or not.
func (s *Server) workerProfileSave(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	worker, profile, err := s.surveyFor(r, id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	// The CSRF middleware has already parsed the form.
	answers, v := decodeAnswers(r, profile)
	if !v.Any() {
		answers = profile.Prune(answers)
		_, err = s.store.Profiles.Submit(r.Context(), auth.ScopeFrom(r.Context()), auth.MustUser(r.Context()),
			id, profile, answers, "form", s.clientIP(r))
		var refused *domain.ValidationError
		switch {
		case err == nil:
			setFlash(w, s.secure(), "ok", "Saved the "+profile.Name+" for "+worker.FullName()+".")
			http.Redirect(w, r, workerPath(id), http.StatusSeeOther)
			return
		case errors.As(err, &refused):
			v = refused
		default:
			s.notFoundOrFail(w, r, err)
			return
		}
	}

	s.render(w, r, http.StatusUnprocessableEntity, "worker_profile", profileFormPage{
		Worker: worker, Profile: profile,
		Fields: surveyFields(profile, answers, v.Fields), Errors: v.Fields,
	})
}

// decodeAnswers reads the form into answers by question code. A closed answer
// is an option code, checked by Profile.Check; an open one is parsed by its
// question, which is the same reading an import gets.
func decodeAnswers(r *http.Request, p domain.Profile) (domain.Answers, *domain.ValidationError) {
	v := domain.NewValidationError()
	answers := domain.Answers{}
	for _, q := range p.Questions {
		if !q.Active {
			continue
		}
		var values []string
		for _, raw := range r.PostForm[q.Code] {
			if raw = strings.TrimSpace(raw); raw == "" {
				continue
			}
			if q.Closed {
				values = append(values, raw)
				continue
			}
			parsed, err := q.Parse(raw)
			if err != nil {
				v.Add(q.Code, err.Error())
				continue
			}
			values = append(values, parsed...)
		}
		if len(values) > 0 {
			answers[q.Code] = values
		}
	}
	return answers, v
}

// answerRow is one answered question as the show page reads it.
type answerRow struct {
	Prompt string
	Answer string
}

// surveyView is one profile on the show page: its latest answers, and whether
// the worker's current cadre still answers it.
type surveyView struct {
	Profile     domain.Profile
	Latest      domain.Submission
	Rows        []answerRow
	Applies     bool
	Submissions int
}

// answerRows reads a submission back in the profile's order, answered
// questions only: the page says "not recorded" once rather than fourteen times.
func answerRows(p domain.Profile, a domain.Answers) []answerRow {
	var out []answerRow
	for _, q := range p.Questions {
		values := a[q.Code]
		if len(values) == 0 {
			continue
		}
		labels := make([]string, len(values))
		for i, v := range values {
			labels[i] = q.Label(v)
		}
		out = append(out, answerRow{Prompt: q.Prompt, Answer: strings.Join(labels, ", ")})
	}
	return out
}

// surveysFor assembles every profile the show page has to say something
// about: the ones the worker's cadre answers, and any they answered before a
// recadre took them out of it, which stay readable but are no longer edited.
func (s *Server) surveysFor(r *http.Request, worker domain.HealthWorker) ([]surveyView, error) {
	sc := auth.ScopeFrom(r.Context())
	var applicable []domain.Profile
	if worker.Deployment != nil {
		var err error
		if applicable, err = s.store.Profiles.ForCadre(r.Context(), worker.Deployment.CadreID); err != nil {
			return nil, err
		}
	}
	answered, err := s.store.Profiles.Answered(r.Context(), sc, worker.ID)
	if err != nil {
		return nil, err
	}

	var out []surveyView
	seen := map[int16]bool{}
	add := func(p domain.Profile, applies bool) error {
		if seen[p.ID] {
			return nil
		}
		seen[p.ID] = true
		history, err := s.store.Profiles.History(r.Context(), sc, worker.ID, p)
		if err != nil {
			return err
		}
		view := surveyView{Profile: p, Applies: applies, Submissions: len(history)}
		if len(history) > 0 {
			view.Latest = history[0]
			view.Rows = answerRows(p, history[0].Answers)
		}
		out = append(out, view)
		return nil
	}
	for _, p := range applicable {
		if err := add(p, true); err != nil {
			return nil, err
		}
	}
	for _, p := range answered {
		if err := add(p, false); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// groupFloat writes a bound the way the form's reader writes a number: whole,
// with thousands grouped.
func groupFloat(f float64) string {
	n := strconv.FormatInt(int64(f), 10)
	if f > -10000 && f < 10000 {
		return n // a year is not a quantity: 2100, not 2,100
	}
	for i := len(n) - 3; i > 0 && n[i-1] != '-'; i -= 3 {
		n = n[:i] + "," + n[i:]
	}
	return n
}
