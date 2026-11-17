"""Pulls course content from Canvas into the local db.

Items are skipped if their updated_at hasn't changed, so re-running is fast.
"""
from __future__ import annotations

import logging
import re
from pathlib import Path

import httpx

from .canvas_client import CanvasClient
from .extract import file_to_text, html_to_text
from .storage import Document, Store

log = logging.getLogger(__name__)
MAX_FILE_BYTES = 200 * 1024 * 1024  # skip huge stuff like lecture recordings


def _safe(name: str) -> str:
    return re.sub(r'[<>:"/\\|?*\x00-\x1f]', "_", name).strip() or "untitled"


def sync_all(client: CanvasClient, store: Store, files_dir: Path, course_filter: list[int] | None = None) -> dict:
    stats: dict[str, int] = {}
    for course in client.courses():
        if "name" not in course:  # some old courses come back without any details
            continue
        cid = course["id"]
        if course_filter and cid not in course_filter:
            continue
        store.upsert_course(cid, course["name"], course.get("course_code"), (course.get("term") or {}).get("name"))
        log.info("Syncing %s", course["name"])
        course_dir = files_dir / _safe(course.get("course_code") or str(cid))
        for kind, fn in [
            ("file", lambda: _sync_files(client, store, cid, course_dir)),
            ("page", lambda: _sync_pages(client, store, cid)),
            ("assignment", lambda: _sync_assignments(client, store, cid)),
            ("announcement", lambda: _sync_announcements(client, store, cid)),
            ("module", lambda: _sync_modules(client, store, cid)),
        ]:
            try:
                stats[kind] = stats.get(kind, 0) + fn()
            except httpx.HTTPStatusError as exc:
                # profs often hide tabs like Files, which gives 401/403/404
                log.warning("  skipped %ss: HTTP %s", kind, exc.response.status_code)
        store.commit()
    return stats


def _sync_files(client: CanvasClient, store: Store, cid: int, course_dir: Path) -> int:
    n = 0
    for f in client.files(cid):
        doc_id = f"file:{f['id']}"
        if store.updated_at(doc_id) == f.get("updated_at"):
            continue
        if not f.get("url") or f.get("locked_for_user") or f.get("size", 0) > MAX_FILE_BYTES:
            continue
        dest = course_dir / f"{f['id']}_{_safe(f['display_name'])}"
        client.download(f["url"], dest)
        store.upsert_document(Document(
            id=doc_id, course_id=cid, kind="file", title=f["display_name"],
            url=f.get("url"), local_path=str(dest), updated_at=f.get("updated_at"),
            body=file_to_text(dest),
        ))
        n += 1
    return n


def _sync_pages(client: CanvasClient, store: Store, cid: int) -> int:
    n = 0
    for p in client.pages(cid):
        doc_id = f"page:{p['page_id']}"
        if store.updated_at(doc_id) == p.get("updated_at"):
            continue
        full = client.page(cid, p["url"])  # list endpoint omits the body
        store.upsert_document(Document(
            id=doc_id, course_id=cid, kind="page", title=p["title"], url=p.get("html_url"),
            updated_at=p.get("updated_at"), body=html_to_text(full.get("body")),
        ))
        n += 1
    return n


def _sync_assignments(client: CanvasClient, store: Store, cid: int) -> int:
    n = 0
    for a in client.assignments(cid):
        doc_id = f"assignment:{a['id']}"
        if store.updated_at(doc_id) == a.get("updated_at"):
            continue
        store.upsert_document(Document(
            id=doc_id, course_id=cid, kind="assignment", title=a["name"], url=a.get("html_url"),
            updated_at=a.get("updated_at"), due_at=a.get("due_at"),
            body=html_to_text(a.get("description")),
        ))
        n += 1
    return n


def _sync_announcements(client: CanvasClient, store: Store, cid: int) -> int:
    n = 0
    for a in client.announcements(cid):
        doc_id = f"announcement:{a['id']}"
        stamp = a.get("last_reply_at") or a.get("posted_at")
        if store.updated_at(doc_id) == stamp:
            continue
        store.upsert_document(Document(
            id=doc_id, course_id=cid, kind="announcement", title=a["title"], url=a.get("html_url"),
            updated_at=stamp, body=html_to_text(a.get("message")),
        ))
        n += 1
    return n


def _sync_modules(client: CanvasClient, store: Store, cid: int) -> int:
    # store modules as a simple outline of the course
    n = 0
    for m in client.modules(cid):
        lines = [f"- {it.get('title')} ({it.get('type')})" for it in m.get("items", [])]
        store.upsert_document(Document(
            id=f"module:{m['id']}", course_id=cid, kind="module", title=m["name"],
            body="\n".join(lines),
        ))
        n += 1
    return n
