// Package app wires features into the HTTP handler.
package app

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/system"
	"github.com/nazacd/makonbook-api/internal/system/systemdb"
)

// Handler builds the complete HTTP handler: every feature's routes behind the
// shared middleware.
func Handler(pool *pgxpool.Pool, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", httpx.NotFound)

	system.NewHandler(systemdb.New(pool), pool).Register(mux)

	return httpx.Chain(mux,
		httpx.WithRequestContext(log),
		httpx.AccessLog,
		httpx.Recover,
	)
}
