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

// A tool distribution is recorded as it happened: a hand-out in one district on
// one date, and which worker received which tool. The form lists the
// district's active workers — narrowed by the same cascade the register uses,
// since a district can hold thousands — with a box per tool their cadre
// carries; the schema refuses anything else.

type distributionsPage struct {
	Distributions []domain.ToolDistribution
	CanDistribute bool
}

type distributionFormPage struct {
	Districts  []districtOption
	DistrictID int64
	Prefill    map[string]int64
	Date       string
	Note       string
	Tools      []domain.Tool
	Rows       []distributionRow
	// Truncated says the selection was longer than the form lists; the
	// reader narrows it with the cascade.
	Truncated bool
	Error     string
}

// distributionRow is one worker with a box per tool, ticked or not, and
// disabled where the tool is not one their cadre carries.
type distributionRow struct {
	Worker domain.HealthWorker
	Boxes  []distributionBox
}

type distributionBox struct {
	Value   string // "<worker>:<tool>"
	Allowed bool
	Checked bool
}

const distributionRowLimit = 200

func (s *Server) distributionsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Activities.Distributions(r.Context(), auth.ScopeFrom(r.Context()), 100)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "distributions", distributionsPage{
		Distributions: list,
		CanDistribute: auth.Can(auth.MustUser(r.Context()).Role, auth.CapToolDistribute),
	})
}

func (s *Server) distributionShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	d, err := s.store.Activities.Distribution(r.Context(), auth.ScopeFrom(r.Context()), id)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "distribution_show", d)
}

// distributionNew draws the form. A district user's district is theirs; a
// national user picks one, and the workers appear once they have.
func (s *Server) distributionNew(w http.ResponseWriter, r *http.Request) {
	p, err := s.distributionForm(r, queryLocation(r), r.URL.Query().Get("reporting_date"), "", nil)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "distribution_form", p)
}

func (s *Server) distributionCreate(w http.ResponseWriter, r *http.Request) {
	sc := auth.ScopeFrom(r.Context())
	districtID, err := strconv.ParseInt(trimmed(r, "district"), 10, 64)
	if err != nil || !sc.Allows(districtID) {
		s.notFound(w, r)
		return
	}
	date := trimmed(r, "reporting_date")
	note := trimmed(r, "note")
	ticked := r.PostForm["item"]

	var items []store.DistributionItem
	for _, v := range ticked {
		worker, tool, ok := strings.Cut(v, ":")
		wid, werr := strconv.ParseInt(worker, 10, 64)
		tid, terr := strconv.ParseInt(tool, 10, 16)
		if !ok || werr != nil || terr != nil {
			s.notFound(w, r)
			return
		}
		items = append(items, store.DistributionItem{WorkerID: wid, ToolID: int16(tid), Quantity: 1})
	}

	on, ok := domain.ParseDate(date)
	problem := ""
	switch {
	case !ok || on.After(time.Now()):
		problem = "Give the date of the hand-out: today or earlier."
	case len(items) == 0:
		problem = "Tick at least one tool given to a worker."
	}
	if problem == "" {
		d, err := s.store.Activities.Distribute(r.Context(), sc, auth.MustUser(r.Context()),
			districtID, on, note, items, s.clientIP(r))
		switch {
		case err == nil:
			setFlash(w, s.secure(), "ok", fmt.Sprintf("Recorded %d tool(s) for %d worker(s).", d.Units, d.Recipients))
			http.Redirect(w, r, "/distributions/"+strconv.FormatInt(d.ID, 10), http.StatusSeeOther)
			return
		case errors.Is(err, domain.ErrRefused):
			problem = "That hand-out was refused: a worker ticked was not posted in this district on that date, " +
				"or was given a tool their cadre does not carry."
		default:
			s.notFoundOrFail(w, r, err)
			return
		}
	}

	location := deepestLocation(r)
	if location == 0 {
		location = districtID
	}
	p, err := s.distributionForm(r, location, date, note, ticked)
	if err != nil {
		s.notFoundOrFail(w, r, err)
		return
	}
	p.Error = problem
	s.render(w, r, http.StatusUnprocessableEntity, "distribution_form", p)
}

// distributionForm assembles the form for a location: the district it is in
// decides the hand-out's district, and the location narrows the workers.
func (s *Server) distributionForm(r *http.Request, location int64, date, note string, ticked []string) (distributionFormPage, error) {
	ctx := r.Context()
	sc := auth.ScopeFrom(ctx)
	if location == 0 {
		if id, pinned := sc.DistrictID(); pinned {
			location = id
		}
	}
	if date == "" {
		date = time.Now().Format(time.DateOnly)
	}
	p := distributionFormPage{Date: date, Note: note, Prefill: map[string]int64{}}

	if location != 0 {
		places, err := s.store.Locations.Ancestors(ctx, sc, location)
		if err != nil {
			return p, err
		}
		for _, place := range places {
			p.Prefill[string(place.Level)] = place.ID
		}
		p.DistrictID = p.Prefill[string(domain.LevelDistrict)]
	}

	districts, err := s.store.Locations.Districts(ctx, sc)
	if err != nil {
		return p, err
	}
	for _, d := range districts {
		p.Districts = append(p.Districts, districtOption{ID: d.ID, Name: d.Name, Selected: d.ID == p.DistrictID})
	}
	if p.DistrictID == 0 {
		return p, nil
	}

	if p.Tools, err = s.store.Activities.Tools(ctx); err != nil {
		return p, err
	}
	page, err := s.store.Workers.List(ctx, sc, store.Filter{
		LocationID: location, Status: domain.WorkerActive, Limit: distributionRowLimit,
	})
	if err != nil {
		return p, err
	}
	p.Truncated = page.HasNext

	checked := map[string]bool{}
	for _, v := range ticked {
		checked[v] = true
	}
	carries := map[int16]map[int16]bool{} // cadre -> tools it carries
	for _, wk := range page.Workers {
		if wk.Deployment == nil || !wk.Deployment.Active() {
			continue
		}
		cadre := wk.Deployment.CadreID
		if carries[cadre] == nil {
			tools, err := s.store.Activities.ToolsFor(ctx, cadre)
			if err != nil {
				return p, err
			}
			carries[cadre] = map[int16]bool{}
			for _, t := range tools {
				carries[cadre][t.ID] = true
			}
		}
		row := distributionRow{Worker: wk}
		for _, t := range p.Tools {
			value := fmt.Sprintf("%d:%d", wk.ID, t.ID)
			row.Boxes = append(row.Boxes, distributionBox{Value: value, Allowed: carries[cadre][t.ID], Checked: checked[value]})
		}
		p.Rows = append(p.Rows, row)
	}
	return p, nil
}

// queryLocation is deepestLocation for a GET form: the deepest level of the
// cascade the query string fills in.
func queryLocation(r *http.Request) int64 {
	q := r.URL.Query()
	for _, field := range []string{"village_id", "parish_id", "subcounty_id", "district_id"} {
		if id, err := strconv.ParseInt(strings.TrimSpace(q.Get(field)), 10, 64); err == nil && id > 0 {
			return id
		}
	}
	return 0
}
