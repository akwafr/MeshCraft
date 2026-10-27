package s3400

// VLAN and port writes.
//
// Every form below was RUN on a S3400-48T4SP (Version 2.0.2J) on 2026-08-03
// and read back from "show running-config interface" and "show vlan":
//
//	config                          → Switch_config#
//	vlan 200                        → Switch_config_vlan200#
//	 name TEST_CLAUDE
//	 exit
//	interface g0/1                  → Switch_config_g0/1#
//	 switchport pvid 200            (access VLAN — NOT "switchport access vlan")
//	 switchport mode trunk
//	 switchport trunk vlan-allowed 1-10
//	 exit
//	exit
//
// The reverts ("no switchport pvid", "no vlan 200") were run too, and the
// switch came back to its exact baseline.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"meshcraft/internal/driver"
)

// ApplyPorts writes the VLAN plan.
//
// Order matters and is not negotiable: a VLAN must EXIST before a port can be
// put in it, and the management VLAN is never removed from the uplink that
// carries this very session.
func (c *Client) ApplyPorts(ctx context.Context, plan PortPlan) (ApplyResult, error) {
	var res ApplyResult
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}

	// What exists now — a plan is applied against the device, not against a
	// hope about it.
	var before, readErr = c.ReadState(ctx)
	if readErr != nil {
		return res, fmt.Errorf("reading the state before applying: %w", readErr)
	}

	var existing = map[int]bool{}
	var v driver.VLAN
	for _, v = range before.VLANs {
		existing[v.ID] = true
	}

	// 1) Create the VLANs the plan needs.
	var needed = neededVLANs(plan, before)
	var id int
	for _, id = range needed {
		if existing[id] {
			continue
		}
		if err = c.createVLAN(ctx, id, vlanNameFor(id, plan)); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("vlan %d: %v", id, err))
			continue
		}
		existing[id] = true
		res.VLANsCreated = append(res.VLANsCreated, id)
	}

	// 2) Write the ports.
	var names = sortedPortNames(plan.Desired)
	var name string
	for _, name = range names {
		var want = plan.Desired[name]
		var have, known = before.Ports[name]
		if known && samePort(have, want) {
			continue
		}
		var mgmt = plan.MgmtVLAN
		if mgmtGuardBlocks(have, want, mgmt, before.HasLink(name), plan.Force) {
			// Taking the management VLAN off a port is how a switch is lost.
			// Force says the caller meant it; without it, refuse and say so.
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: would stop carrying the management VLAN %d — refused without force", name, mgmt))
			continue
		}
		if err = c.writePort(ctx, name, want); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		res.PortsWritten = append(res.PortsWritten, name)
	}

	// LLDP by default. This platform ships it OFF, and a switch with no
	// "lldp run" is invisible to every neighbour and sees none — the
	// topology view stays empty on a perfectly healthy device. It is written
	// here rather than left to provisioning so a switch adopted before this
	// existed also gets it on its next apply.
	if c.lldpKnown && !c.lldpOn {
		if err = c.EnsureLLDP(ctx); err != nil {
			res.Skipped = append(res.Skipped, "lldp run: "+err.Error())
		} else {
			c.lldpOn = true
			res.Notes = append(res.Notes,
				"LLDP was off and has been enabled (\"lldp run\") — neighbours appear "+
					"within one advertisement interval")
		}
	}

	if len(res.PortsWritten) > 0 || len(res.VLANsCreated) > 0 {
		res.Notes = append(res.Notes,
			"applied to the running configuration; \"write\" is what makes it survive a reboot")
	}
	return res, nil
}

// createVLAN: "vlan N" then "name X". Captured — the VLAN configuration
// mode is entered by the bare id and the prompt becomes Switch_config_vlanN#.
func (c *Client) createVLAN(ctx context.Context, id int, name string) error {
	var err = c.configBlock(ctx, []string{
		fmt.Sprintf("vlan %d", id),
		"name " + sanitizeVlanName(name),
		"exit",
	})
	if err != nil {
		return err
	}
	c.step(true, fmt.Sprintf("VLAN %d created", id), "name "+sanitizeVlanName(name))
	return nil
}

// portConfigCmds builds the configuration block for one port.
//
// Pure so the exact wire form can be asserted in a test — this is the function
// that got a switch wrong once already (see the access branch). It stays pure
// now that the cage numbering is a session fact: what it takes is the FACT
// (ADR-0018), not the session, so a test still pins the exact wire form under
// either convention without a device.
func portConfigCmds(cages cageNumbering, standard string, want PortState) ([]string, error) {
	var dev, nerr = cages.cliPortName(standard)
	if nerr != nil {
		return nil, nerr
	}
	var cmds = []string{"interface " + dev}
	if strings.EqualFold(want.Mode, "trunk") {
		cmds = append(cmds, "switchport mode trunk")
		if len(want.Tagged) > 0 {
			// The native VLAN must be in the allowed list for the device, while
			// meshcraft's model carries it separately as PVID — so it is added
			// here, and the read side removes it again.
			var allowed = append([]int{}, want.Tagged...)
			if want.PVID > 0 {
				allowed = append(allowed, want.PVID)
			}
			cmds = append(cmds, "switchport trunk vlan-allowed "+compactVlanList(allowed))
		}
		if want.PVID > 0 {
			cmds = append(cmds, fmt.Sprintf("switchport pvid %d", want.PVID))
		}
		cmds = append(cmds, "exit")
		return cmds, nil
	}

	// ACCESS. "switchport mode access" MUST be sent, and this is where an
	// earlier version of this connector was wrong: it assumed access was the
	// platform default and sent "switchport pvid" alone. That is true of a
	// FACTORY port and false of a port that is currently a trunk — which kept
	// "switchport mode trunk" and went on carrying every allowed VLAN, for
	// ever, because meshcraft only re-applies after a configuration change.
	// Observed 2026-08-03 on GE45-48 of the lab 48T4SP: still trunks after
	// repeated applies of an access intent.
	//
	// The trunk's allowed list SURVIVES the mode change (captured), inert on an
	// access port but ready to come back to life if the port were ever made a
	// trunk again with an empty tagged set — so it is cleared too. Both
	// commands were verified idempotent on a port that was ALREADY access
	// (g0/44), which is what makes it safe to send them unconditionally.
	cmds = append(cmds, "switchport mode access", "no switchport trunk vlan-allowed")
	if want.PVID > 0 {
		cmds = append(cmds, fmt.Sprintf("switchport pvid %d", want.PVID))
	}
	cmds = append(cmds, "exit")
	return cmds, nil
}

// writePort applies one port's intent.
func (c *Client) writePort(ctx context.Context, standard string, want PortState) error {
	var cmds, nerr = portConfigCmds(c.cages, standard, want)
	if nerr != nil {
		// NOTHING WAS SENT. The refusal is the whole point: see ADR-0018 and
		// cageNumbering.cliPortName — configBlock would have opened a block on
		// an invented interface name and typed the rest of it one level up.
		return nerr
	}
	var err = c.configBlock(ctx, cmds)
	if err != nil {
		return err
	}
	c.step(true, standard+" written", describePort(want))
	return nil
}

// configBlock runs a list of commands inside "config" … "exit".
//
// Each command waits for its own prompt: this CLI answers an error as three
// lines (the echo, a caret, then "Unknown command") and sending the next
// command before that has landed would attribute the error to the wrong line.
func (c *Client) configBlock(ctx context.Context, cmds []string) error {
	var _, err = c.cli.run(ctx, "config", cmdTimeout)
	if err != nil {
		return fmt.Errorf("entering configuration mode: %w", err)
	}
	var failures []string
	var cmd string
	for _, cmd = range cmds {
		var _, cerr = c.cli.run(ctx, cmd, cmdTimeout)
		if cerr != nil {
			failures = append(failures, fmt.Sprintf("\"%s\": %v", cmd, cerr))
		}
	}
	// Leave configuration mode whatever happened — a session left in
	// Switch_config# would make the next read's parser see prompts it does not
	// expect.
	var _, _ = c.cli.run(ctx, "exit", cmdTimeout)
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, " ; "))
	}
	return nil
}

// neededVLANs is every VLAN the plan references, plus the site's list.
func neededVLANs(plan PortPlan, before State) []int {
	var want = map[int]bool{}
	var ps PortState
	for _, ps = range plan.Desired {
		if ps.PVID > 0 {
			want[ps.PVID] = true
		}
		var id int
		for _, id = range ps.Tagged {
			want[id] = true
		}
	}
	var v driver.VLAN
	for _, v = range plan.SiteVLANs {
		if v.ID > 0 {
			want[v.ID] = true
		}
	}
	// VLAN 1 always exists and is never "created".
	delete(want, 1)
	var out []int
	var id int
	for id = range want {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

func vlanNameFor(id int, plan PortPlan) string {
	var v driver.VLAN
	for _, v = range plan.SiteVLANs {
		if v.ID == id && strings.TrimSpace(v.Name) != "" {
			return v.Name
		}
	}
	return fmt.Sprintf("VLAN%04d", id)
}

func sortedPortNames(m map[string]PortState) []string {
	var out []string
	var k string
	for k = range m {
		out = append(out, k)
	}
	driver.SortPortNames(out, splitStandard)
	return out
}

func samePort(a, b PortState) bool {
	if !strings.EqualFold(orAccess(a.Mode), orAccess(b.Mode)) {
		return false
	}
	if a.PVID != b.PVID {
		return false
	}
	return sameInts(a.Tagged, b.Tagged)
}

func orAccess(mode string) string {
	if mode == "" {
		return "access"
	}
	return mode
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	var x = append([]int{}, a...)
	var y = append([]int{}, b...)
	sort.Ints(x)
	sort.Ints(y)
	var i int
	for i = range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// carriesMgmt: does this port state carry the management VLAN?
func carriesMgmt(ps PortState, mgmt int) bool {
	if ps.PVID == mgmt {
		return true
	}
	var id int
	for _, id = range ps.Tagged {
		if id == mgmt {
			return true
		}
	}
	return false
}

// mgmtGuardBlocks reports whether the management-VLAN guardrail refuses this
// write.
//
// LINK-GATED, like every other connector's version of this guardrail (cf.
// cisco/ios, instanton, fs/s3900): a port with no cable cannot carry the
// session that would be cut, so an unlinked port is NEVER blocked here.
// Refusing it anyway only stops provisioning a device before anything is
// plugged into it — exactly the case adoption needs to work, and the
// opposite of what the guardrail exists to protect.
func mgmtGuardBlocks(have, want PortState, mgmt int, linked, force bool) bool {
	if force || mgmt <= 0 || !linked {
		return false
	}
	return carriesMgmt(have, mgmt) && !carriesMgmt(want, mgmt)
}

func describePort(ps PortState) string {
	if strings.EqualFold(ps.Mode, "trunk") {
		return fmt.Sprintf("trunk, native %d, tagged %s", ps.PVID, compactVlanList(ps.Tagged))
	}
	return fmt.Sprintf("access, pvid %d", ps.PVID)
}

// compactVlanList turns ids into the "1-10,99" form the CLI accepts.
func compactVlanList(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	var s = append([]int{}, ids...)
	sort.Ints(s)
	var uniq []int
	var i int
	for i = range s {
		if i == 0 || s[i] != s[i-1] {
			uniq = append(uniq, s[i])
		}
	}
	var parts []string
	var start = uniq[0]
	var prev = uniq[0]
	var flush = func() {
		if start == prev {
			parts = append(parts, fmt.Sprintf("%d", start))
			return
		}
		parts = append(parts, fmt.Sprintf("%d-%d", start, prev))
	}
	for i = 1; i < len(uniq); i++ {
		if uniq[i] == prev+1 {
			prev = uniq[i]
			continue
		}
		flush()
		start, prev = uniq[i], uniq[i]
	}
	flush()
	return strings.Join(parts, ",")
}

// Persist saves the running configuration to flash.
//
// "write" is the verb on this platform (captured: it answers "OK!"), NOT
// "copy running-config startup-config".
func (c *Client) Persist(ctx context.Context) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	var out string
	out, err = c.cli.run(ctx, "write", saveTimeout)
	if err != nil {
		c.step(false, "write refused", firstLine(err.Error()))
		return fmt.Errorf("write on %s: %w", c.host, err)
	}
	if !reSaveOK.MatchString(out) {
		// The prompt came back with no confirmation. Say so rather than
		// claiming a save that may not have happened.
		c.step(false, "write returned no confirmation",
			"the prompt came back without \"OK!\" — verify the configuration survives a reboot")
		return fmt.Errorf("write on %s: no confirmation in the answer", c.host)
	}
	c.step(true, "configuration saved", "\"write\" — OK!")
	return nil
}

var reSaveOK = regexp.MustCompile(`(?i)\bOK!?\b|\[OK\]|success`)
