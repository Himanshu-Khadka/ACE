package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Himanshu-Khadka/ACE/go/internal/mcpserver"
	"github.com/Himanshu-Khadka/ACE/go/internal/service"
	"github.com/Himanshu-Khadka/ACE/go/internal/store"
)

func setup(t *testing.T) (*httptest.Server, time.Time) {
	url := os.Getenv("ACE_TEST_DB")
	if url == "" {
		t.Skip("ACE_TEST_DB not set")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.ResetForTests(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	at := func(h int) *time.Time { x := now.Add(time.Duration(h) * time.Hour); return &x }
	st.UpsertCourse(ctx, 1, "Practical Programming", "CMPUT 201", "Fall 2026")
	st.UpsertCourse(ctx, 2, "Linear Algebra", "MATH 125", "Fall 2026")
	st.UpsertDocument(ctx, store.Document{ID: "file:1", CourseID: 1, Kind: "file", Title: "pointers.pdf", Body: "pointers and malloc"})
	st.UpsertCalendarItem(ctx, store.CalendarItem{ID: "assignment:1", CourseID: 1, Type: "assignment", Title: "Lab 4", StartAt: at(30), IsDeadline: true, Status: "todo"})
	st.UpsertCalendarItem(ctx, store.CalendarItem{ID: "assignment:2", CourseID: 1, Type: "assignment", Title: "Lab 3", StartAt: at(20), IsDeadline: true, Status: "submitted"})
	st.UpsertCalendarItem(ctx, store.CalendarItem{ID: "assignment:3", CourseID: 2, Type: "quiz", Title: "Quiz 2", StartAt: at(-5), IsDeadline: true, Status: "todo"})
	st.UpsertCalendarItem(ctx, store.CalendarItem{ID: "event:1", CourseID: 2, Type: "event", Title: "Midterm", StartAt: at(72)})
	st.AddChange(ctx, &store.Change{Kind: "status_changed", ItemID: "assignment:2", CourseID: 1, Title: "Lab 3", Old: "todo", New: "submitted"})

	svc := service.New(st, time.UTC)
	svc.Now = func() time.Time { return now }
	srv := &Server{Svc: svc, Token: "secret", DataDir: t.TempDir(), MCP: mcpserver.Handler(svc)}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, now
}

func get(t *testing.T, ts *httptest.Server, path string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		// decoding into a used slice keeps old field values, start fresh
		switch v := out.(type) {
		case *[]store.CalendarItem:
			*v = nil
		case *[]store.Change:
			*v = nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	return resp.StatusCode
}

func TestEndpoints(t *testing.T) {
	ts, _ := setup(t)

	// no token
	resp, _ := http.Get(ts.URL + "/courses")
	if resp.StatusCode != 401 {
		t.Errorf("want 401 without token, got %d", resp.StatusCode)
	}

	var items []store.CalendarItem
	get(t, ts, "/deadlines?days=7", &items)
	if len(items) != 1 || items[0].Title != "Lab 4" {
		t.Errorf("deadlines: %+v", items)
	}
	get(t, ts, "/deadlines?days=7&include_done=true", &items)
	if len(items) != 2 || items[0].Title != "Lab 3" {
		t.Errorf("deadlines with done: %+v", items)
	}
	get(t, ts, "/deadlines/missing", &items)
	if len(items) != 1 || items[0].Title != "Quiz 2" || items[0].Status != "missing" {
		t.Errorf("missing: %+v", items)
	}
	get(t, ts, "/calendar?start=2026-11-18&end=2026-11-21&course=math125", &items)
	if len(items) != 2 || items[1].Title != "Midterm" || items[1].Status != "" {
		t.Errorf("calendar: %+v", items)
	}

	var changes []store.Change
	get(t, ts, "/changes?since=24h", &changes)
	if len(changes) != 1 || changes[0].New != "submitted" {
		t.Errorf("changes: %+v", changes)
	}
	get(t, ts, "/changes?after_id=1", &changes)
	if len(changes) != 0 {
		t.Errorf("after_id: %+v", changes)
	}

	var hits []store.Document
	get(t, ts, "/search?q=malloc", &hits)
	if len(hits) != 1 {
		t.Errorf("search: %+v", hits)
	}

	var e map[string]string
	if code := get(t, ts, "/deadlines?course=nope", &e); code != 404 {
		t.Errorf("unknown course should 404, got %d %v", code, e)
	}
	if code := get(t, ts, "/calendar?start=yesterday", &e); code != 400 {
		t.Errorf("bad date should 400, got %d", code)
	}
	if code := get(t, ts, "/documents/file:1/download", &e); code != 404 {
		t.Errorf("doc without file should 404, got %d", code)
	}

	// ics works with ?token= for calendar subscriptions
	resp, _ = http.Get(ts.URL + "/calendar.ics?token=secret")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Due: Lab 4") {
		t.Errorf("ics: %d %s", resp.StatusCode, body)
	}
}

type tokenTransport struct{}

func (tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer secret")
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCP(t *testing.T) {
	ts, _ := setup(t)
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	sess, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: tokenTransport{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	tools, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) < 8 {
		t.Errorf("only %d tools", len(tools.Tools))
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "upcoming_deadlines", Arguments: map[string]any{"days": 7}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError || !strings.Contains(text, "Lab 4") {
		t.Errorf("upcoming_deadlines: %s", text)
	}
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "course_overview", Arguments: map[string]any{"course": "physics"}})
	if err != nil {
		t.Fatal(err)
	}
	text = res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.Contains(text, "CMPUT 201") {
		t.Errorf("bad course should list the real ones: %s", text)
	}
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "get_document", Arguments: map[string]any{"doc_id": "file:1", "max_chars": 8}})
	if err != nil {
		t.Fatal(err)
	}
	var chunk struct {
		Text      string `json:"text"`
		Remaining int    `json:"remaining"`
	}
	json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &chunk)
	if chunk.Text != "pointers" || chunk.Remaining != 11 {
		t.Errorf("chunk: %+v", chunk)
	}
	p, err := sess.GetPrompt(ctx, &mcp.GetPromptParams{Name: "quiz_me", Arguments: map[string]string{"course": "CMPUT 201", "topic": "pointers"}})
	if err != nil || len(p.Messages) != 1 {
		t.Errorf("prompt: %v %+v", err, p)
	}
}
