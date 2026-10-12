package sync

import (
	"fmt"
	"strings"
	"time"

	"github.com/nazacd/makonbook-api/internal/attendance"
	"github.com/nazacd/makonbook-api/internal/hollihop"
	"github.com/nazacd/makonbook-api/internal/lessons"
	"github.com/nazacd/makonbook-api/internal/people"
)

// fetched is the raw HolliHop data for one run.
type fetched struct {
	teachers    []hollihop.Teacher
	units       []hollihop.EdUnit
	students    []hollihop.Student
	enrollments []hollihop.EdUnitStudent
	lessonDays  []hollihop.EdUnitDays
	studentDays []hollihop.EdUnitStudentDays
}

// buildSnapshot converts HolliHop rows into a people.Snapshot. Rows with
// unparseable dates or times are skipped and described in skipped, so one
// malformed record can't stop the whole sync.
func buildSnapshot(f fetched, today time.Time) (s people.Snapshot, skipped []string) {
	s.Today = today

	offices := map[int32]people.Office{}
	addOffice := func(id int32, name, offset string) {
		if id == 0 {
			return
		}
		o := offices[id]
		o.ID, o.Name = id, name
		if offset != "" {
			o.UTCOffset = offset
		}
		offices[id] = o
	}

	for _, t := range f.teachers {
		s.Teachers = append(s.Teachers, people.Teacher{
			ID:        t.ID,
			FirstName: strings.TrimSpace(t.FirstName),
			LastName:  strings.TrimSpace(t.LastName),
			PhoneRaw:  strings.TrimSpace(t.Mobile),
			Fired:     t.Fired,
		})
	}

	for _, u := range f.units {
		addOffice(u.OfficeOrCompanyID, u.OfficeOrCompanyName, u.OfficeTimeZone)
		g := people.Group{
			ID:           u.ID,
			Name:         u.Name,
			Discipline:   u.Discipline,
			Level:        u.Level,
			LearningType: u.LearningType,
			OfficeID:     u.OfficeOrCompanyID,
		}
		for _, it := range u.ScheduleItems {
			item, err := scheduleItem(it)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("schedule item %d of group %d: %v", it.ID, u.ID, err))
				continue
			}
			g.Schedule = append(g.Schedule, item)
		}
		s.Groups = append(s.Groups, g)
	}

	for _, st := range f.students {
		var officeIDs []int32
		for _, o := range st.OfficesAndCompanies {
			addOffice(o.ID, o.Name, "")
			officeIDs = append(officeIDs, o.ID)
		}
		s.Students = append(s.Students, people.Student{
			ClientID:    st.ClientID,
			HHStudentID: st.ID,
			FirstName:   strings.TrimSpace(st.FirstName),
			LastName:    strings.TrimSpace(st.LastName),
			PhoneRaw:    strings.TrimSpace(st.Mobile),
			StatusID:    st.StatusID,
			OfficeIDs:   officeIDs,
		})
	}

	for _, e := range f.enrollments {
		begin, err1 := parseDate(e.BeginDate)
		end, err2 := parseDate(e.EndDate)
		if err1 != nil || err2 != nil {
			skipped = append(skipped, fmt.Sprintf("enrollment of client %d in group %d: bad dates %q–%q",
				e.StudentClientID, e.EdUnitID, e.BeginDate, e.EndDate))
			continue
		}
		s.Enrollments = append(s.Enrollments, people.Enrollment{
			GroupID:         e.EdUnitID,
			StudentClientID: e.StudentClientID,
			BeginDate:       begin,
			EndDate:         end,
			Weekdays:        e.Weekdays,
		})
	}

	for _, o := range offices {
		s.Offices = append(s.Offices, o)
	}
	return s, skipped
}

// buildLessonDays converts GetEdUnits' Days; a day with an unparseable date
// is skipped and described.
func buildLessonDays(units []hollihop.EdUnitDays) (days []lessons.Day, skipped []string) {
	for _, u := range units {
		for _, d := range u.Days {
			date, err := parseDate(d.Date)
			if err != nil || date.IsZero() {
				skipped = append(skipped, fmt.Sprintf("lesson day %q of group %d: bad date", d.Date, u.ID))
				continue
			}
			days = append(days, lessons.Day{
				GroupID:         u.ID,
				Date:            date,
				Minutes:         lessons.MinutesOf(d.Minutes),
				ScheduleItemIDs: d.ScheduleItemIDs,
			})
		}
	}
	return days, skipped
}

// buildRecords converts GetEdUnitStudents' Days; a day with an unparseable
// date is skipped and described.
func buildRecords(links []hollihop.EdUnitStudentDays) (records []attendance.Record, skipped []string) {
	for _, l := range links {
		for _, d := range l.Days {
			date, err := parseDate(d.Date)
			if err != nil || date.IsZero() {
				skipped = append(skipped, fmt.Sprintf("attendance day %q of client %d in group %d: bad date", d.Date, l.StudentClientID, l.EdUnitID))
				continue
			}
			records = append(records, attendance.Record{GroupID: l.EdUnitID, ClientID: l.StudentClientID, Date: date, Pass: d.Pass})
		}
	}
	return records, skipped
}

func scheduleItem(it hollihop.ScheduleItem) (people.ScheduleItem, error) {
	begin, err := parseDate(it.BeginDate)
	if err != nil {
		return people.ScheduleItem{}, err
	}
	end, err := parseDate(it.EndDate)
	if err != nil {
		return people.ScheduleItem{}, err
	}
	from, err := parseClock(it.BeginTime)
	if err != nil {
		return people.ScheduleItem{}, err
	}
	to, err := parseClock(it.EndTime)
	if err != nil {
		return people.ScheduleItem{}, err
	}
	return people.ScheduleItem{
		ID:         it.ID,
		BeginDate:  begin,
		EndDate:    end,
		BeginTime:  from,
		EndTime:    to,
		Weekdays:   it.Weekdays,
		Classroom:  it.ClassroomName,
		TeacherIDs: it.TeacherIDs,
	}, nil
}

// parseDate reads "2006-01-02" (ignoring any time part) as midnight UTC.
// "" means open-ended and returns the zero time.
func parseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if len(s) > 10 {
		s = s[:10]
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad date %q", s)
	}
	return t, nil
}

// parseClock reads "15:04" as a duration since midnight.
func parseClock(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("bad time %q", s)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// dateIn returns the calendar date of t in loc, as midnight UTC — the same
// representation parseDate uses, so the two compare directly.
func dateIn(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
