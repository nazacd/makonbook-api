package migrate_test

import (
	"io"
	"io/fs"
	"testing"

	"github.com/nazacd/makonbook-api/db"
	"github.com/nazacd/makonbook-api/internal/platform/migrate"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// Every migration must apply cleanly, roll back cleanly, and apply again —
// otherwise `migrate down` is useless in an incident.
func TestMigrationsAreReversible(t *testing.T) {
	pool := testdb.NewEmpty(t)
	ctx := t.Context()

	files, err := fs.Glob(db.Migrations(), "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}

	if err := migrate.Up(ctx, pool, io.Discard); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if pending, err := migrate.HasPending(ctx, pool); err != nil || pending {
		t.Fatalf("HasPending after up = %v, %v; want false, nil", pending, err)
	}

	for range files {
		if err := migrate.Down(ctx, pool, io.Discard); err != nil {
			t.Fatalf("down: %v", err)
		}
	}

	if err := migrate.Up(ctx, pool, io.Discard); err != nil {
		t.Fatalf("second up: %v", err)
	}
}
