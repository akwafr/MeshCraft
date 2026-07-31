package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Pipelines: state of what meshcraft is doing on the fleet.
// See docs/fr/01-Architecture/Monitoring (unified monitoring and pipelines) §5.

// Pipeline states. `blocked` deserves to exist beside `failed`: it says
// "waiting for something", not "it failed". Conflating them makes you restart
// what does not need restarting.
const (
	PipelinePending = "pending"
	PipelineRunning = "running"
	PipelineBlocked = "blocked"
	PipelineDone    = "done"
	PipelineFailed  = "failed"
)

// PipelineStep is a step as shown to the operator.
type PipelineStep struct {
	Seq    int    `json:"seq"`
	Name   string `json:"name"`
	State  string `json:"state"` // done | failed | skipped
	Detail string `json:"detail,omitempty"`
}

// Pipeline is an end-to-end tracked operation.
type Pipeline struct {
	ID       string  `json:"id"`
	SiteID   string  `json:"site_id"`
	Kind     string  `json:"kind"`
	DeviceID *string `json:"device_id,omitempty"`
	// DeviceName accompanies the ID: a list of UUIDs is unreadable.
	DeviceName    string         `json:"device_name,omitempty"`
	State         string         `json:"state"`
	Label         string         `json:"label"`
	BlockedReason string         `json:"blocked_reason,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	EndedAt       *time.Time     `json:"ended_at,omitempty"`
	Steps         []PipelineStep `json:"steps,omitempty"`
}

const pipelineCols = `p.id::text, p.site_id::text, p.kind, p.device_id::text,
	coalesce(d.name, ''), p.state, p.label, p.blocked_reason, p.created_at, p.ended_at`

func scanPipeline(row pgx.Row) (Pipeline, error) {
	var p Pipeline
	var err = row.Scan(&p.ID, &p.SiteID, &p.Kind, &p.DeviceID, &p.DeviceName,
		&p.State, &p.Label, &p.BlockedReason, &p.CreatedAt, &p.EndedAt)
	return p, notFound(err)
}

// RecordPipeline records a COMPLETED operation, with its steps, in one
// transaction.
//
// Recording at the end rather than incrementally is a choice: current operations
// are synchronous and last a few seconds. Writing each step separately would
// cost one round trip per step on a database that already serves the UI, for
// granularity nobody watches at that scale. A LONG operation will need
// incremental writes — that is when visibility will pay off.
func (s *Store) RecordPipeline(ctx context.Context, p Pipeline, by string) (Pipeline, error) {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var actor any
	if by != "" {
		actor = by
	}
	var id string
	if err = tx.QueryRow(ctx,
		`insert into pipelines (site_id, kind, device_id, state, label, blocked_reason,
		     started_at, ended_at, created_by)
		 values ($1::uuid, $2, nullif($3,'')::uuid, $4, $5, $6, now(), now(), $7::uuid)
		 returning id::text`,
		p.SiteID, p.Kind, derefOr(p.DeviceID, ""), p.State, p.Label, p.BlockedReason,
		actor).Scan(&id); err != nil {
		return Pipeline{}, err
	}
	var i int
	for i = range p.Steps {
		if _, err = tx.Exec(ctx,
			`insert into pipeline_steps (pipeline_id, seq, name, state, detail)
			 values ($1::uuid, $2, $3, $4, $5)`,
			id, i+1, p.Steps[i].Name, p.Steps[i].State, p.Steps[i].Detail); err != nil {
			return Pipeline{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Pipeline{}, err
	}
	p.ID = id
	return p, nil
}

func derefOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

// ListPipelines returns a site's operations, most recent first.
//
// States that CALL FOR ACTION rise to the top, regardless of age: a pipeline
// blocked for two days is more interesting than a success from two minutes ago,
// and the question "what is stuck?" must be answerable without scrolling.
func (s *Store) ListPipelines(ctx context.Context, siteID string, limit int) ([]Pipeline, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows, err = s.pool.Query(ctx,
		`select `+pipelineCols+`
		 from pipelines p left join devices d on d.id = p.device_id
		 where p.site_id = $1::uuid
		 order by (p.state in ('blocked','failed','running')) desc, p.created_at desc
		 limit $2`, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = []Pipeline{}
	for rows.Next() {
		var p Pipeline
		if p, err = scanPipeline(rows); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPipeline returns an operation with step detail.
func (s *Store) GetPipeline(ctx context.Context, id string) (Pipeline, error) {
	var p, err = scanPipeline(s.pool.QueryRow(ctx,
		`select `+pipelineCols+`
		 from pipelines p left join devices d on d.id = p.device_id
		 where p.id = $1::uuid`, id))
	if err != nil {
		return p, err
	}
	var rows pgx.Rows
	if rows, err = s.pool.Query(ctx,
		`select seq, name, state, detail from pipeline_steps
		 where pipeline_id = $1::uuid order by seq`, id); err != nil {
		return p, err
	}
	defer rows.Close()
	p.Steps = []PipelineStep{}
	for rows.Next() {
		var st PipelineStep
		if err = rows.Scan(&st.Seq, &st.Name, &st.State, &st.Detail); err != nil {
			return p, err
		}
		p.Steps = append(p.Steps, st)
	}
	return p, rows.Err()
}
