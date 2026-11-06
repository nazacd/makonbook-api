package people

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nazacd/makonbook-api/internal/people/peopledb"
)

// Result summarises what Apply changed. The sync uses the before/deactivated
// counts for its mass-change guard and stores the rest as run statistics.
type Result struct {
	Groups        int `json:"groups"`
	GroupsRemoved int `json:"groups_removed"`
	ScheduleItems int `json:"schedule_items"`
	Enrollments   int `json:"enrollments"`

	ActiveTeachersBefore int     `json:"active_teachers_before"`
	ActiveTeachers       int     `json:"active_teachers"`
	DeactivatedTeachers  []int32 `json:"deactivated_teachers"`
	ReactivatedTeachers  []int32 `json:"reactivated_teachers"`

	ActiveStudentsBefore int     `json:"active_students_before"`
	ActiveStudents       int     `json:"active_students"`
	StudentsReceived     int     `json:"students_received"`
	StudentsMissing      int     `json:"students_missing"`
	DeactivatedStudents  []int32 `json:"deactivated_students"`
	ReactivatedStudents  []int32 `json:"reactivated_students"`

	LoginPhones    int            `json:"login_phones"`
	Issues         map[string]int `json:"issues"` // open issues by kind
	IssuesResolved int            `json:"issues_resolved"`

	// Linked are self-registered accounts that became HolliHop students in
	// this run (spec 21). The caller moves what other features keep under
	// the old id without a foreign key (auth.MoveStudent).
	Linked []Link `json:"linked,omitempty"`
}

// Link is a self-registered student re-keyed to their HolliHop ClientId.
type Link struct {
	SelfID   int32 `json:"self_id"`
	ClientID int32 `json:"client_id"`
}

// Apply writes a HolliHop snapshot inside tx. It must run in a transaction:
// schedule items and enrollments are deleted and re-inserted, and readers
// must never see the gap.
//
// Ownership rules (spec 6.2): only HolliHop-sourced columns are written;
// people who leave are deactivated, never deleted.
func Apply(ctx context.Context, tx pgx.Tx, s Snapshot) (Result, error) {
	q := peopledb.New(tx)
	var res Result

	before, err := q.CountActiveTeachers(ctx)
	if err != nil {
		return res, fmt.Errorf("count active teachers: %w", err)
	}
	res.ActiveTeachersBefore = int(before)
	before, err = q.CountActiveStudents(ctx)
	if err != nil {
		return res, fmt.Errorf("count active students: %w", err)
	}
	res.ActiveStudentsBefore = int(before)

	if err := applyGroups(ctx, q, s, &res); err != nil {
		return res, err
	}
	if err := applyTeachers(ctx, q, s, &res); err != nil {
		return res, err
	}
	studentIDs, fresh, err := applyStudents(ctx, q, s, &res)
	if err != nil {
		return res, err
	}
	plan, err := planLoginPhones(ctx, q)
	if err != nil {
		return res, err
	}
	if err := linkSelfAccounts(ctx, q, plan, fresh, &res); err != nil {
		return res, err
	}
	if err := applyEnrollments(ctx, q, s, studentIDs, &res); err != nil {
		return res, err
	}
	if err := applyLoginPhones(ctx, q, plan, &res); err != nil {
		return res, err
	}
	return res, nil
}

func applyGroups(ctx context.Context, q *peopledb.Queries, s Snapshot, res *Result) error {
	offices := make([]peopledb.UpsertOfficesParams, 0, len(s.Offices))
	for _, o := range s.Offices {
		offices = append(offices, peopledb.UpsertOfficesParams{ID: o.ID, Name: o.Name, UtcOffset: o.UTCOffset})
	}
	if err := execBatch(len(offices), func() batchResults { return q.UpsertOffices(ctx, offices) }); err != nil {
		return fmt.Errorf("upsert offices: %w", err)
	}

	groups := make([]peopledb.UpsertGroupsParams, 0, len(s.Groups))
	ids := make([]int32, 0, len(s.Groups))
	var items []peopledb.InsertScheduleItemsParams
	for _, g := range s.Groups {
		groups = append(groups, peopledb.UpsertGroupsParams{
			ID:           g.ID,
			Name:         g.Name,
			Discipline:   g.Discipline,
			Level:        g.Level,
			LearningType: g.LearningType,
			OfficeID:     pgtype.Int4{Int32: g.OfficeID, Valid: g.OfficeID != 0},
		})
		ids = append(ids, g.ID)
		for _, it := range g.Schedule {
			items = append(items, peopledb.InsertScheduleItemsParams{
				ID:         it.ID,
				GroupID:    g.ID,
				BeginDate:  date(it.BeginDate),
				EndDate:    date(it.EndDate),
				BeginTime:  timeOfDay(it.BeginTime),
				EndTime:    timeOfDay(it.EndTime),
				Weekdays:   it.Weekdays,
				Classroom:  it.Classroom,
				TeacherIds: nonNil(it.TeacherIDs),
			})
		}
	}
	if err := execBatch(len(groups), func() batchResults { return q.UpsertGroups(ctx, groups) }); err != nil {
		return fmt.Errorf("upsert groups: %w", err)
	}
	removed, err := q.MarkGroupsRemoved(ctx, ids)
	if err != nil {
		return fmt.Errorf("mark removed groups: %w", err)
	}

	if err := q.DeleteScheduleItems(ctx); err != nil {
		return fmt.Errorf("delete schedule items: %w", err)
	}
	if _, err := q.InsertScheduleItems(ctx, items); err != nil {
		return fmt.Errorf("insert schedule items: %w", err)
	}

	res.Groups, res.GroupsRemoved, res.ScheduleItems = len(groups), int(removed), len(items)
	return nil
}

func applyTeachers(ctx context.Context, q *peopledb.Queries, s Snapshot, res *Result) error {
	active := ActiveTeacherIDs(s)

	existing, err := q.ListTeacherIDs(ctx)
	if err != nil {
		return fmt.Errorf("list teachers: %w", err)
	}
	known := toSet(existing)

	// Store active teachers and keep stored ones up to date; never store
	// someone who has never been active (data minimisation).
	var rows []peopledb.UpsertTeachersParams
	for _, t := range s.Teachers {
		if active[t.ID] || known[t.ID] {
			rows = append(rows, peopledb.UpsertTeachersParams{
				ID: t.ID, FirstName: t.FirstName, LastName: t.LastName, PhoneRaw: t.PhoneRaw, Fired: t.Fired,
			})
		}
	}
	if err := execBatch(len(rows), func() batchResults { return q.UpsertTeachers(ctx, rows) }); err != nil {
		return fmt.Errorf("upsert teachers: %w", err)
	}

	activeIDs := keys(active)
	if res.DeactivatedTeachers, err = q.DeactivateTeachersExcept(ctx, activeIDs); err != nil {
		return fmt.Errorf("deactivate teachers: %w", err)
	}
	if res.ReactivatedTeachers, err = q.ReactivateTeachers(ctx, activeIDs); err != nil {
		return fmt.Errorf("reactivate teachers: %w", err)
	}
	n, err := q.CountActiveTeachers(ctx)
	res.ActiveTeachers = int(n)
	return err
}

// applyStudents returns the client ids of every stored student afterwards,
// and those stored for the first time in this run (fresh).
func applyStudents(ctx context.Context, q *peopledb.Queries, s Snapshot, res *Result) (stored, fresh map[int32]bool, err error) {
	existing, err := q.ListStudentClientIDs(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list students: %w", err)
	}
	stored = toSet(existing)
	fresh = map[int32]bool{}

	var rows []peopledb.UpsertStudentsParams
	present := make([]int32, 0, len(s.Students))
	for _, st := range s.Students {
		present = append(present, st.ClientID)
		if st.StatusID != StatusStudying && !stored[st.ClientID] {
			continue // never stored, not studying: nothing to do
		}
		rows = append(rows, peopledb.UpsertStudentsParams{
			ClientID:    st.ClientID,
			HhStudentID: st.HHStudentID,
			FirstName:   st.FirstName,
			LastName:    st.LastName,
			PhoneRaw:    st.PhoneRaw,
			HhStatusID:  pgtype.Int4{Int32: st.StatusID, Valid: st.StatusID != 0},
			OfficeIds:   nonNil(st.OfficeIDs),
		})
		if !stored[st.ClientID] {
			fresh[st.ClientID] = true
		}
		stored[st.ClientID] = true
	}
	if err := execBatch(len(rows), func() batchResults { return q.UpsertStudents(ctx, rows) }); err != nil {
		return nil, nil, fmt.Errorf("upsert students: %w", err)
	}
	res.StudentsReceived = len(s.Students)

	if s.StudentsComplete {
		missing, err := q.MarkStudentsMissing(ctx, present)
		if err != nil {
			return nil, nil, fmt.Errorf("mark missing students: %w", err)
		}
		res.StudentsMissing = int(missing)
	}

	if res.DeactivatedStudents, err = q.DeactivateStudents(ctx, StatusStudying); err != nil {
		return nil, nil, fmt.Errorf("deactivate students: %w", err)
	}
	if res.ReactivatedStudents, err = q.ReactivateStudents(ctx, StatusStudying); err != nil {
		return nil, nil, fmt.Errorf("reactivate students: %w", err)
	}
	n, err := q.CountActiveStudents(ctx)
	res.ActiveStudents = int(n)
	return stored, fresh, err
}

// loginPlan is who may log in with which number, and the issues of those
// who may not (PlanLoginPhones over the active people after this run).
type loginPlan struct {
	login  map[PersonKey]string
	issues []Issue
}

func planLoginPhones(ctx context.Context, q *peopledb.Queries) (loginPlan, error) {
	rows, err := q.ListActivePhones(ctx)
	if err != nil {
		return loginPlan{}, fmt.Errorf("list phones: %w", err)
	}
	people := make([]PersonPhone, 0, len(rows))
	for _, r := range rows {
		people = append(people, PersonPhone{Kind: r.PersonKind, ID: r.PersonID, PhoneRaw: r.PhoneRaw, Name: r.LastName + " " + r.FirstName})
	}
	login, issues := PlanLoginPhones(people)
	return loginPlan{login: login, issues: issues}, nil
}

// linkSelfAccounts handles enrolment (spec 21): a student stored for the first
// time in this run (fresh) whose login phone is a self-registered account's
// is that person. Instead of keeping a second account, the self-registered row
// takes the HolliHop ClientId and data (foreign keys cascade), keeping its
// password, username, sessions and name overrides. Only fresh students are
// linked: one already stored has a history of their own, and merging two
// accounts is left to people (applyLoginPhones reports it).
func linkSelfAccounts(ctx context.Context, q *peopledb.Queries, plan loginPlan, fresh map[int32]bool, res *Result) error {
	if len(fresh) == 0 {
		return nil
	}
	self, err := q.SelfStudentPhones(ctx)
	if err != nil {
		return fmt.Errorf("self-registered phones: %w", err)
	}
	if len(self) == 0 {
		return nil
	}
	byPhone := make(map[string]int32, len(self))
	for _, r := range self {
		byPhone[r.LoginPhone] = r.ClientID
	}
	var links []Link
	for k, phone := range plan.login {
		if selfID, ok := byPhone[phone]; ok && k.Kind == KindStudent && fresh[k.ID] {
			links = append(links, Link{SelfID: selfID, ClientID: k.ID})
		}
	}
	sort.Slice(links, func(i, j int) bool { return links[i].ClientID < links[j].ClientID })
	for _, l := range links {
		hh, err := q.DeleteNewStudent(ctx, l.ClientID)
		if err != nil {
			return fmt.Errorf("link %d: remove the new row: %w", l.ClientID, err)
		}
		n, err := q.RekeySelfStudent(ctx, peopledb.RekeySelfStudentParams{
			ClientID: l.ClientID, HhStudentID: hh.HhStudentID, FirstName: hh.FirstName, LastName: hh.LastName,
			PhoneRaw: hh.PhoneRaw, HhStatusID: hh.HhStatusID, OfficeIds: hh.OfficeIds, SelfID: l.SelfID,
		})
		if err != nil {
			return fmt.Errorf("link %d to %d: %w", l.SelfID, l.ClientID, err)
		}
		if n != 1 {
			return fmt.Errorf("link %d to %d: self-registered row is gone", l.SelfID, l.ClientID)
		}
	}
	res.Linked = links
	return nil
}

// applyEnrollments replaces all enrollments with the current and future ones
// of stored students. HolliHop returns the whole history, and leaves
// enrollments of departed students open, so filtering here matters (spec 4.4).
func applyEnrollments(ctx context.Context, q *peopledb.Queries, s Snapshot, students map[int32]bool, res *Result) error {
	groups := make(map[int32]bool, len(s.Groups))
	for _, g := range s.Groups {
		groups[g.ID] = true
	}

	var rows []peopledb.InsertEnrollmentsParams
	for _, e := range s.Enrollments {
		if !e.EndDate.IsZero() && e.EndDate.Before(s.Today) {
			continue
		}
		if !students[e.StudentClientID] || !groups[e.GroupID] {
			continue
		}
		rows = append(rows, peopledb.InsertEnrollmentsParams{
			GroupID:         e.GroupID,
			StudentClientID: e.StudentClientID,
			BeginDate:       date(e.BeginDate),
			EndDate:         date(e.EndDate),
			Weekdays:        e.Weekdays,
		})
	}

	if err := q.DeleteEnrollments(ctx); err != nil {
		return fmt.Errorf("delete enrollments: %w", err)
	}
	if _, err := q.InsertEnrollments(ctx, rows); err != nil {
		return fmt.Errorf("insert enrollments: %w", err)
	}
	res.Enrollments = len(rows)
	return nil
}

// applyLoginPhones sets the plan's login phones and records its issues. A
// number that is still a self-registered account's (after linkSelfAccounts)
// stays with that account: the HolliHop person gets an issue instead, since
// one number can't sign in two accounts.
func applyLoginPhones(ctx context.Context, q *peopledb.Queries, plan loginPlan, res *Result) error {
	login, issues := plan.login, plan.issues
	self, err := q.SelfStudentPhones(ctx)
	if err != nil {
		return fmt.Errorf("self-registered phones: %w", err)
	}
	if len(self) > 0 {
		byPhone := make(map[string]peopledb.SelfStudentPhonesRow, len(self))
		for _, r := range self {
			byPhone[r.LoginPhone] = r
		}
		for k, phone := range login {
			r, ok := byPhone[phone]
			if !ok {
				continue
			}
			delete(login, k)
			issues = append(issues, Issue{Person: k, Kind: IssueSelfAccount, Detail: fmt.Sprintf(
				"mobile %s is the number of a self-registered account (%s %s, id %d) that MakonBook doesn't merge with an existing person; "+
					"if it is the same person, the accounts must be merged by hand, otherwise correct the number in HolliHop",
				phone, r.LastName, r.FirstName, r.ClientID)})
		}
		sortIssues(issues)
	}

	var studentIDs, teacherIDs []int32
	var studentPhones, teacherPhones []string
	for k, phone := range login {
		if k.Kind == KindStudent {
			studentIDs, studentPhones = append(studentIDs, k.ID), append(studentPhones, phone)
		} else {
			teacherIDs, teacherPhones = append(teacherIDs, k.ID), append(teacherPhones, phone)
		}
	}
	// Clear both tables before setting either, so a number moving between a
	// student and a teacher never exists twice.
	sp := peopledb.ClearStudentLoginPhonesParams{Ids: nonNil(studentIDs), Phones: nonNil(studentPhones)}
	tp := peopledb.ClearTeacherLoginPhonesParams{Ids: nonNil(teacherIDs), Phones: nonNil(teacherPhones)}
	if err := q.ClearStudentLoginPhones(ctx, sp); err != nil {
		return fmt.Errorf("clear student login phones: %w", err)
	}
	if err := q.ClearTeacherLoginPhones(ctx, tp); err != nil {
		return fmt.Errorf("clear teacher login phones: %w", err)
	}
	if err := q.SetStudentLoginPhones(ctx, peopledb.SetStudentLoginPhonesParams(sp)); err != nil {
		return fmt.Errorf("set student login phones: %w", err)
	}
	if err := q.SetTeacherLoginPhones(ctx, peopledb.SetTeacherLoginPhonesParams(tp)); err != nil {
		return fmt.Errorf("set teacher login phones: %w", err)
	}
	res.LoginPhones = len(login)

	upserts := make([]peopledb.UpsertIssuesParams, 0, len(issues))
	resolve := peopledb.ResolveIssuesExceptParams{PersonKinds: []string{}, PersonIds: []int32{}, Kinds: []string{}}
	res.Issues = map[string]int{}
	for _, is := range issues {
		upserts = append(upserts, peopledb.UpsertIssuesParams{
			PersonKind: is.Person.Kind, PersonID: is.Person.ID, Kind: is.Kind, Detail: is.Detail,
		})
		resolve.PersonKinds = append(resolve.PersonKinds, is.Person.Kind)
		resolve.PersonIds = append(resolve.PersonIds, is.Person.ID)
		resolve.Kinds = append(resolve.Kinds, is.Kind)
		res.Issues[is.Kind]++
	}
	resolved, err := q.ResolveIssuesExcept(ctx, resolve)
	if err != nil {
		return fmt.Errorf("resolve issues: %w", err)
	}
	res.IssuesResolved = int(resolved)
	if err := execBatch(len(upserts), func() batchResults { return q.UpsertIssues(ctx, upserts) }); err != nil {
		return fmt.Errorf("upsert issues: %w", err)
	}
	return nil
}

type batchResults interface {
	Exec(func(int, error))
	Close() error
}

// execBatch sends a batch (unless empty) and returns its first error.
func execBatch(n int, send func() batchResults) error {
	if n == 0 {
		return nil
	}
	br := send()
	var first error
	br.Exec(func(_ int, err error) {
		if err != nil && first == nil {
			first = err
		}
	})
	if err := br.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

func date(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t, Valid: !t.IsZero()}
}

func timeOfDay(d time.Duration) pgtype.Time {
	return pgtype.Time{Microseconds: d.Microseconds(), Valid: true}
}

func toSet(ids []int32) map[int32]bool {
	m := make(map[int32]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func keys(m map[int32]bool) []int32 {
	out := make([]int32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// nonNil turns a nil slice into an empty one: pgx sends nil as SQL NULL,
// and "x = ANY(NULL)" or unnest(NULL) silently match nothing.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
