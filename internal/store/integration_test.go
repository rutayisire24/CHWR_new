package store

// Integration tests: these run the store's SQL against a real, migrated and
// seeded database. They need a disposable one, because workers are never
// deleted and so nothing a test writes can be cleaned up afterwards:
//
//	make test-db             # clone the seeded database into hwr_test
//	make test-integration    # HWR_TEST_DATABASE_URL=postgres:///hwr_test go test ./internal/store/...
//
// Without HWR_TEST_DATABASE_URL they skip, so `go test ./...` stays hermetic.
//
// Each test tags its workers with a fresh surname, and asserts only about the
// rows it made — the database may hold anything else.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"hwr/internal/auth"
	"hwr/internal/db"
	"hwr/internal/domain"
)

var (
	fixtureOnce sync.Once
	fixture     *world
	fixtureErr  error
)

// world is what every integration test shares: the stores, and two districts
// with enough of a hierarchy beneath each to place, transfer and attach.
type world struct {
	pool  *pgxpool.Pool
	store *Store

	vht, chew int16

	a, b district

	admin   domain.User // national_admin
	manager domain.User // district_manager for a
}

type district struct {
	id       int64
	code     string // the three letters worker codes begin with
	villages []int64
	parish   int64
	facility int64
}

func (d district) scope() auth.Scope { return auth.District(d.id) }

var ip = netip.MustParseAddr("192.0.2.1")

func integration(t *testing.T) *world {
	t.Helper()
	url := os.Getenv("HWR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("HWR_TEST_DATABASE_URL not set; run `make test-db test-integration`")
	}
	fixtureOnce.Do(func() { fixture, fixtureErr = build(url) })
	if fixtureErr != nil {
		t.Fatalf("integration fixture: %v", fixtureErr)
	}
	return fixture
}

func build(url string) (*world, error) {
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		return nil, err
	}
	w := &world{pool: pool, store: New(pool)}

	if err := pool.QueryRow(ctx,
		`SELECT (SELECT id FROM cadres WHERE slug='vht'), (SELECT id FROM cadres WHERE slug='chew')`,
	).Scan(&w.vht, &w.chew); err != nil {
		return nil, err
	}

	// Two districts that each hold a facility and at least two villages, so a
	// test can transfer within a district as well as across one.
	rows, err := pool.Query(ctx, `
	    SELECT d.id, c.abbr FROM locations d
	      JOIN district_codes c ON c.district_code = d.code
	     WHERE d.level = 'district'
	       AND EXISTS (SELECT 1 FROM facilities f WHERE f.district_id = d.id)
	     ORDER BY d.id LIMIT 2`)
	if err != nil {
		return nil, err
	}
	var ds []district
	for rows.Next() {
		var d district
		if err := rows.Scan(&d.id, &d.code); err != nil {
			return nil, err
		}
		ds = append(ds, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ds) != 2 {
		return nil, errors.New("the database needs two seeded districts with facilities")
	}
	for i := range ds {
		d := &ds[i]
		under := `path LIKE (SELECT path FROM locations WHERE id = $1) || '%' AND active`
		vs, err := pool.Query(ctx, `SELECT id FROM locations WHERE level='village' AND `+under+` ORDER BY id LIMIT 2`, d.id)
		if err != nil {
			return nil, err
		}
		for vs.Next() {
			var v int64
			if err := vs.Scan(&v); err != nil {
				return nil, err
			}
			d.villages = append(d.villages, v)
		}
		if err := vs.Err(); err != nil {
			return nil, err
		}
		if err := pool.QueryRow(ctx, `SELECT id FROM locations WHERE level='parish' AND `+under+` ORDER BY id LIMIT 1`, d.id).Scan(&d.parish); err != nil {
			return nil, err
		}
		if err := pool.QueryRow(ctx, `SELECT id FROM facilities WHERE district_id = $1 ORDER BY id LIMIT 1`, d.id).Scan(&d.facility); err != nil {
			return nil, err
		}
	}
	w.a, w.b = ds[0], ds[1]

	tag := randomTag()
	w.admin, err = w.store.Users.Create(ctx, auth.National(), NewUser{
		Email: "admin-" + tag + "@test.hwr", FullName: "Test Admin",
		Role: domain.RoleNationalAdmin, Password: "integration-pass-1",
	})
	if err != nil {
		return nil, err
	}
	w.manager, err = w.store.Users.Create(ctx, auth.National(), NewUser{
		Email: "manager-" + tag + "@test.hwr", FullName: "Test Manager",
		Role: domain.RoleDistrictManager, DistrictID: &w.a.id, Password: "integration-pass-1",
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

// randomTag is a surname no seeded record carries, so a search for it returns
// exactly the rows this test made. Letters only: it is a name.
func randomTag() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	s := hex.EncodeToString(b)
	return "Zq" + strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return 'a' + (r - '0') // hex digits become g..p-ish letters
		}
		return r
	}, s)
}

// randomNIN fits health_workers.nin: two letters, eleven letters or digits,
// one letter.
func randomNIN() string {
	b := make([]byte, 11)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	mid := make([]byte, 11)
	for i, x := range b {
		mid[i] = alnum[int(x)%len(alnum)]
	}
	return "CM" + string(mid) + "Q"
}

func age(n int16) *int16 { return &n }

// vhtIn creates an active VHT in a village of d, as the national admin.
func (w *world) vhtIn(t *testing.T, d district, first, last string) domain.HealthWorker {
	t.Helper()
	hw, err := w.store.Workers.Create(context.Background(), auth.National(), w.admin, WorkerInput{
		FirstName: first, LastName: last, Sex: domain.SexFemale, AgeYears: age(30),
		CadreID: w.vht, LocationID: d.villages[0],
	}, ip)
	if err != nil {
		t.Fatalf("create VHT %s %s: %v", first, last, err)
	}
	return hw
}

// auditRow is one audit_log row as a test reads it back.
type auditRow struct {
	action, entity string
	districtID     *int64
	before, after  map[string]any
}

// auditFor returns the audit rows about a worker — the worker's own and their
// deployments' — oldest first.
func (w *world) auditFor(t *testing.T, workerID int64) []auditRow {
	t.Helper()
	rows, err := w.pool.Query(context.Background(), `
	    SELECT a.action, a.entity, a.district_id, a.before, a.after
	      FROM audit_log a
	      LEFT JOIN deployments d ON a.entity = 'deployment' AND d.id = a.entity_id
	     WHERE (a.entity = 'health_worker' AND a.entity_id = $1)
	        OR d.health_worker_id = $1
	     ORDER BY a.id`, workerID)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var before, after []byte
		if err := rows.Scan(&r.action, &r.entity, &r.districtID, &before, &after); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		if before != nil {
			if err := json.Unmarshal(before, &r.before); err != nil {
				t.Fatal(err)
			}
		}
		if after != nil {
			if err := json.Unmarshal(after, &r.after); err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func actions(rows []auditRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.action
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ids(ws []domain.HealthWorker) []int64 {
	out := make([]int64, len(ws))
	for i, w := range ws {
		out[i] = w.ID
	}
	return out
}

func contains(xs []int64, x int64) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ scope

// A district user reads their own district and nothing else, by every road
// into the register: a direct fetch, the listing, a search by worker code, the
// count, the export, the posting history and the audit trail. The Scope is a
// required argument, but only the SQL can prove it was actually applied.
func TestADistrictScopeSeesOnlyItsOwnDistrict(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	tag := randomTag()
	mine := w.vhtIn(t, w.a, "Mine", tag)
	theirs := w.vhtIn(t, w.b, "Theirs", tag)
	sc := w.a.scope()

	if _, err := w.store.Workers.Get(ctx, sc, mine.ID); err != nil {
		t.Errorf("Get own worker: %v", err)
	}
	if _, err := w.store.Workers.Get(ctx, sc, theirs.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get another district's worker = %v, want ErrNotFound", err)
	}

	page, err := w.store.Workers.List(ctx, sc, Filter{Query: tag})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page.Workers); len(got) != 1 || got[0] != mine.ID {
		t.Errorf("district listing = %v, want only %d", got, mine.ID)
	}
	page, err = w.store.Workers.List(ctx, auth.National(), Filter{Query: tag})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page.Workers); len(got) != 2 {
		t.Errorf("national listing = %v, want both", got)
	}

	// A code is looked up exactly, and the scope still decides.
	page, err = w.store.Workers.List(ctx, sc, Filter{Query: theirs.Code})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Workers) != 0 {
		t.Errorf("searching another district's code %s found %v", theirs.Code, ids(page.Workers))
	}

	if n, err := w.store.Workers.Matching(ctx, sc, Filter{Query: tag}); err != nil || n != 1 {
		t.Errorf("Matching = %d, %v; want 1", n, err)
	}

	var exported []int64
	if err := w.store.Export.Rows(ctx, sc, Filter{Query: tag}, func(r ExportRow) error {
		exported = append(exported, r.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(exported) != 1 || exported[0] != mine.ID {
		t.Errorf("district export = %v, want only %d", exported, mine.ID)
	}

	if deps, err := w.store.Deployments.ForWorker(ctx, sc, theirs.ID); err != nil || len(deps) != 0 {
		t.Errorf("another district's postings = %d rows, %v; want none", len(deps), err)
	}

	log, err := w.store.Audit.List(ctx, sc, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range log {
		if e.DistrictID == nil || *e.DistrictID != w.a.id {
			t.Errorf("district audit shows %s from district %v", e.Action, e.DistrictID)
		}
	}
}

// A cursor is a position, not a permission. One that names a row in another
// district, or sits just before one, still pages only the caller's district.
func TestACursorCannotReachPastTheScope(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	tag := randomTag()
	mine := w.vhtIn(t, w.a, "Bmine", tag)
	theirs := w.vhtIn(t, w.b, "Atheirs", tag) // sorts first

	before := Cursor{LastName: tag, FirstName: "A", ID: 0}
	page, err := w.store.Workers.List(ctx, w.a.scope(), Filter{Query: tag, After: &before})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page.Workers); len(got) != 1 || got[0] != mine.ID {
		t.Errorf("page after a cursor = %v, want only %d", got, mine.ID)
	}

	onTheirs, onMine := cursorFor(theirs), cursorFor(mine)
	page, err = w.store.Workers.List(ctx, w.a.scope(), Filter{Query: tag, Before: &onMine})
	if err != nil {
		t.Fatal(err)
	}
	if contains(ids(page.Workers), theirs.ID) {
		t.Errorf("paging backwards reached another district's worker %d", theirs.ID)
	}
	page, err = w.store.Workers.List(ctx, w.a.scope(), Filter{Query: tag, After: &onTheirs})
	if err != nil {
		t.Fatal(err)
	}
	if contains(ids(page.Workers), theirs.ID) {
		t.Errorf("a cursor on another district's worker returned them")
	}
}

// A district user cannot write outside their district either: not placing a
// new worker there, not transferring one of theirs away, and not touching one
// that is not theirs. Each refusal must leave no trace.
func TestADistrictScopeCannotWriteOutsideItsDistrict(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	sc := w.a.scope()
	tag := randomTag()

	_, err := w.store.Workers.Create(ctx, sc, w.manager, WorkerInput{
		FirstName: "Stray", LastName: tag, Sex: domain.SexMale,
		CadreID: w.vht, LocationID: w.b.villages[0],
	}, ip)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("create in another district = %v, want ErrForbidden", err)
	}
	if n, _ := w.store.Workers.Matching(ctx, auth.National(), Filter{Query: tag}); n != 0 {
		t.Errorf("a refused create left %d worker(s) behind", n)
	}

	mine := w.vhtIn(t, w.a, "Mine", tag)
	_, err = w.store.Workers.Update(ctx, sc, w.manager, mine.ID, WorkerInput{
		FirstName: mine.FirstName, LastName: mine.LastName, Sex: mine.Sex, AgeYears: mine.AgeYears,
		CadreID: w.vht, LocationID: w.b.villages[0],
	}, ip)
	if err == nil {
		t.Error("a district user transferred a worker out of their district")
	}
	after, err := w.store.Workers.Get(ctx, auth.National(), mine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Deployment.LocationID != w.a.villages[0] || !after.Deployment.Active() {
		t.Errorf("a refused transfer moved the worker to %d", after.Deployment.LocationID)
	}

	theirs := w.vhtIn(t, w.b, "Theirs", tag)
	if _, err := w.store.Workers.Update(ctx, sc, w.manager, theirs.ID, WorkerInput{
		FirstName: "Renamed", LastName: tag, Sex: theirs.Sex, CadreID: w.vht, LocationID: w.b.villages[0],
	}, ip); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("update another district's worker = %v, want ErrNotFound", err)
	}
	if _, err := w.store.Workers.Deactivate(ctx, sc, w.manager, theirs.ID, "left", ip); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deactivate another district's worker = %v, want ErrNotFound", err)
	}
	got, _ := w.store.Workers.Get(ctx, auth.National(), theirs.ID)
	if got.FirstName != "Theirs" || !got.Active() {
		t.Errorf("a refused write changed another district's worker: %+v", got)
	}
	if err := w.store.Deployments.SetFacility(ctx, sc, w.manager, theirs.ID, &w.b.facility, ip); err == nil {
		t.Error("a district user attached a facility to another district's worker")
	}
}

// The location cascade is scoped on the ancestor: a district user may walk
// anything inside their district and nothing outside it.
func TestTheCascadeStopsAtTheDistrictBoundary(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	sc := w.a.scope()

	ds, err := w.store.Locations.Districts(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].ID != w.a.id {
		t.Errorf("districts for a district user = %v, want only %d", ds, w.a.id)
	}
	if subs, err := w.store.Locations.Descendants(ctx, sc, w.a.id, domain.LevelSubcounty); err != nil || len(subs) == 0 {
		t.Errorf("own subcounties = %d, %v", len(subs), err)
	}
	if _, err := w.store.Locations.Descendants(ctx, sc, w.b.id, domain.LevelSubcounty); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("another district's subcounties = %v, want ErrNotFound", err)
	}
	if _, err := w.store.Locations.Descendants(ctx, sc, w.b.parish, domain.LevelVillage); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("villages of another district's parish = %v, want ErrNotFound", err)
	}
	if _, err := w.store.Deployments.FacilitiesIn(ctx, sc, w.b.id); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("another district's facilities = %v, want ErrNotFound", err)
	}
}

// ------------------------------------------------------------------ the lifecycle, and its audit trail

// Every mutation writes to audit_log in its own transaction (invariant 6).
// Walked end to end: create, correct a name, transfer, attach a facility,
// promote, deactivate, reactivate, redeploy — and at every step the audit
// trail says exactly what happened, with before and after.
func TestEveryStepOfALifeIsAudited(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	nat := auth.National()
	tag := randomTag()

	hw := w.vhtIn(t, w.a, "Grace", tag)
	expect := []string{ActionWorkerCreate, ActionDeploymentStart}
	check := func(step string) []auditRow {
		t.Helper()
		got := w.auditFor(t, hw.ID)
		if !sameStrings(actions(got), expect) {
			t.Fatalf("after %s audit = %v, want %v", step, actions(got), expect)
		}
		return got
	}
	check("create")
	if hw.DistrictID == nil || *hw.DistrictID != w.a.id {
		t.Errorf("district_id = %v, want the derived %d", hw.DistrictID, w.a.id)
	}

	in := WorkerInput{FirstName: "Gracious", LastName: tag, Sex: domain.SexFemale, AgeYears: age(30),
		CadreID: w.vht, LocationID: w.a.villages[0]}
	if _, err := w.store.Workers.Update(ctx, nat, w.admin, hw.ID, in, ip); err != nil {
		t.Fatal(err)
	}
	expect = append(expect, ActionWorkerUpdate)
	rows := check("rename")
	last := rows[len(rows)-1]
	if last.before["first_name"] != "Grace" || last.after["first_name"] != "Gracious" {
		t.Errorf("rename audit before/after = %v / %v", last.before["first_name"], last.after["first_name"])
	}

	// Saving the form unchanged is not a mutation and writes nothing.
	if _, err := w.store.Workers.Update(ctx, nat, w.admin, hw.ID, in, ip); err != nil {
		t.Fatal(err)
	}
	check("an unchanged save")

	in.LocationID = w.a.villages[1]
	if _, err := w.store.Workers.Update(ctx, nat, w.admin, hw.ID, in, ip); err != nil {
		t.Fatal(err)
	}
	expect = append(expect, ActionDeploymentEnd, ActionDeploymentStart)
	check("transfer within the district")

	if err := w.store.Deployments.SetFacility(ctx, nat, w.admin, hw.ID, &w.a.facility, ip); err != nil {
		t.Fatal(err)
	}
	expect = append(expect, ActionDeploymentUpdate)
	check("attach a facility")

	in.CadreID, in.LocationID = w.chew, w.a.parish
	promoted, err := w.store.Workers.Update(ctx, nat, w.admin, hw.ID, in, ip)
	if err != nil {
		t.Fatal(err)
	}
	// A promotion inside the district carries the facility onto the new posting.
	expect = append(expect, ActionDeploymentEnd, ActionDeploymentStart, ActionDeploymentUpdate)
	check("promote to CHEW")
	if promoted.Deployment.FacilityID == nil || *promoted.Deployment.FacilityID != w.a.facility {
		t.Errorf("promotion in-district dropped the facility: %v", promoted.Deployment.FacilityID)
	}

	deps, err := w.store.Deployments.ForWorker(ctx, nat, hw.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 3 {
		t.Fatalf("postings = %d, want 3", len(deps))
	}
	open := 0
	for _, d := range deps {
		if d.Active() {
			open++
		} else if d.EndReason == "" {
			t.Errorf("posting %d ended without a reason", d.ID)
		}
	}
	if open != 1 {
		t.Errorf("open postings = %d, want 1", open)
	}
	if deps[1].EndReason != "recadre" || deps[2].EndReason != "transfer" {
		t.Errorf("end reasons = %q, %q; want recadre, transfer", deps[1].EndReason, deps[2].EndReason)
	}

	gone, err := w.store.Workers.Deactivate(ctx, nat, w.admin, hw.ID, "relocated", ip)
	if err != nil {
		t.Fatal(err)
	}
	expect = append(expect, ActionDeploymentEnd, ActionWorkerDeactivate)
	rows = check("deactivate")
	if gone.Active() || gone.DeactivatedAt == nil || gone.DeactivationReason != "relocated" {
		t.Errorf("deactivated worker = %+v", gone)
	}
	// Still listed where they last served, and still their district's.
	if gone.Deployment == nil || gone.Deployment.LocationID != w.a.parish || gone.Deployment.Active() {
		t.Errorf("a deactivated worker should be seen through their ended posting: %+v", gone.Deployment)
	}
	if _, err := w.store.Workers.Get(ctx, w.a.scope(), hw.ID); err != nil {
		t.Errorf("the district lost sight of a worker who left: %v", err)
	}
	if rows[len(rows)-1].after["status"] != "inactive" || rows[len(rows)-1].before["status"] != "active" {
		t.Errorf("deactivate audit = %v -> %v", rows[len(rows)-1].before["status"], rows[len(rows)-1].after["status"])
	}

	// A double submit is not a second deactivation.
	if _, err := w.store.Workers.Deactivate(ctx, nat, w.admin, hw.ID, "relocated", ip); err != nil {
		t.Fatal(err)
	}
	check("a repeated deactivate")

	back, err := w.store.Workers.Reactivate(ctx, nat, w.admin, hw.ID, ip)
	if err != nil {
		t.Fatal(err)
	}
	expect = append(expect, ActionWorkerReactivate)
	rows = check("reactivate")
	if rows[len(rows)-1].before["deactivation_reason"] != "relocated" {
		t.Error("reactivating lost the reason they left from the history")
	}
	if !back.Active() || back.Deployment.Active() {
		t.Errorf("reactivation should open no posting: %+v", back.Deployment)
	}

	// The prefilled form posts the placement they left from; saving it places
	// them again, and the facility they last reported to comes back with
	// them, because it is in the same district.
	again, err := w.store.Workers.Update(ctx, nat, w.admin, hw.ID, in, ip)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Deployment.Active() || again.Deployment.FacilityID == nil || *again.Deployment.FacilityID != w.a.facility {
		t.Errorf("redeployed posting = %+v", again.Deployment)
	}
	expect = append(expect, ActionDeploymentStart, ActionDeploymentUpdate)
	check("redeploy after reactivation")

	for _, r := range w.auditFor(t, hw.ID) {
		if r.districtID == nil || *r.districtID != w.a.id {
			t.Errorf("%s audited against district %v, want %d", r.action, r.districtID, w.a.id)
		}
	}
}

// A transfer across districts moves the RBAC anchor with the worker, leaves
// the facility behind rather than stranding it, and keeps the worker code: it
// says where the worker entered the register.
func TestACrossDistrictTransfer(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	nat := auth.National()
	hw := w.vhtIn(t, w.a, "Moses", randomTag())

	if code, ok := domain.NormalizeWorkerCode(hw.Code); !ok || code != hw.Code || !strings.HasPrefix(hw.Code, w.a.code) {
		t.Errorf("worker code %q, want %s followed by five digits", hw.Code, w.a.code)
	}
	if err := w.store.Deployments.SetFacility(ctx, nat, w.admin, hw.ID, &w.a.facility, ip); err != nil {
		t.Fatal(err)
	}

	moved, err := w.store.Workers.Update(ctx, nat, w.admin, hw.ID, WorkerInput{
		FirstName: hw.FirstName, LastName: hw.LastName, Sex: hw.Sex, AgeYears: hw.AgeYears,
		CadreID: w.vht, LocationID: w.b.villages[0],
	}, ip)
	if err != nil {
		t.Fatal(err)
	}
	if moved.DistrictID == nil || *moved.DistrictID != w.b.id {
		t.Errorf("district_id after transfer = %v, want %d", moved.DistrictID, w.b.id)
	}
	if moved.Deployment.FacilityID != nil {
		t.Errorf("the facility crossed the district line: %d", *moved.Deployment.FacilityID)
	}
	if moved.Code != hw.Code {
		t.Errorf("worker code changed on transfer: %s -> %s", hw.Code, moved.Code)
	}

	// The anchor moved, so the scopes swap too.
	if _, err := w.store.Workers.Get(ctx, w.a.scope(), hw.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("old district still sees a transferred worker: %v", err)
	}
	if _, err := w.store.Workers.Get(ctx, w.b.scope(), hw.ID); err != nil {
		t.Errorf("new district cannot see its transferred worker: %v", err)
	}
	// And the code still finds them, wherever they now serve.
	page, err := w.store.Workers.List(ctx, w.b.scope(), Filter{Query: strings.ToLower(hw.Code)})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page.Workers); len(got) != 1 || got[0] != hw.ID {
		t.Errorf("search by code %s = %v", hw.Code, got)
	}
}

// Duplicate NIN refuses; a duplicate name at the same location is only found,
// for the form to warn about.
func TestDuplicates(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	nat := auth.National()
	tag := randomTag()
	nin := randomNIN()

	in := WorkerInput{NIN: nin, FirstName: "Ruth", LastName: tag, Sex: domain.SexFemale,
		CadreID: w.vht, LocationID: w.a.villages[0]}
	first, err := w.store.Workers.Create(ctx, nat, w.admin, in, ip)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.Workers.Create(ctx, nat, w.admin, in, ip); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second worker with NIN %s = %v, want ErrConflict", nin, err)
	}
	if n, _ := w.store.Workers.Matching(ctx, nat, Filter{Query: tag}); n != 1 {
		t.Errorf("a refused duplicate left %d workers", n)
	}

	in.NIN = ""
	second, err := w.store.Workers.Create(ctx, nat, w.admin, in, ip)
	if err != nil {
		t.Fatalf("same name, no NIN: %v", err)
	}
	dups, err := w.store.Workers.PossibleDuplicates(ctx, nat, w.a.villages[0], "ruth", strings.ToUpper(tag), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(dups); len(got) != 1 || got[0] != first.ID {
		t.Errorf("possible duplicates = %v, want %d", got, first.ID)
	}
	if dups, _ := w.store.Workers.PossibleDuplicates(ctx, w.b.scope(), w.a.villages[0], "Ruth", tag, second.ID); len(dups) != 0 {
		t.Errorf("the duplicate probe answered across the scope: %v", ids(dups))
	}

	// A NIN is not searchable: the one box takes names and codes only.
	page, err := w.store.Workers.List(ctx, nat, Filter{Query: nin})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Workers) != 0 {
		t.Errorf("searching by NIN found %v", ids(page.Workers))
	}
}

// ------------------------------------------------------------------ the listing

// Keyset pages walk the selection once each way: every row appears exactly
// once going forward, in name order, and walking back retraces the same pages.
func TestKeysetPagesCoverTheSelectionOnce(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	sc := w.a.scope()
	tag := randomTag()

	// Two share a name: id is what makes the order total.
	for _, first := range []string{"Esther", "Amos", "david", "Carol", "Carol"} {
		w.vhtIn(t, w.a, first, tag)
	}

	var forward [][]int64
	var cursors []Page
	f := Filter{Query: tag, Limit: 2}
	for {
		page, err := w.store.Workers.List(ctx, sc, f)
		if err != nil {
			t.Fatal(err)
		}
		forward = append(forward, ids(page.Workers))
		cursors = append(cursors, page)
		if !page.HasNext {
			break
		}
		if len(forward) > 5 {
			t.Fatal("paging did not terminate")
		}
		f.After, f.Before = page.Last, nil
	}

	var all []int64
	var names []string
	for _, p := range cursors {
		for _, hw := range p.Workers {
			all = append(all, hw.ID)
			names = append(names, strings.ToLower(hw.FirstName))
		}
	}
	if len(forward) != 3 || len(all) != 5 {
		t.Fatalf("pages = %v, want 3 pages holding 5 rows", forward)
	}
	seen := map[int64]bool{}
	for _, id := range all {
		if seen[id] {
			t.Errorf("worker %d appeared twice", id)
		}
		seen[id] = true
	}
	if want := []string{"amos", "carol", "carol", "david", "esther"}; !sameStrings(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}
	if cursors[0].HasPrev || !cursors[2].HasPrev {
		t.Error("HasPrev wrong at the ends")
	}

	back, err := w.store.Workers.List(ctx, sc, Filter{Query: tag, Limit: 2, Before: cursors[2].First})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(back.Workers); len(got) != 2 || got[0] != forward[1][0] || got[1] != forward[1][1] {
		t.Errorf("page back = %v, want %v", got, forward[1])
	}
}

// The dashboard and the listing count the same register through the same
// posting; a tile that disagreed with the list beneath it would be believed.
func TestTheDashboardAgreesWithTheListing(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	tag := randomTag()
	w.vhtIn(t, w.a, "Counted", tag)
	left := w.vhtIn(t, w.a, "Departed", tag)
	if _, err := w.store.Workers.Deactivate(ctx, auth.National(), w.admin, left.ID, "left", ip); err != nil {
		t.Fatal(err)
	}

	for _, sc := range []auth.Scope{w.a.scope(), auth.National()} {
		totals, err := w.store.Stats.Totals(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		all, _ := w.store.Workers.Matching(ctx, sc, Filter{})
		active, _ := w.store.Workers.Matching(ctx, sc, Filter{Status: domain.WorkerActive})
		inactive, _ := w.store.Workers.Matching(ctx, sc, Filter{Status: domain.WorkerInactive})
		if totals.Total != all || totals.Active != active || totals.Inactive != inactive {
			t.Errorf("national=%v: tiles %d/%d/%d, listing %d/%d/%d", sc.IsNational(),
				totals.Total, totals.Active, totals.Inactive, all, active, inactive)
		}

		split, err := w.store.Stats.Cadres(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range split {
			n, _ := w.store.Workers.Matching(ctx, sc, Filter{Cadre: c.Slug})
			if c.Active+c.Inactive != n {
				t.Errorf("national=%v: %s tile %d, listing %d", sc.IsNational(), c.Slug, c.Active+c.Inactive, n)
			}
		}
	}
}

// ------------------------------------------------------------------ sessions

// Sessions are rows so that revocation is a DELETE; they expire absolutely
// and when idle; a disabled account's session stops working at once; and the
// raw token never reaches the table (invariant 10).
func TestSessionLifecycle(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	s := w.store.Sessions

	user, err := w.store.Users.Create(ctx, auth.National(), NewUser{
		Email: "session-" + randomTag() + "@test.hwr", FullName: "Session",
		Role: domain.RoleNationalViewer, Password: "integration-pass-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	open := func() (string, []byte) {
		t.Helper()
		token, err := s.Create(ctx, user.ID, ip, "go test")
		if err != nil {
			t.Fatal(err)
		}
		return token, auth.HashToken(token)
	}
	live := func(hash []byte) error {
		_, err := s.Authenticate(ctx, hash)
		return err
	}

	token, hash := open()
	if got, err := s.Authenticate(ctx, hash); err != nil || got.ID != user.ID {
		t.Fatalf("fresh session = %v, %v", got.ID, err)
	}
	var raw int
	if err := w.pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE token_hash = $1 OR encode(token_hash,'escape') = $2`,
		[]byte(token), token).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 0 {
		t.Error("the raw session token is stored")
	}
	if err := live([]byte(token)); !errors.Is(err, domain.ErrSessionExpired) {
		t.Errorf("the raw token authenticated as a hash: %v", err)
	}

	if err := s.Delete(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if err := live(hash); !errors.Is(err, domain.ErrSessionExpired) {
		t.Errorf("revoked session = %v, want ErrSessionExpired", err)
	}

	_, hash = open()
	if _, err := w.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = now() - $2::interval - interval '1 minute'
	    WHERE token_hash = $1`, hash, auth.SessionIdle.String()); err != nil {
		t.Fatal(err)
	}
	if err := live(hash); !errors.Is(err, domain.ErrSessionExpired) {
		t.Errorf("idle session = %v, want ErrSessionExpired", err)
	}

	_, hash = open()
	if _, err := w.pool.Exec(ctx, `UPDATE sessions SET expires_at = now() - interval '1 second'
	    WHERE token_hash = $1`, hash); err != nil {
		t.Fatal(err)
	}
	if err := live(hash); !errors.Is(err, domain.ErrSessionExpired) {
		t.Errorf("expired session = %v, want ErrSessionExpired", err)
	}

	_, hash = open()
	if _, err := w.store.Users.SetStatus(ctx, auth.National(), user.ID, domain.UserDisabled); err != nil {
		t.Fatal(err)
	}
	if err := live(hash); !errors.Is(err, domain.ErrSessionExpired) {
		t.Errorf("disabled user's session = %v, want ErrSessionExpired", err)
	}
}

// ------------------------------------------------------------------ imports

// Committing is claimed first, so a double-clicked commit cannot run twice;
// and a batch is read through the uploader's scope.
func TestAnImportBatchIsClaimedOnceAndScoped(t *testing.T) {
	w := integration(t)
	ctx := context.Background()

	batch, err := w.store.Imports.Create(ctx, w.a.scope(), w.manager, "claim.csv", domain.FormatCSV, []string{"first_name"}, ip)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.Imports.Get(ctx, w.b.scope(), batch.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("another district opened the batch: %v", err)
	}
	if ok, err := w.store.Imports.Claim(ctx, w.b.scope(), batch.ID); err != nil || ok {
		t.Errorf("another district claimed the batch: %v, %v", ok, err)
	}

	var wg sync.WaitGroup
	won := make(chan bool, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := w.store.Imports.Claim(ctx, w.a.scope(), batch.ID)
			if err != nil {
				t.Error(err)
			}
			won <- ok
		}()
	}
	wg.Wait()
	close(won)
	winners := 0
	for ok := range won {
		if ok {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("%d concurrent claims won, want exactly 1", winners)
	}

	// A released claim can be taken again; a stale one expires.
	if err := w.store.Imports.Release(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.store.Imports.Claim(ctx, w.a.scope(), batch.ID); !ok {
		t.Error("a released batch could not be claimed")
	}
	if _, err := w.pool.Exec(ctx, `UPDATE import_batches SET committing_at = now() - $2::interval - interval '1 minute' WHERE id = $1`,
		batch.ID, CommitLease.String()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.store.Imports.Claim(ctx, w.a.scope(), batch.ID); !ok {
		t.Error("a lapsed lease was not reclaimable")
	}
}

// A refused row carries its reason all the way into quarantine (invariant 7):
// the store will not quarantine a row with nothing to say, and the schema will
// not hold a refusal without one.
func TestARefusalIsNeverSilent(t *testing.T) {
	w := integration(t)
	ctx := context.Background()
	batch, err := w.store.Imports.Create(ctx, auth.National(), w.admin, "refuse.csv", domain.FormatCSV, []string{"first_name"}, ip)
	if err != nil {
		t.Fatal(err)
	}

	silent := domain.ImportRow{Number: 2, Raw: map[string]string{"first_name": "x"}, Status: domain.RowRejected}
	if err := w.store.Imports.Stage(ctx, auth.National(), batch.ID, []domain.ImportRow{silent}); err == nil {
		t.Error("staged a rejected row with no problem to explain it")
	}
	if err := w.store.Imports.Quarantine(ctx, batch.ID, silent); err == nil {
		t.Error("quarantined a row with no reason")
	}
	explained := silent
	explained.Problems = []domain.Problem{{Field: "village", Code: domain.ProblemLocationMissing, Message: "no such village"}}
	if err := w.store.Imports.Stage(ctx, auth.National(), batch.ID, []domain.ImportRow{explained}); err != nil {
		t.Fatalf("stage an explained refusal: %v", err)
	}
	if err := w.store.Imports.Quarantine(ctx, batch.ID, explained); err != nil {
		t.Fatal(err)
	}
	var reason, detail string
	if err := w.pool.QueryRow(ctx, `SELECT reason, detail FROM import_quarantine WHERE batch_id = $1`, batch.ID).
		Scan(&reason, &detail); err != nil {
		t.Fatal(err)
	}
	if reason != string(domain.ProblemLocationMissing) || detail != "no such village" {
		t.Errorf("quarantine = %q / %q", reason, detail)
	}

	// Marking a staged refusal failed without a reason keeps the one it had.
	if err := w.store.Imports.MarkRow(ctx, batch.ID, 2, domain.RowFailed, nil); err != nil {
		t.Errorf("re-marking a refused row failed: %v", err)
	}
}
