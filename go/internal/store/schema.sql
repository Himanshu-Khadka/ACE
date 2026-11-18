-- runs on every start, so everything has to be IF NOT EXISTS

CREATE TABLE IF NOT EXISTS courses (
    id        BIGINT PRIMARY KEY,
    name      TEXT NOT NULL,
    code      TEXT NOT NULL DEFAULT '',
    term      TEXT NOT NULL DEFAULT '',
    synced_at TIMESTAMPTZ
);

-- files, pages, assignments, announcements, modules, syllabus
CREATE TABLE IF NOT EXISTS documents (
    id           TEXT PRIMARY KEY,            -- "file:123", "page:456", "syllabus:789"
    course_id    BIGINT NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    title        TEXT NOT NULL,
    url          TEXT NOT NULL DEFAULT '',
    local_path   TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT '',
    size         BIGINT NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ,                 -- canvas timestamp, used to skip unchanged stuff
    due_at       TIMESTAMPTZ,
    body         TEXT NOT NULL DEFAULT '',
    first_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- tsvector maxes out at 1MB so only index the start of really long files
    search       TSVECTOR GENERATED ALWAYS AS (
        setweight(to_tsvector('english', title), 'A') ||
        setweight(to_tsvector('english', left(body, 300000)), 'B')
    ) STORED
);
CREATE INDEX IF NOT EXISTS documents_course_idx ON documents (course_id, kind);
CREATE INDEX IF NOT EXISTS documents_search_idx ON documents USING GIN (search);

-- anything with a date: deadlines, calendar events, planner to-dos.
-- no foreign key on course_id because planner notes don't have a course
CREATE TABLE IF NOT EXISTS calendar_items (
    id          TEXT PRIMARY KEY,             -- "assignment:1", "event:2", "planner:quiz:3"
    course_id   BIGINT,
    type        TEXT NOT NULL,                -- assignment | quiz | discussion | event | todo
    title       TEXT NOT NULL,
    start_at    TIMESTAMPTZ,                  -- the due date for deadlines
    end_at      TIMESTAMPTZ,
    all_day     BOOLEAN NOT NULL DEFAULT false,
    is_deadline BOOLEAN NOT NULL DEFAULT false,
    status      TEXT NOT NULL DEFAULT '',     -- todo | submitted | graded | missing | excused | none
    points      DOUBLE PRECISION,
    score       DOUBLE PRECISION,
    location    TEXT NOT NULL DEFAULT '',
    url         TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    first_seen  TIMESTAMPTZ NOT NULL DEFAULT now(),
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS calendar_start_idx ON calendar_items (start_at);

-- what the scheduler noticed between syncs. this is what an agent polls
CREATE TABLE IF NOT EXISTS changes (
    id          BIGSERIAL PRIMARY KEY,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind        TEXT NOT NULL,                -- new_file, status_changed, due_soon, ...
    item_id     TEXT NOT NULL,
    course_id   BIGINT,
    title       TEXT NOT NULL,
    old_value   TEXT NOT NULL DEFAULT '',
    new_value   TEXT NOT NULL DEFAULT '',
    url         TEXT NOT NULL DEFAULT '',
    dedupe_key  TEXT UNIQUE                   -- so "due soon" only fires once per due date
);
CREATE INDEX IF NOT EXISTS changes_time_idx ON changes (detected_at);

CREATE TABLE IF NOT EXISTS sync_runs (
    id          BIGSERIAL PRIMARY KEY,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    ok          BOOLEAN,
    error       TEXT NOT NULL DEFAULT '',
    stats       JSONB,
    changes     INT NOT NULL DEFAULT 0
);
