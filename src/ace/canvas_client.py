"""Small Canvas REST API client.

Canvas paginates using the Link header, so paginate() keeps following
rel="next" until there are no pages left.
"""
from __future__ import annotations

import time
from pathlib import Path
from typing import Any, Iterator

import httpx


class CanvasClient:
    def __init__(self, base_url: str, token: str, transport: httpx.BaseTransport | None = None):
        self._http = httpx.Client(
            base_url=f"{base_url}/api/v1",
            headers={"Authorization": f"Bearer {token}"},
            timeout=60,
            follow_redirects=True,
            transport=transport,
        )

    def close(self) -> None:
        self._http.close()

    def _get(self, url: str, params: dict | None = None) -> httpx.Response:
        for attempt in range(4):
            resp = self._http.get(url, params=params)
            # canvas sends 403 "Rate Limit Exceeded" when throttling
            if resp.status_code == 429 or (
                resp.status_code == 403 and "Rate Limit" in resp.text
            ):
                time.sleep(2**attempt)
                continue
            resp.raise_for_status()
            return resp
        resp.raise_for_status()
        return resp

    def paginate(self, path: str, params: dict | None = None) -> Iterator[dict[str, Any]]:
        params = {"per_page": 100, **(params or {})}
        resp = self._get(path, params)
        while True:
            data = resp.json()
            yield from data if isinstance(data, list) else [data]
            nxt = resp.links.get("next", {}).get("url")
            if not nxt:
                return
            resp = self._get(nxt)

    def courses(self) -> Iterator[dict]:
        return self.paginate("/courses", {"enrollment_state": "active", "include[]": "term"})

    def modules(self, course_id: int) -> Iterator[dict]:
        return self.paginate(f"/courses/{course_id}/modules", {"include[]": "items"})

    def files(self, course_id: int) -> Iterator[dict]:
        return self.paginate(f"/courses/{course_id}/files")

    def pages(self, course_id: int) -> Iterator[dict]:
        return self.paginate(f"/courses/{course_id}/pages")

    def page(self, course_id: int, url: str) -> dict:
        return self._get(f"/courses/{course_id}/pages/{url}").json()

    def assignments(self, course_id: int) -> Iterator[dict]:
        return self.paginate(f"/courses/{course_id}/assignments")

    def announcements(self, course_id: int) -> Iterator[dict]:
        # without start_date Canvas only returns the last 14 days
        return self.paginate(
            "/announcements",
            {"context_codes[]": f"course_{course_id}", "start_date": "2000-01-01"},
        )

    def download(self, url: str, dest: Path) -> None:
        dest.parent.mkdir(parents=True, exist_ok=True)
        tmp = dest.with_suffix(dest.suffix + ".part")
        with self._http.stream("GET", url) as resp:
            resp.raise_for_status()
            with tmp.open("wb") as fh:
                for chunk in resp.iter_bytes():
                    fh.write(chunk)
        tmp.replace(dest)
