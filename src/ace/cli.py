"""ace sync | serve | mcp | search"""
from __future__ import annotations

import argparse
import logging


def main() -> None:
    parser = argparse.ArgumentParser(prog="ace")
    sub = parser.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("sync", help="download/refresh course content from Canvas")
    s.add_argument("--course", type=int, action="append", help="only sync this course id (repeatable)")
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
            stats = sync_all(client, Store(cfg.db_path), cfg.files_dir, args.course)
        finally:
            client.close()
        print("Updated:", ", ".join(f"{v} {k}s" for k, v in stats.items()) or "nothing new")
    elif args.cmd == "serve":
        import uvicorn

        from .api import create_app

        uvicorn.run(create_app(), host="127.0.0.1", port=args.port)
    elif args.cmd == "mcp":
        from .mcp_server import run

        run()
    elif args.cmd == "search":
        cfg = load_settings(require_token=False)
        for r in Store(cfg.db_path).search(args.query):
            print(f"{r['id']:<20} {r['title']}\n    {r['snippet']}\n")


if __name__ == "__main__":
    main()
