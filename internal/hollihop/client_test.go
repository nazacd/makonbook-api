package hollihop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "SECRET-AUTH-KEY"

func TestPagingStopsOnShortPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/Api/V2/GetTeachers" || r.URL.Query().Get("authkey") != testKey {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		take, _ := strconv.Atoi(r.URL.Query().Get("take"))
		var rows []Teacher
		for i := skip; i < min(skip+take, 5); i++ { // 5 teachers in total
			rows = append(rows, Teacher{ID: int32(i + 1)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Teachers": rows})
	}))
	defer srv.Close()

	c := New(srv.URL+"/Api/V2/", testKey, WithPageSize(2))
	got, err := c.GetTeachers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[4].ID != 5 {
		t.Errorf("got %d teachers: %+v", len(got), got)
	}
	if calls.Load() != 3 { // pages of 2, 2, 1
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestGetStudentsReturnsFirstPageNowAndPassesCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("lastUpdatedFrom"); got != "2026-10-01T00:00:00" {
			t.Errorf("lastUpdatedFrom = %q", got)
		}
		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		rows := []Student{{ClientID: int32(skip + 1)}}
		if skip == 0 {
			rows = append(rows, Student{ClientID: 2})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Students": rows,
			"Now":      fmt.Sprintf("2026-10-10T12:00:0%d", skip/2),
		})
	}))
	defer srv.Close()

	c := New(srv.URL, testKey, WithPageSize(2))
	students, now, err := c.GetStudents(t.Context(), "2026-10-01T00:00:00")
	if err != nil {
		t.Fatal(err)
	}
	if len(students) != 3 || now != "2026-10-10T12:00:00" {
		t.Errorf("students=%d now=%q", len(students), now)
	}
}

func TestRetriesTransientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			http.Error(w, "oops", http.StatusBadGateway)
		case 2:
			_, _ = w.Write([]byte(`{"Errors":["Requests limit is exceeded."]}`))
		default:
			_, _ = w.Write([]byte(`{"EdUnits":[{"Id":7}]}`))
		}
	}))
	defer srv.Close()

	c := New(srv.URL, testKey, WithRetry(3, time.Millisecond))
	units, err := c.GetEdUnits(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || calls.Load() != 3 {
		t.Errorf("units=%v calls=%d", units, calls.Load())
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad key", http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := New(srv.URL, testKey, WithRetry(3, time.Millisecond)).GetEdUnits(t.Context())
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

// Go's HTTP client puts the full URL — auth key included — into its errors.
func TestErrorsNeverContainAuthKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // connection refused from now on

	_, err := New(srv.URL, testKey, WithRetry(1, time.Millisecond)).GetTeachers(t.Context())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "authkey") {
		t.Errorf("error leaks the auth key: %v", err)
	}
}

func TestGetEdUnitStudentsAsksForNormalStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("statuses") != "Normal" {
			t.Errorf("statuses = %q", r.URL.Query().Get("statuses"))
		}
		_, _ = w.Write([]byte(`{"EdUnitStudents":[{"EdUnitId":1,"StudentClientId":2,"Weekdays":21,"Status":"Normal"}]}`))
	}))
	defer srv.Close()

	rows, err := New(srv.URL, testKey).GetEdUnitStudents(t.Context())
	if err != nil || len(rows) != 1 || rows[0].StudentClientID != 2 || rows[0].BeginDate != "" {
		t.Errorf("rows=%+v err=%v", rows, err)
	}
}

func TestGetLessonDays(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/GetEdUnits" || q.Get("queryDays") != "true" || q.Get("dateFrom") != "2026-10-03" || q.Get("dateTo") != "2026-10-24" {
			t.Errorf("unexpected request %s %v", r.URL.Path, q)
		}
		// Shape as documented: Days carry Date, Minutes and ScheduleItemIds;
		// other fields are ignored.
		_, _ = w.Write([]byte(`{"EdUnits":[{"Id":100,"Name":"G","Days":[
			{"Date":"2026-10-12","Minutes":90,"Pass":false,"ScheduleItemIds":[7]},
			{"Date":"2026-10-14","Minutes":67.5,"ScheduleItemIds":[7,8]}]},
			{"Id":101,"Name":"No lessons"}]}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL, testKey).GetLessonDays(t.Context(), "2026-10-03", "2026-10-24")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got[0].Days) != 2 || got[0].Days[1].Minutes != 67.5 || got[0].Days[1].ScheduleItemIDs[1] != 8 || got[1].Days != nil {
		t.Errorf("got %+v", got)
	}
}

func TestSetStudentPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/SetStudentPasses" || r.URL.Query().Get("authkey") != testKey ||
			!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Errorf("unexpected request %s %s %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		var rows []map[string]any
		if err := json.NewDecoder(r.Body).Decode(&rows); err != nil {
			t.Fatal(err)
		}
		// Payable is left out (unchanged) unless set; the manual-entry guard is explicit.
		if _, has := rows[0]["Payable"]; has || rows[0]["OverwriteAcceptedManually"] != false || rows[0]["Pass"] != false || rows[0]["Date"] != "2026-10-12" {
			t.Errorf("row 0 = %v", rows[0])
		}
		if rows[1]["Payable"] != true || rows[1]["Pass"] != true || rows[1]["EdUnitId"] != 100.0 || rows[1]["StudentClientId"] != 7.0 {
			t.Errorf("row 1 = %v", rows[1])
		}
		_, _ = w.Write([]byte(`{"Errors":[{"errorType":"x","errorMessage":"The day info was set manually","Date":"2026-10-12T00:00:00","EdUnitId":100,"StudentClientId":7}]}`))
	}))
	defer srv.Close()

	yes := true
	errs, err := New(srv.URL, testKey).SetStudentPasses(t.Context(), []StudentPass{
		{Date: "2026-10-12", EdUnitID: 100, StudentClientID: 6},
		{Date: "2026-10-12", EdUnitID: 100, StudentClientID: 7, Pass: true, Payable: &yes},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 || errs[0].Message != PassSetManually || errs[0].StudentClientID != 7 {
		t.Errorf("errors = %+v", errs)
	}
}
