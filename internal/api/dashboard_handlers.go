package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/modelid"
	"meshcraft/internal/store"
)

// Dashboards: several per site, arranged by the user, one PUBLIC link each.
//
// The screens they replace were two fixed columns of cards compiled into the
// binary (web/src/pages/Dashboard.tsx and Monitoring.tsx). Everything they showed
// survives as a placeable tile; the arrangement became data (migration 0041).
//
// # Rights: three levels, and they are not the same question
//
//	read     viewer      — a dashboard shows what a viewer may already read on
//	                       the site's other screens. Nothing is revealed by the
//	                       arrangement itself.
//	write    operator    — creating, renaming, rearranging, deleting. A
//	                       dashboard belongs to the SITE, not to the person who
//	                       drew it (migration 0041), so rearranging changes what
//	                       everyone sees. That is above a viewer and below the
//	                       privilege of touching a switch.
//	share    site_admin  — minting, rotating and revoking the public link.
//
// The third gap is the one worth defending. Every other write here can be undone
// by the next operator; publishing a link cannot be undone in the same sense —
// once the URL has left the building, revoking is all that is left, and whoever
// received it may already have looked. Handing that to `operator` would mean the
// person allowed to rearrange tiles is the person allowed to make the site's
// state readable by the internet. Those are not the same decision.
//
// # The public route
//
// `GET /api/public/dashboards/{token}` is registered OUTSIDE the session group in
// api.go, and it is the only route in this product that answers without a user.
// What that costs, and what has been done about it, is written at
// handlePublicDashboard. Read that before adding anything to the response.

// --- Input shapes ---

// tileInput: a tile as the browser sends it. Deliberately not store.DashboardTile:
// the stored form carries an `id` the client must not be able to choose, and
// accepting the store type would let a caller aim an insert at another dashboard's
// tile ids the day this becomes an upsert.
type tileInput struct {
	Type    string          `json:"type"`
	X       int             `json:"x"`
	Y       int             `json:"y"`
	W       int             `json:"w"`
	H       int             `json:"h"`
	Options json.RawMessage `json:"options,omitempty"`
}

// dashboardGridColumns: the grid is twelve columns wide, here as in
// web/src/dashboardLayout.ts and in the SQL check constraint of migration 0041.
//
// Three copies, and the duplication is deliberate — each one exists for a
// different attacker. The browser's copy places tiles; this one refuses a payload
// the browser would never have produced; the SQL one refuses what would reach the
// table by any other path. A layer that trusted the one above it would not be a
// layer.
const dashboardGridColumns = 12

// dashboardMaxTiles caps one dashboard's tile count.
//
// Not a display concern: a layout is replaced wholesale on every save
// (SetDashboardTiles), so an uncapped list is an uncapped number of INSERTs in
// one transaction, from a route an operator can call in a loop. Sixty is far
// beyond what fits on a screen at the minimum tile size, and the cap is stated
// rather than implied by the 1 MiB body limit, which would cut a payload
// mid-JSON and report a syntax error for a quantity problem.
const dashboardMaxTiles = 60

// dashboardMaxTileHeight caps a tile's height in grid rows. Width is already
// bounded by the column count; height is not bounded by anything, and a tile 10^6
// rows tall would produce a page whose scrollbar is a rounding error.
const dashboardMaxTileHeight = 24

// dashboardMaxTileOptions caps one tile's options blob, in bytes.
//
// Until ADR-0027 `options` was checked for being valid JSON and nothing else,
// which meant unbounded: the biggest legitimate value in there is a four-digit
// window and a severity, and the route accepted a megabyte of them per tile,
// sixty tiles at a time. Two kilobytes is far past anything a tile needs and
// small enough that the cap is not the 1 MiB body limit reporting a syntax
// error for a quantity problem.
const dashboardMaxTileOptions = 2048

// dashboardMaxTileTitle caps the operator's own title for a tile, in RUNES.
//
// Runes and not bytes, because a title bounded in bytes is a title whose length
// depends on the language it is written in — an accented French title would be
// allowed one count and its Japanese equivalent another, for the same words.
//
// Sixty is not a fit guarantee and must not be read as one: a tile can be two
// grid columns wide, where four words already overflow, and the header truncates
// with CSS. The bound exists so the stored value is a title rather than a
// document (ADR-0027).
const dashboardMaxTileTitle = 60

// valid checks one tile and REFUSES rather than clamps.
//
// Clamping was the first instinct and it is wrong here: a layout is written as a
// whole, so silently moving one tile would return a dashboard that does not match
// what the operator sees on their own screen — and they would then save that back,
// making our correction the new truth. A refusal makes the disagreement visible
// while the operator is still looking at it.
func (in tileInput) valid(w http.ResponseWriter) bool {
	if !store.TileKnown(in.Type) {
		respondErr(w, http.StatusBadRequest, "unknown tile type: "+in.Type)
		return false
	}
	if in.W < 1 || in.H < 1 || in.X < 0 || in.Y < 0 {
		respondErr(w, http.StatusBadRequest, "tile geometry must be positive")
		return false
	}
	if in.X+in.W > dashboardGridColumns {
		respondErr(w, http.StatusBadRequest,
			"tile overflows the "+strconv.Itoa(dashboardGridColumns)+"-column grid")
		return false
	}
	if in.H > dashboardMaxTileHeight {
		respondErr(w, http.StatusBadRequest, "tile is too tall")
		return false
	}
	if len(in.Options) > 0 && !json.Valid(in.Options) {
		respondErr(w, http.StatusBadRequest, "tile options are not valid JSON")
		return false
	}
	if len(in.Options) > dashboardMaxTileOptions {
		respondErr(w, http.StatusBadRequest,
			"tile options exceed "+strconv.Itoa(dashboardMaxTileOptions)+" bytes")
		return false
	}
	if !in.titleValid(w) {
		return false
	}
	return true
}

// titleValid bounds `options.title` — the one key in the blob that the operator
// writes in prose and the interface then DISPLAYS (ADR-0027).
//
// The rest of `options` stays opaque here on purpose: a window or a severity the
// browser invents is a browser bug that shows as a wrong chart, while an
// unbounded displayed string is a different class of thing. So exactly one key
// is read, and a title of the wrong JSON TYPE is refused rather than ignored —
// `{"title": 42}` is a client that has misunderstood the field, and silently
// dropping it would hide that while losing whatever the operator typed.
func (in tileInput) titleValid(w http.ResponseWriter) bool {
	if len(in.Options) == 0 {
		return true
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(in.Options, &bag) != nil {
		// Not an object — an array or a bare scalar. Nothing the UI produces,
		// and it carries no title to check; the size cap above already bounds
		// it, so let it through rather than inventing a shape requirement the
		// store does not have.
		return true
	}
	var raw, ok = bag["title"]
	if !ok {
		return true
	}
	var title string
	if json.Unmarshal(raw, &title) != nil {
		respondErr(w, http.StatusBadRequest, "tile title must be a string")
		return false
	}
	if utf8.RuneCountInString(title) > dashboardMaxTileTitle {
		respondErr(w, http.StatusBadRequest,
			"a tile title is at most "+strconv.Itoa(dashboardMaxTileTitle)+" characters")
		return false
	}
	return true
}

// tilesFrom validates a whole arrangement and converts it. Returns ok=false after
// having responded, like the rest of the package's input helpers.
func tilesFrom(w http.ResponseWriter, in []tileInput) ([]store.DashboardTile, bool) {
	if len(in) > dashboardMaxTiles {
		respondErr(w, http.StatusBadRequest,
			"a dashboard carries at most "+strconv.Itoa(dashboardMaxTiles)+" tiles")
		return nil, false
	}
	var out = make([]store.DashboardTile, 0, len(in))
	var t tileInput
	for _, t = range in {
		if !t.valid(w) {
			return nil, false
		}
		out = append(out, store.DashboardTile{
			Type: t.Type, X: t.X, Y: t.Y, W: t.W, H: t.H, Options: t.Options,
		})
	}
	return out, true
}

// dashboardName trims and bounds a name. Empty is refused on creation and means
// "unchanged" on update (cf. store.UpdateDashboard).
func dashboardName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > 80 {
		name = name[:80]
	}
	return name
}

// requireDashboard loads a dashboard by URL id and checks the caller's rights ON
// ITS SITE.
//
// The site is READ FROM THE ROW, never from the request. That is the same rule as
// every other sub-resource in this API (cf. handleUpdateVlan): a caller who could
// name the site their rights are checked against would only ever name one where
// they have rights.
func (s *Server) requireDashboard(w http.ResponseWriter, r *http.Request,
	minRole string) (store.Dashboard, bool) {
	var d, err = s.Store.GetDashboard(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return store.Dashboard{}, false
	}
	if !s.Auth.CanSite(r.Context(), d.SiteID, minRole) {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return store.Dashboard{}, false
	}
	return d, true
}

// --- Authenticated CRUD ---

// Seed names for the two defaults. English, like the 'Default' VLAN CreateSite
// inserts on every site: they are user data from the first second — renamable,
// deletable — and a name chosen from the creator's browser locale would be the
// wrong one for the next person to open the site.
const (
	defaultDashboardName  = "Dashboard"
	defaultMonitoringName = "Monitoring"
)

// handleListDashboards returns the site's dashboards, tiles included, and seeds
// the two defaults the first time a site is ever asked.
//
// A read handler that writes, which deserves its justification: the first thing
// anyone does with this feature is LOOK at it, so seeding on first write would
// mean a brand-new site shows an empty page and a "create one" button where the
// product promises two ready dashboards. The write happens once per site ever
// (dashboard_seeds, migration 0041), and its failure is not fatal — see below.
func (s *Server) handleListDashboards(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	// Seeding failure does NOT fail the read. A site whose seed transaction lost a
	// race, or whose `dashboard_seeds` row was written but whose dashboards were
	// then deleted, must still be able to show the dashboards it does have — and
	// the alternative, a 500 on the home screen because of a one-off write, would
	// be a worse failure than the missing defaults it reports.
	var err = s.Store.EnsureSiteDashboards(r.Context(), siteID,
		defaultDashboardName, defaultMonitoringName)
	if err != nil {
		slog.Warn("dashboards: seeding the two defaults failed — the site will show "+
			"whatever dashboards it already has, and the seed will be retried on the "+
			"next open unless its marker row was written",
			"site_id", siteID, "err", err)
	}
	var list []store.Dashboard
	list, err = s.Store.ListDashboards(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, list)
}

func (s *Server) handleCreateDashboard(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var in struct {
		Name  string      `json:"name"`
		Tiles []tileInput `json:"tiles"`
	}
	if !decode(w, r, &in) {
		return
	}
	var name = dashboardName(in.Name)
	if name == "" {
		respondErr(w, http.StatusBadRequest, "name required")
		return
	}
	var tiles, tilesOK = tilesFrom(w, in.Tiles)
	if !tilesOK {
		return
	}
	var d, err = s.Store.CreateDashboard(r.Context(), siteID, name, tiles)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusCreated, d)
}

// handleUpdateDashboard renames and/or reorders. Both fields are optional, and
// `position` is a POINTER for the reason RenameSite states about the site domain:
// a plain int cannot distinguish "leave it alone" from "put it first".
func (s *Server) handleUpdateDashboard(w http.ResponseWriter, r *http.Request) {
	var cur, ok = s.requireDashboard(w, r, "operator")
	if !ok {
		return
	}
	var in struct {
		Name     string `json:"name"`
		Position *int   `json:"position"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Position != nil && (*in.Position < 0 || *in.Position > 1000) {
		respondErr(w, http.StatusBadRequest, "position out of range")
		return
	}
	var d, err = s.Store.UpdateDashboard(r.Context(), cur.ID, dashboardName(in.Name),
		in.Position)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, d)
}

// handleSetDashboardTiles replaces the whole arrangement. PUT, not PATCH: what
// travels is the layout entire, and a partial layout has no meaning — see
// store.SetDashboardTiles for why this is not a diff.
func (s *Server) handleSetDashboardTiles(w http.ResponseWriter, r *http.Request) {
	var cur, ok = s.requireDashboard(w, r, "operator")
	if !ok {
		return
	}
	var in struct {
		Tiles []tileInput `json:"tiles"`
	}
	if !decode(w, r, &in) {
		return
	}
	var tiles, tilesOK = tilesFrom(w, in.Tiles)
	if !tilesOK {
		return
	}
	var err = s.Store.SetDashboardTiles(r.Context(), cur.ID, tiles)
	if err != nil {
		fail(w, err)
		return
	}
	var d store.Dashboard
	d, err = s.Store.GetDashboard(r.Context(), cur.ID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDashboard(w http.ResponseWriter, r *http.Request) {
	var cur, ok = s.requireDashboard(w, r, "operator")
	if !ok {
		return
	}
	var err = s.Store.DeleteDashboard(r.Context(), cur.ID)
	if err != nil {
		fail(w, err)
		return
	}
	// A shared dashboard that is deleted takes its link with it, and that is
	// worth a journal line: the link stops working, and whoever holds it will ask
	// why.
	if cur.Shared {
		s.emitSite(r, cur.SiteID, "dashboard.deleted", model.SevNotice,
			"dashboard \""+cur.Name+"\" deleted — its public share link no longer resolves", nil)
	}
	respond(w, http.StatusNoContent, nil)
}

// --- The share link ---

// shareTokenBytes: 32 random bytes, hex-encoded into a 64-character token.
//
// The same size as a session token (auth.NewSessionToken) and for the same
// reason. 256 bits from crypto/rand is not "hard to guess", it is unreachable:
// there is no rate limit low enough to matter and no fleet large enough to make
// a hit likely, so enumeration is not defended by a limiter — it is defended by
// the size of the space, which is the only defence that does not degrade under
// load.
const shareTokenBytes = 32

// newShareToken mints a token and its storage hash.
//
// A local function rather than auth.NewSessionToken, although the two are
// currently identical: that one mints SESSIONS, and a share link is not a
// session — it authenticates nobody and carries no rights beyond one dashboard.
// Sharing the constructor would make a future change to session tokens (a prefix,
// a different length, an expiry encoded in the value) silently change share links
// too, and every live link in every customer's wiki would have to keep working.
func newShareToken() (token string, hash []byte, err error) {
	var raw = make([]byte, shareTokenBytes)
	_, err = rand.Read(raw)
	if err != nil {
		return "", nil, err
	}
	token = hex.EncodeToString(raw)
	return token, auth.HashToken(token), nil
}

// handleShareDashboard mints a public link, or ROTATES the existing one.
//
// One route for both, deliberately. Rotation is not a different operation: it
// writes a new hash where the old one was, so the old link is dead at commit
// (store.SetDashboardShare). Two routes would have implied two states to reason
// about, and the second one would eventually have been written as "keep the old
// one valid for a while" — which is the one thing a revocation must never do.
//
// The clear token is in this response and NOWHERE ELSE, ever. Losing it means
// rotating, which is a `site_admin` gesture and invalidates the copy someone else
// may still hold: that is the correct outcome, not a shortcoming.
func (s *Server) handleShareDashboard(w http.ResponseWriter, r *http.Request) {
	var cur, ok = s.requireDashboard(w, r, "site_admin")
	if !ok {
		return
	}
	var token, hash, err = newShareToken()
	if err != nil {
		fail(w, err)
		return
	}
	var by = auth.UserFrom(r.Context())
	if err = s.Store.SetDashboardShare(r.Context(), cur.ID, hash, by.ID); err != nil {
		fail(w, err)
		return
	}
	var what = "public share link created for dashboard \"" + cur.Name + "\""
	if cur.Shared {
		what = "public share link ROTATED for dashboard \"" + cur.Name +
			"\" — the previous link no longer resolves"
	}
	// SevNotice and a journal line: making a site's state readable without a
	// login is a decision the site's other administrators must be able to find
	// afterwards. The token is NOT in the message, for the obvious reason — the
	// event log is readable by every viewer on the site.
	s.emitSite(r, cur.SiteID, "dashboard.shared", model.SevNotice, what, nil)
	respond(w, http.StatusOK, map[string]any{
		"token": token,
		// The path, so the UI does not rebuild it from a string it guessed. The
		// ORIGIN is deliberately absent: the controller is reached through
		// whatever hostname the operator's browser used, and a server-side guess
		// at its own public URL is wrong exactly when it matters (reverse proxy,
		// split DNS). The browser knows; it prepends its own origin.
		"path": publicDashboardPath(token),
	})
}

// handleUnshareDashboard revokes the public link. Idempotent: revoking a
// dashboard that is not shared succeeds. The gesture's meaning is "there must be
// no public link on this dashboard", and that is true afterwards either way —
// answering 404 to the second click of a nervous administrator would suggest the
// first one failed.
func (s *Server) handleUnshareDashboard(w http.ResponseWriter, r *http.Request) {
	var cur, ok = s.requireDashboard(w, r, "site_admin")
	if !ok {
		return
	}
	var err = s.Store.ClearDashboardShare(r.Context(), cur.ID)
	if err != nil {
		fail(w, err)
		return
	}
	if cur.Shared {
		s.emitSite(r, cur.SiteID, "dashboard.share_revoked", model.SevNotice,
			"public share link revoked for dashboard \""+cur.Name+"\"", nil)
	}
	respond(w, http.StatusNoContent, nil)
}

// publicDashboardPath builds the SPA path a share token opens. One function so
// the Go route, the UI route and this string cannot drift.
func publicDashboardPath(token string) string { return "/share/dashboard/" + token }

// --- The public route ---

// shareTokenLooksValid: exactly 64 lowercase hex characters, which is what
// newShareToken produces and the only shape that can match a stored hash.
//
// Checked BEFORE hashing, not to save the hash — sha256 of a short string is
// free — but to keep the database out of the path of anything that is obviously
// not a token. A scanner walking `/api/public/dashboards/<random garbage>` gets
// no query, which is the difference between a scan that is inert and a scan that
// is load.
func shareTokenLooksValid(token string) bool {
	if len(token) != shareTokenBytes*2 {
		return false
	}
	var i int
	for i = 0; i < len(token); i++ {
		var c = token[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// publicTile: a tile as an anonymous visitor sees it. No `id` — nothing public
// addresses a tile — and no stored identity of any kind.
type publicTile struct {
	Type    string          `json:"type"`
	X       int             `json:"x"`
	Y       int             `json:"y"`
	W       int             `json:"w"`
	H       int             `json:"h"`
	Options json.RawMessage `json:"options,omitempty"`
}

// publicDevice: what the tiles actually draw about a device, and not one field
// more.
//
// This projection is the heart of the public route, so what is ABSENT is worth
// naming:
//
//   - `mgmt_ip` — a management address is a target. The old Monitoring table
//     showed it in the device column; on a share link that column shows the model
//     instead. A wall display in a workshop does not need the address of the
//     switch, and a link forwarded twice must not be a map of the administration
//     VLAN.
//   - `site_id` — the site UUID is the key of every authenticated site route. A
//     public page has no use for it and handing it out gives a visitor the exact
//     parameter they would need if a rights check anywhere were ever wrong.
//   - `extra`, `observed`, `last_apply`, sync and drift state, credential and
//     proxy attachment — configuration and operational internals. No tile reads
//     them.
//
// `port_groups` and `ports` DO travel, because every rate, port count and PoE
// figure on the old screens is computed in the browser from them (see
// web/src/network.ts, siteRates / deviceRates). They carry port labels and
// per-port VLAN intent, which is the same information the VLAN and top-ports
// tiles are there to show.
type publicDevice struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
	// ModelLabel: the canonical name of the hardware (ADR-0014). The raw
	// `Model` travels with it, as everywhere else — a public page shows the
	// identity, and nothing is rewritten to get there.
	ModelLabel string          `json:"model_label,omitempty"`
	Vendor     string          `json:"vendor"`
	Status     string          `json:"status"`
	StatusNote string          `json:"status_note"`
	PoEBudgetW int             `json:"poe_budget_w"`
	PortGroups json.RawMessage `json:"port_groups"`
	Ports      json.RawMessage `json:"ports"`
}

// publicVlan / publicLink / publicEvent: same exercise, one line each.
type publicVlan struct {
	ID     string `json:"id"`
	VlanID int    `json:"vlan_id"`
	Name   string `json:"name"`
	Color  string `json:"color"`
}

type publicLink struct {
	ID      string `json:"id"`
	ADevice string `json:"a_device"`
	BDevice string `json:"b_device"`
	Status  string `json:"status"`
	Note    string `json:"note"`
}

// publicEvent drops two fields the authenticated log carries.
//
// `actor` is an EMAIL ADDRESS — the person who made the change. A share link has
// no audience control, so publishing it would put staff addresses on whatever
// screen the URL ends up on, for a column that says nothing about the network.
// The public event table shows no author.
//
// `payload` is dropped for the same family of reasons: it is free-form JSON
// written by whichever code path emitted the event, so its contents are not
// reviewed against this boundary and cannot be.
type publicEvent struct {
	ID       int64     `json:"id"`
	TS       time.Time `json:"ts"`
	DeviceID *string   `json:"device_id,omitempty"`
	// THE RAW TYPE IS ABSENT TOO, for the same reason as the message.
	//
	// It looked structured and therefore safe, and it is neither: `credentials.rotated`,
	// `device.console_opened` and `serial.published` each tell an anonymous
	// viewer what kind of privileged administrative work is happening and when.
	// A wall display showing "credentials rotated, 14:02" is an operational
	// signal nobody chose to publish, and the set grows every time somebody adds
	// an event type.
	//
	// [publicEventLabel] renders it into a closed vocabulary instead. That
	// mapping is the decision point: adding a case there is a deliberate choice
	// to publish that fact, and everything unmapped reads as "activity".
	Severity int `json:"severity"`
	// THE MESSAGE BODY IS DELIBERATELY ABSENT, and this is the one field on the
	// public view whose omission cost a finding rather than a review comment.
	//
	// The first version passed `Message` through. It projected away `actor` and
	// `payload` — the two fields that obviously carry staff identity — and that
	// looked sufficient, because the leak assertions were written against
	// STRUCTURED fields. Probing the live route found this instead:
	//
	//	Console on "192.0.2.5" opened by admin@meshcraft.local closed after 4s
	//
	// A management address and an operator's email address, on a link that
	// requires no login, from a code path that never touched `actor`.
	//
	// The fix is not to redact that one sentence. Event messages are FREE TEXT
	// composed by dozens of call sites across this codebase — connectors quoting
	// device output, provisioning naming accounts, refusals quoting the address
	// they refused — and there is no invariant anyone can enforce that keeps them
	// free of addresses, hostnames, account names or command transcripts. A
	// filter would have to be right about every message that will ever be
	// written, including ones added after this comment.
	//
	// So the public view carries only what is STRUCTURED and bounded: when,
	// which device, what kind of thing happened, and how serious. That is enough
	// for the "recent activity" tile a wall display wants, and it cannot leak a
	// sentence nobody audited. A viewer who needs the wording logs in.
	//
	// [publicEventLabel] is the deliberate exception: a fixed vocabulary this
	// file controls, never text from the database.
	Label string `json:"label,omitempty"`
}

// publicEventLabel renders an event kind for an anonymous viewer.
//
// A CLOSED VOCABULARY, mapped here, and never the stored message. Adding a case
// is a decision to publish that fact to anyone holding the link; the default is
// deliberately vague rather than falling back to `e.Type`, because event types
// are added freely and a new one would otherwise publish itself.
func publicEventLabel(evType string) string {
	switch evType {
	case "device.up":
		return "device came up"
	case "device.down":
		return "device went down"
	case "device.snmp_up", "device.snmp_down":
		return "collection state changed"
	case "port.up":
		return "port came up"
	case "port.down":
		return "port went down"
	case "sweep.done":
		return "network sweep completed"
	case "host.seen":
		return "new host seen"
	}
	// Everything else — provisioning, credentials, console, serial, apply,
	// spanning tree — is reported as activity and nothing more. The severity and
	// the timestamp still travel, so a tile can still show that something
	// happened and how serious it was.
	return "activity"
}

// publicDashboardView is the whole response. The dashboard's `id`, `site_id` and
// share metadata are all absent: a public visitor addresses the dashboard by
// token and by nothing else.
type publicDashboardView struct {
	Name     string       `json:"name"`
	SiteName string       `json:"site_name"`
	Tiles    []publicTile `json:"tiles"`
	// Datasets: which of the blocks below were computed, so the browser can tell
	// "no devices on this site" from "this dashboard has no tile that reads
	// devices".
	Datasets []string       `json:"datasets"`
	Devices  []publicDevice `json:"devices,omitempty"`
	Vlans    []publicVlan   `json:"vlans,omitempty"`
	Links    []publicLink   `json:"links,omitempty"`
	Events   []publicEvent  `json:"events,omitempty"`
	Traffic  *trafficSeries `json:"traffic,omitempty"`
	// Optics: the low receive-level rows, served AS the authenticated route
	// serves them. No projection needed: device names and IDs are already in
	// publicDevice, the port is a faceplate name, and a dBm level reveals
	// nothing a port state does not.
	Optics *opticsAlertsReply `json:"optics,omitempty"`
}

// publicEventLimit caps the log a share link serves.
//
// The same 300 the old Monitoring page asked for, and it is a cap not a default:
// the public route takes no `limit` parameter at all. Every filter the event-log
// tile offers (severity, type) is applied in the browser over this window, which
// is what the authenticated page did for the severity chips anyway.
const publicEventLimit = 300

// publicTrafficHours: the windows the traffic chart offers, and the ONLY values
// the public route accepts.
//
// An allow-list rather than a range check. The authenticated route clamps
// anything to [1,720] because its caller is a session that could read the series
// store through other doors anyway; here the parameter is the one thing an
// anonymous caller controls, and each distinct value is a distinct query against
// VictoriaMetrics. Four values means at most four cached shapes, no matter how
// many people open the link.
var publicTrafficHours = map[int]bool{24: true, 72: true, 168: true, 720: true}

// handlePublicDashboard serves ONE shared dashboard, to a caller with no session.
//
// # What this route can reach
//
// The dashboard named by the token, its tiles, and — for the site that dashboard
// belongs to — only the datasets those tiles read (store.DatasetsFor), each
// through the projections above. Concretely: device names, models, reachability
// and port state; VLAN names and colours; declared links; the event log's last
// 300 lines without their authors; the aggregate traffic series.
//
// # What this route cannot reach, and why each one is deliberate
//
//   - Any OTHER dashboard, including another one on the same site: the lookup is
//     by token hash alone and there is no parameter to steer it.
//   - Any other SITE: everything below is keyed on `d.SiteID`, read from the row
//     the token resolved to. The request contributes no identifier whatsoever
//     beyond the token itself.
//   - PROXIES. The old alert tile listed proxies with no heartbeat, and proxies
//     are an INSTANCE resource — not site-scoped. Serving them here would leak
//     the shape of the deployment (how many collectors exist, what they are
//     called, which are unhealthy) to whoever holds a link to one site's
//     dashboard. The alert tile on a public dashboard therefore shows device and
//     link alerts only, and says so in the UI rather than silently showing a
//     shorter list.
//   - Anything WRITABLE. This is a GET, it is registered outside the session
//     group and outside `notifyOnWrite`, and it touches no other verb.
//   - Secrets, credentials, management addresses, serial numbers, users. None of
//     the projections above carry a field that could hold one.
//
// # Uniform 404
//
// Malformed token, unknown token, revoked token, rotated token and deleted
// dashboard all produce the same 404 with the same sentence. Distinguishing them
// would answer "did this link ever exist?" for free, which is the only question
// an enumerator can otherwise not answer.
//
// # What a token in a URL costs, said out loud
//
// The token travels in the PATH, which is where a bearer credential in a link
// always travels, and paths end up in places bodies do not: the access log of any
// reverse proxy in front of this controller, a browser's history, a Referer
// header if the page ever linked outward (it does not — every link is inert under
// `publicView`). That is inherent to "a URL anyone can open", not something this
// handler can fix, and it is the reason the gesture is `site_admin`, the reason
// rotation is one click, and the reason `share_last_used_at` exists: the honest
// answer to a leaked link is to notice and rotate it.
//
// This controller itself does not log request paths (Router mounts RealIP,
// Recoverer and Compress — no request logger), so the token is not written to
// meshcraft's own journal by simply being used, and the share event deliberately
// does not carry it.
func (s *Server) handlePublicDashboard(w http.ResponseWriter, r *http.Request) {
	var token = chi.URLParam(r, "token")
	if !shareTokenLooksValid(token) {
		publicNotFound(w)
		return
	}
	var d, err = s.Store.DashboardByShareHash(r.Context(), auth.HashToken(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			publicNotFound(w)
			return
		}
		fail(w, err)
		return
	}
	// Bookkeeping, best effort: "is anyone still using this link?" is the
	// question that decides whether to revoke it, and it cannot be answered from
	// the journal — opening a public page emits no event, by design.
	s.Store.TouchDashboardShare(r.Context(), d.ID)

	var site model.Site
	site, err = s.Store.GetSite(r.Context(), d.SiteID)
	if err != nil {
		fail(w, err)
		return
	}
	var out = publicDashboardView{
		Name:     d.Name,
		SiteName: site.Name,
		Tiles:    publicTilesOf(d.Tiles),
	}
	var need = store.DatasetsFor(d.Tiles)
	out.Datasets = datasetList(need)

	// Devices are fetched once even though three datasets can want them; the
	// traffic series needs them too (it sums per-device port counters), and the
	// optics comparison needs their facades to name ports.
	var devices []model.Device
	if need[store.DatasetDevices] || need[store.DatasetTraffic] ||
		need[store.DatasetOptics] {
		devices, err = s.Store.ListDevices(r.Context(), d.SiteID)
		if err != nil {
			fail(w, err)
			return
		}
	}
	if need[store.DatasetDevices] {
		out.Devices = publicDevicesOf(devices)
	}
	if need[store.DatasetVlans] {
		var vlans []model.Vlan
		vlans, err = s.Store.ListVlans(r.Context(), d.SiteID)
		if err != nil {
			fail(w, err)
			return
		}
		out.Vlans = publicVlansOf(vlans)
	}
	if need[store.DatasetLinks] {
		var links []model.DeviceLink
		links, err = s.Store.ListLinks(r.Context(), d.SiteID)
		if err != nil {
			fail(w, err)
			return
		}
		out.Links = publicLinksOf(links)
	}
	if need[store.DatasetEvents] {
		var events []model.Event
		events, err = s.Store.ListEvents(r.Context(), store.EventFilter{
			SiteID: d.SiteID, Limit: publicEventLimit,
		})
		if err != nil {
			fail(w, err)
			return
		}
		out.Events = publicEventsOf(events)
	}
	if need[store.DatasetTraffic] {
		out.Traffic = s.publicTraffic(r.Context(), devices, r.URL.Query().Get("hours"))
	}
	if need[store.DatasetOptics] {
		// Same sharing rule as publicTraffic: the authorization question was
		// answered by the token lookup, and opticsRxAlerts takes the devices that
		// answer produced rather than re-checking a session it does not have.
		var optics = s.opticsRxAlerts(r.Context(), devices)
		out.Optics = &optics
	}
	// No-store: this page is the site's live state under a permanent URL. A proxy
	// or CDN that cached it would keep serving the fleet's state after the link
	// was revoked — the one thing revocation is supposed to stop.
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, out)
}

// publicNotFound: the single answer for every failure to resolve a token. One
// function so no future branch can invent a more helpful message.
func publicNotFound(w http.ResponseWriter) {
	respondErr(w, http.StatusNotFound, "this share link is unknown or no longer valid")
}

// publicTraffic computes the aggregate series for a public dashboard.
//
// Deliberately NOT a call into handleSiteTraffic: that handler starts by checking
// a session's rights on a site named in the URL, which is exactly what this route
// does not have. Reusing it would have meant giving it a "skip the rights check"
// path — an argument that, once it exists, is one refactor away from being passed
// by mistake from a route that should not have it.
//
// It reuses the parts that carry no authority: the sampling resolution and the
// series read (measuredTraffic). An unmeasured window yields an empty grid, same
// as the authenticated chart — a share link must not invent a flat line where
// nothing was collected.
func (s *Server) publicTraffic(ctx context.Context, devices []model.Device,
	hoursParam string) *trafficSeries {
	var hours, _ = strconv.Atoi(hoursParam)
	if !publicTrafficHours[hours] {
		hours = 24
	}
	var window = time.Duration(hours) * time.Hour
	var step = chartStep(window)
	var points = chartPointCount(window, step)
	var now = time.Now()

	var measured, err = s.measuredTraffic(ctx, devices, now, step, points)
	if err == nil && len(measured) > 0 {
		return &trafficSeries{Hours: hours, Simulated: false, Points: measured}
	}
	var out = trafficSeries{Hours: hours, Simulated: true, Points: []trafficPoint{}}
	var i int
	for i = 0; i < points; i++ {
		out.Points = append(out.Points,
			trafficPoint{TS: now.Add(-time.Duration(points-1-i) * step)})
	}
	return &out
}

// --- Projections ---
//
// Written as free functions taking and returning slices, so a test can exercise
// them without a database: this is where a field leaks if one is added upstream
// without thinking, and the check must be cheap enough to keep.

func publicTilesOf(tiles []store.DashboardTile) []publicTile {
	var out = make([]publicTile, 0, len(tiles))
	var t store.DashboardTile
	for _, t = range tiles {
		out = append(out, publicTile{
			Type: t.Type, X: t.X, Y: t.Y, W: t.W, H: t.H, Options: t.Options,
		})
	}
	return out
}

func publicDevicesOf(devices []model.Device) []publicDevice {
	var out = make([]publicDevice, 0, len(devices))
	var d model.Device
	for _, d = range devices {
		out = append(out, publicDevice{
			ID: d.ID, Name: d.Name, Model: d.Model, Vendor: d.Vendor,
			// The canonical model label, derived here too (ADR-0014): this
			// projection does not pass through withSyncVLANs, and the fleet
			// table is the SAME component as the authenticated one — without
			// it, a shared dashboard would be the one screen still naming one
			// switch three ways.
			ModelLabel: modelid.Of(d.Model).Label,
			Status:     d.Status, StatusNote: d.StatusNote, PoEBudgetW: d.PoEBudgetW,
			PortGroups: d.PortGroups, Ports: d.Ports,
		})
	}
	return out
}

func publicVlansOf(vlans []model.Vlan) []publicVlan {
	var out = make([]publicVlan, 0, len(vlans))
	var v model.Vlan
	for _, v = range vlans {
		out = append(out, publicVlan{ID: v.ID, VlanID: v.VlanID, Name: v.Name, Color: v.Color})
	}
	return out
}

func publicLinksOf(links []model.DeviceLink) []publicLink {
	var out = make([]publicLink, 0, len(links))
	var l model.DeviceLink
	for _, l = range links {
		out = append(out, publicLink{
			ID: l.ID, ADevice: l.ADevice, BDevice: l.BDevice, Status: l.Status, Note: l.Note,
		})
	}
	return out
}

func publicEventsOf(events []model.Event) []publicEvent {
	var out = make([]publicEvent, 0, len(events))
	var e model.Event
	for _, e = range events {
		out = append(out, publicEvent{
			ID: e.ID, TS: e.TS, DeviceID: e.DeviceID,
			Severity: e.Severity, Label: publicEventLabel(e.Type),
		})
	}
	return out
}

// datasetList turns the dataset SET into a stable, sorted list. Sorted because it
// travels in a JSON response that a test compares literally, and a Go map's
// iteration order would make that test flap.
func datasetList(need map[string]bool) []string {
	var all = []string{store.DatasetDevices, store.DatasetVlans, store.DatasetLinks,
		store.DatasetEvents, store.DatasetTraffic, store.DatasetOptics}
	var out = []string{}
	var name string
	for _, name = range all {
		if need[name] {
			out = append(out, name)
		}
	}
	return out
}
