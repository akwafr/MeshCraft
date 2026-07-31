package store

import (
	"context"
	"strconv"

	"meshcraft/internal/model"
)

// InsertEvent appends a row to the event log (append-only).
func (s *Store) InsertEvent(ctx context.Context, ev model.Event) error {
	var _, err = s.pool.Exec(ctx,
		`insert into events (site_id, device_id, port_name, type, severity, message, actor, payload)
		 values ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8)`,
		ev.SiteID, ev.DeviceID, ev.PortName, ev.Type, ev.Severity, ev.Message, ev.Actor,
		jsonOr(ev.Payload, `{}`))
	return err
}

type EventFilter struct {
	// Empty SiteID = INSTANCE events (site_id null): proxy lifecycle, discovery,
	// audit. They belong to no site and therefore cannot appear in a site log.
	SiteID      string
	DeviceID    string
	Type        string // prefix: "config." matches config.applied, config.imported…
	SeverityMin int
	Limit       int
}

func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]model.Event, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	var q = `select id, ts, site_id::text, device_id::text, port_name, type, severity,
	           message, actor, payload
	         from events where site_id = $1::uuid and severity >= $2`
	var args = []any{f.SiteID, f.SeverityMin}
	if f.SiteID == "" {
		q = `select id, ts, site_id::text, device_id::text, port_name, type, severity,
		       message, actor, payload
		     from events where site_id is null and severity >= $1`
		args = []any{f.SeverityMin}
	}
	if f.DeviceID != "" {
		args = append(args, f.DeviceID)
		q += ` and device_id = $` + strconv.Itoa(len(args)) + `::uuid`
	}
	if f.Type != "" {
		args = append(args, f.Type+"%")
		q += ` and type like $` + strconv.Itoa(len(args))
	}
	args = append(args, f.Limit)
	q += ` order by ts desc, id desc limit $` + strconv.Itoa(len(args))

	var rows, err = s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.Event{}
	for rows.Next() {
		var ev model.Event
		var payload []byte
		if err = rows.Scan(&ev.ID, &ev.TS, &ev.SiteID, &ev.DeviceID, &ev.PortName,
			&ev.Type, &ev.Severity, &ev.Message, &ev.Actor, &payload); err != nil {
			return nil, err
		}
		ev.Payload = payload
		out = append(out, ev)
	}
	return out, rows.Err()
}

// CountEventsBySeverity returns event counts by severity over the last `hours`
// (feeds dashboard counters).
func (s *Store) CountEventsBySeverity(ctx context.Context, siteID string, hours int) (map[int]int, error) {
	var rows, err = s.pool.Query(ctx,
		`select severity, count(*) from events
		 where site_id = $1::uuid and ts > now() - make_interval(hours => $2)
		 group by severity`, siteID, hours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = map[int]int{}
	for rows.Next() {
		var sev, n int
		if err = rows.Scan(&sev, &n); err != nil {
			return nil, err
		}
		out[sev] = n
	}
	return out, rows.Err()
}
