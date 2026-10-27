package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

// What is locked here is the BOUNDARY of the public route, and nothing else: the
// scope computation that decides which datasets an anonymous visitor triggers,
// the token shape that keeps garbage away from the database, and the projections
// that decide which fields leave the building. All three are pure, deliberately
// (cf. proxyscope.go doctrine: the refusal that protects the fleet is taken in a
// function a test can call without a database).

// A dashboard made only of stat tiles must not cause an event query, and a
// dashboard with no traffic chart must never reach the series store. This is the
// property that makes "it serves that dashboard's tiles and their data, and
// nothing else" true rather than aspirational.
func TestPublicScopeFollowsTheTiles(t *testing.T) {
	var cases = []struct {
		name  string
		tiles []store.DashboardTile
		want  []string
	}{
		{
			name:  "empty dashboard reads nothing",
			tiles: nil,
			want:  []string{},
		},
		{
			name:  "one stat tile reads devices only",
			tiles: []store.DashboardTile{{Type: "stat.active-ports"}},
			want:  []string{store.DatasetDevices},
		},
		{
			name:  "the traffic chart reads the series and nothing else",
			tiles: []store.DashboardTile{{Type: "chart.traffic"}},
			want:  []string{store.DatasetTraffic},
		},
		{
			name: "the alert list needs devices AND links",
			tiles: []store.DashboardTile{
				{Type: "list.alerts"}, {Type: "stat.alerts"},
			},
			want: []string{store.DatasetDevices, store.DatasetLinks},
		},
		{
			name:  "an unknown type contributes nothing and does not fail",
			tiles: []store.DashboardTile{{Type: "tile.from.a.newer.version"}},
			want:  []string{},
		},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got = datasetList(store.DatasetsFor(c.tiles))
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("datasets = %v, want %v", got, c.want)
			}
		})
	}
}

// The token shape. Anything that is not exactly what newShareToken mints is
// refused before a query is made — a scanner walking the route is inert.
func TestShareTokenShape(t *testing.T) {
	var token, hash, err = newShareToken()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !shareTokenLooksValid(token) {
		t.Fatalf("a freshly minted token was rejected: %q", token)
	}
	if len(token) != 64 {
		t.Fatalf("token length = %d, want 64 hex characters (256 bits)", len(token))
	}
	// The stored value is the sha256 of the token, not the token: a database dump
	// must not yield a working URL.
	if len(hash) != 32 || string(hash) == token {
		t.Fatalf("stored value is not a sha256 hash")
	}
	var same = auth.HashToken(token)
	if string(same) != string(hash) {
		t.Fatalf("hash is not auth.HashToken — the lookup would never match")
	}
	// Two mints must not collide in any way a human would notice.
	var other, _, _ = newShareToken()
	if other == token {
		t.Fatalf("two mints produced the same token")
	}

	var bad = []string{
		"", "short",
		strings.Repeat("g", 64), // not hex
		strings.Repeat("A", 64), // uppercase: not what we mint
		strings.Repeat("a", 63), // one short
		strings.Repeat("a", 65), // one long
		"../../api/sites",       // path traversal shapes
		token + "?x=1",          // trailing junk
	}
	var b string
	for _, b = range bad {
		if shareTokenLooksValid(b) {
			t.Fatalf("token %q was accepted", b)
		}
	}
}

// The projections. What is asserted is ABSENCE: the fields that must never leave
// through this door. A field added upstream to model.Device does not appear here
// unless someone adds it to publicDevice on purpose.
func TestPublicProjectionsDropWhatMustNotTravel(t *testing.T) {
	var siteID = "11111111-1111-1111-1111-111111111111"
	var devices = []model.Device{{
		ID:         "d1",
		SiteID:     siteID,
		Name:       "sw-core",
		Model:      "S3900-24T4S",
		MgmtIP:     "192.0.2.4",
		Status:     "ok",
		PortGroups: json.RawMessage(`[{"prefix":"Gi","count":2}]`),
		Ports:      json.RawMessage(`{"Gi1":{"link":"up"}}`),
		Extra:      json.RawMessage(`{"serial":"SECRET-SERIAL"}`),
		Observed:   json.RawMessage(`{"whatever":true}`),
	}}
	var raw, err = json.Marshal(publicDevicesOf(devices))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var body = string(raw)
	var forbidden = []string{"192.0.2.4", "SECRET-SERIAL", siteID, "observed", "extra"}
	var f string
	for _, f = range forbidden {
		if strings.Contains(body, f) {
			t.Fatalf("public device projection leaked %q: %s", f, body)
		}
	}
	// And it does carry what the tiles compute from.
	if !strings.Contains(body, "sw-core") || !strings.Contains(body, "port_groups") {
		t.Fatalf("public device projection dropped what the tiles need: %s", body)
	}

	var actor = "operator@example.com"
	var events = []model.Event{{
		ID: 7, Type: "port.down", Severity: model.SevWarning, Message: "Gi1 down",
		Actor: actor, Payload: json.RawMessage(`{"internal":"detail"}`),
	}}
	raw, err = json.Marshal(publicEventsOf(events))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body = string(raw)
	// An email address on a page with no audience control is the one that matters.
	if strings.Contains(body, actor) || strings.Contains(body, "internal") {
		t.Fatalf("public event projection leaked its actor or payload: %s", body)
	}
	// AND THE MESSAGE BODY MUST NOT TRAVEL EITHER.
	//
	// This assertion used to require the opposite — it checked that "Gi1 down"
	// WAS present — and that is how the leak shipped. Dropping `actor` and
	// `payload` looked sufficient because both obviously carry staff identity,
	// and the test was written against those structured fields. Probing the
	// deployed route found what the structure could not show:
	//
	//	Console on "192.0.2.5" opened by admin@meshcraft.local closed after 4s
	//
	// A management address and an operator's email, on a link requiring no
	// login, from a code path that never touched `actor`.
	//
	// Event messages are free text composed by dozens of call sites — connectors
	// quoting device output, provisioning naming accounts, refusals quoting the
	// address they refused. No invariant keeps them free of addresses, hostnames
	// or account names, least of all one enforced by a test written today about
	// messages written later. So the public view carries the closed-vocabulary
	// label instead, and this asserts the body is gone.
	if strings.Contains(body, "Gi1 down") {
		t.Fatalf("public event projection carried the free-text message body: %s", body)
	}
	if !strings.Contains(body, "port went down") {
		t.Fatalf("public event projection carries no label to show instead: %s", body)
	}
	// A type this file does not map must NOT publish itself. Event types are
	// added freely, and a default that fell back to `e.Type` would put every
	// future one on an anonymous page without anybody deciding to.
	var unknown = []model.Event{{ID: 8, Type: "credentials.rotated", Severity: model.SevNotice,
		Message: "rotated mcadmin on 192.0.2.5"}}
	raw, err = json.Marshal(publicEventsOf(unknown))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body = string(raw)
	if strings.Contains(body, "credentials.rotated") || strings.Contains(body, "192.0.2.5") ||
		strings.Contains(body, "mcadmin") {
		t.Fatalf("an unmapped event type published itself: %s", body)
	}
}

// The two seeded defaults must reproduce the two old pages, and every tile in the
// catalogue must be placed by one of them: that was the requirement — no tile
// that existed before this feature may be lost on the way.
func TestDefaultDashboardsCoverTheWholeCatalogue(t *testing.T) {
	var placed = map[string]bool{}
	var d store.Dashboard
	for _, d = range store.DefaultDashboards(defaultDashboardName, defaultMonitoringName) {
		var tile store.DashboardTile
		for _, tile = range d.Tiles {
			if !store.TileKnown(tile.Type) {
				t.Fatalf("default dashboard %q places unknown tile %q", d.Name, tile.Type)
			}
			if placed[tile.Type] {
				t.Fatalf("tile %q is placed twice across the defaults", tile.Type)
			}
			placed[tile.Type] = true
			// The same geometry rule the API enforces on input, applied to what we
			// ship: a default that the write path would refuse is a default nobody
			// can save back after moving one tile.
			var in = tileInput{Type: tile.Type, X: tile.X, Y: tile.Y, W: tile.W, H: tile.H}
			if in.X < 0 || in.Y < 0 || in.W < 1 || in.H < 1 ||
				in.X+in.W > dashboardGridColumns || in.H > dashboardMaxTileHeight {
				t.Fatalf("default tile %q has a geometry the API would refuse: %+v",
					tile.Type, tile)
			}
		}
	}
	var name string
	var spec store.TileSpec
	for name, spec = range store.TileCatalog {
		// A TARGETED tile is exempt, and the exemption is the catalogue's own
		// statement rather than a list kept here (ADR-0027): it means nothing
		// until it is pointed at a device and a port, so seeded onto a
		// brand-new site it would be an empty frame asking a question. The
		// price — it is discoverable only through the tile picker — is paid
		// knowingly, which is why the flag is declared beside the tile and not
		// skipped in a test nobody reads.
		if spec.Targeted {
			if placed[name] {
				t.Fatalf("targeted tile %q is seeded on a default dashboard: "+
					"it has no target there, so it would greet every new site "+
					"with an empty frame", name)
			}
			continue
		}
		if !placed[name] {
			t.Fatalf("catalogue tile %q is on neither default dashboard: it would be "+
				"invisible to anyone who never opens the tile picker", name)
		}
	}
}

// What `options` is allowed to carry — ADR-0027.
//
// This is the admission check for a column that was, until that decision,
// validated as "is it JSON" and nothing else. It is worth a test precisely
// because the column is OPAQUE: nothing else in the build will ever complain
// that a displayed field stopped being bounded.
func TestTileOptionsAdmission(t *testing.T) {
	var cases = []struct {
		name    string
		tile    tileInput
		wantErr string // "" = must be accepted
	}{
		{
			name: "the targeted tile is in the catalogue",
			tile: tileInput{Type: "chart.port-traffic", X: 0, Y: 0, W: 6, H: 4,
				Options: json.RawMessage(`{"device_id":"a58cfc06-0950-4f26-918a-ac996e321dbc","port":"XG3"}`)},
		},
		{
			name: "a title is a string, and a short one is fine",
			tile: tileInput{Type: "chart.traffic", W: 7, H: 3,
				Options: json.RawMessage(`{"title":"Server room uplink","hours":168}`)},
		},
		{
			name: "no options at all",
			tile: tileInput{Type: "chart.traffic", W: 7, H: 3},
		},
		{
			// The browser deletes the key rather than storing "", but the route
			// is not the browser's: an empty title is a string of length zero
			// and there is nothing to refuse about it.
			name: "an empty title is accepted, not refused",
			tile: tileInput{Type: "chart.traffic", W: 7, H: 3,
				Options: json.RawMessage(`{"title":""}`)},
		},
		{
			name: "a title past the bound is refused",
			tile: tileInput{Type: "chart.traffic", W: 7, H: 3,
				Options: json.RawMessage(`{"title":"` +
					strings.Repeat("a", dashboardMaxTileTitle+1) + `"}`)},
			wantErr: "characters",
		},
		{
			// Counted in RUNES: sixty accented characters is sixty characters,
			// whatever they weigh in UTF-8. A byte count would refuse this.
			name: "a title of sixty accented characters is within the bound",
			tile: tileInput{Type: "chart.traffic", W: 7, H: 3,
				Options: json.RawMessage(`{"title":"` +
					strings.Repeat("日", dashboardMaxTileTitle) + `"}`)},
		},
		{
			name: "a title that is not a string is refused rather than ignored",
			tile: tileInput{Type: "chart.traffic", W: 7, H: 3,
				Options: json.RawMessage(`{"title":42}`)},
			wantErr: "must be a string",
		},
		{
			name: "an oversized options blob is refused",
			tile: tileInput{Type: "chart.traffic", W: 7, H: 3,
				Options: json.RawMessage(`{"pad":"` +
					strings.Repeat("x", dashboardMaxTileOptions) + `"}`)},
			wantErr: "bytes",
		},
		{
			name:    "an unknown tile type is still refused",
			tile:    tileInput{Type: "chart.invented", W: 2, H: 1},
			wantErr: "unknown tile type",
		},
	}

	var tc = cases[0]
	for _, tc = range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec = httptest.NewRecorder()
			var ok = tc.tile.valid(rec)
			if tc.wantErr == "" {
				if !ok {
					t.Fatalf("refused a tile that must be accepted: %s", rec.Body.String())
				}
				return
			}
			if ok {
				t.Fatalf("accepted a tile that must be refused")
			}
			if !strings.Contains(rec.Body.String(), tc.wantErr) {
				t.Fatalf("refused for the wrong reason: want %q, got %s",
					tc.wantErr, rec.Body.String())
			}
		})
	}
}

// The share path is built in one place, so the Go route, the SPA route and the
// copied link cannot drift.
func TestPublicDashboardPath(t *testing.T) {
	if publicDashboardPath("abc") != "/share/dashboard/abc" {
		t.Fatalf("share path changed: %q", publicDashboardPath("abc"))
	}
}
