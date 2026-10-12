package hollihop

// Only the fields MakonBook uses are decoded; everything else HolliHop sends
// (emails, birthdays, parents, notes…) is ignored on purpose (spec 16, data
// minimisation).
//
// Dates come as "2006-01-02" and times as "15:04"; absent dates mean
// "open-ended" and are decoded as "". Ids are int32 to match the database;
// a value that doesn't fit fails decoding instead of wrapping silently.

// Teacher is one row of GetTeachers.
type Teacher struct {
	ID        int32  `json:"Id"`
	FirstName string `json:"FirstName"`
	LastName  string `json:"LastName"`
	Mobile    string `json:"Mobile"`
	Fired     bool   `json:"Fired"`
}

// EdUnit is one row of GetEdUnits: a group, or a one-to-one "group".
type EdUnit struct {
	ID                  int32          `json:"Id"`
	Name                string         `json:"Name"`
	Type                string         `json:"Type"`
	Discipline          string         `json:"Discipline"`
	Level               string         `json:"Level"`
	LearningType        string         `json:"LearningType"`
	OfficeOrCompanyID   int32          `json:"OfficeOrCompanyId"`
	OfficeOrCompanyName string         `json:"OfficeOrCompanyName"`
	OfficeTimeZone      string         `json:"OfficeTimeZone"` // e.g. "+05:00"
	ScheduleItems       []ScheduleItem `json:"ScheduleItems"`
}

// ScheduleItem is a recurring lesson slot of an EdUnit. Teachers are attached
// here, not to the EdUnit.
type ScheduleItem struct {
	ID            int32   `json:"Id"`
	BeginDate     string  `json:"BeginDate"`
	EndDate       string  `json:"EndDate"`
	BeginTime     string  `json:"BeginTime"`
	EndTime       string  `json:"EndTime"`
	Weekdays      int16   `json:"Weekdays"` // bit set: Mon=1, Tue=2, … Sun=64
	ClassroomName string  `json:"ClassroomName"`
	TeacherIDs    []int32 `json:"TeacherIds"`
}

// Student is one row of GetStudents. ClientID, not ID, is the key other
// methods (enrollments, attendance) refer to.
type Student struct {
	ID                  int32    `json:"Id"`
	ClientID            int32    `json:"ClientId"`
	FirstName           string   `json:"FirstName"`
	LastName            string   `json:"LastName"`
	Mobile              string   `json:"Mobile"`
	StatusID            int32    `json:"StatusId"` // 0 when HolliHop sends none
	OfficesAndCompanies []Office `json:"OfficesAndCompanies"`
}

type Office struct {
	ID   int32  `json:"Id"`
	Name string `json:"Name"`
}

// EdUnitStudent is one row of GetEdUnitStudents: a student's membership of an
// EdUnit over a date range.
type EdUnitStudent struct {
	EdUnitID        int32  `json:"EdUnitId"`
	StudentClientID int32  `json:"StudentClientId"`
	BeginDate       string `json:"BeginDate"`
	EndDate         string `json:"EndDate"`
	Weekdays        int16  `json:"Weekdays"`
	Status          string `json:"Status"`
}

// EdUnitDays is one row of GetEdUnits with queryDays: a group's lesson days in
// the requested range.
type EdUnitDays struct {
	ID   int32       `json:"Id"`
	Days []LessonDay `json:"Days"`
}

// LessonDay is one lesson of an EdUnit. Its times and teachers come from the
// schedule items it names.
type LessonDay struct {
	Date            string  `json:"Date"`
	Minutes         float64 `json:"Minutes"`
	ScheduleItemIDs []int32 `json:"ScheduleItemIds"`
}
