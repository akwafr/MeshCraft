package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// External driver repositories → builds → running instances (migration 0043).
// The shape is pipelines': declaration, job history with its whole log, live
// state reconciled at startup.

// DriverRepo is one registered source of a driver.
type DriverRepo struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	Subdir     string    `json:"subdir,omitempty"`
	Ref        string    `json:"ref,omitempty"`
	DriverName string    `json:"driver_name,omitempty"`
	Enabled    bool      `json:"enabled"`
	AddedBy    string    `json:"added_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DriverBuild is one build attempt, log included: a failed third-party build
// is diagnosed from that text or not at all.
type DriverBuild struct {
	ID         string     `json:"id"`
	RepoID     string     `json:"repo_id"`
	GitCommit  string     `json:"git_commit,omitempty"`
	ImageID    string     `json:"image_id,omitempty"`
	State      string     `json:"state"`
	Log        string     `json:"log,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// DriverInstance is the container the controller runs for a repo, and what
// its /supports declared at load.
type DriverInstance struct {
	ID          string          `json:"id"`
	RepoID      string          `json:"repo_id"`
	BuildID     *string         `json:"build_id,omitempty"`
	DriverName  string          `json:"driver_name"`
	Protocol    int             `json:"protocol"`
	ContainerID string          `json:"container_id,omitempty"`
	Addr        string          `json:"addr,omitempty"`
	State       string          `json:"state"`
	Error       string          `json:"error,omitempty"`
	Supports    json.RawMessage `json:"supports"`
	Info        json.RawMessage `json:"info"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

const driverRepoCols = `id, url, subdir, ref, driver_name, enabled, added_by,
	created_at, updated_at`

func scanDriverRepo(row pgx.Row) (DriverRepo, error) {
	var r DriverRepo
	var err = row.Scan(&r.ID, &r.URL, &r.Subdir, &r.Ref, &r.DriverName,
		&r.Enabled, &r.AddedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// ListDriverRepos returns every registered repository, stable order.
func (s *Store) ListDriverRepos(ctx context.Context) ([]DriverRepo, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+driverRepoCols+` from driver_repos order by created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []DriverRepo{}
	for rows.Next() {
		var r, scanErr = scanDriverRepo(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddDriverRepo registers a repository. The (url, subdir) pair is unique —
// registering the same driver twice is a mistake worth a plain error.
func (s *Store) AddDriverRepo(ctx context.Context, url, subdir, ref, by string) (DriverRepo, error) {
	return scanDriverRepo(s.pool.QueryRow(ctx,
		`insert into driver_repos (url, subdir, ref, added_by)
		 values ($1, $2, $3, $4) returning `+driverRepoCols,
		url, subdir, ref, by))
}

// GetDriverRepo fetches one repository.
func (s *Store) GetDriverRepo(ctx context.Context, id string) (DriverRepo, error) {
	return scanDriverRepo(s.pool.QueryRow(ctx,
		`select `+driverRepoCols+` from driver_repos where id = $1::uuid`, id))
}

// SetDriverRepoEnabled flips the switch; disabling is what "unload" means at
// the declarative layer — the reconciler stops the container.
func (s *Store) SetDriverRepoEnabled(ctx context.Context, id string, enabled bool) error {
	var _, err = s.pool.Exec(ctx,
		`update driver_repos set enabled = $2, updated_at = now() where id = $1::uuid`,
		id, enabled)
	return err
}

// SetDriverRepoName records what driver.yaml declared, after the first
// successful build.
func (s *Store) SetDriverRepoName(ctx context.Context, id, name string) error {
	var _, err = s.pool.Exec(ctx,
		`update driver_repos set driver_name = $2, updated_at = now() where id = $1::uuid`,
		id, name)
	return err
}

// DeleteDriverRepo removes the repository, its builds and its instance row
// (cascade). Stopping the container is the reconciler's half.
func (s *Store) DeleteDriverRepo(ctx context.Context, id string) error {
	var _, err = s.pool.Exec(ctx, `delete from driver_repos where id = $1::uuid`, id)
	return err
}

const driverBuildCols = `id, repo_id, git_commit, image_id, state, log, error,
	started_at, finished_at, created_at`

func scanDriverBuild(row pgx.Row) (DriverBuild, error) {
	var b DriverBuild
	var err = row.Scan(&b.ID, &b.RepoID, &b.GitCommit, &b.ImageID, &b.State,
		&b.Log, &b.Error, &b.StartedAt, &b.FinishedAt, &b.CreatedAt)
	return b, err
}

// CreateDriverBuild queues a build.
func (s *Store) CreateDriverBuild(ctx context.Context, repoID string) (DriverBuild, error) {
	return scanDriverBuild(s.pool.QueryRow(ctx,
		`insert into driver_builds (repo_id) values ($1::uuid)
		 returning `+driverBuildCols, repoID))
}

// StartDriverBuild marks the build running.
func (s *Store) StartDriverBuild(ctx context.Context, id string) error {
	var _, err = s.pool.Exec(ctx,
		`update driver_builds set state = 'running', started_at = now()
		 where id = $1::uuid`, id)
	return err
}

// FinishDriverBuild lands the outcome, log included — whole, because it is
// the only diagnostic surface an operator has.
func (s *Store) FinishDriverBuild(ctx context.Context, id, state, gitCommit,
	imageID, log, errText string) error {
	var _, err = s.pool.Exec(ctx,
		`update driver_builds set state = $2, git_commit = $3, image_id = $4,
		    log = $5, error = $6, finished_at = now()
		 where id = $1::uuid`,
		id, state, gitCommit, imageID, log, errText)
	return err
}

// LatestDriverBuild returns a repo's most recent build, or pgx.ErrNoRows.
func (s *Store) LatestDriverBuild(ctx context.Context, repoID string) (DriverBuild, error) {
	return scanDriverBuild(s.pool.QueryRow(ctx,
		`select `+driverBuildCols+` from driver_builds
		 where repo_id = $1::uuid order by created_at desc limit 1`, repoID))
}

const driverInstCols = `id, repo_id, build_id, driver_name, protocol,
	container_id, addr, state, error, supports, info, updated_at`

func scanDriverInstance(row pgx.Row) (DriverInstance, error) {
	var i DriverInstance
	var err = row.Scan(&i.ID, &i.RepoID, &i.BuildID, &i.DriverName, &i.Protocol,
		&i.ContainerID, &i.Addr, &i.State, &i.Error, &i.Supports, &i.Info,
		&i.UpdatedAt)
	return i, err
}

// UpsertDriverInstance records the (single) instance a repo runs as. Keyed by
// repo: one repo, one container, by design.
func (s *Store) UpsertDriverInstance(ctx context.Context, inst DriverInstance) (DriverInstance, error) {
	if len(inst.Supports) == 0 {
		inst.Supports = json.RawMessage(`[]`)
	}
	if len(inst.Info) == 0 {
		inst.Info = json.RawMessage(`{}`)
	}
	return scanDriverInstance(s.pool.QueryRow(ctx,
		`insert into driver_instances (repo_id, build_id, driver_name, protocol,
		    container_id, addr, state, error, supports, info)
		 values ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 on conflict (repo_id) do update set
		    build_id = excluded.build_id, driver_name = excluded.driver_name,
		    protocol = excluded.protocol, container_id = excluded.container_id,
		    addr = excluded.addr, state = excluded.state, error = excluded.error,
		    supports = excluded.supports, info = excluded.info, updated_at = now()
		 returning `+driverInstCols,
		inst.RepoID, inst.BuildID, inst.DriverName, inst.Protocol,
		inst.ContainerID, inst.Addr, inst.State, inst.Error,
		inst.Supports, inst.Info))
}

// ListDriverInstances returns the live layer, joined by the reconciler at
// startup against what Docker actually runs.
func (s *Store) ListDriverInstances(ctx context.Context) ([]DriverInstance, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+driverInstCols+` from driver_instances order by driver_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []DriverInstance{}
	for rows.Next() {
		var i, scanErr = scanDriverInstance(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SetDriverInstanceState flips the live state with its reason.
func (s *Store) SetDriverInstanceState(ctx context.Context, id, state, errText string) error {
	var _, err = s.pool.Exec(ctx,
		`update driver_instances set state = $2, error = $3, updated_at = now()
		 where id = $1::uuid`, id, state, errText)
	return err
}
