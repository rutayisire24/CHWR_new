// Package store holds the SQL. One file per aggregate, hand-written queries,
// no builders and no ORM.
//
// Every method that reads or writes registry data takes an auth.Scope, so a
// handler cannot forget to apply one — the call does not compile without it.
// The single exception is Users.Credentials, which runs before there is a
// principal to scope by; it is documented at its definition.
package store

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"chwr/internal/domain"
)

// Store bundles the per-aggregate stores over one pool.
type Store struct {
	Users     *Users
	Sessions  *Sessions
	Audit     *Audit
	Locations *Locations
	CHWs      *CHWs
	Profiles  *Profiles
	Stats     *Stats
}

// New builds every store over the shared pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{
		Users:     &Users{pool: pool},
		Sessions:  &Sessions{pool: pool},
		Audit:     &Audit{pool: pool},
		Locations: &Locations{pool: pool},
		CHWs:      &CHWs{pool: pool},
		Profiles:  &Profiles{pool: pool},
		Stats:     &Stats{pool: pool},
	}
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
		}
	}
	return err
}
