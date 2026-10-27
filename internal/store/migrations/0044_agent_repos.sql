-- Agent repositories and their builds.
--
-- The SAME declarative pattern as driver_repos (0043), with one structural
-- difference: an agent is NEVER run on the controller. A driver's product is a
-- container the controller starts (driver_instances); an agent's product is an
-- OCI LAYOUT on disk that a remote proxy pulls through the built-in registry.
-- So there is no agent_instances table — the live state of an agent is the
-- layout under OCIDir/<agent_name>/, and the DB keeps the declaration and the
-- build history that produced it.

CREATE TABLE agent_repos (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Any git URL the host can clone.
    url         TEXT NOT NULL,
    -- Subdirectory holding the agent's Dockerfile; empty means the root.
    subdir      TEXT NOT NULL DEFAULT '',
    -- Git ref to build; empty = the remote's default branch.
    ref         TEXT NOT NULL DEFAULT '',
    -- The repository name the built image is served under, e.g.
    -- meshcraft/agent-foo. This is what the registry keys on and what a proxy
    -- pulls; it must be a valid registry path (no "." or ".." segments, which
    -- the registry refuses anyway).
    agent_name  TEXT NOT NULL,
    -- The version (OCI tag) of the most recent successful build — a UTC build
    -- stamp. What a proxy pins to; empty until the first build lands.
    current_version TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    added_by    TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (url, subdir),
    UNIQUE (agent_name)
);

CREATE TABLE agent_builds (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id     UUID NOT NULL REFERENCES agent_repos(id) ON DELETE CASCADE,
    -- The exact commit built: "which code is this image" resolved months later.
    git_commit  TEXT NOT NULL DEFAULT '',
    image_id    TEXT NOT NULL DEFAULT '',
    -- The OCI tag this build was written under (its version stamp). The layout
    -- at OCIDir/<agent_name>/ answers to it.
    version     TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'queued', -- queued|running|ok|failed
    -- The build log, WHOLE: an operator with no shell on the controller
    -- diagnoses a failed third-party build from this text or not at all.
    log         TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX agent_builds_repo ON agent_builds (repo_id, created_at DESC);
