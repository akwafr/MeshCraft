package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/model"
	"meshcraft/internal/ouidb"
)

// The Clients screen's read: one request, the whole assembled view.
//
// Assembly happens HERE and not in the browser, because three of the columns
// are cross-source joins the browser has no business redoing: the location
// (sightings × links), the identity (OUI registry × LLDP neighbours), and the
// port telemetry (speed and oper status, resolved from the series' device
// spellings onto facade names). What the page keeps for itself is what a
// table view owns: sorting, filtering, refresh cadence.

// clientFreshWindow bounds "online": a client whose location was sighted
// within this window is present. Three FDB cycles — a bridge holds an aged
// entry ~300 s, so a machine that left reads offline here BEFORE the bridge
// itself has forgotten it, and a lost poll or two does not flap the column.
const clientFreshWindow = 3 * time.Minute

// clientView is one row of the page.
type clientView struct {
	MAC string `json:"mac"`
	// Vendor from the OUI registry; empty when unknown. Randomized says the
	// address carries the locally-administered bit — the privacy default of
	// every phone OS — which is an ANSWER, not a registry gap.
	Vendor     string `json:"vendor,omitempty"`
	Randomized bool   `json:"randomized,omitempty"`
	// Name / Description: what the machine SAYS about itself, when it speaks
	// LLDP — the fleet's neighbour tables are the fingerprint source (a
	// Proxmox host announces its hostname, an RB5009 its RouterOS version).
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Where it lives.
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	Port       string `json:"port"`
	VLANs      []int  `json:"vlans"`
	// Elsewhere: distinct other (device,port) pairs this MAC was also sighted
	// on — nonzero means the location is the fewest-neighbours heuristic and
	// the screen should hedge.
	Elsewhere int `json:"elsewhere,omitempty"`
	// Port telemetry at the location.
	SpeedMbps float64 `json:"speed_mbps,omitempty"`
	LinkUp    bool    `json:"link_up"`
	Online    bool    `json:"online"`
	// The ledger.
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
	RxBytes   int64      `json:"rx_bytes"`
	TxBytes   int64      `json:"tx_bytes"`
	BucketAt  *time.Time `json:"bucket_at,omitempty"`
}

// handleListSiteClients renders the site's client list.
//
// `viewer`, like /lldp: who is plugged in where is the same sensitivity class
// as the cabling plan, and it is the same audience reading both.
func (s *Server) handleListSiteClients(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var ctx = r.Context()
	var sightings, err = s.Store.ListMACSightings(ctx, siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var ledger []model.Client
	if ledger, err = s.Store.ListClients(ctx, siteID); err != nil {
		fail(w, err)
		return
	}
	var devices []model.Device
	if devices, err = s.Store.ListDevices(ctx, siteID); err != nil {
		fail(w, err)
		return
	}
	var neighbors []model.LLDPNeighbor
	if neighbors, err = s.Store.ListLLDPNeighbors(ctx, siteID); err != nil {
		fail(w, err)
		return
	}
	var selves []model.LLDPSelf
	if selves, err = s.Store.ListLLDPSelf(ctx, siteID); err != nil {
		fail(w, err)
		return
	}
	var manual []model.DeviceLink
	if manual, err = s.Store.ListLinks(ctx, siteID); err != nil {
		fail(w, err)
		return
	}

	var view = buildLLDPView(neighbors, selves, devices)
	var uplinks = uplinkPorts(view.Links, manual)
	var locations = clientLocations(sightings, uplinks)

	// The ledger indexed; a located MAC missing from it (race between snapshot
	// and read) still renders, with zeroes.
	var byMAC = map[string]model.Client{}
	var i int
	for i = range ledger {
		byMAC[ledger[i].MAC] = ledger[i]
	}
	var names = map[string]string{}
	var byID = map[string]model.Device{}
	for i = range devices {
		names[devices[i].ID] = devices[i].Name
		byID[devices[i].ID] = devices[i]
	}

	// LLDP as fingerprint: what each MAC ANNOUNCED, keyed by its address.
	var announced = map[string][2]string{}
	for i = range neighbors {
		var token = macIdentityToken(neighbors[i].ChassisID)
		if token == "" {
			continue
		}
		var mac = colonMAC(token)
		var prev, has = announced[mac]
		if has && prev[0] != "" {
			continue
		}
		announced[mac] = [2]string{neighbors[i].SysName, neighbors[i].SysDesc}
	}

	// Port telemetry, two instant queries for the whole site.
	var speed, oper = s.clientPortTelemetry(ctx, devices)

	var now = time.Now()
	var out = []clientView{}
	var mac string
	for mac = range locations {
		var loc = locations[mac]
		var row = clientView{
			MAC:      mac,
			DeviceID: loc.DeviceID, DeviceName: names[loc.DeviceID],
			Port: loc.Port, VLANs: loc.VLANs, Elsewhere: loc.Elsewhere,
			FirstSeen: loc.FirstSeen, LastSeen: loc.LastSeen,
			Online: now.Sub(loc.LastSeen) < clientFreshWindow,
		}
		if row.VLANs == nil {
			row.VLANs = []int{}
		}
		var led, inLedger = byMAC[mac]
		if inLedger {
			row.FirstSeen = led.FirstSeen
			row.RxBytes, row.TxBytes = led.RxBytes, led.TxBytes
			row.BucketAt = led.BucketAt
		}
		row.Vendor, _ = ouidb.Vendor(mac)
		row.Randomized = ouidb.LocallyAdministered(mac)
		var said, spoke = announced[mac]
		if spoke {
			row.Name, row.Description = said[0], said[1]
		}
		var key = clientPortKey(loc.DeviceID, loc.Port)
		row.SpeedMbps = speed[key]
		row.LinkUp = oper[key] == 1
		out = append(out, row)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].MAC < out[b].MAC })

	respond(w, http.StatusOK, map[string]any{
		"clients": out,
		"counts": map[string]int{
			"clients":   len(out),
			"sightings": len(sightings),
			// The ledger may hold MACs with no current location — machines gone
			// or currently only visible through uplinks. Counted so "38 known,
			// 25 located" reads as the design rather than as a loss.
			"known": len(ledger),
		},
		"fresh_window_s": int(clientFreshWindow.Seconds()),
	})
}

// The BROOM. `DELETE /api/sites/{siteID}/clients?days=N` forgets every client
// of the site last seen more than N days ago.
//
// It exists because the ledger is append-only by design (cf.
// store.ReplaceFDB): a guest VLAN accumulates one permanent row per phone
// that ever associated with a randomized address, and nothing in the
// collection path will ever take one away. The screen that shows the list is
// the right place to carry its broom.
//
// `operator`, not `viewer` (which the read is) and not `site_admin`: this
// destroys OBSERVATIONS, not configuration — the same class of act as
// clearing a counter, and the same audience. It is traced as an event either
// way, because "the list got shorter" has to have an author.

// clientPruneMaxDays is the ceiling on the window, ten years. Not a business
// rule — a guard: it makes an absurd parameter a 400 instead of a cutoff so
// far in the past that it deletes nothing while reporting success, which is
// the failure that looks like a broken feature.
const clientPruneMaxDays = 3650

// parseClientPruneDays reads the window off the query string.
//
// EVERY REJECTION HERE IS THE SAME REJECTION: a window that is absent, zero,
// negative or unreadable must never fall back to a default, because every
// plausible default of a "clean up" gesture deletes something. An unparsable
// parameter is a caller that does not know what it is asking for, and the
// answer to that is 400 — never "well, a day then".
func parseClientPruneDays(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, errors.New("no window: `days` says how long a client must have " +
			"been absent to be forgotten, and it has no default — every default " +
			"of this gesture would delete something nobody asked for")
	}
	var n, err = strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, errors.New("`days` must be a whole number of days, got \"" + raw + "\"")
	}
	if n < 1 {
		return 0, errors.New("`days` must be at least 1: a window of zero or less " +
			"would forget clients seen a moment ago, including every one currently online")
	}
	if n > clientPruneMaxDays {
		return 0, errors.New("`days` is capped at " + strconv.Itoa(clientPruneMaxDays) +
			" (ten years): a longer window forgets nothing and reads as a broken button")
	}
	return n, nil
}

// clientPruneCutoff turns a window into the instant a client must have been
// seen after to survive. Separate from the parse so the arithmetic is read
// once: the window is in DAYS and days are 24 h here, deliberately — a
// calendar-aware subtraction would make "3 days" mean a different thing twice
// a year for no gain on a threshold whose point is to be coarse.
func clientPruneCutoff(days int, now time.Time) time.Time {
	return now.Add(-time.Duration(days) * 24 * time.Hour)
}

func (s *Server) handlePruneSiteClients(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var days, err = parseClientPruneDays(r.URL.Query().Get("days"))
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var cutoff = clientPruneCutoff(days, time.Now())
	var clients, sightings int64
	if clients, sightings, err = s.Store.PruneClients(r.Context(), siteID, cutoff); err != nil {
		fail(w, err)
		return
	}
	// Severity NOTICE and the numbers in the message: this is the only trace
	// that a list shrank, and whoever reads the journal later needs the window
	// as well as the count — "28 forgotten" alone cannot be judged.
	s.emitSite(r, siteID, "clients.pruned", model.SevNotice,
		"Clients not seen for "+strconv.Itoa(days)+" day(s) forgotten: "+
			strconv.FormatInt(clients, 10)+" from the ledger, "+
			strconv.FormatInt(sightings, 10)+" stale sighting(s).",
		map[string]any{"days": days, "clients": clients, "sightings": sightings})
	respond(w, http.StatusOK, map[string]any{
		"days":      days,
		"clients":   clients,
		"sightings": sightings,
	})
}

// colonMAC renders a 12-hex-digit token as the canonical colon spelling.
func colonMAC(token string) string {
	if len(token) != 12 {
		return ""
	}
	var out = make([]byte, 0, 17)
	var i int
	for i = 0; i < 12; i += 2 {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, token[i], token[i+1])
	}
	return string(out)
}

// clientPortTelemetry reads the freshest speed and oper status of every port
// of the given devices, resolved onto facade names. Best-effort: without a
// series store the two maps stay empty and the columns render as unknown.
func (s *Server) clientPortTelemetry(ctx context.Context,
	devices []model.Device) (map[string]float64, map[string]float64) {
	var speed = map[string]float64{}
	var oper = map[string]float64{}
	if s.TSDB == nil || len(devices) == 0 {
		return speed, oper
	}
	var ids = make([]string, 0, len(devices))
	var byID = map[string]model.Device{}
	var i int
	for i = range devices {
		ids = append(ids, devices[i].ID)
		byID[devices[i].ID] = devices[i]
	}
	var matcher = `{device=~"` + strings.Join(ids, "|") + `"}`
	var fill = func(query string, into map[string]float64) {
		var series, err = s.TSDB.QueryInstant(ctx, query)
		if err != nil {
			return
		}
		var j int
		for j = range series {
			var dev = series[j].Labels["device"]
			var raw = series[j].Labels["port"]
			var d, knownDev = byID[dev]
			if !knownDev || raw == "" {
				continue
			}
			// The series carry the DEVICE's port spelling; the sightings carry
			// facade names. Same resolution as everywhere else.
			var name = facadeOf(d).resolve(raw)
			if name == "" {
				continue
			}
			var p, has = series[j].Last()
			if !has {
				continue
			}
			into[clientPortKey(dev, name)] = p.Value
		}
	}
	fill("last_over_time("+metricbuf.MetricIfSpeedMbps+matcher+"[15m])", speed)
	fill("last_over_time("+metricbuf.MetricIfOperStatus+matcher+"[15m])", oper)
	return speed, oper
}
