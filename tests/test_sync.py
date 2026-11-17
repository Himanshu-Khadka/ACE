"""Runs a full sync against a fake Canvas (no network or token needed)."""
import json

import httpx
from fastapi.testclient import TestClient

from ace.api import create_app
from ace.canvas_client import CanvasClient
from ace.storage import Store
from ace.sync import sync_all

BASE = "https://canvas.test"


def fake_canvas(request: httpx.Request) -> httpx.Response:
    p = request.url.path
    routes = {
        "/api/v1/courses": [{"id": 1, "name": "Intro to Systems", "course_code": "CMPUT 201", "term": {"name": "Fall 2026"}}],
        "/api/v1/courses/1/files": [{"id": 10, "display_name": "notes.txt", "url": f"{BASE}/dl/10", "size": 20, "updated_at": "2026-09-01T00:00:00Z"}],
        "/api/v1/courses/1/pages": [{"page_id": 20, "url": "syllabus", "title": "Syllabus", "updated_at": "2026-09-01T00:00:00Z"}],
        "/api/v1/courses/1/pages/syllabus": {"body": "<p>Midterm is worth <b>30%</b></p>"},
        "/api/v1/courses/1/assignments": [{"id": 30, "name": "Lab 1", "due_at": "2099-01-01T00:00:00Z", "description": "<p>Write a pointer quiz</p>", "updated_at": "x"}],
        "/api/v1/announcements": [{"id": 40, "title": "Welcome", "message": "<p>Office hours Tuesday</p>", "posted_at": "x"}],
        "/api/v1/courses/1/modules": [{"id": 50, "name": "Week 1", "items": [{"title": "Lecture 1", "type": "File"}]}],
    }
    if p == "/dl/10":
        return httpx.Response(200, content=b"malloc and free explained")
    if p == "/api/v1/courses/1/files" and "page=2" not in str(request.url):
        # page 1 links to page 2 to test pagination
        return httpx.Response(200, json=routes[p], headers={"Link": f'<{BASE}/api/v1/courses/1/files?page=2>; rel="next"'})
    if p == "/api/v1/courses/1/files":
        return httpx.Response(200, json=[{"id": 11, "display_name": "empty.bin", "url": f"{BASE}/dl/11", "size": 0, "updated_at": "y"}])
    if p == "/dl/11":
        return httpx.Response(200, content=b"")
    if p in routes:
        return httpx.Response(200, content=json.dumps(routes[p]), headers={"content-type": "application/json"})
    return httpx.Response(404)


def test_sync_and_search(tmp_path):
    store = Store(tmp_path / "db.sqlite")
    client = CanvasClient(BASE, "tok", transport=httpx.MockTransport(fake_canvas))
    stats = sync_all(client, store, tmp_path / "files")
    assert stats == {"file": 2, "page": 1, "assignment": 1, "announcement": 1, "module": 1}

    assert store.search("malloc")[0]["id"] == "file:10"
    assert store.search("midterm")[0]["id"] == "page:20"
    assert store.search("C++ what's")  == []

    # nothing changed so the second sync should skip everything
    assert sync_all(client, store, tmp_path / "files") == {"file": 0, "page": 0, "assignment": 0, "announcement": 0, "module": 1}

    api = TestClient(create_app(store))
    assert api.get("/courses").json()[0]["code"] == "CMPUT 201"
    assert api.get("/upcoming").json()[0]["title"] == "Lab 1"
    assert "pointer" in api.get("/documents/assignment:30").json()["body"]
def test_empty_search(tmp_path):
    from ace.storage import Store
    assert Store(tmp_path / "db.sqlite").search("   ") == []
