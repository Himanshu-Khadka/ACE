"""MCP server so Claude (or other assistants) can use my Canvas data to help me study.

The docstrings on each tool are what the model reads to pick a tool, so they
say what each one is for. Anywhere a tool takes `course` you can pass the code
("CMPUT 201"), part of the name, or the id.
"""
from __future__ import annotations

from datetime import datetime

from mcp.server.mcpserver import MCPServer

from . import service
from .config import load_settings
from .storage import Store

mcp = MCPServer(
    "ace",
    instructions=(
        "You have access to the student's University of Alberta Canvas courses: lecture files, "
        "pages, assignments, announcements, deadlines with submission status, and calendar events. "
        "Times are UTC; convert to the student's local time (America/Edmonton) when talking about them. "
        "When helping them study, ground explanations in their actual course material by searching "
        "it and citing the file or page you used."
    ),
)
_store: Store | None = None


def store() -> Store:
    global _store
    if _store is None:
        _store = Store(load_settings(require_token=False).db_path)
    return _store


def _safe(fn, *args):
    # return errors as data so the model can recover (e.g. ask which course)
    try:
        return fn(*args)
    except LookupError as exc:
        return {"error": str(exc), "courses": [c["code"] or c["name"] for c in store().list_courses()]}


# overview ----------------------------------------------------------------------
@mcp.tool()
def list_courses() -> list[dict]:
    """List the synced courses with their ids, codes, terms and how much content each has."""
    return store().list_courses()


@mcp.tool()
def course_overview(course: str) -> dict:
    """Syllabus, module outline, upcoming and missing work, and recent announcements for one
    course. Good starting point for any question about a course."""
    return _safe(service.overview, store(), course)


# deadlines / calendar -------------------------------------------------------------
@mcp.tool()
def upcoming_deadlines(days: int = 14, course: str | None = None, include_done: bool = False) -> list[dict] | dict:
    """Assignments, quizzes and discussions due in the next `days` days, soonest first.
    Each has a status: todo, missing, submitted, graded, excused or none (nothing to submit).
    Already-submitted work is hidden unless include_done is true."""
    return _safe(service.deadlines, store(), days, course, include_done)


@mcp.tool()
def missing_work(course: str | None = None) -> list[dict] | dict:
    """Past-due work that hasn't been submitted."""
    return _safe(service.missing, store(), course)


@mcp.tool()
def calendar(start_date: str | None = None, end_date: str | None = None, course: str | None = None) -> list[dict] | dict:
    """Deadlines plus calendar events (lectures, exams, office hours) between two dates
    (YYYY-MM-DD). Defaults to the next 30 days."""
    return _safe(service.calendar, store(), start_date, end_date, course)


# course material ---------------------------------------------------------------
@mcp.tool()
def search_course_content(query: str, course: str | None = None, limit: int = 10) -> list[dict] | dict:
    """Keyword search over lecture slides, PDFs, pages, assignments, announcements and modules.
    Every word must appear, so use 1-3 specific keywords. Use get_document to read a result."""
    return _safe(lambda: store().search(query, store().resolve_course(course), limit))


@mcp.tool()
def list_documents(course: str, kind: str | None = None) -> list[dict] | dict:
    """List what's in a course. kind is one of: file, page, assignment, announcement, module."""
    return _safe(lambda: store().list_documents(store().resolve_course(course), kind))


@mcp.tool()
def get_document(doc_id: str, offset: int = 0, max_chars: int = 15000) -> dict:
    """Read the text of a document (e.g. 'file:123'). Long files are returned in chunks:
    if `remaining` > 0, call again with offset = next_offset to keep reading."""
    doc = store().get_document(doc_id)
    if not doc:
        return {"error": f"no document {doc_id}"}
    body = doc.pop("body")
    doc["text"] = body[offset:offset + max_chars]
    doc["next_offset"] = offset + max_chars
    doc["remaining"] = max(0, len(body) - offset - max_chars)
    return doc


# study prompts (show up as slash commands in Claude Desktop) ----------------
@mcp.prompt()
def study_plan(days: str = "7") -> str:
    """Make a study plan for the next few days based on what's due."""
    today = datetime.now().strftime("%A %B %d")
    return (
        f"Today is {today}. Use upcoming_deadlines (days={days}, include_done=false), missing_work and "
        "calendar to see what I have coming up. Then make me a realistic day-by-day study plan: put "
        "missing and soon-due work first, split big assignments into chunks, and leave time before exams. "
        "For each block say which course material to review (search the course content for it)."
    )


@mcp.prompt()
def quiz_me(course: str, topic: str) -> str:
    """Quiz me on a topic using the actual course material."""
    return (
        f"Search the {course} course content for '{topic}' and read the most relevant lecture files. "
        "Then quiz me with 5 questions one at a time, mixing concept and practice questions in the style "
        "of the course. Wait for my answer before the next question, and when I get one wrong explain it "
        "using the lecture material and tell me which file/slide to review."
    )


@mcp.prompt()
def explain_assignment(course: str, assignment: str) -> str:
    """Break down an assignment and point to the material needed for it."""
    return (
        f"Find the assignment '{assignment}' in {course} (search_course_content, then get_document). "
        "Explain what it's asking in plain words, list the steps to do it, say when it's due and how many "
        "points it's worth, and find the lecture material that covers what I need. Don't write the "
        "solution for me."
    )


def run() -> None:
    mcp.run()
