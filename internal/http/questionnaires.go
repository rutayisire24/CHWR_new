package http

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"hwr/internal/auth"
	"hwr/internal/domain"
	"hwr/internal/store"
)

// The questionnaires are national vocabulary, administered by the national
// admin at /profiles the way cadres are at /cadres. A profile and a question
// are rows, so a new survey question is a form here, not a migration. The
// schema freezes what answers depend on — a question's code, type, range and
// existing choices, once anyone has answered it — so the edit form offers only
// what it would accept: rewording, help, order, required, retiring, and more
// choices.

type profilesPage struct {
	Profiles []profileListRow
}

type profileListRow struct {
	store.ProfileRow
	Cadres string
}

type profilePage struct {
	Row       store.ProfileRow
	Cadres    []cadreChoice
	Answered  map[string]bool
	DataTypes []domain.DataType
	Errors    map[string]string
	// New is the question form as last posted, so a refusal redisplays it.
	New questionDraft
}

type cadreChoice struct {
	ID       int16
	Label    string
	Selected bool
}

type questionDraft struct {
	Code, Prompt, Help, DataType, Options, Min, Max, DependsOn, DependsOnOption, SubsetOf string
	Multi, Required                                                                       bool
}

var (
	questionCode = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}$`)
	optionCode   = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)
)

func (s *Server) profilesList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Profiles.List(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cadres, err := s.store.Cadres.All(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	label := map[int16]string{}
	for _, c := range cadres {
		label[c.ID] = c.Label
	}
	page := profilesPage{}
	for _, row := range rows {
		var names []string
		for _, id := range row.CadreIDs {
			names = append(names, label[id])
		}
		page.Profiles = append(page.Profiles, profileListRow{ProfileRow: row, Cadres: strings.Join(names, ", ")})
	}
	s.render(w, r, http.StatusOK, "profiles", page)
}

func (s *Server) profileShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	s.renderProfile(w, r, http.StatusOK, int16(id), questionDraft{DataType: string(domain.DataYesNo)}, nil)
}

func (s *Server) renderProfile(w http.ResponseWriter, r *http.Request, status int, id int16, draft questionDraft, errs map[string]string) {
	row, err := s.store.Profiles.Row(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	answered, err := s.store.Profiles.AnsweredQuestions(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	choices, err := s.cadreChoices(r, row.CadreIDs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if errs == nil {
		errs = map[string]string{}
	}
	s.render(w, r, status, "profile", profilePage{
		Row: row, Cadres: choices, Answered: answered, Errors: errs, New: draft,
		DataTypes: []domain.DataType{domain.DataYesNo, domain.DataCharacter, domain.DataInteger,
			domain.DataNumeric, domain.DataText, domain.DataDate, domain.DataMonth},
	})
}

func (s *Server) cadreChoices(r *http.Request, selected []int16) ([]cadreChoice, error) {
	cadres, err := s.store.Cadres.All(r.Context())
	if err != nil {
		return nil, err
	}
	on := map[int16]bool{}
	for _, id := range selected {
		on[id] = true
	}
	var out []cadreChoice
	for _, c := range cadres {
		out = append(out, cadreChoice{ID: c.ID, Label: c.CategoryLabel + " — " + c.Label, Selected: on[c.ID]})
	}
	return out, nil
}

// profileSave creates a profile (POST /profiles) or updates one.
func (s *Server) profileSave(w http.ResponseWriter, r *http.Request) {
	var id int16
	if raw := r.PathValue("id"); raw != "" {
		n, ok := pathID(r, "id")
		if !ok {
			s.notFound(w, r)
			return
		}
		id = int16(n)
	}
	in := store.ProfileInput{
		Code:        strings.ToLower(trimmed(r, "code")),
		Name:        trimmed(r, "name"),
		Description: trimmed(r, "description"),
		Active:      id == 0 || r.PostForm.Get("active") == "1",
	}
	for _, raw := range r.PostForm["cadre_id"] {
		n, err := strconv.ParseInt(raw, 10, 16)
		if err != nil {
			s.notFound(w, r)
			return
		}
		in.CadreIDs = append(in.CadreIDs, int16(n))
	}
	switch {
	case id == 0 && !codePattern.MatchString(in.Code):
		setFlash(w, s.secure(), "error", "The profile code: "+codeRule)
		http.Redirect(w, r, "/profiles", http.StatusSeeOther)
		return
	case in.Name == "":
		setFlash(w, s.secure(), "error", "Give the profile a name.")
		http.Redirect(w, r, "/profiles", http.StatusSeeOther)
		return
	}

	saved, err := s.store.Profiles.SaveProfile(r.Context(), auth.ScopeFrom(r.Context()), auth.MustUser(r.Context()), id, in, s.clientIP(r))
	switch {
	case errors.Is(err, domain.ErrConflict):
		setFlash(w, s.secure(), "error", "Another profile already has that code.")
		http.Redirect(w, r, "/profiles", http.StatusSeeOther)
		return
	case err != nil:
		s.notFoundOrFail(w, r, err)
		return
	}
	setFlash(w, s.secure(), "ok", "Saved "+in.Name+".")
	http.Redirect(w, r, fmt.Sprintf("/profiles/%d", saved), http.StatusSeeOther)
}

// questionAdd adds a question to a profile.
func (s *Server) questionAdd(w http.ResponseWriter, r *http.Request) {
	n, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	id := int16(n)
	row, err := s.store.Profiles.Row(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	draft := questionDraft{
		Code: strings.ToLower(trimmed(r, "code")), Prompt: trimmed(r, "prompt"), Help: trimmed(r, "help"),
		DataType: trimmed(r, "data_type"), Options: r.PostForm.Get("options"),
		Min: trimmed(r, "min"), Max: trimmed(r, "max"),
		DependsOn: trimmed(r, "depends_on"), DependsOnOption: trimmed(r, "depends_on_option"),
		SubsetOf: trimmed(r, "subset_of"),
		Multi:    r.PostForm.Get("multi") == "1", Required: r.PostForm.Get("required") == "1",
	}
	in, v := decodeQuestion(draft, row.Profile)
	if !v.Any() {
		err = s.store.Profiles.AddQuestion(r.Context(), auth.ScopeFrom(r.Context()), auth.MustUser(r.Context()), id, in, s.clientIP(r))
		switch {
		case err == nil:
			setFlash(w, s.secure(), "ok", "Added the question "+in.Code+".")
			http.Redirect(w, r, fmt.Sprintf("/profiles/%d", id), http.StatusSeeOther)
			return
		case errors.Is(err, domain.ErrConflict):
			v.Add("code", "Another question already has that code.")
		case errors.Is(err, domain.ErrRefused):
			v.Add("options", strings.TrimPrefix(err.Error(), "edit profile "+strconv.Itoa(int(id))+": "))
		default:
			s.fail(w, r, err)
			return
		}
	}
	s.renderProfile(w, r, http.StatusUnprocessableEntity, id, draft, v.Fields)
}

// decodeQuestion reads the add-question form. Choices are one per line,
// `code = Prompt`; a bare word is both. A yes/no question gets its two choices
// whatever was typed.
func decodeQuestion(d questionDraft, p domain.Profile) (store.QuestionInput, *domain.ValidationError) {
	v := domain.NewValidationError()
	in := store.QuestionInput{
		Code: d.Code, Prompt: d.Prompt, Help: d.Help, Multi: d.Multi, Required: d.Required,
		DataType: domain.DataType(d.DataType), Active: true,
		DependsOn: d.DependsOn, DependsOnOption: d.DependsOnOption, SubsetOf: d.SubsetOf,
		SortOrder: int16(len(p.Questions) + 1),
	}
	if !questionCode.MatchString(in.Code) {
		v.Add("code", "2–48 characters: lower-case letters, digits and underscores, starting with a letter.")
	} else if _, taken := p.Question(in.Code); taken {
		v.Add("code", "This profile already asks a question with that code.")
	}
	if in.Prompt == "" {
		v.Add("prompt", "Write the question as it is asked.")
	}

	switch in.DataType {
	case domain.DataYesNo:
		in.Multi = false
		in.Options = []domain.Option{{ID: 1, Code: "yes", Prompt: "Yes"}, {ID: 2, Code: "no", Prompt: "No"}}
	case domain.DataCharacter:
		in.Options = parseOptions(d.Options, in.Multi, v)
		if len(in.Options) == 0 && !v.Any() {
			v.Add("options", "A list question needs its choices, one per line.")
		}
	case domain.DataInteger, domain.DataNumeric:
		in.Min = optionalFloat(d.Min, "min", v)
		in.Max = optionalFloat(d.Max, "max", v)
		in.Multi = false
	case domain.DataText, domain.DataDate, domain.DataMonth:
		in.Multi = false
	default:
		v.Add("data_type", "Choose what kind of answer it takes.")
	}

	if in.DependsOn != "" {
		parent, ok := p.Question(in.DependsOn)
		if !ok || !parent.Closed || parent.Multi {
			v.Add("depends_on", "A branch hangs on a single-choice question of this profile.")
		} else if _, ok := parent.Option(in.DependsOnOption); !ok {
			v.Add("depends_on", fmt.Sprintf("%s has no choice %q.", parent.Code, in.DependsOnOption))
		}
	}
	if in.SubsetOf != "" {
		parent, ok := p.Question(in.SubsetOf)
		if !ok || !parent.Closed || !in.Multi {
			v.Add("subset_of", "A subset is a list question whose answers are drawn from another list question's.")
		} else {
			in.Options = parent.Options
		}
	}
	return in, v
}

func parseOptions(raw string, multi bool, v *domain.ValidationError) []domain.Option {
	var out []domain.Option
	if multi {
		out = append(out, domain.Option{ID: 0, Code: domain.NoneOption, Prompt: "None"})
	}
	seen := map[string]bool{domain.NoneOption: multi}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		code, prompt, found := strings.Cut(line, "=")
		code, prompt = strings.ToLower(strings.TrimSpace(code)), strings.TrimSpace(prompt)
		if !found {
			prompt = strings.TrimSpace(line)
			code = strings.ToLower(strings.Join(strings.Fields(line), "_"))
		}
		if !optionCode.MatchString(code) || prompt == "" {
			v.Add("options", fmt.Sprintf("%q is not `code = Prompt`, with a code of lower-case letters, digits and underscores.", line))
			return nil
		}
		if seen[code] {
			v.Add("options", fmt.Sprintf("The choice %q appears twice.", code))
			return nil
		}
		seen[code] = true
		out = append(out, domain.Option{ID: int32(len(out) + boolInt(!multi)), Code: code, Prompt: prompt})
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func optionalFloat(raw, field string, v *domain.ValidationError) *float64 {
	if raw == "" {
		return nil
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
	if err != nil {
		v.Add(field, "Not a number.")
		return nil
	}
	return &f
}

// questionUpdate rewords, reorders, retires or restores a question, and adds
// choices to a list question.
func (s *Server) questionUpdate(w http.ResponseWriter, r *http.Request) {
	n, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	id := int16(n)
	code := r.PathValue("code")
	row, err := s.store.Profiles.Row(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	q, ok := row.Question(code)
	if !ok {
		s.notFound(w, r)
		return
	}
	order, err := strconv.ParseInt(trimmed(r, "sort_order"), 10, 16)
	if err != nil {
		order = int64(q.SortOrder)
	}
	in := store.QuestionInput{
		Prompt: trimmed(r, "prompt"), Help: trimmed(r, "help"), SortOrder: int16(order),
		Required: r.PostForm.Get("required") == "1", Active: r.PostForm.Get("active") == "1",
	}
	if in.Prompt == "" {
		setFlash(w, s.secure(), "error", "A question needs its wording.")
		http.Redirect(w, r, fmt.Sprintf("/profiles/%d#q-%s", id, code), http.StatusSeeOther)
		return
	}
	// New choices are appended after the existing ones, which keep their ids
	// and codes: answers hold the ids.
	if added := r.PostForm.Get("add_options"); strings.TrimSpace(added) != "" && q.Closed && q.DataType != domain.DataYesNo {
		v := domain.NewValidationError()
		extra := parseOptions(added, false, v)
		if v.Any() {
			setFlash(w, s.secure(), "error", v.Fields["options"])
			http.Redirect(w, r, fmt.Sprintf("/profiles/%d#q-%s", id, code), http.StatusSeeOther)
			return
		}
		next := int32(0)
		for _, o := range q.Options {
			next = max(next, o.ID)
		}
		in.Options = append(in.Options, q.Options...)
		for _, o := range extra {
			if _, taken := q.Option(o.Code); taken {
				setFlash(w, s.secure(), "error", "The choice "+o.Code+" is already offered.")
				http.Redirect(w, r, fmt.Sprintf("/profiles/%d#q-%s", id, code), http.StatusSeeOther)
				return
			}
			next++
			o.ID = next
			in.Options = append(in.Options, o)
		}
	}

	if err := s.store.Profiles.UpdateQuestion(r.Context(), auth.ScopeFrom(r.Context()), auth.MustUser(r.Context()),
		id, code, in, s.clientIP(r)); err != nil {
		if errors.Is(err, domain.ErrRefused) {
			setFlash(w, s.secure(), "error", "That change was refused: "+err.Error())
			http.Redirect(w, r, fmt.Sprintf("/profiles/%d#q-%s", id, code), http.StatusSeeOther)
			return
		}
		s.notFoundOrFail(w, r, err)
		return
	}
	setFlash(w, s.secure(), "ok", "Saved "+code+".")
	http.Redirect(w, r, fmt.Sprintf("/profiles/%d#q-%s", id, code), http.StatusSeeOther)
}
