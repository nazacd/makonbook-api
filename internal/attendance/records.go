package attendance

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nazacd/makonbook-api/internal/attendance/attendancedb"
)

// How far back the sync mirrors HolliHop's attendance (spec 8.7): the
// nightly full run refreshes the whole table window, the 15-minute runs only
// the last week, where admins' corrections happen.
const (
	RecordsBackFull        = 120 // days
	RecordsBackIncremental = 7
)

// Record is HolliHop's mark for one student on one lesson day.
type Record struct {
	GroupID  int32
	ClientID int32
	Date     time.Time // midnight UTC, the calendar date
	Pass     bool      // true = absent
}

// ApplyRecords replaces the stored records between from and to (inclusive).
// It runs inside the sync's transaction.
func ApplyRecords(ctx context.Context, db attendancedb.DBTX, from, to time.Time, records []Record) error {
	q := attendancedb.New(db)
	if err := q.DeleteRecordsBetween(ctx, attendancedb.DeleteRecordsBetweenParams{DateFrom: date(from), DateTo: date(to)}); err != nil {
		return fmt.Errorf("delete attendance records: %w", err)
	}
	params := make([]attendancedb.InsertRecordParams, 0, len(records))
	for _, r := range records {
		if r.Date.Before(from) || r.Date.After(to) {
			continue
		}
		params = append(params, attendancedb.InsertRecordParams{GroupID: r.GroupID, ClientID: r.ClientID, Day: date(r.Date), Pass: r.Pass})
	}
	var err error
	q.InsertRecord(ctx, params).Exec(func(_ int, e error) {
		if e != nil && err == nil {
			err = e
		}
	})
	if err != nil {
		return fmt.Errorf("insert attendance records: %w", err)
	}
	return nil
}

// GroupAttendance is one group of the student's attendance table.
type GroupAttendance struct {
	GroupID    int32       `json:"group_id"`
	GroupName  string      `json:"group_name"`
	Discipline string      `json:"discipline"`
	Summary    Summary     `json:"summary"`
	Days       []RecordDay `json:"days"` // newest first
}

type Summary struct {
	Lessons int `json:"lessons"`
	Present int `json:"present"`
	Late    int `json:"late"`
	Absent  int `json:"absent"`
}

type RecordDay struct {
	Date  string `json:"date"`
	State string `json:"state"` // present, late, absent
}

// StudentTable is the student's attendance over the last RecordsBackFull
// days (spec 8.7): HolliHop's marks for past lessons — so admins'
// corrections count — with "late" and today's lessons from the app.
func (s *Service) StudentTable(ctx context.Context, clientID int32, today time.Time) ([]GroupAttendance, error) {
	q := attendancedb.New(s.pool)
	rows, err := q.StudentAttendance(ctx, attendancedb.StudentAttendanceParams{
		ClientID: clientID, DateFrom: date(today.AddDate(0, 0, -RecordsBackFull)), Today: date(today),
	})
	if err != nil {
		return nil, fmt.Errorf("student attendance: %w", err)
	}
	byGroup := map[int32]*GroupAttendance{}
	var ids []int32
	for _, r := range rows {
		g := byGroup[r.GroupID]
		if g == nil {
			g = &GroupAttendance{GroupID: r.GroupID, Days: []RecordDay{}}
			byGroup[r.GroupID] = g
			ids = append(ids, r.GroupID)
		}
		g.Days = append(g.Days, RecordDay{Date: r.Date.Time.Format(time.DateOnly), State: r.State})
		g.Summary.Lessons++
		switch r.State {
		case StatePresent:
			g.Summary.Present++
		case StateLate:
			g.Summary.Late++
		case StateAbsent:
			g.Summary.Absent++
		}
	}
	out := make([]GroupAttendance, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	names, err := q.GroupNames(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("group names: %w", err)
	}
	for _, n := range names {
		g := byGroup[n.ID]
		g.GroupName, g.Discipline = n.Name, n.Discipline
	}
	for _, id := range ids {
		out = append(out, *byGroup[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Discipline < out[j].Discipline })
	return out, nil
}
