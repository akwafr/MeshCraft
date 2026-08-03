package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/recon"
)

// Configuration drift: read the device, compare, and propose arbitration. See
// docs/01-Architecture/Reconciliation sections 2, 3, and 6.
//
// The principle governing this entire file: a READ never modifies intent. Without
// this rule, drift would absorb itself on the first refresh and nobody would ever
// see it — exactly the opposite of what we want.

// observedState is what we store in `devices.observed`.
//
// TYPES COME FROM THE CONTRACT (internal/driver/state.go), not from one connector:
// that lets MikroTik state and Instant On state live in the same column, and lets
// configuration drift compare them without knowing the platform.
//
// JSON TAGS ARE UNCHANGED, and that is not cosmetic: this structure is serialized
// in `devices.observed` across the already-read fleet. Renaming a tag would re-read
// "zero VLANs, no ports" everywhere, presenting as massive drift and triggering applies.
type observedState struct {
	VLANs []driver.VLAN               `json:"vlans"`
	Ports map[string]driver.PortState `json:"ports"`
	// LinkUp: ports that had a link at read time.
	//
	// Kept because a VLAN present on a port WITHOUT a cable proves no path. That is
	// the difference between "the management VLAN is configured somewhere" and "there is
	// an uplink through which it actually exits" — and the second question decides
	// whether a switchover loses control. Absent from earlier readbacks: absence means
	// "we do not know", not "no link".
	//
	// WITHOUT `omitempty`, deliberately. An empty map means "read back, and no port
	// has a link"; a missing map means "never read back". With `omitempty` both write
	// the same, and the migration safeguard would answer "we do not know" where it
	// should answer "there is no path" — letting through the case it exists to stop.
	LinkUp map[string]bool `json:"link_up"`
	// Links: the richer per-port operational state (up / down / disabled /
	// unplugged / err-disabled), additive to link_up. Omitempty and a NEW tag so
	// existing devices.observed rows stay byte-identical — the migration-safety
	// rule the LinkUp comment states. err-disabled is the value the UI surfaces
	// on the interface.
	Links map[string]driver.LinkState `json:"links,omitempty"`
	// MgmtVLAN is the VLAN the device says it is administered through (0 = untagged /
	// unknown). Optional with omitempty so older observed rows stay valid.
	MgmtVLAN int `json:"mgmt_vlan,omitempty"`
}

// withSync enriches a device with its synchronization state.
//
// COMPUTED on every read, never stored: a frozen state in the database would lie on
// the next change, and a lying indicator is worse than no indicator.
//
// Became a method when "all" stopped being a special case: diff now needs the SITE's
// VLANs to know what "all" requires.
func (s *Server) withSync(ctx context.Context, d model.Device) model.Device {
	var out = withSyncVLANs(d, s.siteVLANIDs(ctx, d.SiteID))
	out.WatchExcluded = s.WatchExcl.reasonFor(d.ID)
	// Live provisioning: true only while the connector is writing to THIS device.
	// Read from the apply queue, not the database — it is a runtime fact.
	out.Applying = s.applyingNow(d.ID)
	return out
}

// withSyncVLANs is the part that does not touch the database, so list and detail share
// the same computation without re-reading the plan per device.
func withSyncVLANs(d model.Device, siteVLANs []int) model.Device {
	var changes = driftOf(d, siteVLANs)
	d.Sync = string(recon.State(d.ObservedAt, d.DesiredChangedAt, d.AppliedAt, changes))
	d.ApplyStalled = applyStalled(d, changes)
	var c recon.Change
	for _, c = range changes {
		d.Drift = append(d.Drift, c)
	}
	// ERR-DISABLED IS CARRIED ONTO THE PORT before observed is stripped.
	//
	// The list view drops the bulky observed blob, but a port the SWITCH shut
	// itself is an alarm the facade must show without a second call. So the one
	// fact worth keeping — the operational state, and the reason when the
	// connector gave one — is lifted onto each port's intent JSON here, cheaply,
	// while `d.Observed` is still readable.
	d = mergeLinkState(d)

	// Observed state is bulky and only matters on the detail screen: it has no place
	// in a list of two hundred devices.
	d.Observed = nil
	return d
}

// mergeLinkState lifts each port's observed operational state (and the
// err-disabled reason, when the connector reported one) onto the served port
// intent, so the facade can flag a switch-shut port without shipping the whole
// observed blob.
//
// Best-effort and non-destructive: a device that was never read, or whose read
// carried no links, comes through untouched.
func mergeLinkState(d model.Device) model.Device {
	if len(d.Observed) == 0 || len(d.Ports) == 0 {
		return d
	}
	var obs struct {
		Links map[string]string `json:"links"`
		Notes []string          `json:"notes"`
	}
	if json.Unmarshal(d.Observed, &obs) != nil || len(obs.Links) == 0 {
		return d
	}
	var ports map[string]map[string]any
	if json.Unmarshal(d.Ports, &ports) != nil {
		return d
	}
	var name, state string
	for name, state = range obs.Links {
		var port, ok = ports[name]
		if !ok {
			continue
		}
		port["link_state"] = state
		if state == "err-disabled" {
			// The reason lives in a device-level note that names the port. It is
			// a SENTENCE for a human (« GE25 was shut down BY THE SWITCH
			// (err-disabled), reason gbic-invalid ») — surfaced verbatim, matched
			// by the port name it contains.
			var note string
			for _, note = range obs.Notes {
				if strings.Contains(note, name) {
					port["err_reason"] = note
					break
				}
			}
		}
	}
	var raw, err = json.Marshal(ports)
	if err == nil {
		d.Ports = raw
	}
	return d
}

// applyStalled: the last apply happened AFTER the last change, yet drift remains.
//
// This is the difference between "not yet applied" and "applied, but incomplete", both
// grouped as "in progress" by sync state. The first resolves alone in seconds; the second
// will NEVER resolve alone, because auto-apply triggers only on intent change — and there
// will be none.
//
// This is what an "all" trunk produced in bulk: apply skipped it silently, did not date
// application for failing to apply everything, and the screen had no reason to show
// anything. A half-configured device looked identical to one still being configured.
func applyStalled(d model.Device, changes []recon.Change) bool {
	if len(changes) == 0 || len(d.LastApply) == 0 {
		return false
	}
	var rec struct {
		At *time.Time `json:"at"`
	}
	if json.Unmarshal(d.LastApply, &rec) != nil || rec.At == nil {
		return false
	}
	// A change AFTER apply will be picked up by auto-apply: that is delay, not blockage,
	// and signaling it would teach people to ignore the banner.
	if d.DesiredChangedAt != nil && d.DesiredChangedAt.After(*rec.At) {
		return false
	}
	return true
}

// siteVLANIDs returns VLANs declared by a site.
//
// An error returns an EMPTY list, deliberately: "all" stays unresolved, apply writes
// nothing on those ports and diff requests nothing. At worst meshcraft does not advance —
// it does not apply guessed configuration from a plan it failed to read.
func (s *Server) siteVLANIDs(ctx context.Context, siteID string) []int {
	var vlans, err = s.Store.ListVlans(ctx, siteID)
	if err != nil {
		return nil
	}
	var out = make([]int, 0, len(vlans))
	var v model.Vlan
	for _, v = range vlans {
		out = append(out, v.VlanID)
	}
	return out
}

// driftOf compares desired to observed for a device.
//
// `siteVLANs` resolves "all". Apply uses the SAME resolution (cf. applyDeviceConfig):
// if they diverged, apply would write a list diff would immediately flag as drift, forever.
func driftOf(d model.Device, siteVLANs []int) []recon.Change {
	if d.ObservedAt == nil {
		return nil
	}
	var obs observedState
	if len(d.Observed) > 0 {
		_ = json.Unmarshal(d.Observed, &obs)
	}
	var observed = map[string]recon.PortIntent{}
	var name string
	var p driver.PortState
	for name, p = range obs.Ports {
		observed[name] = recon.PortIntent{Mode: p.Mode, PVID: p.PVID, Tagged: p.Tagged}
	}
	return recon.DiffPorts(recon.Resolve(recon.ParsePorts(d.Ports), siteVLANs), observed)
}

// portPrefix returns the naming prefix of the device's FIRST faceplate group ("GE"…),
// and nothing more.
//
// THIS IS NOT A UNIVERSAL TRUTH, and must be read that way or you write to the wrong
// port. On platforms that NUMBER their ports (Instant On, SwOS), this prefix translates
// "GE12" to device index 12, and a single-prefix faceplate summarizes it entirely. On
// RouterOS, meshcraft names ARE device names (`ether3`, `sfp-sfpplus1`): the faceplate
// carries SEVERAL prefixes, none summarizes it, and the connector deliberately ignores
// this field (cf. driver.Target.PortPrefix). Taking "ether" as the prefix for all ports
// on a CRS326 would write to an optical cage thinking it is copper, cutting off a client.
//
// There is therefore NO correct value to find here, which is why this function does not
// try: it returns what the faceplate says first, passes it through [driver.Target.PortPrefix],
// and lets each connector decide if it needs it. Fallback "GE" is the line this code was
// written for; on an unreadable faceplate, a numbering connector applies its own default
// anyway rather than writing blind.
func portPrefix(d model.Device) string {
	var groups []struct {
		Prefix string `json:"prefix"`
	}
	if json.Unmarshal(d.PortGroups, &groups) == nil && len(groups) > 0 &&
		groups[0].Prefix != "" {
		return groups[0].Prefix
	}
	return "GE"
}

// handleReadDeviceState re-reads the device and records what it carries.
//
// READ-ONLY on the device side: replayable as often as needed, including on a production
// fleet. `operator` level — observing is not modifying.
func (s *Server) handleReadDeviceState(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	if d.MgmtIP == "" {
		respondErr(w, http.StatusUnprocessableEntity,
			"this device has no known management address")
		return
	}

	// ONE-TIME PORT-NAME CATCH-UP. A device whose ports were written before
	// its connector adopted a standard naming scheme (or that was adopted by
	// pasting its configuration before this remap existed at import time)
	// still carries the platform's RAW names in devices.ports. Left alone,
	// every later apply/diff/telemetry lookup on it silently misses by
	// comparing under two different vocabularies — this is exactly the "port
	// profile applied to one port did nothing" symptom a stale FS S3900
	// device showed after its connector switched to standard GE-style names.
	//
	// This is NOT an intent change: the device is presumably already
	// configured correctly under its OLD bookkeeping key, so it must not
	// schedule a re-apply (desired_changed_at is deliberately left alone) —
	// only the key changes, nothing is asked of the wire. Best-effort and
	// silent when there is nothing stale: it renames only keys the connector
	// itself says translate, never guesses.
	var portsRenamed bool
	d, portsRenamed = s.remapDevicePortsIfStale(r.Context(), d)

	// SINGLE resolution point: the connector discovery designated, transport,
	// credentials, and try order (cf. adminFor).
	var _, adm, _, err = s.adminFor(r.Context(), d)
	var out = map[string]any{}
	if err != nil {
		// A DOWN PROXY STAYS A 503, and that is not status-code zeal: resolution now
		// goes through one path, and drowning "this proxy's tunnel is down" in the same
		// `200 ok:false` as "the device refuses credentials" would send troubleshooting
		// to a switch that is fine.
		if errors.Is(err, errNoTunnel) {
			respondErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		// Transcript is shown only when present: connectors differ on nil (cf. transcriptOf).
		out["ok"], out["error"], out["steps"] = false, err.Error(), transcriptOf(adm)
		respond(w, http.StatusOK, out)
		return
	}
	var state driver.State
	state, err = adm.ReadState(r.Context())
	out["steps"] = adm.Transcript()
	if err != nil {
		out["ok"], out["error"] = false, err.Error()
		respond(w, http.StatusOK, out)
		return
	}
	// Connector notes are PHRASES to display, with no decision to draw: present but
	// INERT VLAN configuration ("disabled" mode on SwOS, inactive `vlan-filtering` on
	// RouterOS) appears here and nowhere else. Hiding them would show segmentation that
	// does not exist.
	if len(state.Notes) > 0 {
		out["notes"] = state.Notes
	}

	var raw []byte
	if raw, err = json.Marshal(observedState{
		VLANs: state.VLANs, Ports: state.Ports, LinkUp: state.LinkUp(), Links: state.Links,
		MgmtVLAN: state.MgmtVLAN,
	}); err != nil {
		fail(w, err)
		return
	}
	if err = s.Store.SaveObserved(r.Context(), d.ID, raw); err != nil {
		fail(w, err)
		return
	}

	// MANAGEMENT ADDRESSING is re-read IN THE SAME GESTURE (migration 0027): by what
	// means did this device obtain its address, static or DHCP? Asking the operator for
	// two clicks to re-read the same device would make them carry an internal code
	// boundary — the two pieces come from different pages, but answer the same question,
	// "what does this device actually carry?"
	//
	// Its failure does NOT interrupt this read, and order guarantees it: port state is
	// already saved at this point. Losing VLANs and ports across a fleet because an
	// addressing page is unreadable on one line would be a regression, and the reason is
	// returned to the screen rather than hidden.
	var addrState, addrNote = s.readMgmtAddressing(r.Context(), d.ID, adm)
	if addrNote != "" {
		out["mgmt_addr_note"] = addrNote
	}
	if addrState.Mode != "" {
		out["mgmt_addr_mode"] = addrState.Mode
	}
	if len(addrState.Notes) > 0 {
		// Connector addressing notes are KEPT SEPARATE from state notes: "SwOS carries
		// neither management gateway nor DNS" and "VLAN configuration is inert" do not
		// belong in the same screen area, and mixing them loses both.
		out["mgmt_addr_notes"] = addrState.Notes
	}

	// Spanning tree is re-read in the same gesture: topology needs mode, root and
	// per-port roles without a second round-trip. Failure does not interrupt the
	// read — STP is optional per platform.
	var stpState, stpNote = s.readSpanningTree(r.Context(), d.ID, adm)
	if stpNote != "" {
		out["stp_note"] = stpNote
	}
	if stpState.Mode != "" || stpState.Enabled || len(stpState.Ports) > 0 {
		out["stp"] = stpState
	}

	// FACEPLATE catch-up, in the same read gesture. A device adopted before its
	// connector could read the faceplate (or entered by hand) carries no port
	// groups, so nothing draws and SNMP/LLDP port resolution degrades. If it is
	// empty and the connector can read it, fill it now — the faceplate is a fact
	// of the hardware, like observed state, so a read is the right place. Failure
	// never interrupts: ports and VLANs are already saved.
	//
	// ALSO forced when the stored groups still carry the connector's OLD prefix
	// scheme (facadeNamingStale) — this is deliberately NOT the same condition
	// as portsRenamed: a device already caught up on a PRIOR read has
	// portsRenamed=false on this one (nothing left to rename in devices.ports),
	// but its port_groups can still carry the stale prefix from before that
	// prior read ever ran fillFaceplate. Checking the stored groups directly,
	// every time, is what makes this catch-up actually converge instead of
	// firing once and silently leaving the facade behind.
	if faceplateEmpty(d.PortGroups) || portsRenamed || s.facadeNamingStale(r.Context(), d) {
		s.fillFaceplate(r.Context(), d, adm, out)
	}

	var fresh model.Device
	if fresh, err = s.Store.GetDevice(r.Context(), d.ID); err != nil {
		fail(w, err)
		return
	}
	var changes = driftOf(fresh, s.siteVLANIDs(r.Context(), d.SiteID))
	var sync = recon.State(fresh.ObservedAt, fresh.DesiredChangedAt, fresh.AppliedAt, changes)
	// Drift is logged: an operational fact, not a display detail. Someone touched the
	// device, and history must say so.
	if sync == recon.SyncDrift {
		s.emitDeviceEvent(r, d, "config.drift", model.SevWarning,
			"Configuration drift on \""+d.Name+"\": "+itoa(len(changes))+
				" difference(s) between what meshcraft knows and the device.")
	}
	out["ok"], out["sync"], out["drift"] = true, sync, changes
	out["vlans"], out["ports"] = state.VLANs, len(state.Ports)

	// A MANUAL refresh is also the operator saying "make this device right". If
	// supervision should be up and is not, kick the SNMP re-push here — in the
	// background, so this read-only response stays read-only and instant. It is
	// the user-triggered twin of RunSNMPHeal: same idempotent gesture, but the
	// manual click clears the backoff instead of waiting up to a day for it.
	if fresh.SnmpStatus != "ok" {
		s.KickSNMPHeal(fresh)
		out["snmp_reprovision"] = "triggered"
	}
	respond(w, http.StatusOK, out)
}

// remapPortNamesIfStale is the PURE decision behind remapDevicePortsIfStale:
// which keys of `ports` a connector's PortNamer would rename, and the
// resulting JSON. Extracted so it is testable without a database — house
// style throughout this codebase keeps the logic in a pure function and the
// I/O in a thin wrapper around it.
//
// A key that already matches the connector's current standard form, or that
// it cannot translate at all, is copied through unchanged: nothing is ever
// guessed, only what the connector itself confirms is renamed.
func remapPortNamesIfStale(namer driver.PortNamer, ports json.RawMessage) (json.RawMessage, bool) {
	if len(ports) == 0 {
		return ports, false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(ports, &raw) != nil {
		return ports, false
	}
	var out = map[string]json.RawMessage{}
	var changed bool
	var k string
	var v json.RawMessage
	for k, v = range raw {
		var std, ok = namer.StandardPortName(k)
		if ok && std != k {
			out[std] = v
			changed = true
			continue
		}
		out[k] = v
	}
	if !changed {
		return ports, false
	}
	var encoded, err = json.Marshal(out)
	if err != nil {
		return ports, false
	}
	return encoded, true
}

// facadeGroupPrefix is the one field facadeNamingStale needs from a stored
// port group — kept separate from portrules.Port / the facade JSON shape used
// elsewhere, because this is a MINIMAL probe, not a full decode.
type facadeGroupPrefix struct {
	Prefix string `json:"prefix"`
	From   int    `json:"from"`
}

// facadeNamingStale is the PURE decision behind the facade half of the
// standard-naming catch-up: does at least one stored port group still carry a
// prefix the connector's PortNamer would translate to something ELSE? If so,
// the group predates the connector's current naming scheme.
//
// Same probe shape as remapPortNamesIfStale, applied to port_groups instead
// of ports: test the group's FIRST port (prefix + from) — on every numbering
// connector shipped today the mapping is affine, so one sample proves the
// whole group.
func facadeNamingStale(namer driver.PortNamer, portGroups json.RawMessage) bool {
	if len(portGroups) == 0 {
		return false // faceplateEmpty already covers this case
	}
	var groups []facadeGroupPrefix
	if json.Unmarshal(portGroups, &groups) != nil {
		return false
	}
	var g facadeGroupPrefix
	for _, g = range groups {
		if g.From <= 0 || g.Prefix == "" {
			continue
		}
		var sample = g.Prefix + strconv.Itoa(g.From)
		var std, ok = namer.StandardPortName(sample)
		if ok && std != sample {
			return true
		}
	}
	return false
}

// facadeNamingStale (method) resolves the device's connector and applies the
// pure check above. False on any resolution failure — an unresolvable
// connector or one without PortNamer has nothing to be stale AGAINST.
func (s *Server) facadeNamingStale(ctx context.Context, d model.Device) bool {
	var _, name = s.deviceIdentity(ctx, d)
	var drv, known = driver.Get(name)
	if !known {
		return false
	}
	var namer, canName = driver.AsPortNamer(drv)
	if !canName {
		return false
	}
	return facadeNamingStale(namer, d.PortGroups)
}

// remapDevicePortsIfStale renames devices.ports keys still written under a
// platform's RAW port name into the standard-model name the connector
// currently uses, when the connector declares that translation
// (driver.PortNamer). Returns the input unchanged when there is nothing
// stale — most devices, most of the time.
//
// A FULL REPLACE of the ports column, not the incremental SetDevicePorts
// (whose jsonb_set only adds or updates the keys it is given and never
// removes any): a rename must make the OLD key actually disappear, or the
// device would carry both the stale entry and its renamed twin — doubling it
// in every desired-state iteration downstream.
func (s *Server) remapDevicePortsIfStale(ctx context.Context, d model.Device) (model.Device, bool) {
	var _, name = s.deviceIdentity(ctx, d)
	var drv, known = driver.Get(name)
	if !known {
		return d, false
	}
	var namer, canName = driver.AsPortNamer(drv)
	if !canName {
		return d, false
	}
	var encoded, changed = remapPortNamesIfStale(namer, d.Ports)
	if !changed {
		return d, false
	}
	var fresh, err = s.Store.ReplaceDevicePorts(ctx, d.ID, encoded)
	if err != nil {
		return d, false
	}
	return fresh, true
}

// faceplateEmpty reports whether a device carries no drawable port groups.
func faceplateEmpty(pg json.RawMessage) bool {
	var s = strings.TrimSpace(string(pg))
	if s == "" || s == "null" || s == "[]" || s == "{}" {
		return true
	}
	var groups []any
	if json.Unmarshal(pg, &groups) != nil {
		return false // unreadable but non-empty: do not overwrite it
	}
	return len(groups) == 0
}

// fillFaceplate reads the physical faceplate from the live device and persists it,
// on a connector that can. Best-effort: any failure is noted, never fatal — the
// read already saved ports and VLANs. This is the catch-up that keeps a
// blank-adopted device from staying portless forever (cf. driver.FaceplateReader).
func (s *Server) fillFaceplate(ctx context.Context, d model.Device, adm driver.Admin, out map[string]any) {
	var reader, ok = driver.AsFaceplateReader(adm)
	if !ok {
		return
	}
	var groups, err = reader.ReadFaceplate(ctx)
	if err != nil {
		out["faceplate_note"] = "faceplate not read: " + err.Error()
		return
	}
	var raw, mErr = json.Marshal(groups)
	if mErr != nil || len(groups) == 0 {
		return
	}
	var _, sErr = s.Store.SetDeviceFaceplate(ctx, d.ID, raw)
	if sErr != nil {
		out["faceplate_note"] = "faceplate read but not saved: " + sErr.Error()
		return
	}
	out["faceplate_filled"] = len(groups)
}

// handleReadRawSection returns one raw CONFIGURATION UNIT from the device, as its
// platform serves it.
//
// RECONNAISSANCE tool, global-admin only and strictly read-only. It exists because
// these interfaces are undocumented: before writing a unit — and writing the management
// VLAN is the most dangerous operation in the product — you must have seen its real
// field set. Guessing yields `statusCode 0` and no change, or worse.
//
// WHAT "A UNIT" MEANS DEPENDS ON THE PLATFORM, which is why a refusal message uses
// [driver.RawReader.RawKind] instead of talking about `wcd` sections: asking for a
// `wcd` section name to someone looking at a MikroTik would look like equipment failure
// when it is misunderstanding. A `wcd` section on Instant On, a `*.b` page on SwOS, a
// REST menu on RouterOS.
//
// It modifies nothing and can be replayed on a production fleet.
func (s *Server) handleReadRawSection(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	if d.MgmtIP == "" {
		respondErr(w, http.StatusUnprocessableEntity, errNoMgmtIP.Error())
		return
	}
	var conn, adm, _, err = s.adminFor(r.Context(), d)
	if err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": transcriptOf(adm)})
		return
	}
	// Raw read is OPTIONAL: a platform may have no notion of a configuration unit
	// readable as-is. The type assertion is what lets us say so instead of discovering
	// it on transport failure.
	var rr, canRead = adm.(driver.RawReader)
	if !canRead {
		respondErr(w, http.StatusUnprocessableEntity, driver.NotSupported(
			conn.Info().Name, "read a raw configuration unit",
			"this platform exposes no configuration unit readable as-is: there is no "+
				"name to request, and therefore nothing this reconnaissance tool can "+
				"return").Error())
		return
	}
	// SEVERAL units per request, comma-separated.
	//
	// Not a convenience: Instant On allows only FIVE simultaneous sessions, and one
	// session per read exhausts the device in a few requests — learned the hard way.
	// Recognizing an unknown set of units requires many reads.
	//
	// RouterOS menus contain slashes (`interface/bridge`): the route is
	// `/wcd/*` so the wildcard captures the full menu path.
	var out = map[string]any{"ok": true, "kind": rr.RawKind()}
	var found = map[string]string{}
	var sectionParam = chi.URLParam(r, "*")
	if sectionParam == "" {
		sectionParam = chi.URLParam(r, "section")
	}
	var name string
	for _, name = range strings.Split(sectionParam, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var body string
		body, err = rr.ReadRaw(r.Context(), name)
		if err != nil {
			// Stop at the first transport failure: insisting on a device that no longer
			// responds only makes it worse.
			out["ok"], out["error"] = false, err.Error()
			break
		}
		found[name] = body
	}
	out["sections"] = found
	out["steps"] = adm.Transcript()
	respond(w, http.StatusOK, out)
}

// handlePrepareMgmtMigration applies the FIRST half of the switchover: an IPv4
// interface as DHCP client on the plan's management VLAN.
//
// Nothing is removed and nothing is saved to flash. The device keeps its current
// address — so it stays reachable — and also acquires an address on the target network.
// This step makes the switchover VERIFIABLE instead of a bet: confirm the new address
// responds before cutting the old one.
//
// Refused if no active uplink carries the target VLAN: that would place an interface
// on a network the device cannot reach.
func (s *Server) handlePrepareMgmtMigration(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		VlanID int `json:"vlan_id"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	var vlanID, plan, refuse = s.resolveMgmtTargetVLAN(r.Context(), d, in.VlanID)
	if refuse != "" {
		respondErr(w, http.StatusUnprocessableEntity, refuse)
		return
	}
	// Operator-selected VLAN: any up access OR trunk carrying it. Plan-only
	// prepare (vlan_id omitted) keeps the plan's tagged/native preference.
	var state, reason string
	if in.VlanID > 0 {
		state, reason = mgmtVLANPathOf(d, vlanID, outOfBandOf(d))
	} else {
		state, reason = mgmtPathOf(d, plan, outOfBandOf(d))
	}
	if state == "absent" {
		respondErr(w, http.StatusUnprocessableEntity, reason)
		return
	}
	var conn, adm, _, err = s.adminFor(r.Context(), d)
	if err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": transcriptOf(adm)})
		return
	}
	var mover, canMove = mgmtMoverOf(conn, adm)
	if !canMove {
		respondErr(w, http.StatusUnprocessableEntity, mgmtMoverRefusal(conn))
		return
	}
	if err = mover.PrepareMgmt(r.Context(), vlanID); err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": adm.Transcript(),
		})
		return
	}
	s.emitDeviceEvent(r, d, "mgmt.migration_prepared", model.SevWarning,
		"Management interface added on VLAN "+itoa(vlanID)+" of \""+
			d.Name+"\". Not saved to flash: power loss cancels it.")
	var out = map[string]any{
		"ok": true, "vlan": vlanID, "path_state": state,
		"steps": adm.Transcript(),
		"note":  "not saved to flash — verify the new address before switching over",
	}
	// USEFUL PROOF IS [driver.MgmtMover.MgmtAddressed], not this text: it says whether
	// the prepared access obtained an address, and the second half requires it. Raw
	// re-read is only a COMFORT for debugging — seeing the device's interface list — so
	// it is conditional on raw read capability, and absence is not a missing signal.
	var rr, canRead = adm.(driver.RawReader)
	if canRead {
		var after, rawErr = rr.ReadRaw(r.Context(), "IPv4InterfaceList")
		if rawErr != nil {
			after = "re-read failed: " + rawErr.Error()
		}
		out["interfaces"] = after
	}
	respond(w, http.StatusOK, out)
}

// handleCommitMgmtMigration switches administration to the plan VLAN.
//
// SECOND half, and it requires the first to have succeeded: refuse if the target VLAN
// interface has NOT OBTAINED an address. Moving administration to a VLAN where the
// device has no address loses it — and this line has neither SSH nor serial.
//
// Still no flash save. Until something is written, power loss restores prior state,
// and that safety net allows attempting the maneuver on in-service hardware.
func (s *Server) handleCommitMgmtMigration(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		VlanID int `json:"vlan_id"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	var vlanID, _, refuse = s.resolveMgmtTargetVLAN(r.Context(), d, in.VlanID)
	if refuse != "" {
		respondErr(w, http.StatusUnprocessableEntity, refuse)
		return
	}
	var conn, adm, _, err = s.adminFor(r.Context(), d)
	if err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": transcriptOf(adm)})
		return
	}
	var mover, canMove = mgmtMoverOf(conn, adm)
	if !canMove {
		respondErr(w, http.StatusUnprocessableEntity, mgmtMoverRefusal(conn))
		return
	}
	var addressed bool
	var addr string
	if addressed, addr, err = mover.MgmtAddressed(r.Context(), vlanID); err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": adm.Transcript()})
		return
	}
	if !addressed {
		respondErr(w, http.StatusUnprocessableEntity,
			"VLAN "+itoa(vlanID)+" interface has not obtained an address yet: "+
				"switching now would make the device unreachable. Prepare the migration, "+
				"wait for the lease, then try again.")
		return
	}
	// ORDER CONSTRAINT IS IN THE CONNECTOR now, and that is the right place:
	// [driver.MgmtMover.CommitMgmt] does everything the platform requires in the order
	// it requires. On Instant On, that means remove the relay interface THEN designate
	// the management VLAN — the device refuses a management VLAN that already carries IP
	// configuration, because it will install the address there.
	//
	// The API layer used to apply those two gestures itself, in sequence. It worked, and
	// it could not last: splitting an order constraint across two packages guarantees
	// one day forgets its half, and that half costs a site move where the device has no
	// address left.
	//
	// This is the only truly delicate moment: between removal and obtaining the new
	// lease, the device has no address on the target VLAN. It will get it back — same MAC,
	// so very likely the same lease — and with nothing written to flash, power loss rolls
	// everything back.
	if err = mover.CommitMgmt(r.Context(), vlanID); err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": adm.Transcript(),
		})
		return
	}
	var newIP string
	if addr != "" {
		var adoptErr error
		newIP, adoptErr = s.adoptWrittenMgmtIP(r.Context(), d, addr, "")
		if adoptErr != nil {
			fail(w, adoptErr)
			return
		}
	}
	var migMsg = "Administration of \"" + d.Name + "\" switched to VLAN " + itoa(vlanID) +
		" (" + addr + "). Not saved to flash: power loss cancels it."
	if newIP != "" {
		migMsg += " Reachability address updated to " + newIP + "."
	} else {
		migMsg += " " + s.kickRelocateAfterMgmtWrite(r.Context(), d, "management VLAN migrate",
			addr) + "."
	}
	s.emitDeviceEvent(r, d, "mgmt.migrated", model.SevWarning, migMsg)
	var out = map[string]any{
		"ok": true, "vlan": vlanID, "address": addr,
		"steps": adm.Transcript(),
		"note":  "not saved to flash — verify access before making permanent",
	}
	if newIP != "" {
		out["mgmt_ip"] = newIP
	} else {
		out["note"] = "new address unknown after migrate — relocate started when possible; not saved to flash"
	}
	// Debugging comfort, as in preparation: proof that matters is the device DECLARES
	// it administers via this VLAN, and `ReadState().MgmtVLAN` carries it — persistence
	// already requires it before writing anything.
	var rr, canRead = adm.(driver.RawReader)
	if canRead {
		var after, rawErr = rr.ReadRaw(r.Context(), "ManagementInterfaceGlobalSetting")
		if rawErr != nil {
			after = "re-read failed: " + rawErr.Error()
		}
		out["management"] = after
	}
	respond(w, http.StatusOK, out)
}

// handlePersistMgmtMigration makes the switchover PERMANENT by saving to flash.
//
// Third and final gesture, separated from the other two on purpose. Until it happens,
// power loss restores prior addressing — the safety net that allows attempting the
// maneuver. But that net becomes a trap once the switchover is confirmed good: meshcraft
// then knows the new address, and a simple power cut would restore the old one unnoticed.
//
// We therefore REQUIRE confirming the device responds on the target VLAN first: saving an
// unverified switchover would burn in an error.
func (s *Server) handlePersistMgmtMigration(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		VlanID int `json:"vlan_id"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	var vlanID, _, refuse = s.resolveMgmtTargetVLAN(r.Context(), d, in.VlanID)
	if refuse != "" {
		respondErr(w, http.StatusUnprocessableEntity, refuse)
		return
	}
	var conn, adm, _, err = s.adminFor(r.Context(), d)
	if err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": transcriptOf(adm)})
		return
	}
	// SWITCHOVER IS INSTANT-ON ONLY, and this assertion is what makes that VISIBLE. It
	// is refused here, not via transport failure: "this platform cannot move its
	// management addressing under the session that commands it" is fixed — or not — by
	// gestures unrelated to "the device does not respond".
	var _, canMove = mgmtMoverOf(conn, adm)
	if !canMove {
		respondErr(w, http.StatusUnprocessableEntity, mgmtMoverRefusal(conn))
		return
	}
	// PROOF REQUIRED BEFORE BURNING IN: the device DECLARES it administers via the plan
	// VLAN, and said so at the address meshcraft just used to reach it. This check is
	// kept word-for-word from pre-contract code — saving an unverified switchover burns
	// an error, and on a line with no out-of-band escape, that costs a truck roll.
	var state driver.State
	if state, err = adm.ReadState(r.Context()); err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": adm.Transcript(),
		})
		return
	}
	if state.MgmtVLAN != vlanID {
		respondErr(w, http.StatusUnprocessableEntity,
			"device declares administration via VLAN "+itoa(state.MgmtVLAN)+
				", not target VLAN "+itoa(vlanID)+": nothing to make permanent, and "+
				"saving would burn in an unverified state.")
		return
	}
	// Persistence is the THIRD gesture, caller's responsibility: the contract's sole
	// exception, so the safety net — not saving — stays with the operator (cf.
	// driver.MgmtMover). A platform without [driver.Persister] has nothing to save; but
	// if it can move management without persisting, we must SAY SO rather than imply a
	// burned-in state.
	var saver, canSave = adm.(driver.Persister)
	if !canSave {
		respondErr(w, http.StatusUnprocessableEntity, driver.NotSupported(
			conn.Info().Name, "make the switchover permanent",
			"this platform applies and persists in one gesture: there is no separate "+
				"startup configuration to write. The switchover is ALREADY definitive, and "+
				"that is what you must know — there is no rollback via power loss here").Error())
		return
	}
	if err = saver.Persist(r.Context()); err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": adm.Transcript(),
		})
		return
	}
	s.emitDeviceEvent(r, d, "mgmt.migration_persisted", model.SevNotice,
		"Switchover of \""+d.Name+"\" to VLAN "+itoa(vlanID)+
			" saved to flash: it will survive a reboot.")
	respond(w, http.StatusOK, map[string]any{
		"ok": true, "vlan": state.MgmtVLAN, "steps": adm.Transcript(),
	})
}

// --- THE SINGLE RESOLUTION POINT ---
//
// Everything touching a device goes through here, and that is this batch's reason for
// being: while each handler called `instanton.OpenAdmin` hard-coded, none could pick the
// wrong platform — they all did, all the time, visible only on a single-vendor fleet.
// The defect surfaced: adopting a MikroTik switch sent SNMP provisioning in Instant On
// protocol to it, `https://10.99.0.54/`, connection refused because that device does not
// listen on 443.

// connectorFor returns the connector DISCOVERY designated for this device, or why it is
// missing.
//
// Name comes from `devices.extra.driver`, falling back to the discovery candidate (cf.
// deviceIdentity): a fact observed on the device, never deduced at write time.
// [driver.ConnectorFor] then distinguishes three refusals fixed by unrelated gestures —
// no associated connector, connector unknown to this binary, connector that describes the
// platform but cannot reach it.
func (s *Server) connectorFor(ctx context.Context, d model.Device) (driver.Connector, error) {
	var _, name = s.deviceIdentity(ctx, d)
	return driver.ConnectorFor(name)
}

// accessFor gathers everything needed to reach a device, WITHOUT reaching it.
//
// Separated from [Server.adminFor] for a reason that is not style: what the platform
// cannot WRITE must be announced even when connection fails (cf. unsupportedNotes), so
// the connector must be resolved before transport exists.
//
// ACCOUNT NAME IS RESOLVED HERE, a fundamental fix from this batch: candidates were built
// on raw `cred.Username`, i.e. the name the operator WANTS (`mcadmin`). On a platform
// with no account notion — SwOS — presenting that name fails Digest on a nonexistent
// name, and failure reads "password refused". [driver.AdminAccount.EffectiveUsername]
// decides BEFORE building the list, as the contract requires.
func (s *Server) accessFor(ctx context.Context, d model.Device) (driver.Connector,
	driver.Target, []driver.Candidate, error) {
	var tgt driver.Target
	var conn, connErr = s.connectorFor(ctx, d)
	if connErr != nil {
		return nil, tgt, nil, connErr
	}
	if d.MgmtIP == "" {
		return conn, tgt, nil, errNoMgmtIP
	}
	var dialer, dialErr = s.dialerFor(s.proxyOf(ctx, d))
	if dialErr != nil {
		return conn, tgt, nil, dialErr
	}
	var groupName = s.groupNameOfCtx(ctx, d)
	var cred, _, credErr = s.credentialForDevice(ctx, d, groupName)
	if credErr != nil {
		return conn, tgt, nil, credErr
	}
	var wanted = conn.Capabilities(d.Model).Admin.EffectiveUsername(cred.Username)
	var cands, _, _, _, buildErr = s.candidatesFor(ctx, d, wanted, groupName, "")
	if buildErr != nil {
		return conn, tgt, nil, buildErr
	}
	// Address is WITHOUT scheme or port: the connector knows how its platform is reached
	// (443 https for Instant On, 80 http for SwOS which listens ONLY there, 80 for RouterOS
	// REST with 443 closed for lack of factory certificate). The caller must not decide —
	// exactly the fault this batch closes.
	tgt = driver.Target{
		Host: d.MgmtIP, Dialer: dialer, Model: d.Model, PortPrefix: portPrefix(d),
	}
	return conn, tgt, cands, nil
}

// adminFor returns PROVEN administration access to the device, and the aggregate the
// caller needs around it.
//
// FOUR VALUES NOT ONE, each serving a purpose: `Connector` carries the name that makes
// refusal messages readable and supports capability assertions, `Target` will serve a
// second call without rebuilding transport, and `Admin` is the access. Recomputing them
// per handler would eventually make two variables designate two different devices — exactly
// what the contract warns against.
//
// ONE [driver.Connector.Authenticate] per request. Not economy: Instant On allows only
// FIVE simultaneous sessions and try budget is capped because these platforms LOCK accounts.
//
// ACCESS MAY BE NIL ON FAILURE, and the caller must tolerate it. The three shipped
// connectors differ: Instant On and RouterOS return access even on failure so transcript
// survives, SwOS returns nil so unproven access does not circulate. Both are valid. Caller
// rule is unique: ERROR message is the detail source, transcript shown only when present
// (cf. transcriptOf), and a connector that returns the session anyway is a bonus — never a
// dependency.
func (s *Server) adminFor(ctx context.Context, d model.Device) (driver.Connector,
	driver.Admin, driver.Target, error) {
	var conn, tgt, cands, err = s.accessFor(ctx, d)
	if err != nil {
		return conn, nil, tgt, err
	}
	var adm driver.Admin
	adm, err = conn.Authenticate(ctx, tgt, cands)
	return conn, adm, tgt, err
}

// transcriptOf returns the transcript of access THAT MAY BE NIL.
//
// One line carrying the rule above: without it, every API failure path would risk nil
// dereference on SwOS connector — i.e. controller panic exactly where the operator
// expects an error message.
func transcriptOf(adm driver.Admin) []driver.Step {
	if adm == nil {
		return nil
	}
	return adm.Transcript()
}

// mgmtMoverOf tests ability to MOVE management addressing.
//
// A function rather than an assertion copied in three handlers: the three switchover
// steps (prepare, switch, make permanent) must refuse on the same criterion with the same
// phrase. Two diverging would let the screen offer a second half the first already refused.
func mgmtMoverOf(conn driver.Connector, adm driver.Admin) (driver.MgmtMover, bool) {
	if adm == nil || conn == nil {
		return nil, false
	}
	var m, ok = adm.(driver.MgmtMover)
	return m, ok
}

// mgmtMoverRefusal says why switchover is not offered on this platform, and especially
// whether anything is worth waiting for.
//
// The contract requires ADDING an administration path, confirming it responds, then
// switching: three verifiable steps. A platform that cannot carry two addresses at once
// cannot honor that, and the right answer is to NOT declare it — rather than jump blindly.
// Nothing regresses by refusing here: it never worked outside Instant On. What changes is
// absence becomes VISIBLE instead of looking like transport failure.
func mgmtMoverRefusal(conn driver.Connector) string {
	var name = conn.Info().Name
	var caps = conn.Capabilities("").Mgmt
	// Single-address platforms (SwOS today; JumpVLAN when proven): dual-path
	// is structurally impossible — say so, and point at jump or manual Notes.
	if caps.JumpVLAN || (!caps.OutOfBand && !caps.MigrateVLAN) {
		return driver.NotSupported(name,
			"move this device's management addressing",
			"the dual-path migrate procedure needs ADDING an administration path on the "+
				"target VLAN, confirming it obtained an address, then only then switching. "+
				"This platform cannot carry two addresses at once. Use "+
				"« Conform Management » (`mgmt-addressing/jump`) when offered, or change "+
				"addressing by hand — connector capabilities (`mgmt.notes`) give the "+
				"exact order of operations").Error()
	}
	// Dual-path is possible (e.g. RouterOS) but not implemented / not proven yet.
	return driver.NotSupported(name,
		"move this device's management addressing",
		"the procedure requires ADDING an administration path on the target VLAN, "+
			"confirming it obtained an address, then only then switching over. This "+
			"connector does not yet implement that dual-path migrate (or has not proven "+
			"the writes on hardware). Connector capabilities (`mgmt.notes`) give the "+
			"exact order of operations for a manual change").Error()
}

// handleImportDeviceConfig ABSORBS device configuration: what it carries becomes what
// meshcraft wants (section 6, "absorb").
//
// Two precautions that separate absorbing from overwriting:
//
//   - port LABELS are kept. The device does not know them, and losing them would punish
//     the operator for documenting their fleet;
//   - VLANs missing from the site are CREATED. Importing configuration referencing unknown
//     VLANs would produce invalid intent nothing could apply again.
func (s *Server) handleImportDeviceConfig(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	if d.ObservedAt == nil {
		respondErr(w, http.StatusUnprocessableEntity,
			"this device has never been read back: nothing to import")
		return
	}
	var obs observedState
	if json.Unmarshal(d.Observed, &obs) != nil {
		respondErr(w, http.StatusUnprocessableEntity, "unreadable observed state")
		return
	}

	var created, err = s.ensureVlans(r.Context(), d.SiteID, obs)
	if err != nil {
		fail(w, err)
		return
	}

	// Merge: configuration comes from the device, label stays meshcraft's.
	var existing map[string]map[string]any
	if json.Unmarshal(d.Ports, &existing) != nil {
		existing = map[string]map[string]any{}
	}
	var merged = map[string]map[string]any{}
	var name string
	var p driver.PortState
	for name, p = range obs.Ports {
		var port = map[string]any{"mode": p.Mode, "pvid": p.PVID}
		if p.Tagged == nil {
			port["tagged"] = []int{}
		} else {
			port["tagged"] = p.Tagged
		}
		var prev, had = existing[name]
		if had {
			var key string
			for _, key = range []string{"label", "link", "poe", "poe_w", "rx_mbps", "tx_mbps"} {
				var v, seen = prev[key]
				if seen {
					port[key] = v
				}
			}
		}
		merged[name] = port
	}

	var raw []byte
	if raw, err = json.Marshal(merged); err != nil {
		fail(w, err)
		return
	}
	if err = s.Store.AbsorbObserved(r.Context(), d.ID, raw); err != nil {
		fail(w, err)
		return
	}
	// Intent that failed was just abandoned: trace of that apply no longer applies. Keeping
	// it would show perpetual failure, with a "retry" button that would re-apply configuration
	// that no longer exists. An error here does not cancel import, which did succeed.
	_ = s.Store.ClearApplyOutcome(r.Context(), d.ID)
	s.emitDeviceEvent(r, d, "config.absorbed", model.SevNotice,
		"Configuration of \""+d.Name+"\" imported into meshcraft ("+
			itoa(len(merged))+" port(s), "+itoa(len(created))+" VLAN(s) created).")
	respond(w, http.StatusOK, map[string]any{
		"ok": true, "ports": len(merged), "vlans_created": created,
	})
}

// ensureVlans creates site VLANs the device references that meshcraft lacks. Returns
// those created so the UI can say so.
func (s *Server) ensureVlans(ctx context.Context, siteID string,
	obs observedState) ([]int, error) {
	var known, err = s.Store.ListVlans(ctx, siteID)
	if err != nil {
		return nil, err
	}
	var have = map[int]bool{}
	var v model.Vlan
	for _, v = range known {
		have[v.VlanID] = true
	}

	// All cited VLANs: declared ones, and those ports carry.
	var wanted = map[int]string{}
	var dv driver.VLAN
	for _, dv = range obs.VLANs {
		wanted[dv.ID] = dv.Name
	}
	var p driver.PortState
	for _, p = range obs.Ports {
		if p.PVID > 0 {
			var _, seen = wanted[p.PVID]
			if !seen {
				wanted[p.PVID] = ""
			}
		}
		var id int
		for _, id = range p.Tagged {
			var _, seen = wanted[id]
			if !seen {
				wanted[id] = ""
			}
		}
	}

	var ids []int
	var id int
	for id = range wanted {
		if !have[id] {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)

	var created []int
	for _, id = range ids {
		var name = wanted[id]
		if name == "" {
			// A VLAN without a name on the device gets an explicit one: "VLAN 42" reads;
			// a blank line in the VLAN list does not.
			name = "VLAN " + itoa(id)
		}
		if _, err = s.Store.CreateVlan(ctx, siteID, id, name,
			"#8b96a8", "imported from device"); err != nil {
			// A VLAN created in the meantime must not fail import.
			continue
		}
		created = append(created, id)
	}
	return created, nil
}

// LIST enrichment now lives in withConformity: it must happen AFTER conformity
// calculation, which needs observed state this enrichment clears. Separating them left
// order to the caller, and reversing it was invisible.

// errNoMgmtIP: without an address, there is nothing to reach.
var errNoMgmtIP = errors.New("this device has no known management address")

// saveObservedState records what the device carries, after readback.
//
// `state.Notes` is NOT kept, deliberately: phrases for an operator, written at read time,
// not state to compare. Storing them in `devices.observed` would enter drift calculation
// where they decide nothing.
func (s *Server) saveObservedState(ctx context.Context, deviceID string,
	state driver.State) error {
	var raw, err = json.Marshal(observedState{
		VLANs: state.VLANs, Ports: state.Ports, LinkUp: state.LinkUp(), Links: state.Links,
		MgmtVLAN: state.MgmtVLAN,
	})
	if err != nil {
		return err
	}
	return s.Store.SaveObserved(ctx, deviceID, raw)
}

// groupNameOfCtx: groupNameOf without HTTP request, for auto-apply.
func (s *Server) groupNameOfCtx(ctx context.Context, d model.Device) string {
	if d.GroupID == nil || *d.GroupID == "" {
		return ""
	}
	var g, err = s.Store.GetGroup(ctx, *d.GroupID)
	if err != nil {
		return ""
	}
	return g.Name
}

// handleApplyDeviceConfig APPLIES meshcraft configuration to the device: section 6
// "reapply", section 5 Apply step.
//
// Apply is normally AUTOMATIC after a change (cf. autoapply.go). This button remains
// useful for two cases: recover after failure, and resolve drift in meshcraft's favor.
//
// Reserved to `site_admin`: applying network configuration is not the same gesture as
// labeling a port.
func (s *Server) handleApplyDeviceConfig(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		// Force lifts the safeguard protecting administration ports. Never implicit: the
		// operator accepts the risk.
		Force bool `json:"force"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	var res, err = s.applyDeviceConfig(r.Context(), d, in.Force)
	var out = map[string]any{
		"steps": res.Steps, "result": res.Result, "unsupported": res.Unsupported,
		// What the connector observed on RE-READ: VLAN configuration may be applied yet
		// INERT ("disabled" mode on SwOS, inactive `vlan-filtering` on RouterOS). Without
		// these phrases, the screen would show segmentation that does not exist.
		"notes": res.Notes,
	}
	if err != nil {
		out["ok"], out["error"] = false, err.Error()
		// Trace and pipeline are owned by applyDeviceConfig: failure must leave the same
		// footprint whether from the button or auto-apply, or the retry banner shows half.
		s.emitDeviceEvent(r, d, "config.apply_failed", model.SevWarning,
			"Configuration not applied on \""+d.Name+"\": "+err.Error())
		respond(w, http.StatusOK, out)
		return
	}
	out["ok"], out["drift"], out["sync"] = true, res.Remaining, res.Sync
	if in.Force {
		s.emitDeviceEvent(r, d, "config.forced", model.SevWarning,
			"FORCED apply on \""+d.Name+"\": safeguard protecting administration ports "+
				"was lifted by the operator.")
	}
	s.emitDeviceEvent(r, d, "config.applied", model.SevNotice,
		"Configuration applied on \""+d.Name+"\": "+itoa(len(res.Result.PortsWritten))+
			" port(s) written, "+itoa(len(res.Result.VLANsCreated))+" VLAN(s) created, "+
			itoa(len(res.Result.Skipped))+" skipped, "+itoa(len(res.Remaining))+
			" drift(s) remaining.")
	respond(w, http.StatusOK, out)
}

// noteDesiredChanged timestamps an intent change, copies the date to the returned object,
// and SCHEDULES apply.
//
// An error does not interrupt the caller: configuration IS saved, and refusing the request
// because a timestamp could not be set would be disproportionate.
func (s *Server) noteDesiredChanged(ctx context.Context, d *model.Device) {
	if s.Store.TouchDesired(ctx, d.ID) == nil {
		var now = time.Now()
		d.DesiredChangedAt = &now
	}
	// Configuring in the UI IS approval: apply follows, batched with other changes in the
	// same burst.
	s.schedule(d.ID)
}
