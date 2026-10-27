package ios

// ApplyPorts — VLAN membership of the ports, the most dangerous operation in
// the product. The guardrails follow the FS S3900 connector (apply.go); two
// of them are IOS-specific and exist because this CLI has two behaviours the
// others do not.
//
// # 1. An absent allowed-vlan line means ALL, not none
//
// On IOS a trunk with no "switchport trunk allowed vlan" carries every VLAN.
// Reading that as an empty tagged list is not a rounding error, it inverts the
// meaning: an "add" would change nothing (everything was already allowed),
// the re-read would still show no line, and the port would report drift for
// ever while every apply issued a no-op. So an exact intent on such a port is
// written with the SET form ("switchport trunk allowed vlan 99,100"), which
// is the only one that actually restricts, and the native VLAN is always
// included in it — IOS drops native traffic on a trunk whose allowed list does
// not contain the native VLAN.
//
// # 2. Restricting a trunk can cut the session even when its native VLAN is safe
//
// The classic guardrail protects the port whose NATIVE VLAN is the management
// one. On this platform administration usually arrives TAGGED on an uplink
// whose native VLAN is 1 — so that check sees nothing, and the exact-set write
// above would quietly remove the management VLAN from the only port carrying
// it. Hence [protectedTaggedLoss]: an active port never loses a protected VLAN
// from its allowed list without Force.
//
// # What is CAPTURED and what is DEDUCED
//
// Nothing is captured (cf. the package doc). The forms that genuinely differ
// across the Catalyst family are therefore run as ORDERED candidates — trunk
// encapsulation being the one that matters — and the accepted form is recorded
// in the transcript, which is how this connector builds the capture it lacks.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// ApplyPorts writes VLAN membership on the ports, then PERSISTS (IOS
// distinguishes running from startup; a write a reboot would undo is worse
// than a refused write).
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

	// A write NEVER re-reads a cache: the device comes first. The
	// running-config read here also serves as the safety copy returned to the
	// caller.
	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("pre-write read failed: %v", err)
	}
	out.Backup = runOut
	var rc = parseRunningConfig(runOut, c.host)

	// LLDP, once, if the switch is not already running it. IOS ships with LLDP
	// OFF and speaks only CDP, so a Catalyst is invisible to every neighbour in
	// the fleet and serves an EMPTY LLDP-MIB — meshcraft's topology has a hole
	// exactly where the switch is (observed 2026-08-03: an adopted 2960-X with
	// "% LLDP is not enabled", zero neighbours, while it was cabled to the
	// fleet). Provisioning writes "lldp run", but a device adopted before that
	// never got it; enabling it on apply closes the gap for the whole existing
	// fleet. Idempotent and best-effort — a failure here must not fail the port
	// write it rides along with.
	if !rc.LLDPRun {
		var lerr = c.runConfigBlock(ctx, []string{"lldp run"})
		if lerr == nil {
			c.step(true, "LLDP enabled globally", "\"lldp run\" — the switch was invisible to LLDP neighbours")
		} else {
			out.Notes = append(out.Notes, "could not enable LLDP (\"lldp run\"): "+firstLine(lerr.Error()))
		}
	}

	var vlanOut string
	vlanOut, err = c.cli.run(ctx, "show vlan brief", cmdTimeout)
	if err != nil && vlanOut == "" {
		return out, fmt.Errorf("VLAN database read failed: %v — meshcraft cannot tell a "+
			"missing VLAN from an existing one, so nothing is written", err)
	}
	var haveVLANs = parseVlanBrief(vlanOut)

	var statusOut string
	statusOut, err = c.cli.run(ctx, "show interfaces status", cmdTimeout)
	if err != nil && statusOut == "" {
		return out, fmt.Errorf("link read failed: %v — the guardrails cannot tell an "+
			"active port from a dead one, so nothing is written", err)
	}
	// How this chassis spells an interface name, before anything is addressed to
	// one (cf. Client.learnPortStem).
	c.learnPortStem(runOut)
	c.learnPortStem(statusOut)
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
		return out, fmt.Errorf("unknown management VLAN, from plan or device: write " +
			"refused, meshcraft cannot guarantee not cutting access")
	}
	if plan.MgmtVLAN > 0 && current.MgmtVLAN > 0 && plan.MgmtVLAN != current.MgmtVLAN {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"site plan says management VLAN %d but the device administers itself through "+
				"VLAN %d: both stay protected", plan.MgmtVLAN, current.MgmtVLAN))
	}

	// 1. Missing VLANs first — a port cannot be placed in a VLAN that does not
	//    exist, and on IOS the interface command is accepted anyway, which
	//    would leave the port assigned to a VLAN nothing forwards.
	var missing = missingVLANs(current.VLANs, plan.SiteVLANs, plan.MgmtVLAN, desired)
	if len(missing) > 0 {
		var cmds = vlanCreateCmds(missing)
		var cerr = c.runConfigBlock(ctx, cmds)
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
					"%d (%s) from its allowed list. On this platform administration usually "+
					"arrives TAGGED on the uplink, so this is exactly how a switch is lost. "+
					"Use force only knowing why", name, lost, lostWhy))
			continue
		}
		if portConverged(want, now, allowedAll) {
			continue
		}
		var cliName = c.portName(name)
		if cliName == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: this name is outside the standard-model mapping this "+
					"connector translates (stack member 1, module slot 0) — nothing was sent "+
					"to the device", name))
			continue
		}

		var groups = portCmdCandidates(name, cliName, want, now, allowedAll)
		var applied, detail = c.runWithFallbacks(ctx, groups)
		if !applied {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not converged: the CLI refused every known form (%s) — command set "+
					"written against IOS %s from documentation, survey this device's forms",
				name, detail, DocumentedRelease))
			continue
		}
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

// protectedTaggedLoss answers: would applying this intent REMOVE a protected
// VLAN from this port's allowed list? Returns the VLAN and the reason it is
// protected, or 0.
//
// Only an EXACT intent can lose anything: a TaggedAtLeast intent never removes.
// An `allowedAll` port is the dangerous case — everything is currently allowed,
// so an exact list drops every VLAN it does not name.
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
// reserved IDs excluded, sorted.
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
		if id < 1 || id > 4094 || reservedVLAN(id) || existing[id] {
			continue
		}
		out = append(out, VLAN{ID: id, Name: nm})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// vlanCreateCmds: the IOS global-configuration form. A bare "vlan N" is
// enough (unlike FSOS, which demands a name and a media); the name is set on
// the following line when the plan carries one, and OMITTED when it does not —
// IOS then keeps its own default (VLAN0100), which is exactly what
// parseVlanBrief will read back.
func vlanCreateCmds(missing []VLAN) []string {
	var cmds []string
	var v VLAN
	for _, v = range missing {
		cmds = append(cmds, fmt.Sprintf("vlan %d", v.ID))
		if strings.TrimSpace(v.Name) != "" {
			cmds = append(cmds, "name "+sanitizeVlanName(v.Name))
		}
		cmds = append(cmds, "exit")
	}
	return cmds
}

// portConverged: nothing to write when the observed state already carries the
// intent.
//
// `allowedAll` is what makes this correct on IOS: a trunk with no explicit
// allowed list satisfies any AT-LEAST intent (everything is allowed) and
// satisfies no exact intent (it allows more than was asked).
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
// VLANs PLUS the native one. IOS drops native traffic on a trunk whose allowed
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

// cmdGroup: one intent, several ORDERED candidate CLI forms.
type cmdGroup struct {
	label string
	// alternatives: command sets tried in order; the first set whose commands
	// ALL pass wins.
	alternatives [][]string
}

// portCmdCandidates builds the convergence for one port.
//
// `name` is the standard-model name and labels the transcript; `cliName` is the
// FULL IOS name the caller resolved against this device's own spelling
// ([Client.portName]) and is the only thing sent to the wire. They are two
// arguments and not one because a 3560 answers to "GigabitEthernet0/24" and
// nothing else, while the label an operator reads stays "GE24" across the
// fleet.
//
// The trunk block carries TWO alternatives and that is the one family
// difference that actually bites: a 2960/2960-X speaks 802.1Q only and refuses
// "switchport trunk encapsulation dot1q", while a 3560/3750 refuses
// "switchport mode trunk" until that same line has been set. Neither order
// works everywhere, so both are tried and the transcript records which one the
// device took.
func portCmdCandidates(name, cliName string, want, now PortState, allowedAll bool) []cmdGroup {
	var enter = "interface " + cliName
	var groups []cmdGroup

	if want.Mode == "trunk" {
		var body []string
		body = append(body, "switchport mode trunk")
		if want.PVID > 0 && want.PVID != now.PVID {
			body = append(body, "switchport trunk native vlan "+strconv.Itoa(want.PVID))
		}
		if want.TaggedAtLeast {
			// Never restrict on an unresolved "all": only add what is missing,
			// and add nothing at all when everything is already allowed.
			if !allowedAll {
				var toAdd = subtractInts(want.Tagged, now.Tagged)
				if len(toAdd) > 0 {
					body = append(body, "switchport trunk allowed vlan add "+joinInts(toAdd))
				}
			}
		} else {
			// The SET form, not "add": it is the only one that restricts, and
			// it is idempotent, which "add" on an all-allowed trunk is not.
			body = append(body, "switchport trunk allowed vlan "+joinInts(exactAllowed(want)))
		}
		body = append(body, "exit")

		var plain = append([]string{enter}, body...)
		var encap = append([]string{enter, "switchport trunk encapsulation dot1q"}, body...)
		groups = append(groups, cmdGroup{
			label:        name + ": trunk",
			alternatives: [][]string{plain, encap},
		})
		return groups
	}

	// access — one form, universal across the family since IOS 12.1.
	var pvid = want.PVID
	if pvid < 1 {
		pvid = 1
	}
	groups = append(groups, cmdGroup{
		label: name + ": access VLAN " + strconv.Itoa(pvid),
		alternatives: [][]string{
			{enter, "switchport mode access", "switchport access vlan " + strconv.Itoa(pvid), "exit"},
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
// "end". The first CLI refusal interrupts the block — but "end" is ALWAYS
// sent, so the session is never left in a sub-mode the next gesture does not
// know about.
func (c *Client) runConfigBlock(ctx context.Context, cmds []string) error {
	var _, err = c.cli.run(ctx, "configure terminal", cmdTimeout)
	if err != nil {
		return fmt.Errorf("configure terminal: %v", err)
	}
	var failure error
	var cmd string
	for _, cmd = range cmds {
		var timeout = cmdTimeout
		if strings.HasPrefix(cmd, "crypto key generate") {
			timeout = cryptoTimeout
		}
		_, err = c.cli.run(ctx, cmd, timeout)
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

func joinInts(list []int) string {
	var parts []string
	var v int
	for _, v = range list {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ",")
}
