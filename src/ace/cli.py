"""ace sync | courses | search | serve | mcp"""
from __future__ import annotations

import argparse
import logging


def main() -> None:
    parser = argparse.ArgumentParser(prog="ace")
    sub = parser.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("sync", help="download/refresh course content from Canvas")
    s.add_argument("--course", type=int, action="append", help="only sync this course id (repeatable)")
    sub.add_parser("courses", help="list synced courses and what's in each")
    v = sub.add_parser("serve", help="run the REST API")
    v.add_argument("--port", type=int, default=8000)
    sub.add_parser("mcp", help="run the MCP server (stdio) for AI assistants")
    q = sub.add_parser("search", help="quick search from the terminal")
    q.add_argument("query")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(message)s")
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
    elif args.cmd == "serve":
        import uvicorn

        from .api import create_app

        uvicorn.run(create_app(), host="127.0.0.1", port=args.port)
    elif args.cmd == "mcp":
        from .mcp_server import run

        run()
    elif args.cmd == "courses":
        cfg = load_settings(require_token=False)
        for c in Store(cfg.db_path).list_courses():
            counts = ", ".join(f"{n} {k}s" for k, n in sorted(c["counts"].items()))
            print(f"{c['code'] or c['name']:<30} {counts or 'empty'}   (id {c['id']})")
    elif args.cmd == "search":
        cfg = load_settings(require_token=False)
        results = Store(cfg.db_path).search(args.query, limit=30)
        if not results:
            print("No results. Try fewer words, every word has to match.")
        current = None
        for r in sorted(results, key=lambda r: r["course"] or ""):  # group by course
            if r["course"] != current:
                current = r["course"]
                print(f"\n== {current} ==")
            print(f"  [{r['kind']}] {r['title']}\n      {r['snippet'].replace(chr(10), ' ')}")


if __name__ == "__main__":
    main()
