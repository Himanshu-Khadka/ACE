// Package mcpserver exposes ACE as an MCP server over streamable http at
// /mcp, so Claude (or any agent that speaks MCP) can use it directly.
//
// The tool descriptions are what the model reads to pick a tool, so they say
// what each one is for. Anywhere a tool takes `course` you can pass the code
// ("CMPUT 201"), part of the name, or the id.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Himanshu-Khadka/ACE/go/internal/service"
	"github.com/Himanshu-Khadka/ACE/go/internal/store"
)

const instructions = `You have access to the student's University of Alberta Canvas courses: lecture files, pages,
assignments, announcements, deadlines with submission status, calendar events, and a log of what changed
on Canvas (new files, moved due dates, things getting graded). Times are RFC3339; talk about them in the
student's local time. When helping them study, ground explanations in their actual course material by
searching it and citing the file or page you used.`

// Handler returns the http handler for /mcp.
func Handler(svc *service.Service) http.Handler {
	s := New(svc)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
}

type courseArg struct {
	Course string `json:"course,omitempty" jsonschema:"course code like CMPUT 201, part of the name, or the id. leave empty for all courses"`
}

type deadlinesArgs struct {
	Days        float64 `json:"days,omitempty" jsonschema:"how many days ahead to look, default 14"`
	Course      string  `json:"course,omitempty" jsonschema:"course code, name or id. empty means all courses"`
	IncludeDone bool    `json:"include_done,omitempty" jsonschema:"also show submitted/graded work"`
}

type calendarArgs struct {
	StartDate string `json:"start_date,omitempty" jsonschema:"YYYY-MM-DD, default today"`
	EndDate   string `json:"end_date,omitempty" jsonschema:"YYYY-MM-DD (inclusive), default 30 days after start"`
	Course    string `json:"course,omitempty" jsonschema:"course code, name or id. empty means all courses"`
}

type changesArgs struct {
	Since   string   `json:"since,omitempty" jsonschema:"a time (2026-11-18T00:00:00Z), a date, or how long ago like 24h or 7d. default 7d"`
	AfterID int64    `json:"after_id,omitempty" jsonschema:"only changes with an id bigger than this (use the last id you saw)"`
	Kinds   []string `json:"kinds,omitempty" jsonschema:"filter by kind: new_file, file_updated, new_page, page_updated, new_announcement, new_deadline, due_date_changed, status_changed, deadline_removed, new_event, event_changed, event_removed, due_soon, syllabus_updated"`
	Course  string   `json:"course,omitempty" jsonschema:"course code, name or id"`
}

type searchArgs struct {
	Query  string `json:"query" jsonschema:"what to look for. supports quotes for phrases and -word to exclude"`
	Course string `json:"course,omitempty" jsonschema:"course code, name or id"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max results, default 10"`
}

type listDocsArgs struct {
	Course string `json:"course" jsonschema:"course code, name or id"`
	Kind   string `json:"kind,omitempty" jsonschema:"file, page, assignment, announcement or module"`
}

type getDocArgs struct {
	DocID    string `json:"doc_id" jsonschema:"document id like file:123 or page:45"`
	Offset   int    `json:"offset,omitempty" jsonschema:"character to start reading from, for long files"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"how much text to return, default 15000"`
}

type overviewArgs struct {
	Course string `json:"course" jsonschema:"course code, name or id"`
}

func New(svc *service.Service) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "ace", Version: "0.3.0"}, &mcp.ServerOptions{Instructions: instructions})

	tool(s, svc, "list_courses", "List the synced courses with their ids, codes, terms and how much content each has.",
		func(ctx context.Context, _ struct{}) (any, error) { return svc.Store.Courses(ctx) })

	tool(s, svc, "course_overview", "Syllabus, module outline, upcoming and missing work, and recent announcements "+
		"for one course. Good starting point for any question about a course.",
		func(ctx context.Context, a overviewArgs) (any, error) { return svc.Overview(ctx, a.Course) })

	tool(s, svc, "upcoming_deadlines", "Assignments, quizzes and discussions due in the next `days` days, soonest "+
		"first. Each has a status: todo, missing, submitted, graded, excused or none (nothing to submit). "+
		"Already submitted work is hidden unless include_done is true.",
		func(ctx context.Context, a deadlinesArgs) (any, error) {
			return svc.Deadlines(ctx, a.Days, a.Course, a.IncludeDone)
		})

	tool(s, svc, "missing_work", "Past due work that hasn't been submitted.",
		func(ctx context.Context, a courseArg) (any, error) { return svc.Missing(ctx, a.Course) })

	tool(s, svc, "calendar", "Deadlines plus calendar events (lectures, exams, office hours) between two dates. "+
		"Defaults to the next 30 days. Use this to plan the student's week or add things to their calendar.",
		func(ctx context.Context, a calendarArgs) (any, error) {
			return svc.Calendar(ctx, a.StartDate, a.EndDate, a.Course)
		})

	tool(s, svc, "recent_changes", "What changed on Canvas: new files and pages, new announcements, new or "+
		"moved deadlines, submission status changes (e.g. todo -> submitted -> graded), new/changed/cancelled "+
		"events and work that's due soon. Use this to tell the student what's new since they last checked.",
		func(ctx context.Context, a changesArgs) (any, error) {
			return svc.Changes(ctx, a.Since, a.AfterID, a.Kinds, a.Course, 200)
		})

	tool(s, svc, "search_course_content", "Full text search over lecture slides, PDFs, pages, assignments, "+
		"announcements and modules. Use a few specific keywords, then get_document to read a result.",
		func(ctx context.Context, a searchArgs) (any, error) {
			cid, err := svc.Store.ResolveCourse(ctx, a.Course)
			if err != nil {
				return nil, err
			}
			return svc.Store.Search(ctx, a.Query, cid, a.Limit)
		})

	tool(s, svc, "list_documents", "List what's in a course, optionally only one kind.",
		func(ctx context.Context, a listDocsArgs) (any, error) {
			cid, err := svc.Store.ResolveCourse(ctx, a.Course)
			if err != nil {
				return nil, err
			}
			return svc.Store.Documents(ctx, cid, a.Kind, time.Time{}, 0)
		})

	tool(s, svc, "get_document", "Read the text of a document (e.g. file:123). Long files come back in chunks: "+
		"if remaining > 0, call again with offset = next_offset to keep reading.",
		func(ctx context.Context, a getDocArgs) (any, error) {
			doc, err := svc.Store.Document(ctx, a.DocID)
			if err != nil {
				return nil, err
			}
			return chunk(doc, a.Offset, a.MaxChars), nil
		})

	prompts(s)
	return s
}

// tool registers a handler that returns json text. Errors go back to the
// model as text (with the list of courses when it couldn't find one) so it
// can fix the call and try again.
func tool[In any](s *mcp.Server, svc *service.Service, name, desc string, fn func(context.Context, In) (any, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: desc},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			out, err := fn(ctx, in)
			if err != nil {
				msg := map[string]any{"error": err.Error()}
				if errors.Is(err, store.ErrNotFound) {
					if cs, cerr := svc.Store.Courses(ctx); cerr == nil {
						var codes []string
						for _, c := range cs {
							codes = append(codes, c.Code)
						}
						msg["courses"] = codes
					}
				}
				return textResult(msg, true), nil, nil
			}
			return textResult(out, false), nil, nil
		})
}

func textResult(v any, isErr bool) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		b = []byte(fmt.Sprintf(`{"error": %q}`, err.Error()))
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, IsError: isErr}
}

type docChunk struct {
	*store.Document
	Text       string `json:"text"`
	NextOffset int    `json:"next_offset"`
	Remaining  int    `json:"remaining"`
}

func chunk(doc *store.Document, offset, maxChars int) docChunk {
	if maxChars <= 0 {
		maxChars = 15000
	}
	body := []rune(doc.Body)
	offset = min(max(offset, 0), len(body))
	end := min(offset+maxChars, len(body))
	text := string(body[offset:end])
	doc.Body = ""
	return docChunk{Document: doc, Text: text, NextOffset: end, Remaining: len(body) - end}
}

func prompts(s *mcp.Server) {
	text := func(desc, t string) *mcp.GetPromptResult {
		return &mcp.GetPromptResult{Description: desc,
			Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: t}}}}
	}
	arg := func(name, desc string, required bool) *mcp.PromptArgument {
		return &mcp.PromptArgument{Name: name, Description: desc, Required: required}
	}

	s.AddPrompt(&mcp.Prompt{Name: "study_plan", Description: "Make a study plan for the next few days based on what's due.",
		Arguments: []*mcp.PromptArgument{arg("days", "how many days to plan, default 7", false)}},
		func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			days := req.Params.Arguments["days"]
			if days == "" {
				days = "7"
			}
			return text("study plan", fmt.Sprintf("Today is %s. Use upcoming_deadlines (days=%s), missing_work "+
				"and calendar to see what I have coming up. Then make me a realistic day by day study plan: put "+
				"missing and soon-due work first, split big assignments into chunks, and leave time before exams. "+
				"For each block say which course material to review (search the course content for it).",
				time.Now().Format("Monday January 2"), days)), nil
		})

	s.AddPrompt(&mcp.Prompt{Name: "whats_new", Description: "Catch me up on what changed on Canvas.",
		Arguments: []*mcp.PromptArgument{arg("since", "how far back, like 24h or 7d. default 3d", false)}},
		func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			since := req.Params.Arguments["since"]
			if since == "" {
				since = "3d"
			}
			return text("what's new", fmt.Sprintf("Use recent_changes (since=%s) and upcoming_deadlines to catch "+
				"me up. Group it by course: new files/announcements worth reading, deadlines that moved, anything "+
				"that got graded, and what's due soon that I haven't submitted. Keep it short.", since)), nil
		})

	s.AddPrompt(&mcp.Prompt{Name: "quiz_me", Description: "Quiz me on a topic using the actual course material.",
		Arguments: []*mcp.PromptArgument{arg("course", "course code", true), arg("topic", "what to be quizzed on", true)}},
		func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			a := req.Params.Arguments
			return text("quiz", fmt.Sprintf("Search the %s course content for '%s' and read the most relevant "+
				"lecture files. Then quiz me with 5 questions one at a time, mixing concept and practice questions "+
				"in the style of the course. Wait for my answer before the next question, and when I get one wrong "+
				"explain it using the lecture material and tell me which file/slide to review.", a["course"], a["topic"])), nil
		})

	s.AddPrompt(&mcp.Prompt{Name: "explain_assignment", Description: "Break down an assignment and point to the material needed for it.",
		Arguments: []*mcp.PromptArgument{arg("course", "course code", true), arg("assignment", "assignment name", true)}},
		func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			a := req.Params.Arguments
			return text("assignment", fmt.Sprintf("Find the assignment '%s' in %s (search_course_content, then "+
				"get_document). Explain what it's asking in plain words, list the steps to do it, say when it's "+
				"due and how many points it's worth, and find the lecture material that covers what I need. "+
				"Don't write the solution for me.", a["assignment"], a["course"])), nil
		})
}
