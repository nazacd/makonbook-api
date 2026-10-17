// Package app wires features into the HTTP handler.
package app

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/admin"
	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/r2"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/system"
	"github.com/nazacd/makonbook-api/internal/system/systemdb"
	adminweb "github.com/nazacd/makonbook-api/web/admin"
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
	// Figures reads question images for the practice editor (R2, read
	// only); nil when not configured.
	Figures              *r2.Client
	HolliHopWriteEnabled bool
	// AdminHost serves the admin panel (empty: no panel).
	AdminHost string
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
		Lessons: lessonsSvc, Attendance: attendanceSvc, People: directory, Practice: practice.NewService(d.Pool),
		Location: d.Location, WriteEnabled: d.HolliHopWriteEnabled,
	}
	if d.Figures != nil {
		panel.Figures = d.Figures
	}
	admin.NewHandler(admin.NewService(d.Pool, d.HolliHop), panel, d.TrustProxy, d.SecureCookies).Register(mux)

	var root http.Handler = mux
	if d.AdminHost != "" {
		root = adminHost(d.AdminHost, adminweb.Handler(), mux)
	}
	return httpx.Chain(root,
		httpx.WithRequestContext(d.Log),
		httpx.AccessLog,
		httpx.Recover,
	)
}

// adminHost sends requests for the admin host to the panel, except the
// admin API, which shares its origin (so its cookie stays first-party and
// no CORS is needed). The rest of the API isn't reachable there.
func adminHost(host string, panel, api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Host
		if name, _, err := net.SplitHostPort(h); err == nil {
			h = name
		}
		if !strings.EqualFold(h, host) {
			api.ServeHTTP(w, r)
			return
		}
		switch p := r.URL.Path; {
		case strings.HasPrefix(p, "/v1/admin/"), p == "/healthz", p == "/readyz":
			api.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/v1/"):
			httpx.NotFound(w, r)
		default:
			panel.ServeHTTP(w, r)
		}
	})
}
