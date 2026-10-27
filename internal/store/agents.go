package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Agent repositories and their builds — the git→OCI-layout pipeline's
// persistence. The shape mirrors driver_repos (drivers.go), minus the
// instances: an agent is pulled by a remote proxy, never run on the
// controller, so its live state is the layout on disk, not a container.

// AgentRepo is a registered agent source.
type AgentRepo struct {
	ID             string    `json:"id"`
	URL            string    `json:"url"`
	Subdir         string    `json:"subdir,omitempty"`
	Ref            string    `json:"ref,omitempty"`
	AgentName      string    `json:"agent_name"`
	CurrentVersion string    `json:"current_version,omitempty"`
	Enabled        bool      `json:"enabled"`
	AddedBy        string    `json:"added_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// AgentBuild is one build attempt, log included.
type AgentBuild struct {
	ID         string     `json:"id"`
	RepoID     string     `json:"repo_id"`
	GitCommit  string     `json:"git_commit,omitempty"`
	ImageID    string     `json:"image_id,omitempty"`
	Version    string     `json:"version,omitempty"`
	State      string     `json:"state"`
	Log        string     `json:"log,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

const agentRepoCols = `id, url, subdir, ref, agent_name, current_version,
	enabled, added_by, created_at, updated_at`

func scanAgentRepo(row pgx.Row) (AgentRepo, error) {
	var r AgentRepo
	var err = row.Scan(&r.ID, &r.URL, &r.Subdir, &r.Ref, &r.AgentName,
		&r.CurrentVersion, &r.Enabled, &r.AddedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// ListAgentRepos returns every registered agent repository, stable order.
func (s *Store) ListAgentRepos(ctx context.Context) ([]AgentRepo, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+agentRepoCols+` from agent_repos order by created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []AgentRepo{}
	for rows.Next() {
		var r, scanErr = scanAgentRepo(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetAgentRepo returns one repository, or pgx.ErrNoRows.
func (s *Store) GetAgentRepo(ctx context.Context, id string) (AgentRepo, error) {
	return scanAgentRepo(s.pool.QueryRow(ctx,
		`select `+agentRepoCols+` from agent_repos where id = $1::uuid`, id))
}

// AddAgentRepo registers a repository. (url, subdir) and agent_name are both
// unique — registering the same source, or the same image name, twice is a
// mistake worth a plain error rather than a second silent layout.
func (s *Store) AddAgentRepo(ctx context.Context, url, subdir, ref, agentName, by string) (AgentRepo, error) {
	return scanAgentRepo(s.pool.QueryRow(ctx,
		`insert into agent_repos (url, subdir, ref, agent_name, added_by)
		 values ($1, $2, $3, $4, $5) returning `+agentRepoCols,
		url, subdir, ref, agentName, by))
}

// SetAgentRepoEnabled toggles whether the reconciler builds this repository.
func (s *Store) SetAgentRepoEnabled(ctx context.Context, id string, enabled bool) error {
	var _, err = s.pool.Exec(ctx,
		`update agent_repos set enabled = $2, updated_at = now() where id = $1::uuid`,
		id, enabled)
	return err
}

// SetAgentRepoVersion records the version of the most recent successful build —
// what a proxy pins to.
func (s *Store) SetAgentRepoVersion(ctx context.Context, id, version string) error {
	var _, err = s.pool.Exec(ctx,
		`update agent_repos set current_version = $2, updated_at = now() where id = $1::uuid`,
		id, version)
	return err
}

// DeleteAgentRepo removes a repository and its build history (cascade). The
// on-disk layout is the caller's to remove — the store owns rows, not files.
func (s *Store) DeleteAgentRepo(ctx context.Context, id string) error {
	var _, err = s.pool.Exec(ctx, `delete from agent_repos where id = $1::uuid`, id)
	return err
}

const agentBuildCols = `id, repo_id, git_commit, image_id, version, state,
	log, error, started_at, finished_at, created_at`

func scanAgentBuild(row pgx.Row) (AgentBuild, error) {
	var b AgentBuild
	var err = row.Scan(&b.ID, &b.RepoID, &b.GitCommit, &b.ImageID, &b.Version,
		&b.State, &b.Log, &b.Error, &b.StartedAt, &b.FinishedAt, &b.CreatedAt)
	return b, err
}

// CreateAgentBuild opens a queued build for a repository.
func (s *Store) CreateAgentBuild(ctx context.Context, repoID string) (AgentBuild, error) {
	return scanAgentBuild(s.pool.QueryRow(ctx,
		`insert into agent_builds (repo_id) values ($1::uuid)
		 returning `+agentBuildCols, repoID))
}

// StartAgentBuild marks the build running.
func (s *Store) StartAgentBuild(ctx context.Context, id string) error {
	var _, err = s.pool.Exec(ctx,
		`update agent_builds set state = 'running', started_at = now()
		 where id = $1::uuid`, id)
	return err
}

// FinishAgentBuild lands the outcome, log included.
func (s *Store) FinishAgentBuild(ctx context.Context, id, state, gitCommit,
	imageID, version, log, errText string) error {
	var _, err = s.pool.Exec(ctx,
		`update agent_builds set state = $2, git_commit = $3, image_id = $4,
		    version = $5, log = $6, error = $7, finished_at = now()
		 where id = $1::uuid`,
		id, state, gitCommit, imageID, version, log, errText)
	return err
}

// ListAgentBuilds returns a repository's builds, newest first, capped.
func (s *Store) ListAgentBuilds(ctx context.Context, repoID string, limit int) ([]AgentBuild, error) {
	if limit <= 0 {
		limit = 20
	}
	var rows, err = s.pool.Query(ctx,
		`select `+agentBuildCols+` from agent_builds
		 where repo_id = $1::uuid order by created_at desc limit $2`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []AgentBuild{}
	for rows.Next() {
		var b, scanErr = scanAgentBuild(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
