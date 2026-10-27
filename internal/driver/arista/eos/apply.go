package eos

// ApplyPorts — VLAN membership of the ports, the most dangerous operation in the
// product. The guardrails are the Cisco connector's, because the two CLIs share
// the one behaviour that makes those guardrails necessary; what is NOT shared is
// the fallback machinery, and the absence is worth stating.
//
// # 1. An absent allowed-vlan line means ALL, not none
//
// On EOS a trunk with no "switchport trunk allowed vlan" carries every VLAN.
// Reading that as an empty tagged list is not a rounding error, it inverts the
// meaning: an "add" would change nothing (everything was already allowed), the
// re-read would still show no line, and the port would report drift for ever
// while every apply issued a no-op. So an exact intent on such a port is written
// with the SET form, which is the only one that actually restricts, and the
// native VLAN is always included in it — EOS drops native traffic on a trunk
// whose allowed list does not contain the native VLAN.
//
// # 2. Restricting a trunk can cut the session even when its native VLAN is safe
//
// The classic guardrail protects the port whose NATIVE VLAN is the management
// one. Administration often arrives TAGGED on an uplink whose native VLAN is 1 —
// so that check sees nothing, and the exact-set write above would quietly remove
// the management VLAN from the only port carrying it. Hence
// [protectedTaggedLoss]: an active port never loses a protected VLAN from its
// allowed list without Force.
//
// # 3. NO ORDERED CANDIDATES, and that is a fact about EOS rather than a shortcut
//
// cisco/ios runs its trunk block as two alternatives because a 2960-X and a 3750
// disagree about `switchport trunk encapsulation dot1q` — one refuses the line,
// the other refuses the mode without it. EOS has no such command: the platform
// speaks 802.1Q and only 802.1Q, there is no ISL to choose against, and there is
// therefore exactly ONE form for a trunk. A fallback here would be a second form
// nobody could name, tried after a refusal that would mean something else
// entirely — so a refusal is reported as a refusal, which is the honest answer on
// a platform with one syntax.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// ApplyPorts writes VLAN membership on the ports, then PERSISTS (EOS
// distinguishes running from startup; a write a reboot would undo is worse than a
// refused write).
func (c *Client) ApplyPorts(ctx context.Context, plan PortPlan) (ApplyResult, error) {
	var out = ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	var desired = driver.ExpandAllTrunks(plan.Desired, plan.SiteVLANs)

	// A write NEVER re-reads a cache: the device comes first. The running-config
	// read here also serves as the safety copy returned to the caller.
	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("pre-write read failed: %v", err)
	}
	out.Backup = runOut
	var rc = parseRunningConfig(runOut, c.host)

	// LLDP, once, and ONLY if somebody turned it off.
	//
	// The opposite of the Cisco connector's handling, and for the opposite reason:
	// IOS ships with LLDP off, so that connector turns it on for a whole fleet
	// that never had it. EOS ships with it ON, so the only switches missing it are
	// ones where a person wrote "no lldp run" deliberately. Re-enabling it
	// silently on every apply would undo that person's decision without asking; it
	// is REPORTED instead (cf. configFacts), and turning it back on is the
	// provisioning push's job, where the operator has asked for a device to be
	// brought to the fleet standard.
	if !rc.LLDPRun {
		out.Notes = append(out.Notes,
			"LLDP is turned off on this switch and this apply did NOT turn it back on: on "+
				"EOS that line is a deliberate change somebody made, unlike on IOS where its "+
				"absence is the factory state. Re-provisioning the device sets it")
	}

	var vlanOut string
	vlanOut, err = c.cli.run(ctx, "show vlan", cmdTimeout)
	if err != nil && vlanOut == "" {
		return out, fmt.Errorf("VLAN database read failed: %v — meshcraft cannot tell a "+
			"missing VLAN from an existing one, so nothing is written", err)
	}
	var haveVLANs = parseVlanTable(vlanOut)

	var statusOut string
	statusOut, err = c.cli.run(ctx, "show interfaces status", cmdTimeout)
	if err != nil && statusOut == "" {
		return out, fmt.Errorf("link read failed: %v — the guardrails cannot tell an active "+
			"port from a dead one, so nothing is written", err)
	}
	var current = stateFromReads(rc, haveVLANs, parseInterfacesStatus(statusOut))

	// Guardrails: the protected VLANs, the plan's AND the device's own.
	var protected = map[int]string{}
	if plan.MgmtVLAN > 0 {
		protected[plan.MgmtVLAN] = "site plan management VLAN"
	}
	if current.MgmtVLAN > 0 {
		protected[current.MgmtVLAN] = "management VLAN declared by the device"
	}
	if len(protected) == 0 {
		return out, fmt.Errorf("unknown management VLAN, from plan or device: write refused, " +
			"meshcraft cannot guarantee not cutting access")
	}
	if plan.MgmtVLAN > 0 && current.MgmtVLAN > 0 && plan.MgmtVLAN != current.MgmtVLAN {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"site plan says management VLAN %d but the device administers itself through "+
				"VLAN %d: both stay protected", plan.MgmtVLAN, current.MgmtVLAN))
	}

	// 1. Missing VLANs first — a port cannot be placed in a VLAN that does not
	//    exist, and the interface command is accepted anyway, which would leave
	//    the port assigned to a VLAN nothing forwards.
	var missing = missingVLANs(current.VLANs, plan.SiteVLANs, plan.MgmtVLAN, desired)
	if len(missing) > 0 {
		var cerr = c.runConfigBlock(ctx, vlanCreateCmds(missing))
		if cerr != nil {
			return out, fmt.Errorf("VLAN creation: %v", cerr)
		}
		var v VLAN
		for _, v = range missing {
			out.VLANsCreated = append(out.VLANsCreated, v.ID)
		}
		c.step(true, fmt.Sprintf("%d VLAN(s) created", len(missing)))
	}

	// 2. Ports, one at a time.
	var names []string
	var name string
	for name = range desired {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return stdPortLess(names[i], names[j]) })

	for _, name = range names {
		var want = desired[name]
		if want.Mode == "" || want.Mode == "libre" {
			continue // absent or "libre" = unmanaged, leave it alone
		}
		var now, known = current.Ports[name]
		if !known {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: the device does not report this port", name))
			continue
		}
		var allowedAll = rc.AllowedAll[name]
		var linkUp = current.HasLink(name)

		var why = protected[now.PVID]
		if why != "" && want.PVID != now.PVID && linkUp && !plan.Force {
			// A spared port is spared IN FULL: neither PVID nor lists.
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: it carries an ACTIVE link and its native VLAN is %d (%s) — "+
					"changing it would very likely cut access. Use force only knowing why",
				name, now.PVID, why))
			continue
		}
		var lost, lostWhy = protectedTaggedLoss(want, now, allowedAll, protected)
		if lost > 0 && linkUp && !plan.Force {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: it carries an ACTIVE link and the intent would remove VLAN "+
					"%d (%s) from its allowed list. Administration often arrives TAGGED on the "+
					"uplink, so this is exactly how a switch is lost. Use force only knowing why",
				name, lost, lostWhy))
			continue
		}
		if portConverged(want, now, allowedAll) {
			continue
		}
		if cliPortName(name) == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: this name is outside the standard-model mapping this "+
					"connector translates (a breakout lane, or the out-of-band management "+
					"port) — nothing was sent to the device", name))
			continue
		}

		var cerr = c.runConfigBlock(ctx, portCmds(name, want, now, allowedAll))
		if cerr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not converged: the CLI refused the command (%s) — this platform has ONE "+
					"trunk syntax, so there is no second form to try. Command set written "+
					"against EOS %s from documentation; survey this device's forms",
				name, firstLine(cerr.Error()), DocumentedRelease))
			continue
		}
		c.step(true, name+": "+want.Mode)
		out.PortsWritten = append(out.PortsWritten, name)
	}

	// 3. Persistence — the contract requires it of the write itself.
	var perr = c.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes,
			"applied but NOT persisted: "+firstLine(perr.Error())+
				" — a reboot would undo this configuration")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", SaveCmd)
	return out, nil
}

// stateFromReads assembles the current state from the three reads.
func stateFromReads(rc runConfig, vlans []VLAN, status []statusPort) State {
	var st = State{
		VLANs:    vlans,
		Ports:    rc.Ports,
		MgmtVLAN: rc.MgmtVLAN,
		Links:    map[string]driver.LinkState{},
	}
	var p statusPort
	for _, p = range status {
		if p.LinkUp {
			st.Links[p.Name] = driver.LinkUpState
		}
		var _, known = st.Ports[p.Name]
		if !known {
			st.Ports[p.Name] = PortState{Mode: "access", PVID: 1}
		}
	}
	return st
}

// protectedTaggedLoss answers: would applying this intent REMOVE a protected VLAN
// from this port's allowed list? Returns the VLAN and the reason it is protected,
// or 0.
//
// Only an EXACT intent can lose anything: a TaggedAtLeast intent never removes.
// An `allowedAll` port is the dangerous case — everything is currently allowed, so
// an exact list drops every VLAN it does not name.
func protectedTaggedLoss(want, now PortState, allowedAll bool, protected map[int]string) (int, string) {
	if want.Mode != "trunk" || want.TaggedAtLeast {
		return 0, ""
	}
	var keep = map[int]bool{}
	var v int
	for _, v = range want.Tagged {
		keep[v] = true
	}
	if want.PVID > 0 {
		keep[want.PVID] = true
	}
	var ids []int
	var id int
	for id = range protected {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id = range ids {
		if keep[id] {
			continue
		}
		if allowedAll {
			// Everything is allowed today; the exact list would drop this one.
			return id, protected[id]
		}
		for _, v = range now.Tagged {
			if v == id {
				return id, protected[id]
			}
		}
		if now.PVID == id {
			return id, protected[id]
		}
	}
	return 0, ""
}

// missingVLANs: the VLANs to create — site plan ∪ management VLAN ∪ any VLAN
// referenced by a wanted port, minus what already exists. Bounded 1..4094,
// sorted.
//
// UNLIKE the Cisco connector, no ID range is excluded here. IOS carries
// 1002–1005 as immovable FDDI / Token Ring defaults that can never be created or
// removed, so reporting them would make the site VLAN diff permanently wrong. EOS
// has no such reserved block — those IDs are ordinary VLANs on this platform —
// and copying the exclusion across would have made four perfectly usable VLAN IDs
// silently impossible to deploy.
func missingVLANs(have []VLAN, site []VLAN, mgmtVLAN int, desired map[string]PortState) []VLAN {
	var existing = map[int]bool{}
	var v VLAN
	for _, v = range have {
		existing[v.ID] = true
	}
	var wanted = map[int]string{}
	for _, v = range site {
		wanted[v.ID] = v.Name
	}
	if mgmtVLAN > 0 {
		var _, seen = wanted[mgmtVLAN]
		if !seen {
			wanted[mgmtVLAN] = ""
		}
	}
	var p PortState
	for _, p = range desired {
		if p.PVID > 0 {
			var _, seen = wanted[p.PVID]
			if !seen {
				wanted[p.PVID] = ""
			}
		}
		var t int
		for _, t = range p.Tagged {
			var _, seen = wanted[t]
			if !seen {
				wanted[t] = ""
			}
		}
	}
	var out []VLAN
	var id int
	var nm string
	for id, nm = range wanted {
		if id < 1 || id > 4094 || existing[id] {
			continue
		}
		out = append(out, VLAN{ID: id, Name: nm})
	}
	sortVLANs(out)
	return out
}

// portConverged: nothing to write when the observed state already carries the
// intent.
//
// `allowedAll` is what makes this correct: a trunk with no explicit allowed list
// satisfies any AT-LEAST intent (everything is allowed) and satisfies no exact
// intent (it allows more than was asked).
func portConverged(want, now PortState, allowedAll bool) bool {
	if want.Mode != now.Mode {
		return false
	}
	if want.PVID > 0 && want.PVID != now.PVID {
		return false
	}
	if want.Mode == "trunk" {
		if want.TaggedAtLeast {
			if allowedAll {
				return true
			}
			return containsAll(now.Tagged, want.Tagged)
		}
		if allowedAll {
			return false
		}
		return equalInts(exactAllowed(want), now.Tagged)
	}
	return true
}

// exactAllowed is the list an exact trunk intent must end up with: the tagged
// VLANs PLUS the native one. EOS drops native traffic on a trunk whose allowed
// list omits the native VLAN, so leaving it out would produce a port that looks
// configured and forwards nothing.
func exactAllowed(want PortState) []int {
	var set = map[int]bool{}
	var out []int
	var v int
	for _, v = range want.Tagged {
		if !set[v] {
			set[v] = true
			out = append(out, v)
		}
	}
	var native = want.PVID
	if native < 1 {
		native = 1
	}
	if !set[native] {
		out = append(out, native)
	}
	sort.Ints(out)
	return out
}

// portCmds builds the convergence for one port. ONE form per mode — cf. the file
// header on why there are no ordered candidates on this platform.
func portCmds(name string, want, now PortState, allowedAll bool) []string {
	var enter = "interface " + cliPortName(name)

	if want.Mode == "trunk" {
		var cmds = []string{enter, "switchport mode trunk"}
		if want.PVID > 0 && want.PVID != now.PVID {
			cmds = append(cmds, "switchport trunk native vlan "+strconv.Itoa(want.PVID))
		}
		if want.TaggedAtLeast {
			// Never restrict on an unresolved "all": only add what is missing, and
			// add nothing at all when everything is already allowed.
			if !allowedAll {
				var toAdd = subtractInts(want.Tagged, now.Tagged)
				if len(toAdd) > 0 {
					cmds = append(cmds, "switchport trunk allowed vlan add "+joinInts(toAdd))
				}
			}
		} else {
			// The SET form, not "add": it is the only one that restricts, and it
			// is idempotent, which "add" on an all-allowed trunk is not.
			cmds = append(cmds, "switchport trunk allowed vlan "+joinInts(exactAllowed(want)))
		}
		return append(cmds, "exit")
	}

	var pvid = want.PVID
	if pvid < 1 {
		pvid = 1
	}
	return []string{
		enter,
		"switchport mode access",
		"switchport access vlan " + strconv.Itoa(pvid),
		"exit",
	}
}

// runConfigBlock runs commands in configuration mode: "configure", the commands,
// "end". The first CLI refusal interrupts the block — but "end" is ALWAYS sent, so
// the session is never left in a sub-mode the next gesture does not know about.
//
// "configure" and not "configure terminal": EOS accepts both, and the short
// form is the one the platform's own documentation and its prompts use. The
// distinction matters only in a transcript, which is exactly where it matters:
// an operator pasting these lines back should be pasting EOS, not IOS-with-an-
// accent.
func (c *Client) runConfigBlock(ctx context.Context, cmds []string) error {
	if len(cmds) == 0 {
		return nil
	}
	var _, err = c.cli.run(ctx, "configure", cmdTimeout)
	if err != nil {
		return fmt.Errorf("configure: %v", err)
	}
	var failure error
	var cmd string
	for _, cmd = range cmds {
		_, err = c.cli.run(ctx, cmd, cmdTimeout)
		if err != nil {
			failure = fmt.Errorf("%s: %v", cmd, err)
			break
		}
	}
	var _, endErr = c.cli.run(ctx, "end", cmdTimeout)
	if failure != nil {
		return failure
	}
	if endErr != nil {
		return fmt.Errorf("end: %v", endErr)
	}
	return nil
}

// redactSecrets hides account secrets before a command block reaches the
// transcript.
//
// The transcript is shown to an operator and stored with the pipeline run, so a
// "username X ... secret 0 hunter2" line copied into it verbatim would put the
// fleet's credential in a record that outlives the session. Only the tail after
// "secret" is replaced — the rest of the line is what makes the transcript
// diagnosable, and blanking the whole command would defeat the purpose of having
// one.
func redactSecrets(cmds []string) string {
	var out = make([]string, 0, len(cmds))
	var cmd string
	for _, cmd = range cmds {
		var idx = strings.Index(strings.ToLower(cmd), " secret ")
		if idx >= 0 {
			out = append(out, cmd[:idx+len(" secret ")]+"•••")
			continue
		}
		out = append(out, cmd)
	}
	return strings.Join(out, " ; ")
}
