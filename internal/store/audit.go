package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/auth"
	"chwr/internal/domain"
)

// Audit appends to audit_log. Every mutation lands here with before/after
// JSONB; the table doubles as the CHW change history, which is why there is no
// separate versioning table.
type Audit struct {
	pool *pgxpool.Pool
}

// Action names are `entity.verb`. They are the vocabulary the audit screen
// filters on, so they are constants rather than free text at call sites.
const (
	ActionLogin          = "auth.login"
	ActionLoginFailed    = "auth.login_failed"
	ActionLogout         = "auth.logout"
	ActionPasswordChange = "auth.password_change"
	ActionUserCreate     = "user.create"
	ActionUserUpdate     = "user.update"
	ActionUserStatus     = "user.status"
	ActionUserReset      = "user.password_reset"
	ActionCHWCreate      = "chw.create"
	ActionCHWUpdate      = "chw.update"
	ActionCHWDeactivate  = "chw.deactivate"
	ActionCHWReactivate  = "chw.reactivate"
)

// Entry is one audit row. before and after are marshalled to JSONB; leave them
// nil for actions that carry no record state, such as a login.
type Entry struct {
	ActorID    *int64
	ActorEmail string
	Action     string
	Entity     string
	EntityID   *int64
	DistrictID *int64
	Before     any
	After      any
	IP         netip.Addr
}

// Record appends an entry outside any transaction. Use RecordTx when the entry
// accompanies a mutation, so the two commit or fail together.
func (a *Audit) Record(ctx context.Context, e Entry) error {
	return recordOn(ctx, a.pool, e)
}

// RecordTx appends an entry inside a caller's transaction. Invariant 6 says
// every mutation writes to audit_log; committing the two together is what
// makes that true rather than merely usual.
func (a *Audit) RecordTx(ctx context.Context, tx pgx.Tx, e Entry) error {
	return recordOn(ctx, tx, e)
}

// execer is the slice of *pgxpool.Pool and pgx.Tx that recordOn needs, so one
// implementation serves both the transactional and the standalone path.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func recordOn(ctx context.Context, q execer, e Entry) error {
	before, err := toJSON(e.Before)
	if err != nil {
		return fmt.Errorf("audit before: %w", err)
	}
	after, err := toJSON(e.After)
	if err != nil {
		return fmt.Errorf("audit after: %w", err)
	}

	var ipArg any
	if e.IP.IsValid() {
		ipArg = e.IP.String()
	}

	_, err = q.Exec(ctx, `
	    INSERT INTO audit_log (actor_id, actor_email, action, entity, entity_id,
	                           district_id, before, after, ip)
	    VALUES ($1, nullif($2,'')::citext, $3, $4, $5, $6, $7, $8, $9::inet)`,
		e.ActorID, e.ActorEmail, e.Action, e.Entity, e.EntityID,
		e.DistrictID, before, after, ipArg)
	if err != nil {
		return fmt.Errorf("write audit %s: %w", e.Action, translate(err))
	}
	return nil
}

func toJSON(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// LogEntry is an audit row read back for display.
type LogEntry struct {
	ID         int64
	ActorEmail string
	Action     string
	Entity     string
	EntityID   *int64
	DistrictID *int64
	CreatedAt  time.Time
}

// List returns the most recent entries inside the scope. District managers see
// their own district's slice; national admins see everything.
func (a *Audit) List(ctx context.Context, sc auth.Scope, limit int) ([]LogEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	q := `SELECT id, coalesce(actor_email::text,''), action, entity, entity_id,
	             district_id, created_at
	      FROM audit_log
	      WHERE true`
	var args []any

	if frag, extra := sc.Filter("district_id", len(args)+1); frag != "" {
		q += frag
		args = append(args, extra...)
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)

	rows, err := a.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", translate(err))
	}
	defer rows.Close()

	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.ActorEmail, &e.Action, &e.Entity,
			&e.EntityID, &e.DistrictID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ActorFrom builds the actor half of an Entry from an authenticated user.
func ActorFrom(u domain.User) Entry {
	return Entry{ActorID: &u.ID, ActorEmail: u.Email, DistrictID: u.DistrictID}
}
