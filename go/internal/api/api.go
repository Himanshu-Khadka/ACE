// Package api is the HTTP api: courses, deadlines, calendar (json + ics),
// changes, documents and search. GET / lists the endpoints.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Himanshu-Khadka/ACE/go/internal/ics"
	"github.com/Himanshu-Khadka/ACE/go/internal/service"
	"github.com/Himanshu-Khadka/ACE/go/internal/store"
	"github.com/Himanshu-Khadka/ACE/go/internal/syncer"
)

type Server struct {
	Svc       *service.Service
	Scheduler *syncer.Scheduler // nil when the api runs without syncing
	Token     string
	DataDir   string
	MCP       http.Handler // optional, mounted at /mcp
	Log       *slog.Logger
}

var endpoints = []string{
	"GET  /health",
	"GET  /status                         scheduler state + last syncs",
	"POST /sync                           start a sync now",
	"GET  /courses",
	"GET  /courses/{course}/overview      syllabus, modules, upcoming + missing work, announcements",
	"GET  /deadlines?days=14&course=&include_done=false",
	"GET  /deadlines/missing?course=",
	"GET  /calendar?start=2026-11-01&end=2026-11-30&course=",
	"GET  /calendar.ics?days=120&course=",
	"GET  /changes?since=24h&after_id=&kind=new_file,status_changed&course=&limit=",
	"GET  /documents?course=&kind=file&since=7d",
	"GET  /documents/{id}",
	"GET  /documents/{id}/download",
	"GET  /search?q=&course=&limit=10",
	"POST /mcp                            MCP (streamable http)",
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"name": "ace", "endpoints": endpoints})
	})
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /status", s.status)
	mux.HandleFunc("POST /sync", s.sync)
	mux.HandleFunc("GET /courses", s.courses)
	mux.HandleFunc("GET /courses/{course}/overview", s.overview)
	mux.HandleFunc("GET /deadlines", s.deadlines)
	mux.HandleFunc("GET /deadlines/missing", s.missing)
	mux.HandleFunc("GET /calendar", s.calendar)
	mux.HandleFunc("GET /calendar.ics", s.calendarICS)
	mux.HandleFunc("GET /changes", s.changes)
	mux.HandleFunc("GET /documents", s.documents)
	mux.HandleFunc("GET /documents/{id}", s.document)
	mux.HandleFunc("GET /documents/{id}/download", s.download)
	mux.HandleFunc("GET /search", s.search)
	if s.MCP != nil {
		mux.Handle("/mcp", s.MCP)
	}
	return s.logRequests(s.auth(mux))
}

// auth checks API_TOKEN if one is set. It can come as a bearer token or as
// ?token= because calendar apps can't send headers when subscribing to the ics.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token == "" || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
			writeError(w, http.StatusUnauthorized, errors.New("missing or wrong API token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if s.Log != nil && r.URL.Path != "/health" {
			s.Log.Debug("request", "method", r.Method, "path", r.URL.Path, "took", time.Since(start))
		}
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.Svc.Store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	runs, err := s.Svc.Store.Runs(r.Context(), 5)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	out := map[string]any{"recent_syncs": runs}
	if s.Scheduler != nil {
		out["scheduler"] = s.Scheduler.State()
	}
	writeJSON(w, 200, out)
}

func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	if s.Scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("syncing is turned off"))
		return
	}
	if !s.Scheduler.Trigger() {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "a sync is already queued"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sync started, check /status"})
}

func (s *Server) courses(w http.ResponseWriter, r *http.Request) {
	out, err := s.Svc.Store.Courses(r.Context())
	respond(w, out, err)
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	out, err := s.Svc.Overview(r.Context(), r.PathValue("course"))
	respond(w, out, err)
}

func (s *Server) deadlines(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days, _ := strconv.ParseFloat(q.Get("days"), 64)
	out, err := s.Svc.Deadlines(r.Context(), days, q.Get("course"), q.Get("include_done") == "true")
	respond(w, out, err)
}

func (s *Server) missing(w http.ResponseWriter, r *http.Request) {
	out, err := s.Svc.Missing(r.Context(), r.URL.Query().Get("course"))
	respond(w, out, err)
}

func (s *Server) calendar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.Svc.Calendar(r.Context(), q.Get("start"), q.Get("end"), q.Get("course"))
	respond(w, out, err)
}

func (s *Server) calendarICS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days, _ := strconv.ParseFloat(q.Get("days"), 64)
	if days <= 0 {
		days = 120
	}
	ctx := r.Context()
	cid, err := s.Svc.Store.ResolveCourse(ctx, q.Get("course"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	now := s.Svc.Now()
	items, err := s.Svc.Store.Calendar(ctx, store.CalendarFilter{
		Start: now.AddDate(0, 0, -14), End: now.Add(time.Duration(days * 24 * float64(time.Hour))), CourseID: cid,
	})
	if err != nil {
		writeError(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="ace.ics"`)
	w.Write([]byte(ics.Write(items, "ACE (Canvas)", s.Svc.Loc, now)))
}

func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	afterID, _ := strconv.ParseInt(q.Get("after_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	var kinds []string
	if k := q.Get("kind"); k != "" {
		kinds = strings.Split(k, ",")
	}
	out, err := s.Svc.Changes(r.Context(), q.Get("since"), afterID, kinds, q.Get("course"), limit)
	respond(w, out, err)
}

func (s *Server) documents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ctx := r.Context()
	cid, err := s.Svc.Store.ResolveCourse(ctx, q.Get("course"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	var since time.Time
	if v := q.Get("since"); v != "" {
		if since, err = s.Svc.ParseTime(v); err != nil {
			writeError(w, 400, err)
			return
		}
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := s.Svc.Store.Documents(ctx, cid, q.Get("kind"), since, limit)
	if out == nil {
		out = []store.Document{}
	}
	respond(w, out, err)
}

func (s *Server) document(w http.ResponseWriter, r *http.Request) {
	out, err := s.Svc.Store.Document(r.Context(), r.PathValue("id"))
	respond(w, out, err)
}

// download serves the original file (pdf, slides...) that the sync saved.
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	doc, err := s.Svc.Store.Document(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if doc.LocalPath == "" {
		writeError(w, 404, errors.New("this document has no file, use /documents/{id} for its text"))
		return
	}
	// only ever serve stuff from inside the data folder
	root, _ := filepath.Abs(s.DataDir)
	path, _ := filepath.Abs(doc.LocalPath)
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		writeError(w, 403, errors.New("file is outside the data folder"))
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, 404, errors.New("file isn't on disk anymore, run a sync"))
		return
	}
	defer f.Close()
	if doc.ContentType != "" {
		w.Header().Set("Content-Type", doc.ContentType)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(doc.Title, `"`, "")+`"`)
	http.ServeContent(w, r, doc.Title, modTime(doc), f)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if strings.TrimSpace(q.Get("q")) == "" {
		writeError(w, 400, errors.New("q is required"))
		return
	}
	ctx := r.Context()
	cid, err := s.Svc.Store.ResolveCourse(ctx, q.Get("course"))
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	out, err := s.Svc.Store.Search(ctx, q.Get("q"), cid, limit)
	if out == nil {
		out = []store.Document{}
	}
	respond(w, out, err)
}

func modTime(d *store.Document) time.Time {
	if d.UpdatedAt != nil {
		return *d.UpdatedAt
	}
	return time.Time{}
}

func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	writeJSON(w, 200, v)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return 404
	case errors.Is(err, context.Canceled):
		return 499
	case errors.Is(err, service.ErrBadInput):
		return 400
	}
	return 500
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
