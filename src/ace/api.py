"""REST API over the local db. Run `ace serve` and open /docs."""
from __future__ import annotations

from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException

from .config import load_settings
from .storage import Store


def create_app(store: Store | None = None) -> FastAPI:
    if store is None:
        store = Store(load_settings(require_token=False).db_path)
    app = FastAPI(title="ace", version="0.1.0")

    @app.get("/courses")
    def courses():
        return store.list_courses()

    @app.get("/documents")
    def documents(course_id: int | None = None, kind: str | None = None):
        return store.list_documents(course_id, kind)

    @app.get("/documents/{doc_id}")
    def document(doc_id: str):
        doc = store.get_document(doc_id)
        if not doc:
            raise HTTPException(404, "not found")
        return doc

    @app.get("/search")
    def search(q: str, course_id: int | None = None, limit: int = 10):
        return store.search(q, course_id, limit)

    @app.get("/upcoming")
    def upcoming(limit: int = 20):
        return store.upcoming(datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"), limit)

    return app
