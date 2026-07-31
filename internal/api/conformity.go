package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// Management-plan conformity for a device on its site
// (cf. docs/01-Architecture/Adressage de management).
//
// This is a DISTINCT axis from reachability, and conflating them would be a
// display mistake: a device can respond perfectly to ping while being off-plan.
// It is not down — it needs migration. Hence a warning state (orange), not an
// error (red).
//
// Conformity is CALCULATED on read, never stored: the site plan can change at any
// moment, and a boolean frozen in the database would lie from the first plan
// change.

// MgmtConformity says whether a device is in the plan, and if not, why.
type MgmtConformity struct {
	// Checked: does the site have a plan to compare against?
	Checked  bool   `json:"checked"`
	Conforms bool   `json:"conforms"`
	Reason   string `json:"reason,omitempty"`
	// Target summarizes what the plan requires, for display without a second call.
	TargetVlan   int    `json:"target_vlan,omitempty"`
	TargetTagged bool   `json:"target_tagged,omitempty"`
	TargetSubnet string `json:"target_subnet,omitempty"`
	TargetSource string `json:"target_source,omitempty"`
	// Migratable: can migration be proposed? An unreachable device is not migrated —
	// we do not switch over what we cannot verify afterwards.
	Migratable bool `json:"migratable"`
	// PathState says whether there is, RIGHT NOW, a practicable path for the target
	// management VLAN: "ready", "absent", or "unknown".
	//
	// This is the question reachability does not ask. A device can respond perfectly
	// on its current VLAN and have no uplink carrying the target VLAN: switching
	// management into that VLAN would silence it immediately, and this product line
	// has neither SSH nor a serial port.
	PathState  string `json:"path_state,omitempty"`
	PathReason string `json:"path_reason,omitempty"`
}

// outOfBandOf says whether this device's platform keeps an OUT-OF-BAND
// administration path — serial console, MAC-winbox — through which we recover a
// device rendered silent by an addressing mistake.
//
// PURE AND DATABASE-FREE, deliberately: it is called on LISTS of two hundred
// devices (cf. withConformity), and going back through the discovery candidate
// would cost one query per row. `devices.extra.driver` is set at adoption and
// suffices.
//
// FALSE when unknown — connector missing from `extra`, or unknown to this
// binary. This is the project rule applied where it costs the most: erring this
// way shows too harsh a warning; erring the other way attempts an irreversible
// maneuver on a device we believed recoverable.
func outOfBandOf(d model.Device) bool {
	var extra map[string]string
	if json.Unmarshal(d.Extra, &extra) != nil {
		return false
	}
	var drv, known = driver.Get(extra["driver"])
	if !known {
		return false
	}
	return drv.Capabilities(d.Model).Mgmt.OutOfBand
}

// mgmtPathOf looks for an uplink that ALREADY carries the target management VLAN.
//
// The criterion is deliberately strict: a port that carries the target VLAN AND
// an active link. Both matter. A VLAN configured on a port with no cable proves
// no path, and that is precisely the mistake we seek to prevent — "the VLAN is on
// the switch" has never meant "it egresses there".
//
// Three outcomes, and indeterminate is a REAL one: a device never read back, or
// read back before meshcraft stored link state, does not allow a conclusion.
// Answering "absent" in that case would turn ignorance into a prohibition, and
// answering "ready" would cause exactly the damage we want to avoid. We say we do
// not know, and invite a read-back.
//
// `outOfBand` DOES NOT CHANGE THE VERDICT, it changes the PHRASE — and that was a
// lie to fix. This refusal claimed "this product line has neither SSH nor serial
// port: the only recourse would be a factory reset on site". True of the two
// web-only lines shipped, FALSE of RouterOS, which keeps a serial console AND
// MAC-winbox (both observed). An entire refusal was calibrated on an unrecoverable
// platform and recited verbatim to a platform recoverable with a cable. A message
// that exaggerates severity gets learned and ignored, and the day it is true,
// nobody listens anymore.
func mgmtPathOf(d model.Device, plan model.MgmtPlan, outOfBand bool) (string, string) {
	if plan.VlanID <= 0 {
		return "", ""
	}
	var target = itoa(plan.VlanID)
	if d.ObservedAt == nil || len(d.Observed) == 0 {
		return "unknown", "this device has never been read back: meshcraft cannot " +
			"say whether an uplink carries VLAN " + target + ". Read it back before switching."
	}
	var obs observedState
	if json.Unmarshal(d.Observed, &obs) != nil {
		return "unknown", "observed state unreadable: read the device back before switching."
	}
	if obs.LinkUp == nil {
		return "unknown", "the last read predates link-state verification: " +
			"read the device back to learn whether an uplink carries VLAN " + target + "."
	}

	// Tagged or native: the plan says which, and the two are not looked up in the
	// same place. A "native" plan expects the VLAN as PVID; a "tagged" plan expects
	// it in the port's tagged VLAN list.
	var name string
	var p driver.PortState
	for name, p = range obs.Ports {
		if !obs.LinkUp[name] {
			continue
		}
		if plan.Untagged {
			if p.PVID == plan.VlanID {
				return "ready", "port " + name + " has an active link and VLAN " +
					target + " as native."
			}
			continue
		}
		var v int
		for _, v = range p.Tagged {
			if v == plan.VlanID {
				return "ready", "port " + name + " has an active link and VLAN " +
					target + " tagged."
			}
		}
	}

	var how = "tagged"
	if plan.Untagged {
		how = "as native"
	}
	return "absent", "NO port with an active link carries VLAN " + target + " " +
		how + ". Switching management into this VLAN now would make the device " +
		"unreachable over the network, " + recoveryClause(outOfBand) +
		" Configure the management uplink first, then read the device back."
}

// mgmtVLANPathOf checks whether ANY up port already carries vlanID — as access
// native (PVID) OR as tagged trunk membership.
//
// Used when the operator picks a management VLAN in the UI (not only the site
// plan). Plan conformity still uses [mgmtPathOf] with the plan's tagged/native
// preference; this check is the union the operator asked for before a VLAN move.
func mgmtVLANPathOf(d model.Device, vlanID int, outOfBand bool) (string, string) {
	if vlanID <= 0 {
		return "", ""
	}
	var target = itoa(vlanID)
	if d.ObservedAt == nil || len(d.Observed) == 0 {
		return "unknown", "this device has never been read back: meshcraft cannot " +
			"say whether an uplink carries VLAN " + target + ". Read it back before switching."
	}
	var obs observedState
	if json.Unmarshal(d.Observed, &obs) != nil {
		return "unknown", "observed state unreadable: read the device back before switching."
	}
	if obs.LinkUp == nil {
		return "unknown", "the last read predates link-state verification: " +
			"read the device back to learn whether an uplink carries VLAN " + target + "."
	}

	var name string
	var p driver.PortState
	for name, p = range obs.Ports {
		if !obs.LinkUp[name] {
			continue
		}
		if p.PVID == vlanID {
			return "ready", "port " + name + " has an active link and VLAN " +
				target + " as native (access)."
		}
		var v int
		for _, v = range p.Tagged {
			if v == vlanID {
				return "ready", "port " + name + " has an active link and VLAN " +
					target + " tagged (trunk)."
			}
		}
	}

	return "absent", "NO port with an active link carries VLAN " + target +
		" as access (native) or trunk (tagged). Switching management into this VLAN " +
		"now would make the device unreachable over the network, " +
		recoveryClause(outOfBand) +
		" Put VLAN " + target + " on an up trunk or access port first, then read the device back."
}

// resolveMgmtTargetVLAN picks the VLAN a migrate/jump should target.
// Body vlan_id wins when > 0; otherwise the site plan; otherwise error text.
func (s *Server) resolveMgmtTargetVLAN(ctx context.Context, d model.Device,
	bodyVLAN int) (int, model.MgmtPlan, string) {
	var plan, err = s.Store.GetMgmtPlan(ctx, d.SiteID)
	if err != nil {
		return 0, plan, err.Error()
	}
	if bodyVLAN > 0 {
		return bodyVLAN, plan, ""
	}
	if plan.Configured && plan.VlanID > 0 {
		return plan.VlanID, plan, ""
	}
	return 0, plan, "no management VLAN selected and this site has no usable management plan"
}

// recoveryClause says WHAT IT COSTS to get it wrong, on this platform.
//
// Separate and pure because this is vocabulary, and vocabulary is half the
// safeguard here: the two phrases do not lead to the same action. On a platform
// with no fallback path, repair is a move WITH loss of all configuration; with a
// fallback path, it is a move — or a colleague on site with a cable — and
// configuration stays intact. Presenting them the same would either give up
// needlessly or attempt blindly.
func recoveryClause(outOfBand bool) string {
	if outOfBand {
		return "and you would then recover it through its OUT OF BAND fallback " +
			"(serial console, MAC-winbox depending on platform): the mistake is " +
			"recoverable on site without wiping configuration, which makes it less " +
			"costly — not acceptable."
	}
	return "and meshcraft knows NO out-of-band fallback on this platform — no SSH, " +
		"no Telnet, no serial port: the only recourse would be a factory reset at the " +
		"button, on site, with loss of all configuration."
}

// conformityOf compares a device to its site's plan.
func (s *Server) conformityOf(ctx context.Context, d model.Device) MgmtConformity {
	var plan, err = s.Store.GetMgmtPlan(ctx, d.SiteID)
	if err != nil {
		return MgmtConformity{}
	}
	return conformityWith(d, plan)
}

// conformityWith is the decision itself, without database access: the plan is the
// same for an entire site and is read once, not once per device.
func conformityWith(d model.Device, plan model.MgmtPlan) MgmtConformity {
	var c MgmtConformity
	if !plan.Configured {
		// No plan: nothing to compare, therefore nothing to fault.
		return c
	}
	c.Checked = true
	// Management VLAN path does not depend on subnet or current address: it is
	// computed as soon as a plan exists. It also applies to a device ALREADY
	// conforming — an uplink can unplug later, and the question "where does
	// management egress?" remains relevant.
	c.PathState, c.PathReason = mgmtPathOf(d, plan, outOfBandOf(d))
	c.TargetVlan, c.TargetTagged = plan.VlanID, !plan.Untagged
	c.TargetSubnet, c.TargetSource = plan.SubnetV4, plan.Source
	c.Conforms = true

	if plan.SubnetV4 == "" {
		// A plan without subnet says nothing about the address: we cannot judge,
		// and we do not pretend otherwise.
		c.Checked = false
		return c
	}
	var prefix, prefixErr = netip.ParsePrefix(plan.SubnetV4)
	if prefixErr != nil {
		c.Checked = false
		return c
	}
	var addr, addrErr = netip.ParseAddr(d.MgmtIP)
	if addrErr != nil {
		c.Conforms = false
		c.Reason = "no known administration address"
		return c
	}
	if !prefix.Contains(addr) {
		c.Conforms = false
		c.Reason = "address " + d.MgmtIP + " outside the site's management subnet (" +
			plan.SubnetV4 + ")"
		// Migration requires being able to verify afterwards: the device must
		// respond now, AND the target VLAN must have a path out.
		//
		// An INDETERMINATE path does not block: that would turn ignorance into a
		// prohibition on a fleet where many devices have not yet been read back. An
		// ABSENT path, however, is certainty — and that one costs an on-site visit.
		c.Migratable = d.Status == "ok" && c.PathState != "absent"
		return c
	}
	// The address is in the plan. Management VLAN is not yet readable without
	// connecting to the device: we do not claim it conforming, we say it is not
	// verified.
	c.Reason = "management VLAN not verified (requires a device read-back)"
	return c
}

// handleSetDeviceAddressDomain declares the network in which this device's
// address makes sense.
//
// Reserved to `site_admin`: the domain decides which proxy is legitimate to
// monitor this address, so getting it wrong cuts supervision — not the same order
// as labeling a port.
//
// Empty string accepted: it removes the declaration. A wrong domain is worse than
// no domain at all, so we must be able to revert.
func (s *Server) handleSetDeviceAddressDomain(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		AddressDomainID string `json:"address_domain_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.AddressDomainID != "" {
		// An unknown ID would land in the database as an unreadable foreign-key
		// violation: refuse with a message that makes sense.
		var _, domErr = s.Store.GetAddressDomain(r.Context(), in.AddressDomainID)
		if domErr != nil {
			respondErr(w, http.StatusUnprocessableEntity, "unknown address domain")
			return
		}
	}
	var fresh, err = s.Store.SetDeviceAddressDomain(r.Context(), d.ID, in.AddressDomainID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitDeviceEvent(r, d, "device.address_domain", model.SevNotice,
		domainChangeMessage(d.Name, in.AddressDomainID))
	respond(w, http.StatusOK, s.withSync(r.Context(), fresh))
}

func domainChangeMessage(deviceName, domainID string) string {
	if domainID == "" {
		return "Address domain removed from \"" + deviceName + "\"."
	}
	return "Address domain declared on \"" + deviceName + "\"."
}

// deviceView is a device enriched with what the UI needs without a second call.
type deviceView struct {
	model.Device
	Mgmt MgmtConformity `json:"mgmt"`
	// MgmtAddressing: the EFFECTIVE addressing mode and where it comes from (migration 0027).
	//
	// Separate from `Mgmt`, which is plan conformity, because these are two distinct
	// axes and merging them would make them inseparable in the UI: a device on a
	// static address in the right subnet is perfectly CONFORMING to the plan — that
	// is not a drift, it is a mode. Conflating them would show an orange warning on
	// the three MikroTik devices in the reference installation, which have nothing to
	// fault.
	MgmtAddressing MgmtAddressingView `json:"mgmt_addressing"`
	// STP: last spanning-tree observation (Extra.stp_observed), for topology view.
	STP *driver.SpanningTreeState `json:"stp,omitempty"`
}

// withConformity enriches a device list with conformity AND sync state.
//
// The ORDER of the two is binding, which is why they are done here rather than by
// the caller: conformity needs observed state — it says which port carries a link —
// while sync enrichment REMOVES it, to avoid loading a list of two hundred devices.
// Chaining them the other way breaks nothing visible: the management VLAN path
// simply becomes "indeterminate" everywhere, silently.
func (s *Server) withConformity(ctx context.Context, devices []model.Device) []deviceView {
	var out = make([]deviceView, 0, len(devices))
	// Plan and VLANs are the same for an entire site: do not re-read them per device.
	var plans = map[string]model.MgmtPlan{}
	var vlans = map[string][]int{}
	var d model.Device
	for _, d = range devices {
		var plan, seen = plans[d.SiteID]
		if !seen {
			plan, _ = s.Store.GetMgmtPlan(ctx, d.SiteID)
			plans[d.SiteID] = plan
			vlans[d.SiteID] = s.siteVLANIDs(ctx, d.SiteID)
		}
		var c = conformityWith(d, plan)
		var enriched = withSyncVLANs(d, vlans[d.SiteID])
		// Why no proxy watches this device, if there is a reason.
		// Computed here rather than in withSyncVLANs, which is pure and has no access
		// to proxy state.
		enriched.WatchExcluded = s.WatchExcl.reasonFor(d.ID)
		// Addressing is computed on `d`, not on `enriched`: withSyncVLANs REMOVES
		// observed state to avoid loading a list of two hundred devices, and the
		// "observed and kept" mode comes from observed state.
		// Order already cost a defect on the management VLAN path, just above.
		out = append(out, deviceView{
			Device: enriched, Mgmt: c,
			MgmtAddressing: addressingViewOf(d, plan),
			STP:            stpObservedOf(d),
		})
	}
	return out
}

// stpObservedOf extracts spanning-tree observation stored under Extra.stp_observed.
func stpObservedOf(d model.Device) *driver.SpanningTreeState {
	if len(d.Extra) == 0 {
		return nil
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(d.Extra, &bag) != nil {
		return nil
	}
	var raw, ok = bag["stp_observed"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var st driver.SpanningTreeState
	if json.Unmarshal(raw, &st) != nil {
		return nil
	}
	// Empty blob after unmarshal: never treat a bare {} as "STP off".
	if !st.Enabled && st.Mode == "" && len(st.Ports) == 0 && len(st.Notes) == 0 {
		return nil
	}
	return &st
}
