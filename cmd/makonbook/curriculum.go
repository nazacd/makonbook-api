package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nazacd/makonbook-api/internal/platform/config"
	"github.com/nazacd/makonbook-api/internal/practice"
	"github.com/nazacd/makonbook-api/internal/roadmap"
)

const curriculumUsage = `
  curriculum import <file or folder>…
                            load SAT MAKON's curriculum: one JSON file per course (subject ×
                            level; a folder: its *.json files). Everything is checked first and
                            nothing is stored unless all of it passes; each changed course
                            becomes a new version, live at once (progress is kept by lesson id).
                            Then prints the coverage report
  curriculum coverage       how many approved questions each level check and homework set has,
                            per difficulty; SHORT marks a set the bank can't fill as asked
`

func validCurriculumArgs(args []string) bool {
	switch args[1] {
	case "import":
		return len(args) >= 3
	case "coverage":
		return len(args) == 2
	}
	return false
}

func runCurriculum(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	var courses []roadmap.Course
	if args[1] == "import" {
		files, err := courseFiles(args[2:])
		if err != nil {
			return err
		}
		for _, f := range files {
			data, err := os.ReadFile(f) //nolint:gosec // the operator's own files
			if err != nil {
				return err
			}
			c, err := roadmap.ParseCourse(data)
			if err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			courses = append(courses, c)
		}
	}
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc := roadmap.NewService(pool, practice.NewService(pool, nil), nil)
	var ids []string
	if args[1] == "import" {
		done, err := svc.Import(ctx, courses)
		if err != nil {
			return err
		}
		for _, d := range done {
			ids = append(ids, d.ID)
			if d.Changed {
				fmt.Fprintf(stdout, "%s: version %d, live now\n", d.ID, d.Version)
			} else {
				fmt.Fprintf(stdout, "%s: unchanged (version %d)\n", d.ID, d.Version)
			}
		}
		fmt.Fprintln(stdout)
	}
	report, err := svc.Coverage(ctx, ids...)
	if err != nil {
		return err
	}
	if len(report) == 0 {
		fmt.Fprintln(stdout, "No curriculum imported yet.")
	}
	for _, c := range report {
		printCoverage(stdout, c)
	}
	return nil
}

// courseFiles expands folders into their JSON files.
func courseFiles(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p) //nolint:gosec // the operator's own files
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		matches, err := filepath.Glob(filepath.Join(p, "*.json"))
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s: no .json files", p)
		}
		slices.Sort(matches)
		out = append(out, matches...)
	}
	return out, nil
}

func printCoverage(w io.Writer, c roadmap.Coverage) {
	fmt.Fprintf(w, "%s (version %d): %d sets, %d short\n", c.ID, c.Version, len(c.Sets), c.Short)
	for _, s := range c.Sets {
		var parts []string
		for _, n := range s.Need {
			parts = append(parts, fmt.Sprintf("%s %d/%d", n.Difficulty, n.Have, n.Want))
		}
		flag := ""
		if s.Short {
			flag = "  SHORT"
		}
		fmt.Fprintf(w, "  %-12s %-12s %s (any difficulty: %d)%s\n", s.LessonID, s.Set, strings.Join(parts, ", "), s.Any, flag)
	}
	if len(c.Unknown) > 0 {
		fmt.Fprintf(w, "  skills with no approved question: %s\n", strings.Join(c.Unknown, "; "))
	}
	fmt.Fprintln(w)
}
