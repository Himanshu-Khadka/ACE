// Package canvas is a small client for the Canvas LMS REST API.
//
// Canvas paginates with the Link header, so list calls keep following
// rel="next" until there are no pages left.
package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	token   string
	http    *http.Client
}

// StatusError is returned for non 2xx responses so callers can tell a hidden
// tab (403/404) apart from a real failure.
type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string { return fmt.Sprintf("canvas: HTTP %d for %s", e.Code, e.URL) }

// IsNoAccess is true when canvas says we're not allowed to see something,
// which happens a lot when a prof hides the Files or Pages tab.
func IsNoAccess(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code == 401 || se.Code == 403 || se.Code == 404)
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 2 * time.Minute},
	}
}

func (c *Client) get(ctx context.Context, rawURL string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 300 {
			return resp, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		// canvas sends 403 "Rate Limit Exceeded" when it throttles you
		throttled := resp.StatusCode == 429 ||
			(resp.StatusCode == 403 && strings.Contains(string(body), "Rate Limit"))
		if throttled && attempt < 4 {
			select {
			case <-time.After(time.Duration(1<<attempt) * time.Second):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, &StatusError{Code: resp.StatusCode, URL: rawURL}
	}
}

func (c *Client) apiURL(path string, params url.Values) string {
	u := c.BaseURL + "/api/v1" + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return u
}

func (c *Client) getJSON(ctx context.Context, path string, params url.Values, out any) error {
	resp, err := c.get(ctx, c.apiURL(path, params))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// list fetches every page of a list endpoint.
func list[T any](ctx context.Context, c *Client, path string, params url.Values) ([]T, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("per_page", "100")
	next := c.apiURL(path, params)
	var all []T
	for next != "" {
		resp, err := c.get(ctx, next)
		if err != nil {
			return all, err
		}
		var page []T
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return all, fmt.Errorf("decoding %s: %w", path, err)
		}
		all = append(all, page...)
		next = ""
		if m := nextLink.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
			next = m[1]
		}
	}
	return all, nil
}

func (c *Client) Courses(ctx context.Context) ([]Course, error) {
	return list[Course](ctx, c, "/courses", url.Values{
		"enrollment_state": {"active"},
		"include[]":        {"term"},
	})
}

func (c *Client) Modules(ctx context.Context, courseID int64) ([]Module, error) {
	return list[Module](ctx, c, fmt.Sprintf("/courses/%d/modules", courseID), url.Values{"include[]": {"items"}})
}

func (c *Client) ModuleItems(ctx context.Context, courseID, moduleID int64) ([]ModuleItem, error) {
	return list[ModuleItem](ctx, c, fmt.Sprintf("/courses/%d/modules/%d/items", courseID, moduleID), nil)
}

func (c *Client) Files(ctx context.Context, courseID int64) ([]File, error) {
	return list[File](ctx, c, fmt.Sprintf("/courses/%d/files", courseID), nil)
}

func (c *Client) File(ctx context.Context, fileID int64) (File, error) {
	var f File
	err := c.getJSON(ctx, fmt.Sprintf("/files/%d", fileID), nil, &f)
	return f, err
}

func (c *Client) Syllabus(ctx context.Context, courseID int64) (string, error) {
	var out struct {
		SyllabusBody string `json:"syllabus_body"`
	}
	err := c.getJSON(ctx, fmt.Sprintf("/courses/%d", courseID), url.Values{"include[]": {"syllabus_body"}}, &out)
	return out.SyllabusBody, err
}

func (c *Client) Pages(ctx context.Context, courseID int64) ([]Page, error) {
	return list[Page](ctx, c, fmt.Sprintf("/courses/%d/pages", courseID), nil)
}

// Page gets one page with its body (the list endpoint leaves the body out).
func (c *Client) Page(ctx context.Context, courseID int64, pageURL string) (Page, error) {
	var p Page
	err := c.getJSON(ctx, fmt.Sprintf("/courses/%d/pages/%s", courseID, url.PathEscape(pageURL)), nil, &p)
	return p, err
}

func (c *Client) Assignments(ctx context.Context, courseID int64) ([]Assignment, error) {
	// include[]=submission is what tells us if it's submitted / graded / missing
	return list[Assignment](ctx, c, fmt.Sprintf("/courses/%d/assignments", courseID), url.Values{"include[]": {"submission"}})
}

func (c *Client) Events(ctx context.Context, courseID int64) ([]Event, error) {
	return list[Event](ctx, c, "/calendar_events", url.Values{
		"context_codes[]": {fmt.Sprintf("course_%d", courseID)},
		"all_events":      {"true"},
		"type":            {"event"},
	})
}

func (c *Client) Announcements(ctx context.Context, courseID int64) ([]Announcement, error) {
	// without start_date canvas only gives the last 14 days
	return list[Announcement](ctx, c, "/announcements", url.Values{
		"context_codes[]": {fmt.Sprintf("course_%d", courseID)},
		"start_date":      {"2000-01-01"},
	})
}

func (c *Client) PlannerItems(ctx context.Context, start, end time.Time) ([]PlannerItem, error) {
	return list[PlannerItem](ctx, c, "/planner/items", url.Values{
		"start_date": {start.Format("2006-01-02")},
		"end_date":   {end.Format("2006-01-02")},
	})
}

// Download saves a file to dest. It writes to a .part file first so a
// failed download never leaves a half written file behind.
func (c *Client) Download(ctx context.Context, fileURL, dest string) error {
	resp, err := c.get(ctx, fileURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
