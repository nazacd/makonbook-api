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
	"github.com/nazacd/makonbook-api/internal/assignments"
	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/library"
	"github.com/nazacd/makonbook-api/internal/parents"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/alert"
	"github.com/nazacd/makonbook-api/internal/platform/httpx"
	"github.com/nazacd/makonbook-api/internal/platform/mail"
	"github.com/nazacd/makonbook-api/internal/platform/r2"
	"github.com/nazacd/makonbook-api/internal/platform/render"
	"github.com/nazacd/makonbook-api/internal/platform/sms"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap"
	"github.com/nazacd/makonbook-api/internal/system"
	"github.com/nazacd/makonbook-api/internal/system/systemdb"
	"github.com/nazacd/makonbook-api/internal/universities"
	"github.com/nazacd/makonbook-api/internal/vocab"
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
	HolliHopWriteEnabled bool
	// Figures reads question images for the practice editor (SAT MAKON's
	// R2 bucket, read only); nil when not configured.
	Figures *r2.Client
	// Uploads is MakonBook's own R2 bucket, for editors' images; nil when
	// not configured.
	Uploads *r2.Client
	// Renderer draws question pictures (a headless Chrome); nil when not
	// configured.
	Renderer *render.Browser
	// AdminHost serves the admin panel (empty: no panel).
	AdminHost string
	// Notifier tells the admins about parents' reports (Telegram, or the log).
	Notifier alert.Notifier
	// Accounts (spec 21): SMS sends the codes of sign-up and password reset,
	// in SMSText ({code}); nil turns both off. Google turns on Google
	// sign-in; nil leaves it off.
	SMS     sms.Sender
	SMSText string
	Google  *auth.GoogleConfig
	// Mail sends the email codes sign-up can start with; nil: no email.
	Mail mail.Sender
	// Telegram turns on Telegram sign-in (OpenID Connect); nil leaves it off.
	Telegram *auth.TelegramConfig
}

// Handler builds the complete HTTP handler: every feature's routes behind the
// shared middleware.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", httpx.NotFound)

	directory := people.NewDirectory(d.Pool)
	authSvc := auth.NewService(d.Pool, directory, auth.NewHasher()).WithLog(d.Log)
	if d.SMS != nil {
		authSvc.WithSMS(d.SMS, d.SMSText, d.Log)
	}
	if d.Google != nil {
		authSvc.WithGoogle(*d.Google)
	}
	if d.Mail != nil {
		authSvc.WithMail(d.Mail)
	}
	if d.Telegram != nil {
		authSvc.WithTelegram(*d.Telegram)
	}
	signIn := system.SignIn{
		Signup: authSvc.SignupEnabled(), Reset: authSvc.ResetEnabled(), Email: authSvc.MailEnabled(), Google: authSvc.GoogleEnabled(),
		Telegram: authSvc.TelegramEnabled(),
	}
	system.NewHandler(systemdb.New(d.Pool), d.Pool).WithSignIn(signIn).Register(mux)
	authHandler := auth.NewHandler(authSvc, d.Location, d.TrustProxy)
	authHandler.Register(mux)
	lessonsSvc := lessons.NewService(d.Pool, d.Location)
	lessons.NewHandler(lessonsSvc, d.Location, authHandler.Require).Register(mux)
	attendanceSvc := attendance.NewService(d.Pool, lessonsSvc)
	attendance.NewHandler(attendanceSvc, d.Location, authHandler.Require).Register(mux)
	notifier := d.Notifier
	if notifier == nil {
		notifier = alert.Log{Logger: d.Log}
	}
	adminURL := ""
	if d.AdminHost != "" {
		adminURL = "https://" + d.AdminHost
	}
	parentsSvc := parents.NewService(d.Pool, directory, attendanceSvc, notifier, adminURL)
	parents.NewHandler(parentsSvc, d.Location, authHandler.Require, d.TrustProxy, d.Log).Register(mux)
	var logos universities.Store // the uploads bucket, where the import put the logos
	if d.Uploads != nil {
		logos = d.Uploads
	}
	var books library.Store // the uploads bucket, where the import put the books
	if d.Uploads != nil {
		books = d.Uploads
	}
	library.NewHandler(library.NewService(d.Pool, books), authHandler.RequireContent).Register(mux)
	universitiesSvc := universities.NewService(d.Pool, logos)
	universities.NewHandler(universitiesSvc, authHandler.Require).Register(mux)
	practiceSvc := practiceService(d).WithUniversities(universitiesSvc.SummaryOf)
	// The content is locked for registered students until they add a phone.
	practice.NewHandler(practiceSvc, d.Location, authHandler.RequireContent).Register(mux)
	vocab.NewHandler(vocab.NewService(d.Pool, signer(d.Uploads)), d.Location, authHandler.RequireContent).Register(mux)
	roadmapSvc := roadmap.NewService(d.Pool, practiceSvc, directory)
	authSvc.WithOnboarded(roadmapSvc.Onboarded)
	roadmap.NewHandler(roadmapSvc, d.Location, authSvc.Profile, authHandler.Require, authHandler.RequireContent).Register(mux)
	assignments.NewHandler(assignments.NewService(d.Pool, roadmapSvc, practiceSvc, d.Location), d.Location,
		authHandler.Require, authHandler.RequireContent).Register(mux)
	panel := admin.Panel{
		Lessons: lessonsSvc, Attendance: attendanceSvc, People: directory, Practice: practiceSvc, Parents: parentsSvc,
		Location: d.Location, WriteEnabled: d.HolliHopWriteEnabled,
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

// uploadStore keeps a nil client a nil interface (no uploads).
// practiceService is the practice service with what is configured: the
// two image buckets and the picture renderer.
func practiceService(d Deps) *practice.Service {
	svc := practice.NewService(d.Pool, uploadStore(d.Uploads))
	if d.Figures != nil {
		svc.WithBank(d.Figures)
	}
	if d.Renderer != nil {
		page, err := adminweb.CardPage()
		if err != nil {
			d.Log.Warn("no question pictures", "err", err)
		} else {
			svc.WithPictures(d.Renderer, page)
		}
	}
	return svc
}

// signer keeps a nil client a nil interface (no image links).
func signer(c *r2.Client) vocab.Signer {
	if c == nil {
		return nil
	}
	return c
}

func uploadStore(c *r2.Client) practice.Store {
	if c == nil {
		return nil
	}
	return c
}
