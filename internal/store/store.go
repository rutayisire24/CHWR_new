// Package store holds the SQL. One file per aggregate, hand-written queries,
// no builders and no ORM.
//
// Every method that reads or writes registry data takes an auth.Scope, so a
// handler cannot forget to apply one — the call does not compile without it.
// The single exception is Users.Credentials, which runs before there is a
// principal to scope by; it is documented at its definition.
package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"hwr/internal/domain"
)

// Store bundles the per-aggregate stores over one pool.
type Store struct {
	Users       *Users
	Sessions    *Sessions
	Audit       *Audit
	Locations   *Locations
	Workers     *Workers
	Deployments *Deployments
	Cadres      *Cadres
	Profiles    *Profiles
	Persons     *Persons
	Activities  *Activities
	Stats       *Stats
	Imports     *Imports
	Export      *Export
}

// New builds every store over the shared pool.
func New(pool *pgxpool.Pool) *Store {
	deployments := &Deployments{pool: pool}
	return &Store{
		Users:       &Users{pool: pool},
		Sessions:    &Sessions{pool: pool},
		Audit:       &Audit{pool: pool},
		Locations:   &Locations{pool: pool},
		Workers:     &Workers{pool: pool, deployments: deployments},
		Deployments: deployments,
		Cadres:      &Cadres{pool: pool},
		Profiles:    &Profiles{pool: pool},
		Persons:     &Persons{pool: pool},
		Activities:  &Activities{pool: pool},
		Stats:       &Stats{pool: pool},
		Imports:     &Imports{pool: pool},
		Export:      &Export{pool: pool},
	}
}

// begin opens a transaction that writes as actor. Every row it inserts or
// updates is stamped with the actor in created_by / last_updated_by by the
// record-column triggers (0002), which read it from a transaction-local
// setting. Mutations open their transaction here rather than with pool.Begin:
// this is the one place an actor could be forgotten, so it is the one place
// it is supplied.
func begin(ctx context.Context, pool *pgxpool.Pool, actor domain.User) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if err := ActAs(ctx, tx, actor); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// ActAs names the acting user for the rest of a caller's transaction, for a
// transaction this package did not open. A zero user writes as the system.
func ActAs(ctx context.Context, tx pgx.Tx, actor domain.User) error {
	if actor.ID == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('hwr.actor_id', $1, true)`,
		strconv.FormatInt(actor.ID, 10)); err != nil {
		return fmt.Errorf("act as user %d: %w", actor.ID, err)
	}
	return nil
}

// translate maps pgx and PostgreSQL errors onto the domain sentinels. Callers
// wrap the result with context; only the sentinel identity matters upstream.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return domain.ErrConflict
		case "P0001": // raise_exception: a trigger enforcing a rule
			return fmt.Errorf("%w: %s", domain.ErrRefused, pgErr.Message)
		}
	}
	return err
}
