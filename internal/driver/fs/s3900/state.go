package s3900

// State reading — the "Read" step of the reconciliation pipeline.
//
// The AUTHORITATIVE reading of port modes is the running-config: 1.7's
// "show vlan all" lists members without distinguishing tagged from untagged
// (lab capture: "Eth1/25(S)" in both cases), whereas the interface blocks
// carry the intent exactly ("switchport mode trunk", "switchport trunk
// allowed vlan add 99"). Links come from "show interfaces brief" — that's
// what feeds apply's guardrails.

import (
	"context"
	"fmt"
	"time"

	"meshcraft/internal/driver"
)

// showRunTimeout: "Building running configuration. Please wait..." takes
// several seconds on 1.7.0 (captured over serial).
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

	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && runOut == "" {
		c.step(false, "show run failed", firstLine(err.Error()))
		out.Notes = []string{"INCOMPLETE READ: " + firstLine(err.Error())}
		return out, err
	}
	c.step(true, "running-config read", fmt.Sprintf("%d characters", len(runOut)))
	var rc = parseRunningConfig(runOut)

	out.VLANs = rc.VLANs
	out.Ports = rc.Ports
	out.MgmtVLAN = rc.MgmtVLAN

	var briefOut string
	briefOut, err = c.cli.run(ctx, "show interfaces brief", cmdTimeout)
	if err != nil && briefOut == "" {
		c.step(false, "show interfaces brief failed", firstLine(err.Error()))
		out.Notes = append(out.Notes,
			"links not read ("+firstLine(err.Error())+") — apply guardrails will treat every port as possibly active")
		return out, err
	}
	var ports = parseInterfacesBrief(briefOut)
	var p briefPort
	for _, p = range ports {
		if p.LinkUp {
			out.Links[p.Name] = driver.LinkUpState
		}
		// A port present on the faceplate but absent from the running-config
		// (block never written) is at its factory default: access VLAN 1.
		var _, known = out.Ports[p.Name]
		if !known {
			out.Ports[p.Name] = PortState{Mode: "access", PVID: 1}
		}
	}
	c.step(true, "interfaces read",
		fmt.Sprintf("%d ports, %d with link", len(ports), len(out.Links)))

	if c.dialect != dialectFSOS17 {
		out.Notes = append(out.Notes,
			"CLI dialect not recognized: this read is best-effort and writes are refused "+
				"until the post-upgrade CLI is surveyed")
	}
	return out, nil
}

// ReadMgmtAddressing reads the administration addressing declared by the device.
func (c *Client) ReadMgmtAddressing(ctx context.Context) (driver.MgmtAddrState, error) {
	if c.cli == nil {
		return driver.MgmtAddrState{}, fmt.Errorf("no open session on this access")
	}
	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && runOut == "" {
		return driver.MgmtAddrState{}, err
	}
	var rc = parseRunningConfig(runOut)
	if rc.Mgmt.Interface == "" {
		rc.Mgmt.Notes = append(rc.Mgmt.Notes,
			"no SVI carries an address in the running-config — administration may come through the out-of-band craft port")
	}
	return rc.Mgmt, nil
}
