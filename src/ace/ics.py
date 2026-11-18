"""Export calendar items as an .ics file (works with Google Calendar, Outlook, Apple)."""
from __future__ import annotations

from datetime import datetime, timedelta, timezone


def _esc(text: str) -> str:
    return text.replace("\\", "\\\\").replace(";", "\\;").replace(",", "\\,").replace("\n", "\\n")


def _fold(line: str) -> list[str]:
    # the spec says lines should be max 75 octets, continuation lines start with a space
    out, cur = [], ""
    for ch in line:
        if len((cur + ch).encode()) > 74:
            out.append(cur)
            cur = " " + ch
        else:
            cur += ch
    return out + [cur]


def _dt(iso: str) -> datetime:
    return datetime.fromisoformat(iso.replace("Z", "+00:00"))


def _fmt(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).strftime("%Y%m%dT%H%M%SZ")


def to_ics(items: list[dict], name: str = "ACE") -> str:
    stamp = _fmt(datetime.now(timezone.utc))
    lines = ["BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//ACE//Canvas export//EN",
             "CALSCALE:GREGORIAN", "METHOD:PUBLISH", f"X-WR-CALNAME:{_esc(name)}"]
    for it in items:
        if not it.get("start_at"):
            continue
        start = _dt(it["start_at"])
        course = f"[{it['course']}] " if it.get("course") else ""
        if it["is_deadline"]:
            done = it.get("status") in ("submitted", "graded", "excused")
            summary = f"{course}{'(done) ' if done else ''}Due: {it['title']}"
            # show deadlines as a 30 min block ending at the due time
            dtstart, dtend = _fmt(start - timedelta(minutes=30)), _fmt(start)
        else:
            summary = f"{course}{it['title']}"
            end = _dt(it["end_at"]) if it.get("end_at") else start + timedelta(hours=1)
            dtstart, dtend = _fmt(start), _fmt(end)

        desc = "\n".join(x for x in [it.get("url"), it.get("description", "")[:500]] if x)
        ev = ["BEGIN:VEVENT", f"UID:{it['id'].replace(':', '-')}@ace", f"DTSTAMP:{stamp}"]
        if it.get("all_day"):
            day = start.astimezone().date()
            ev += [f"DTSTART;VALUE=DATE:{day:%Y%m%d}", f"DTEND;VALUE=DATE:{day + timedelta(days=1):%Y%m%d}"]
        else:
            ev += [f"DTSTART:{dtstart}", f"DTEND:{dtend}"]
        ev += [f"SUMMARY:{_esc(summary)}", f"DESCRIPTION:{_esc(desc)}"]
        if it.get("location"):
            ev.append(f"LOCATION:{_esc(it['location'])}")
        if it.get("url"):
            ev.append(f"URL:{it['url']}")
        if it["is_deadline"] and it.get("status") in ("todo", "missing"):
            # reminder the day before
            ev += ["BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-P1D", f"DESCRIPTION:{_esc(summary)}", "END:VALARM"]
        ev.append("END:VEVENT")
        lines += ev
    lines.append("END:VCALENDAR")
    return "\r\n".join(part for line in lines for part in _fold(line)) + "\r\n"
