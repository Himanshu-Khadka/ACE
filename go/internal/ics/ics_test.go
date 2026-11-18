package ics

import (
	"strings"
	"testing"
	"time"

	"github.com/Himanshu-Khadka/ACE/go/internal/store"
)

func TestWrite(t *testing.T) {
	due := time.Date(2026, 11, 20, 6, 59, 0, 0, time.UTC)
	loc := time.FixedZone("MST", -7*3600)
	out := Write([]store.CalendarItem{
		{ID: "assignment:1", Course: "CMPUT 201", Title: "Lab 3, part 2", StartAt: &due, IsDeadline: true, Status: "todo"},
		{ID: "assignment:2", Title: "Lab 2", StartAt: &due, IsDeadline: true, Status: "submitted"},
		{ID: "event:3", Title: "Reading week", StartAt: &due, AllDay: true},
		{ID: "event:4", Title: "no date"},
	}, "ACE", loc, due)

	for _, want := range []string{
		"SUMMARY:[CMPUT 201] Due: Lab 3\\, part 2",
		"DTSTART:20261120T062900Z", "DTEND:20261120T065900Z",
		"SUMMARY:(done) Due: Lab 2",
		"DTSTART;VALUE=DATE:20261119", // 06:59 UTC is still the 19th at UTC-7
		"UID:assignment-1@ace",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "BEGIN:VEVENT") != 3 {
		t.Errorf("want 3 events")
	}
	if strings.Count(out, "BEGIN:VALARM") != 1 {
		t.Errorf("only the unfinished deadline should get a reminder")
	}
	for _, line := range strings.Split(out, "\r\n") {
		if len(line) > 75 {
			t.Errorf("line not folded: %q", line)
		}
	}
}

func TestFoldKeepsUTF8(t *testing.T) {
	long := "SUMMARY:" + strings.Repeat("é", 60)
	got := fold(long)
	if strings.ReplaceAll(got, "\r\n ", "") != long {
		t.Fatal("folding changed the text")
	}
}
