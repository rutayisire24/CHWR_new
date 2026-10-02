package http

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"hwr/internal/auth"
	"hwr/internal/domain"
	"hwr/internal/store"
)

// The cadre taxonomy, as the national admin edits it. A cadre added here is
// immediately a choice on the worker form, a filter on the listing, a tile on
// the dashboard and a value the importer's cadre column accepts — all four read
// the same rows — so this screen is the whole of "adding a kind of health
// worker", short of a category's own profile.

type cadresPage struct {
	Groups []categorySection
	// Category is the new-category form, redisplayed when it was refused.
	Category       store.CategoryInput
	CategoryErrors map[string]string
}

// categorySection is one category and its cadres, in the order the form shows them.
type categorySection struct {
	Category domain.CadreCategory
	Cadres   []store.CadreRow
	// HasProfile says whether the category carries a profile surface. Only the
	// CHW category does today; the page says so, so an admin adding a nursing
	// category knows its workers get the core record and a posting only.
	HasProfile bool
}

type cadreFormPage struct {
	Action     string
	Cadre      store.CadreRow
	Aliases    string // as typed, one per line
	Categories []domain.CadreCategory
	Levels     []domain.Level
	Errors     map[string]string
}

func (s *Server) cadresList(w http.ResponseWriter, r *http.Request) {
	p, err := s.cadresPage(r, store.CategoryInput{}, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "cadres", p)
}

func (s *Server) cadresPage(r *http.Request, draft store.CategoryInput, errs map[string]string) (cadresPage, error) {
	categories, err := s.store.Cadres.Categories(r.Context())
	if err != nil {
		return cadresPage{}, err
	}
	cadres, err := s.store.Cadres.All(r.Context())
	if err != nil {
		return cadresPage{}, err
	}

	groups := make([]categorySection, 0, len(categories))
	at := make(map[int16]int, len(categories))
	for i, c := range categories {
		groups = append(groups, categorySection{Category: c, HasProfile: c.Code == domain.CategoryCHW})
		at[c.ID] = i
	}
	for _, c := range cadres {
		if i, ok := at[c.CategoryID]; ok {
			groups[i].Cadres = append(groups[i].Cadres, c)
		}
	}
	if errs == nil {
		errs = map[string]string{}
	}
	return cadresPage{Groups: groups, Category: draft, CategoryErrors: errs}, nil
}

func (s *Server) cadreNew(w http.ResponseWriter, r *http.Request) {
	draft := store.CadreRow{}
	draft.Active = true
	draft.PlacementLevel = domain.LevelVillage
	// Arriving from a category's "Add a cadre" link preselects it.
	if id, err := strconv.ParseInt(r.URL.Query().Get("category"), 10, 16); err == nil {
		draft.CategoryID = int16(id)
	}
	s.renderCadreForm(w, r, http.StatusOK, draft, "", "/cadres/new", nil)
}

func (s *Server) cadreCreate(w http.ResponseWriter, r *http.Request) {
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	in, aliasText, v := decodeCadre(r)
	s.checkCadre(r, 0, in, v)
	if v.Any() {
		s.renderCadreForm(w, r, http.StatusUnprocessableEntity, draftCadre(0, in), aliasText, "/cadres/new", v.Fields)
		return
	}

	created, err := s.store.Cadres.Create(r.Context(), sc, actor, in, s.clientIP(r))
	if errors.Is(err, domain.ErrConflict) {
		v.Add("code", "Another cadre already has that code.")
		s.renderCadreForm(w, r, http.StatusUnprocessableEntity, draftCadre(0, in), aliasText, "/cadres/new", v.Fields)
		return
	}
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	setFlash(w, s.secure(), "ok", created.Label+" can now be chosen on the worker form and named in an import.")
	http.Redirect(w, r, "/cadres", http.StatusSeeOther)
}

func (s *Server) cadreEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := cadreID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	c, err := s.store.Cadres.Get(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	s.renderCadreForm(w, r, http.StatusOK, c, strings.Join(c.ImportAliases, "\n"), cadrePath(id), nil)
}

func (s *Server) cadreUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := cadreID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	before, err := s.store.Cadres.Get(r.Context(), id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	in, aliasText, v := decodeCadre(r)
	if before.InUse() {
		// Fixed once anyone has served in it. The form shows these read-only;
		// a value posted anyway is replaced, not refused, because the store
		// would carry the stored ones over regardless.
		in.CategoryID = before.CategoryID
		in.Code = before.Code
		in.PlacementLevel = before.PlacementLevel
		v = withoutFields(v, "category_id", "code", "placement_level")
	}
	s.checkCadre(r, id, in, v)
	if v.Any() {
		draft := draftCadre(id, in)
		draft.Deployments, draft.Serving = before.Deployments, before.Serving
		s.renderCadreForm(w, r, http.StatusUnprocessableEntity, draft, aliasText, cadrePath(id), v.Fields)
		return
	}

	after, err := s.store.Cadres.Update(r.Context(), sc, actor, id, in, s.clientIP(r))
	if errors.Is(err, domain.ErrConflict) {
		v.Add("code", "Another cadre already has that code.")
		draft := draftCadre(id, in)
		draft.Deployments, draft.Serving = before.Deployments, before.Serving
		s.renderCadreForm(w, r, http.StatusUnprocessableEntity, draft, aliasText, cadrePath(id), v.Fields)
		return
	}
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}

	msg := "Saved " + after.Label + "."
	if before.Active && !after.Active {
		msg = after.Label + " is retired: it is no longer offered on the form or accepted on import."
		if after.Serving > 0 {
			msg += " The " + strconv.FormatInt(after.Serving, 10) + " serving in it keep it until they are re-cadred."
		}
	}
	setFlash(w, s.secure(), "ok", msg)
	http.Redirect(w, r, "/cadres", http.StatusSeeOther)
}

func (s *Server) categoryCreate(w http.ResponseWriter, r *http.Request) {
	actor := auth.MustUser(r.Context())
	sc := auth.ScopeFrom(r.Context())

	v := domain.NewValidationError()
	in := store.CategoryInput{
		Code:  strings.ToLower(trimmed(r, "category_code")),
		Label: trimmed(r, "category_label"),
	}
	if !codePattern.MatchString(in.Code) {
		v.Add("category_code", codeRule)
	}
	if in.Label == "" {
		v.Add("category_label", "Name the category.")
	} else if len(in.Label) > 80 {
		v.Add("category_label", "Keep the name under 80 characters.")
	}
	in.SortOrder = sortOrder(r, "category_sort_order", v, "category_sort_order")

	if !v.Any() {
		created, err := s.store.Cadres.CreateCategory(r.Context(), sc, actor, in, s.clientIP(r))
		switch {
		case errors.Is(err, domain.ErrConflict):
			v.Add("category_code", "Another category already has that code.")
		case err != nil:
			s.notFoundOrFail(w, r, err)
			return
		default:
			setFlash(w, s.secure(), "ok", "Added "+created.Label+". Add its first cadre below.")
			http.Redirect(w, r, "/cadres", http.StatusSeeOther)
			return
		}
	}

	p, err := s.cadresPage(r, in, v.Fields)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusUnprocessableEntity, "cadres", p)
}

// codePattern is the code CHECK on cadres and cadre_categories. It is
// checked here so the admin gets a sentence rather than a constraint name.
var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

const codeRule = "2–32 characters: lower-case letters, digits and underscores, starting with a letter."

// decodeCadre reads the cadre form. The aliases are one per line (commas work
// too), because an alias may itself contain a space.
func decodeCadre(r *http.Request) (store.CadreInput, string, *domain.ValidationError) {
	v := domain.NewValidationError()
	in := store.CadreInput{
		Code:           strings.ToLower(trimmed(r, "code")),
		Label:          trimmed(r, "label"),
		PlacementLevel: domain.Level(trimmed(r, "placement_level")),
		Active:         r.PostForm.Get("active") == "1",
	}

	if id, err := strconv.ParseInt(trimmed(r, "category_id"), 10, 16); err == nil {
		in.CategoryID = int16(id)
	} else {
		v.Add("category_id", "Choose a category.")
	}
	if !codePattern.MatchString(in.Code) {
		v.Add("code", codeRule)
	}
	if in.Label == "" {
		v.Add("label", "Name the cadre.")
	} else if len(in.Label) > 80 {
		v.Add("label", "Keep the name under 80 characters.")
	}
	levelOK := false
	for _, l := range domain.PlacementLevels {
		levelOK = levelOK || l == in.PlacementLevel
	}
	if !levelOK {
		v.Add("placement_level", "Choose the level this cadre is placed at.")
	}
	in.SortOrder = sortOrder(r, "sort_order", v, "sort_order")

	aliasText := r.PostForm.Get("import_aliases")
	seen := map[string]bool{domain.FoldImport(in.Code): true}
	for _, a := range strings.FieldsFunc(aliasText, func(r rune) bool { return r == '\n' || r == ',' }) {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if len(a) > 60 {
			v.Add("import_aliases", "Keep each spelling under 60 characters.")
			continue
		}
		if seen[domain.FoldImport(a)] {
			continue // the code itself, or a repeat: nothing to store
		}
		seen[domain.FoldImport(a)] = true
		in.ImportAliases = append(in.ImportAliases, a)
	}
	return in, aliasText, v
}

// checkCadre is the validation that reads the register: the category must
// exist, and no spelling this cadre answers to on import may already belong to
// another. The importer takes the first cadre that matches, so an overlap would
// not fail loudly — it would file one cadre's workers under the other.
func (s *Server) checkCadre(r *http.Request, id int16, in store.CadreInput, v *domain.ValidationError) {
	categories, err := s.store.Cadres.Categories(r.Context())
	if err != nil {
		v.Add("category_id", "The categories could not be read. Try again.")
		return
	}
	if _, ok := v.Fields["category_id"]; !ok {
		found := false
		for _, c := range categories {
			found = found || c.ID == in.CategoryID
		}
		if !found {
			v.Add("category_id", "Choose a category from the list.")
		}
	}

	others, err := s.store.Cadres.All(r.Context())
	if err != nil {
		v.Add("import_aliases", "The other cadres could not be read. Try again.")
		return
	}
	mine := domain.Cadre{Code: in.Code, ImportAliases: in.ImportAliases}
	for _, other := range others {
		if other.ID == id {
			continue
		}
		for _, spelling := range mine.ImportSpellings() {
			if other.MatchesImport(spelling) {
				field := "import_aliases"
				if spelling == in.Code {
					field = "code"
				}
				v.Add(field, "“"+spelling+"” already names "+other.Label+" on import. Each spelling can name one cadre only.")
				return
			}
		}
	}
}

func (s *Server) renderCadreForm(w http.ResponseWriter, r *http.Request, status int, c store.CadreRow, aliases, action string, errs map[string]string) {
	categories, err := s.store.Cadres.Categories(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if errs == nil {
		errs = map[string]string{}
	}
	s.render(w, r, status, "cadre_form", cadreFormPage{
		Action:     action,
		Cadre:      c,
		Aliases:    aliases,
		Categories: categories,
		Levels:     domain.PlacementLevels,
		Errors:     errs,
	})
}

func draftCadre(id int16, in store.CadreInput) store.CadreRow {
	var c store.CadreRow
	c.ID = id
	c.CategoryID = in.CategoryID
	c.Code = in.Code
	c.Label = in.Label
	c.PlacementLevel = in.PlacementLevel
	c.ImportAliases = in.ImportAliases
	if in.SortOrder != nil {
		c.SortOrder = *in.SortOrder
	}
	c.Active = in.Active
	return c
}

// sortOrder reads an optional small integer. Blank is nil, which the store
// reads as "last" for a new row and "unchanged" for an existing one.
func sortOrder(r *http.Request, key string, v *domain.ValidationError, field string) *int16 {
	raw := trimmed(r, key)
	if raw == "" {
		return nil
	}
	n, err := strconv.ParseInt(raw, 10, 16)
	if err != nil || n < 0 || n > 999 {
		v.Add(field, "A whole number from 0 to 999, or blank.")
		return nil
	}
	order := int16(n)
	return &order
}

// withoutFields drops messages about fields the form does not let the user
// change, so a read-only value cannot block a save.
func withoutFields(v *domain.ValidationError, fields ...string) *domain.ValidationError {
	for _, f := range fields {
		delete(v.Fields, f)
	}
	return v
}

func cadreID(r *http.Request) (int16, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 16)
	if err != nil || id <= 0 {
		return 0, false
	}
	return int16(id), true
}

func cadrePath(id int16) string {
	return "/cadres/" + strconv.FormatInt(int64(id), 10)
}
