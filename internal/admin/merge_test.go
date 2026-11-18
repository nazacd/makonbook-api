package admin

import (
	"strconv"
	"testing"

	"github.com/nazacd/makonbook-api/internal/people"
)

// A teacher whose number HolliHop gained only after they made a web account
// with it: a super admin merges the web account into them.
func TestMergeWebAccount(t *testing.T) {
	e := setup(t)
	ctx := t.Context()
	web, err := people.CreateSelfStudent(ctx, e.pool, "Bek", "Tursunov", "+998901000099")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO credentials (person_kind, person_id, password_hash, password_changed_at, email)
		VALUES ('student', $1, 'x', now(), 'bek@example.com')`, web); err != nil {
		t.Fatal(err)
	}
	// What the sync records once HolliHop has the number (teacher 11 had none).
	if _, err := e.pool.Exec(ctx, `UPDATE teachers SET phone_raw = '+998 90 100 00 99' WHERE id = 11`); err != nil {
		t.Fatal(err)
	}
	var issue int64
	if err := e.pool.QueryRow(ctx, `INSERT INTO data_quality_issues (person_kind, person_id, kind, detail)
		VALUES ('teacher', 11, 'self_account_conflict', 'mobile +998901000099 is the number of a self-registered account') RETURNING id`).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	path := "/v1/admin/data-quality/issues/" + strconv.FormatInt(issue, 10) + "/merge"

	_, _, novza := e.login("novza", "pw-novza")
	if status, _, _ := e.do("POST", path, novza, nil, true); status != 403 {
		t.Errorf("a branch admin merges: %d", status)
	}
	_, _, boss := e.login("boss", "pw-boss")
	if status, body, _ := e.do("POST", path, boss, nil, true); status != 204 {
		t.Fatalf("merge: %d %v", status, body)
	}
	var phone, email string
	if err := e.pool.QueryRow(ctx, `SELECT coalesce(t.login_phone, ''), coalesce(c.email, '') FROM teachers t
		LEFT JOIN credentials c ON c.person_kind = 'teacher' AND c.person_id = t.id WHERE t.id = 11`).Scan(&phone, &email); err != nil {
		t.Fatal(err)
	}
	if phone != "+998901000099" || email != "bek@example.com" {
		t.Errorf("the teacher after the merge: phone %q email %q", phone, email)
	}
	var left int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM students WHERE client_id = $1`, web).Scan(&left)
	if left != 0 {
		t.Error("the web account is still there")
	}
	_, body, _ := e.do("GET", "/v1/admin/data-quality", boss, nil, false)
	for _, x := range body["issues"].([]any) {
		if x.(map[string]any)["kind"] == "self_account_conflict" {
			t.Errorf("the issue is still open: %v", x)
		}
	}
	if status, _, _ := e.do("POST", path, boss, nil, true); status != 404 {
		t.Errorf("merge again: %d", status)
	}
}
