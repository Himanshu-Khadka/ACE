// Package store keeps everything in postgres: courses, documents (with full
// text search), calendar items, detected changes and sync history.
package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

var ErrNotFound = errors.New("not found")

type Store struct {
	db *pgxpool.Pool
}

// Open connects and creates the tables. Postgres can take a few seconds to
// come up in docker so it retries for a bit.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	for i := 0; ; i++ {
		err = pool.Ping(ctx)
		if err == nil || i == 30 {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("creating tables: %w", err)
	}
	return &Store{db: pool}, nil
}

func (s *Store) Close() { s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

// ---- courses ----

type Course struct {
	ID       int64            `json:"id"`
	Name     string           `json:"name"`
	Code     string           `json:"code"`
	Term     string           `json:"term"`
	SyncedAt *time.Time       `json:"synced_at"`
	Counts   map[string]int64 `json:"counts"`
}

func (s *Store) UpsertCourse(ctx context.Context, id int64, name, code, term string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO courses (id, name, code, term) VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET name = $2, code = $3, term = $4`, id, name, code, term)
	return err
}

func (s *Store) MarkCourseSynced(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE courses SET synced_at = now() WHERE id = $1`, id)
	return err
}

func (s *Store) Courses(ctx context.Context) ([]Course, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, code, term, synced_at FROM courses ORDER BY code, name`)
	if err != nil {
		return nil, err
	}
	courses, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Course, error) {
		c := Course{Counts: map[string]int64{}}
		return c, r.Scan(&c.ID, &c.Name, &c.Code, &c.Term, &c.SyncedAt)
	})
	if err != nil {
		return nil, err
	}
	byID := map[int64]*Course{}
	for i := range courses {
		byID[courses[i].ID] = &courses[i]
	}
	rows, err = s.db.Query(ctx, `SELECT course_id, kind, count(*) FROM documents GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, n int64
		var kind string
		if err := rows.Scan(&id, &kind, &n); err != nil {
			return nil, err
		}
		if c := byID[id]; c != nil {
			c.Counts[kind] = n
		}
	}
	return courses, rows.Err()
}

func (s *Store) Course(ctx context.Context, id int64) (*Course, error) {
	all, err := s.Courses(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if c.ID == id {
			return &c, nil
		}
	}
	return nil, ErrNotFound
}

// ResolveCourse turns "cmput201", "CMPUT 201", part of a name or an id into a
// course id. Empty string means "all courses" and gives 0.
func (s *Store) ResolveCourse(ctx context.Context, q string) (int64, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, nil
	}
	if id, err := strconv.ParseInt(q, 10, 64); err == nil {
		return id, nil
	}
	norm := func(x string) string { return strings.ToLower(strings.ReplaceAll(x, " ", "")) }
	needle := norm(q)
	courses, err := s.Courses(ctx)
	if err != nil {
		return 0, err
	}
	var exact, partial []Course
	for _, c := range courses {
		if norm(c.Code) == needle || norm(c.Name) == needle {
			exact = append(exact, c)
		} else if strings.Contains(norm(c.Code), needle) || strings.Contains(norm(c.Name), needle) {
			partial = append(partial, c)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 0:
		return 0, fmt.Errorf("%w: no course matching %q", ErrNotFound, q)
	case 1:
		return matches[0].ID, nil
	}
	var names []string
	for _, c := range matches {
		names = append(names, c.Code)
	}
	return 0, fmt.Errorf("%w: %q matches more than one course (%s), be more specific",
		ErrNotFound, q, strings.Join(names, ", "))
}

// ---- documents ----

type Document struct {
	ID          string     `json:"id"`
	CourseID    int64      `json:"course_id"`
	Course      string     `json:"course,omitempty"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	URL         string     `json:"url,omitempty"`
	LocalPath   string     `json:"local_path,omitempty"`
	ContentType string     `json:"content_type,omitempty"`
	Size        int64      `json:"size,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at"`
	DueAt       *time.Time `json:"due_at,omitempty"`
	FirstSeen   time.Time  `json:"first_seen"`
	Body        string     `json:"body,omitempty"`
	Snippet     string     `json:"snippet,omitempty"`
}

// DocumentStamp gives the canvas updated_at we saved last time, so the sync
// can skip things that haven't changed. found is false for new documents.
func (s *Store) DocumentStamp(ctx context.Context, id string) (stamp *time.Time, found bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT updated_at FROM documents WHERE id = $1`, id).Scan(&stamp)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return stamp, err == nil, err
}

func (s *Store) UpsertDocument(ctx context.Context, d Document) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO documents (id, course_id, kind, title, url, local_path, content_type, size, updated_at, due_at, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO UPDATE SET
			title = $4, url = $5, local_path = $6, content_type = $7, size = $8,
			updated_at = $9, due_at = $10, body = $11`,
		d.ID, d.CourseID, d.Kind, d.Title, d.URL, d.LocalPath, d.ContentType, d.Size, d.UpdatedAt, d.DueAt, d.Body)
	return err
}

const docCols = `d.id, d.course_id, c.code, d.kind, d.title, d.url, d.local_path, d.content_type, d.size,
	d.updated_at, d.due_at, d.first_seen`

func scanDoc(r pgx.Row, d *Document, extra ...any) error {
	return r.Scan(append([]any{&d.ID, &d.CourseID, &d.Course, &d.Kind, &d.Title, &d.URL, &d.LocalPath,
		&d.ContentType, &d.Size, &d.UpdatedAt, &d.DueAt, &d.FirstSeen}, extra...)...)
}

func (s *Store) Document(ctx context.Context, id string) (*Document, error) {
	var d Document
	row := s.db.QueryRow(ctx, `SELECT `+docCols+`, d.body FROM documents d JOIN courses c ON c.id = d.course_id
		WHERE d.id = $1`, id)
	if err := scanDoc(row, &d, &d.Body); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

// Documents lists documents without their body. courseID 0 means all courses,
// since > zero time only gives things first seen after it.
func (s *Store) Documents(ctx context.Context, courseID int64, kind string, since time.Time, limit int) ([]Document, error) {
	rows, err := s.db.Query(ctx, `SELECT `+docCols+` FROM documents d JOIN courses c ON c.id = d.course_id
		WHERE ($1::bigint = 0 OR d.course_id = $1) AND ($2 = '' OR d.kind = $2) AND d.first_seen > $3
		ORDER BY c.code, d.kind, d.title LIMIT $4`, courseID, kind, since, limitOr(limit, 1000))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Document, error) {
		var d Document
		return d, scanDoc(r, &d)
	})
}

func (s *Store) RecentDocuments(ctx context.Context, courseID int64, kind string, limit int) ([]Document, error) {
	rows, err := s.db.Query(ctx, `SELECT `+docCols+`, left(d.body, 800) FROM documents d JOIN courses c ON c.id = d.course_id
		WHERE d.course_id = $1 AND d.kind = $2 ORDER BY d.updated_at DESC NULLS LAST LIMIT $3`, courseID, kind, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Document, error) {
		var d Document
		return d, scanDoc(r, &d, &d.Body)
	})
}

// Search uses websearch_to_tsquery so stuff like `"linked list" -array` works
// and random input like C++ can't break the query.
func (s *Store) Search(ctx context.Context, q string, courseID int64, limit int) ([]Document, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+docCols+`,
			ts_headline('english', left(d.body, 100000), query, 'MaxFragments=2, MaxWords=30, MinWords=10, StartSel=[, StopSel=]')
		FROM documents d JOIN courses c ON c.id = d.course_id, websearch_to_tsquery('english', $1) query
		WHERE d.search @@ query AND ($2::bigint = 0 OR d.course_id = $2)
		ORDER BY ts_rank(d.search, query) DESC LIMIT $3`, q, courseID, limitOr(limit, 10))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Document, error) {
		var d Document
		return d, scanDoc(r, &d, &d.Snippet)
	})
}

// ---- calendar ----

type CalendarItem struct {
	ID          string     `json:"id"`
	CourseID    int64      `json:"course_id,omitempty"`
	Course      string     `json:"course,omitempty"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	StartAt     *time.Time `json:"start_at"`
	EndAt       *time.Time `json:"end_at,omitempty"`
	AllDay      bool       `json:"all_day"`
	IsDeadline  bool       `json:"is_deadline"`
	Status      string     `json:"status,omitempty"`
	Points      *float64   `json:"points,omitempty"`
	Score       *float64   `json:"score,omitempty"`
	Location    string     `json:"location,omitempty"`
	URL         string     `json:"url,omitempty"`
	Description string     `json:"description,omitempty"`
}

const calCols = `k.id, coalesce(k.course_id, 0), coalesce(c.code, ''), k.type, k.title, k.start_at, k.end_at,
	k.all_day, k.is_deadline, k.status, k.points, k.score, k.location, k.url, k.description`

func scanCal(r pgx.Row) (CalendarItem, error) {
	var k CalendarItem
	err := r.Scan(&k.ID, &k.CourseID, &k.Course, &k.Type, &k.Title, &k.StartAt, &k.EndAt, &k.AllDay,
		&k.IsDeadline, &k.Status, &k.Points, &k.Score, &k.Location, &k.URL, &k.Description)
	return k, err
}

func (s *Store) CalendarItem(ctx context.Context, id string) (*CalendarItem, error) {
	k, err := scanCal(s.db.QueryRow(ctx, `SELECT `+calCols+` FROM calendar_items k
		LEFT JOIN courses c ON c.id = k.course_id WHERE k.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func (s *Store) UpsertCalendarItem(ctx context.Context, k CalendarItem) error {
	var course *int64
	if k.CourseID != 0 {
		course = &k.CourseID
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO calendar_items (id, course_id, type, title, start_at, end_at, all_day, is_deadline,
			status, points, score, location, url, description)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (id) DO UPDATE SET
			course_id = $2, type = $3, title = $4, start_at = $5, end_at = $6, all_day = $7,
			is_deadline = $8, status = $9, points = $10, score = $11, location = $12, url = $13,
			description = $14,
			changed_at = CASE WHEN (calendar_items.start_at, calendar_items.end_at, calendar_items.status,
				calendar_items.title) IS DISTINCT FROM ($5, $6, $9, $4) THEN now() ELSE calendar_items.changed_at END`,
		k.ID, course, k.Type, k.Title, k.StartAt, k.EndAt, k.AllDay, k.IsDeadline, k.Status, k.Points,
		k.Score, k.Location, k.URL, k.Description)
	return err
}

// CalendarIDs gives the ids for one course that start with prefix, e.g.
// "event:". Used to notice when something was deleted on canvas.
func (s *Store) CalendarIDs(ctx context.Context, courseID int64, prefix string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM calendar_items WHERE course_id = $1 AND starts_with(id, $2)`,
		courseID, prefix)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (s *Store) DeleteCalendarItem(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM calendar_items WHERE id = $1`, id)
	return err
}

type CalendarFilter struct {
	Start, End    time.Time // zero means open ended
	CourseID      int64
	DeadlinesOnly bool
	Statuses      []string
	Limit         int
}

func (s *Store) Calendar(ctx context.Context, f CalendarFilter) ([]CalendarItem, error) {
	sql := `SELECT ` + calCols + ` FROM calendar_items k LEFT JOIN courses c ON c.id = k.course_id
		WHERE k.start_at IS NOT NULL`
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if !f.Start.IsZero() {
		sql += " AND k.start_at >= " + arg(f.Start)
	}
	if !f.End.IsZero() {
		sql += " AND k.start_at < " + arg(f.End)
	}
	if f.CourseID != 0 {
		sql += " AND k.course_id = " + arg(f.CourseID)
	}
	if f.DeadlinesOnly {
		sql += " AND k.is_deadline"
	}
	if len(f.Statuses) > 0 {
		sql += " AND k.status = ANY(" + arg(f.Statuses) + ")"
	}
	sql += " ORDER BY k.start_at, k.title LIMIT " + arg(limitOr(f.Limit, 2000))
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CalendarItem, error) { return scanCal(r) })
}

// ---- changes ----

type Change struct {
	ID         int64     `json:"id"`
	DetectedAt time.Time `json:"detected_at"`
	Kind       string    `json:"kind"`
	ItemID     string    `json:"item_id"`
	CourseID   int64     `json:"course_id,omitempty"`
	Course     string    `json:"course,omitempty"`
	Title      string    `json:"title"`
	Old        string    `json:"old,omitempty"`
	New        string    `json:"new,omitempty"`
	URL        string    `json:"url,omitempty"`
	DedupeKey  string    `json:"-"`
}

// AddChange saves a change and fills in its id and time. If it has a dedupe
// key that was already used it's skipped and added is false.
func (s *Store) AddChange(ctx context.Context, ch *Change) (added bool, err error) {
	var course *int64
	if ch.CourseID != 0 {
		course = &ch.CourseID
	}
	var key *string
	if ch.DedupeKey != "" {
		key = &ch.DedupeKey
	}
	err = s.db.QueryRow(ctx, `
		INSERT INTO changes (kind, item_id, course_id, title, old_value, new_value, url, dedupe_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (dedupe_key) DO NOTHING
		RETURNING id, detected_at`,
		ch.Kind, ch.ItemID, course, ch.Title, ch.Old, ch.New, ch.URL, key).Scan(&ch.ID, &ch.DetectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

type ChangeFilter struct {
	Since    time.Time
	AfterID  int64
	Kinds    []string
	CourseID int64
	Limit    int
}

func (s *Store) Changes(ctx context.Context, f ChangeFilter) ([]Change, error) {
	rows, err := s.db.Query(ctx, `
		SELECT h.id, h.detected_at, h.kind, h.item_id, coalesce(h.course_id, 0), coalesce(c.code, ''),
			h.title, h.old_value, h.new_value, h.url
		FROM changes h LEFT JOIN courses c ON c.id = h.course_id
		WHERE h.detected_at > $1 AND h.id > $2 AND (coalesce(cardinality($3::text[]), 0) = 0 OR h.kind = ANY($3))
			AND ($4::bigint = 0 OR h.course_id = $4)
		ORDER BY h.id LIMIT $5`, f.Since, f.AfterID, f.Kinds, f.CourseID, limitOr(f.Limit, 500))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Change, error) {
		var h Change
		return h, r.Scan(&h.ID, &h.DetectedAt, &h.Kind, &h.ItemID, &h.CourseID, &h.Course, &h.Title, &h.Old, &h.New, &h.URL)
	})
}

// ---- sync runs ----

type SyncRun struct {
	ID         int64           `json:"id"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at"`
	OK         *bool           `json:"ok"`
	Error      string          `json:"error,omitempty"`
	Stats      json.RawMessage `json:"stats,omitempty"`
	Changes    int             `json:"changes"`
}

func (s *Store) StartRun(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO sync_runs DEFAULT VALUES RETURNING id`).Scan(&id)
	return id, err
}

func (s *Store) FinishRun(ctx context.Context, id int64, runErr error, stats any, changes int) error {
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
	}
	b, _ := json.Marshal(stats)
	_, err := s.db.Exec(ctx, `UPDATE sync_runs SET finished_at = now(), ok = $2, error = $3, stats = $4, changes = $5
		WHERE id = $1`, id, runErr == nil, msg, b, changes)
	return err
}

// HasCompletedRun is false on the very first sync. That one just fills the
// db, otherwise every file in every course would show up as "new".
func (s *Store) HasCompletedRun(ctx context.Context) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sync_runs WHERE ok)`).Scan(&ok)
	return ok, err
}

func (s *Store) Runs(ctx context.Context, limit int) ([]SyncRun, error) {
	rows, err := s.db.Query(ctx, `SELECT id, started_at, finished_at, ok, error, stats, changes
		FROM sync_runs ORDER BY id DESC LIMIT $1`, limitOr(limit, 10))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SyncRun, error) {
		var x SyncRun
		return x, r.Scan(&x.ID, &x.StartedAt, &x.FinishedAt, &x.OK, &x.Error, &x.Stats, &x.Changes)
	})
}

func limitOr(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

// ResetForTests empties every table. Only the tests call this.
func (s *Store) ResetForTests(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `TRUNCATE courses, documents, calendar_items, changes, sync_runs RESTART IDENTITY CASCADE`)
	return err
}
