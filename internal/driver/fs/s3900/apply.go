package s3900

// ApplyPorts — VLAN membership of the ports, the most dangerous operation in
// the product. The guardrails follow RouterOS (apply.go); the CLI does not.
//
// # What is CAPTURED and what is DEDUCED — the boundary at the heart of this file
//
// Captured on the lab OpCode 1.7.0: "vlan database" + "vlan N name X media
// ethernet", "switchport mode trunk", "switchport trunk allowed vlan add L",
// saving via "copy running-config startup-config" + the answer "startup1.cfg"
// to the filename prompt.
//
// DEDUCED from the Accton family (never typed on this firmware): the form of
// ACCESS ports and of removals. Each deduced form is therefore run as ORDERED
// candidates: the first one accepted wins, a double refusal puts the port in
// Skipped with the CLI's exact words — never a silent success on a rejected
// command. The transcript keeps which form was accepted: it is the capture
// that was missing, made by the tool itself.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// ApplyPorts writes VLAN membership on the ports, then PERSISTS (the platform
// distinguishes running from startup; a write a reboot would undo would be
// worse than a refused write).
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
	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("pre-write read failed: %v", err)
	}
	out.Backup = runOut
	var rc = parseRunningConfig(runOut, c.host)

	var briefOut string
	briefOut, err = c.cli.run(ctx, "show interfaces brief", cmdTimeout)
	if err != nil && briefOut == "" {
		return out, fmt.Errorf("link read failed: %v — the guardrails cannot tell an "+
			"active port from a dead one, so nothing is written", err)
	}
	var current = stateFromReads(rc, parseInterfacesBrief(briefOut))

	// Guardrails: the protected VLANs, the plan's AND the device's own.
	var protected = map[int]string{}
	if plan.MgmtVLAN > 0 {
		protected[plan.MgmtVLAN] = "site plan management VLAN"
	}
	if current.MgmtVLAN > 0 {
		protected[current.MgmtVLAN] = "management VLAN declared by the device"
	}
	if len(protected) == 0 {
		return out, fmt.Errorf("unknown management VLAN, from plan or device: write " +
			"refused, meshcraft cannot guarantee not cutting access")
	}
	if plan.MgmtVLAN > 0 && current.MgmtVLAN > 0 && plan.MgmtVLAN != current.MgmtVLAN {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"site plan says management VLAN %d but the device administers itself through "+
				"VLAN %d: both stay protected", plan.MgmtVLAN, current.MgmtVLAN))
	}

	// 1. Missing VLANs first — a port cannot be placed in a VLAN that is absent.
	var missing = missingVLANs(current.VLANs, plan.SiteVLANs, plan.MgmtVLAN, desired)
	if len(missing) > 0 {
		var applied, detail = c.runWithFallbacks(ctx, []cmdGroup{{
			label:        fmt.Sprintf("%d VLAN(s) created", len(missing)),
			alternatives: vlanCreateCandidates(missing, c.facts),
		}})
		if !applied {
			// A VLAN that does not exist cannot receive a port: stopping here is
			// what keeps the rest of the run from reporting a pile of port
			// failures whose single cause was this one.
			return out, fmt.Errorf("VLAN creation refused in every known form (%s)", detail)
		}
		var v VLAN
		for _, v = range missing {
			out.VLANsCreated = append(out.VLANsCreated, v.ID)
		}
	}

	// 2. Ports, one at a time.
	var names []string
	var name string
	for name = range desired {
		names = append(names, name)
	}
	sort.Strings(names)

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
		var why = protected[now.PVID]
		if why != "" && want.PVID != now.PVID && current.HasLink(name) && !plan.Force {
			// A spared port is spared IN FULL: neither PVID nor lists.
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: it carries an ACTIVE link and its native VLAN is %d (%s) — "+
					"changing it would very likely cut access. Use force only knowing why",
				name, now.PVID, why))
			continue
		}
		if portConverged(want, now) {
			continue
		}
		if cliPortName(name) == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: this name is outside the standard-model mapping this "+
					"connector translates (unit 1 only) — nothing was sent to the device", name))
			continue
		}

		var groups = portCmdCandidates(name, want, now)
		var applied, detail = c.runWithFallbacks(ctx, groups)
		if !applied {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not converged: the CLI refused every known form (%s) — command set "+
					"verified on OpCode %s.x, survey this firmware's forms", name, detail, VerifiedOpCode))
			continue
		}
		out.PortsWritten = append(out.PortsWritten, name)
	}

	// 3. Persistence — the contract requires it of the write itself.
	var perr = c.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes,
			"applied but NOT persisted: "+firstLine(perr.Error())+" — a reboot would undo this configuration")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", "copy running-config startup-config → "+StartupConfigFile)
	return out, nil
}

// stateFromReads assembles the current state from the two reads.
func stateFromReads(rc runConfig, brief []briefPort) State {
	var st = State{
		VLANs:    rc.VLANs,
		Ports:    rc.Ports,
		MgmtVLAN: rc.MgmtVLAN,
		Links:    map[string]driver.LinkState{},
	}
	var p briefPort
	for _, p = range brief {
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

// missingVLANs: the VLANs to create — site plan ∪ management VLAN ∪ any VLAN
// referenced by a wanted port, minus what already exists. Bounded 1..4094, sorted.
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
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// vlanCreateCmds: the "vlan database" block, in the form THIS firmware accepts.
//
// # The trailing "media ethernet" is version-dependent, and that is CAPTURED
//
// OpCode 1.7.0 (the original lab survey) REQUIRES it: a bare "vlan N" answers
// "% Incomplete command", and the accepted form is "vlan N name X media ethernet".
//
// OpCode 1.7.7 REJECTS it. Captured on the lab S3900-24T4S after its upgrade,
// 2026-08-02, with the caret pointing straight at the removed keyword:
//
//	Switch-6(config-vlan)#vlan 4000 name MCTEST
//	Switch-6(config-vlan)#vlan 4001 name MCTEST2 media ethernet
//	                                              ^
//	% Invalid input detected at '^' marker.
//
//	Switch-6#show vlan id 4000 → VLAN ID : 4000, Name : MCTEST, Type : Static
//	Switch-6#show vlan id 4001 → No such VLAN.
//
// So the bare form works on the new firmware and the old form does not. One
// keyword, and it is the difference between "VLAN created" and every port that
// needed that VLAN landing in Skipped.
//
// # Why both forms ship, ordered rather than chosen
//
// The version gate picks which to TRY FIRST; the other stays as a fallback, and
// the transcript records which was accepted. That is the same discipline
// portCmdCandidates already applies to deduced forms, and it is what keeps this
// working on a fleet running both firmwares — including the case this file cannot
// foresee, a revision that reverts the change or a variant that never had it.
//
// Reading the version and trusting it alone would be the brittle choice: it would
// turn every unrecognised revision string into a refusal, where trying two forms
// costs one extra command on a device that is about to be reconfigured anyway.
func vlanCreateCmds(missing []VLAN, media bool) []string {
	var cmds = []string{"vlan database"}
	var v VLAN
	for _, v = range missing {
		var name = v.Name
		if name == "" {
			name = fmt.Sprintf("VLAN%04d", v.ID)
		}
		var line = fmt.Sprintf("vlan %d name %s", v.ID, sanitizeVlanName(name))
		if media {
			line += " media ethernet"
		}
		cmds = append(cmds, line)
	}
	cmds = append(cmds, "exit")
	return cmds
}

// vlanCreateCandidates orders the two forms for THIS device's firmware.
//
// The bare form first from 1.7.7 onward, the "media ethernet" form first below
// it. Both are always present: the gate decides the order, never the menu.
func vlanCreateCandidates(missing []VLAN, facts versionFacts) [][]string {
	if wantsBareVlanCreate(facts) {
		return [][]string{
			vlanCreateCmds(missing, false),
			vlanCreateCmds(missing, true),
		}
	}
	return [][]string{
		vlanCreateCmds(missing, true),
		vlanCreateCmds(missing, false),
	}
}

// portConverged: nothing to write when the observed state already carries the intent.
func portConverged(want, now PortState) bool {
	if want.Mode != now.Mode {
		return false
	}
	if want.PVID > 0 && want.PVID != now.PVID {
		return false
	}
	if want.Mode == "trunk" {
		if want.TaggedAtLeast {
			return containsAll(now.Tagged, want.Tagged)
		}
		return equalInts(want.Tagged, now.Tagged)
	}
	return true
}

// cmdGroup: one intent, several ORDERED candidate CLI forms.
type cmdGroup struct {
	label string
	// alternatives: command sets tried in order; the first set whose commands
	// ALL pass wins.
	alternatives [][]string
}

// portCmdCandidates builds the convergence for one port. Captured forms carry
// only one alternative; deduced forms carry several.
func portCmdCandidates(name string, want, now PortState) []cmdGroup {
	var enter = "interface " + cliPortName(name)
	var groups []cmdGroup

	if want.Mode == "trunk" {
		var cmds = []string{enter, "switchport mode trunk"}
		var toAdd = subtractInts(want.Tagged, now.Tagged)
		if len(toAdd) > 0 {
			cmds = append(cmds, "switchport trunk allowed vlan add "+joinInts(toAdd))
		}
		if want.PVID > 0 && want.PVID != now.PVID {
			// DEDUCED form (the native form was never captured): two spellings
			// of the same family.
			groups = append(groups, cmdGroup{
				label: name + ": trunk native VLAN " + strconv.Itoa(want.PVID),
				alternatives: [][]string{
					append(append([]string{}, cmds...),
						"switchport trunk native vlan "+strconv.Itoa(want.PVID), "exit"),
					append(append([]string{}, cmds...),
						"switchport native vlan "+strconv.Itoa(want.PVID), "exit"),
				},
			})
		} else {
			groups = append(groups, cmdGroup{
				label:        name + ": trunk",
				alternatives: [][]string{append(cmds, "exit")},
			})
		}
		if !want.TaggedAtLeast {
			var toRemove = subtractInts(now.Tagged, want.Tagged)
			// The wanted native VLAN is never removed from its own port.
			toRemove = removeInt(toRemove, want.PVID)
			if len(toRemove) > 0 {
				groups = append(groups, cmdGroup{
					label: name + ": remove stale tagged " + joinInts(toRemove),
					alternatives: [][]string{
						{enter, "switchport trunk allowed vlan remove " + joinInts(toRemove), "exit"},
						{enter, "switchport allowed vlan remove " + joinInts(toRemove), "exit"},
					},
				})
			}
		}
		return groups
	}

	// access — DEDUCED form: "switchport access vlan" first (more recent
	// family), Accton hybrid style next.
	var pvid = want.PVID
	if pvid < 1 {
		pvid = 1
	}
	groups = append(groups, cmdGroup{
		label: name + ": access VLAN " + strconv.Itoa(pvid),
		alternatives: [][]string{
			{enter, "switchport mode access", "switchport access vlan " + strconv.Itoa(pvid), "exit"},
			{enter, "switchport allowed vlan add " + strconv.Itoa(pvid) + " untagged",
				"switchport native vlan " + strconv.Itoa(pvid), "exit"},
		},
	})
	return groups
}

// runWithFallbacks runs each group: the first alternative whose commands ALL
// pass wins. Returns (fully applied?, refusal detail).
func (c *Client) runWithFallbacks(ctx context.Context, groups []cmdGroup) (bool, string) {
	var refusals []string
	var allOK = true
	var g cmdGroup
	for _, g = range groups {
		var groupOK = false
		var alt []string
		var ai int
		for ai, alt = range g.alternatives {
			var err = c.runConfigBlock(ctx, alt)
			if err == nil {
				var how = ""
				if ai > 0 {
					how = fmt.Sprintf(" (fallback form %d)", ai+1)
				}
				c.step(true, g.label+how)
				groupOK = true
				break
			}
			refusals = append(refusals, firstLine(err.Error()))
			c.step(false, g.label+": form rejected", firstLine(err.Error()))
		}
		if !groupOK {
			allOK = false
		}
	}
	return allOK, strings.Join(refusals, " ; ")
}

// runConfigBlock runs commands in configuration mode: "conf t", the commands,
// "end". The first CLI error interrupts the block — but "end" is ALWAYS sent,
// so the session is never left in a sub-mode the next gesture doesn't know about.
func (c *Client) runConfigBlock(ctx context.Context, cmds []string) error {
	var _, err = c.cli.run(ctx, "conf t", cmdTimeout)
	if err != nil {
		return fmt.Errorf("conf t: %v", err)
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

// --- set helpers (sorted, PortState contract) ---

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	var i int
	for i = range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsAll(have, want []int) bool {
	var set = map[int]bool{}
	var v int
	for _, v = range have {
		set[v] = true
	}
	for _, v = range want {
		if !set[v] {
			return false
		}
	}
	return true
}

func subtractInts(from, minus []int) []int {
	var set = map[int]bool{}
	var v int
	for _, v = range minus {
		set[v] = true
	}
	var out []int
	for _, v = range from {
		if !set[v] {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func removeInt(list []int, x int) []int {
	var out []int
	var v int
	for _, v = range list {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func joinInts(list []int) string {
	var parts []string
	var v int
	for _, v = range list {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ",")
}
