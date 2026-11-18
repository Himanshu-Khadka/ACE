# ACE - Assignment & Coursework Engine

UAlberta uses Canvas and I got tired of clicking through every course to find slides, due dates and announcements. ACE downloads all of it to my laptop, pulls the text out of the PDFs/slides/docs, and puts it in a searchable SQLite db. Then I can query it from the terminal, a small REST API, or straight from Claude through an MCP server.

## What it syncs

- files (PDF, PPTX, DOCX, code, text)
- pages + syllabus
- assignments, quizzes and discussions with due dates **and whether I submitted them** (todo / submitted / graded / missing / excused)
- calendar events the profs post (exams, labs, office hours)
- planner to-dos (ungraded discussions, pages with to-do dates, my own notes)
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
  deadlines.py       turns assignments/events/planner items into calendar rows + works out status
  service.py         queries shared by the cli, api and mcp server
  ics.py             .ics export for calendar apps
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
ace due                   # what's due in the next 14 days (--days 30, --course cmput201, --all)
ace missing               # past due and not submitted
ace ics                   # write ace.ics with all deadlines + events
ace search "midterm"      # results grouped by course (--course to filter)
ace serve                 # API docs at http://127.0.0.1:8000/docs
```

Anywhere it asks for a course you can use the code (`cmput201`, `"CMPUT 201"`), part of the name, or the id.

### Adding deadlines to your calendar

Run `ace ics` and import `ace.ics` (Google Calendar: Settings > Import & export > Import). Deadlines show as 30 min blocks ending at the due time with a reminder the day before, and anything already submitted is marked "(done)". Re-importing updates the same events instead of making duplicates.

### REST API

`ace serve`, then everything is documented at http://127.0.0.1:8000/docs. The main ones:

| endpoint | what it gives you |
|---|---|
| `GET /courses` | courses + how much content each has |
| `GET /courses/{course}/overview` | syllabus, modules, upcoming + missing work, recent announcements |
| `GET /deadlines?days=14&course=&include_done=false` | upcoming deadlines with status |
| `GET /deadlines/missing` | overdue work |
| `GET /calendar?start=2026-11-01&end=2026-12-15` | deadlines + events in a date range |
| `GET /calendar.ics` | same thing as an iCalendar file |
| `GET /search?q=&course=` | full text search |
| `GET /documents/{id}` | full text of one file/page/assignment |

All times are UTC (ISO 8601).

### Using it with Claude Desktop

Run this and follow what it prints, it fills in the right paths for your machine:

```bash
ace mcp-config
```

Then restart Claude Desktop (fully quit it from the tray). Claude gets these tools:

- `list_courses`, `course_overview`
- `upcoming_deadlines`, `missing_work`, `calendar`
- `search_course_content`, `list_documents`, `get_document` (reads long PDFs in chunks)

and three prompts you can pick from the + menu: **study_plan**, **quiz_me**, **explain_assignment**.

Things to ask it: "what do I have due this week?", "make me a study plan for my CMPUT 201 midterm", "quiz me on pointers using the lecture slides".

## Tests

```bash
pytest
```

The test fakes the Canvas API so it runs without a token.

## TODO

- semantic search with embeddings
- auto sync on a schedule (Windows Task Scheduler for now)
- push straight to Google Calendar instead of importing a file
- web UI
