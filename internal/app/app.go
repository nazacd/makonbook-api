// Package app wires features into the HTTP handler.
package app

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/admin"
	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/lessons"
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
	// HolliHop checks admins' sign-ins; nil (not configured) refuses them.
	HolliHop admin.HolliHop
	// SecureCookies marks cookies Secure (HTTPS only); off only for local
	// development over plain HTTP.
	SecureCookies bool
	// HolliHopWriteEnabled: attendance is really written to HolliHop (the
	// worker's setting), so the admin panel can say when it's a dry run.
	HolliHopWriteEnabled bool
}

// Handler builds the complete HTTP handler: every feature's routes behind the
// shared middleware.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", httpx.NotFound)

	directory := people.NewDirectory(d.Pool)
	authSvc := auth.NewService(d.Pool, directory, auth.NewHasher())

	system.NewHandler(systemdb.New(d.Pool), d.Pool).Register(mux)
	authHandler := auth.NewHandler(authSvc, d.Location, d.TrustProxy)
	authHandler.Register(mux)
	lessonsSvc := lessons.NewService(d.Pool, d.Location)
	lessons.NewHandler(lessonsSvc, d.Location, authHandler.Require).Register(mux)
	attendanceSvc := attendance.NewService(d.Pool, lessonsSvc)
	attendance.NewHandler(attendanceSvc, d.Location, authHandler.Require).Register(mux)
	panel := admin.Panel{
		Lessons: lessonsSvc, Attendance: attendanceSvc, People: directory,
		Location: d.Location, WriteEnabled: d.HolliHopWriteEnabled,
	}
	admin.NewHandler(admin.NewService(d.Pool, d.HolliHop), panel, d.TrustProxy, d.SecureCookies).Register(mux)

	return httpx.Chain(mux,
		httpx.WithRequestContext(d.Log),
		httpx.AccessLog,
		httpx.Recover,
	)
}
