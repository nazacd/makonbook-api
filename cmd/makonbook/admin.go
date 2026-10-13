package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/nazacd/makonbook-api/internal/admin"
	"github.com/nazacd/makonbook-api/internal/hollihop"
	"github.com/nazacd/makonbook-api/internal/platform/config"
)

const adminUsage = `
  admin find <name>         search HolliHop staff, to get the id for grant
  admin offices             list branches (ids and names)
  admin list                who can use the admin panel
  admin grant employee|teacher <id> --super
  admin grant employee|teacher <id> --offices NOVZA,MINOR
                            give panel access: all branches, or the ones listed (names or ids)
  admin revoke employee|teacher <id>
                            take it away (their sessions end at once)
`

func validAdminArgs(args []string) bool {
	switch {
	case len(args) == 2 && (args[1] == "offices" || args[1] == "list"):
		return true
	case len(args) >= 3 && args[1] == "find":
		return true
	case len(args) == 4 && args[1] == "revoke":
		return memberType(args[2]) != "" && isID(args[3])
	case len(args) == 5 && args[1] == "grant" && args[4] == "--super":
		return memberType(args[2]) != "" && isID(args[3])
	case len(args) == 6 && args[1] == "grant" && args[4] == "--offices":
		return memberType(args[2]) != "" && isID(args[3]) && args[5] != ""
	}
	return false
}

func memberType(s string) string {
	switch strings.ToLower(s) {
	case "employee":
		return admin.TypeEmployee
	case "teacher":
		return admin.TypeTeacher
	}
	return ""
}

func isID(s string) bool {
	n, err := strconv.ParseInt(s, 10, 32)
	return err == nil && n > 0
}

func runAdmin(ctx context.Context, cfg config.Config, args []string, stdout io.Writer) error {
	if args[1] == "find" {
		if cfg.HolliHopBaseURL == "" || cfg.HolliHopAuthKey == "" {
			return errors.New("HOLLIHOP_BASE_URL and HOLLIHOP_AUTHKEY are required")
		}
		found, err := hollihop.New(cfg.HolliHopBaseURL, cfg.HolliHopAuthKey).FindEmployees(ctx, strings.Join(args[2:], " "))
		if err != nil {
			return err
		}
		if len(found) == 0 {
			fmt.Fprintln(stdout, "No staff found. (Teachers aren't listed here: use their teacher id from HolliHop.)")
		}
		for _, e := range found {
			var offices []string
			for _, o := range e.Offices {
				offices = append(offices, o.Name)
			}
			fired := ""
			if e.Fired {
				fired = "  [fired]"
			}
			fmt.Fprintf(stdout, "employee %d  %s %s  %s  branches: %s%s\n", e.ID, e.FirstName, e.LastName, e.Position, strings.Join(offices, ", "), fired)
		}
		return nil
	}

	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	svc := admin.NewService(pool, nil)
	offices, err := svc.Offices(ctx)
	if err != nil {
		return err
	}
	officeName := map[int32]string{}
	for _, o := range offices {
		officeName[o.ID] = o.Name
	}

	switch args[1] {
	case "offices":
		for _, o := range offices {
			fmt.Fprintf(stdout, "%d  %s\n", o.ID, o.Name)
		}
		return nil
	case "list":
		list, err := svc.List(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Fprintln(stdout, "Nobody has panel access yet.")
		}
		for _, a := range list {
			scope := "ALL branches (super admin)"
			if !a.Super {
				var names []string
				for _, id := range a.OfficeIds {
					names = append(names, officeName[id])
				}
				scope = strings.Join(names, ", ")
			}
			last := "never signed in"
			if a.LastLoginAt.Valid {
				last = "last sign-in " + a.LastLoginAt.Time.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(stdout, "%s %d  %s  %s  (%s)\n", strings.ToLower(a.MemberType), a.MemberID, a.Name, scope, last)
		}
		return nil
	case "revoke":
		id, _ := strconv.ParseInt(args[3], 10, 32)
		if err := svc.Revoke(ctx, memberType(args[2]), int32(id)); errors.Is(err, admin.ErrNotFound) {
			return fmt.Errorf("%s %d has no panel access", args[2], id)
		} else if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Panel access of %s %d removed.\n", args[2], id)
		return nil
	}

	// grant
	id, _ := strconv.ParseInt(args[3], 10, 32)
	super := args[4] == "--super"
	var ids []int32
	if !super {
		for _, want := range strings.Split(args[5], ",") {
			want = strings.TrimSpace(want)
			found := false
			for _, o := range offices {
				if strings.EqualFold(o.Name, want) || strconv.Itoa(int(o.ID)) == want {
					ids, found = append(ids, o.ID), true
					break
				}
			}
			if !found {
				return fmt.Errorf("no branch %q (see: makonbook admin offices)", want)
			}
		}
	}
	a, err := svc.Grant(ctx, memberType(args[2]), int32(id), "", super, ids)
	if err != nil {
		return err
	}
	scope := "all branches (super admin)"
	if !super {
		var names []string
		for _, id := range a.OfficeIds {
			names = append(names, officeName[id])
		}
		scope = strings.Join(names, ", ")
	}
	fmt.Fprintf(stdout, "%s %d can now sign in to the admin panel with their HolliHop login: %s.\n", args[2], id, scope)
	return nil
}
