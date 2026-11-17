"""MCP server so Claude (or other assistants) can query the synced courses.

The docstrings on each tool are what the model reads to pick a tool.
"""
from __future__ import annotations

from datetime import datetime, timezone

from mcp.server.mcpserver import MCPServer

from .config import load_settings
from .storage import Store

mcp = MCPServer("ace")
_store: Store | None = None


def store() -> Store:
    global _store
    if _store is None:
        _store = Store(load_settings(require_token=False).db_path)
    return _store


@mcp.tool()
def list_courses() -> list[dict]:
    """List the user's synced Canvas courses with their ids, names and terms."""
    return store().list_courses()


@mcp.tool()
def search_course_content(query: str, course_id: int | None = None, limit: int = 10) -> list[dict]:
    """Full-text search across lecture files, pages, assignments, announcements
    and modules. Returns matching document ids with a short snippet. Use
    get_document to read the full text of a result."""
    return store().search(query, course_id, limit)


@mcp.tool()
def list_documents(course_id: int, kind: str | None = None) -> list[dict]:
    """List documents in a course. kind is one of: file, page, assignment,
    announcement, module."""
    return store().list_documents(course_id, kind)


@mcp.tool()
def get_document(doc_id: str, max_chars: int = 20000) -> dict:
    """Read the full extracted text of one document, e.g. 'file:123'."""
    doc = store().get_document(doc_id)
    if not doc:
        return {"error": f"no document {doc_id}"}
    if len(doc["body"]) > max_chars:
        doc["body"] = doc["body"][:max_chars] + "\n[truncated]"
    return doc


@mcp.tool()
def upcoming_deadlines(limit: int = 20) -> list[dict]:
    """Assignments due from now on, soonest first."""
    now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    return store().upcoming(now, limit)


def run() -> None:
    mcp.run()
