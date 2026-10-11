// Package app wires features into the HTTP handler.
package app

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/system"
	"github.com/nazacd/makonbook-api/internal/system/systemdb"
)

// Deps are the shared resources features are built from.
type Deps struct {
	Pool       *pgxpool.Pool
	Log        *slog.Logger
	Location   *time.Location // school time zone
	TrustProxy bool           // behind a reverse proxy (X-Forwarded-For)
}

// Handler builds the complete HTTP handler: every feature's routes behind the
// shared middleware.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", httpx.NotFound)

	directory := people.NewDirectory(d.Pool)
	authSvc := auth.NewService(d.Pool, directory, auth.NewHasher())

	system.NewHandler(systemdb.New(d.Pool), d.Pool).Register(mux)
	auth.NewHandler(authSvc, d.Location, d.TrustProxy).Register(mux)

	return httpx.Chain(mux,
		httpx.WithRequestContext(d.Log),
		httpx.AccessLog,
		httpx.Recover,
	)
}
