package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/recon"
)

// DHCP snooping, both halves: the automatic profile-driven path and the
// immediate operator gesture. Sibling of poe_apply.go, and deliberately shaped
// the same way — the two features differ in what they protect, not in how the
// API layer must offer them.
//
// # The bool on the wire is TRUST, not "enable"
//
// Everything below carries [driver.DHCPSnoopPortIntent.Trusted]. The driver
// contract (internal/driver/dhcpsnoop.go) explains at length why "enable
// snooping on this port" has no meaning a connector could act on: snooping is
// a switch-wide feature, per-VLAN below that, and the only per-port control is
// whether DHCP SERVER replies are accepted there. Untrusted is the protected
// state AND the platforms' own default. So the frontend sends `true` for "this
// is the uplink toward the real DHCP server", and that is the whole vocabulary.
//
// GLOBAL AND PER-VLAN ENABLE ARE THE CONNECTOR'S JOB. The plan carries the
// VLANs to inspect and the connector turns the feature on over them; nothing
// here pushes a global switch, and nothing here ever turns the feature OFF.
//
// # Where the VLANs come from, and why the frontend never sends them
//
// FROM THE PORTS THE GESTURE NAMES, server-side — their PVID and their tagged
// membership, resolved by dhcpSnoopVLANsFor. Until 2026-08-08 this file sent the
// SITE's whole list instead (Server.siteVLANIDs), and that was wrong in both
// directions: it switched inspection on across every VLAN a site declares
// because one uplink was trusted, and it still missed the VLAN a named port
// actually carries when the site does not declare it. A port trusted outside
// every inspected VLAN does not even appear in some platforms' trust tables, so
// the gesture reads back as a success and protects nothing (established on the
// lab EdgeSwitch — cf. dhcpSnoopVLANsFor's own comment).
//
// The site list survives as the FALLBACK for what cannot be enumerated — a port
// meshcraft holds no intent for, or one carrying "all" VLANs — and this layer
// says so in ApplyResult.Notes when it falls back, because the widening is a
// decision made here and not something the device did.
//
// A caller naming its own VLAN list is still refused the chance: it could enable
// snooping on a VLAN meshcraft does not model, that no later pass would ever
// reconcile or remove. An empty result is a legitimate answer and NOT an error —
// nothing was derivable and the site declares nothing either — and the connector
// then turns the global feature on and says in ApplyResult.Notes that snooping
// covers nothing yet.

// applyDeviceDHCPSnooping trusts every port that requests DHCP snooping, when
// the connector implements DHCPSnoopApplier and declares
// WritesExtra("dhcp_snooping").
//
// ENABLE ONLY — the same constraint as applyDevicePoE, and it comes from the
// same place: recon.RequestedExtras only ever reports POSITIVE intents (cf. its
// comment, "only positive values count"), so this path can only ever ask a
// connector to TRUST a port. It can never untrust one, and it must not try:
// a stored `dhcp_snooping: false` is a neutral value, indistinguishable from a
// port that simply says nothing, and re-untrusting on that basis would take a
// trust flag away from an uplink somebody set on purpose.
//
// The consequence the operator must live with is the mirror of PoE's: removing
// trust is an EXPLICIT gesture through the HTTP endpoint below, never a
// consequence of an automatic pass.
//
// THE STORED SHAPE IS A BARE BOOL: `ports.GE5.dhcp_snooping = true` means
// TRUSTED. Not a nested object — recon.RequestedExtras reads `dhcp_snooping`
// through positiveIntent, which accepts a bool directly, and a nested object
// would be read as "any true field counts", i.e. `{trusted:false}` would come
// back as a positive intent. That is the one shape that would invert the
// meaning of the flag while looking correct.
func (s *Server) applyDeviceDHCPSnooping(ctx context.Context, d model.Device,
	conn driver.Connector, adm driver.Admin) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if !conn.WritesExtra("dhcp_snooping") {
		return out, nil
	}
	var ports = recon.RequestedExtras(d.Ports)["dhcp_snooping"]
	if len(ports) == 0 {
		return out, nil
	}
	var applier, ok = driver.AsDHCPSnoopApplier(adm)
	if !ok {
		out.Notes = append(out.Notes,
			"connector declares DHCP snooping writable but Admin has no ApplyDHCPSnooping")
		return out, nil
	}
	var plan driver.DHCPSnoopPlan
	var name string
	for _, name = range ports {
		plan.Ports = append(plan.Ports,
			driver.DHCPSnoopPortIntent{Port: name, Trusted: true})
	}
	// The VLANs come from the ports being trusted, not from the site: trusting a
	// port on a VLAN nothing inspects is a write the device accepts and ignores
	// (cf. dhcpSnoopVLANsFor).
	var why string
	plan.VLANs, why = dhcpSnoopVLANsFor(d, ports, s.siteVLANIDs(ctx, d.SiteID))
	var res, err = applier.ApplyDHCPSnooping(ctx, plan)
	if why != "" {
		res.Notes = append(res.Notes, why)
	}
	return res, err
}

// handleApplyDeviceDHCPSnooping pushes explicit per-port TRUST flags onto one
// device, NOW — the immediate half of the port panel's snooping control, next
// to the stored intent the panel writes through PUT /ports.
//
// Body: `{ "ports": { "GE5": true, "GE6": false } }`, where the bool is TRUSTED
// and is taken at face value in BOTH directions. That is the difference with
// the automatic path above and the reason this endpoint exists at all: untrust
// is only reachable from here, because only here did a human name it.
//
// Same privilege as the other device writes that touch hardware (site_admin).
// Trusting a port is not a cosmetic change: it is the one setting that decides
// whether a rogue DHCP server on that jack can hand out addresses to a whole
// VLAN, and getting it backwards protects nothing while showing a protection.
//
// DELIBERATELY STATELESS about intent, exactly like handleApplyDevicePoE: the
// caller keeps writing the `dhcp_snooping` intent through PUT /ports, and this
// endpoint carries only the wire half. Splitting them is what keeps the
// automatic path honest (it stays enable-only, so an untrust done here is never
// silently re-trusted by a later auto-apply) and what keeps a recorded decision
// alive when a device refuses the write.
//
// WHAT IS NOT DONE HERE: nothing checks that the device's global snooping ends
// up on. The connector turns it on as part of the same gesture and SAYS SO in
// ApplyResult.Notes — those notes ride back in `result`, and a UI that hides
// them will show a trust flag on a device that inspects nothing.
func (s *Server) handleApplyDeviceDHCPSnooping(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		Ports map[string]bool `json:"ports"`
	}
	if !decode(w, r, &in) {
		return
	}
	var intents, refusal = dhcpSnoopIntents(in.Ports)
	if refusal.respond(w) {
		return
	}

	// The platform is judged BEFORE anything is dialled, and the order matters
	// beyond tidiness: Instant On allows five simultaneous sessions and these
	// platforms lock accounts (cf. Server.adminFor). A gesture that will be
	// refused anyway must not spend one.
	var conn, connErr = s.connectorFor(r.Context(), d)
	refusal = dhcpSnoopPlatformRefusal(conn, connErr)
	if refusal.respond(w) {
		return
	}

	// The VLAN list is resolved BEFORE the device is reached, and its failure is
	// not fatal: siteVLANIDs returns an empty list on a read error, which the
	// contract defines as "the site declares no VLANs" — the connector then
	// enables the global feature only and says so. Refusing the write instead
	// would leave the operator with an untrusted uplink and a database hiccup to
	// explain it.
	//
	// It is derived from THE PORTS THIS REQUEST NAMES, not from the whole site:
	// the gesture is per-port, and a port trusted on a VLAN nothing inspects does
	// not even appear in some platforms' trust tables (cf. dhcpSnoopVLANsFor).
	var plan driver.DHCPSnoopPlan
	var named = make([]string, 0, len(intents))
	var i int
	for i = range intents {
		named = append(named, intents[i].Port)
	}
	var vlanWhy string
	plan.VLANs, vlanWhy = dhcpSnoopVLANsFor(d, named, s.siteVLANIDs(r.Context(), d.SiteID))
	plan.Ports = intents

	var _, adm, _, err = s.adminFor(r.Context(), d)
	if adm != nil {
		defer adm.Close()
	}
	var applier driver.DHCPSnoopApplier
	applier, refusal = dhcpSnoopApplierFor(adm, err)
	if refusal.respond(w) {
		return
	}
	var res driver.ApplyResult
	res, err = applier.ApplyDHCPSnooping(r.Context(), plan)
	refusal = dhcpSnoopDeviceRefusal(err)
	if refusal.respond(w) {
		return
	}

	// Say it here rather than in the connector: the fallback is a decision this
	// layer made about what to ask for, not something the device did.
	if vlanWhy != "" {
		res.Notes = append(res.Notes, vlanWhy)
	}

	// Refresh the stored observation in the same gesture, best-effort — the
	// same reasoning, line for line, as handleApplyDevicePoE's re-read: the
	// panel repaints from `dhcp_snoop_state`, and leaving the pre-write snapshot
	// in place would show the old trust flags exactly when the operator watches
	// for the new ones. It matters more here than for PoE, because the
	// observation is also where the GLOBAL enable becomes visible: the connector
	// may have turned snooping on for the whole box as a side effect of this
	// click, and the re-read is what puts that on screen. Failure only
	// annotates — the write above stands.
	s.refreshDHCPSnoopObserved(r.Context(), d.ID, adm, &res)

	s.emitDeviceEvent(r, d, "device.dhcp_snooping_apply", model.SevInfo,
		"DHCP snooping trust written on \""+d.Name+"\": "+dhcpSnoopApplySummary(in.Ports))
	respond(w, http.StatusOK, map[string]any{
		"ok":     true,
		"result": res,
		"steps":  transcriptOf(adm),
	})
}

// --- THE REFUSALS, JUDGED APART FROM THE HANDLER ---

// dhcpSnoopRefusal is the answer given INSTEAD of writing: an HTTP status and
// the sentence that goes in it. The zero value means "nothing to refuse".
//
// A TYPE, with the judgements below as PURE FUNCTIONS returning it, for exactly
// the reason dhcpSnoopIntents already gives about the body — extended to the
// rest of the ladder because the status codes are judgements of the same
// weight. internal/api tests run without PostgreSQL (Server.Store is a concrete
// *store.Store), so nothing that begins with deviceForWrite can be driven from
// a test: left inline, these codes were unassertable and drifted silently.
//
// What each one asserts, and why it is not interchangeable with its neighbour:
//
//   - 400 — the REQUEST is wrong, and no device was involved. Resending it
//     unchanged will fail again.
//   - 422 — the request is well formed and this PLATFORM will not do it. It is
//     the guard that stops a UI offering a trust control on a range where
//     nobody proved the write, and "stop looking" is a different instruction
//     from "try again".
//   - 502 — the device, or the path to it, said no. The gesture was legitimate
//     and may well succeed on the next attempt.
//
// Collapsing 422 into 502 would send an operator hunting a network fault that
// does not exist; collapsing 502 into 422 would tell them their hardware cannot
// do something it does every day.
type dhcpSnoopRefusal struct {
	Code int
	Msg  string
}

// respond writes the refusal and says whether it wrote one, so each rung reads
// `if refusal.respond(w) { return }`.
//
// A ZERO REFUSAL WRITES NOTHING and returns false. That is the property that
// makes the shape safe: a caller reaching a rung it must not refuse leaves the
// response untouched rather than committing a 0-status header that would freeze
// everything written after it.
func (ref dhcpSnoopRefusal) respond(w http.ResponseWriter) bool {
	if ref.Code == 0 {
		return false
	}
	respondErr(w, ref.Code, ref.Msg)
	return true
}

// dhcpSnoopPlatformRefusal judges what can be judged WITHOUT reaching the
// device: is there a connector, and does it declare this write?
//
// 422 for both, and the two messages stay distinct because they are repaired by
// unrelated gestures — re-run discovery so the device gets a connector, versus
// nothing at all, this hardware is simply not proven for snooping. The
// connector's own sentence is carried VERBATIM: driver.ConnectorFor writes three
// different explanations (no connector, connector unknown to this binary,
// connector that describes the platform without reaching it) and each names the
// repair, which a message rewritten here would throw away.
//
// A nil connector with no error is not something ConnectorFor produces; it is
// refused rather than dereferenced, because the alternative is a panic in a
// handler whose whole job is to answer a status code.
func dhcpSnoopPlatformRefusal(conn driver.Connector, connErr error) dhcpSnoopRefusal {
	if connErr != nil {
		return dhcpSnoopRefusal{Code: http.StatusUnprocessableEntity, Msg: connErr.Error()}
	}
	if conn == nil {
		return dhcpSnoopRefusal{Code: http.StatusUnprocessableEntity,
			Msg: "no connector is associated with this device"}
	}
	if !conn.WritesExtra("dhcp_snooping") {
		return dhcpSnoopRefusal{Code: http.StatusUnprocessableEntity,
			Msg: "this platform does not declare DHCP snooping as writable"}
	}
	return dhcpSnoopRefusal{}
}

// dhcpSnoopApplierFor turns proven access into the applier to write through, or
// the refusal to answer with.
//
// THE ERROR IS TESTED FIRST, and that ordering is the judgement: adminFor may
// return an Admin AND an error at once — Instant On and RouterOS hand back the
// session on failure so the transcript survives (cf. Server.adminFor). Asserting
// the interface first would find a perfectly good applier on unproven access and
// answer 422 "this platform cannot do that" about a device that merely refused
// the password.
//
// The 422 that follows is a STAGING mismatch, not a hardware limit: the
// connector declared dhcp_snooping writable and its Admin has no
// ApplyDHCPSnooping. Same sentence as the automatic path's note, deliberately —
// the two paths meet the same half-built connector and must say the same thing.
func dhcpSnoopApplierFor(adm driver.Admin, admErr error) (driver.DHCPSnoopApplier, dhcpSnoopRefusal) {
	var refusal = dhcpSnoopDeviceRefusal(admErr)
	if refusal.Code != 0 {
		return nil, refusal
	}
	var applier, ok = driver.AsDHCPSnoopApplier(adm)
	if !ok {
		return nil, dhcpSnoopRefusal{Code: http.StatusUnprocessableEntity,
			Msg: "connector declares DHCP snooping writable but Admin has no ApplyDHCPSnooping"}
	}
	return applier, dhcpSnoopRefusal{}
}

// dhcpSnoopDeviceRefusal turns a failure the DEVICE or the path to it produced
// into 502, and nothing else into anything.
//
// One function for both places that can fail that way — reaching the device and
// writing on it — so the two cannot drift into two different codes for the same
// kind of event. 502 and not 500: nothing in the controller is broken, an
// upstream did not cooperate, and the operator's next move is to look at the
// switch rather than at meshcraft's logs.
func dhcpSnoopDeviceRefusal(err error) dhcpSnoopRefusal {
	if err == nil {
		return dhcpSnoopRefusal{}
	}
	return dhcpSnoopRefusal{Code: http.StatusBadGateway, Msg: err.Error()}
}

// dhcpSnoopIntents turns the request body's port map into the plan's intents,
// or returns the 400 to answer with.
//
// SEPARATE FROM THE HANDLER because it is where the body is JUDGED, and the
// judgements are worth pinning without a database: an empty map, a blank port
// name, and the order the intents come out in.
//
// THE BLANK NAME IS REFUSED HERE rather than left to the connector. `{"ports":
// {"": true}}` used to travel all the way down, where each connector turned it
// into its own flavour of ApplyResult.Skipped — and the event line logged for
// the gesture read "" → trusted"". A port with no name is not a port a screen
// can have clicked, so it is a malformed request and says so at the boundary.
//
// The order is STABLE so two identical gestures produce two identical
// transcripts. It is lexical and that is enough here: every connector re-sorts
// into its own faceplate order before writing (cf. cisco/ios stdPortLess),
// because only the connector knows what the panel looks like.
//
// It returns a dhcpSnoopRefusal rather than a bare sentence so that the CODE
// travels with the reason: a malformed body is 400 wherever it is judged, and
// that is now pinned here instead of in a handler no test can enter.
func dhcpSnoopIntents(ports map[string]bool) ([]driver.DHCPSnoopPortIntent, dhcpSnoopRefusal) {
	if len(ports) == 0 {
		return nil, dhcpSnoopRefusal{Code: http.StatusBadRequest, Msg: "no ports in request"}
	}
	var out = make([]driver.DHCPSnoopPortIntent, 0, len(ports))
	var name string
	var trusted bool
	for name, trusted = range ports {
		if strings.TrimSpace(name) == "" {
			return nil, dhcpSnoopRefusal{Code: http.StatusBadRequest,
				Msg: "a port with an empty name cannot be trusted or untrusted"}
		}
		out = append(out, driver.DHCPSnoopPortIntent{Port: name, Trusted: trusted})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, dhcpSnoopRefusal{}
}

// refreshDHCPSnoopObserved re-reads snooping and persists it under
// `extra.dhcp_snooping_observed`. Best-effort by construction: a connector that
// writes without being able to read back is a legitimate staging point (cf.
// driver.DHCPSnoopReader — the two interfaces are independent), so an absent
// reader is silence, not a note. A reader that FAILS is a note, because
// somebody asked and did not get an answer.
func (s *Server) refreshDHCPSnoopObserved(ctx context.Context, deviceID string,
	adm driver.Admin, res *driver.ApplyResult) {
	var reader, canRead = driver.AsDHCPSnoopReader(adm)
	if !canRead {
		return
	}
	var fresh, readErr = reader.ReadDHCPSnooping(ctx)
	if readErr != nil {
		res.Notes = append(res.Notes,
			"DHCP snooping state not re-read after write: "+readErr.Error())
		return
	}
	var raw, marshalErr = json.Marshal(fresh)
	if marshalErr != nil {
		return
	}
	_ = s.Store.SaveDHCPSnoopObserved(ctx, deviceID, raw)
}

// dhcpSnoopApplySummary names what was asked, for the event log: "GE5 →
// trusted, GE6 → untrusted". Sorted so two identical gestures read as two
// identical lines, and spelled out in words rather than "true" / "false":
// the log is read months later by someone who will not remember which way the
// bool pointed.
func dhcpSnoopApplySummary(ports map[string]bool) string {
	var names = make([]string, 0, len(ports))
	var name string
	for name = range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	var out = ""
	for _, name = range names {
		if out != "" {
			out += ", "
		}
		out += name + " → " + trustWord(ports[name])
	}
	return out
}

func trustWord(trusted bool) string {
	if trusted {
		return "trusted"
	}
	return "untrusted"
}

// dhcpSnoopObservedOf extracts the observation stored under
// Extra.dhcp_snooping_observed.
//
// Same contract as stpObservedOf and poeObservedOf: absent, unreadable, or
// empty means nil. The emptiness test is the one that needs care here — a bare
// `{}` unmarshals into a state whose Enabled is FALSE, and showing that as
// "DHCP snooping is off on this device" would be an assertion nobody made.
// "Not observed" and "observed off" are different sentences and only one of
// them is true.
func dhcpSnoopObservedOf(d model.Device) *driver.DHCPSnoopState {
	if len(d.Extra) == 0 {
		return nil
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(d.Extra, &bag) != nil {
		return nil
	}
	var raw, ok = bag["dhcp_snooping_observed"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var st driver.DHCPSnoopState
	if json.Unmarshal(raw, &st) != nil {
		return nil
	}
	if !st.Enabled && len(st.VLANs) == 0 && len(st.Ports) == 0 && len(st.Notes) == 0 {
		return nil
	}
	return &st
}

// dhcpSnoopVLANsFor decides WHICH VLANs a snooping gesture must switch on, from
// the ports the gesture actually names.
//
// # Why not simply the site's VLANs
//
// That is what this file did until 2026-08-08, and it is wrong in both
// directions at once. Snooping a whole site because an operator trusted one
// uplink turns on inspection for every VLAN the site declares — a switch-wide
// change nobody asked for, on VLANs the clicked port has nothing to do with.
// And it is not even sufficient in the case that matters: a port carrying a VLAN
// the SITE does not declare would be trusted on a VLAN nothing inspects.
//
// The gesture an operator performs is per-PORT — an access port toward a client,
// or the uplink toward the DHCP server. So the VLANs to inspect are the VLANs
// THOSE PORTS CARRY, and nothing else.
//
// # The per-VLAN enable is not optional plumbing, it is what makes trust real
//
// Established on the lab EdgeSwitch (2026-08-08, cf.
// driver/ubiquiti/edgeswitch/dhcpsnoop.go): a port trusted while sitting outside
// every snooped VLAN does not even APPEAR in the switch's trust table. The write
// lands in the running configuration and the feature ignores it. Deriving the
// VLAN list from the port is therefore not a refinement — without it, trusting an
// access port can be a no-op that reads back as success.
//
// # Both PVID and tagged membership
//
// The untagged VLAN is the one an access port's client lives on, which is the
// common case. The tagged ones matter for the other one: an uplink trusted for
// the VLANs it carries is precisely how DHCP replies are allowed in, and leaving
// them out would trust a trunk on none of the traffic crossing it.
//
// # When it cannot be derived, fall back rather than protect nothing
//
// A port meshcraft has no intent for, or one carrying "all" VLANs, cannot be
// enumerated here. Returning an empty list would mean "the site declares no
// VLANs", which the contract defines as "turn the global feature on and inspect
// nothing" — a device left with snooping enabled and zero coverage, which is the
// silent non-protection driver/dhcpsnoop.go exists to prevent. The site list is
// the honest fallback, and the caller says so in a note.
//
// Returns the VLANs and, when it had to fall back, the sentence explaining why.
func dhcpSnoopVLANsFor(d model.Device, ports []string, siteVLANs []int) ([]int, string) {
	var intents = recon.ParsePorts(d.Ports)
	var set = map[int]bool{}
	var unknown []string
	var taggedAll []string

	var name string
	for _, name = range ports {
		var pi, known = intents[name]
		if !known {
			unknown = append(unknown, name)
			continue
		}
		if pi.TaggedAll {
			// "all" is not an enumeration, and this layer must not invent one:
			// recon.ResolveTaggedAll turns it into the site's VLANs precisely
			// because the device's interface wants a list.
			taggedAll = append(taggedAll, name)
		}
		if pi.PVID > 0 {
			set[pi.PVID] = true
		}
		var v int
		for _, v = range pi.Tagged {
			if v > 0 {
				set[v] = true
			}
		}
	}

	if len(taggedAll) > 0 || (len(set) == 0 && len(unknown) > 0) {
		// Fall back to the site, and merge rather than replace: whatever WAS
		// derivable stays, so a plan naming one known and one unknown port still
		// covers the known one exactly.
		var v int
		for _, v = range siteVLANs {
			if v > 0 {
				set[v] = true
			}
		}
	}

	var out = make([]int, 0, len(set))
	var id int
	for id = range set {
		out = append(out, id)
	}
	sort.Ints(out)

	var why string
	switch {
	case len(taggedAll) > 0:
		why = "the VLANs to inspect were taken from the whole site, not from the ports " +
			"named: " + strings.Join(taggedAll, ", ") + " carries \"all\" VLANs, which is " +
			"not a list this layer can enumerate"
	case len(set) == 0 && len(unknown) > 0:
		why = "meshcraft holds no port intent for " + strings.Join(unknown, ", ") +
			", so the VLANs to inspect were taken from the whole site instead of from " +
			"those ports"
	case len(unknown) > 0:
		why = "meshcraft holds no port intent for " + strings.Join(unknown, ", ") +
			", so nothing was added to the inspected VLANs on their account"
	}
	return out, why
}
