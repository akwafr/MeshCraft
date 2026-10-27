package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Dashboards: several per site, arranged by the user, one public link each.
// See migration 0041 for the schema reasoning and internal/api/dashboard_handlers.go
// for the rights model and for what the public route may and may not reach.

// --- The tile catalogue ---
//
// # Why it lives in the store and not in the API package
//
// Three things need the same table and MUST NOT hold three copies of it:
//
//   - the default dashboards seeded on every site (below) name tile types;
//   - the API refuses an unknown tile type on write, so the catalogue is the
//     admission list;
//   - the PUBLIC route decides what to read from the database from the tile
//     types a shared dashboard carries — and that decision is the boundary of
//     what an unauthenticated visitor can obtain.
//
// The third is the reason this is not "product knowledge that belongs in the UI
// layer". A second, drifting copy of "which data does this tile need" would not
// break a build: it would silently serve a dataset to a public link whose
// dashboard has no tile to display it. One table, consulted by all three.
//
// The RENDERING of a tile is of course not here — that is React's business
// (web/src/dashboardTiles.tsx). What is here is the contract the server must
// enforce: this type exists, and it needs exactly these datasets.

// Dataset names. A tile declares what it must be fed; nothing else is fetched.
const (
	// DatasetDevices: the site's devices — the fleet table, every stat tile that
	// counts ports or watts, the device half of the alert list.
	DatasetDevices = "devices"
	// DatasetVlans: VLAN definitions, for the per-VLAN port breakdown and for the
	// colours the top-ports bars use.
	DatasetVlans = "vlans"
	// DatasetLinks: DECLARED inter-device links, for the link half of the alert
	// list.
	DatasetLinks = "links"
	// DatasetEvents: the site event log.
	DatasetEvents = "events"
	// DatasetTraffic: the aggregated site throughput series.
	DatasetTraffic = "traffic"
	// DatasetOptics: the low receive-level list — cages whose current RX sits
	// below the lowest healthy level their own history shows. Computed from the
	// series store, one comparison per cage (cf. internal/api,
	// opticsalerts_handlers.go).
	DatasetOptics = "optics"
)

// TileSpec: what the server must know about one tile type.
//
// Deliberately minimal — no label, no icon, no default size. Those belong to the
// UI and change with taste; what is written here changes only when a tile starts
// reading something new, which is precisely the event that must be reviewed.
type TileSpec struct {
	// Needs: the datasets this tile reads. A tile that reads nothing (there is
	// none today) would carry an empty list and cost a public visitor nothing.
	Needs []string
	// Targeted: this tile means nothing until the operator points it AT
	// something — a device and a port, carried in its options (ADR-0027).
	//
	// The one thing the server does with this is EXEMPT THE TILE FROM THE
	// SEEDING REQUIREMENT. Both default dashboards together place every tile in
	// the catalogue, which is tested, and the test exists so that no tile is
	// invisible to someone who never opens the tile picker. A targeted tile has
	// nothing to show on a brand-new site: seeded with no target it would greet
	// every new install with an empty frame asking a question. So it is not
	// seeded, and the price is stated in the ADR — it is found through the
	// picker or not at all.
	Targeted bool
}

// TileCatalog: every placeable tile type.
//
// The keys are a WIRE FORMAT: they are stored in `dashboard_tiles.tile_type` and
// travel to the browser. Renaming one orphans the tile on every dashboard that
// placed it, so they are chosen to age well — "what it shows", not "where it used
// to live".
//
// The first ten reproduce the old Dashboard page, the last seven the old
// Monitoring page. Every tile that existed before this table is in here: that was
// the requirement, and it is checked by dashboardDefaultsCoverCatalog in
// dashboards_test.go.
var TileCatalog = map[string]TileSpec{
	// --- Stat tiles, ex-Dashboard ---
	"stat.devices-online": {Needs: []string{DatasetDevices}},
	"stat.active-ports":   {Needs: []string{DatasetDevices}},
	"stat.rx":             {Needs: []string{DatasetDevices}},
	"stat.tx":             {Needs: []string{DatasetDevices}},
	"stat.poe":            {Needs: []string{DatasetDevices}},
	// The alert COUNT needs the same two sources as the alert LIST: a number that
	// counted only device alerts would contradict the list next to it.
	"stat.alerts": {Needs: []string{DatasetDevices, DatasetLinks}},

	// --- Charts and lists, ex-Dashboard ---
	"chart.traffic":    {Needs: []string{DatasetTraffic}},
	"chart.vlan-ports": {Needs: []string{DatasetDevices, DatasetVlans}},
	"list.alerts":      {Needs: []string{DatasetDevices, DatasetLinks}},
	"list.events":      {Needs: []string{DatasetEvents}},

	// --- Stat tiles, ex-Monitoring ---
	"stat.unreachable": {Needs: []string{DatasetDevices}},
	"stat.events-24h":  {Needs: []string{DatasetEvents}},
	"stat.warnings":    {Needs: []string{DatasetEvents}},
	"stat.last-event":  {Needs: []string{DatasetEvents}},

	// --- Tables and chart, ex-Monitoring ---
	"table.fleet":     {Needs: []string{DatasetDevices}},
	"chart.top-ports": {Needs: []string{DatasetDevices, DatasetVlans}},
	"table.event-log": {Needs: []string{DatasetEvents, DatasetDevices}},

	// --- Added after the two default pages ---

	// list.low-bandwidth: ports UP at 10 or 100 Mbit/s.
	//
	// Needs devices ALONE: the negotiated speed rides on the port record
	// (`link`), so no telemetry series is queried and a dashboard carrying only
	// this tile touches nothing else. That matters for the public route, whose
	// scope is exactly the union of what its tiles need.
	"list.low-bandwidth": {Needs: []string{DatasetDevices}},

	// list.low-rx: SFP cages receiving less light than they ever have.
	//
	// Its own dataset rather than a devices projection: the rows come from two
	// series-store queries and a comparison, which the public route must be able
	// to serve — and to NOT run — per dashboard, exactly like traffic.
	"list.low-rx": {Needs: []string{DatasetOptics}},

	// chart.port-traffic: what ONE port, chosen by the operator, is carrying.
	//
	// Needs devices ALONE, and the curve is not in that dataset: the series
	// come from `GET /api/devices/{id}/telemetry`, which the browser fetches
	// per device and which requires a session. What `devices` buys is the half
	// that must work without it — resolving the target. The tile has to be able
	// to say "that device is gone" and "that port is no longer on the faceplate"
	// (ADR-0027), and both of those are read off the device record.
	//
	// Consequence on the PUBLIC route, deliberate: a shared dashboard names the
	// port this tile points at and says the curve needs a session. Serving
	// per-port telemetry to whoever holds a share link would widen that route
	// from "this site's dashboard" to "any port's history, one request at a
	// time" — the same reasoning that keeps proxies off the public projection.
	"chart.port-traffic": {Needs: []string{DatasetDevices}, Targeted: true},
}

// TileKnown reports whether a tile type is in the catalogue. The API's admission
// check, and the reason `tile_type` is free text in SQL rather than an enum.
func TileKnown(tileType string) bool {
	var _, ok = TileCatalog[tileType]
	return ok
}

// DatasetsFor returns the SET of datasets a list of tiles needs, deduplicated.
//
// THIS FUNCTION IS THE PUBLIC ROUTE'S SCOPE. An unauthenticated visitor obtains
// exactly what the tiles on the dashboard they were given read, and nothing
// beside it: a dashboard made of four stat tiles causes no event query, and a
// dashboard with no traffic chart never touches the series store.
//
// Unknown types are skipped rather than rejected. The API refuses them on write,
// so a row carrying one is a row from a version that knew a tile this binary
// does not — a downgrade. Refusing to serve the whole dashboard because of it
// would turn a cosmetic mismatch into a blank screen; skipping serves the rest
// and feeds the unknown tile nothing, which is what the browser will do with it
// anyway.
func DatasetsFor(tiles []DashboardTile) map[string]bool {
	var out = map[string]bool{}
	var t DashboardTile
	for _, t = range tiles {
		var spec, ok = TileCatalog[t.Type]
		if !ok {
			continue
		}
		var d string
		for _, d = range spec.Needs {
			out[d] = true
		}
	}
	return out
}

// --- Types ---

// DashboardTile is one placed tile: a catalogue type and where it sits.
type DashboardTile struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// X, Y, W, H: cells in a 12-column grid, top-left anchored. Discrete on
	// purpose — cf. web/src/dashboardLayout.ts, which explains why this product
	// places tiles in cells rather than at pixel coordinates.
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
	// Options: per-tile settings, opaque to the store — and since ADR-0027 no
	// longer only settings. Three kinds of thing live in here, and the third is
	// the one to know about:
	//
	//   - closed enumerations the API bounds because the PUBLIC route acts on
	//     them (the traffic window, which decides what is asked of the series
	//     store);
	//   - `title`, a string the OPERATOR typed. Not an i18n key, never
	//     translated, bounded by the API because it is displayed;
	//   - `device_id` and `port` on a targeted tile: a REFERENCE no foreign key
	//     protects. Deleting a device leaves tiles pointing at it, and that is
	//     the accepted behaviour — the tile survives and says its target is
	//     gone, rather than vanishing and leaving an unexplained hole in
	//     somebody's arrangement.
	Options json.RawMessage `json:"options,omitempty"`
}

// Dashboard is one arrangement, with its tiles.
//
// The share fields say whether a public link EXISTS and since when — never the
// token. There is no method on this store that returns a token, because there is
// no column holding one (cf. migration 0041).
type Dashboard struct {
	ID       string `json:"id"`
	SiteID   string `json:"site_id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
	// Shared: a public link exists. Derived from share_token_hash being non-null;
	// the hash itself never leaves the database.
	Shared          bool            `json:"shared"`
	ShareCreatedAt  *time.Time      `json:"share_created_at,omitempty"`
	ShareLastUsedAt *time.Time      `json:"share_last_used_at,omitempty"`
	Tiles           []DashboardTile `json:"tiles"`
}

const dashboardCols = `id::text, site_id::text, name, position,
	share_token_hash is not null, share_created_at, share_last_used_at`

func scanDashboard(row pgx.Row) (Dashboard, error) {
	var d Dashboard
	var err = row.Scan(&d.ID, &d.SiteID, &d.Name, &d.Position, &d.Shared,
		&d.ShareCreatedAt, &d.ShareLastUsedAt)
	if err != nil {
		return Dashboard{}, notFound(err)
	}
	d.Tiles = []DashboardTile{}
	return d, nil
}

// --- Reads ---

// ListDashboards returns a site's dashboards IN THE ORDER the operator arranged
// them, tiles included.
//
// Two queries and not one join: a join would repeat the dashboard row once per
// tile, and the tile count is the larger number here. Two queries also keep the
// "dashboard with no tile" case free — a freshly created, empty dashboard —
// which an inner join would have dropped and an outer join would have made
// scannable only through nullable columns.
func (s *Store) ListDashboards(ctx context.Context, siteID string) ([]Dashboard, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+dashboardCols+` from dashboards where site_id = $1::uuid
		 order by position, name, id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = []Dashboard{}
	var index = map[string]int{}
	for rows.Next() {
		var d, scanErr = scanDashboard(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		index[d.ID] = len(out)
		out = append(out, d)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	var tileRows pgx.Rows
	tileRows, err = s.pool.Query(ctx,
		`select t.id::text, t.dashboard_id::text, t.tile_type, t.x, t.y, t.w, t.h, t.options
		   from dashboard_tiles t
		   join dashboards d on d.id = t.dashboard_id
		  where d.site_id = $1::uuid
		  order by t.y, t.x, t.id`, siteID)
	if err != nil {
		return nil, err
	}
	defer tileRows.Close()
	for tileRows.Next() {
		var t DashboardTile
		var dashID string
		if err = tileRows.Scan(&t.ID, &dashID, &t.Type, &t.X, &t.Y, &t.W, &t.H,
			&t.Options); err != nil {
			return nil, err
		}
		var at, ok = index[dashID]
		if !ok {
			continue
		}
		out[at].Tiles = append(out[at].Tiles, t)
	}
	return out, tileRows.Err()
}

// GetDashboard reads one dashboard by id, tiles included. The API calls this
// before every write on `/api/dashboards/{id}`: the site it belongs to is what
// the rights check needs, and the caller does not get to supply it.
func (s *Store) GetDashboard(ctx context.Context, id string) (Dashboard, error) {
	var d, err = scanDashboard(s.pool.QueryRow(ctx,
		`select `+dashboardCols+` from dashboards where id = $1::uuid`, id))
	if err != nil {
		return Dashboard{}, err
	}
	d.Tiles, err = s.dashboardTiles(ctx, id)
	if err != nil {
		return Dashboard{}, err
	}
	return d, nil
}

func (s *Store) dashboardTiles(ctx context.Context, dashboardID string) ([]DashboardTile, error) {
	var rows, err = s.pool.Query(ctx,
		`select id::text, tile_type, x, y, w, h, options from dashboard_tiles
		  where dashboard_id = $1::uuid order by y, x, id`, dashboardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []DashboardTile{}
	for rows.Next() {
		var t DashboardTile
		if err = rows.Scan(&t.ID, &t.Type, &t.X, &t.Y, &t.W, &t.H, &t.Options); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- Writes ---

// CreateDashboard creates a dashboard with its tiles, in one transaction.
//
// Position is chosen here rather than by the caller: it goes last. A caller
// picking its own would race with a concurrent creation on the same site and
// produce two dashboards claiming the same slot — harmless in appearance, and
// then the tab order flips between two page loads for no visible reason.
func (s *Store) CreateDashboard(ctx context.Context, siteID, name string,
	tiles []DashboardTile) (Dashboard, error) {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var d Dashboard
	d, err = scanDashboard(tx.QueryRow(ctx,
		`insert into dashboards (site_id, name, position)
		 values ($1::uuid, $2,
		         coalesce((select max(position) + 1 from dashboards where site_id = $1::uuid), 0))
		 returning `+dashboardCols, siteID, name))
	if err != nil {
		return Dashboard{}, err
	}
	if err = insertTiles(ctx, tx, d.ID, tiles); err != nil {
		return Dashboard{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Dashboard{}, err
	}
	d.Tiles, err = s.dashboardTiles(ctx, d.ID)
	if err != nil {
		return Dashboard{}, err
	}
	return d, nil
}

// UpdateDashboard renames and/or moves a dashboard in the tab order.
//
// Both parameters are OPTIONAL in the same way `RenameSite` handles the site
// domain: an empty name keeps the current one, a nil position keeps the current
// slot. A rename must not be able to reset the order as a side effect, and
// reordering must not require the caller to send back a name it never touched —
// which is how a concurrent rename gets silently reverted.
func (s *Store) UpdateDashboard(ctx context.Context, id, name string,
	position *int) (Dashboard, error) {
	var d, err = scanDashboard(s.pool.QueryRow(ctx,
		`update dashboards
		    set name = coalesce(nullif($2, ''), name),
		        position = coalesce($3, position),
		        updated_at = now()
		  where id = $1::uuid
		 returning `+dashboardCols, id, name, position))
	if err != nil {
		return Dashboard{}, err
	}
	d.Tiles, err = s.dashboardTiles(ctx, id)
	if err != nil {
		return Dashboard{}, err
	}
	return d, nil
}

// SetDashboardTiles REPLACES the whole arrangement, in one transaction.
//
// Delete-then-insert, and not a diff. A diff would need stable tile identity
// across a drag — which the browser has no reason to preserve when the operator
// removes a tile and adds another of the same type — and would leave the door
// open to a half-applied layout: some tiles moved, some not, with nothing on
// screen saying which. A layout is one value; it is written as one value.
//
// The cost is a new `id` for every tile at each save. Nothing depends on those
// ids beyond the React key for the current render, and that is deliberate: the
// day something does depend on them, this comment says what it costs to keep
// them.
func (s *Store) SetDashboardTiles(ctx context.Context, dashboardID string,
	tiles []DashboardTile) error {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tag, delErr = tx.Exec(ctx,
		`delete from dashboard_tiles where dashboard_id = $1::uuid`, dashboardID)
	if delErr != nil {
		return delErr
	}
	// A dashboard that exists but has no tile deletes zero rows, so the delete
	// count says nothing about existence. The update below does: it is what makes
	// a layout write on a deleted dashboard a 404 rather than a silent success.
	_ = tag
	var upd pgconn.CommandTag
	upd, err = tx.Exec(ctx,
		`update dashboards set updated_at = now() where id = $1::uuid`, dashboardID)
	if err != nil {
		return err
	}
	if upd.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err = insertTiles(ctx, tx, dashboardID, tiles); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// insertTiles writes a whole arrangement. Shared by creation and layout
// replacement so the two cannot disagree about what a stored tile looks like.
func insertTiles(ctx context.Context, tx pgx.Tx, dashboardID string,
	tiles []DashboardTile) error {
	var t DashboardTile
	for _, t = range tiles {
		var opts = t.Options
		if len(opts) == 0 {
			opts = json.RawMessage(`{}`)
		}
		var _, err = tx.Exec(ctx,
			`insert into dashboard_tiles (dashboard_id, tile_type, x, y, w, h, options)
			 values ($1::uuid, $2, $3, $4, $5, $6, $7)`,
			dashboardID, t.Type, t.X, t.Y, t.W, t.H, []byte(opts))
		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteDashboard removes a dashboard, its tiles (cascade) and its public link
// (same cascade: the token hash lives on the row). A shared dashboard that is
// deleted takes its link with it — the URL stops resolving immediately, which is
// the only acceptable behaviour for a link that pointed at data now gone.
func (s *Store) DeleteDashboard(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx, `delete from dashboards where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// --- The public link ---

// SetDashboardShare stores (or REPLACES) the share token hash, and returns
// nothing: the clear token exists only in the caller's hand, and only until it
// is shown. Same contract as SetSocksToken, for the same reason.
//
// Replacement IS rotation. The previous hash is overwritten in place, so at the
// instant this commits the old link matches no row and is dead — no grace
// period, no second valid token. That is what "rotating must invalidate the old
// link immediately" means, and it is why there is no history table.
func (s *Store) SetDashboardShare(ctx context.Context, id string, hash []byte,
	createdBy string) error {
	var by *string
	if createdBy != "" {
		by = &createdBy
	}
	var tag, err = s.pool.Exec(ctx,
		`update dashboards
		    set share_token_hash = $2, share_created_at = now(),
		        share_created_by = $3::uuid, share_last_used_at = null,
		        updated_at = now()
		  where id = $1::uuid`, id, hash, by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ClearDashboardShare revokes the public link. The row survives; only the hash
// goes. Everything that made the link work is in that one column, so clearing it
// is the whole revocation — there is no cached copy anywhere else to invalidate.
func (s *Store) ClearDashboardShare(ctx context.Context, id string) error {
	var tag, err = s.pool.Exec(ctx,
		`update dashboards
		    set share_token_hash = null, share_created_at = null,
		        share_created_by = null, share_last_used_at = null, updated_at = now()
		  where id = $1::uuid`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DashboardByShareHash resolves a public link. THE ONLY read path an
// unauthenticated request can trigger.
//
// It takes a HASH, never a token: the comparison happens in the index, on a
// value that a database dump would not let anyone replay as a URL. It matches on
// `share_token_hash` alone — no site, no id, nothing the caller could steer —
// so a visitor holding a token for dashboard A cannot reach dashboard B by
// adding a parameter, because there is no parameter to add.
//
// ErrNotFound covers unknown, revoked and rotated alike, and the caller must
// keep it that way: distinguishing them would turn this route into an oracle for
// which links used to exist (cf. SocksTokenFor, same rule).
func (s *Store) DashboardByShareHash(ctx context.Context, hash []byte) (Dashboard, error) {
	var d, err = scanDashboard(s.pool.QueryRow(ctx,
		`select `+dashboardCols+` from dashboards where share_token_hash = $1`, hash))
	if err != nil {
		return Dashboard{}, err
	}
	d.Tiles, err = s.dashboardTiles(ctx, d.ID)
	if err != nil {
		return Dashboard{}, err
	}
	return d, nil
}

// TouchDashboardShare records that the public link was opened. Best-effort by
// design, like TouchSocksToken: a bookkeeping write must never be able to fail
// the page it is bookkeeping for.
func (s *Store) TouchDashboardShare(ctx context.Context, id string) {
	_, _ = s.pool.Exec(ctx,
		`update dashboards set share_last_used_at = now() where id = $1::uuid`, id)
}

// --- The two defaults ---

// DefaultDashboards returns the two dashboards every site starts with: the old
// Dashboard page and the old Monitoring page, tile for tile.
//
// They are ORDINARY dashboards from the first second — renamable, rearrangeable,
// deletable. Nothing in the schema marks them, and that is the point: a "default"
// flag would eventually mean "this one is protected", and the requirement was the
// opposite.
//
// The geometry is the 12-column grid of web/src/dashboardLayout.ts, laid out to
// reproduce what the two pages looked like: a row of stat tiles across the top,
// then the wide content. Row heights are grid cells, not pixels; a stat tile is
// one cell tall because that is what its content is.
func DefaultDashboards(dashboardName, monitoringName string) []Dashboard {
	return []Dashboard{
		{
			Name: dashboardName,
			Tiles: []DashboardTile{
				// Six stat tiles across the top, two columns each — the old
				// `.tile-row`, which fitted six on a wide screen.
				{Type: "stat.devices-online", X: 0, Y: 0, W: 2, H: 1},
				{Type: "stat.active-ports", X: 2, Y: 0, W: 2, H: 1},
				{Type: "stat.rx", X: 4, Y: 0, W: 2, H: 1},
				{Type: "stat.tx", X: 6, Y: 0, W: 2, H: 1},
				{Type: "stat.poe", X: 8, Y: 0, W: 2, H: 1},
				{Type: "stat.alerts", X: 10, Y: 0, W: 2, H: 1},
				// Then the two `.dash-grid` rows, 7/5 and 6/6 as they were.
				{Type: "chart.traffic", X: 0, Y: 1, W: 7, H: 3},
				{Type: "chart.vlan-ports", X: 7, Y: 1, W: 5, H: 3},
				{Type: "list.alerts", X: 0, Y: 4, W: 6, H: 3},
				{Type: "list.events", X: 6, Y: 4, W: 6, H: 3},
			},
		},
		{
			Name: monitoringName,
			Tiles: []DashboardTile{
				{Type: "stat.unreachable", X: 0, Y: 0, W: 3, H: 1},
				{Type: "stat.events-24h", X: 3, Y: 0, W: 3, H: 1},
				{Type: "stat.warnings", X: 6, Y: 0, W: 3, H: 1},
				{Type: "stat.last-event", X: 9, Y: 0, W: 3, H: 1},
				{Type: "table.fleet", X: 0, Y: 1, W: 7, H: 4},
				{Type: "chart.top-ports", X: 7, Y: 1, W: 5, H: 4},
				{Type: "table.event-log", X: 0, Y: 5, W: 12, H: 5},
				// Low-bandwidth ports: on the MONITORING default rather than the
				// Dashboard one, because it is a fault-finding list and not a
				// summary — the same reason table.fleet and the event log live
				// here. Below the log, since a fleet usually has none of these and
				// a tile that is normally empty should not push the log down.
				{Type: "list.low-bandwidth", X: 0, Y: 10, W: 6, H: 3},
				// Low receive levels: the optical twin of low-bandwidth — same
				// fault-finding nature, same "usually empty, so below the log"
				// placement, side by side with it.
				{Type: "list.low-rx", X: 6, Y: 10, W: 6, H: 3},
			},
		},
	}
}

// EnsureSiteDashboards seeds a site's two defaults, ONCE EVER.
//
// The `dashboard_seeds` row is the memory, and it is inserted in the SAME
// transaction as the dashboards: a crash between the two would either re-seed
// forever or never seed at all, and both are worse than the write failing.
//
// `on conflict do nothing` on that row makes the whole thing idempotent AND
// race-safe: two browsers opening the page of a fresh site at the same instant
// both run this, one inserts the marker, the other's insert affects no row, sees
// it, and rolls back before creating a second pair of dashboards. Without that
// check the site would end up with four.
//
// Called from a READ handler (the dashboard list), which is unusual enough to
// justify: seeding cannot wait for a write, because the first thing a user does
// with this feature is look at it, and a site showing zero dashboards on first
// open would look broken. The write happens once per site in the product's
// lifetime.
func (s *Store) EnsureSiteDashboards(ctx context.Context, siteID string,
	dashboardName, monitoringName string) error {
	// Fast path, and it carries almost all the traffic: after the first open, a
	// site is seeded forever, so every subsequent page load would otherwise pay a
	// transaction, an insert that touches nothing, and a rollback — for a question
	// a primary-key lookup answers. The check is NOT the guarantee (two first
	// opens can both pass it); the `on conflict` below is. This only keeps the
	// steady state cheap.
	var seeded bool
	var err = s.pool.QueryRow(ctx,
		`select exists(select 1 from dashboard_seeds where site_id = $1::uuid)`,
		siteID).Scan(&seeded)
	if err != nil {
		return err
	}
	if seeded {
		return nil
	}

	var tx pgx.Tx
	tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tag pgconn.CommandTag
	tag, err = tx.Exec(ctx,
		`insert into dashboard_seeds (site_id) values ($1::uuid)
		 on conflict (site_id) do nothing`, siteID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Already seeded — possibly by the transaction running right now in
		// another connection. Nothing to do, and nothing to report: this is the
		// ordinary case, on every page load after the first.
		return nil
	}

	var d Dashboard
	var position int
	for position, d = range DefaultDashboards(dashboardName, monitoringName) {
		var id string
		err = tx.QueryRow(ctx,
			`insert into dashboards (site_id, name, position)
			 values ($1::uuid, $2, $3) returning id::text`,
			siteID, d.Name, position).Scan(&id)
		if err != nil {
			return err
		}
		if err = insertTiles(ctx, tx, id, d.Tiles); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
