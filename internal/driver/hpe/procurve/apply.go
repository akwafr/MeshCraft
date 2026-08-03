package procurve

// ApplyPorts — VLAN membership, translated from meshcraft's port-centric intent
// into this platform's VLAN-centric commands.
//
// # The translation, and the rule that makes it safe
//
// meshcraft says "port 5 is access VLAN 42". This CLI has no such sentence. It
// says "VLAN 42 has port 5 untagged", and a port is untagged in AT MOST ONE
// VLAN — the model cannot express anything else. So a PVID change is a REMOVAL
// followed by an ADDITION, and the order is not free: removing first leaves the
// port briefly in no untagged VLAN, adding first is refused or silently moves it
// depending on revision.
//
// The order chosen is ADD THEN REMOVE, and only removing what the add did not
// already take. On this platform assigning « untagged » moves the port out of its
// previous untagged VLAN by itself, so the removal is a reconciliation of what is
// left over, not the mechanism. Doing it the other way round would open a window
// where an uplink belongs to nothing.
//
// # Command forms are CAPTURED, from the device's own configuration
//
// « vlan N », « untagged <ports> », « tagged <ports> », « no untagged <ports> »,
// « no tagged <ports> » and the compact range grammar all appear verbatim in the
// running-config of the reference unit (2610-48, R.11.121, 2026-08-02). On this
// platform the configuration file IS the command language — what it prints back
// is what it accepts — so these are captured forms rather than deduced ones.
//
// « write memory » is the persist step: this range applies to the running
// configuration immediately and loses it at reboot until saved
// ([driver.CommitApplyThenSave]).

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// ApplyPorts writes VLAN membership, then persists.
//
// THE UPLINK GUARD comes first and is not negotiable: this range has no second
// path. Dropping the management VLAN off the port that carries it leaves the
// serial console as the only way back, and on a switch in a rack that is a
// journey. The management VLAN is therefore never removed from a port, and the
// refusal is reported rather than worked around.
func (c *Client) ApplyPorts(ctx context.Context, plan PortPlan) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if c.cli == nil {
		return out, errors.New("no open session on this access")
	}

	// A write NEVER acts on a cached state: re-read first.
	var current, err = c.ReadState(ctx)
	if err != nil && len(current.Ports) == 0 {
		return out, fmt.Errorf("state re-read before writing: %v", err)
	}
	var mgmt = current.MgmtVLAN
	if plan.MgmtVLAN > 0 && mgmt == 0 {
		mgmt = plan.MgmtVLAN
	}

	// 1. VLANs that do not exist yet. A port cannot join an absent VLAN, and on
	//    this platform creating one is the same stanza that assigns membership.
	var have = map[int]bool{}
	var v driver.VLAN
	for _, v = range current.VLANs {
		have[v.ID] = true
	}
	var missing = missingVLANs(have, plan)
	if len(missing) > 0 {
		var cmds []string
		var m driver.VLAN
		for _, m = range missing {
			cmds = append(cmds, "vlan "+strconv.Itoa(m.ID))
			if m.Name != "" {
				cmds = append(cmds, "name \""+sanitizeVlanName(m.Name)+"\"")
			}
			cmds = append(cmds, "exit")
		}
		if err = c.runConfigBlock(ctx, cmds); err != nil {
			return out, fmt.Errorf("VLAN creation: %v", err)
		}
		for _, m = range missing {
			out.VLANsCreated = append(out.VLANsCreated, m.ID)
		}
		c.step(true, fmt.Sprintf("%d VLAN(s) created", len(missing)))
	}

	// 2. Membership, gathered PER VLAN because that is how it is written.
	var addUntagged = map[int][]int{}
	var addTagged = map[int][]int{}
	var delTagged = map[int][]int{}

	var names []string
	var name string
	for name = range plan.Desired {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name = range names {
		var want = plan.Desired[name]
		if want.Mode == "" || want.Mode == "libre" {
			continue // unmanaged: left exactly as it is
		}
		var num, ok = portNumber(name)
		if !ok {
			out.Skipped = append(out.Skipped, name+
				" not modified: this name does not translate to a port number on this platform")
			continue
		}
		var now = current.Ports[name]

		if want.PVID > 0 && want.PVID != now.PVID {
			if now.PVID == mgmt && mgmt > 0 {
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s not modified: it is untagged in the MANAGEMENT VLAN %d, and this "+
						"range has no second path — moving it would cut administration",
					name, mgmt))
				continue
			}
			addUntagged[want.PVID] = append(addUntagged[want.PVID], num)
		}

		var wantTag = map[int]bool{}
		var t int
		for _, t = range want.Tagged {
			wantTag[t] = true
			if !containsInt(now.Tagged, t) {
				addTagged[t] = append(addTagged[t], num)
			}
		}
		if !want.TaggedAtLeast {
			for _, t = range now.Tagged {
				if wantTag[t] {
					continue
				}
				if t == mgmt && mgmt > 0 {
					out.Skipped = append(out.Skipped, fmt.Sprintf(
						"%s: management VLAN %d left tagged on this port — removing it is "+
							"how this switch is lost", name, mgmt))
					continue
				}
				delTagged[t] = append(delTagged[t], num)
			}
		}
	}

	// 3. Emit, VLAN by VLAN. Additions before removals (cf. the header).
	var cmds []string
	cmds = append(cmds, membershipCmds(addUntagged, "untagged")...)
	cmds = append(cmds, membershipCmds(addTagged, "tagged")...)
	cmds = append(cmds, membershipCmds(delTagged, "no tagged")...)
	if len(cmds) == 0 {
		c.step(true, "nothing to write: the device already carries the intent")
		return out, nil
	}
	if err = c.runConfigBlock(ctx, cmds); err != nil {
		return out, fmt.Errorf("VLAN membership: %v", err)
	}
	c.step(true, fmt.Sprintf("membership written (%d command(s))", len(cmds)))

	// 4. Persist. A write a reboot would undo is worse than a refused one: it was
	//    observed as successful.
	if err = c.persist(ctx); err != nil {
		out.Notes = append(out.Notes,
			"configuration applied but NOT saved: it will be lost at the next reboot ("+
				firstLine(err.Error())+")")
		return out, nil
	}
	c.step(true, "configuration persisted", "write memory")
	return out, nil
}

// membershipCmds renders one stanza per VLAN, with the ports in this platform's
// compact range grammar.
func membershipCmds(byVLAN map[int][]int, verb string) []string {
	if len(byVLAN) == 0 {
		return nil
	}
	var ids []int
	var id int
	for id = range byVLAN {
		ids = append(ids, id)
	}
	sortInts(ids)
	var cmds []string
	for _, id = range ids {
		cmds = append(cmds,
			"vlan "+strconv.Itoa(id),
			verb+" "+formatPortRange(byVLAN[id]),
			"exit")
	}
	return cmds
}

// missingVLANs: what the plan needs and the device does not have.
func missingVLANs(have map[int]bool, plan PortPlan) []driver.VLAN {
	var want = map[int]string{}
	var v driver.VLAN
	for _, v = range plan.SiteVLANs {
		want[v.ID] = v.Name
	}
	var p driver.PortState
	for _, p = range plan.Desired {
		if p.PVID > 0 {
			if _, seen := want[p.PVID]; !seen {
				want[p.PVID] = ""
			}
		}
		var t int
		for _, t = range p.Tagged {
			if _, seen := want[t]; !seen {
				want[t] = ""
			}
		}
	}
	var out []driver.VLAN
	var id int
	var name string
	for id, name = range want {
		if id < 1 || id > 4094 || have[id] {
			continue
		}
		out = append(out, driver.VLAN{ID: id, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sanitizeVlanName keeps what this CLI accepts inside quotes. A name is
// cosmetic; a name that breaks the command is not.
func sanitizeVlanName(s string) string {
	var b strings.Builder
	var i int
	for i = 0; i < len(s) && b.Len() < 32; i++ {
		var ch = s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9', ch == '_', ch == '-':
			b.WriteByte(ch)
		case ch == ' ':
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "VLAN"
	}
	return b.String()
}

func containsInt(list []int, want int) bool {
	var v int
	for _, v = range list {
		if v == want {
			return true
		}
	}
	return false
}

// runConfigBlock enters configuration mode, runs the commands, and always
// leaves it.
//
// « exit » is sent unconditionally at the end, including after a failure: a
// session abandoned inside a VLAN sub-mode makes the NEXT gesture run its first
// command in a context it does not expect, and on this platform that context
// silently accepts membership commands for the wrong VLAN.
func (c *Client) runConfigBlock(ctx context.Context, cmds []string) error {
	var all = append([]string{"configure terminal"}, cmds...)
	var failure error
	var cmd string
	for _, cmd = range all {
		var text, err = c.cli.run(ctx, cmd, cmdTimeout)
		if err != nil {
			failure = fmt.Errorf("%s: %v", cmd, err)
			break
		}
		// This CLI reports refusals in the OUTPUT, not through an exit status: the
		// "%" line is the only signal that a command did not take.
		var bad = cliRefusal(text)
		if bad != "" {
			failure = fmt.Errorf("%s: %s", cmd, bad)
			break
		}
	}
	// « exit » is sent even after a failure: a session left inside a VLAN sub-mode
	// makes the NEXT gesture run its first command in a context it does not
	// expect, and that context silently accepts membership for the wrong VLAN.
	var _, exitErr = c.cli.run(ctx, "exit", cmdTimeout)
	if failure != nil {
		return failure
	}
	return exitErr
}

// persist writes the running configuration to flash.
func (c *Client) persist(ctx context.Context) error {
	var text, err = c.cli.run(ctx, "write memory", saveTimeout)
	if err != nil {
		return err
	}
	var bad = cliRefusal(text)
	if bad != "" {
		return errors.New(bad)
	}
	return nil
}

// cliRefusal finds this CLI's refusal shape in a transcript, and returns it
// verbatim.
//
// CAPTURED on the reference unit, where an unsupported keyword produced:
//
//	vlan 4001 name X media ethernet
//	                  ^
//	% Invalid input detected at '^' marker.
//
// Matching the "% " line and NOT the caret: the caret's position is meaningful
// to a human reading the transcript and useless as a pattern.
func cliRefusal(text string) string {
	var line string
	for _, line = range strings.Split(text, "\n") {
		var t = strings.TrimSpace(line)
		if strings.HasPrefix(t, "%") && len(t) > 1 {
			return t
		}
		// The 2600 CLI also refuses with a bare sentence on some commands.
		if strings.HasPrefix(t, "Invalid input") ||
			strings.HasPrefix(t, "Incomplete input") ||
			strings.HasPrefix(t, "Unrecognized command") {
			return t
		}
	}
	return ""
}
