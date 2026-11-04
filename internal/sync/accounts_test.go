package sync

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nazacd/makonbook-api/internal/auth"
	"github.com/nazacd/makonbook-api/internal/hollihop"
	"github.com/nazacd/makonbook-api/internal/people"
	"github.com/nazacd/makonbook-api/internal/platform/testdb"
)

// selfAccount stores a self-registered student (spec 21) with a password and
// some of their own data, and signs them in. It returns their id and token.
func selfAccount(t *testing.T, pool *pgxpool.Pool, svc *auth.Service, first, last, phone, password string) (int32, string) {
	t.Helper()
	ctx := t.Context()
	id, err := people.CreateSelfStudent(ctx, pool, first, last, phone)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.NewHasher().Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO credentials (person_kind, person_id, username, password_hash, password_changed_at)
		 VALUES ('student', $1, 'self' || -$1::int, $2, now())`,
		`INSERT INTO student_goals (student_client_id, target_math) VALUES ($1, 700)`,
		`INSERT INTO parked_questions (id, student_client_id, test, version, subject, module, position, question_id)
		 VALUES (gen_random_uuid(), $1, 'DAY1', 1, 'math', 1, 3, gen_random_uuid())`,
	} {
		args := []any{id}
		if strings.Contains(sql, "$2") {
			args = append(args, hash)
		}
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	res, err := svc.Login(ctx, phone, password, "web")
	if err != nil {
		t.Fatalf("self-registered login: %v", err)
	}
	return id, res.Token
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestEnrolmentLinksTheSelfRegisteredAccount(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	src := newFixture(3)
	r := newRunner(pool, src, &fakeNotifier{})
	if _, err := r.Run(ctx, ModeFull, Options{}); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(pool, people.NewDirectory(pool), auth.NewHasher())

	// Someone signs up on the web, chooses a name of their own, then enrols.
	const phone = "+998935550001"
	self, token := selfAccount(t, pool, svc, "Dilnoza", "Web", phone, "a strong one")
	if self != -1000001 {
		t.Errorf("first self-registered id = %d", self)
	}
	first := "Dili"
	if err := people.NewDirectory(pool).SetNames(ctx, people.KindStudent, self, &first, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO google_links (sub, person_kind, person_id, email)
		VALUES ('g-77', 'student', $1, 'dili@gmail.com')`, self); err != nil {
		t.Fatal(err)
	}
	src.students = append(src.students, hollihop.Student{ID: 900, ClientID: 77, FirstName: "Dilnoza", LastName: "Karimova",
		Mobile: "93 555 00 01", StatusID: people.StatusStudying, OfficesAndCompanies: []hollihop.Office{{ID: 3, Name: "NOVZA"}}})
	src.enrollments = append(src.enrollments, hollihop.EdUnitStudent{EdUnitID: 100, StudentClientID: 77, Weekdays: 21, Status: "Normal"})

	rep, err := r.Run(ctx, ModeFull, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.People.Linked) != 1 || rep.People.Linked[0] != (people.Link{SelfID: self, ClientID: 77}) {
		t.Fatalf("linked = %+v", rep.People.Linked)
	}
	if n := count(t, pool, "SELECT count(*) FROM students WHERE client_id = $1", self); n != 0 {
		t.Error("the self-registered row is still there")
	}
	var source, firstName, lastName, custom, login string
	var hhStudent int32
	if err := pool.QueryRow(ctx, `SELECT source, first_name, last_name, coalesce(first_name_custom, ''), coalesce(login_phone, ''), hh_student_id
		FROM students WHERE client_id = 77`).Scan(&source, &firstName, &lastName, &custom, &login, &hhStudent); err != nil {
		t.Fatal(err)
	}
	if source != people.SourceHolliHop || firstName != "Dilnoza" || lastName != "Karimova" || custom != "Dili" || login != phone || hhStudent != 900 {
		t.Errorf("linked row: %s %s %s custom=%q phone=%s hh=%d", source, firstName, lastName, custom, login, hhStudent)
	}
	// What they did and how they sign in came along.
	for _, sql := range []string{
		"SELECT count(*) FROM student_goals WHERE student_client_id = 77",
		"SELECT count(*) FROM parked_questions WHERE student_client_id = 77",
		"SELECT count(*) FROM credentials WHERE person_kind = 'student' AND person_id = 77 AND username = 'self1000001'",
		"SELECT count(*) FROM google_links WHERE person_kind = 'student' AND person_id = 77",
		"SELECT count(*) FROM enrollments WHERE student_client_id = 77",
	} {
		if n := count(t, pool, sql); n != 1 {
			t.Errorf("%s = %d", sql, n)
		}
	}
	p, err := svc.Authenticate(ctx, token)
	if err != nil || p.ID != 77 || p.MustChangePassword || p.Registered {
		t.Fatalf("session after enrolment: %+v %v", p, err)
	}
	prof, err := svc.Profile(ctx, p, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	if err != nil || prof.FirstName != "Dili" || prof.LastName != "Karimova" || len(prof.Groups) != 1 {
		t.Errorf("profile after enrolment: %+v %v", prof, err)
	}
	if _, err := svc.Login(ctx, "935550001", "a strong one", "phone"); err != nil {
		t.Errorf("password after enrolment: %v", err)
	}

	// The next run changes nothing.
	again, err := r.Run(ctx, ModeFull, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.People.Linked) != 0 || again.People.Issues[people.IssueSelfAccount] != 0 {
		t.Errorf("second run: %+v", again.People)
	}
	if p, err := svc.Authenticate(ctx, token); err != nil || p.ID != 77 {
		t.Errorf("session after a second run: %+v %v", p, err)
	}
	if n := count(t, pool, "SELECT count(*) FROM students WHERE client_id = 77 AND first_name_custom = 'Dili'"); n != 1 {
		t.Error("the second run lost the name override")
	}
}

func TestStoredPersonWithASelfRegisteredNumberIsAConflict(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	src := newFixture(3)
	r := newRunner(pool, src, &fakeNotifier{})
	if _, err := r.Run(ctx, ModeFull, Options{}); err != nil {
		t.Fatal(err)
	}
	svc := auth.NewService(pool, people.NewDirectory(pool), auth.NewHasher())
	self, token := selfAccount(t, pool, svc, "Sardor", "Web", "+998935550002", "a strong one")
	teacherSelf, _ := selfAccount(t, pool, svc, "Teacher", "Web", "+998935550003", "a strong one")

	// Admins give student 2 (already stored) and the teacher those numbers.
	src.students[1].Mobile = "+998935550002"
	src.teachers[0].Mobile = "+998935550003"
	rep, err := r.Run(ctx, ModeFull, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.People.Linked) != 0 || rep.People.Issues[people.IssueSelfAccount] != 2 {
		t.Fatalf("people = %+v", rep.People)
	}
	if n := count(t, pool, "SELECT count(*) FROM students WHERE client_id IN ($1, $2) AND source = 'self'", self, teacherSelf); n != 2 {
		t.Errorf("self-registered rows = %d, want both kept", n)
	}
	if n := count(t, pool, "SELECT count(*) FROM students WHERE client_id = 2 AND login_phone IS NULL"); n != 1 {
		t.Error("student 2 got the self-registered account's number")
	}
	if n := count(t, pool, "SELECT count(*) FROM teachers WHERE id = 10 AND login_phone IS NULL"); n != 1 {
		t.Error("the teacher got the self-registered account's number")
	}
	var detail string
	if err := pool.QueryRow(ctx, `SELECT detail FROM data_quality_issues
		WHERE person_kind = 'student' AND person_id = 2 AND kind = 'self_account_conflict' AND resolved_at IS NULL`).Scan(&detail); err != nil {
		t.Fatalf("no open issue for student 2: %v", err)
	}
	if !strings.Contains(detail, fmt.Sprint(self)) {
		t.Errorf("detail = %q", detail)
	}
	if p, err := svc.Authenticate(ctx, token); err != nil || p.ID != self {
		t.Errorf("self-registered session: %+v %v", p, err)
	}
	if _, err := svc.Login(ctx, "+998935550002", "a strong one", "phone"); err != nil {
		t.Errorf("the self-registered account can't sign in: %v", err)
	}

	// Admins correct the number: the issue goes, student 2 signs in.
	src.students[1].Mobile = "+998902000002"
	rep, err = r.Run(ctx, ModeFull, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.People.Issues[people.IssueSelfAccount] != 1 {
		t.Errorf("issues after the fix = %v", rep.People.Issues)
	}
	if n := count(t, pool, "SELECT count(*) FROM students WHERE client_id = 2 AND login_phone = '+998902000002'"); n != 1 {
		t.Error("student 2 has no login phone after the fix")
	}
}

func TestGuardIgnoresSelfRegisteredStudents(t *testing.T) {
	pool := testdb.New(t)
	ctx := t.Context()
	src := newFixture(20)
	r := newRunner(pool, src, &fakeNotifier{})
	if _, err := r.Run(ctx, ModeFull, Options{}); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if _, err := people.CreateSelfStudent(ctx, pool, "Web", "Student", fmt.Sprintf("+99893%07d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// 5 of the 20 HolliHop students leave: 25 %, blocked. Counting the 200
	// self-registered students it would be 2 %.
	for i := range src.students[:5] {
		src.students[i].StatusID = 3
	}
	rep, err := r.Run(ctx, ModeFull, Options{})
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if rep.People.ActiveStudentsBefore != 20 {
		t.Errorf("active before = %d, want 20", rep.People.ActiveStudentsBefore)
	}
	if n := count(t, pool, "SELECT count(*) FROM students WHERE source = 'self' AND deactivated_at IS NULL"); n != 200 {
		t.Errorf("self-registered active = %d", n)
	}
}
