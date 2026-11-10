package main

import (
	"context"
	"fmt"
	"io"

	"github.com/nazacd/makonbook-api/internal/platform/config"
	"github.com/nazacd/makonbook-api/internal/universities"
)

const universitiesUsage = `
  universities import <folder>
                            load the universities from a checkout of the private
                            makonbook-universities repository: data/list.jsonl, the records
                            collected so far (data/universities.jsonl), data/countries.jsonl and
                            logos/ (uploaded to the uploads bucket: needs R2_UPLOADS_*).
                            Everything is checked first and nothing is stored unless all of it
                            passes; universities gone from the data are kept, unlisted, for the
                            students who chose them
`

func runUniversities(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	im := universities.Importer{Pool: pool}
	if c := uploadsClient(cfg); c != nil {
		im.Store = c
	}
	s, err := im.Import(ctx, args[2])
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d universities listed (%d new, %d changed, %d unlisted); %d with their whole record, %d with a logo\n",
		s.Listed, s.New, s.Changed, s.Unlisted, s.WithDetails, s.WithLogo)
	fmt.Fprintf(stdout, "%d logos uploaded; %d countries\n", s.LogosUploaded, s.Countries)
	return nil
}
