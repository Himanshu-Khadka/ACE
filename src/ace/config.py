"""Settings come from env vars / .env so nothing secret ends up in the code."""
from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

from dotenv import find_dotenv, load_dotenv


@dataclass(frozen=True)
class Settings:
    base_url: str
    token: str
    data_dir: Path

    @property
    def db_path(self) -> Path:
        return self.data_dir / "canvas.db"

    @property
    def courses_dir(self) -> Path:
        return self.data_dir / "courses"


def load_settings(require_token: bool = True) -> Settings:
    env_file = find_dotenv(usecwd=True)
    load_dotenv(env_file)
    base_url = os.environ.get("CANVAS_BASE_URL", "").rstrip("/")
    token = os.environ.get("CANVAS_TOKEN", "")
    if require_token and (not base_url or not token):
        raise SystemExit(
            "Set CANVAS_BASE_URL and CANVAS_TOKEN in your .env file (see .env.example)."
        )
    # relative DATA_DIR is relative to the .env file, not wherever you ran the
    # command from (Claude Desktop starts the mcp server in some random folder)
    root = Path(env_file).parent if env_file else Path.cwd()
    data_dir = Path(os.environ.get("DATA_DIR", "data"))
    if not data_dir.is_absolute():
        data_dir = (root / data_dir).resolve()
    data_dir.mkdir(parents=True, exist_ok=True)
    return Settings(base_url=base_url, token=token, data_dir=data_dir)
