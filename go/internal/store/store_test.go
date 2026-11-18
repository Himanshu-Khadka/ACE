package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// testDB gives a fresh store, or skips if ACE_TEST_DB isn't set. Run with
//
//	ACE_TEST_DB=postgres://postgres:test@localhost:55432/ace?sslmode=disable go test -p 1 ./...
func testDB(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("ACE_TEST_DB")
	if url == "" {
		t.Skip("ACE_TEST_DB not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ResetForTests(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestSearchAndResolve(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	big := int64(100000000012345) // canvas ids can be bigger than int32
	must(t, s.UpsertCourse(ctx, big, "Practical Programming Methodology", "CMPUT 201", "Fall 2026"))
	must(t, s.UpsertCourse(ctx, 2, "Linear Algebra I", "MATH 125", "Fall 2026"))
	must(t, s.UpsertDocument(ctx, Document{ID: "file:1", CourseID: big, Kind: "file", Title: "Lecture 5 pointers.pdf",
		Body: "a pointer stores the address of another variable"}))
	must(t, s.UpsertDocument(ctx, Document{ID: "page:2", CourseID: 2, Kind: "page", Title: "Eigenvalues",
		Body: "eigenvalues and eigenvectors"}))

	id, err := s.ResolveCourse(ctx, "cmput201")
	if err != nil || id != big {
		t.Fatalf("resolve: %v %v", id, err)
	}
	if _, err := s.ResolveCourse(ctx, "nope"); err == nil {
		t.Fatal("expected error for unknown course")
	}

	res, err := s.Search(ctx, "pointers", 0, 10)
	must(t, err)
	if len(res) != 1 || res[0].ID != "file:1" || res[0].Course != "CMPUT 201" {
		t.Fatalf("search: %+v", res)
	}
	// weird input shouldn't blow up the query
	_, err = s.Search(ctx, `C++ "unclosed -`, big, 10)
	must(t, err)

	courses, err := s.Courses(ctx)
	must(t, err)
	if courses[0].Counts["file"] != 1 {
		t.Fatalf("counts: %+v", courses[0])
	}
}

func TestCalendarAndChanges(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	must(t, s.UpsertCourse(ctx, 1, "Data Structures", "CMPUT 175", ""))
	due := time.Now().Add(24 * time.Hour)
	must(t, s.UpsertCalendarItem(ctx, CalendarItem{ID: "assignment:1", CourseID: 1, Type: "assignment",
		Title: "Lab 4", StartAt: &due, IsDeadline: true, Status: "todo"}))
	must(t, s.UpsertCalendarItem(ctx, CalendarItem{ID: "planner:planner_note:9", Type: "todo",
		Title: "buy textbook", StartAt: &due, IsDeadline: true, Status: "todo"}))

	items, err := s.Calendar(ctx, CalendarFilter{Start: time.Now(), CourseID: 1, Statuses: []string{"todo"}})
	must(t, err)
	if len(items) != 1 || items[0].Course != "CMPUT 175" {
		t.Fatalf("calendar: %+v", items)
	}
	all, err := s.Calendar(ctx, CalendarFilter{})
	must(t, err)
	if len(all) != 2 {
		t.Fatalf("want 2 items, got %d", len(all))
	}

	added, err := s.AddChange(ctx, &Change{Kind: "due_soon", ItemID: "assignment:1", CourseID: 1, Title: "Lab 4", DedupeKey: "x"})
	must(t, err)
	again, err := s.AddChange(ctx, &Change{Kind: "due_soon", ItemID: "assignment:1", CourseID: 1, Title: "Lab 4", DedupeKey: "x"})
	must(t, err)
	if !added || again {
		t.Fatalf("dedupe didn't work: %v %v", added, again)
	}
	_, err = s.AddChange(ctx, &Change{Kind: "new_file", ItemID: "file:3", CourseID: 1, Title: "notes.pdf"})
	must(t, err)

	got, err := s.Changes(ctx, ChangeFilter{})
	must(t, err)
	if len(got) != 2 || got[0].Course != "CMPUT 175" {
		t.Fatalf("changes: %+v", got)
	}
	got, err = s.Changes(ctx, ChangeFilter{Kinds: []string{"new_file"}})
	must(t, err)
	if len(got) != 1 {
		t.Fatalf("kind filter: %+v", got)
	}
	got, err = s.Changes(ctx, ChangeFilter{AfterID: got[0].ID})
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("after id: %+v", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
