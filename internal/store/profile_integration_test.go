package store

// The questionnaire, the person's details and the dated events, through the
// store: what is saved reads back, "no" and "not asked" stay different
// answers, saving writes history rather than overwriting it, every write is
// audited and scoped, and the dashboard counts what was saved.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"hwr/internal/auth"
	"hwr/internal/domain"
)

func (w *world) baseline(t *testing.T) domain.Profile {
	t.Helper()
	p, err := w.store.Profiles.ByCode(context.Background(), CHWBaseline)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestASubmissionReadsBackAsSaved(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	p := w.baseline(t)
	hw := w.vhtIn(t, w.a, "Profiled", randomTag())

	empty, err := w.store.Profiles.Latest(ctx, w.a.scope(), hw.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Exists() || empty.Answers.Answered() {
		t.Fatal("a new worker has answers before any were saved")
	}

	first := domain.Answers{
		"owns_phone": {"yes"}, "phone_for_reporting": {"no"},
		"households_served": {"120"}, "receives_incentive": {"yes"},
		"incentive_frequency": {"monthly"}, "incentive_amount_ugx": {"20000"},
		"received_supervision": {"yes"}, "last_supervised_on": {"2026-03-01"},
		"tools_held": {"bicycle", "torch"}, "tools_functional": {"torch"},
		"services_provided": {"iccm", "hiv"}, "services_trained": {"iccm"},
	}
	if _, err := w.store.Profiles.Submit(ctx, w.a.scope(), w.manager, hw.ID, p, first, "form", ip); err != nil {
		t.Fatal(err)
	}
	got, err := w.store.Profiles.Latest(ctx, w.a.scope(), hw.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	if !sameAnswers(got.Answers, first) {
		t.Errorf("read back %v, want %v", got.Answers, first)
	}
	if got.Answers.One("phone_for_reporting") != "no" {
		t.Error("an explicit no did not come back as no")
	}
	if _, asked := got.Answers["service_start_year"]; asked {
		t.Error("an unasked question came back answered")
	}
	if got.CreatedBy == nil || *got.CreatedBy != w.manager.ID {
		t.Errorf("submission created_by = %v, want the acting manager", got.CreatedBy)
	}

	// Saving the same answers writes nothing; changing them writes history.
	if _, err := w.store.Profiles.Submit(ctx, w.a.scope(), w.manager, hw.ID, p, first, "form", ip); err != nil {
		t.Fatal(err)
	}
	second := domain.Answers{"owns_phone": {"no"}, "tools_held": {"none"}}
	if _, err := w.store.Profiles.Submit(ctx, w.a.scope(), w.manager, hw.ID, p, second, "form", ip); err != nil {
		t.Fatal(err)
	}
	history, err := w.store.Profiles.History(ctx, w.a.scope(), hw.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || !sameAnswers(history[0].Answers, second) || !sameAnswers(history[1].Answers, first) {
		t.Fatalf("history = %+v, want the second submission over the first", history)
	}

	// A refused answer comes back keyed by question, and writes nothing.
	_, err = w.store.Profiles.Submit(ctx, w.a.scope(), w.manager, hw.ID, p,
		domain.Answers{"owns_phone": {"no"}, "phone_for_reporting": {"yes"}}, "form", ip)
	var v *domain.ValidationError
	if !errors.As(err, &v) || v.Fields["phone_for_reporting"] == "" {
		t.Errorf("a closed branch answered: err = %v", err)
	}

	var submits []auditRow
	for _, r := range w.auditFor(t, hw.ID) {
		if r.action == ActionProfileSubmit {
			submits = append(submits, r)
		}
	}
	if len(submits) != 2 {
		t.Fatalf("%d submission audit rows, want 2", len(submits))
	}
	if submits[0].before != nil || submits[1].before == nil || submits[1].after["profile"] != CHWBaseline {
		t.Errorf("submission audit rows = %+v", submits)
	}
	if submits[0].districtID == nil || *submits[0].districtID != w.a.id {
		t.Errorf("submission audited against %v, want %d", submits[0].districtID, w.a.id)
	}
}

func TestSubmissionsOutsideTheScopeAreRefused(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	p := w.baseline(t)
	hw := w.vhtIn(t, w.b, "Elsewhere", randomTag())

	if _, err := w.store.Profiles.Latest(ctx, w.a.scope(), hw.ID, p); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a district read another district's survey: %v", err)
	}
	if _, err := w.store.Profiles.Submit(ctx, w.a.scope(), w.manager, hw.ID, p,
		domain.Answers{"owns_phone": {"no"}}, "form", ip); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a district wrote another district's survey: %v", err)
	}
	if _, err := w.store.Persons.Details(ctx, w.a.scope(), hw.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a district read another district's person details: %v", err)
	}
	if err := w.store.Persons.AddContact(ctx, w.a.scope(), w.manager, hw.ID,
		domain.Contact{Kind: domain.ContactPhone, Value: "700000001"}, ip); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a district wrote another district's contact: %v", err)
	}
}

// The completeness and services charts count exactly what was saved. The
// dashboard drops the search box, so the test cannot narrow to its own rows:
// it measures one village before and after, and asserts the difference.
func TestTheSurveyChartsCountWhatWasSaved(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	p := w.baseline(t)
	tag := randomTag()
	f := Filter{LocationID: w.a.villages[0]}

	c0, err := w.store.Stats.Completeness(ctx, w.a.scope(), f)
	if err != nil {
		t.Fatal(err)
	}
	svc0, resp0, err := w.store.Stats.Services(ctx, w.a.scope(), f)
	if err != nil {
		t.Fatal(err)
	}

	full := w.vhtIn(t, w.a, "Full", tag)
	half := w.vhtIn(t, w.a, "Half", tag)
	w.vhtIn(t, w.a, "Blank", tag)

	if _, err := w.store.Profiles.Submit(ctx, auth.National(), w.admin, full.ID, p, domain.Answers{
		"owns_phone": {"yes"}, "service_start_year": {"2010"}, "households_served": {"50"},
		"receives_incentive": {"no"}, "received_supervision": {"no"},
		"tools_held": {"bicycle"}, "services_provided": {"iccm"}, "services_trained": {"iccm"},
	}, "form", ip); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.Profiles.Submit(ctx, auth.National(), w.admin, half.ID, p, domain.Answers{
		"owns_phone": {"no"}, "services_provided": {"iccm", "maternal_newborn"},
	}, "form", ip); err != nil {
		t.Fatal(err)
	}

	c, err := w.store.Stats.Completeness(ctx, w.a.scope(), f)
	if err != nil {
		t.Fatal(err)
	}
	if c.CHWs-c0.CHWs != 3 {
		t.Fatalf("CHWs grew by %d, want 3", c.CHWs-c0.CHWs)
	}
	want := map[string]int64{
		"Survey started": 2, "Phone ownership": 2, "Year started service": 1,
		"Households served": 1, "Incentive": 1, "Supervision": 1, "Services offered": 2, "Tools held": 1,
	}
	for i, m := range c.Profile {
		if n, ok := want[m.Label]; !ok {
			t.Errorf("unexpected measure %q", m.Label)
		} else if d := m.Have - c0.Profile[i].Have; d != n {
			t.Errorf("%s grew by %d, want %d", m.Label, d, n)
		}
	}

	svc, respondents, err := w.store.Stats.Services(ctx, w.a.scope(), f)
	if err != nil {
		t.Fatal(err)
	}
	if d := respondents - resp0; d != 2 {
		t.Errorf("respondents grew by %d, want 2", d)
	}
	if len(svc) < 2 || len(svc) != len(svc0) {
		t.Fatalf("%d service rows, %d before", len(svc), len(svc0))
	}
	grew := func(i int) (int64, int64) {
		return svc[i].Provides - svc0[i].Provides, svc[i].Trained - svc0[i].Trained
	}
	if pr, tr := grew(0); pr != 2 || tr != 1 {
		t.Errorf("%s grew by %d provides, %d trained; want 2, 1", svc[0].Label, pr, tr)
	}
	if pr, tr := grew(1); pr != 1 || tr != 0 {
		t.Errorf("%s grew by %d provides, %d trained; want 1, 0", svc[1].Label, pr, tr)
	}
}

func TestPersonDetailsAreRecordedAndRemoved(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	hw := w.vhtIn(t, w.a, "Detailed", randomTag())
	yes := true

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.store.Persons.WriteSurveyDetailsTx(ctx, tx, w.a.scope(), w.manager, hw.ID, SurveyDetails{
		Phones:    []domain.Contact{{Kind: domain.ContactPhone, Value: "772123456", Owned: &yes, IsPrimary: true}},
		Education: domain.EducationUCE,
		English:   &domain.LanguageSkill{Understanding: domain.ProficiencyBasic, Reading: domain.ProficiencyNone},
	}, ip); err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	d, err := w.store.Persons.Details(ctx, w.a.scope(), hw.ID)
	if err != nil {
		t.Fatal(err)
	}
	en, ok := d.Language(domain.LanguageEnglish)
	switch {
	case d.Phone() != "772123456":
		t.Errorf("phone = %q", d.Phone())
	case d.HighestEducation() != domain.EducationUCE:
		t.Errorf("education = %q", d.HighestEducation())
	case !ok || en.Understanding != domain.ProficiencyBasic || en.Reading != domain.ProficiencyNone || en.Writing != "":
		t.Errorf("english = %+v; writing should be unasked, reading an explicit none", en)
	}

	// A second primary phone demotes the first rather than colliding with it.
	if err := w.store.Persons.AddContact(ctx, w.a.scope(), w.manager, hw.ID,
		domain.Contact{Kind: domain.ContactPhone, Value: "700111222", IsPrimary: true}, ip); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Persons.AddKin(ctx, w.a.scope(), w.manager, hw.ID,
		domain.Kin{Name: "Akello Mary", Relationship: "sister", IsEmergency: true}, ip); err != nil {
		t.Fatal(err)
	}
	d, _ = w.store.Persons.Details(ctx, w.a.scope(), hw.ID)
	if d.Phone() != "700111222" || len(d.Kin) != 1 {
		t.Fatalf("details after adds = %+v", d)
	}
	if err := w.store.Persons.Remove(ctx, w.a.scope(), w.manager, hw.ID, DetailKin, d.Kin[0].ID, ip); err != nil {
		t.Fatal(err)
	}
	// Another worker's row, reached through this worker's id, is not there.
	other := w.vhtIn(t, w.a, "Other", randomTag())
	if err := w.store.Persons.Remove(ctx, w.a.scope(), w.manager, other.ID, DetailContact, d.Contacts[0].ID, ip); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("removed a contact through another worker: %v", err)
	}

	var adds, removes int
	for _, r := range w.auditFor(t, hw.ID) {
		switch r.action {
		case ActionPersonDetailAdd:
			adds++
		case ActionPersonDetailRemove:
			removes++
		}
	}
	if adds != 3 || removes != 1 {
		t.Errorf("detail audit rows: %d adds, %d removes; want 3, 1", adds, removes)
	}
}

func TestServiceUpdatesAndDistributions(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	hw := w.vhtIn(t, w.a, "Active", randomTag())
	there := w.vhtIn(t, w.b, "Away", randomTag())

	services, err := w.store.Activities.ServicesFor(ctx, w.vht)
	if err != nil || len(services) < 2 {
		t.Fatalf("services for VHT: %v %v", services, err)
	}
	if _, err := w.store.Activities.ReportServices(ctx, w.a.scope(), w.manager, hw.ID, today,
		[]int16{services[0].ID, services[1].ID}, ip); err != nil {
		t.Fatal(err)
	}
	// A second report for the date replaces the first.
	if _, err := w.store.Activities.ReportServices(ctx, w.a.scope(), w.manager, hw.ID, today,
		[]int16{services[1].ID}, ip); err != nil {
		t.Fatal(err)
	}
	updates, err := w.store.Activities.ServiceUpdates(ctx, w.a.scope(), hw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || len(updates[0].Services) != 1 || updates[0].Services[0].ID != services[1].ID {
		t.Errorf("service updates = %+v", updates)
	}
	if _, err := w.store.Activities.ReportServices(ctx, w.a.scope(), w.manager, hw.ID, today.AddDate(0, 0, -400),
		[]int16{services[0].ID}, ip); err == nil {
		t.Error("a report dated before the worker's posting was accepted")
	}

	tools, err := w.store.Activities.ToolsFor(ctx, w.vht)
	if err != nil || len(tools) == 0 {
		t.Fatalf("tools for VHT: %v %v", tools, err)
	}
	if _, err := w.store.Activities.Distribute(ctx, w.a.scope(), w.manager, w.b.id, today, "", nil, ip); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("a district distributed in another: %v", err)
	}
	if _, err := w.store.Activities.Distribute(ctx, auth.National(), w.admin, w.a.id, today, "",
		[]DistributionItem{{WorkerID: hw.ID, ToolID: tools[0].ID}, {WorkerID: there.ID, ToolID: tools[0].ID}}, ip); err == nil {
		t.Error("a hand-out in one district reached a worker posted in another")
	}
	d, err := w.store.Activities.Distribute(ctx, w.a.scope(), w.manager, w.a.id, today, "quarterly kit",
		[]DistributionItem{{WorkerID: hw.ID, ToolID: tools[0].ID, Quantity: 2}}, ip)
	if err != nil {
		t.Fatal(err)
	}
	if d.Recipients != 1 || d.Units != 2 {
		t.Errorf("distribution = %+v", d)
	}
	// The dashboard counts the hand-out against the selection the worker is in.
	given, to, err := w.store.Stats.ToolsHandedOut(ctx, w.a.scope(), Filter{Query: "", LocationID: w.a.villages[0]})
	if err != nil {
		t.Fatal(err)
	}
	var units int64
	for _, g := range given {
		units += g.Count
	}
	if units < 2 || to < 1 {
		t.Errorf("dashboard counts %d tools to %d workers after a hand-out of 2", units, to)
	}
	reported, reporters, err := w.store.Stats.ServicesReported(ctx, w.a.scope(), Filter{LocationID: w.a.villages[0]})
	if err != nil || reporters < 1 || len(reported) == 0 {
		t.Errorf("dashboard services reported = %v from %d workers (%v)", reported, reporters, err)
	}

	got, err := w.store.Activities.ToolsReceived(ctx, w.a.scope(), hw.ID)
	if err != nil || len(got) != 1 || got[0].Quantity != 2 {
		t.Errorf("tools received = %+v %v", got, err)
	}
	if _, err := w.store.Activities.Distribution(ctx, w.b.scope(), d.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("another district read the hand-out: %v", err)
	}
	listed, err := w.store.Activities.Distributions(ctx, w.b.scope(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(listed, func(x domain.ToolDistribution) bool { return x.ID == d.ID }) {
		t.Error("another district lists the hand-out")
	}
}

// Every posting carries its worker's code and its ordinal, and every row a
// mutation writes names who wrote it.
func TestCodesAndRecordColumns(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	hw := w.vhtIn(t, w.a, "Coded", randomTag())

	if !strings.HasPrefix(hw.Deployment.Code, hw.Code+"-") || !strings.HasSuffix(hw.Deployment.Code, "-01") {
		t.Errorf("first posting code = %q for worker %q", hw.Deployment.Code, hw.Code)
	}
	moved, err := w.store.Workers.Update(ctx, auth.National(), w.admin, hw.ID, WorkerInput{
		FirstName: hw.FirstName, LastName: hw.LastName, Sex: hw.Sex, DOB: hw.DOB, DOBEstimated: hw.DOBEstimated,
		CadreID: w.vht, LocationID: w.a.villages[1],
	}, ip)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Deployment.Code != hw.Code+"-02" {
		t.Errorf("second posting code = %q, want %s-02", moved.Deployment.Code, hw.Code)
	}

	var personBy, workerBy, depBy *int64
	if err := w.pool.QueryRow(ctx, `
	    SELECT p.created_by, w.created_by, d.last_updated_by
	      FROM health_workers w JOIN persons p ON p.id = w.person_id
	      JOIN deployments d ON d.health_worker_id = w.id AND d.ended_on IS NOT NULL
	     WHERE w.id = $1`, hw.ID).Scan(&personBy, &workerBy, &depBy); err != nil {
		t.Fatal(err)
	}
	for name, by := range map[string]*int64{"person": personBy, "worker": workerBy, "ended posting": depBy} {
		if by == nil || *by != w.admin.ID {
			t.Errorf("%s written by %v, want the admin %d", name, by, w.admin.ID)
		}
	}
}
