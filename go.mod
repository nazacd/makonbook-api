module github.com/nazacd/makonbook-api

go 1.27.0

require (
	github.com/chromedp/cdproto v0.157.8
	github.com/chromedp/chromedp v0.20.0
	github.com/chromedp/chromedp/remote v0.1.0
	github.com/jackc/pgx/v5 v5.11.0
	github.com/nazacd/makonbook-api/web/admin v0.0.0
	github.com/nyaruka/phonenumbers v1.8.1
	github.com/pressly/goose/v3 v3.29.0
	golang.org/x/crypto v0.58.0
	golang.org/x/image v0.30.0
	golang.org/x/text v0.43.0
)

require (
	github.com/go-json-experiment/json v0.0.0-20260213210345-44df1a37e875 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/sethvargo/go-retry v0.5.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/nazacd/makonbook-api/web/admin => ./web/admin
