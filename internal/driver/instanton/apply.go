package instanton

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Port configuration writes.
//
// This is the "Apply" step of the pipeline (docs/en/01-Architecture/Reconciliation.md §4 and
// §5), and the most dangerous step in the whole connector: this product line has no
// SSH, Telnet, or serial port. A write that removes the uplink port from its VLAN
// makes the device unreachable, and the only recovery is a paperclip in the reset
// button, on site.
//
// Hence the central rule in this file: **never touch the management VLAN**. This is
// not a temporary limitation we will lift for convenience; it is the "never cut off
// your own hand" rule translated into the only place it can be guaranteed — the code
// that writes.

// membership is ONE VLAN's membership as the device stores it.
//
// The device reasons by VLAN ("who is a member?"), meshcraft by port
// ("what does this port carry?"). Writing therefore requires the inverse pivot of
// reading.
type membership struct {
	VLAN     int
	Tagged   []string
	Untagged []string
}

// MembershipFor translates per-PORT configuration into per-VLAN membership.
//
// Pure function, by design: it decides which ports stay in which VLAN. A mistake
// here only shows when a switch goes silent.
func MembershipFor(desired map[string]PortState) []membership {
	var byVLAN = map[int]*membership{}
	var at = func(id int) *membership {
		var m, seen = byVLAN[id]
		if !seen {
			m = &membership{VLAN: id}
			byVLAN[id] = m
		}
		return m
	}

	var names = make([]string, 0, len(desired))
	var name string
	for name = range desired {
		names = append(names, name)
	}
	// Stable order: two runs must produce exactly the same request, otherwise
	// comparing two transcripts means nothing.
	sort.Strings(names)

	for _, name = range names {
		var p = desired[name]
		if p.Mode == "" || p.Mode == "libre" {
			continue // "absent = unmanaged": meshcraft does not claim this port
		}
		if p.PVID > 0 {
			var m = at(p.PVID)
			m.Untagged = append(m.Untagged, name)
		}
		var id int
		for _, id = range p.Tagged {
			// A VLAN both native and tagged on the same port makes no sense:
			// native wins, as the device UI does.
			if id == p.PVID {
				continue
			}
			var m = at(id)
			m.Tagged = append(m.Tagged, name)
		}
	}

	var out = make([]membership, 0, len(byVLAN))
	var id int
	for id = range byVLAN {
		out = append(out, *byVLAN[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VLAN < out[j].VLAN })
	return out
}

// portNumbers strips the prefix to yield device numbering.
// A port whose name does not follow the convention is IGNORED rather than guessed:
// sending a wrong port number is a write to the wrong place.
func portNumbers(names []string, prefix string) []string {
	var out []string
	var n string
	for _, n = range names {
		var num = strings.TrimPrefix(n, prefix)
		if num == n || num == "" {
			continue
		}
		var _, err = strconv.Atoi(num)
		if err != nil {
			continue
		}
		out = append(out, num)
	}
	return out
}

// MANAGEMENT addressing switch — the most dangerous operation in the product,
// since it moves access under the session that commands it.
//
// Three observations on a real 1930 govern what follows:
//
//	IPv4InterfaceList              → one entry per IP interface. VLAN1 carries
//	                                 "owner 2" and `DHCPclientIdSupported`,
//	                                 i.e. a DHCP CLIENT; the default interface on
//	                                 169.254 carries "owner 3".
//	IPv4GlobalSetting              → managementInterfaceName = "VLAN1"
//	ManagementInterfaceGlobalSetting → managementVLAN = 1
//
// The good news from the observation: the device accepts MULTIPLE IPv4 interfaces.
// The switch is therefore not a blind replacement but a two-step maneuver — add,
// verify, then only switch and clean up. While the old interface remains, current
// access stays open.
//
// And the safety net: none of this is written to flash without an explicit
// `SaveConfig`. A failed switch is undone by power loss, which is exactly what
// makes the operation testable.

// ownerDHCP is the `owner` field value for a DHCP client interface.
//
// Deduced from observation rather than documentation, which does not exist for this
// interface: VLAN1, which carries an address clearly obtained by lease, is 2 — the
// default interface at 169.254.0.1, configured by nobody, is 3.
const ownerDHCP = 2

// vlanIfName renders the interface name as the device names it: "VLAN99".
func vlanIfName(vlanID int) string { return "VLAN" + strconv.Itoa(vlanID) }

// AddManagementInterface ADDS an IPv4 interface as a DHCP client on a VLAN.
//
// Removes nothing, deliberately. The device keeps its current address and stays
// reachable throughout: that is what lets us confirm the new address responds BEFORE
// cutting the old one. Doing the opposite would be jumping without looking.
// The field set is MINIMAL, and the device decided that.
//
// The temptation was to copy the entry from a working interface, observed on hardware.
// It is refused: "code 3, wrong field for dhcp address". A DHCP entry does not
// accept static entry fields — not `status`, not `adminStatus`, not broadcast options.
// The opposite of this product line's usual rule, where partial sections are the problem.
//
// On read, an interface created this way carries `status 6` until a lease is granted,
// versus `status 1` for an addressed interface. That is NOT a configuration defect:
// the device says it is still waiting for a DHCP reply. Not confusing the two avoided
// "fixing" a write that was already correct.
func (a *Admin) AddManagementInterface(ctx context.Context, vlanID int) error {
	var name = vlanIfName(vlanID)
	return a.writeSection(ctx, "IPv4InterfaceList", "set",
		"<ifEntry><interfaceName>"+name+"</interfaceName>"+
			"<owner>"+strconv.Itoa(ownerDHCP)+"</owner>"+
			"<DHCPclientIdSupported>1</DHCPclientIdSupported></ifEntry>",
		fmt.Sprintf("interface %s added as DHCP client", name))
}

// ifStatusAddressed is the `status` field value for an interface that actually
// carries an address. A DHCP interface without a lease is 6.
const ifStatusAddressed = "1"

// InterfaceHasAddress reports whether the target VLAN interface HAS OBTAINED its address.
//
// This is the green light for the second half of the switch: while red, moving
// administration to this VLAN would cut access with no replacement.
func (a *Admin) InterfaceHasAddress(ctx context.Context, vlanID int) (bool, string, error) {
	var body, err = a.RawSection(ctx, "IPv4InterfaceList")
	if err != nil {
		return false, "", err
	}
	var want = vlanIfName(vlanID)
	var block string
	for _, block = range reIfEntry.FindAllString(body, -1) {
		if xmlField(block, "interfaceName") != want {
			continue
		}
		var addr = xmlField(block, "IPAddr")
		return xmlField(block, "status") == ifStatusAddressed, addr, nil
	}
	return false, "", nil
}

var reIfEntry = regexp.MustCompile(`(?is)<ifEntry>(.*?)</ifEntry>`)

// RemoveManagementInterface removes a VLAN's IPv4 interface.
//
// Required just BEFORE the switch, and not an extra annoyance: it is the device
// model. It refuses to designate a management VLAN that already carries IP
// configuration — "Please remove IPv4 configuration on new management VLAN" —
// because IT installs the management address there and does not want another on its path.
//
// `owner` accompanies removal, not just addition: without it, the device replies
// "Invalid owner value". An entry in this table is identified by its interface AND
// who owns it.
func (a *Admin) RemoveManagementInterface(ctx context.Context, vlanID int) error {
	var name = vlanIfName(vlanID)
	return a.writeSection(ctx, "IPv4InterfaceList", "delete",
		"<ifEntry><interfaceName>"+name+"</interfaceName>"+
			"<owner>"+strconv.Itoa(ownerDHCP)+"</owner></ifEntry>",
		fmt.Sprintf("interface %s removed", name))
}

// SetManagementVLAN designates the VLAN through which the device is administered.
//
// Call ONLY AFTER confirming the target VLAN interface has obtained an address: this
// call moves administration, and therefore cuts the current session if run too early.
//
// `managementPort` is sent back unchanged: observation gives 0, and this interface
// rejects partial sections far more often than it accepts them.
func (a *Admin) SetManagementVLAN(ctx context.Context, vlanID int) error {
	return a.writeSection(ctx, "ManagementInterfaceGlobalSetting", "set",
		"<managementVLAN>"+strconv.Itoa(vlanID)+"</managementVLAN>"+
			"<managementPort>0</managementPort>",
		"management VLAN set to "+strconv.Itoa(vlanID))
}

// `ApplyResult` is now the contract type (`driver.ApplyResult`), by ALIAS: cf.
// contract.go. Its three fields are what this package carried, JSON tags included —
// they go in the HTTP response the frontend displays.
//
// It gains a fourth, `Backup`, which this product line leaves EMPTY. That is not a
// gap: it exists for SwOS, which applies and persists in one gesture, where the
// configuration copy taken before the write is the only rollback. Here the safety net
// is elsewhere, and better — until `SaveConfig` has run, nothing is in flash and a
// power loss undoes everything.

// missing returns elements of `want` absent from `have`. This turns two states into a
// DELTA, the only form the device accepts.
func missing(want, have []int) []int {
	var seen = map[int]bool{}
	var v int
	for _, v = range have {
		seen[v] = true
	}
	var out []int
	for _, v = range want {
		if !seen[v] {
			out = append(out, v)
		}
	}
	return out
}

// vlanCSV renders a VLAN list as the device expects.
func vlanCSV(ids []int) string {
	var parts = make([]string, 0, len(ids))
	var v int
	for _, v = range ids {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ",")
}

// ApplyPorts applies port configuration on the device.
//
// Writes are PER INTERFACE, via `VLANInterfaceISList`, not per VLAN.
// The first version wrote `VLANInterfaceMembershipTable`: the device replied
// `statusCode 0` without changing anything — this interface's characteristic silent
// failure, caught only because apply re-reads and compares.
// It is also the model closest to meshcraft, which reasons by port.
//
// Order follows §4: create VLANs BEFORE assigning ports, write ports, then save to flash.
//
// GUARDRAIL, at the PORT not the VLAN: a port that is BOTH an untagged member of a
// management VLAN AND carrying a link never has its native VLAN changed. Current
// session traffic very likely goes through it, and this product line has no SSH,
// Telnet, or serial — the mistake is fixed with a paperclip, on site. ADDING tagged
// VLANs remains allowed: that does not touch untagged traffic.
//
// `force` lifts this guardrail. It is never enabled by default, nor by automatic
// apply: an explicit operator gesture, who knows their topology better than meshcraft
// and accepts the risk of having to travel on site.
//
// The LINK condition is what makes the guardrail usable. On a factory-fresh device,
// all 48 ports are untagged in VLAN 1, which is also administration: without it, no
// port could ever be modified and "adopt" would mean nothing. A port with no cable
// cannot carry any session.
//
// Two VLANs count as management: the one the site plan wants, and the one the DEVICE
// declares. They differ until a device has been migrated — observed on a 1930: plan
// at 99, device at 1. Protecting only the first would leave the session VLAN writable.
//
// THE FORMER FIVE PARAMETERS ARE NOW ONE [PortPlan], from the contract, and the port
// prefix now comes from the session (cf. Admin.prefix). Nothing this function does
// has changed: the same guardrails, in the same order, on the same sections. The
// aggregate exists so that when apply needs one more constraint, all three connectors
// change together or not at all — a parameter added in three places gets forgotten at
// the third, and this platform's apply would write something other than what the screen
// announces.
func (a *Admin) ApplyPorts(ctx context.Context, plan PortPlan) (ApplyResult, error) {
	var out ApplyResult
	var prefix = a.portPrefix()
	var desired = driver.ExpandAllTrunks(plan.Desired, plan.SiteVLANs)
	var planVLAN = plan.MgmtVLAN
	var force = plan.Force

	// Never diff against cached state (§2).
	var current, err = a.ReadState(ctx)
	if err != nil {
		return out, err
	}
	var protected = map[int]string{}
	if planVLAN > 0 {
		protected[planVLAN] = "site plan management VLAN"
	}
	if current.MgmtVLAN > 0 {
		protected[current.MgmtVLAN] = "device-declared management VLAN"
	}
	if len(protected) == 0 {
		return out, fmt.Errorf("management VLAN unknown from plan and device: write refused, " +
			"meshcraft cannot guarantee it will not cut off access")
	}
	var exists = map[int]bool{}
	var v VLAN
	for _, v = range current.VLANs {
		exists[v.ID] = true
	}

	// 1. Create missing VLANs. Assigning a port to a nonexistent VLAN is refused by
	//    the device, and the reverse order would leave half the plan applied.
	//
	//    The SITE VLAN plan is applied in full, not only VLANs a port uses: an adopted
	//    device must know its site's VLANs, otherwise assigning a port from the device
	//    UI — or plugging a trunk — would offer nothing. That is what we expect from an
	//    "adopted" device rather than "merely reachable".
	//
	//    "set" not "add": the dispatcher looks for a function named
	//    `<action>_<Section>`, and `add_VLANList` does not exist — it says so plainly
	//    ("function name not found"). Its interface creates a VLAN with `set`.
	var todo = append([]VLAN{}, plan.SiteVLANs...)
	var m membership
	for _, m = range MembershipFor(desired) {
		todo = append(todo, VLAN{ID: m.VLAN})
	}
	var want VLAN
	for _, want = range todo {
		// VLAN 1 always exists, and reserved bounds are not created.
		if want.ID <= 1 || want.ID > 4092 || exists[want.ID] {
			continue
		}
		var label = want.Name
		if label == "" {
			label = "vlan" + strconv.Itoa(want.ID)
		}
		if err = a.writeSection(ctx, "VLANList", "set",
			"<VLAN><VLANID>"+strconv.Itoa(want.ID)+"</VLANID>"+
				"<VLANName>"+xmlEscape(label)+"</VLANName></VLAN>",
			fmt.Sprintf("VLAN %d %q created", want.ID, label)); err != nil {
			return out, err
		}
		exists[want.ID] = true
		out.VLANsCreated = append(out.VLANsCreated, want.ID)
	}

	// 2. Write ports, one by one.
	var names = make([]string, 0, len(desired))
	var name string
	for name = range desired {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name = range names {
		var want = desired[name]
		if want.Mode == "" || want.Mode == "libre" {
			continue // "absent = unmanaged"
		}
		var num = strings.TrimPrefix(name, prefix)
		if num == name || num == "" {
			continue
		}
		var _, convErr = strconv.Atoi(num)
		if convErr != nil {
			continue
		}

		// Does the port currently carry administration untagged?
		var now = current.Ports[name]
		var why = protected[now.PVID]
		if why != "" && want.PVID != now.PVID && current.HasLink(name) && !force {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: it has an ACTIVE link and is an untagged member of "+
					"VLAN %d (%s) — changing its native VLAN would very likely cut "+
					"access. This product line has no out-of-band console.", name, now.PVID, why))
			continue
		}

		// The device expects a DELTA, not a full state: `set` ADDS VLANs, `delete`
		// removes them. Sending the full list yields "wrong Vlans List have specified"
		// — that is how its interface works (`postFormat`), and there is no other path.
		var toAdd = missing(want.Tagged, now.Tagged)
		var toDrop = missing(now.Tagged, want.Tagged)
		// Unresolved "all" (`TaggedAtLeast`, empty site plan) must not strip
		// tagged membership. A resolved "all" carries the exact site VLAN list
		// in Tagged — extras are removed like any explicit trunk.
		if want.TaggedAtLeast {
			toDrop = nil
		}
		var changed bool

		// SET BEFORE DELETE, and this order is load-bearing, not cosmetic.
		//
		// CAPTURED 2026-08-04 on sw-labo-1930 ("device refused change (code 3
		// port 7 MUST belong to at least one vlan)"): GE7 was a TAGGED-ONLY
		// member of VLAN 99 with no native VLAN of its own (a legal state on
		// this range), moving to native access VLAN 100 with no tagged
		// membership at all. The two writes below used to go DELETE then SET
		// — dropping the port's only membership (VLAN 99, tagged) before its
		// replacement (VLAN 100, native) had been written. The device
		// validates each POST synchronously, not the eventual end state: the
		// DELETE alone would have left port 7 belonging to nothing, and it
		// refused right there, before the SET that would have fixed it ever
		// went out. Sending SET first means the port only ever GAINS
		// membership before anything is taken away — it can never be
		// observed at zero.
		//
		// Native VLAN is sent ONLY when it changes: reaffirming the same
		// value is refused.
		var entry string
		if want.PVID != now.PVID {
			entry += "<generalPVID>" + strconv.Itoa(want.PVID) + "</generalPVID>" +
				"<generalUntaggedVLANs>" + strconv.Itoa(want.PVID) + "</generalUntaggedVLANs>"
		}
		if len(toAdd) > 0 {
			entry += "<generalTaggedVLANs>" + vlanCSV(toAdd) + "</generalTaggedVLANs>"
		}
		if entry != "" {
			if err = a.writeSection(ctx, "VLANInterfaceISList", "set",
				"<Entry><interfaceName>"+num+"</interfaceName>"+entry+"</Entry>",
				fmt.Sprintf("%s: native %d, tagged VLANs added [%s]", name, want.PVID,
					vlanCSV(toAdd))); err != nil {
				return out, err
			}
			changed = true
		}
		if len(toDrop) > 0 {
			if err = a.writeSection(ctx, "VLANInterfaceISList", "delete",
				"<Entry><interfaceName>"+num+"</interfaceName>"+
					"<generalTaggedVLANs>"+vlanCSV(toDrop)+"</generalTaggedVLANs></Entry>",
				fmt.Sprintf("%s: tagged VLANs removed [%s]", name, vlanCSV(toDrop))); err != nil {
				return out, err
			}
			changed = true
		}
		if !changed {
			continue // already compliant: sending nothing beats a round trip
		}
		out.PortsWritten = append(out.PortsWritten, name)
	}

	// 3. Make durable. Without this step, everything disappears on reboot — and an
	//    apply undone by reboot is worse than a refused apply, because it was reported
	//    successful.
	a.SaveConfig(ctx) //nolint:errcheck // recorded in transcript
	return out, nil
}
