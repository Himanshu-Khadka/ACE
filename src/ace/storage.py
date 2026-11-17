"""SQLite storage + full text search (FTS5).

All content types go in one `documents` table (ids like "file:123") so a
single search covers everything. Triggers keep the FTS index updated.
"""
from __future__ import annotations

import sqlite3
from dataclasses import dataclass
from pathlib import Path
from typing import Any

SCHEMA = """
CREATE TABLE IF NOT EXISTS courses (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    code TEXT,
    term TEXT
);
CREATE TABLE IF NOT EXISTS documents (
    id TEXT PRIMARY KEY,          -- e.g. "file:123", "page:456"
    course_id INTEGER NOT NULL REFERENCES courses(id),
    kind TEXT NOT NULL,           -- file | page | assignment | announcement | module
    title TEXT NOT NULL,
    url TEXT,                     -- link back to Canvas
    local_path TEXT,              -- downloaded file, if any
    updated_at TEXT,              -- Canvas timestamp, used to skip unchanged items
    due_at TEXT,                  -- assignments only
    body TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_docs_course ON documents(course_id, kind);
CREATE INDEX IF NOT EXISTS idx_docs_due ON documents(due_at);

CREATE VIRTUAL TABLE IF NOT EXISTS documents_fts USING fts5(
    title, body, content='documents', content_rowid='rowid', tokenize='porter unicode61'
);
CREATE TRIGGER IF NOT EXISTS docs_ai AFTER INSERT ON documents BEGIN
    INSERT INTO documents_fts(rowid, title, body) VALUES (new.rowid, new.title, new.body);
END;
CREATE TRIGGER IF NOT EXISTS docs_ad AFTER DELETE ON documents BEGIN
    INSERT INTO documents_fts(documents_fts, rowid, title, body)
    VALUES ('delete', old.rowid, old.title, old.body);
END;
CREATE TRIGGER IF NOT EXISTS docs_au AFTER UPDATE ON documents BEGIN
    INSERT INTO documents_fts(documents_fts, rowid, title, body)
    VALUES ('delete', old.rowid, old.title, old.body);
    INSERT INTO documents_fts(rowid, title, body) VALUES (new.rowid, new.title, new.body);
END;
"""


@dataclass
class Document:
    id: str
    course_id: int
    kind: str
    title: str
    body: str = ""
    url: str | None = None
    local_path: str | None = None
    updated_at: str | None = None
    due_at: str | None = None


class Store:
    def __init__(self, db_path: Path | str):
        self.conn = sqlite3.connect(str(db_path), check_same_thread=False)
        self.conn.row_factory = sqlite3.Row
        self.conn.executescript(SCHEMA)

    def upsert_course(self, id: int, name: str, code: str | None, term: str | None) -> None:
        self.conn.execute(
            "INSERT INTO courses(id, name, code, term) VALUES (?,?,?,?) "
            "ON CONFLICT(id) DO UPDATE SET name=excluded.name, code=excluded.code, term=excluded.term",
            (id, name, code, term),
        )

    def upsert_document(self, d: Document) -> None:
        self.conn.execute(
            """INSERT INTO documents(id, course_id, kind, title, url, local_path, updated_at, due_at, body)
               VALUES (?,?,?,?,?,?,?,?,?)
               ON CONFLICT(id) DO UPDATE SET
                 title=excluded.title, url=excluded.url, local_path=excluded.local_path,
                 updated_at=excluded.updated_at, due_at=excluded.due_at, body=excluded.body""",
            (d.id, d.course_id, d.kind, d.title, d.url, d.local_path, d.updated_at, d.due_at, d.body),
        )

    def updated_at(self, doc_id: str) -> str | None:
        row = self.conn.execute("SELECT updated_at FROM documents WHERE id=?", (doc_id,)).fetchone()
        return row["updated_at"] if row else None

    def commit(self) -> None:
        self.conn.commit()

    def list_courses(self) -> list[dict[str, Any]]:
        """Courses with how many of each kind of document they have."""
        courses = [dict(r) for r in self.conn.execute("SELECT * FROM courses ORDER BY code, name")]
        counts = self.conn.execute("SELECT course_id, kind, COUNT(*) AS n FROM documents GROUP BY course_id, kind")
        by_id = {c["id"]: c for c in courses}
        for c in courses:
            c["counts"] = {}
        for r in counts:
            if r["course_id"] in by_id:
                by_id[r["course_id"]]["counts"][r["kind"]] = r["n"]
        return courses

    def list_documents(self, course_id: int | None = None, kind: str | None = None) -> list[dict]:
        sql = "SELECT id, course_id, kind, title, url, local_path, updated_at, due_at FROM documents WHERE 1=1"
        args: list[Any] = []
        if course_id is not None:
            sql += " AND course_id=?"
            args.append(course_id)
        if kind:
            sql += " AND kind=?"
            args.append(kind)
        return [dict(r) for r in self.conn.execute(sql + " ORDER BY title", args)]

    def get_document(self, doc_id: str) -> dict | None:
        row = self.conn.execute("SELECT * FROM documents WHERE id=?", (doc_id,)).fetchone()
        return dict(row) if row else None

    def search(self, query: str, course_id: int | None = None, limit: int = 10) -> list[dict]:
        if not _fts_query(query):
            return []
        sql = """SELECT d.id, d.course_id, c.code AS course, d.kind, d.title, d.url, d.due_at,
                        snippet(documents_fts, 1, '[', ']', ' ... ', 24) AS snippet
                 FROM documents_fts JOIN documents d ON d.rowid = documents_fts.rowid
                 JOIN courses c ON c.id = d.course_id
                 WHERE documents_fts MATCH ?"""
        args: list[Any] = [_fts_query(query)]
        if course_id is not None:
            sql += " AND d.course_id=?"
            args.append(course_id)
        sql += " ORDER BY bm25(documents_fts, 5.0, 1.0) LIMIT ?"
        args.append(limit)
        return [dict(r) for r in self.conn.execute(sql, args)]

    def upcoming(self, since_iso: str, limit: int = 20) -> list[dict]:
        sql = """SELECT d.id, d.title, d.due_at, d.url, c.code AS course
                 FROM documents d JOIN courses c ON c.id = d.course_id
                 WHERE d.kind='assignment' AND d.due_at >= ? ORDER BY d.due_at LIMIT ?"""
        return [dict(r) for r in self.conn.execute(sql, (since_iso, limit))]


# quote every word, otherwise input like "C++" breaks the FTS query syntax
def _fts_query(text: str) -> str:
    words = [w.replace('"', "") for w in text.split()]
    return " ".join(f'"{w}"' for w in words if w)
