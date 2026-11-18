# ACE in Go (docker version)

Same idea as the python ACE in the repo root, but rewritten in Go so it can run 24/7 in a docker container with Postgres. Instead of me running `ace sync` by hand, it checks Canvas every 30 min on its own, keeps track of what changed, and has an API my AI agent can use to manage my calendar.

**What it does on every sync**

- downloads new/updated files, pages, assignments, announcements, modules, syllabus (still works when the prof hides the Files or Pages tab, same trick as the python version: it follows links from modules, assignments, announcements and the syllabus)
- pulls deadlines with submission status (todo / submitted / graded / missing / excused), calendar events and the Canvas to-do list
- compares everything with the last sync and logs changes:

| kind | when |
|---|---|
| `new_file`, `file_updated` | prof uploaded or replaced a file |
| `new_page`, `page_updated`, `syllabus_updated` | |
| `new_announcement` | |
| `new_deadline`, `due_date_changed`, `deadline_removed` | assignments/quizzes/discussions |
| `status_changed` | e.g. `todo -> submitted`, `submitted -> graded (8/10)`, `todo -> missing` |
| `new_event`, `event_changed`, `event_removed` | exams, labs, office hours on the Canvas calendar |
| `due_soon` | something unfinished is due within `DUE_SOON` (48h default). fires once per due date |

The very first sync just fills the database, otherwise every single file would show up as "new".

## About the speed

Go is faster than Python, but honestly a sync is almost all waiting on Canvas (HTTP requests + downloads), so the language barely matters there. What actually makes it faster is that it syncs a few courses at the same time (`SYNC_WORKERS`, default 3, and Canvas rate limits if you go too high). The real wins of the Go version are deployment ones: one small static binary, a ~30MB image, low memory, starts instantly, and it runs on a schedule by itself.

## Running it

Needs [Docker Desktop](https://www.docker.com/products/docker-desktop/) (Windows/mac) or docker + compose on linux.

```bash
cd go
copy .env.example .env      # mac/linux: cp .env.example .env
```

Fill in `.env`: `CANVAS_TOKEN` (Canvas > Account > Settings > + New Access Token), and set a `POSTGRES_PASSWORD` and an `API_TOKEN`. `.env` is gitignored and `.dockerignore`d, so the token never goes into git or into the image.

```bash
docker compose up -d --build
docker compose logs -f ace       # watch the first sync
```

The API is on http://localhost:8080 (only reachable from your own computer, see the comment in `docker-compose.yml` if you want it on your network). Data lives in two docker volumes, so `docker compose down` keeps everything; `down -v` wipes it.

## API

Send `Authorization: Bearer <API_TOKEN>` (or `?token=`) with every request. `GET /` lists all endpoints. Course can be the code (`cmput201`, `CMPUT 201`), part of the name, or the id. Dates can be `2026-11-20` (local time from `TZ`), a full timestamp, or "ago" like `24h` / `7d`.

| endpoint | what you get |
|---|---|
| `GET /deadlines?days=14&course=&include_done=false` | upcoming work with status, soonest first |
| `GET /deadlines/missing` | past due and not submitted |
| `GET /calendar?start=2026-11-01&end=2026-11-30` | deadlines + events in a range |
| `GET /calendar.ics?days=120` | same as an iCalendar file. you can subscribe to this url in Google Calendar |
| `GET /changes?since=24h` | what changed. filter with `kind=new_file,status_changed` and `course=` |
| `GET /changes?after_id=42` | only changes newer than the last one you saw (easiest for polling) |
| `GET /courses`, `GET /courses/{course}/overview` | courses, or syllabus + modules + upcoming/missing work + announcements for one |
| `GET /documents?course=&kind=file&since=7d` | list files/pages/etc |
| `GET /documents/{id}` | full text of one (e.g. `file:123`) |
| `GET /documents/{id}/download` | the original pdf/slides |
| `GET /search?q=linked list` | full text search (Postgres), supports `"exact phrase"` and `-word` |
| `POST /sync` | sync now instead of waiting |
| `GET /status` | last syncs, errors, next run |

```bash
curl -H "Authorization: Bearer $API_TOKEN" "localhost:8080/deadlines?days=7"
```

If you set `WEBHOOK_URL`, every sync that finds something POSTs `{"changes": [...]}` there, so an agent / n8n / a discord bot can react without polling.

## Using it from an AI agent (MCP)

The same container also serves MCP at `http://localhost:8080/mcp`, with tools for deadlines, missing work, calendar, `recent_changes`, search and reading documents, plus prompts (`study_plan`, `whats_new`, `quiz_me`, `explain_assignment`).

Claude Code:

```bash
claude mcp add --transport http ace http://localhost:8080/mcp --header "Authorization: Bearer <API_TOKEN>"
```

Claude Desktop (uses `mcp-remote` to bridge to a local http server), in `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "ace": {
      "command": "npx",
      "args": ["mcp-remote", "http://localhost:8080/mcp", "--header", "Authorization: Bearer <API_TOKEN>"]
    }
  }
}
```

Then ask stuff like "what changed on canvas since yesterday?" or "put my deadlines for next week in my calendar".

## Settings (`.env`)

| var | default | |
|---|---|---|
| `CANVAS_BASE_URL` | | e.g. `https://canvas.ualberta.ca` |
| `CANVAS_TOKEN` | | your personal access token |
| `SYNC_INTERVAL` | `30m` | how often to sync |
| `DUE_SOON` | `48h` | window for `due_soon` alerts |
| `SYNC_WORKERS` | `3` | courses synced in parallel |
| `TZ` | `America/Edmonton` | for plain dates and all day events |
| `API_TOKEN` | | required for every request if set |
| `WEBHOOK_URL` | | optional, gets new changes after each sync |
| `POSTGRES_PASSWORD` | | required, any password you want |

## Code

```
cmd/ace/            main: `ace serve` (api + scheduler), `ace sync` (once), `ace api` (no syncing)
internal/canvas     canvas api client (pagination, rate limit retry)
internal/syncer     the sync, change detection, scheduler, webhook
internal/store      postgres: tables, full text search, queries
internal/service    queries shared by the api and mcp
internal/api        http api
internal/mcpserver  mcp tools + prompts
internal/extract    text out of pdf / pptx / docx / html
internal/ics        .ics export
```

Run it without docker: `go run ./cmd/ace serve` with `DATABASE_URL` pointing at any Postgres.

### Tests

The sync test runs against a fake Canvas server, so no token needed, but it needs a Postgres:

```bash
docker run -d --name acepg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=ace -p 55432:5432 postgres:17-alpine
ACE_TEST_DB="postgres://postgres:test@localhost:55432/ace?sslmode=disable" go test -p 1 ./...
```

Without `ACE_TEST_DB` the database tests are skipped.

## Differences from the python version

- Postgres instead of SQLite, change log + scheduler are new
- files are saved per course in the `/data` volume, but pages/assignments/announcements only go in the db (no `.md` copies), read them through the API
- no CLI for searching, use the API or MCP instead
- MCP runs over http inside the container instead of stdio
