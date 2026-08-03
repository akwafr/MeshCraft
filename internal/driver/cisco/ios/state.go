package ios

// State reading — the "Read" step of the reconciliation pipeline.
//
// THREE reads and not one, because on IOS no single command answers the whole
// question and picking the wrong source is how a connector invents drift:
//
//   - `show running-config` is authoritative for PORT MODES. It carries the
//     intent exactly ("switchport mode trunk", "switchport trunk allowed vlan
//     1,99"), including the ABSENCE of an allowed-vlan line, which means "all"
//     and not "none" (cf. runConfig.AllowedAll);
//   - `show vlan brief` is authoritative for the VLAN LIST. The
//     running-config omits a VLAN still carrying its default name (VLAN0100)
//     entirely, so reading the list from there would report existing VLANs as
//     missing and re-create them on every apply;
//   - `show interfaces status` is authoritative for LINKS, and that is what
//     feeds apply's guardrails — which port must not be touched because
//     something is plugged into it.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// showRunTimeout: "Building configuration..." takes several seconds on a
// Catalyst, longer on a switch carrying a large VLAN database.
const showRunTimeout = 30 * time.Second

// ReadState re-reads the device. READ ONLY, replayable.
func (c *Client) ReadState(ctx context.Context) (State, error) {
	var out = State{
		Ports: map[string]PortState{},
		Links: map[string]driver.LinkState{},
	}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}

	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		c.step(false, "show running-config failed", firstLine(err.Error()))
		out.Notes = []string{"INCOMPLETE READ: " + firstLine(err.Error())}
		return out, err
	}
	c.step(true, "running-config read", fmt.Sprintf("%d characters", len(runOut)))
	var rc = parseRunningConfig(runOut)
	out.Ports = rc.Ports
	out.MgmtVLAN = rc.MgmtVLAN

	var vlanOut string
	vlanOut, err = c.cli.run(ctx, "show vlan brief", cmdTimeout)
	if err != nil && vlanOut == "" {
		c.step(false, "show vlan brief failed", firstLine(err.Error()))
		out.Notes = append(out.Notes,
			"VLAN list not read ("+firstLine(err.Error())+"): missing VLANs cannot be told "+
				"from existing ones, so no VLAN will be created")
	} else {
		out.VLANs = parseVlanBrief(vlanOut)
		c.step(true, "VLAN database read", fmt.Sprintf("%d VLAN(s)", len(out.VLANs)))
	}

	var statusOut string
	statusOut, err = c.cli.run(ctx, "show interfaces status", cmdTimeout)
	if err != nil && statusOut == "" {
		c.step(false, "show interfaces status failed", firstLine(err.Error()))
		out.Notes = append(out.Notes,
			"links not read ("+firstLine(err.Error())+") — apply guardrails will treat every "+
				"port as possibly active")
		return out, err
	}
	var ports = parseInterfacesStatus(statusOut)
	var p statusPort
	for _, p = range ports {
		out.Links[p.Name] = iosLinkState(p.Status)
		// A port present on the faceplate but absent from the running-config
		// (block never written) is at its factory default: access VLAN 1.
		var _, known = out.Ports[p.Name]
		if !known {
			out.Ports[p.Name] = PortState{Mode: "access", PVID: 1}
		}
	}
	c.step(true, "interfaces read",
		fmt.Sprintf("%d ports, %d with link", len(ports), len(out.LinkUp())))

	// A port the SWITCH shut by itself reads exactly like an empty one, and the
	// operator has no way to tell the two apart from the port drawing. Ask for
	// the reason — one extra command, and only when there is something to ask
	// about.
	out.Notes = append(out.Notes, c.errDisabledNotes(ctx, ports)...)

	// Facts the operator needs and that no other screen carries.
	if !rc.LLDPRun {
		out.Notes = append(out.Notes,
			"« lldp run » is NOT configured: this switch serves an empty LLDP-MIB, so the "+
				"neighbour view will stay empty. This connector does not turn it on")
	}
	var name string
	for name = range rc.AllowedAll {
		out.Notes = append(out.Notes,
			"trunk "+name+" carries no explicit « switchport trunk allowed vlan » line: on "+
				"IOS that means ALL VLANs, not none")
	}
	if c.dialect != dialectIOS {
		out.Notes = append(out.Notes,
			"CLI dialect not recognized: this read is best-effort and writes are refused")
	}
	return out, nil
}

// errDisabledNotes explains every port the switch shut by itself.
//
// Worth a second command because the plain status table does not carry the
// REASON, and the reason is the whole of what the operator needs: a
// gbic-invalid port is a refused transceiver (nothing wrong with the link), a
// bpduguard port is a loop someone just created, and a port-security port is a
// device that moved. All three read as "no link" on the drawing.
//
// Nothing here is an error: a read that could not obtain the reasons returns
// what it has. Ports stay reported as down either way — only the explanation
// is missing.
func (c *Client) errDisabledNotes(ctx context.Context, ports []statusPort) []string {
	var shut []statusPort
	var p statusPort
	for _, p = range ports {
		if p.errDisabled() {
			shut = append(shut, p)
		}
	}
	if len(shut) == 0 {
		return nil
	}
	var reasons = map[string]string{}
	var out, err = c.cli.run(ctx, "show interfaces status err-disabled", cmdTimeout)
	if err == nil || out != "" {
		reasons = parseErrDisabled(out)
	}

	var notes []string
	var gbic []string
	for _, p = range shut {
		var why = reasons[p.Name]
		if why == ErrDisableGBIC {
			gbic = append(gbic, p.Name)
			continue
		}
		if why == "" {
			notes = append(notes, p.Name+" was shut down BY THE SWITCH (err-disabled) and it "+
				"did not report a reason — « show interfaces status err-disabled » on the "+
				"device gives it")
			continue
		}
		notes = append(notes, p.Name+" was shut down BY THE SWITCH (err-disabled), reason « "+
			why+" » — this is not an empty port, and clearing it is an operator decision")
	}
	if len(gbic) > 0 {
		notes = append(notes, strings.Join(gbic, ", ")+": "+UnsupportedTransceiverNote)
		c.step(true, "transceiver refused by the switch on "+strings.Join(gbic, ", "),
			"reason gbic-invalid. Provisioning sets "+
				strings.Join(UnsupportedTransceiverCmds(), " ; ")+" — finding a port in this "+
				"state means those lines are absent from the running configuration "+
				"(a factory reset removes them). Re-provisioning, or applying them by hand "+
				"and bouncing the port, clears it")
	}
	return notes
}

// ReadMgmtAddressing reads the administration addressing the device declares.
func (c *Client) ReadMgmtAddressing(ctx context.Context) (driver.MgmtAddrState, error) {
	if c.cli == nil {
		return driver.MgmtAddrState{}, fmt.Errorf("no open session on this access")
	}
	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return driver.MgmtAddrState{}, err
	}
	var rc = parseRunningConfig(runOut)
	if rc.Mgmt.Interface == "" {
		rc.Mgmt.Notes = append(rc.Mgmt.Notes,
			"no SVI carries an address that is administratively up — administration may be "+
				"coming through the console, or through an SVI left shut down")
	}
	return rc.Mgmt, nil
}

// iosLinkState maps « show interfaces status » Status words onto the shared
// vocabulary. The words are the ones the parser keeps whole for exactly this
// (cf. statusPort.Status).
func iosLinkState(status string) driver.LinkState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "connected":
		return driver.LinkUpState
	case "notconnect", "inactive":
		return driver.LinkUnplugged
	case "disabled":
		return driver.LinkDisabled
	case "err-disabled", "errdisable":
		return driver.LinkErrDisabled
	}
	return driver.LinkDown
}
