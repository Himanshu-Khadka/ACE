package syncer

import (
	"fmt"
	"strings"
	"time"

	"github.com/Himanshu-Khadka/ACE/go/internal/canvas"
	"github.com/Himanshu-Khadka/ACE/go/internal/extract"
	"github.com/Himanshu-Khadka/ACE/go/internal/store"
)

// Canvas has no single "did I do this" field, so the status gets worked out
// from the submission object. Same rules as the python version.

// assignments you can't hand anything in for on canvas
var noSubmission = map[string]bool{"none": true, "on_paper": true, "not_graded": true, "wiki_page": true}

var Done = []string{"submitted", "graded", "excused", "none"}
var NotDone = []string{"todo", "missing"}

func submissionStatus(sub *canvas.Submission, due *time.Time, types []string, now time.Time) string {
	if sub == nil {
		sub = &canvas.Submission{}
	}
	switch {
	case sub.Excused:
		return "excused"
	case sub.WorkflowState == "graded" && sub.Score != nil:
		return "graded"
	case sub.SubmittedAt != nil:
		return "submitted"
	case len(types) > 0 && allNoSubmission(types):
		return "none"
	case sub.Missing || (due != nil && due.Before(now)):
		return "missing"
	}
	return "todo"
}

func allNoSubmission(types []string) bool {
	for _, t := range types {
		if !noSubmission[t] {
			return false
		}
	}
	return true
}

func assignmentType(a canvas.Assignment) string {
	for _, t := range a.SubmissionTypes {
		if t == "online_quiz" {
			return "quiz"
		}
	}
	if a.QuizID != 0 || a.IsQuizAssignment {
		return "quiz"
	}
	for _, t := range a.SubmissionTypes {
		if t == "discussion_topic" {
			return "discussion"
		}
	}
	return "assignment"
}

func short(s string) string {
	if len(s) <= 2000 {
		return s
	}
	return strings.ToValidUTF8(s[:2000], "")
}

func fromAssignment(a canvas.Assignment, courseID int64, now time.Time) store.CalendarItem {
	var score *float64
	if a.Submission != nil {
		score = a.Submission.Score
	}
	return store.CalendarItem{
		ID: fmt.Sprintf("assignment:%d", a.ID), CourseID: courseID, Type: assignmentType(a), Title: a.Name,
		StartAt: a.DueAt, IsDeadline: true,
		Status: submissionStatus(a.Submission, a.DueAt, a.SubmissionTypes, now),
		Points: a.PointsPossible, Score: score, URL: a.HTMLURL,
		Description: short(extract.HTML(a.Description)),
	}
}

func fromEvent(e canvas.Event, courseID int64) store.CalendarItem {
	return store.CalendarItem{
		ID: fmt.Sprintf("event:%d", e.ID), CourseID: courseID, Type: "event", Title: e.Title,
		StartAt: e.StartAt, EndAt: e.EndAt, AllDay: e.AllDay, Location: e.LocationName, URL: e.HTMLURL,
		Description: short(extract.HTML(e.Description)),
	}
}

var plannerTypes = map[string]string{
	"quiz": "quiz", "discussion_topic": "discussion", "wiki_page": "todo",
	"planner_note": "todo", "assessment_request": "todo",
}

// fromPlanner is only used for to-do items that aren't already an assignment
// or a calendar event (ungraded quizzes/discussions, pages with a to-do date,
// personal notes). ok is false for types we don't care about.
func fromPlanner(p canvas.PlannerItem, baseURL string) (item store.CalendarItem, ok bool) {
	kind, ok := plannerTypes[p.PlannableType]
	if !ok {
		return item, false
	}
	sub := p.SubmissionState()
	status := "todo"
	switch {
	case sub.Excused:
		status = "excused"
	case sub.Graded:
		status = "graded"
	case sub.Submitted:
		status = "submitted"
	case sub.Missing:
		status = "missing"
	case p.PlannerOverride != nil && p.PlannerOverride.MarkedComplete:
		status = "submitted" // ticked off in the canvas to-do list
	}
	url := p.HTMLURL
	if strings.HasPrefix(url, "/") {
		url = baseURL + url
	}
	title := p.Plannable.Title
	if title == "" {
		title = "untitled"
	}
	return store.CalendarItem{
		ID:       fmt.Sprintf("planner:%s:%d", p.PlannableType, p.PlannableID),
		CourseID: p.CourseID, Type: kind, Title: title, StartAt: p.PlannableDate, IsDeadline: true,
		Status: status, Points: p.Plannable.PointsPossible, URL: url,
		Description: short(extract.HTML(p.Plannable.Details)),
	}, true
}
