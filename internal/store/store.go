// Package store wraps pgx for the audiobooks portal. Domain-specific
// wrappers live in sibling files; this file holds the shared scaffolding.
package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is a thin pgx wrapper for the audiobooks plugin.
type Store struct {
	pool *pgxpool.Pool
}

// New constructs a Store bound to the given pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the underlying pool for transactional callers.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ErrNotFound is returned by Get* methods when a row does not exist.
var ErrNotFound = errors.New("not found")
