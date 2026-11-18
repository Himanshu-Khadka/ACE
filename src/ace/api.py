"""REST API over the local db. Run `ace serve` and open /docs."""
from __future__ import annotations

from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse, Response

from . import service
from .config import load_settings
from .ics import to_ics
from .storage import Store


def create_app(store: Store | None = None) -> FastAPI:
    if store is None:
        store = Store(load_settings(require_token=False).db_path)
    app = FastAPI(title="ACE", version="0.2.0",
                  description="Your Canvas courses, deadlines and calendar as an API.")

    @app.exception_handler(LookupError)
    def not_found(_: Request, exc: LookupError):
        return JSONResponse(status_code=404, content={"detail": str(exc)})

    # courses ---------------------------------------------------------------
    @app.get("/courses", tags=["courses"])
    def courses():
        return store.list_courses()

    @app.get("/courses/{course}/overview", tags=["courses"])
    def course_overview(course: str):
        """`course` can be the id or the code, e.g. `cmput201`."""
        return service.overview(store, course)

    # deadlines + calendar --------------------------------------------------
    @app.get("/deadlines", tags=["calendar"])
    def deadlines(days: float = 14, course: str | None = None, include_done: bool = False):
        """Upcoming assignments/quizzes/discussions. Done ones are hidden unless include_done=true."""
        return service.deadlines(store, days, course, include_done)

    @app.get("/deadlines/missing", tags=["calendar"])
    def missing(course: str | None = None):
        return service.missing(store, course)

    @app.get("/calendar", tags=["calendar"])
    def calendar(start: str | None = None, end: str | None = None, course: str | None = None):
        """Deadlines and calendar events between start and end (YYYY-MM-DD). Defaults to the next 30 days."""
        return service.calendar(store, start, end, course)

    @app.get("/calendar.ics", tags=["calendar"])
    def calendar_ics(days: float = 120, course: str | None = None):
        """Import this into Google Calendar / Outlook."""
        items = store.calendar_items(start=service.now_iso(-7), end=service.now_iso(days),
                                     course_id=store.resolve_course(course))
        return Response(to_ics(items), media_type="text/calendar",
                        headers={"Content-Disposition": 'attachment; filename="ace.ics"'})

    # content ---------------------------------------------------------------
    @app.get("/documents", tags=["content"])
    def documents(course: str | None = None, kind: str | None = None):
        return store.list_documents(store.resolve_course(course), kind)

    @app.get("/documents/{doc_id}", tags=["content"])
    def document(doc_id: str):
        doc = store.get_document(doc_id)
        if not doc:
            raise HTTPException(404, "not found")
        return doc

    @app.get("/search", tags=["content"])
    def search(q: str, course: str | None = None, limit: int = 10):
        return store.search(q, store.resolve_course(course), limit)

    return app
