"""Deadlines, submission status, calendar events, planner items and the .ics export."""
import httpx
from fastapi.testclient import TestClient

from ace import service
from ace.api import create_app
from ace.canvas_client import CanvasClient
from ace.deadlines import now_iso, submission_status
from ace.ics import to_ics
from ace.storage import Store
from ace.sync import sync_all

BASE = "https://canvas.test"
SOON, LATER, PAST = now_iso(3), now_iso(10), now_iso(-2)

ROUTES = {
    "/api/v1/courses": [{"id": 1, "name": "Intro to Systems", "course_code": "CMPUT 201"}],
    "/api/v1/courses/1/assignments": [
        {"id": 30, "name": "Lab 1", "due_at": SOON, "points_possible": 10, "submission_types": ["online_upload"],
         "submission": {"workflow_state": "unsubmitted"}, "updated_at": "a"},
        {"id": 31, "name": "Lab 0", "due_at": PAST, "points_possible": 5, "submission_types": ["online_upload"],
         "submission": {"workflow_state": "unsubmitted", "missing": True}, "updated_at": "a"},
        {"id": 32, "name": "Quiz 1", "due_at": LATER, "points_possible": 20, "submission_types": ["online_quiz"],
         "submission": {"workflow_state": "graded", "score": 18, "submitted_at": PAST}, "updated_at": "a"},
    ],
    "/api/v1/calendar_events": [
        {"id": 60, "title": "Midterm", "start_at": LATER, "end_at": None, "location_name": "CCIS 1-430",
         "description": "<p>Covers weeks 1-6, bring ID</p>"},
    ],
    "/api/v1/planner/items": [
        # already covered by the assignment, should be skipped
        {"plannable_type": "assignment", "plannable_id": 30, "course_id": 1, "plannable_date": SOON,
         "plannable": {"title": "Lab 1"}},
        {"plannable_type": "discussion_topic", "plannable_id": 70, "course_id": 1, "plannable_date": SOON,
         "plannable": {"title": "Intro post"}, "submissions": {"submitted": False}, "html_url": "/courses/1/discussion_topics/70"},
    ],
}


def handler(request: httpx.Request) -> httpx.Response:
    if request.url.path in ROUTES:
        return httpx.Response(200, json=ROUTES[request.url.path])
    return httpx.Response(404)


def synced(tmp_path) -> Store:
    store = Store(tmp_path / "db.sqlite")
    sync_all(CanvasClient(BASE, "tok", transport=httpx.MockTransport(handler)), store, tmp_path / "courses")
    return store


def test_deadlines_and_status(tmp_path):
    store = synced(tmp_path)
    due = service.deadlines(store, 14)
    assert [d["title"] for d in due] == ["Lab 1", "Intro post"]  # quiz is graded so hidden
    assert due[1]["url"] == f"{BASE}/courses/1/discussion_topics/70"

    everything = {d["title"]: d for d in service.deadlines(store, 14, "cmput201", include_done=True)}
    assert everything["Quiz 1"]["status"] == "graded" and everything["Quiz 1"]["type"] == "quiz"
    assert [m["title"] for m in service.missing(store)] == ["Lab 0"]

    cal = service.calendar(store)
    assert "Midterm" in [c["title"] for c in cal]


def test_submission_status_rules():
    assert submission_status({"excused": True}, None, []) == "excused"
    assert submission_status({"submitted_at": "x"}, PAST, ["online_upload"]) == "submitted"
    assert submission_status({}, PAST, ["online_upload"]) == "missing"
    assert submission_status({}, SOON, ["online_upload"]) == "todo"
    assert submission_status({}, PAST, ["on_paper"]) == "none"


def test_api_and_ics(tmp_path):
    api = TestClient(create_app(synced(tmp_path)))
    assert [d["title"] for d in api.get("/deadlines/missing").json()] == ["Lab 0"]
    assert api.get("/deadlines", params={"course": "nope"}).status_code == 404
    overview = api.get("/courses/cmput 201/overview").json()
    assert overview["course"]["code"] == "CMPUT 201" and overview["missing"][0]["title"] == "Lab 0"

    ics = api.get("/calendar.ics").text
    assert ics.startswith("BEGIN:VCALENDAR") and ics.count("BEGIN:VEVENT") == 5
    assert "SUMMARY:[CMPUT 201] Due: Lab 1" in ics
    assert "LOCATION:CCIS 1-430" in ics
    assert all(len(line.encode()) <= 75 for line in ics.split("\r\n"))


def test_ics_escaping():
    item = {"id": "event:1", "title": "Exam; room A, B", "start_at": SOON, "is_deadline": False,
            "description": "x" * 300}
    ics = to_ics([item])
    assert "Exam\\; room A\\, B" in ics


def test_course_lookup(tmp_path):
    store = Store(tmp_path / "db.sqlite")
    store.upsert_course(1, "Intro to Systems", "CMPUT 201", None)
    store.upsert_course(2, "Data Structures", "CMPUT 204", None)
    assert store.resolve_course("cmput 204") == 2
    assert store.resolve_course("data struct") == 2
    assert store.resolve_course("1") == 1
    import pytest
    with pytest.raises(LookupError, match="more than one"):
        store.resolve_course("cmput")
