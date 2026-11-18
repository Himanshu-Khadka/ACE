// Package syncer pulls everything from canvas into postgres and records what
// changed since the last run.
//
// A lot of profs hide the Files and Pages tabs, so listing them gives a
// 403/404 even though the files are still linked from modules, pages and
// assignments. To get those we collect every file id / page we see while
// syncing the rest of the course and fetch them one by one.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Himanshu-Khadka/ACE/go/internal/canvas"
	"github.com/Himanshu-Khadka/ACE/go/internal/extract"
	"github.com/Himanshu-Khadka/ACE/go/internal/store"
)

const maxFileBytes = 200 << 20 // skip huge stuff like lecture recordings

type Syncer struct {
	Canvas  *canvas.Client
	Store   *store.Store
	DataDir string
	Workers int
	DueSoon time.Duration
	Log     *slog.Logger
	Now     func() time.Time // tests can fake the clock
}

// Result is what one sync did. It's saved in sync_runs and shown in /status.
type Result struct {
	Synced   map[string]int  `json:"synced"`
	NoAccess map[string]int  `json:"no_access"`
	Errors   []string        `json:"errors,omitempty"`
	Changes  []*store.Change `json:"-"`
	Baseline bool            `json:"baseline"` // first run, changes aren't recorded

	mu sync.Mutex
}

func (r *Result) ok(kind string) {
	r.mu.Lock()
	r.Synced[kind]++
	r.mu.Unlock()
}

func (r *Result) noAccess(kind string) {
	r.mu.Lock()
	r.NoAccess[kind]++
	r.mu.Unlock()
}

func (r *Result) fail(what string, err error) {
	r.mu.Lock()
	r.Errors = append(r.Errors, fmt.Sprintf("%s: %v", what, err))
	r.mu.Unlock()
}

// run is the state for one sync.
type run struct {
	*Syncer
	res *Result
	now time.Time
}

func (s *Syncer) Run(ctx context.Context) (*Result, error) {
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
	res := &Result{Synced: map[string]int{}, NoAccess: map[string]int{}}
	hadRun, err := s.Store.HasCompletedRun(ctx)
	if err != nil {
		return res, err
	}
	res.Baseline = !hadRun
	r := &run{Syncer: s, res: res, now: s.Now()}

	courses, err := s.Canvas.Courses(ctx)
	if err != nil {
		return res, fmt.Errorf("listing courses: %w", err)
	}

	// courses are independent so a few run at the same time. most of the time
	// is spent waiting on canvas, so this is where go actually helps
	workers := max(s.Workers, 1)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, c := range courses {
		if c.Name == "" { // old courses sometimes come back without details
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(c canvas.Course) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.course(ctx, c); err != nil {
				res.fail(c.CourseCode, err)
			}
		}(c)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if err := r.planner(ctx); err != nil && !canvas.IsNoAccess(err) {
		res.fail("planner", err)
	}
	if err := r.dueSoon(ctx); err != nil {
		res.fail("due soon", err)
	}
	return res, nil
}

// refs are file ids and page urls found while syncing other content.
type refs struct {
	files map[int64]bool
	pages map[string]bool
}

var (
	fileLink = regexp.MustCompile(`/files/(\d+)`)
	pageLink = regexp.MustCompile(`/courses/\d+/pages/([^"'?#/\s<>]+)`)
)

func (f *refs) scan(html string) {
	for _, m := range fileLink.FindAllStringSubmatch(html, -1) {
		var id int64
		fmt.Sscan(m[1], &id)
		f.files[id] = true
	}
	for _, m := range pageLink.FindAllStringSubmatch(html, -1) {
		if slug, err := url.PathUnescape(m[1]); err == nil {
			f.pages[slug] = true
		}
	}
}

func (r *run) course(ctx context.Context, c canvas.Course) error {
	term := ""
	if c.Term != nil {
		term = c.Term.Name
	}
	if err := r.Store.UpsertCourse(ctx, c.ID, c.Name, c.CourseCode, term); err != nil {
		return err
	}
	r.Log.Info("syncing", "course", c.CourseCode)
	ref := &refs{files: map[int64]bool{}, pages: map[string]bool{}}

	// order matters: files and pages go last so they can use the refs found before
	steps := []struct {
		name string
		fn   func(context.Context, canvas.Course, *refs) error
	}{
		{"modules", r.modules},
		{"syllabus", r.syllabus},
		{"assignments", r.assignments},
		{"announcements", r.announcements},
		{"calendar", r.events},
		{"pages", r.pages},
		{"files", r.files},
	}
	for _, step := range steps {
		err := step.fn(ctx, c, ref)
		switch {
		case err == nil:
		case canvas.IsNoAccess(err):
			// tab is hidden or disabled for students, nothing we can do
			r.res.noAccess(step.name)
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			r.res.fail(c.CourseCode+" "+step.name, err)
		}
	}
	return r.Store.MarkCourseSynced(ctx, c.ID)
}

// change records something new. On the first ever sync nothing is recorded,
// that run just fills the db.
func (r *run) change(ctx context.Context, ch store.Change) error {
	if r.res.Baseline {
		return nil
	}
	added, err := r.Store.AddChange(ctx, &ch)
	if err != nil || !added {
		return err
	}
	r.res.mu.Lock()
	r.res.Changes = append(r.res.Changes, &ch)
	r.res.mu.Unlock()
	return nil
}

func (r *run) modules(ctx context.Context, c canvas.Course, ref *refs) error {
	mods, err := r.Canvas.Modules(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, m := range mods {
		items := m.Items
		if items == nil { // canvas leaves "items" out when there's a lot of them
			items, _ = r.Canvas.ModuleItems(ctx, c.ID, m.ID)
		}
		var lines []string
		for _, it := range items {
			switch {
			case it.Type == "File" && it.ContentID != 0:
				ref.files[it.ContentID] = true
			case it.Type == "Page" && it.PageURL != "":
				ref.pages[it.PageURL] = true
			}
			lines = append(lines, fmt.Sprintf("- %s (%s)", it.Title, it.Type))
		}
		err := r.Store.UpsertDocument(ctx, store.Document{
			ID: fmt.Sprintf("module:%d", m.ID), CourseID: c.ID, Kind: "module", Title: m.Name,
			Body: strings.Join(lines, "\n"),
		})
		if err != nil {
			return err
		}
		r.res.ok("module")
	}
	return nil
}

func (r *run) syllabus(ctx context.Context, c canvas.Course, ref *refs) error {
	html, err := r.Canvas.Syllabus(ctx, c.ID)
	if err != nil || html == "" {
		return err
	}
	ref.scan(html)
	id := fmt.Sprintf("syllabus:%d", c.ID)
	body := extract.HTML(html)
	old, err := r.Store.Document(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if old != nil && old.Body == body {
		return nil
	}
	if err := r.Store.UpsertDocument(ctx, store.Document{ID: id, CourseID: c.ID, Kind: "page", Title: "Syllabus",
		URL: fmt.Sprintf("%s/courses/%d/assignments/syllabus", r.Canvas.BaseURL, c.ID), Body: body}); err != nil {
		return err
	}
	r.res.ok("syllabus")
	if old != nil {
		return r.change(ctx, store.Change{Kind: "syllabus_updated", ItemID: id, CourseID: c.ID, Title: "Syllabus"})
	}
	return nil
}

func (r *run) assignments(ctx context.Context, c canvas.Course, ref *refs) error {
	list, err := r.Canvas.Assignments(ctx, c.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, a := range list {
		ref.scan(a.Description)
		item := fromAssignment(a, c.ID, r.now)
		seen[item.ID] = true
		// always check this one: handing something in doesn't change the
		// assignment's updated_at, only the submission
		if err := r.saveCalendar(ctx, item); err != nil {
			return err
		}
		id := fmt.Sprintf("assignment:%d", a.ID)
		if same, err := r.unchanged(ctx, id, a.UpdatedAt); err != nil || same {
			if err != nil {
				return err
			}
			continue
		}
		err := r.Store.UpsertDocument(ctx, store.Document{
			ID: id, CourseID: c.ID, Kind: "assignment", Title: a.Name, URL: a.HTMLURL,
			UpdatedAt: a.UpdatedAt, DueAt: a.DueAt, Body: extract.HTML(a.Description),
		})
		if err != nil {
			return err
		}
		r.res.ok("assignment")
	}
	return r.removeMissing(ctx, c.ID, "assignment:", seen)
}

func (r *run) events(ctx context.Context, c canvas.Course, _ *refs) error {
	list, err := r.Canvas.Events(ctx, c.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range list {
		item := fromEvent(e, c.ID)
		seen[item.ID] = true
		if err := r.saveCalendar(ctx, item); err != nil {
			return err
		}
		r.res.ok("event")
	}
	return r.removeMissing(ctx, c.ID, "event:", seen)
}

// removeMissing deletes calendar items that canvas doesn't return anymore
// (the prof deleted the assignment or cancelled the event).
func (r *run) removeMissing(ctx context.Context, courseID int64, prefix string, seen map[string]bool) error {
	ids, err := r.Store.CalendarIDs(ctx, courseID, prefix)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		old, err := r.Store.CalendarItem(ctx, id)
		if err != nil || old == nil {
			continue
		}
		if err := r.Store.DeleteCalendarItem(ctx, id); err != nil {
			return err
		}
		kind := "deadline_removed"
		if !old.IsDeadline {
			kind = "event_removed"
		}
		if err := r.change(ctx, store.Change{Kind: kind, ItemID: id, CourseID: courseID, Title: old.Title,
			Old: fmtTime(old.StartAt), URL: old.URL}); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) announcements(ctx context.Context, c canvas.Course, ref *refs) error {
	list, err := r.Canvas.Announcements(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, a := range list {
		ref.scan(a.Message)
		id := fmt.Sprintf("announcement:%d", a.ID)
		stamp := a.PostedAt
		if a.LastReplyAt != nil {
			stamp = a.LastReplyAt
		}
		old, found, err := r.Store.DocumentStamp(ctx, id)
		if err != nil {
			return err
		}
		if found && sameTime(old, stamp) {
			continue
		}
		err = r.Store.UpsertDocument(ctx, store.Document{
			ID: id, CourseID: c.ID, Kind: "announcement", Title: a.Title, URL: a.HTMLURL,
			UpdatedAt: stamp, Body: extract.HTML(a.Message),
		})
		if err != nil {
			return err
		}
		r.res.ok("announcement")
		if !found { // replies bump the stamp too, only the first post is news
			if err := r.change(ctx, store.Change{Kind: "new_announcement", ItemID: id, CourseID: c.ID,
				Title: a.Title, New: fmtTime(a.PostedAt), URL: a.HTMLURL}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *run) pages(ctx context.Context, c canvas.Course, ref *refs) error {
	listed, listErr := r.Canvas.Pages(ctx, c.ID)
	if listErr != nil && !canvas.IsNoAccess(listErr) {
		return listErr
	}
	known := map[string]canvas.Page{}
	for _, p := range listed {
		known[p.URL] = p
		ref.pages[p.URL] = true
	}
	slugs := make([]string, 0, len(ref.pages))
	for slug := range ref.pages {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		// skip the extra request when the list already says it hasn't changed
		if p, ok := known[slug]; ok {
			same, err := r.unchanged(ctx, fmt.Sprintf("page:%d", p.PageID), p.UpdatedAt)
			if err != nil {
				return err
			}
			if same {
				continue
			}
		}
		p, err := r.Canvas.Page(ctx, c.ID, slug) // the list doesn't include the body
		if err != nil {
			if !canvas.IsNoAccess(err) {
				return err
			}
			r.res.noAccess("page") // unpublished or locked
			continue
		}
		ref.scan(p.Body)
		id := fmt.Sprintf("page:%d", p.PageID)
		stamp, found, err := r.Store.DocumentStamp(ctx, id)
		if err != nil {
			return err
		}
		if found && sameTime(stamp, p.UpdatedAt) {
			continue
		}
		err = r.Store.UpsertDocument(ctx, store.Document{
			ID: id, CourseID: c.ID, Kind: "page", Title: p.Title, URL: p.HTMLURL,
			UpdatedAt: p.UpdatedAt, Body: extract.HTML(p.Body),
		})
		if err != nil {
			return err
		}
		r.res.ok("page")
		kind := "new_page"
		if found {
			kind = "page_updated"
		}
		if err := r.change(ctx, store.Change{Kind: kind, ItemID: id, CourseID: c.ID, Title: p.Title, URL: p.HTMLURL}); err != nil {
			return err
		}
	}
	if listErr != nil {
		r.res.noAccess("pages tab")
	}
	return nil
}

func (r *run) files(ctx context.Context, c canvas.Course, ref *refs) error {
	listed, listErr := r.Canvas.Files(ctx, c.ID)
	if listErr != nil && !canvas.IsNoAccess(listErr) {
		return listErr
	}
	byID := map[int64]canvas.File{}
	for _, f := range listed {
		byID[f.ID] = f
		ref.files[f.ID] = true
	}
	ids := make([]int64, 0, len(ref.files))
	for id := range ref.files {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	dir := filepath.Join(r.DataDir, "courses", safeName(firstNonEmpty(c.CourseCode, c.Name)), "files")
	for _, id := range ids {
		f, ok := byID[id]
		if !ok {
			var err error
			if f, err = r.Canvas.File(ctx, id); err != nil {
				if !canvas.IsNoAccess(err) {
					return err
				}
				r.res.noAccess("file")
				continue
			}
		}
		if err := r.saveFile(ctx, c.ID, dir, f); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.res.fail(fmt.Sprintf("%s file %s", c.CourseCode, f.DisplayName), err)
		}
	}
	if listErr != nil {
		r.res.noAccess("files tab")
	}
	return nil
}

func (r *run) saveFile(ctx context.Context, courseID int64, dir string, f canvas.File) error {
	id := fmt.Sprintf("file:%d", f.ID)
	stamp, found, err := r.Store.DocumentStamp(ctx, id)
	if err != nil {
		return err
	}
	if found && sameTime(stamp, f.UpdatedAt) {
		return nil
	}
	if f.URL == "" || f.LockedForUser {
		r.res.noAccess("file")
		return nil
	}
	if f.Size > maxFileBytes {
		return nil
	}
	dest := filepath.Join(dir, fmt.Sprintf("%d_%s", f.ID, safeName(f.DisplayName)))
	if err := r.Canvas.Download(ctx, f.URL, dest); err != nil {
		return err
	}
	err = r.Store.UpsertDocument(ctx, store.Document{
		ID: id, CourseID: courseID, Kind: "file", Title: f.DisplayName, LocalPath: dest,
		URL:         fmt.Sprintf("%s/courses/%d/files/%d", r.Canvas.BaseURL, courseID, f.ID),
		ContentType: f.ContentType, Size: f.Size, UpdatedAt: f.UpdatedAt, Body: extract.File(dest),
	})
	if err != nil {
		return err
	}
	r.res.ok("file")
	kind := "new_file"
	if found {
		kind = "file_updated"
	}
	return r.change(ctx, store.Change{Kind: kind, ItemID: id, CourseID: courseID, Title: f.DisplayName,
		New: fmtTime(f.UpdatedAt), URL: fmt.Sprintf("%s/courses/%d/files/%d", r.Canvas.BaseURL, courseID, f.ID)})
}

// planner adds canvas to-do items that aren't an assignment or event already.
// It's one call for all courses so it runs after them.
func (r *run) planner(ctx context.Context) error {
	items, err := r.Canvas.PlannerItems(ctx, r.now.AddDate(0, 0, -120), r.now.AddDate(1, 0, 0))
	if err != nil {
		return err
	}
	for _, p := range items {
		if p.PlannableType == "assignment" || p.PlannableType == "calendar_event" {
			continue
		}
		if aid := p.Plannable.AssignmentID; aid != 0 {
			// graded quizzes/discussions also show up as assignments
			if old, err := r.Store.CalendarItem(ctx, fmt.Sprintf("assignment:%d", aid)); err != nil || old != nil {
				continue
			}
		}
		item, ok := fromPlanner(p, r.Canvas.BaseURL)
		if !ok {
			continue
		}
		if err := r.saveCalendar(ctx, item); err != nil {
			return err
		}
		r.res.ok("todo")
	}
	return nil
}

// saveCalendar upserts a calendar item and records what changed about it.
func (r *run) saveCalendar(ctx context.Context, item store.CalendarItem) error {
	old, err := r.Store.CalendarItem(ctx, item.ID)
	if err != nil {
		return err
	}
	if err := r.Store.UpsertCalendarItem(ctx, item); err != nil {
		return err
	}
	ch := store.Change{ItemID: item.ID, CourseID: item.CourseID, Title: item.Title, URL: item.URL}
	switch {
	case old == nil && item.IsDeadline:
		ch.Kind, ch.New = "new_deadline", fmtTime(item.StartAt)
		return r.change(ctx, ch)
	case old == nil:
		ch.Kind, ch.New = "new_event", fmtTime(item.StartAt)
		return r.change(ctx, ch)
	}
	if !sameTime(old.StartAt, item.StartAt) || (!item.IsDeadline && !sameTime(old.EndAt, item.EndAt)) {
		ch.Kind, ch.Old, ch.New = "due_date_changed", fmtTime(old.StartAt), fmtTime(item.StartAt)
		if !item.IsDeadline {
			ch.Kind = "event_changed"
		}
		if err := r.change(ctx, ch); err != nil {
			return err
		}
	}
	if old.Status != item.Status && item.IsDeadline {
		ch.Kind, ch.Old, ch.New = "status_changed", old.Status, statusText(item)
		if err := r.change(ctx, ch); err != nil {
			return err
		}
	}
	if !item.IsDeadline && old.Location != item.Location {
		ch.Kind, ch.Old, ch.New = "event_changed", old.Location, item.Location
		if err := r.change(ctx, ch); err != nil {
			return err
		}
	}
	return nil
}

// dueSoon adds a "due_soon" change for unfinished work due in the next
// DueSoon window. The dedupe key includes the due date so it fires once,
// and again only if the date moves.
func (r *run) dueSoon(ctx context.Context) error {
	if r.DueSoon <= 0 {
		return nil
	}
	items, err := r.Store.Calendar(ctx, store.CalendarFilter{
		Start: r.now, End: r.now.Add(r.DueSoon), DeadlinesOnly: true, Statuses: []string{"todo"},
	})
	if err != nil {
		return err
	}
	for _, k := range items {
		ch := store.Change{Kind: "due_soon", ItemID: k.ID, CourseID: k.CourseID, Title: k.Title,
			New: fmtTime(k.StartAt), URL: k.URL, DedupeKey: "due_soon:" + k.ID + ":" + fmtTime(k.StartAt)}
		// this one fires on the first sync too, it's useful right away
		added, err := r.Store.AddChange(ctx, &ch)
		if err != nil {
			return err
		}
		if added {
			r.res.Changes = append(r.res.Changes, &ch)
		}
	}
	return nil
}

func (r *run) unchanged(ctx context.Context, id string, stamp *time.Time) (bool, error) {
	old, found, err := r.Store.DocumentStamp(ctx, id)
	return found && sameTime(old, stamp), err
}

func statusText(k store.CalendarItem) string {
	if k.Status == "graded" && k.Score != nil && k.Points != nil {
		return fmt.Sprintf("graded (%g/%g)", *k.Score, *k.Points)
	}
	return k.Status
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

var unsafeChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

func safeName(s string) string {
	s = strings.TrimSpace(unsafeChars.ReplaceAllString(s, "_"))
	if s == "" {
		return "untitled"
	}
	if len(s) > 150 {
		s = strings.ToValidUTF8(s[:150], "")
	}
	return s
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
