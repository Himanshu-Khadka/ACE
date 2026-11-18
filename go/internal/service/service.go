// Package service has the queries shared by the REST api and the MCP server.
package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Himanshu-Khadka/ACE/go/internal/store"
)

type Service struct {
	Store *store.Store
	Loc   *time.Location // for dates without a time, e.g. "2026-11-20"
	Now   func() time.Time
}

func New(st *store.Store, loc *time.Location) *Service {
	return &Service{Store: st, Loc: loc, Now: time.Now}
}

var notDone = []string{"todo", "missing"}

// ErrBadInput is for things like a date we can't read (the api turns it into a 400).
var ErrBadInput = errors.New("bad input")

// Deadlines is upcoming work, soonest first. Done stuff is left out unless
// includeDone is set.
func (s *Service) Deadlines(ctx context.Context, days float64, course string, includeDone bool) ([]store.CalendarItem, error) {
	cid, err := s.Store.ResolveCourse(ctx, course)
	if err != nil {
		return nil, err
	}
	if days <= 0 {
		days = 14
	}
	now := s.Now()
	f := store.CalendarFilter{Start: now, End: now.Add(time.Duration(days * 24 * float64(time.Hour))),
		CourseID: cid, DeadlinesOnly: true}
	if !includeDone {
		f.Statuses = notDone
	}
	return nonNil(s.Store.Calendar(ctx, f))
}

// Missing is past due and not handed in. Status is only worked out during a
// sync, so something that went past due since then still says "todo".
func (s *Service) Missing(ctx context.Context, course string) ([]store.CalendarItem, error) {
	cid, err := s.Store.ResolveCourse(ctx, course)
	if err != nil {
		return nil, err
	}
	items, err := s.Store.Calendar(ctx, store.CalendarFilter{End: s.Now(), CourseID: cid,
		DeadlinesOnly: true, Statuses: notDone})
	for i := range items {
		items[i].Status = "missing"
	}
	return nonNil(items, err)
}

// Calendar is deadlines and events between start and end. Both are optional,
// the default is the next 30 days.
func (s *Service) Calendar(ctx context.Context, start, end, course string) ([]store.CalendarItem, error) {
	cid, err := s.Store.ResolveCourse(ctx, course)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	from, to := now, now.AddDate(0, 0, 30)
	if start != "" {
		if from, err = s.ParseTime(start); err != nil {
			return nil, err
		}
		if end == "" {
			to = from.AddDate(0, 0, 30)
		}
	}
	if end != "" {
		if to, err = s.ParseTime(end); err != nil {
			return nil, err
		}
		if !strings.Contains(end, "T") {
			to = to.AddDate(0, 0, 1) // end date is inclusive
		}
	}
	return nonNil(s.Store.Calendar(ctx, store.CalendarFilter{Start: from, End: to, CourseID: cid}))
}

// Changes since a time. since can be a timestamp, a date or a duration like
// "24h" (meaning 24 hours ago). afterID is easier for an agent that polls:
// remember the last id you saw and pass it next time.
func (s *Service) Changes(ctx context.Context, since string, afterID int64, kinds []string, course string, limit int) ([]store.Change, error) {
	cid, err := s.Store.ResolveCourse(ctx, course)
	if err != nil {
		return nil, err
	}
	f := store.ChangeFilter{AfterID: afterID, Kinds: kinds, CourseID: cid, Limit: limit}
	if since != "" {
		if f.Since, err = s.ParseTime(since); err != nil {
			return nil, err
		}
	} else if afterID == 0 {
		f.Since = s.Now().Add(-7 * 24 * time.Hour)
	}
	return nonNil(s.Store.Changes(ctx, f))
}

type Module struct {
	Title string `json:"title"`
	Items string `json:"items"`
}

type Overview struct {
	Course        *store.Course        `json:"course"`
	Syllabus      string               `json:"syllabus"`
	Modules       []Module             `json:"modules"`
	Upcoming      []store.CalendarItem `json:"upcoming"`
	Missing       []store.CalendarItem `json:"missing"`
	Announcements []store.Document     `json:"recent_announcements"`
}

// Overview is everything useful about one course in one response.
func (s *Service) Overview(ctx context.Context, course string) (*Overview, error) {
	if strings.TrimSpace(course) == "" {
		return nil, fmt.Errorf("%w: course is required", store.ErrNotFound)
	}
	cid, err := s.Store.ResolveCourse(ctx, course)
	if err != nil {
		return nil, err
	}
	o := &Overview{}
	if o.Course, err = s.Store.Course(ctx, cid); err != nil {
		return nil, fmt.Errorf("course %q hasn't been synced: %w", course, err)
	}
	if syl, err := s.Store.Document(ctx, fmt.Sprintf("syllabus:%d", cid)); err == nil {
		o.Syllabus = cut(syl.Body, 6000)
	}
	mods, err := s.Store.Documents(ctx, cid, "module", time.Time{}, 0)
	if err != nil {
		return nil, err
	}
	sort.Slice(mods, func(i, j int) bool { return idNum(mods[i].ID) < idNum(mods[j].ID) })
	o.Modules = []Module{}
	for _, m := range mods {
		full, err := s.Store.Document(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		o.Modules = append(o.Modules, Module{Title: m.Title, Items: full.Body})
	}
	if o.Upcoming, err = s.Deadlines(ctx, 21, course, false); err != nil {
		return nil, err
	}
	if o.Missing, err = s.Missing(ctx, course); err != nil {
		return nil, err
	}
	if o.Announcements, err = nonNil(s.Store.RecentDocuments(ctx, cid, "announcement", 5)); err != nil {
		return nil, err
	}
	return o, nil
}

// ParseTime accepts RFC3339, "2026-11-20" (midnight local time) or a
// duration like "24h" / "90m" meaning that long ago.
func (s *Service) ParseTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", v, s.Loc); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(strings.TrimPrefix(v, "-")); err == nil {
		return s.Now().Add(-d), nil
	}
	if strings.HasSuffix(v, "d") { // "7d", time.ParseDuration doesn't do days
		if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(v, "-"), "d")); err == nil {
			return s.Now().AddDate(0, 0, -n), nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: can't read time %q, use 2026-11-20, 2026-11-20T15:00:00Z or 24h", ErrBadInput, v)
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func idNum(id string) int64 {
	n, _ := strconv.ParseInt(id[strings.LastIndex(id, ":")+1:], 10, 64)
	return n
}

// so the json is [] instead of null
func nonNil[T any](xs []T, err error) ([]T, error) {
	if xs == nil {
		xs = []T{}
	}
	return xs, err
}
