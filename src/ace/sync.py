"""Pulls course content from Canvas into the local db.

Items are skipped if their updated_at hasn't changed, so re-running is fast.

A lot of profs hide the Files and Pages tabs, so listing them gives a 403/404
even though the files are still linked from modules, pages and assignments.
To get those we collect every file id / page we see while syncing the rest of
the course and fetch them one by one.
"""
from __future__ import annotations

import logging
import re
from dataclasses import dataclass, field
from pathlib import Path

import httpx

from .canvas_client import CanvasClient
from .deadlines import from_assignment, from_event, from_planner, now_iso
from .extract import file_to_text, html_to_text
from .storage import Document, Store

# each course gets its own folder:
#   data/courses/CMPUT 201/files/...
#   data/courses/CMPUT 201/assignments/Lab 1.md
FOLDERS = {"page": "pages", "assignment": "assignments", "announcement": "announcements", "module": "modules"}

log = logging.getLogger(__name__)
MAX_FILE_BYTES = 200 * 1024 * 1024  # skip huge stuff like lecture recordings
FILE_LINK = re.compile(r"/files/(\d+)")


def _safe(name: str) -> str:
    return re.sub(r'[<>:"/\\|?*\x00-\x1f]', "_", name).strip() or "untitled"


@dataclass
class CourseRefs:
    """File ids and page urls found while syncing other content."""
    file_ids: set[int] = field(default_factory=set)
    page_urls: set[str] = field(default_factory=set)

    def scan_html(self, html: str | None) -> None:
        if html:
            self.file_ids.update(int(i) for i in FILE_LINK.findall(html))


@dataclass
class Stats:
    synced: dict[str, int] = field(default_factory=dict)
    failed: dict[str, int] = field(default_factory=dict)

    def ok(self, kind: str) -> None:
        self.synced[kind] = self.synced.get(kind, 0) + 1

    def fail(self, kind: str) -> None:
        self.failed[kind] = self.failed.get(kind, 0) + 1


def sync_all(client: CanvasClient, store: Store, courses_dir: Path, course_filter: list[int] | None = None) -> Stats:
    stats = Stats()
    for course in client.courses():
        if "name" not in course:  # some old courses come back without any details
            continue
        cid = course["id"]
        if course_filter and cid not in course_filter:
            continue
        store.upsert_course(cid, course["name"], course.get("course_code"), (course.get("term") or {}).get("name"))
        log.info("Syncing %s", course["name"])
        course_dir = courses_dir / _safe(course.get("course_code") or course["name"])
        refs = CourseRefs()
        save = lambda doc: _save(store, course_dir, doc)  # noqa: E731

        # order matters: files and pages go last so they can use the refs found above
        _try(lambda: _sync_modules(client, store, save, cid, refs, stats), "modules")
        _try(lambda: _sync_syllabus(client, save, cid, refs, stats), "syllabus")
        _try(lambda: _sync_assignments(client, store, save, cid, refs, stats), "assignments")
        _try(lambda: _sync_announcements(client, store, save, cid, refs, stats), "announcements")
        _try(lambda: _sync_events(client, store, cid, stats), "calendar")
        _sync_pages(client, store, save, cid, refs, stats)
        _sync_files(client, store, cid, course_dir / "files", refs, stats)
        store.commit()
    _try(lambda: _sync_planner(client, store, stats), "planner")
    store.commit()
    return stats


def _save(store: Store, course_dir: Path, doc: Document) -> None:
    """Save to the db and also write a .md copy into the course folder."""
    folder = course_dir / FOLDERS.get(doc.kind, "pages")
    folder.mkdir(parents=True, exist_ok=True)
    name = doc.title
    if doc.kind == "announcement" and doc.updated_at:
        name = f"{doc.updated_at[:10]} {name}"  # date first so they sort nicely
    path = folder / f"{_safe(name)[:150]}.md"
    header = [f"# {doc.title}"]
    if doc.due_at:
        header.append(f"Due: {doc.due_at}")
    if doc.url:
        header.append(f"Link: {doc.url}")
    path.write_text("\n\n".join(header) + "\n\n" + doc.body + "\n", encoding="utf-8")
    doc.local_path = str(path)
    store.upsert_document(doc)


def _try(fn, what: str) -> None:
    try:
        fn()
    except httpx.HTTPStatusError as exc:
        # tab is hidden/disabled for students, nothing we can do
        log.info("  no access to %s (HTTP %s)", what, exc.response.status_code)


def _list_or_empty(gen) -> list[dict]:
    try:
        return list(gen)
    except httpx.HTTPStatusError:
        return []


def _sync_modules(client, store, save, cid, refs, stats) -> None:
    for m in client.modules(cid):
        # canvas leaves out "items" when a module has a lot of them
        items = m.get("items")
        if items is None:
            items = _list_or_empty(client.module_items(cid, m["id"]))
        for it in items:
            if it.get("type") == "File" and it.get("content_id"):
                refs.file_ids.add(it["content_id"])
            elif it.get("type") == "Page" and it.get("page_url"):
                refs.page_urls.add(it["page_url"])
        lines = [f"- {it.get('title')} ({it.get('type')})" for it in items]
        save(Document(
            id=f"module:{m['id']}", course_id=cid, kind="module", title=m["name"],
            body="\n".join(lines),
        ))
        stats.ok("module")


def _sync_syllabus(client, save, cid, refs, stats) -> None:
    html = client.syllabus(cid)
    if not html:
        return
    refs.scan_html(html)
    save(Document(
        id=f"syllabus:{cid}", course_id=cid, kind="page", title="Syllabus",
        body=html_to_text(html),
    ))
    stats.ok("syllabus")


def _sync_assignments(client, store, save, cid, refs, stats) -> None:
    for a in client.assignments(cid):
        refs.scan_html(a.get("description"))
        # always update this one: submitting doesn't change the assignment's updated_at
        store.upsert_calendar(from_assignment(a, cid))
        doc_id = f"assignment:{a['id']}"
        if store.updated_at(doc_id) == a.get("updated_at"):
            continue
        save(Document(
            id=doc_id, course_id=cid, kind="assignment", title=a["name"], url=a.get("html_url"),
            updated_at=a.get("updated_at"), due_at=a.get("due_at"),
            body=html_to_text(a.get("description")),
        ))
        stats.ok("assignment")


def _sync_events(client, store, cid, stats) -> None:
    for e in client.calendar_events(cid):
        store.upsert_calendar(from_event(e, cid))
        stats.ok("event")


def _sync_planner(client, store, stats) -> None:
    # one call for all courses; only adds what isn't already an assignment/event
    for p in client.planner_items(now_iso(-120)[:10], now_iso(365)[:10]):
        assignment_id = (p.get("plannable") or {}).get("assignment_id")
        if p.get("plannable_type") in ("assignment", "calendar_event") or (
            assignment_id and store.has_calendar(f"assignment:{assignment_id}")
        ):
            continue
        item = from_planner(p, client.base_url)
        if item:
            store.upsert_calendar(item)
            stats.ok("todo")


def _sync_announcements(client, store, save, cid, refs, stats) -> None:
    for a in client.announcements(cid):
        refs.scan_html(a.get("message"))
        doc_id = f"announcement:{a['id']}"
        stamp = a.get("last_reply_at") or a.get("posted_at")
        if store.updated_at(doc_id) == stamp:
            continue
        save(Document(
            id=doc_id, course_id=cid, kind="announcement", title=a["title"], url=a.get("html_url"),
            updated_at=stamp, body=html_to_text(a.get("message")),
        ))
        stats.ok("announcement")


def _sync_pages(client, store, save, cid, refs, stats) -> None:
    listed = _list_or_empty(client.pages(cid))
    urls = {p["url"] for p in listed} | refs.page_urls
    known = {p["url"]: p.get("updated_at") for p in listed}
    for url in sorted(urls):
        try:
            # skip the extra request if the list already told us it hasn't changed
            if url in known and _page_unchanged(store, listed, url):
                continue
            p = client.page(cid, url)  # list endpoint doesn't include the body
        except httpx.HTTPStatusError:
            stats.fail("page")
            continue
        refs.scan_html(p.get("body"))
        doc_id = f"page:{p['page_id']}"
        if store.updated_at(doc_id) == p.get("updated_at"):
            continue
        save(Document(
            id=doc_id, course_id=cid, kind="page", title=p["title"], url=p.get("html_url"),
            updated_at=p.get("updated_at"), body=html_to_text(p.get("body")),
        ))
        stats.ok("page")


def _page_unchanged(store, listed, url) -> bool:
    p = next(p for p in listed if p["url"] == url)
    return store.updated_at(f"page:{p['page_id']}") == p.get("updated_at")


def _sync_files(client, store, cid, course_dir, refs, stats) -> None:
    listed = {f["id"]: f for f in _list_or_empty(client.files(cid))}
    for fid in sorted(set(listed) | refs.file_ids):
        try:
            f = listed.get(fid) or client.file(fid)
            _save_file(client, store, cid, course_dir, f, stats)
        except httpx.HTTPError as exc:
            log.debug("  file %s failed: %s", fid, exc)
            stats.fail("file")


def _save_file(client, store, cid, course_dir, f, stats) -> None:
    doc_id = f"file:{f['id']}"
    if store.updated_at(doc_id) == f.get("updated_at"):
        return
    if not f.get("url") or f.get("locked_for_user"):
        stats.fail("file")
        return
    if f.get("size", 0) > MAX_FILE_BYTES:
        return
    dest = course_dir / f"{f['id']}_{_safe(f['display_name'])}"
    client.download(f["url"], dest)
    store.upsert_document(Document(
        id=doc_id, course_id=cid, kind="file", title=f["display_name"],
        url=f.get("url"), local_path=str(dest), updated_at=f.get("updated_at"),
        body=file_to_text(dest),
    ))
    stats.ok("file")
