package eos

// State reading — the "Read" step of the reconciliation pipeline.
//
// THREE reads and not one, because on EOS no single command answers the whole
// question and picking the wrong source is how a connector invents drift:
//
//   - `show running-config` is authoritative for PORT MODES. It carries the
//     intent exactly ("switchport mode trunk", "switchport trunk allowed vlan
//     1,99"), including the ABSENCE of an allowed-vlan line, which means "all"
//     and not "none" (cf. runConfig.AllowedAll);
//   - `show vlan` is authoritative for the VLAN LIST. The running configuration
//     omits a VLAN still carrying its default name (VLAN0100) entirely, so
//     reading the list from there would report existing VLANs as missing and
//     re-create them on every apply;
//   - `show interfaces status` is authoritative for LINKS, and that is what feeds
//     apply's guardrails — which port must not be touched because something is
//     plugged into it.

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

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
	var rc = parseRunningConfig(runOut, c.host)
	out.Ports = rc.Ports
	out.MgmtVLAN = rc.MgmtVLAN

	// Link aggregates — the dense table plus each Po's switchport half out of
	// the running config already in hand. Tolerated failure: State.LAGs nil
	// reads as "not read", never "none".
	var lags, lagErr = c.readLAGs(ctx, runOut)
	if lagErr == nil {
		out.LAGs = lags
	} else {
		out.Notes = append(out.Notes, "aggregates not read: "+firstLine(lagErr.Error()))
	}

	var vlanOut string
	vlanOut, err = c.cli.run(ctx, "show vlan", cmdTimeout)
	if err != nil && vlanOut == "" {
		c.step(false, "show vlan failed", firstLine(err.Error()))
		out.Notes = append(out.Notes,
			"VLAN list not read ("+firstLine(err.Error())+"): missing VLANs cannot be told "+
				"from existing ones, so no VLAN will be created")
	} else {
		out.VLANs = parseVlanTable(vlanOut)
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
	// Split cages are absent from `ports` BY DESIGN (lane names are refused),
	// and that absence must be said here or nowhere: the operator looking at a
	// 52-socket switch drawn with 48 has exactly this note to explain the four.
	var split = breakoutNote(parseBreakoutCages(statusOut))
	if split != "" {
		out.Notes = append(out.Notes, split)
	}
	var p statusPort
	for _, p = range ports {
		out.Links[p.Name] = eosLinkState(p.Status)
		// A port present on the faceplate but absent from the running config
		// (block never written) is at its factory default: access VLAN 1.
		var _, known = out.Ports[p.Name]
		if !known {
			out.Ports[p.Name] = PortState{Mode: "access", PVID: 1}
		}
	}
	c.step(true, "interfaces read",
		fmt.Sprintf("%d ports, %d with link", len(ports), len(out.LinkUp())))

	// A port the SWITCH shut by itself reads exactly like an empty one, and the
	// operator has no way to tell the two apart from the port drawing. Ask for the
	// cause — one extra command, and only when there is something to ask about.
	out.Notes = append(out.Notes, c.errDisabledNotes(ctx, ports)...)

	// Facts the operator needs and that no other screen carries.
	out.Notes = append(out.Notes, configFacts(rc)...)
	if c.ztp {
		out.Notes = append(out.Notes,
			"this switch is still in ZERO TOUCH PROVISIONING: the configuration read above "+
				"is what it holds RIGHT NOW, and it will be discarded at the next ZTP cycle. "+
				"\"zerotouch cancel\" on the console ends that state")
	}
	if c.dialect != dialectEOS {
		out.Notes = append(out.Notes,
			"CLI dialect not recognized: this read is best-effort and writes are refused")
	}
	return out, nil
}

// configFacts turns what the running configuration establishes into the sentences
// an operator needs, and NOTHING is inferred beyond what was read.
//
// Split out of [Client.ReadState] because every one of these is also worth saying
// after a write, and a second copy of the wording would drift from this one.
func configFacts(rc runConfig) []string {
	var notes []string
	if !rc.LLDPRun {
		notes = append(notes,
			"LLDP has been turned OFF on this switch (\"no lldp run\" is in the running "+
				"configuration). EOS runs it by default, so this is a deliberate change "+
				"somebody made: the switch serves an empty LLDP-MIB and is invisible to every "+
				"neighbour in the fleet until it is turned back on")
	}
	if rc.SSHShut {
		notes = append(notes,
			"the SSH service is administratively DOWN (\"management ssh\" carries "+
				"\"shutdown\"). EOS runs SSH by default, so this too is a deliberate change — "+
				"and it is the door meshcraft administers this device through")
	}
	if !rc.UnsupportedXcvr {
		notes = append(notes,
			"\"service unsupported-transceiver\" is NOT configured: this switch will refuse "+
				"any optic it does not read as Arista-coded, and a refused cage reads exactly "+
				"like an empty one. Provisioning writes it; a factory reset removes it")
	}
	if rc.MgmtOOB != "" {
		notes = append(notes,
			"Management1 carries "+rc.MgmtOOB+" — that is the OUT-OF-BAND port, outside the "+
				"switching fabric. meshcraft does not administer through it and does not "+
				"change it; it is reported so an address seen on the device is not mistaken "+
				"for one meshcraft is managing")
	}
	if rc.Mgmt.Gateway != "" && !rc.IPRouting {
		notes = append(notes,
			"a default route to "+rc.Mgmt.Gateway+" is configured while \"ip routing\" is "+
				"not: on EOS that route still serves traffic the switch ITSELF originates "+
				"(which is what management needs), and forwards nothing for anybody else. "+
				"Said out loud because the same two lines on a router would mean something "+
				"quite different")
	}
	// How the management SVI was chosen — said only when the choice was not
	// mechanical, which is the only time it is news (cf. runConfig.MgmtSVINotes).
	// It comes BEFORE the gNMI fact deliberately: that fact names the management
	// VLAN, and an operator must read which SVI that is before reading a verdict
	// built on it.
	notes = append(notes, rc.MgmtSVINotes...)
	notes = append(notes, gnmiVRFFact(rc)...)
	var name string
	for name = range rc.AllowedAll {
		notes = append(notes,
			"trunk "+name+" carries no explicit \"switchport trunk allowed vlan\" line: on "+
				"EOS that means ALL VLANs, not none")
	}
	return notes
}

// gnmiVRFFact says when the gNMI transport listens in a VRF the management
// address does not live in.
//
// # The two-day silence this sentence exists to break
//
// A gNMI transport in the wrong routing instance BINDS SUCCESSFULLY and serves
// nobody (GNMIOpts.VRF states it; vrf.go repeats it). core01.lab proved how
// quiet that failure is: on 2026-08-13 its VLANs were moved out of
// VRF-MANAGEMENT into a new VRF-LAN, the transport stayed behind in the
// now-empty instance, and for two days ping, SSH, SNMP probes and a full config
// absorb all reported a healthy switch while every subscription died with a
// reason the proxy could only classify as "unknown" — a connection reset carries
// no gRPC status. The one place both halves of the contradiction are written
// down is the running configuration, and this read already holds it.
//
// The comparison is on NAMES, case-insensitively, with the empty string meaning
// the default instance on both sides. Nothing is inferred about reachability
// BETWEEN VRFs: routes can leak, and claiming "unreachable" on a box that leaks
// them would be a guess. The fact reported is the configured disagreement, which
// is what the operator must repair either way.
func gnmiVRFFact(rc runConfig) []string {
	if !rc.GNMIConfigured || rc.MgmtVLAN <= 0 {
		return nil
	}
	var tvrf = rc.GNMITransportVRF
	if tvrf == "" {
		tvrf = "default"
	}
	var mvrf = rc.MgmtVRF
	if mvrf == "" {
		mvrf = "default"
	}
	if strings.EqualFold(tvrf, mvrf) {
		return nil
	}
	return []string{fmt.Sprintf(
		"the gNMI transport listens in VRF %s while the management address (Vlan%d) is in "+
			"VRF %s: it binds successfully and serves NOBODY there, so telemetry is dark while "+
			"ping, SSH and SNMP all answer. \"management api gnmi\" → \"transport grpc default\" "+
			"→ \"vrf %s\" rebinds it where the management plane actually is",
		tvrf, rc.MgmtVLAN, mvrf, mvrf)}
}

// errDisabledNotes explains every port the switch shut by itself.
//
// Worth a second command because the plain status table does not carry the CAUSE,
// and the cause is the whole of what the operator needs: an xcvr-unsupported port
// is a refused transceiver (nothing wrong with the link), a bpduguard port is a
// loop somebody just created, and a link-flap port is a cable on its way out. All
// three read as "no link" on the drawing.
//
// Nothing here is an error: a read that could not obtain the causes returns what
// it has. Ports stay reported as down either way — only the explanation is
// missing.
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
	var causes = map[string]string{}
	var out, err = c.cli.run(ctx, "show interfaces status errdisabled", cmdTimeout)
	if err == nil || out != "" {
		causes = parseErrDisabled(out)
	}

	var notes []string
	var xcvr []string
	for _, p = range shut {
		var why = causes[p.Name]
		if why == ErrDisableXcvr {
			xcvr = append(xcvr, p.Name)
			continue
		}
		if why == "" {
			notes = append(notes, p.Name+" was shut down BY THE SWITCH (errdisabled) and it "+
				"did not report a cause — \"show interfaces status errdisabled\" on the device "+
				"gives it")
			continue
		}
		notes = append(notes, p.Name+" was shut down BY THE SWITCH (errdisabled), cause \""+
			why+"\" — this is not an empty port, and clearing it is an operator decision")
	}
	if len(xcvr) > 0 {
		notes = append(notes, strings.Join(xcvr, ", ")+": "+UnsupportedTransceiverNote)
		c.step(true, "transceiver refused by the switch on "+strings.Join(xcvr, ", "),
			"cause "+ErrDisableXcvr+". Provisioning sets "+
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
	var rc = parseRunningConfig(runOut, c.host)
	if rc.Mgmt.Interface == "" {
		var why = "no SVI carries an address that is administratively up — administration may " +
			"be coming through the console, or through an SVI left shut down"
		if rc.MgmtOOB != "" {
			// The one case where the absence has an obvious explanation, and saying
			// it saves an operator from concluding the read is broken.
			why = "no SVI carries an address that is administratively up. Management1 does " +
				"carry " + rc.MgmtOOB + ", but that is the OUT-OF-BAND port: administration " +
				"is arriving there, on a cable outside the switching fabric, and this " +
				"connector neither reads nor writes management through it"
		}
		rc.Mgmt.Notes = append(rc.Mgmt.Notes, why)
	}
	return rc.Mgmt, nil
}

// eosLinkState maps "show interfaces status" Status words onto the shared
// vocabulary. The words are the ones the parser keeps whole for exactly this (cf.
// statusPort.Status).
func eosLinkState(status string) driver.LinkState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "connected":
		return driver.LinkUpState
	case "notconnect", "inactive":
		return driver.LinkUnplugged
	case "disabled":
		return driver.LinkDisabled
	case "errdisabled":
		return driver.LinkErrDisabled
	}
	return driver.LinkDown
}
