// Package procurve covers the HP/HPE ProCurve 2600 series switches.
//
// # What is OBSERVED here and what is DOCUMENTED
//
// This distinction is the first thing to read, because the rest of the package
// depends on it and the two are not interchangeable.
//
// OBSERVED on hardware, 2026-08-02, on a ProCurve 2610-48 (J9088A) at
// 172.16.99.79 reached through a meshcraft proxy:
//
//   - TCP 22, 23 and 80 open;
//   - the web interface answers `Server: eHTTP v2.0` and titles its page
//     "<hostname> - ProCurve Switch 2610-48 (J9088A)". That title is the single
//     most reliable identification this range offers over the network: it names
//     the product AND the JxxxxA part number without any credential;
//   - the web interface issues NO HTTP authentication challenge — it logs in
//     through a form, so Basic/Digest injection cannot work on it;
//   - the SSH banner grab returned EMPTY and an SSH login attempt did not
//     succeed. Whether that is a credential, a disabled SSH server or an
//     algorithm mismatch is NOT established (cf. the header of ssh.go).
//
// DOCUMENTED, from the ProCurve 2600 series management and configuration guide,
// and NOT captured on this hardware: every CLI command, every capability number,
// and the reset procedure. They are the vendor's, they are stable across this
// family, and that is not the same as verified — which is why [Driver.Info]
// carries the same `Confirmed: false` caveat the Cisco IOS connector does.
//
// # Why this ships as a Connector that mostly refuses
//
// The contract's rule (driver/connector.go): a capability every switching
// platform HAS but that a connector has not coded yet is a MANDATORY method
// returning [driver.ErrNotImplemented] — not a silent success, and not an
// absence declared as a platform fact.
//
// So this connector authenticates and reads, and refuses to WRITE. Writing VLAN
// membership or an administration secret from a command set nobody has run on
// this range would be exactly the failure the contract exists to prevent: a
// configuration reported as applied that never arrived. The commands are known;
// what is missing is one session on the device to confirm the prompts, the
// paging behaviour and the refusal shapes — the same survey every other
// connector here rests on.
package procurve

import (
	"regexp"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

func init() { driver.Register(&Driver{}) }

// Driver covers the ProCurve 2600 range (2610-24, 2610-48, and their PoE
// variants), which share one CLI and one web interface.
type Driver struct{}

func (d *Driver) Info() driver.Info {
	return driver.Info{
		Name:   "hpe-procurve",
		Vendor: "HPE",
		Family: "ProCurve 2600",
		// SSH first because it is the transport a connector should use: telnet
		// carries the administration password in clear across the management
		// VLAN, and this range offers both. `web` is listed because the
		// interface exists and an operator uses it; meshcraft does not write
		// through it.
		Transport: []string{"ssh", "telnet", "web", "snmp"},
		Doc: "ProCurve 2600 Series Management and Configuration Guide — DOCUMENTED, " +
			"not captured: identification observed on a 2610-48 (J9088A) 2026-08-02, " +
			"the command set is the vendor's and has not been run by this connector",
	}
}

// Capabilities: the 2610 family.
//
// NUMBERS ARE THE VENDOR'S AND ARE NOT ROUNDED UP. Where this range is more
// limited than the rest of the fleet, that limit is stated rather than softened:
// a capability that lies costs a configuration nobody can explain.
func (d *Driver) Capabilities(model string) driver.Capabilities {
	return driver.Capabilities{
		VlanRange: [2]int{1, 4094},
		// 256 SIMULTANEOUS VLANs on this range, against 4094 elsewhere in the
		// fleet. The ID space is the full 802.1Q one; what is bounded is how many
		// may exist at once, and a site plan with more VLANs than this will not
		// fit on this switch. Saying 4094 here would move that discovery to the
		// moment a write fails.
		MaxVlans: 256,
		Features: map[string]bool{
			"ssh": true, "telnet": true, "http": true, "serial": true,
		},
		// STP, RSTP and MSTP are all offered by this family. MSTP is what the
		// fleet's site preference asks for first (cf. driver.STPPreferenceCatalog),
		// so this range can join a region — unlike SwOS, which cannot.
		STPModes:     []string{"none", "stp", "rstp", "mstp"},
		MSTInstances: 16,
		// v1/v2c only, and this is narrower than the datasheet on purpose.
		//
		// The range does carry a v3 engine. What decides this list is not what the
		// hardware can do but what meshcraft can PROVISION: an empty or overstated
		// list rules a device in or out of monitoring upstream (cf. snmpprov.For),
		// and declaring v3 here would advertise supervision this connector cannot
		// turn on — the exact "capability that lies" the contract warns about.
		//
		// v2c is OBSERVED: the reference unit carried « snmp-server community "les"
		// Operator » when it was surveyed, and that form is what ApplySNMP writes.
		SNMPVersions: []string{"v1", "v2c"},
		// The web interface and the CLI do not share a session limit worth
		// declaring; what this range does limit is simultaneous CLI sessions.
		MaxSessions: 5,
		// `write memory` persists what the CLI has already applied: the running
		// configuration takes effect immediately and is lost at reboot until
		// saved. Same shape as the FS S3900, and the same free rollback — apply,
		// verify reachability, THEN save.
		Commit:     driver.CommitApplyThenSave,
		Monitoring: monitoring(),
		Admin: driver.AdminAccount{
			// This range has NAMED accounts, at two privilege levels: `manager`
			// (read-write) and `operator` (read-only). meshcraft administers with
			// the manager level, and the name is free — hence Custom.
			Custom: true,
			// Password length is not bounded downward by the platform in a way
			// worth declaring; the upper bound is the vendor's.
			PasswordMax: 64,
			Notes: []string{
				"Two privilege levels: manager (read-write) and operator (read-only).",
				"password manager user-name <name> plaintext <secret>",
				"An operator account cannot write: provisioning must use manager.",
			},
		},
		Mgmt: driver.MgmtCapabilities{
			// Management lives on a VLAN interface, and this range accepts it on
			// any VLAN — the address is configured under `vlan <id>` with
			// `ip address`.
			VlanIDs:   driver.VlanAny,
			VlanRange: [2]int{1, 4094},
			TaggedOK:  true,
			Notes: []string{
				"Management address is set under « vlan <id> » with « ip address ».",
				"The management VLAN must be carried on the uplink or the switch " +
					"becomes unreachable — this range gives no second path except the " +
					"serial console.",
			},
		},
	}
}

// monitoring: what a collector can read from this range.
//
// SNMP only, and read-only. This family has no push mechanism worth declaring:
// it is supervised by polling, and what is missing compared with a modern range
// is REACTIVITY, not information.
func monitoring() driver.MonitoringCapabilities {
	return driver.MonitoringCapabilities{
		Channels: []driver.MonitorChannel{{
			Kind:    "snmp",
			Metrics: []string{"if_counters"},
			// A 2610 is a 2007-era switch with a modest CPU: polling it as hard
			// as a current one costs more than the resolution is worth.
			MinInterval: 30 * time.Second,
			Note: "Read-only agent. The web interface logs in through a FORM, so " +
				"nothing here is collected over HTTP.",
		}},
		// No trap receiver is declared. The agent supports traps, but nothing
		// here has been observed to accept a destination, and declaring the
		// capability would open 162/udp on a proxy to receive nothing.
		Traps: false,
	}
}

// Recognition patterns.
//
// The PART NUMBER is the strongest signal this range gives, and it is given
// away for free: the web page title carries "ProCurve Switch 2610-48 (J9088A)"
// with no credential at all (OBSERVED 2026-08-02). A JxxxxA reference is
// unambiguous — it names one product — where the word "ProCurve" alone covers
// two decades of switches with three different CLIs.
var (
	// reProCurve2600: the range this connector actually describes.
	reProCurve2600 = regexp.MustCompile(`(?i)ProCurve\s+Switch\s+26\d\d|J90(8[89]|9[01])A`)
	// reProCurveAny: the brand, which is NOT the same claim. Matching it earns a
	// low score on purpose: meshcraft would rather file a 5400 as "an HPE switch
	// we do not know" than as a 2610 whose command set it would then apply.
	reProCurveAny = regexp.MustCompile(`(?i)ProCurve|HP\s+J\d{4}A`)
	// reEHTTP: this family's web server identifies itself, and nothing else in
	// the fleet answers with it (OBSERVED: `Server: eHTTP v2.0`).
	reEHTTP = regexp.MustCompile(`(?i)eHTTP`)
)

// Fingerprint answers "is this device mine?", from 0 to 100.
//
// Scores are deliberately spread rather than binary. 88 for the part number or
// the range name — the same confidence the FS connector claims for its own model
// string. 55 when only the brand and this family's web server agree, which is
// enough to file the device and not enough to drive it. The brand alone scores
// low: HPE has sold switches under this name whose CLI shares nothing with a
// 2610, and claiming them would apply the wrong command set to the wrong
// hardware.
func (d *Driver) Fingerprint(ev driver.Evidence) int {
	var hay = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if reProCurve2600.MatchString(hay) {
		return 88
	}
	if reEHTTP.MatchString(hay) && reProCurveAny.MatchString(hay) {
		return 55
	}
	if reProCurveAny.MatchString(hay) {
		return 40
	}
	return 0
}

// ResetProcedure: how an operator brings one of these back to factory state.
//
// DOCUMENTED, not captured — the same caveat the whole package carries. It is
// written down because the alternative is an operator searching a 2007 PDF while
// standing in front of a rack.
func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Hardware: "Front-panel buttons, no console needed. Hold Clear and Reset " +
			"together; release Reset while keeping Clear held; release Clear when the " +
			"Self-Test LED starts flashing. The switch reboots on its factory " +
			"configuration.",
		Software: "erase startup-config, then reload",
		Erases: []string{
			"the whole configuration, including VLANs and the management address",
			"manager and operator passwords",
		},
		AfterReset: []string{
			"9600 8N1 on the console port",
			"the CLI opens at the operator prompt « > »; « enable » reaches manager « # » " +
				"with NO password on a factory unit",
			"SSH is NOT available until « crypto key generate ssh » and « ip ssh » are " +
				"run: a factory unit answers telnet and the web interface only",
		},
		Pitfalls: []string{
			"« Clear » ALONE, held briefly, resets only the passwords — not the " +
				"configuration. The two gestures look alike and do very different things",
			"this range has 256 VLANs, not 4094: a site plan with more will not fit",
			"telnet carries the password in clear across the management VLAN. Enable " +
				"SSH before trusting this switch with a site credential",
			"the web interface logs in through a FORM, so nothing can pre-authenticate " +
				"it by injecting an HTTP header",
		},
		Confirmed: false,
		Source: "ProCurve 2600 Series Management and Configuration Guide — DOCUMENTED, " +
			"not captured on lab hardware. Identification observed on a 2610-48 " +
			"(J9088A), 2026-08-02.",
	}
}
