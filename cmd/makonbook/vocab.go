package main

import (
	"context"
	"fmt"
	"io"

	"github.com/nazacd/makonbook-api/internal/platform/config"
	"github.com/nazacd/makonbook-api/internal/platform/r2"
	"github.com/nazacd/makonbook-api/internal/vocab"
)

const vocabUsage = `
  vocab import <path>       load the word lists: a folder with words.jsonl (or words.csv) and its
                            images/, or the file itself (format: docs/platform-spec.md, 11). The
                            bank becomes what the file says; words it no longer has are removed
                            (their reviews stay)
`

func validVocabArgs(args []string) bool {
	return len(args) == 3 && args[1] == "import"
}

func runVocab(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	entries, imagesDir, err := vocab.Read(args[2])
	if err != nil {
		return err
	}
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	var uploads vocab.Store
	if cfg.R2UploadsBucket != "" {
		uploads = &r2.Client{Endpoint: cfg.R2UploadsEndpoint, Bucket: cfg.R2UploadsBucket, Region: cfg.R2Region,
			AccessKey: cfg.R2UploadsAccessKeyID, SecretKey: cfg.R2UploadsSecretAccessKey}
	}
	s, err := vocab.Import(ctx, pool, uploads, entries, imagesDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d lists, %d words; removed: %d words, %d lists; images uploaded: %d\n",
		s.Lists, s.Words, s.RemovedWords, s.RemovedLists, s.ImagesStored)
	if s.Version == s.VersionBefore {
		fmt.Fprintln(stdout, "Nothing changed for students.")
	} else {
		fmt.Fprintf(stdout, "Deck version %s: phones download it the next time they open Words.\n", s.Version)
	}
	return nil
}
