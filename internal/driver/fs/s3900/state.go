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

	// A TRUNCATED CONFIG IS REFUSED, NOT PARSED, for the reason spelled out on the
	// S3400 connector (fs/s3400/state.go): parseRunningConfig cannot fail on a
	// prefix — it reports the ports it reached and is silent about the rest, so
	// "23 of 52" leaves here as a complete-looking answer, gets stored with
	// `observed_at = now()`, and every socket after the cut reads as "the device
	// no longer has this port". The old test was "empty", which is the single
	// truncation this parser could not survive being handed.
	//
	// Keyed on [isTruncated] — "the output is a prefix" — and not on the error
	// alone, because an in-band CLI refusal arrives on a COMPLETE read and this
	// connector is entitled to parse around one. Strict superset of the old test.
	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && (runOut == "" || isTruncated(err)) {
		c.step(false, "show run failed", firstLine(err.Error()))
		out.Notes = []string{"INCOMPLETE READ: " + firstLine(err.Error())}
		return out, err
	}
	c.step(true, "running-config read", fmt.Sprintf("%d characters", len(runOut)))
	var rc = parseRunningConfig(runOut, c.host)

	// Link aggregates, from the same run output plus one status query per
	// discovered group. Nil never happens here (parse is pure); an empty list
	// on a device without groups is the honest answer.
	out.LAGs = c.readLAGs(ctx, runOut)

	// The device's OWN name rides this read and costs nothing more: it is in the
	// configuration and in no cheaper place, and ReadIdentity is forbidden to
	// fetch it itself (cf. identityread.go). This is the ONLY thing that makes an
	// S3900 observable by the hostname reconciliation — it serves no LLDP system
	// name. A configuration with no `hostname` line records the empty string,
	// which reads as "nothing observed", not as "named """.
	c.noteSysName(rc.Hostname)

	out.VLANs = rc.VLANs
	out.Ports = rc.Ports
	out.MgmtVLAN = rc.MgmtVLAN
	// HOW the management SVI was chosen, or why it could not be — said only when
	// the choice was not mechanical, which is the only time it is news. Without
	// this line the whole of [resolveMgmtSVI]'s reasoning stops at the parser:
	// State carries MgmtVLAN and nothing that explains a 0, so "management VLAN
	// unknown" would reach the operator as an apparent failed read on a switch
	// whose configuration they can see has four addressed SVIs.
	out.Notes = append(out.Notes, rc.MgmtSVINotes...)

	var briefOut string
	// Same treatment, and it costs nothing here: this read is ALREADY fatal to
	// ReadState when it comes back empty, so refusing a prefix widens an existing
	// refusal rather than inventing one. A short "show interfaces brief" would
	// otherwise mark every port after the cut as "no link" — the map is built
	// from the LinkUp rows only — and that is a claim about the whole faceplate
	// drawn from part of a read.
	briefOut, err = c.cli.run(ctx, "show interfaces brief", cmdTimeout)
	if err != nil && (briefOut == "" || isTruncated(err)) {
		c.step(false, "show interfaces brief failed", firstLine(err.Error()))
		out.Notes = append(out.Notes,
			"links not read ("+firstLine(err.Error())+") — apply guardrails will treat every port as possibly active")
		return out, err
	}
	var ports = parseInterfacesBrief(briefOut)
	var p briefPort
	for _, p = range ports {
		// ONE ENTRY PER PORT WITH A LINK, the [driver.LinksFromUp] convention: a
		// port that is down is simply absent. So [driver.LinkDisabled] never
		// appears in this map even though the Admin column is read and, since
		// 2026-08-10, its "Down" is captured (portadmin_live_test.go) — a shut
		// S3900 port therefore reads "no link" here, where an S3400 reads
		// "disabled". [Client.ApplyPortAdmin] does NOT go through this map: it
		// reads [briefPort.AdminUp] straight off "show interfaces brief", so
		// its guardrail and its verification keep the distinction this map drops.
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
	var rc = parseRunningConfig(runOut, c.host)
	// An empty Interface used to have ONE cause — no addressed SVI at all — and
	// now has two: that, or several candidates and nothing to tell them apart.
	// [runConfig.MgmtSVINotes] already carries the second case into Mgmt.Notes, so
	// this sentence must not also assert the first one, or the operator is told
	// "no SVI carries an address" about a configuration holding four of them.
	if rc.Mgmt.Interface == "" && len(rc.MgmtSVINotes) == 0 {
		var why = "no SVI carries an address in the running-config — administration may " +
			"come through the out-of-band craft port"
		if rc.MgmtOOB != "" {
			// The one case where the absence has an obvious explanation, and saying
			// it saves an operator from concluding the read is broken.
			why = "no SVI carries an address in the running-config. \"interface craft\" " +
				"does carry " + rc.MgmtOOB + ", but that is the OUT-OF-BAND port: " +
				"administration is arriving there, on a cable outside the switching " +
				"fabric, and this connector neither reads nor writes management through it"
		}
		rc.Mgmt.Notes = append(rc.Mgmt.Notes, why)
	}
	return rc.Mgmt, nil
}
