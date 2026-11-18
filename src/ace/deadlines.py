"""Turns Canvas assignments / calendar events / planner items into CalendarItems.

The tricky part is the status. Canvas doesn't give one field for "did I do
this", so it's worked out from the submission object.
"""
from __future__ import annotations

from datetime import datetime, timedelta, timezone

from .extract import html_to_text
from .storage import CalendarItem

# assignments you can't submit anything for on Canvas
NO_SUBMISSION = {"none", "on_paper", "not_graded", "wiki_page"}


def iso(value: str | None) -> str | None:
    """Normalize Canvas timestamps to 2026-11-17T23:59:00Z so they sort/compare as strings."""
    if not value:
        return None
    dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def now_iso(offset_days: float = 0) -> str:
    return (datetime.now(timezone.utc) + timedelta(days=offset_days)).strftime("%Y-%m-%dT%H:%M:%SZ")


def submission_status(sub: dict | None, due_at: str | None, submission_types: list[str]) -> str:
    sub = sub or {}
    if sub.get("excused"):
        return "excused"
    if sub.get("workflow_state") == "graded" and sub.get("score") is not None:
        return "graded"
    if sub.get("submitted_at"):
        return "submitted"
    if submission_types and set(submission_types) <= NO_SUBMISSION:
        return "none"
    if sub.get("missing") or (due_at and due_at < now_iso()):
        return "missing"
    return "todo"


def assignment_type(a: dict) -> str:
    types = a.get("submission_types") or []
    if "online_quiz" in types or a.get("quiz_id") or a.get("is_quiz_assignment"):
        return "quiz"
    if "discussion_topic" in types:
        return "discussion"
    return "assignment"


def from_assignment(a: dict, course_id: int) -> CalendarItem:
    due = iso(a.get("due_at"))
    sub = a.get("submission") or {}
    return CalendarItem(
        id=f"assignment:{a['id']}", course_id=course_id, type=assignment_type(a), title=a["name"],
        start_at=due, is_deadline=True,
        status=submission_status(sub, due, a.get("submission_types") or []),
        points=a.get("points_possible"), score=sub.get("score"), url=a.get("html_url"),
        description=html_to_text(a.get("description"))[:2000],
    )


def from_event(e: dict, course_id: int) -> CalendarItem:
    return CalendarItem(
        id=f"event:{e['id']}", course_id=course_id, type="event", title=e["title"],
        start_at=iso(e.get("start_at")), end_at=iso(e.get("end_at")), all_day=bool(e.get("all_day")),
        location=e.get("location_name"), url=e.get("html_url"),
        description=html_to_text(e.get("description"))[:2000],
    )


PLANNER_TYPES = {"quiz": "quiz", "discussion_topic": "discussion", "wiki_page": "todo",
                 "planner_note": "todo", "assessment_request": "todo"}


def from_planner(p: dict, base_url: str) -> CalendarItem | None:
    """Only for things that aren't already an assignment or calendar event."""
    kind = PLANNER_TYPES.get(p.get("plannable_type"))
    if not kind:
        return None
    plannable = p.get("plannable") or {}
    subs = p.get("submissions") or {}
    if subs.get("excused"):
        status = "excused"
    elif subs.get("graded"):
        status = "graded"
    elif subs.get("submitted"):
        status = "submitted"
    elif subs.get("missing"):
        status = "missing"
    elif (p.get("planner_override") or {}).get("marked_complete"):
        status = "submitted"
    else:
        status = "todo"
    url = p.get("html_url")
    if url and url.startswith("/"):
        url = base_url + url
    return CalendarItem(
        id=f"planner:{p['plannable_type']}:{p['plannable_id']}", course_id=p.get("course_id"), type=kind,
        title=plannable.get("title") or "untitled", start_at=iso(p.get("plannable_date")),
        is_deadline=True, status=status, points=plannable.get("points_possible"), url=url,
        description=html_to_text(plannable.get("details"))[:2000],
    )
