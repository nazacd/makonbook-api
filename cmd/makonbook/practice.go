package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/nazacd/makonbook-api/internal/platform/config"
	"github.com/nazacd/makonbook-api/internal/platform/r2"
	"github.com/nazacd/makonbook-api/internal/practice"
)

const practiceUsage = `
  practice import [--replace] <path>...
                            load reviewed tests from the makonbook-questionbank checkout:
                            a test folder, its questions.jsonl, or the checkout itself (every
                            test in it). Tests already loaded are skipped; --replace reloads
                            them, as long as no question in them was approved or edited
  practice list             tests and how far their review has come
`

func validPracticeArgs(args []string) bool {
	switch {
	case len(args) == 2 && args[1] == "list":
		return true
	case len(args) >= 3 && args[1] == "import":
		return args[2] != "--replace" || len(args) >= 4
	}
	return false
}

func runPractice(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	im := practice.Importer{Pool: pool}
	if cfg.R2UploadsBucket != "" { // for the figures the fix step drew
		im.Uploads = &r2.Client{Endpoint: cfg.R2UploadsEndpoint, Bucket: cfg.R2UploadsBucket, Region: cfg.R2Region,
			AccessKey: cfg.R2UploadsAccessKeyID, SecretKey: cfg.R2UploadsSecretAccessKey}
	}

	if args[1] == "list" {
		tests, err := practice.NewService(pool, nil).Tests(ctx)
		if err != nil {
			return err
		}
		if len(tests) == 0 {
			fmt.Fprintln(stdout, "No tests loaded yet.")
		}
		for _, t := range tests {
			state := "locked"
			if t.Open {
				state = "OPEN"
			}
			fmt.Fprintf(stdout, "%-8s %-6s approved %3d/%d  to review: red %d, yellow %d  shared with other tests: %d\n",
				t.Code, state, t.Approved, t.Questions, t.Red, t.Yellow, t.Shared)
		}
		return nil
	}

	paths, replace := args[2:], false
	if paths[0] == "--replace" {
		paths, replace = paths[1:], true
	}
	var files []string
	for _, p := range paths {
		found, err := practice.Find(p)
		if err != nil {
			return err
		}
		files = append(files, found...)
	}
	failed := 0
	for _, f := range files {
		t, err := practice.Read(f)
		if err == nil {
			var s practice.Summary
			s, err = im.Import(ctx, t, replace)
			if err == nil && s.Refreshed > 0 {
				fmt.Fprintf(stdout, "%-8s already loaded; %d AI reviews updated (fixes added)\n", s.Code, s.Refreshed)
				continue
			}
			if err == nil {
				verb := "loaded"
				if s.Replaced {
					verb = "reloaded"
				}
				fmt.Fprintf(stdout, "%-8s %s: %d questions (%d new, %d already stored from other tests); green %d, yellow %d, red %d\n",
					s.Code, verb, s.Questions, s.New, s.Repeats, s.Risk["green"], s.Risk["yellow"], s.Risk["red"])
				continue
			}
		}
		if errors.Is(err, practice.ErrImported) {
			fmt.Fprintf(stdout, "%-8s skipped: already loaded\n", t.Code)
			continue
		}
		failed++
		fmt.Fprintf(stdout, "%s: NOT loaded: %v\n", f, err)
	}
	// Green questions get the small fixes their AI review proposed.
	fixed, err := im.ApplyAIFixes(ctx)
	if err != nil {
		return err
	}
	if fixed.Applied+fixed.Review > 0 {
		fmt.Fprintf(stdout, "AI fixes: %d applied to approved questions; %d couldn't be applied, sent to review\n", fixed.Applied, fixed.Review)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d tests not loaded", failed, len(files))
	}
	return nil
}
