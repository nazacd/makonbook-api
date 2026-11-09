package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
                            Then loads the universities' logos from logos/ beside the files
                            (<slug>.svg, .png or .webp; never committed) and prints the
                            coverage report
  curriculum coverage       how many approved questions each level check and homework set has,
                            per difficulty; SHORT marks a set the bank can't fill as asked
  curriculum logos          every university of the roadmaps, the logo file name it takes
                            (<slug>.svg/.png/.webp) and whether it has one
`

func validCurriculumArgs(args []string) bool {
	switch args[1] {
	case "import":
		return len(args) >= 3
	case "coverage", "logos":
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
	if args[1] == "logos" {
		return printUniversities(ctx, svc, stdout)
	}
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
		logos, err := svc.ImportLogos(ctx, logoDirs(args[2:]), courses)
		if err != nil {
			return err
		}
		printLogos(stdout, logos)
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
		// ReadDir, not Glob: Glob hides a folder it can't read (the container
		// runs as distroless's nonroot user, uid 65532) as "no files".
		entries, err := os.ReadDir(p) //nolint:gosec // the operator's own folder
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return nil, fmt.Errorf("%s: can't read the folder (the import runs as uid 65532; chown -R 65532:65532 it): %w", p, err)
			}
			return nil, err
		}
		var matches []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				matches = append(matches, filepath.Join(p, e.Name()))
			}
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

// logoDirs are the logos/ folders beside the curriculum: in each folder
// given, and in the folder of each file given.
func logoDirs(paths []string) []string {
	var out []string
	for _, p := range paths {
		dir := p
		if info, err := os.Stat(p); err == nil && !info.IsDir() { //nolint:gosec // the operator's own files
			dir = filepath.Dir(p)
		}
		if d := filepath.Join(dir, "logos"); !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

func printLogos(w io.Writer, r roadmap.LogoReport) {
	fmt.Fprintf(w, "Logos: %d loaded, %d unchanged, %d skipped\n", len(r.Loaded), len(r.Unchanged), len(r.Skipped))
	for _, slug := range r.Loaded {
		fmt.Fprintf(w, "  loaded     %s\n", slug)
	}
	for _, sk := range r.Skipped {
		fmt.Fprintf(w, "  skipped    %s: %s\n", sk.File, sk.Reason)
	}
	if len(r.Missing) > 0 {
		fmt.Fprintf(w, "No logo yet (%d; file names: curriculum logos):\n", len(r.Missing))
		for _, n := range r.Missing {
			fmt.Fprintf(w, "  %s\n", n)
		}
	}
	fmt.Fprintln(w)
}

func printUniversities(ctx context.Context, svc *roadmap.Service, w io.Writer) error {
	us, err := svc.Universities(ctx)
	if err != nil {
		return err
	}
	if len(us) == 0 {
		fmt.Fprintln(w, "No curriculum imported yet.")
		return nil
	}
	have := 0
	for _, u := range us {
		mark := "no logo"
		if u.HasLogo {
			mark, have = "logo", have+1
		}
		fmt.Fprintf(w, "%-8s %s   (%s)\n", mark, u.Slug, u.Name)
	}
	fmt.Fprintf(w, "\n%d of %d have a logo. Files go in logos/ beside the curriculum, named <slug>.svg, .png or .webp.\n", have, len(us))
	return nil
}
