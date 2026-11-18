package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Himanshu-Khadka/ACE/go/internal/canvas"
	"github.com/Himanshu-Khadka/ACE/go/internal/store"
)

// fakeCanvas is a tiny canvas with one course where the prof hid the Files
// and Pages tabs, like a lot of real courses.
type fakeCanvas struct {
	mu            sync.Mutex
	srv           *httptest.Server
	moduleItems   []map[string]any
	assignments   []map[string]any
	events        []map[string]any
	announcements []map[string]any
}

func newFake(t *testing.T, now time.Time) *fakeCanvas {
	f := &fakeCanvas{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	ts := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	f.moduleItems = []map[string]any{
		{"title": "Lecture 1", "type": "File", "content_id": 10},
		{"title": "Week 1", "type": "Page", "page_url": "week-1"},
	}
	f.assignments = []map[string]any{
		{"id": 1, "name": "Lab 1", "due_at": ts(30 * time.Hour), "updated_at": ts(-72 * time.Hour),
			"submission_types": []string{"online_upload"}, "points_possible": 10,
			"description": `<p>see <a href="/courses/7/files/11/download">handout</a></p>`,
			"submission":  map[string]any{"workflow_state": "unsubmitted"}},
		{"id": 2, "name": "Lab 2", "due_at": ts(200 * time.Hour), "updated_at": ts(-72 * time.Hour),
			"submission_types": []string{"online_upload"}, "points_possible": 10},
	}
	f.events = []map[string]any{
		{"id": 5, "title": "Midterm", "start_at": ts(100 * time.Hour), "end_at": ts(102 * time.Hour), "location_name": "CCIS 1-430"},
	}
	f.announcements = []map[string]any{
		{"id": 20, "title": "Welcome", "message": "<p>hi</p>", "posted_at": ts(-100 * time.Hour)},
	}
	return f
}

func (f *fakeCanvas) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeCanvas) file(id int) map[string]any {
	return map[string]any{"id": id, "display_name": fmt.Sprintf("notes%d.txt", id), "size": 20,
		"url": fmt.Sprintf("%s/dl/%d", f.srv.URL, id), "updated_at": "2026-09-01T00:00:00Z"}
}

func (f *fakeCanvas) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "unauthorized", 401)
		return
	}
	p := r.URL.Path
	var out any
	switch {
	case p == "/api/v1/courses":
		out = []map[string]any{{"id": 7, "name": "Practical Programming", "course_code": "CMPUT 201", "term": map[string]any{"name": "Fall 2026"}}}
	case p == "/api/v1/courses/7/modules":
		out = []map[string]any{{"id": 1, "name": "Week 1", "items": f.moduleItems}}
	case p == "/api/v1/courses/7":
		out = map[string]any{"syllabus_body": "<p>Syllabus. Exam is worth 40%</p>"}
	case p == "/api/v1/courses/7/files", p == "/api/v1/courses/7/pages":
		http.Error(w, `{"status":"unauthorized"}`, 403) // hidden tabs
		return
	case strings.HasPrefix(p, "/api/v1/files/"):
		var id int
		fmt.Sscan(strings.TrimPrefix(p, "/api/v1/files/"), &id)
		out = f.file(id)
	case p == "/api/v1/courses/7/pages/week-1":
		out = map[string]any{"page_id": 3, "url": "week-1", "title": "Week 1", "body": "<p>read chapter 1</p>",
			"updated_at": "2026-09-02T00:00:00Z"}
	case p == "/api/v1/courses/7/assignments":
		// second page to make sure pagination works
		if r.URL.Query().Get("page") == "2" {
			out = f.assignments[1:]
		} else {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="next"`, f.srv.URL, p))
			out = f.assignments[:1]
		}
	case p == "/api/v1/calendar_events":
		out = f.events
	case p == "/api/v1/announcements":
		out = f.announcements
	case p == "/api/v1/planner/items":
		out = []map[string]any{
			{"plannable_type": "planner_note", "plannable_id": 99, "plannable_date": "2026-12-01T18:00:00Z",
				"plannable": map[string]any{"title": "buy textbook"}, "submissions": false},
			// same as Lab 1, should be skipped
			{"plannable_type": "assignment", "plannable_id": 1, "plannable": map[string]any{"title": "Lab 1"}},
		}
	case strings.HasPrefix(p, "/dl/"):
		io.WriteString(w, "file "+strings.TrimPrefix(p, "/dl/")+" pointers and malloc")
		return
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(out)
}

func testStore(t *testing.T) *store.Store {
	url := os.Getenv("ACE_TEST_DB")
	if url == "" {
		t.Skip("ACE_TEST_DB not set")
	}
	s, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.ResetForTests(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func kinds(changes []*store.Change) map[string]int {
	m := map[string]int{}
	for _, c := range changes {
		m[c.Kind]++
	}
	return m
}

func TestSyncAndChanges(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now()
	fake := newFake(t, now)
	sy := &Syncer{Canvas: canvas.New(fake.srv.URL, "tok"), Store: st, DataDir: t.TempDir(), Workers: 2,
		DueSoon: 48 * time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}

	sched := NewScheduler(sy, time.Hour, "")
	sched.runOnce(ctx) // first run goes through the scheduler so it gets saved in sync_runs

	// hidden tabs: file 10 comes from the module, 11 from the assignment, page from the module
	for _, id := range []string{"file:10", "file:11", "page:3", "syllabus:7", "announcement:20"} {
		if _, err := st.Document(ctx, id); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	hits, _ := st.Search(ctx, "malloc", 0, 10)
	if len(hits) != 2 {
		t.Errorf("search should find both files, got %d", len(hits))
	}
	if k, _ := st.CalendarItem(ctx, "planner:planner_note:99"); k == nil || k.Title != "buy textbook" {
		t.Errorf("planner note missing: %+v", k)
	}
	if k, _ := st.CalendarItem(ctx, "planner:assignment:1"); k != nil {
		t.Errorf("assignment shouldn't be duplicated from the planner")
	}
	// first run only records due_soon (Lab 1 is due in 30h)
	first, _ := st.Changes(ctx, store.ChangeFilter{})
	if len(first) != 1 || first[0].Kind != "due_soon" || first[0].ItemID != "assignment:1" {
		t.Fatalf("first run changes: %+v", first)
	}

	// things change on canvas
	fake.set(func() {
		fake.assignments[0]["submission"] = map[string]any{"workflow_state": "submitted", "submitted_at": now.Format(time.RFC3339)}
		fake.assignments = fake.assignments[:1] // Lab 2 deleted
		fake.events[0]["start_at"] = now.Add(124 * time.Hour).UTC().Format(time.RFC3339)
		fake.moduleItems = append(fake.moduleItems, map[string]any{"title": "Lecture 2", "type": "File", "content_id": 12})
		fake.announcements = append(fake.announcements, map[string]any{"id": 21, "title": "Midterm moved",
			"message": "<p>moved to friday</p>", "posted_at": now.UTC().Format(time.RFC3339)})
	})
	res, err := sy.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := kinds(res.Changes)
	want := map[string]int{"status_changed": 1, "deadline_removed": 1, "event_changed": 1, "new_file": 1, "new_announcement": 1}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s: got %d want %d (all: %v)", k, got[k], n, got)
		}
	}
	if len(res.Changes) != 5 {
		t.Errorf("unexpected extra changes: %v", got)
	}
	for _, c := range res.Changes {
		if c.Kind == "status_changed" && (c.Old != "todo" || c.New != "submitted") {
			t.Errorf("status change: %+v", c)
		}
	}

	// nothing changed -> nothing recorded, and due_soon doesn't fire twice
	res, err = sy.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 0 {
		t.Errorf("third run should be quiet, got %v", kinds(res.Changes))
	}
}

func TestSubmissionStatus(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	score := 9.0
	cases := []struct {
		name  string
		sub   *canvas.Submission
		due   *time.Time
		types []string
		want  string
	}{
		{"nothing yet", nil, &future, []string{"online_upload"}, "todo"},
		{"late", nil, &past, []string{"online_upload"}, "missing"},
		{"submitted", &canvas.Submission{SubmittedAt: &past}, &past, nil, "submitted"},
		{"graded", &canvas.Submission{WorkflowState: "graded", Score: &score}, &past, nil, "graded"},
		{"excused", &canvas.Submission{Excused: true}, &past, nil, "excused"},
		{"on paper", nil, &past, []string{"on_paper"}, "none"},
		{"no due date", nil, nil, []string{"online_upload"}, "todo"},
	}
	for _, c := range cases {
		if got := submissionStatus(c.sub, c.due, c.types, now); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}
