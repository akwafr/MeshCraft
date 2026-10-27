package edgeswitch

// Writes: VLAN membership and the session's own secret.
//
// The command forms come from the CLI reference; the MODES they live in were
// confirmed on hardware. Nothing in this file has been run end to end yet, so
// MgmtCapabilities keeps ApplySameVLAN false and the UI does not offer the
// gestures that could make a device mute.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"meshcraft/internal/driver"
)

// ApplyPorts writes the VLAN plan.
//
// Order is not negotiable: a VLAN must EXIST before a port can be put in it,
// and the VLAN database is entered from PRIVILEGED EXEC on this platform —
// "vlan database", not "configure". The port work is Global Config.
func (c *Client) ApplyPorts(ctx context.Context, plan PortPlan) (ApplyResult, error) {
	var res ApplyResult
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}

	var before, readErr = c.ReadState(ctx)
	if readErr != nil {
		return res, fmt.Errorf("reading the state before applying: %w", readErr)
	}
	var existing = map[int]bool{}
	var v driver.VLAN
	for _, v = range before.VLANs {
		existing[v.ID] = true
	}

	// 1) Create the missing VLANs, in the VLAN database.
	var needed = neededVLANs(plan)
	var toCreate []int
	var id int
	for _, id = range needed {
		if !existing[id] {
			toCreate = append(toCreate, id)
		}
	}
	if len(toCreate) > 0 {
		var cmds = []string{"vlan database"}
		for _, id = range toCreate {
			cmds = append(cmds, fmt.Sprintf("vlan %d", id))
			var name = vlanNameFor(id, plan)
			if name != "" {
				cmds = append(cmds, fmt.Sprintf("vlan name %d %s", id, name))
			}
		}
		cmds = append(cmds, "exit")
		if err = c.runBlock(ctx, cmds); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("vlan database: %v", err))
		} else {
			res.VLANsCreated = append(res.VLANsCreated, toCreate...)
			c.step(true, fmt.Sprintf("%d VLAN(s) created", len(toCreate)), fmt.Sprint(toCreate))
		}
	}

	// 2) The ports, in Global Config.
	var names = sortedNames(plan.Desired)
	var name string
	var portCmds = []string{"configure"}
	var wrote []string
	for _, name = range names {
		var want = plan.Desired[name]
		var have, known = before.Ports[name]
		if known && samePort(have, want) {
			continue
		}
		var mgmt = plan.MgmtVLAN
		if mgmtGuardBlocks(have, want, mgmt, before.HasLink(name), plan.Force) {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: would stop carrying the management VLAN %d — refused without force", name, mgmt))
			continue
		}
		portCmds = append(portCmds, PortCmds(cliPortName(name), want.PVID, want.Tagged)...)
		var stale = staleMembership(have, want)
		if len(stale) > 0 {
			portCmds = append(portCmds, RemoveTaggedCmds(cliPortName(name), stale)...)
		}
		wrote = append(wrote, name)
	}
	if len(wrote) > 0 {
		portCmds = append(portCmds, "exit")
		if err = c.runBlock(ctx, portCmds); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("ports: %v", err))
		} else {
			res.PortsWritten = wrote
			c.step(true, fmt.Sprintf("%d port(s) written", len(wrote)), strings.Join(wrote, " "))
		}
	}

	if len(res.PortsWritten) > 0 || len(res.VLANsCreated) > 0 {
		res.Notes = append(res.Notes,
			"applied to the running configuration; \"write memory confirm\" is what makes "+
				"it survive a reboot")
	}
	return res, nil
}

// runBlock sends a list of commands, each waiting for its own prompt.
func (c *Client) runBlock(ctx context.Context, cmds []string) error {
	return runBlockWith(ctx, c.cli.run, cmds)
}

// runBlockWith is runBlock against a [cmdRunner] rather than against the live
// shell — the seam accounts.go already uses, so a write whose REFUSAL has to be
// tested (the last-account guard) can be replayed without hardware. One
// implementation, so the replayed path is the one that runs.
func runBlockWith(ctx context.Context, run cmdRunner, cmds []string) error {
	var failures []string
	var cmd string
	for _, cmd = range cmds {
		var _, err = run(ctx, cmd, cmdTimeout)
		if err != nil {
			failures = append(failures, fmt.Sprintf("\"%s\" : %v", cmd, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, " ; "))
	}
	return nil
}

// Persist saves the running configuration.
//
// "confirm" is not optional here: the plain form asks a question, and a
// session that does not answer it hangs.
func (c *Client) Persist(ctx context.Context) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	var _, rerr = c.cli.run(ctx, "write memory confirm", saveTimeout)
	if rerr != nil {
		c.step(false, "write memory refused", firstLine(rerr.Error()))
		return fmt.Errorf("write memory on %s: %w", c.host, rerr)
	}
	c.step(true, "configuration saved", "\"write memory confirm\"")
	return nil
}

// SetSecret changes the password of the account this session uses.
func (c *Client) SetSecret(ctx context.Context, old, secret string) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	if strings.ContainsAny(secret, " \t\r\n") {
		return fmt.Errorf("secret rejected: this CLI cannot carry whitespace in a password")
	}
	err = c.runBlock(ctx, []string{
		"configure",
		fmt.Sprintf("username %s password %s level 15", c.User, secret),
		"exit",
	})
	if err != nil {
		c.step(false, "secret not changed", firstLine(err.Error()))
		return fmt.Errorf("changing the secret of %s on %s: %w", c.User, c.host, err)
	}
	c.step(true, "secret changed for "+c.User,
		"note that \"enable\" on this platform asks for a password too — it takes the "+
			"login password")
	return nil
}

// cliPortName turns a standard name back into the platform's "0/N".
func cliPortName(standard string) string {
	var n, _, ok = splitStandard(standard)
	if !ok {
		return standard
	}
	return fmt.Sprintf("0/%d", n)
}

func neededVLANs(plan PortPlan) []int {
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
	delete(want, 1) // VLAN 1 is reserved and always exists
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
			return sanitizeVlanName(v.Name)
		}
	}
	return ""
}

func sortedNames(m map[string]PortState) []string {
	var out []string
	var k string
	for k = range m {
		out = append(out, k)
	}
	driver.SortPortNames(out, splitStandard)
	return out
}

func samePort(a, b PortState) bool {
	if a.PVID != b.PVID {
		return false
	}
	return sameInts(a.Tagged, b.Tagged)
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

func carries(ps PortState, vlan int) bool {
	if ps.PVID == vlan {
		return true
	}
	var id int
	for _, id = range ps.Tagged {
		if id == vlan {
			return true
		}
	}
	return false
}

// staleMembership returns the VLANs `have` carries — natively or tagged —
// that `want` no longer carries AT ALL: the participation this write must
// actively EXCLUDE, or it survives as a lingering member forever.
//
// CAPTURED live 2026-08-05, on a fleet of ports that had been provisioned
// trunk-tagged into the management VLAN at adoption (cf. MgmtPushCmds's
// "every port becomes a trunk carrying every VLAN") and were since desired
// as plain access, VLAN unrelated: they kept reading back "trunk, tagged 99"
// through every apply, forever — PortCmds (its own comment says so) only
// ever ADDS membership, and nothing anywhere in this connector ever told the
// device to drop what a previous state (adoption, or an earlier plan) had
// put there. The site's true intent was applied on top of a device that
// still remembered everything it had ever been asked to carry.
//
// The OLD native VLAN is included too, not just old tags: a pvid change
// alone does not, on this platform, retire the port's PARTICIPATION in its
// former native VLAN, and leaving it would make the port an undeclared
// member of a VLAN nothing asked it to keep.
func staleMembership(have, want PortState) []int {
	var wantSet = map[int]bool{}
	if want.PVID > 0 {
		wantSet[want.PVID] = true
	}
	var id int
	for _, id = range want.Tagged {
		wantSet[id] = true
	}
	var seen = map[int]bool{}
	var out []int
	if have.PVID > 0 && !wantSet[have.PVID] {
		seen[have.PVID] = true
		out = append(out, have.PVID)
	}
	for _, id = range have.Tagged {
		if !wantSet[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Ints(out)
	return out
}

// RemoveTaggedCmds returns the Interface Config lines that drop a port's
// participation in the given VLANs.
//
// "vlan participation exclude N" is the documented inverse of "vlan
// participation include N" (cf. PortCmds's own comment on the three-command
// membership model): it removes the port from that VLAN altogether, tagged
// or not — the command this connector never sent before staleMembership.
//
// A SEPARATE Interface Config block from PortCmds's, sent AFTER it in
// ApplyPorts: membership is only ever ADDED before anything is TAKEN AWAY,
// the same ordering discipline applied elsewhere in this codebase (cf.
// instanton's ApplyPorts, "SET BEFORE DELETE") — a port must never be
// observed, even momentarily, with less membership than either its old or
// its new state alone would give it.
func RemoveTaggedCmds(port string, ids []int) []string {
	if len(ids) == 0 {
		return nil
	}
	var out = []string{"interface " + port}
	var id int
	for _, id = range ids {
		out = append(out, fmt.Sprintf("vlan participation exclude %d", id))
	}
	out = append(out, "exit")
	return out
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
	return carries(have, mgmt) && !carries(want, mgmt)
}
