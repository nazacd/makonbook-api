// Package migrate applies the embedded goose migrations.
package migrate

import (
	"context"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/nazacd/makonbook-api/db"
)

func newProvider(pool *pgxpool.Pool) (*goose.Provider, func(), error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, db.Migrations())
	if err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("create migration provider: %w", err)
	}
	return p, func() { _ = sqlDB.Close() }, nil
}

// Up applies every pending migration and reports each one to w.
func Up(ctx context.Context, pool *pgxpool.Pool, w io.Writer) error {
	p, closeDB, err := newProvider(pool)
	if err != nil {
		return err
	}
	defer closeDB()

	results, err := p.Up(ctx)
	for _, r := range results {
		fmt.Fprintf(w, "applied %s (%s)\n", r.Source.Path, r.Duration.Round(1e6))
	}
	if err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	if len(results) == 0 {
		fmt.Fprintln(w, "no pending migrations")
	}
	return nil
}

// Down rolls back the most recently applied migration.
func Down(ctx context.Context, pool *pgxpool.Pool, w io.Writer) error {
	p, closeDB, err := newProvider(pool)
	if err != nil {
		return err
	}
	defer closeDB()

	r, err := p.Down(ctx)
	if err != nil {
		return fmt.Errorf("migrate down: %w", err)
	}
	fmt.Fprintf(w, "rolled back %s\n", r.Source.Path)
	return nil
}

// Status writes the state of every migration to w.
func Status(ctx context.Context, pool *pgxpool.Pool, w io.Writer) error {
	p, closeDB, err := newProvider(pool)
	if err != nil {
		return err
	}
	defer closeDB()

	statuses, err := p.Status(ctx)
	if err != nil {
		return fmt.Errorf("migration status: %w", err)
	}
	for _, s := range statuses {
		applied := ""
		if s.State == goose.StateApplied {
			applied = s.AppliedAt.UTC().Format("2006-01-02 15:04:05Z")
		}
		fmt.Fprintf(w, "%-8s %-40s %s\n", s.State, s.Source.Path, applied)
	}
	return nil
}

// HasPending reports whether any migration has not been applied yet.
func HasPending(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	p, closeDB, err := newProvider(pool)
	if err != nil {
		return false, err
	}
	defer closeDB()
	return p.HasPending(ctx)
}
