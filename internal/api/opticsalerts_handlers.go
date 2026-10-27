package api

import (
	"context"
	"net/http"
	"sort"

	"meshcraft/internal/model"
)

// LOW RECEIVE LEVELS: GET /api/sites/{siteID}/optics/rx-alerts.
//
// The dashboard's optical fault list: every cage whose receive level sits
// materially below the lowest healthy level it has ever shown. The RULE — what
// counts as a reference, what counts as dark, what counts as fresh — lives with
// the query composition in series.go (opticsAlertMarginDB and neighbours),
// because the three constants and the two queries only mean anything together.
//
// # Why the comparison happens HERE and not in PromQL
//
// The two instant queries could be joined store-side (`a < b - 1`), but the
// rows still need facade resolution — the store's `port` label is the
// platform's own spelling ("Ethernet34"), the screen's vocabulary is the
// faceplate's ("ET34"), and only the controller holds the facade. Since every
// row must come back anyway to be renamed, the join might as well be readable
// Go instead of a PromQL `on(device,port,lane)` clause.
//
// # What an empty answer means
//
// Same three-way split as the telemetry route, and the field names match:
// `available: false` is "this instance has no series store"; available with no
// rows is the good morning — no fibre is below its own history.

// opticsAlertRow is one flagged cage.
type opticsAlertRow struct {
	DeviceID string `json:"device_id"`
	Device   string `json:"device"`
	// Port is the FACEPLATE name — resolved, never the store label.
	Port string `json:"port"`
	// Lane: the transceiver channel, "" on single-reading producers and "0" on
	// a single-lane module read per channel. The screen shows it only when it
	// is not the single-lane case.
	Lane string `json:"lane,omitempty"`
	// RxDBm: the current level. FloorDBm: the lowest healthy level previously
	// known (see series.go for both windows).
	RxDBm    float64 `json:"rx_dbm"`
	FloorDBm float64 `json:"floor_dbm"`
}

type opticsAlertsReply struct {
	Available bool   `json:"available"`
	Why       string `json:"why,omitempty"`
	// MarginDB / WindowDays: the rule's parameters, returned so the screen can
	// caption the list truthfully instead of hard-coding a copy of the rule.
	MarginDB   float64          `json:"margin_db"`
	WindowDays int              `json:"window_days"`
	Rows       []opticsAlertRow `json:"rows"`
}

func (s *Server) handleOpticsRxAlerts(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var devices, err = s.Store.ListDevices(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, s.opticsRxAlerts(r.Context(), devices))
}

// opticsRxAlerts computes the list for a set of already-authorized devices.
// Shared by the authenticated route and the public dashboard dataset, which is
// why it takes devices rather than a site: the caller owns the authorization
// question, exactly as publicTraffic does for the traffic series.
func (s *Server) opticsRxAlerts(ctx context.Context,
	devices []model.Device) opticsAlertsReply {

	var out = opticsAlertsReply{
		Available:  s.TSDB != nil,
		MarginDB:   opticsAlertMarginDB,
		WindowDays: int(opticsFloorWindow.Hours() / 24),
		Rows:       []opticsAlertRow{},
	}
	if s.TSDB == nil {
		out.Why = "This instance has no time-series store " +
			"(MESHCRAFT_METRICS_URL empty): optical levels are not retained, " +
			"so there is no history to compare against."
		return out
	}
	var ids = make([]string, 0, len(devices))
	var i int
	for i = range devices {
		ids = append(ids, devices[i].ID)
	}
	var current = s.opticsInstant(ctx, opticsCurrentQuery(ids))
	if len(current) == 0 {
		return out
	}
	var floor = s.opticsInstant(ctx, opticsFloorQuery(ids))
	var byID = map[string]model.Device{}
	for i = range devices {
		byID[devices[i].ID] = devices[i]
	}

	var key string
	for key = range current {
		var ref, known = floor[key]
		if !known {
			// No healthy history: a freshly cabled port, or a cage that has
			// never seen light. Nothing to compare against, so nothing is
			// claimed — the honest answer, not a lenient one.
			continue
		}
		var now = current[key]
		if now.value > ref.value-opticsAlertMarginDB {
			continue
		}
		var d, have = byID[now.device]
		if !have {
			continue
		}
		// FACEPLATE name, same resolution and same refusal as every telemetry
		// consumer: an unresolvable label (a management port, a spelling the
		// connector cannot translate) names nothing an operator can walk to.
		var port = facadeOf(d).resolve(now.port)
		if port == "" {
			continue
		}
		out.Rows = append(out.Rows, opticsAlertRow{
			DeviceID: d.ID,
			Device:   d.Name,
			Port:     port,
			Lane:     now.lane,
			RxDBm:    roundSigned3(now.value),
			FloorDBm: roundSigned3(ref.value),
		})
	}
	// Deepest drop first — the fibre most below its own history is the one to
	// look at first — then a stable name order so the list does not reshuffle
	// under a reader on refresh.
	sort.Slice(out.Rows, func(a, b int) bool {
		var da = out.Rows[a].FloorDBm - out.Rows[a].RxDBm
		var db = out.Rows[b].FloorDBm - out.Rows[b].RxDBm
		if da != db {
			return da > db
		}
		if out.Rows[a].Device != out.Rows[b].Device {
			return out.Rows[a].Device < out.Rows[b].Device
		}
		if out.Rows[a].Port != out.Rows[b].Port {
			return out.Rows[a].Port < out.Rows[b].Port
		}
		return out.Rows[a].Lane < out.Rows[b].Lane
	})
	return out
}

// opticsReading is one instant-query result, keyed for the join.
type opticsInstantReading struct {
	device, port, lane string
	value              float64
}

// opticsInstant runs one composed instant query and indexes the result by
// (device, port, lane). Failure is absorbed into an empty map — the route
// answers "no rows" rather than 502, for the same reason telemetryOf does: one
// failing store query must not blank the dashboard around it.
func (s *Server) opticsInstant(ctx context.Context,
	query string) map[string]opticsInstantReading {

	var out = map[string]opticsInstantReading{}
	if query == "" {
		return out
	}
	var series, err = s.TSDB.QueryInstant(ctx, query)
	if err != nil {
		return out
	}
	var i int
	for i = range series {
		var r = opticsInstantReading{
			device: series[i].Labels["device"],
			port:   series[i].Labels["port"],
			lane:   series[i].Labels["lane"],
		}
		if r.device == "" || r.port == "" {
			continue
		}
		var p, ok = series[i].Last()
		if !ok {
			continue
		}
		r.value = p.Value
		out[r.device+"|"+r.port+"|"+r.lane] = r
	}
	return out
}
