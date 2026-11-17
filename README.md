# ACE - Assignment & Coursework Engine

UAlberta uses Canvas and I got tired of clicking through every course to find slides, due dates and announcements. ACE downloads all of it to my laptop, pulls the text out of the PDFs/slides/docs, and puts it in a searchable SQLite db. Then I can query it from the terminal, a small REST API, or straight from Claude through an MCP server.

## What it syncs

- files (PDF, PPTX, DOCX, code, text)
- pages
- assignments (with due dates)
- announcements
- modules (as an outline of the course)

Re-running `ace sync` only grabs things that changed since last time.

A lot of profs hide the Files or Pages tab, which makes Canvas return 403/404 for the normal file list. ACE gets around that by collecting every file and page linked from modules, assignments, announcements and the syllabus and downloading those one by one. Anything that's still locked or unpublished gets counted as "no access" at the end.

Everything is saved per course:

```
data/courses/
  CMPUT 201/
    files/            the actual pdfs, slides, etc.
    assignments/      one .md per assignment (with due date + link)
    announcements/    named by date so they sort
    pages/            course pages + syllabus
    modules/          outline of each module
```

## Layout

```
src/ace/
  canvas_client.py   talks to the Canvas API (auth, pagination, rate limits)
  extract.py         text extraction for pdf / pptx / docx / html
  storage.py         sqlite + FTS5 full text search
  sync.py            pulls everything from canvas into the db
  api.py             REST API (FastAPI)
  mcp_server.py      MCP tools for Claude
  cli.py             `ace` command
```

## Setup

Needs Python 3.11+.

```bash
python -m venv .venv
.venv\Scripts\activate        # mac/linux: source .venv/bin/activate
pip install -e ".[dev]"
copy .env.example .env        # mac/linux: cp .env.example .env
```

Fill in `.env`:

- `CANVAS_BASE_URL` - the Canvas address you log into
- `CANVAS_TOKEN` - Canvas > Account > Settings > **+ New Access Token**

`.env` is gitignored, don't commit your token.

## Usage

```bash
ace sync                  # download / refresh everything
ace sync --course 12345   # just one course
ace courses               # list courses + what got downloaded for each
ace search "midterm"      # results grouped by course
ace serve                 # API docs at http://127.0.0.1:8000/docs
```

### Using it with Claude Desktop

Add this to `claude_desktop_config.json` (change the paths):

```json
{
  "mcpServers": {
    "ace": {
      "command": "C:\\path\\to\\ACE\\.venv\\Scripts\\ace.exe",
      "args": ["mcp"],
      "env": { "DATA_DIR": "C:\\path\\to\\ACE\\data" }
    }
  }
}
```

Tools it exposes: `list_courses`, `search_course_content`, `list_documents`, `get_document`, `upcoming_deadlines`.

## Tests

```bash
pytest
```

The test fakes the Canvas API so it runs without a token.

## TODO

- semantic search with embeddings
- auto sync on a schedule
- web UI
