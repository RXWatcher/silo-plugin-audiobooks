// Package testutil provides shared helpers for integration tests across this
// plugin's packages.
package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartPG starts a fresh pgvector-enabled Postgres container, creates
// the `audiobooks` schema, and returns a DSN with
// search_path=audiobooks. Tests are skipped when Docker is unavailable.
//
// We use pgvector's image rather than plain postgres:18-alpine because
// migration 0016 installs the pgvector extension for the embedding
// surface; the plain image errors with "extension vector is not
// available." Operators in production also need pgvector — this keeps
// tests honest about the deployment requirement.
func StartPG(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	c, err := tcpostgres.Run(ctx, "pgvector/pgvector:pg17",
		tcpostgres.WithDatabase("silo"),
		tcpostgres.WithUsername("plugin_audiobooks"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("skip: docker postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable&search_path=audiobooks")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS audiobooks"); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return dsn
}
