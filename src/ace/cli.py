"""ace sync | courses | due | missing | ics | search | serve | mcp | mcp-config"""
from __future__ import annotations

import argparse
import json
import logging
import os
import sys
from datetime import datetime
from pathlib import Path


def local(iso: str | None) -> str:
    if not iso:
        return "no date"
    return datetime.fromisoformat(iso.replace("Z", "+00:00")).astimezone().strftime("%a %b %d  %I:%M %p")


def print_items(items: list[dict]) -> None:
    if not items:
        print("Nothing here.")
        return
    for it in items:
        status = f"[{it['status']}]" if it.get("status") else ""
        pts = f"  {it['points']:g} pts" if it.get("points") else ""
        print(f"{local(it['start_at']):<24} {it.get('course') or '':<12} {status:<12} {it['title']}{pts}")


def main() -> None:
    parser = argparse.ArgumentParser(prog="ace")
    sub = parser.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("sync", help="download/refresh course content from Canvas")
    s.add_argument("--course", type=int, action="append", help="only sync this course id (repeatable)")
    sub.add_parser("courses", help="list synced courses and what's in each")
    d = sub.add_parser("due", help="upcoming deadlines")
    d.add_argument("--days", type=float, default=14)
    d.add_argument("--course")
    d.add_argument("--all", action="store_true", help="include stuff already submitted")
    m = sub.add_parser("missing", help="past due and not submitted")
    m.add_argument("--course")
    i = sub.add_parser("ics", help="export deadlines + events to an .ics file for your calendar app")
    i.add_argument("--out", default="ace.ics")
    i.add_argument("--days", type=float, default=120)
    i.add_argument("--course")
    q = sub.add_parser("search", help="quick search from the terminal")
    q.add_argument("query")
    q.add_argument("--course")
    v = sub.add_parser("serve", help="run the REST API")
    v.add_argument("--port", type=int, default=8000)
    sub.add_parser("mcp", help="run the MCP server (stdio) for AI assistants")
    sub.add_parser("mcp-config", help="print the Claude Desktop config for this install")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(message)s")
    from . import service
    from .config import load_settings
    from .storage import Store

    if args.cmd == "sync":
        from .canvas_client import CanvasClient
        from .sync import sync_all

        cfg = load_settings()
        client = CanvasClient(cfg.base_url, cfg.token)
        try:
            stats = sync_all(client, Store(cfg.db_path), cfg.courses_dir, args.course)
        finally:
            client.close()
        print("\nUpdated:", ", ".join(f"{v} {k}s" for k, v in stats.synced.items()) or "nothing new")
        if stats.failed:
            print("No access to:", ", ".join(f"{v} {k}s" for k, v in stats.failed.items()),
                  "(usually locked or unpublished by the prof)")
        print(f"Saved in {cfg.courses_dir}")
        return

    if args.cmd == "mcp":
        from .mcp_server import run

        run()
        return

    cfg = load_settings(require_token=False)
    store = Store(cfg.db_path)
    try:
        if args.cmd == "courses":
            for c in store.list_courses():
                counts = ", ".join(f"{n} {k}s" for k, n in sorted(c["counts"].items()))
                print(f"{c['code'] or c['name']:<30} {counts or 'empty'}   (id {c['id']})")
        elif args.cmd == "due":
            print_items(service.deadlines(store, args.days, args.course, args.all))
        elif args.cmd == "missing":
            print_items(service.missing(store, args.course))
        elif args.cmd == "ics":
            from .ics import to_ics

            items = store.calendar_items(start=service.now_iso(-7), end=service.now_iso(args.days),
                                         course_id=store.resolve_course(args.course))
            Path(args.out).write_text(to_ics(items), encoding="utf-8", newline="")
            print(f"Wrote {len(items)} items to {Path(args.out).resolve()}")
            print("Import it in Google Calendar: Settings > Import & export > Import")
        elif args.cmd == "search":
            results = store.search(args.query, store.resolve_course(args.course), limit=30)
            if not results:
                print("No results. Try fewer words, every word has to match.")
            current = None
            for r in sorted(results, key=lambda r: r["course"] or ""):  # group by course
                if r["course"] != current:
                    current = r["course"]
                    print(f"\n== {current} ==")
                print(f"  [{r['kind']}] {r['title']}\n      {r['snippet'].replace(chr(10), ' ')}")
        elif args.cmd == "serve":
            import uvicorn

            from .api import create_app

            uvicorn.run(create_app(store), host="127.0.0.1", port=args.port)
        elif args.cmd == "mcp-config":
            # use this python + "-m ace" so it works no matter where Claude starts it from
            config = {"mcpServers": {"ace": {
                "command": sys.executable,
                "args": ["-m", "ace", "mcp"],
                "env": {"DATA_DIR": str(cfg.data_dir)},
            }}}
            print(json.dumps(config, indent=2))
            if os.name == "nt":
                where = Path(os.environ.get("APPDATA", "")) / "Claude" / "claude_desktop_config.json"
            else:
                where = Path.home() / "Library/Application Support/Claude/claude_desktop_config.json"
            print(f"\nPaste that into {where} (merge it if the file already has mcpServers),"
                  " then fully quit and reopen Claude Desktop.")
    except LookupError as exc:
        sys.exit(str(exc))


if __name__ == "__main__":
    main()
