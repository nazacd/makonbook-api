package main

import (
	"context"
	"fmt"
	"io"

	"github.com/nazacd/makonbook-api/internal/library"
	"github.com/nazacd/makonbook-api/internal/platform/config"
)

const libraryUsage = `
  library import <folder>   load the Library's books from a checkout of the private
                            makonbook-library repository: books/<name>.json and <name>.pdf, and
                            covers/<name>.png (or .jpg, .webp). New or changed files go to the
                            uploads bucket (needs R2_UPLOADS_*). Everything is checked first and
                            nothing is stored unless all of it passes; a book gone from the
                            folder leaves the shelf (its readers' places are kept)
`

func runLibrary(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	im := library.Importer{Pool: pool, Progress: func(done, total int) {
		fmt.Fprintf(stdout, "files uploaded: %d of %d\n", done, total)
	}}
	if c := uploadsClient(cfg); c != nil {
		im.Store = c
	}
	s, err := im.Import(ctx, args[2])
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d books on the shelf (%d new, %d changed, %d taken off); %d files uploaded\n", s.Listed, s.New, s.Changed, s.Unlisted, s.Uploaded)
	return nil
}
