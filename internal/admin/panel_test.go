package admin

import (
	"slices"
	"testing"
)

func names(body map[string]any, list, field string) []string {
	var out []string
	items, _ := body[list].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)[field].(string))
	}
	slices.Sort(out)
	return out
}

// Branch admins see only their branches' lessons and data problems.
func TestPanelScope(t *testing.T) {
	e := setup(t)
	_, _, novza := e.login("novza", "pw-novza") // NOVZA, MINOR
	_, _, boss := e.login("boss", "pw-boss")

	_, body, _ := e.do("GET", "/v1/admin/lessons?date=2026-10-12", novza, nil, false)
	if got := names(body, "lessons", "group_name"); !slices.Equal(got, []string{"SAT ENGLISH"}) {
		t.Errorf("novza's lessons: %v", got)
	}
	_, body, _ = e.do("GET", "/v1/admin/lessons?date=2026-10-12", boss, nil, false)
	if got := names(body, "lessons", "group_name"); !slices.Equal(got, []string{"SAT ENGLISH", "SAT MATH"}) {
		t.Errorf("boss's lessons: %v", got)
	}
	// Asking for another branch's lessons by id yields nothing.
	_, body, _ = e.do("GET", "/v1/admin/lessons?date=2026-10-12&office_id=5", novza, nil, false)
	if got := names(body, "lessons", "group_name"); len(got) != 0 {
		t.Errorf("novza asking for CHILONZOR: %v", got)
	}
	_, body, _ = e.do("GET", "/v1/admin/lessons?date=2026-10-12&office_id=5", boss, nil, false)
	if got := names(body, "lessons", "group_name"); !slices.Equal(got, []string{"SAT MATH"}) {
		t.Errorf("boss asking for CHILONZOR: %v", got)
	}
	if status, _, _ := e.do("GET", "/v1/admin/lessons?date=12.10.2026", boss, nil, false); status != 400 {
		t.Errorf("bad date: %d", status)
	}

	_, body, _ = e.do("GET", "/v1/admin/data-quality", novza, nil, false)
	if got := names(body, "issues", "last_name"); !slices.Equal(got, []string{"Karimov", "Tursunov"}) {
		t.Errorf("novza's issues: %v", got)
	}
	_, body, _ = e.do("GET", "/v1/admin/data-quality", boss, nil, false)
	if got := names(body, "issues", "last_name"); !slices.Equal(got, []string{"Karimov", "Saidova", "Tursunov"}) {
		t.Errorf("boss's issues: %v", got)
	}

	if status, body, _ := e.do("GET", "/v1/admin/hollihop/writes", novza, nil, false); status != 200 || body["writing_enabled"] != false {
		t.Errorf("writes: %d %v", status, body)
	}
	if status, _, _ := e.do("GET", "/v1/admin/hollihop/writes?status=lost", novza, nil, false); status != 400 {
		t.Errorf("unknown write status: %d", status)
	}
	if status, _, _ := e.do("GET", "/v1/admin/reports/corrections?from=2026-10-12&to=2026-10-01", novza, nil, false); status != 400 {
		t.Errorf("from after to: %d", status)
	}
	if status, _, _ := e.do("GET", "/v1/admin/attendance/sessions/00000000-0000-0000-0000-000000000000", novza, nil, false); status != 404 {
		t.Errorf("unknown session: %d", status)
	}
	// Signed out: nothing.
	if status, _, _ := e.do("GET", "/v1/admin/data-quality", "", nil, false); status != 401 {
		t.Errorf("signed out: %d", status)
	}
}

func TestAccessManagement(t *testing.T) {
	e := setup(t)
	_, _, novza := e.login("novza", "pw-novza")
	_, _, boss := e.login("boss", "pw-boss")

	// Only super admins manage the list.
	for _, path := range []string{"/v1/admin/access", "/v1/admin/staff?q=Recep"} {
		if status, _, _ := e.do("GET", path, novza, nil, false); status != 403 {
			t.Errorf("branch admin GET %s: %d", path, status)
		}
	}
	if status, _, _ := e.do("PUT", "/v1/admin/access/employee/4", novza, map[string]any{"super": true}, true); status != 403 {
		t.Errorf("branch admin grants: %d", status)
	}

	_, body, _ := e.do("GET", "/v1/admin/staff?q=Recep", boss, nil, false)
	if staff, _ := body["staff"].([]any); len(staff) != 1 || staff[0].(map[string]any)["position"] != "Receptionist" {
		t.Errorf("staff search: %v", body)
	}

	// Grant: the name comes from HolliHop; branches are checked.
	status, body, _ := e.do("PUT", "/v1/admin/access/employee/4", boss, map[string]any{"office_ids": []int{4}}, true)
	if status != 200 || !slices.Contains(names(body, "access", "name"), "Recep R") {
		t.Errorf("grant: %d %v", status, body)
	}
	if status, _, _ := e.do("PUT", "/v1/admin/access/employee/4", boss, map[string]any{"office_ids": []int{99}}, true); status != 400 {
		t.Errorf("unknown branch: %d", status)
	}
	if status, body, _ := e.do("PUT", "/v1/admin/access/employee/3", boss, map[string]any{"super": true}, true); status != 422 || code(body) != "fired" {
		t.Errorf("fired: %d %v", status, body)
	}
	if status, _, _ := e.do("PUT", "/v1/admin/access/employee/55", boss, map[string]any{"super": true}, true); status != 404 {
		t.Errorf("unknown employee: %d", status)
	}
	if status, _, _ := e.do("PUT", "/v1/admin/access/teacher/55", boss, map[string]any{"super": true}, true); status != 404 {
		t.Errorf("unknown teacher: %d", status)
	}
	if status, _, _ := e.do("PUT", "/v1/admin/access/student/1", boss, map[string]any{"super": true}, true); status != 404 {
		t.Errorf("student: %d", status)
	}
	if status, _, _ := e.do("PUT", "/v1/admin/access/employee/4", boss, map[string]any{"super": true}, false); status != 403 {
		t.Errorf("grant without CSRF header: %d", status)
	}

	// The last super admin can't be demoted or removed…
	if status, body, _ := e.do("PUT", "/v1/admin/access/employee/1", boss, map[string]any{"office_ids": []int{3}}, true); status != 409 || code(body) != "last_super_admin" {
		t.Errorf("demote the last super admin: %d %v", status, body)
	}
	if status, body, _ := e.do("DELETE", "/v1/admin/access/employee/1", boss, nil, true); status != 409 || code(body) != "last_super_admin" {
		t.Errorf("remove the last super admin: %d %v", status, body)
	}
	// …once there's another, they can.
	if status, _, _ := e.do("PUT", "/v1/admin/access/employee/4", boss, map[string]any{"super": true}, true); status != 200 {
		t.Errorf("second super admin: %d", status)
	}
	if status, _, _ := e.do("DELETE", "/v1/admin/access/employee/1", boss, nil, true); status != 200 {
		t.Errorf("remove a super admin: %d", status)
	}
	if status, _, _ := e.do("GET", "/v1/admin/me", boss, nil, false); status != 401 {
		t.Errorf("removed admin still signed in: %d", status)
	}
	if status, _, _ := e.do("DELETE", "/v1/admin/access/employee/1", "", nil, true); status != 401 {
		t.Errorf("signed out: %d", status)
	}
}
