// Package ics writes calendar items as an iCalendar file. Google Calendar,
// Outlook and Apple Calendar can all import it or subscribe to the url.
package ics

import (
	"fmt"
	"strings"
	"time"

	"github.com/Himanshu-Khadka/ACE/go/internal/store"
)

func esc(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`, "\r", "")
	return r.Replace(s)
}

// fold splits lines longer than 75 bytes, continuation lines start with a
// space. Never splits in the middle of a utf-8 character.
func fold(line string) string {
	var b strings.Builder
	n := 0
	for _, r := range line {
		size := len(string(r))
		if n+size > 74 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += size
	}
	return b.String()
}

func stamp(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

func done(status string) bool {
	return status == "submitted" || status == "graded" || status == "excused"
}

// Write turns calendar items into an .ics file. Deadlines show up as a 30 min
// block ending at the due time with a reminder the day before (unless they're
// already done). loc decides which day all-day events land on.
func Write(items []store.CalendarItem, name string, loc *time.Location, now time.Time) string {
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//ACE//Canvas export//EN",
		"CALSCALE:GREGORIAN", "METHOD:PUBLISH", "X-WR-CALNAME:" + esc(name),
		// how often subscribed calendars should check for updates
		"REFRESH-INTERVAL;VALUE=DURATION:PT1H", "X-PUBLISHED-TTL:PT1H"}
	for _, it := range items {
		if it.StartAt == nil {
			continue
		}
		start := *it.StartAt
		prefix := ""
		if it.Course != "" {
			prefix = "[" + it.Course + "] "
		}
		var summary string
		var from, to time.Time
		if it.IsDeadline {
			mark := ""
			if done(it.Status) {
				mark = "(done) "
			}
			summary = fmt.Sprintf("%s%sDue: %s", prefix, mark, it.Title)
			from, to = start.Add(-30*time.Minute), start
		} else {
			summary = prefix + it.Title
			from, to = start, start.Add(time.Hour)
			if it.EndAt != nil && it.EndAt.After(start) {
				to = *it.EndAt
			}
		}
		desc := it.URL
		if it.Description != "" {
			desc = strings.TrimSpace(desc + "\n" + cut(it.Description, 500))
		}
		ev := []string{"BEGIN:VEVENT", "UID:" + strings.ReplaceAll(it.ID, ":", "-") + "@ace", "DTSTAMP:" + stamp(now)}
		if it.AllDay {
			day := start.In(loc)
			ev = append(ev, "DTSTART;VALUE=DATE:"+day.Format("20060102"),
				"DTEND;VALUE=DATE:"+day.AddDate(0, 0, 1).Format("20060102"))
		} else {
			ev = append(ev, "DTSTART:"+stamp(from), "DTEND:"+stamp(to))
		}
		ev = append(ev, "SUMMARY:"+esc(summary), "DESCRIPTION:"+esc(desc))
		if it.Location != "" {
			ev = append(ev, "LOCATION:"+esc(it.Location))
		}
		if it.URL != "" {
			ev = append(ev, "URL:"+it.URL)
		}
		if it.IsDeadline && !done(it.Status) && it.Status != "none" {
			ev = append(ev, "BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-P1D", "DESCRIPTION:"+esc(summary), "END:VALARM")
		}
		lines = append(lines, append(ev, "END:VEVENT")...)
	}
	lines = append(lines, "END:VCALENDAR")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(fold(l))
		b.WriteString("\r\n")
	}
	return b.String()
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
