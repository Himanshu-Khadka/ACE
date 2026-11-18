"""Runs a full sync against a fake Canvas (no network or token needed)."""
import json

import httpx
from fastapi.testclient import TestClient

from ace.api import create_app
from ace.canvas_client import CanvasClient
from ace.storage import Store
from ace.sync import sync_all

BASE = "https://canvas.test"
FILES = {
    10: {"id": 10, "display_name": "notes.txt", "url": f"{BASE}/dl/10", "size": 20, "updated_at": "a"},
    11: {"id": 11, "display_name": "lab1.txt", "url": f"{BASE}/dl/11", "size": 20, "updated_at": "a"},
    12: {"id": 12, "display_name": "locked.pdf", "locked_for_user": True, "updated_at": "a"},
}


def make_canvas(files_tab_hidden: bool):
    def handler(request: httpx.Request) -> httpx.Response:
        p = request.url.path
        routes = {
            "/api/v1/courses": [{"id": 1, "name": "Intro to Systems", "course_code": "CMPUT 201", "term": {"name": "Fall 2026"}}],
            "/api/v1/courses/1": {"id": 1, "syllabus_body": "<p>Final exam is 40%</p>"},
            "/api/v1/courses/1/pages/syllabus-notes": {"page_id": 20, "url": "syllabus-notes", "title": "Course notes", "body": "<p>Midterm is worth <b>30%</b></p>", "updated_at": "a"},
            "/api/v1/courses/1/assignments": [{"id": 30, "name": "Lab 1", "due_at": "2099-01-01T00:00:00Z", "updated_at": "a",
                                               "description": f'<p>Write a pointer quiz, see <a href="{BASE}/courses/1/files/11/download">lab1</a></p>'}],
            "/api/v1/announcements": [{"id": 40, "title": "Welcome", "message": "<p>Office hours Tuesday</p>", "posted_at": "2026-09-02T10:00:00Z"}],
            # second module leaves out "items" so the client has to fetch them
            "/api/v1/courses/1/modules": [{"id": 50, "name": "Week 1", "items": [{"title": "Lecture 1", "type": "File", "content_id": 10}]},
                                          {"id": 51, "name": "Week 2"}],
            "/api/v1/courses/1/modules/51/items": [{"title": "Notes", "type": "Page", "page_url": "syllabus-notes"},
                                                   {"title": "Old slides", "type": "File", "content_id": 12}],
        }
        if p in ("/api/v1/courses/1/files", "/api/v1/courses/1/pages") and files_tab_hidden:
            return httpx.Response(403, json={"status": "unauthorized"})
        if p == "/api/v1/courses/1/files":
            return httpx.Response(200, json=list(FILES.values()))
        if p == "/api/v1/courses/1/pages":
            return httpx.Response(200, json=[{"page_id": 20, "url": "syllabus-notes", "title": "Course notes", "updated_at": "a"}])
        if p.startswith("/api/v1/files/"):
            return httpx.Response(200, json=FILES[int(p.rsplit("/", 1)[1])])
        if p == "/dl/10":
            return httpx.Response(200, content=b"malloc and free explained")
        if p == "/dl/11":
            return httpx.Response(200, content=b"segfault debugging steps")
        if p in routes:
            return httpx.Response(200, content=json.dumps(routes[p]), headers={"content-type": "application/json"})
        return httpx.Response(404)
    return httpx.MockTransport(handler)


def test_hidden_tabs_still_get_files_and_pages(tmp_path):
    store = Store(tmp_path / "db.sqlite")
    client = CanvasClient(BASE, "tok", transport=make_canvas(files_tab_hidden=True))
    stats = sync_all(client, store, tmp_path / "courses")
    assert stats.synced == {"module": 2, "syllabus": 1, "assignment": 1, "announcement": 1, "page": 1, "file": 2}
    assert stats.failed == {"file": 1}  # the locked one

    assert store.search("malloc")[0]["id"] == "file:10"     # linked from a module
    assert store.search("segfault")[0]["id"] == "file:11"   # linked from an assignment
    assert store.search("midterm")[0]["id"] == "page:20"    # page from a module
    assert store.search("final exam")[0]["course"] == "CMPUT 201"

    course = tmp_path / "courses" / "CMPUT 201"
    assert (course / "files" / "10_notes.txt").exists()
    assert "Due: 2099" in (course / "assignments" / "Lab 1.md").read_text()
    assert (course / "announcements" / "2026-09-02 Welcome.md").exists()


def test_resync_skips_unchanged(tmp_path):
    store = Store(tmp_path / "db.sqlite")
    client = CanvasClient(BASE, "tok", transport=make_canvas(files_tab_hidden=False))
    first = sync_all(client, store, tmp_path / "courses")
    assert first.synced["file"] == 2 and first.synced["page"] == 1
    second = sync_all(client, store, tmp_path / "courses")
    assert "file" not in second.synced and "page" not in second.synced and "assignment" not in second.synced

    api = TestClient(create_app(store))
    course = api.get("/courses").json()[0]
    assert course["code"] == "CMPUT 201" and course["counts"]["file"] == 2
    assert api.get("/deadlines", params={"days": 30000}).json()[0]["title"] == "Lab 1"
    assert "pointer" in api.get("/documents/assignment:30").json()["body"]
    assert store.search("C++ what's") == []


def test_empty_search(tmp_path):
    assert Store(tmp_path / "db.sqlite").search("   ") == []
