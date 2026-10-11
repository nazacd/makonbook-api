// Package testdb gives integration tests their own freshly migrated database.
//
// Tests that need Postgres call New; they are skipped unless
// TEST_DATABASE_URL points at a server where that role may CREATE DATABASE.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/platform/migrate"
	"github.com/nazacd/makonbook-api/internal/platform/postgres"
)

// New creates an empty database, applies all migrations and returns a pool
// connected to it. The database is dropped when the test ends.
func New(t *testing.T) *pgxpool.Pool {
	pool := NewEmpty(t)
	if err := migrate.Up(t.Context(), pool, io.Discard); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return pool
}

// NewEmpty is like New but applies no migrations.
func NewEmpty(t *testing.T) *pgxpool.Pool {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database test")
	}

	ctx := t.Context()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect to TEST_DATABASE_URL: %v", err)
	}

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	u, err := url.Parse(adminURL)
	if err != nil || u.Scheme == "" {
		t.Fatalf("TEST_DATABASE_URL must be a postgres:// URL: %v", err)
	}
	u.Path = "/" + name
	pool, err := postgres.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		ctx := context.Background()
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
		_ = admin.Close(ctx)
	})
	return pool
}
