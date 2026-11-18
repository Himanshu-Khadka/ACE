"""Queries shared by the CLI, the REST API and the MCP server."""
from __future__ import annotations

from .deadlines import now_iso
from .storage import Store

DONE = ["submitted", "graded", "excused", "none"]
NOT_DONE = ["todo", "missing"]


def _day(date: str | None) -> str | None:
    # accept "2026-11-20" or a full timestamp
    if not date:
        return None
    return date if "T" in date else f"{date}T00:00:00Z"


def deadlines(store: Store, days: float = 14, course: str | int | None = None, include_done: bool = False) -> list[dict]:
    return store.calendar_items(
        start=now_iso(), end=now_iso(days), course_id=store.resolve_course(course),
        deadlines_only=True, statuses=None if include_done else NOT_DONE,
    )


def missing(store: Store, course: str | int | None = None) -> list[dict]:
    """Past due and not submitted."""
    items = store.calendar_items(end=now_iso(), course_id=store.resolve_course(course),
                                 deadlines_only=True, statuses=["missing", "todo"])
    for it in items:
        it["status"] = "missing"
    return items


def calendar(store: Store, start: str | None = None, end: str | None = None,
             course: str | int | None = None) -> list[dict]:
    return store.calendar_items(start=_day(start) or now_iso(), end=_day(end) or now_iso(30),
                                course_id=store.resolve_course(course))


def overview(store: Store, course: str | int) -> dict:
    """Everything useful about one course on a single page."""
    cid = store.resolve_course(course)
    info = next((c for c in store.list_courses() if c["id"] == cid), None)
    if info is None:
        raise LookupError(f"course {course!r} hasn't been synced")
    syllabus = store.get_document(f"syllabus:{cid}")
    modules = sorted(store.list_documents(cid, "module"), key=lambda m: int(m["id"].split(":")[1]))
    return {
        "course": info,
        "syllabus": (syllabus or {}).get("body", "")[:6000],
        "modules": [
            {"title": m["title"], "items": (store.get_document(m["id"]) or {}).get("body", "")}
            for m in modules
        ],
        "upcoming": deadlines(store, 21, cid),
        "missing": missing(store, cid),
        "recent_announcements": [
            {"title": a["title"], "date": a["updated_at"], "text": a["body"][:800]}
            for a in store.recent_documents(cid, "announcement", 5)
        ],
    }
