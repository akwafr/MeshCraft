-- External driver repositories, their builds, and the running instances.
--
-- The pattern is pipelines': a declarative row (driver_repos), a job history
-- (driver_builds, forward-only, with its log), and the live state
-- (driver_instances) the controller reconciles containers against at
-- startup. Operator-managed lists live in Postgres, never in env — same rule
-- as scan_prefixes; MESHCRAFT_DRIVER_URLS is the transitional exception and
-- dies with this table's UI.

CREATE TABLE driver_repos (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Any git URL the host can clone: https://github.com/…, https://gitlab…,
    -- or a local path for development (file:// or absolute path).
    url         TEXT NOT NULL,
    -- Subdirectory of the repository holding driver.yaml + Dockerfile; empty
    -- means the repository root. This is what makes the official monorepo
    -- (one subdir per driver) and a third party's one-repo-one-driver both
    -- ordinary.
    subdir      TEXT NOT NULL DEFAULT '',
    -- Git ref to build: a branch, a tag, or a full commit. Empty = the
    -- remote's default branch.
    ref         TEXT NOT NULL DEFAULT '',
    -- The driver name driver.yaml declares; filled after the first
    -- successful build, and what the registry keys on.
    driver_name TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    added_by    TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (url, subdir)
);

CREATE TABLE driver_builds (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id     UUID NOT NULL REFERENCES driver_repos(id) ON DELETE CASCADE,
    -- The exact commit that was built: what "which code is running" resolves
    -- to, six months later.
    git_commit  TEXT NOT NULL DEFAULT '',
    image_id    TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'queued', -- queued|running|ok|failed
    -- The build log, WHOLE. A failed third-party build is diagnosed from
    -- this text or not at all: the operator has no shell on the controller.
    log         TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX driver_builds_repo ON driver_builds (repo_id, created_at DESC);

CREATE TABLE driver_instances (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id      UUID NOT NULL REFERENCES driver_repos(id) ON DELETE CASCADE,
    build_id     UUID REFERENCES driver_builds(id) ON DELETE SET NULL,
    driver_name  TEXT NOT NULL,
    protocol     INT NOT NULL DEFAULT 0,
    container_id TEXT NOT NULL DEFAULT '',
    -- The address the controller dials the MDP endpoint at (bridge IP:port).
    addr         TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL DEFAULT 'stopped', -- starting|ready|failed|stopped
    error        TEXT NOT NULL DEFAULT '',
    -- What /supports declared at load, verbatim: the UI's capability view
    -- must not cost a driver round trip.
    supports     JSONB NOT NULL DEFAULT '[]',
    info         JSONB NOT NULL DEFAULT '{}',
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo_id)
);
