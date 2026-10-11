// Package db embeds the SQL migrations so the binary carries its own schema.
package db

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the goose migration files, rooted at the migrations directory.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the directory is embedded at compile time
	}
	return sub
}
