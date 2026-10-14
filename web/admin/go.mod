// A module of its own so the API's ./... never walks into node_modules (some
// npm packages ship .go files). It holds only embed.go; the API requires it
// through a replace directive.
module github.com/nazacd/makonbook-api/web/admin

go 1.27.0
