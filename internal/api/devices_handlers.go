package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/catalog"
	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/siteconfig"
	"meshcraft/internal/store"
)

// --- Model catalog (Generic switches for now) ---

func (s *Server) handleCatalogModels(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, catalog.Models)
}

// --- Devices ---

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var devices, err = s.Store.ListDevices(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	// Management-plan conformity travels with the device: the UI must distinguish
	// "down" from "off-plan" without a second call. Same for sync state: port badges
	// depend on it, and a second round trip per device would make them flicker late.
	// withConformity also does sync enrichment: the two have a mandatory order, and
	// delegating both prevents reversing it here.
	//
	// Then what each device's PLATFORM does not expose in monitoring (see block
	// below). Last, because that layer depends neither on the site plan nor the
	// database: it reads only the connector and model.
	respond(w, http.StatusOK, withMonitoringGaps(s.withConformity(r.Context(), devices)))
}

// --- What the platform does NOT EXPOSE in monitoring ---
//
// A connector can declare that a metric is unavailable on its platform, with
// whether absence was observed on hardware or only assumed, and optional
// technical survey Source (driver.MetricUnavailable). Operator-facing copy is
// NOT in the connector: the UI localizes from the metric key
// (`monitoring.unavailable.<metric>`). Nobody read this declaration for a long
// time: `driver.Capabilities` is serialized by no route, and its only consumers
// were internal — watch cadences, trap capability, admin account. The JSON tags
// were ready and idle.
//
// The damage this silence causes: an operator looking for SFP+ optical power on a
// 1930 finds NOTHING — no chart, no explanation. They conclude meshcraft does not
// collect it yet and wait for a release, while the device will never publish those
// sensors. The declaration exists, established by a hardware readback, and never
// left the connector code until it was attached to the device payload.
//
// # Why on the device payload, not a dedicated route
//
// The device screen reads `GET /api/sites/{siteID}/devices` and nothing else
// (web/src/pages/SwitchDetail.tsx). Derived data already travels there: management-
// plan conformity, sync state, per-port drift, and especially `watch_excluded`,
// which answers the same question — "why did this information vanish from my
// screen?". Another route would be a second round trip per device for data that
// depends only on the model: the screen would show blank first, then the explanation.
// That blank is exactly what we remove.
//
// Accepted cost: survey Source can be long text, repeated for every device of
// the same line in a site list. Repetition is LITERAL, so router compression
// (middleware.Compress) shrinks it to almost nothing, and the field is `omitempty`:
// a fleet where no connector declares anything pays not a byte.

// deviceMonitoringView: the device record, plus what its platform does not expose
// in monitoring.
//
// A layer distinct from deviceView, not another field inside it: deviceView carries
// what is computed by confronting the device with ITS SITE (management plan, VLANs,
// proxy state), therefore with the database. This declaration depends only on the
// connector and model — so it stays derivable by a pure function, testable in a
// package without a database.
type deviceMonitoringView struct {
	deviceView
	// MonitoringUnavailable: metrics this device's platform does not expose, with
	// whether absence was observed or assumed (and optional technical Source).
	// Operator copy is localized in the UI from the metric key.
	//
	// ABSENT from JSON when the connector declares nothing, and that is the whole
	// point of `omitempty`: an empty array would read on screen as "everything else
	// is available". Missing declaration says nothing about availability, only that
	// nobody documented it — same distinction as driver.MonitoringCapabilities.
	// WhyUnavailable, where `ok=false` never means "it is available".
	MonitoringUnavailable []driver.MetricUnavailable `json:"monitoring_unavailable,omitempty"`
}

// unavailableMetricsOf returns what THIS device's platform does not expose.
//
// PURE and without database access, like judgeCollector above, for the same reason:
// this is the only decision in the lot where mistakes matter, and the package has
// no PostgreSQL in tests.
//
// The connector is read from `extra.driver`, set at discovery then adoption. Same
// key as deviceIdentity; we take only the part that does not touch the database:
// falling back through the discovery candidate would cost one query per device on
// a list of two hundred records, for text that depends only on the model.
//
// Returns nil in all cases of ignorance — no declared connector, unknown connector
// in the registry, unreadable `extra`. Returning an empty slice would claim nothing
// is unavailable, i.e. availability nobody observed.
func unavailableMetricsOf(d model.Device) []driver.MetricUnavailable {
	var drv, known = driver.Get(driver.DriverNameFromExtra(d.Extra))
	if !known {
		return nil
	}
	return drv.Capabilities(d.Model).Monitoring.Unavailable
}

// withMonitoringGaps adds the declaration to each record in a list.
func withMonitoringGaps(views []deviceView) []deviceMonitoringView {
	var out = make([]deviceMonitoringView, 0, len(views))
	var v deviceView
	for _, v = range views {
		out = append(out, deviceMonitoringView{
			deviceView:            v,
			MonitoringUnavailable: unavailableMetricsOf(v.Device),
		})
	}
	return out
}

// deviceInput: what a client may WRITE on a device.
//
// `status` and `status_note` are GONE, and that is a fix. They were accepted then
// written as-is: a screen that sends back the whole record — the site plan does,
// cf. web/src/pages/Plan.tsx — would republish the status it read on open, and
// could bring back to "unknown" a device watch had just marked online. Status is
// OBSERVED, not edited: its only write path is store.SetDeviceStatus, fed by ping.
//
// Fields are removed rather than silently ignored: the project JSON decoder does not
// forbid unknown fields, so a client that keeps sending them is not broken — they
// simply go nowhere. The struct now states what is editable, which a present but
// inert field would not (lesson of `AdminAccount.PasswordMax`, cf.
// docs/01-Architecture/Connecteurs).
type deviceInput struct {
	GroupID    *string         `json:"group_id"`
	Name       string          `json:"name"`
	Model      string          `json:"model"`
	Vendor     string          `json:"vendor"`
	Location   string          `json:"location"`
	MgmtIP     string          `json:"mgmt_ip"`
	PoEBudgetW int             `json:"poe_budget_w"`
	PosX       float64         `json:"pos_x"`
	PosY       float64         `json:"pos_y"`
	PortGroups json.RawMessage `json:"port_groups"`
	Ports      json.RawMessage `json:"ports"`
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in deviceInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" {
		respondErr(w, http.StatusBadRequest, "name required")
		return
	}
	// No status to set: the column defaults to "unknown", the only true state for a
	// device nobody has pinged yet. First observation comes from watch; `status_at`
	// stays NULL until then.
	//
	// Catalog model -> pre-filled port_groups and PoE budget.
	if len(in.PortGroups) == 0 && in.Model != "" {
		in.PortGroups = catalog.PortGroupsJSON(in.Model)
	}
	var d, err = s.Store.CreateDevice(r.Context(), model.Device{
		SiteID: siteID, GroupID: in.GroupID, Name: in.Name, Model: in.Model,
		Vendor: in.Vendor, Location: in.Location, MgmtIP: in.MgmtIP,
		PoEBudgetW: in.PoEBudgetW,
		PosX:       in.PosX, PosY: in.PosY, PortGroups: in.PortGroups, Ports: in.Ports,
	})
	if err != nil {
		fail(w, err)
		return
	}
	s.emit(r, model.Event{
		SiteID: &siteID, DeviceID: &d.ID, Type: "device.created", Severity: model.SevNotice,
		Message: "Device \"" + d.Name + "\" added (" + d.Model + ")",
	})
	// Tag at adoption: that is when device identity arrives, so rules can decide.
	s.autoTagDevice(r.Context(), d)
	// Same moment, same reason: connector capabilities are known, so SNMP identity is
	// decidable. Nothing is pushed to the device.
	s.provisionSNMP(r.Context(), d)
	respond(w, http.StatusCreated, d)
}

// portSummary summarizes a port configuration for the event log.
func portSummary(port json.RawMessage) string {
	var p struct {
		Label  string          `json:"label"`
		Mode   string          `json:"mode"`
		PVID   *int            `json:"pvid"`
		Tagged json.RawMessage `json:"tagged"`
		PoE    bool            `json:"poe"`
	}
	_ = json.Unmarshal(port, &p)
	var parts []string
	switch p.Mode {
	case "access":
		var vlan = "—"
		if p.PVID != nil {
			vlan = strconv.Itoa(*p.PVID)
		}
		parts = append(parts, "access VLAN "+vlan)
	case "trunk":
		var t = "trunk"
		if string(p.Tagged) == `"all"` {
			t = "full trunk"
		}
		if p.PVID != nil {
			t += " (native " + strconv.Itoa(*p.PVID) + ")"
		}
		parts = append(parts, t)
	default:
		parts = append(parts, "disabled")
	}
	if p.PoE {
		parts = append(parts, "PoE")
	}
	if p.Label != "" {
		parts = append(parts, "« "+p.Label+" »")
	}
	return strings.Join(parts, " · ")
}

// deviceForWrite loads the device and checks the role on ITS site.
func (s *Server) deviceForWrite(w http.ResponseWriter, r *http.Request, minRole string) (model.Device, bool) {
	var d, err = s.Store.GetDevice(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return d, false
	}
	if !s.Auth.CanSite(r.Context(), d.SiteID, minRole) {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return d, false
	}
	return d, true
}

// snmpProvisionInputs returns what SNMP provisioning actually depends on.
//
// Comparable with `==`, deliberately: the question is "must we talk to the
// device again?", answered by equality, not a condition list someone forgets to
// extend when a field is added.
//
//   - MODEL and VENDOR decide algorithms the device can carry;
//   - ADDRESS decides whom we will reach;
//   - GROUP decides the credential used to connect (cf. store.CredentialsForDevice).
//
// Name, location, plan position, and PoE budget are not here: they never leave the database.
type snmpInputs struct{ model, vendor, mgmtIP, group string }

func snmpProvisionInputs(d model.Device) snmpInputs {
	var group string
	if d.GroupID != nil {
		group = *d.GroupID
	}
	return snmpInputs{model: d.Model, vendor: d.Vendor, mgmtIP: d.MgmtIP, group: group}
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	var in deviceInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" {
		respondErr(w, http.StatusBadRequest, "name required")
		return
	}
	// Snapshot BEFORE merge: `d` is modified in place just below, and the
	// before/after comparison decides whether we must talk to the device again.
	var snmpBefore = snmpProvisionInputs(d)
	var mgmtIPBefore = d.MgmtIP
	d.GroupID, d.Name, d.Model, d.Vendor = in.GroupID, in.Name, in.Model, in.Vendor
	d.Location, d.MgmtIP = in.Location, in.MgmtIP
	d.PoEBudgetW, d.PosX, d.PosY = in.PoEBudgetW, in.PosX, in.PosY
	// `d.Status` / `d.StatusNote` / `d.StatusAt` are untouched: they come from
	// deviceForWrite's read and leave unchanged, because UpdateDevice no longer
	// writes them at all. Reachability is not editable.
	if len(in.PortGroups) > 0 {
		d.PortGroups = in.PortGroups
	}
	var updated, err = s.Store.UpdateDevice(r.Context(), d)
	if err != nil {
		fail(w, err)
		return
	}
	// Management address typed by hand: forget the previous addressing snapshot.
	// Otherwise the UI keeps claiming the device still carries the old IP.
	if updated.MgmtIP != mgmtIPBefore {
		_ = s.Store.ClearMgmtAddrObserved(r.Context(), updated.ID)
		updated.MgmtAddrObserved = nil
		updated.MgmtAddrObservedAt = nil
		// AND TELL THE PROXIES, or supervision keeps pinging the old address.
		//
		// The watch configuration a proxy holds carries the address to ping, and it
		// is only recomputed when something pushes it. Every OTHER path that moves a
		// management address already does this — [Server.adoptWrittenMgmtIP] after a
		// successful jump, the relocation callback, the renumber promotion — and its
		// comment there names the consequence exactly: "Proxy still had the OLD
		// address until this push — that was half the 'meshcraft lost the device'
		// bug after a successful jump."
		//
		// This was the other half. A management address corrected BY HAND — which is
		// what an operator does when an automatic move failed partway — updated the
		// database, re-tagged, re-provisioned SNMP, and left every proxy pinging an
		// address the device had abandoned. Nothing recovered it either, because
		// proxies emit on TRANSITION only (cf. cmd/proxy/monitor.go): having already
		// concluded "down" at the old address, they had nothing more to say, and the
		// device stayed red until an unrelated tunnel reconnection happened to
		// rebuild the instruction.
		//
		// Observed on s3900lab, 2026-08-02: address moved 10.99.0.2 → 10.99.0.71, SNMP
		// provisioning followed the device to the new address and succeeded, while
		// ICMP watch stayed on the old one and the switch showed offline throughout.
		s.pushWatchForDevice(updated)
	}
	// Identity changed (model, vendor, address, location): exactly what tagging
	// rules rely on. Re-running here avoids a fleet whose tags describe the old device.
	s.autoTagDevice(r.Context(), updated)
	// SNMP provisioning runs again only if what it DEPENDS ON changed.
	//
	// It ran on every save, on faith of a comment calling it "idempotent, and no
	// writes to the fleet". Idempotent, yes; no writes, no: it sets SNMP identity on
	// the device through the proxy. Measured on a real switch, the request went from
	// 5 ms to ~5 SECONDS.
	//
	// Cost was not only slowness. Renaming a device or changing its location —
	// metadata that never leaves the database — still talked to hardware, and the
	// screen froze long enough for the operator to think the action failed.
	//
	// Initial provisioning does not depend on here: it happens at creation and
	// adoption, the two moments a device appears.
	if snmpProvisionInputs(updated) != snmpBefore {
		s.provisionSNMP(r.Context(), updated)
	}
	respond(w, http.StatusOK, updated)
}

// handleSetDevicePort replaces a port configuration (intent + label).
// Operator level: the "technician" action (apply a profile, label); the UI
// reserves fine manual configuration for engineers.
func (s *Server) handleSetDevicePort(w http.ResponseWriter, r *http.Request) {
	var dev, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	var port json.RawMessage
	if !decode(w, r, &port) {
		return
	}
	if !json.Valid(port) {
		respondErr(w, http.StatusBadRequest, "port: invalid JSON")
		return
	}
	var portName = chi.URLParam(r, "port")
	var d, err = s.Store.SetDevicePort(r.Context(), chi.URLParam(r, "id"), portName, port)
	if err != nil {
		fail(w, err)
		return
	}
	// Timestamp INTENT change: this date, compared to the last apply, will tell
	// whether drift came from meshcraft or the device (cf. Reconciliation section 6).
	s.noteDesiredChanged(r.Context(), &d)
	s.emit(r, model.Event{
		SiteID: &dev.SiteID, DeviceID: &dev.ID, PortName: portName,
		Type: "port.configured", Severity: model.SevInfo,
		Message: dev.Name + " · " + portName + " → " + portSummary(port),
		Payload: port,
	})
	respond(w, http.StatusOK, s.withSync(r.Context(), d))
}

// maxPortsPerBatch bounds a batch. No real faceplate approaches this number:
// the bound exists so a faulty client cannot hold a transaction open across
// thousands of writes.
const maxPortsPerBatch = 512

// handleSetDevicePorts applies the same intent to a SELECTION of ports.
//
// The client sends each port's full intent — it knows current state and how to
// merge. The server writes the batch in one block and emits ONE event: forty-eight
// log lines for one gesture would drown what they are meant to clarify.
func (s *Server) handleSetDevicePorts(w http.ResponseWriter, r *http.Request) {
	var dev, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	var in struct {
		Ports map[string]json.RawMessage `json:"ports"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Ports) == 0 {
		respondErr(w, http.StatusBadRequest, "no ports in selection")
		return
	}
	if len(in.Ports) > maxPortsPerBatch {
		respondErr(w, http.StatusBadRequest, "selection too large")
		return
	}
	var names = make([]string, 0, len(in.Ports))
	var name string
	var raw json.RawMessage
	for name, raw = range in.Ports {
		if !json.Valid(raw) {
			respondErr(w, http.StatusBadRequest, "port "+name+": invalid JSON")
			return
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var d, err = s.Store.SetDevicePorts(r.Context(), dev.ID, in.Ports)
	if err != nil {
		fail(w, err)
		return
	}
	s.noteDesiredChanged(r.Context(), &d)
	var payload, _ = json.Marshal(map[string]any{"ports": names, "count": len(names)})
	s.emit(r, model.Event{
		SiteID: &dev.SiteID, DeviceID: &dev.ID,
		Type: "port.configured", Severity: model.SevInfo,
		Message: dev.Name + " · " + portsLabel(names) + " → " + batchSummary(in.Ports, names),
		Payload: payload,
	})
	respond(w, http.StatusOK, s.withSync(r.Context(), d))
}

// portsLabel names a selection without dumping it whole: "12 ports
// (1/1, 1/2, 1/3 … 1/12)". The log must stay scannable at a glance.
func portsLabel(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	var head = names
	var tail string
	if len(head) > 3 {
		head = names[:3]
		tail = " … " + names[len(names)-1]
	}
	return strconv.Itoa(len(names)) + " ports (" + strings.Join(head, ", ") + tail + ")"
}

// batchSummary describes what was applied. Batch intents may differ — PoE on a
// non-PoE port, for example: say so rather than imply uniformity that does not exist.
func batchSummary(ports map[string]json.RawMessage, names []string) string {
	var first = portSummary(ports[names[0]])
	var i int
	for i = 1; i < len(names); i++ {
		if portSummary(ports[names[i]]) != first {
			return "mixed configurations"
		}
	}
	return first
}

// handleMoveDevice changes a device's attachment site. Requires site_admin on
// BOTH the source and destination sites.
func (s *Server) handleMoveDevice(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		SiteID string `json:"site_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.SiteID == "" || in.SiteID == d.SiteID {
		respondErr(w, http.StatusBadRequest, "destination site_id required and different from current site")
		return
	}
	if !s.Auth.CanSite(r.Context(), in.SiteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "insufficient rights on the destination site")
		return
	}
	var target, siteErr = s.Store.GetSite(r.Context(), in.SiteID)
	if siteErr != nil {
		fail(w, siteErr)
		return
	}
	var err = s.Store.MoveDevice(r.Context(), d.ID, in.SiteID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, d.SiteID, "device.moved_out", model.SevWarning,
		"Device \""+d.Name+"\" moved to site \""+target.Name+"\" (links deleted)", nil)
	s.emitSite(r, in.SiteID, "device.moved_in", model.SevNotice,
		"Device \""+d.Name+"\" attached from another site", nil)
	respond(w, http.StatusOK, map[string]string{"status": "ok", "site_id": in.SiteID})
}

// --- Attachment to a collector proxy ---
//
// What this gesture decides, and nothing else: WHO polls the device in SNMP, and
// which address receives its traps (cf. migration 0022). It does not restrict ICMP
// watch, which stays redundant by coverage — determinism where uniqueness matters,
// redundancy where it is free.
//
// The trap this block exists to make unreachable: attachment gives NO coverage.
// Attaching a device to a proxy that does not cover its site does not move
// collection to it, it stops collection entirely — no SNMP polling, no trap
// recipient — without any request failing (store.TestRattachementNeDonnePasLaCouverture).
// Three mechanisms cover this, and none replaces the others:
//
//  1. the screen offers only covering proxies (handleListCollectorProxies);
//  2. the handler REFUSES others — a UI is not access control, and this route is
//     callable directly;
//  3. the device record says when the CURRENT attachment is no longer covering.
//     Write-time refusal is not enough: shrinking a proxy's coverage elsewhere
//     invalidates an attachment already written. The screen handles this by
//     comparing `proxy_id` to that same list.

// collectorVerdict: what we decide about a requested attachment, BEFORE write.
type collectorVerdict int

const (
	// collectorDetach: empty proxy_id. Back to coverage-based collection — a
	// legitimate operating mode, and the only fallback that exists, not absence of choice.
	collectorDetach collectorVerdict = iota
	// collectorAccepted: the requested proxy covers the device's site.
	collectorAccepted
	// collectorNotCovering: it does not cover it. Refuse, never write.
	collectorNotCovering
)

// judgeCollector compares the requested proxy to those covering the site.
//
// Pure, deliberately: this IS the rule of the lot, and the package has no
// database in tests (cf. devices_collector_test.go). The handler only obeys this
// verdict — all error-prone logic lives here, therefore testable.
func judgeCollector(covering []model.Proxy, proxyID string) (collectorVerdict, model.Proxy) {
	if proxyID == "" {
		return collectorDetach, model.Proxy{}
	}
	var p model.Proxy
	for _, p = range covering {
		if p.ID == proxyID {
			return collectorAccepted, p
		}
	}
	return collectorNotCovering, model.Proxy{}
}

// --- Collector CHOSEN at adoption, rather than left empty ---

// collectorForCandidate picks the COLLECTOR proxy for a device being adopted.
//
// # WHY THIS CHOICE HAPPENS HERE AND NOT LATER
//
// Adoption left `devices.proxy_id` EMPTY, so every fleet device lived under coverage-
// based collection: "polled by ALL proxies covering its site". A legitimate fallback,
// yet the wrong default, because three things cannot be decided without a NAMED collector:
//
//  1. SNMP agent SOURCE BOUNDS (cf. snmpSourcesFor). A proxy reaching management by
//     ROUTING has an address outside the plan subnet: without a declared collector,
//     meshcraft cannot request it, bounds the agent to the plan subnet only, and the
//     device goes silent FOR IT. Observed on a CRS326: conforming community, active agent,
//     mute walk — the proxy polled from 10.42.0.67 an agent accepting only 10.99.0.0/16;
//  2. TRAP DESTINATION, which is one address not a list: "several proxies" does not
//     translate to an address (cf. trapdest.go);
//  3. no duplicate collection: two covering proxies report the same port transitions twice
//     (migration 0022).
//
// Adoption is the right moment because it is the ONLY one where the question has a
// MEASURED answer: the candidate carries its sightings — which proxy saw it, at which
// address, with what latency. An hour later, only a list of proxies covering a site
// remains, and "which one can reach it?" becomes a guess.
//
// # THE TWO CONDITIONS, AND NEITHER ALONE SUFFICES
//
//	COVER THE SITE  — attachment gives no coverage. Writing a non-covering proxy would
//	                  stop all collection silently, which [judgeCollector] already refuses
//	                  on screen and API.
//	HAVE SEEN       — a proxy that never reached this address is a guess, not a collector.
//	                  The site is chosen AT ADOPTION: nothing guarantees this site's proxies
//	                  see the device, or that the one that saw it covers this site.
//
// ARBITRATION AMONG CANDIDATES IS RECORDED LATENCY, then last-seen date, then identifier.
// The last criterion is not decorative: without it, two equal-latency proxies would yield
// a different collector on each adoption, and the same fleet adopted twice would not compare.
//
// NOTHING IS CHOSEN when no proxy meets both conditions, and that is an outcome, not failure:
// coverage-based collection resumes, adoption succeeds, and the caller SAYS SO (cf.
// handleAdoptDiscovered, which turns it into a warning). Guessing would be worse — a collector
// that cannot reach the device is dead collection the screen would show as configured.
//
// PURE function: it decides who polls what across the fleet, and this package has no database
// in tests (cf. devices_collector_test.go).
func collectorForCandidate(cand model.Discovered, covering []model.Proxy) (model.Proxy, string) {
	var covers = map[string]model.Proxy{}
	var p model.Proxy
	for _, p = range covering {
		// A revoked proxy stays in the coverage list but polls nothing: naming it
		// collector would shut down collection for this device.
		if p.Revoked {
			continue
		}
		covers[p.ID] = p
	}
	if len(covers) == 0 {
		return model.Proxy{}, "no proxy covers this site"
	}

	// Sightings first: measured facts.
	var best model.Proxy
	var bestSight model.Sighting
	var found bool
	var sg model.Sighting
	for _, sg = range cand.Sightings {
		var pr, ok = covers[sg.ProxyID]
		if !ok {
			continue
		}
		if !found || betterSighting(sg, bestSight) {
			best, bestSight, found = pr, sg, true
		}
	}
	if found {
		return best, ""
	}

	// No usable sighting — manually entered candidate or one from a channel that
	// produces none. The proxy THAT PRODUCED the candidate remains a weaker but real
	// fact: through it meshcraft learned this device exists.
	var origin, ok = covers[cand.ProxyID]
	if ok {
		return origin, ""
	}
	if cand.ProxyID != "" {
		return model.Proxy{}, "proxy \"" + cand.ProxyName + "\", which discovered this " +
			"device, does not cover the site chosen"
	}
	return model.Proxy{}, "no covering proxy has seen this device"
}

// coveringProxies returns proxies covering this site, or an EMPTY list if read fails.
//
// Error is absorbed DELIBERATELY, and the fallback is correct: the caller must
// continue without a collector rather than fail. A device refused adoption stays out;
// one adopted without a collector attaches in one click, and the warning says so.
// A truly unreadable database will show at insert anyway, one line later.
func (s *Server) coveringProxies(ctx context.Context, siteID string) []model.Proxy {
	var out, err = s.Store.ProxiesCoveringSite(ctx, siteID)
	if err != nil {
		return nil
	}
	return out
}

// betterSighting says whether `a` is a better sighting than `b`: faster, then more
// recent, then smallest identifier.
//
// ZERO latency is not "instant" but "not measured" — zero of an integer no sighting
// filled — and treating it as best would let an unmeasured proxy beat one that measured 1 ms.
func betterSighting(a, b model.Sighting) bool {
	var la, lb = a.LatencyMS, b.LatencyMS
	if la != lb {
		if la == 0 {
			return false
		}
		if lb == 0 {
			return true
		}
		return la < lb
	}
	if !a.LastSeen.Equal(b.LastSeen) {
		return a.LastSeen.After(b.LastSeen)
	}
	return a.ProxyID < b.ProxyID
}

// notCoveringMessage says what WOULD happen if attachment were accepted.
//
// A bare "attachment refused" would send the operator looking for a missing
// permission, when what is missing is proxy coverage — and they would leave convinced
// meshcraft blocks something useful. The message must name the consequence, not the rule.
func notCoveringMessage(deviceName, proxyName string) string {
	var who = "this proxy"
	if proxyName != "" {
		who = "proxy \"" + proxyName + "\""
	}
	return "Attachment refused: " + who + " does not cover the site of « " + deviceName +
		"». Attachment gives no coverage: the device would not be " +
		"collected by this proxy, and would no longer be collected by anybody — no more " +
		"SNMP polling and no recipient for its traps, while it would still be pinged. " +
		"Add this site to the proxy coverage, or " +
		"designate a proxy that already covers the site."
}

// collectorLabel names a collector, including its absence.
//
// Bare "none" would read in the log as a forgotten setting. It is an operating
// mode — coverage-based collection, that of the whole fleet before migration 0022 —
// and the trace must say so.
func collectorLabel(name string) string {
	if name == "" {
		return "none (coverage-based collection)"
	}
	return "« " + name + " »"
}

// collectorChangeMessage carries OLD and NEW collector in plain text.
//
// Without this trace, the gesture would be undiagnosable three months later: the
// symptom ("this switch no longer reports ports") has no visible link to a dropdown
// checked on a Tuesday. Identifiers would not suffice — a deleted proxy no longer
// resolves, only its name stays readable.
func collectorChangeMessage(deviceName, before, after string) string {
	return "Collector of « " + deviceName + " »: " +
		collectorLabel(before) + " → " + collectorLabel(after)
}

// collectorWriteError turns a write failure into a readable response. Returns empty
// message when the error is not for this gesture: `fail` handles it then, and
// copying its table here would diverge.
//
// The two handled cases are RACES, which is why they exist despite prior checks:
// between reading the device and update, someone may have deleted the device or
// proxy. A prior check cannot cover them — only the foreign key is never late
// (cf. store.ErrProxyUnknown).
func collectorWriteError(err error) (int, string) {
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, store.ErrProxyUnknown):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, store.ErrNotFound):
		// Bare "not found", what `fail` would return, would not say WHAT: on this
		// route, two objects may be missing.
		return http.StatusNotFound, "device not found"
	}
	return 0, ""
}

// handleListCollectorProxies: proxies that can collect this site's devices.
//
// Read at `viewer`, like /discovery-proxies exposing the same information — name
// and coverage of proxies usable for this site. Reserving it to administrators would
// hide the "this attachment no longer covers this site" banner from people who
// watch the screen all day — precisely those who notice a device stopped reporting.
func (s *Server) handleListCollectorProxies(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var proxies, err = s.Store.ProxiesCoveringSite(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, proxies)
}

// handleSetDeviceCollector declares — or removes — a device's reference collector
// proxy.
//
// Reserved to `site_admin`, like neighbor handleSetDeviceAddressDomain, and for
// an even stronger reason: a wrong address domain degrades arbitration, a wrong
// collector STOPS collection. Not the same order as labeling a port.
//
// Empty string accepted: detachment, as normal as attachment.
func (s *Server) handleSetDeviceCollector(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		ProxyID string `json:"proxy_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	// The SAME list served to the screen (handleListCollectorProxies), therefore the
	// same predicate: the screen cannot offer a choice this check would refuse, and this
	// check cannot allow what the screen hides.
	var covering, err = s.Store.ProxiesCoveringSite(r.Context(), d.SiteID)
	if err != nil {
		fail(w, err)
		return
	}
	var verdict, target = judgeCollector(covering, in.ProxyID)
	if verdict == collectorNotCovering {
		// Not on the list: either the proxy exists and does not cover this site, or it
		// does not exist at all. Both refusals look alike but call for different actions —
		// widen coverage, or fix an identifier — so we look up which rather than return a
		// message that fits both and helps neither.
		var p, getErr = s.Store.GetProxy(r.Context(), in.ProxyID)
		if getErr != nil {
			respondErr(w, http.StatusUnprocessableEntity, store.ErrProxyUnknown.Error())
			return
		}
		respondErr(w, http.StatusUnprocessableEntity, notCoveringMessage(d.Name, p.Name))
		return
	}
	// Snapshot BEFORE write: the old collector must be named in the log and notified of
	// the change, and `d` still carries its prior state.
	var before = d.ProxyID
	if before == in.ProxyID {
		// A "change" that changes nothing: no log, no push. One line per dropdown open
		// would drown real collector moves, exactly what this log exists to find again.
		respond(w, http.StatusOK, s.withSync(r.Context(), d))
		return
	}
	if err = s.Store.SetDeviceProxy(r.Context(), d.ID, in.ProxyID); err != nil {
		var code, msg = collectorWriteError(err)
		if msg != "" {
			respondErr(w, code, msg)
			return
		}
		fail(w, err)
		return
	}
	// SevWarning, not SevNotice: this gesture moves collection for a device and changes
	// its trap destination. It must stand out in a log filtered to warnings, like a site
	// move or management switchover, not drown in port configuration lines.
	s.emitDeviceEvent(r, d, "device.collector", model.SevWarning,
		collectorChangeMessage(d.Name, d.ProxyName, target.Name))
	// Push watch to BOTH proxies involved, old included. Without the old one, it keeps
	// polling the device in SNMP until its next reconnect: two collectors for one device,
	// exactly what attachment exists to prevent (port transitions reported twice, cf.
	// migration 0022).
	//
	// In the background on a fresh context, like other callers (cf. discovery_handlers.go):
	// the request context dies with the response, and the HTTP response must not wait for
	// a tunnel that may be down.
	var concerned = []string{before, in.ProxyID}
	var i int
	for i = range concerned {
		if concerned[i] == "" {
			continue
		}
		// Explicit copy before the goroutine: reading `concerned[i]` from the goroutine
		// would read after the loop advanced.
		var pid = concerned[i]
		go s.PushWatchTo(context.Background(), pid)
	}
	// Re-read: the response must carry attachment as the database sees it, collector name
	// included (`proxy_name` is projected by a subquery, cf. store.deviceCols), and the
	// screen uses it immediately.
	var fresh model.Device
	fresh, err = s.Store.GetDevice(r.Context(), d.ID)
	if err != nil {
		fail(w, err)
		return
	}
	// Re-provision trap destination, for the same reason the line above pushes watch:
	// changing collector changes the address the device must emit to.
	//
	// `SetDeviceProxy` already forgot destination and proof in the database — the record
	// claims nothing. But the DEVICE still carries the old address in its configuration:
	// without this re-provision, it keeps emitting to a proxy that no longer listens, and
	// the fast channel dies silently. Forgetting a claim does not erase what is written in
	// hardware.
	//
	// In the background, on its own budget: provisioning opens a web session on the device,
	// and the HTTP response must not wait for it.
	go s.provisionTrapDestBG(fresh)
	respond(w, http.StatusOK, s.withSync(r.Context(), fresh))
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var err = s.Store.DeleteDevice(r.Context(), d.ID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, d.SiteID, "device.deleted", model.SevWarning,
		"Device \""+d.Name+"\" deleted", nil)
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Links ---

func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var links, err = s.Store.ListLinks(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, links)
}

func (s *Server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in model.DeviceLink
	if !decode(w, r, &in) {
		return
	}
	if in.ADevice == "" || in.BDevice == "" {
		respondErr(w, http.StatusBadRequest, "a_device and b_device required")
		return
	}
	if in.Status == "" {
		in.Status = "up"
	}
	in.SiteID = siteID
	var l, err = s.Store.CreateLink(r.Context(), in)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "link.created", model.SevInfo,
		"Link created ("+l.APort+" ↔ "+l.BPort+", "+l.Status+")", l)
	respond(w, http.StatusCreated, l)
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	var l, err = s.Store.GetLink(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	if !s.Auth.CanSite(r.Context(), l.SiteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return
	}
	if err = s.Store.DeleteLink(r.Context(), l.ID); err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, l.SiteID, "link.deleted", model.SevNotice, "Link deleted", l)
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Site plan ---

func (s *Server) handleGetSitePlan(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var plan, err = s.Store.GetSitePlan(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(plan)
}

// handleUpdateSitePlan saves the synoptic: dimensions and zones drawn behind devices.
func (s *Server) handleUpdateSitePlan(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var plan json.RawMessage
	if !decode(w, r, &plan) {
		return
	}
	if !json.Valid(plan) {
		respondErr(w, http.StatusBadRequest, "plan: invalid JSON")
		return
	}
	var err = s.Store.UpdateSitePlan(r.Context(), siteID, plan)
	if err != nil {
		fail(w, err)
		return
	}
	var parsed struct {
		Zones []struct {
			Label string `json:"label"`
		} `json:"zones"`
	}
	_ = json.Unmarshal(plan, &parsed)
	s.emitSite(r, siteID, "plan.updated", model.SevInfo,
		"Site plan saved ("+strconv.Itoa(len(parsed.Zones))+" zones)", nil)
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Export / import site-config ---

func (s *Server) handleExportSite(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var f, err = s.Store.ExportSiteConfig(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="meshcraft-site-config.json"`)
	respond(w, http.StatusOK, f)
}

func (s *Server) handleImportSite(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var f siteconfig.File
	var dec = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	var err = dec.Decode(&f)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err = s.Store.ImportSiteConfig(r.Context(), siteID, f); err != nil {
		s.emitSite(r, siteID, "config.import_failed", model.SevError,
			"Configuration import refused: "+err.Error(), nil)
		respondErr(w, http.StatusBadRequest, "import refused: "+err.Error())
		return
	}
	s.emitSite(r, siteID, "config.imported", model.SevWarning,
		"Site configuration replaced by import: "+
			strconv.Itoa(len(f.Devices))+" devices, "+
			strconv.Itoa(len(f.Vlans))+" VLAN, "+
			strconv.Itoa(len(f.PortProfiles))+" port profiles",
		map[string]int{
			"devices": len(f.Devices), "vlans": len(f.Vlans),
			"port_profiles": len(f.PortProfiles), "links": len(f.Links),
		})
	respond(w, http.StatusOK, map[string]any{
		"status": "ok",
		"counts": map[string]int{
			"vlans": len(f.Vlans), "port_profiles": len(f.PortProfiles),
			"device_profiles": len(f.DeviceProfiles), "device_groups": len(f.DeviceGroups),
			"devices": len(f.Devices), "links": len(f.Links),
		},
	})
}
