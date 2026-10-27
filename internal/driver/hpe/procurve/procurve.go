// Package procurve covers the HP/HPE ProCurve 2600 series switches.
//
// # What is OBSERVED here and what is DOCUMENTED
//
// This distinction is the first thing to read, because the rest of the package
// depends on it and the two are not interchangeable.
//
// OBSERVED on hardware. The survey this package waited for happened on
// 2026-08-03, on SW-PLAYER-79 — a ProCurve 2610-48 (J9088A, R.11.121) in
// production at a live site, reached through a meshcraft proxy. Its verbatim
// output is in testdata/ and every parser here is tested against it.
//
// What that session established:
//
//   - SSH IS enabled and does negotiate. The server is OpenSSH 3.7.1p2 and it
//     needs the legacy algorithm list this package carries (cf. ssh.go). The
//     earlier "banner grab returned EMPTY, login did not succeed" was neither a
//     credential nor a disabled server: the switch allows FOUR management
//     sessions in total ("show ip ssh"), and abandoned ones hold their slot.
//     A saturated switch refuses the SESSION CHANNEL after a successful
//     authentication, which reads exactly like a pty problem — see cli.go;
//   - the CLI paints with cursor positioning and terminates lines with ESC E;
//   - "show version" carries NO model and no part number. The product and its
//     JxxxxA reference are read from the LLDP system description instead
//     (cf. inspect.go);
//   - the web interface answers `Server: eHTTP v2.0`, titles its page
//     "<hostname> - ProCurve Switch 2610-48 (J9088A)", and issues NO HTTP
//     authentication challenge — it logs in through a form, so Basic/Digest
//     injection cannot work on it.
//
// DOCUMENTED and NOT captured, still: the reset procedure, and the spanning-tree
// commands other than "spanning-tree", "… bpdu-protection" and
// "force-version", which appear verbatim in the reference unit's own
// configuration. The ones that are only documented are implemented AND VERIFIED
// BY RE-READING the device after the write (cf. stp.go), which is what makes
// them safe to ship: a form this firmware does not accept surfaces as an error
// instead of a silent success.
//
// # What this connector writes
//
// VLAN membership, the SNMP community, spanning tree, management addressing on
// the current VLAN, and the manager secret. Each write re-reads before acting
// and — where the platform allows it — verifies afterwards, because on this CLI
// a refused command prints a "%" line and changes nothing: there is no exit
// status to test.
//
// PoE and port security are writable on this platform and are NOT implemented.
// They are absent rather than declared, per the contract's rule
// (driver/connector.go): a capability announced and not delivered is a
// configuration reported as applied that never arrived.
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
		// The CLI sits behind SSH authentication: nothing is identifiable
		// without a credential.
		IdentifyNeedsCredentials: true,
		// SSH first because it is the transport a connector should use: telnet
		// carries the administration password in clear across the management
		// VLAN, and this range offers both. `web` is listed because the
		// interface exists and an operator uses it; meshcraft does not write
		// through it.
		Transport: []string{"ssh", "telnet", "web", "snmp"},
		Doc: "ProCurve 2600 Series Management and Configuration Guide, CAPTURED on a " +
			"2610-48 (J9088A, R.11.121) 2026-08-03: reads, VLAN membership, SNMP, " +
			"spanning tree and management addressing were run on that hardware and " +
			"the verbatim output is in the package's testdata",
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
			// LACP: trunks (TrkN). DOCUMENTED forms only — the range is parked
			// and no unit is adopted; the reader and writer exist (lag.go) and
			// the writer says so in its result Notes on every run.
			"LACP": true,
			// Per-port spanning-tree settings this connector writes. CAPTURED:
			// "spanning-tree 1 bpdu-protection" is in the reference unit's own
			// configuration.
			//
			// NOT declared, deliberately: STPEdgeAuto and STPPortEnable. Auto-edge
			// on this range is a separate, always-on detection with no admin value
			// to write, and a port leaves the tree by being disabled outright —
			// there is no per-port STP switch to offer.
			driver.FeatSTPEdge:   true,
			driver.FeatBPDUGuard: true,
			// MSTRegion: this connector emits the whole triplet —
			// "spanning-tree config-name", "config-revision" and
			// "instance N vlan …" (regionCmds, stp.go), differentially against
			// what the read returned, so an unchanged region sends nothing.
			//
			// The evidence is the CODE, which is what this key asks about: "does
			// this connector emit a region?" (ADR-0022). What has NOT been done is
			// a region apply observed on a 2610 in the lab, and ADR-0023 records
			// that limit rather than letting the declaration imply more than it
			// means. The key gates a screen; it decides no apply behaviour.
			driver.FeatMSTRegion: true,
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
		// v2c is OBSERVED: the reference unit carried "snmp-server community "ops"
		// Operator" when it was surveyed, and that form is what ApplySNMP writes.
		SNMPVersions: []string{"v1", "v2c"},
		// FOUR, and it is the console plus three, not five. CAPTURED from
		// "show ip ssh" on the reference unit:
		//
		//	  Ses Type     | Protocol  Source IP and Port
		//	  --- -------- + --------- ---------------------
		//	  1   console  |
		//	  2   ssh      | SSH v2    203.0.113.3:55374
		//	  3   ssh      | SSH v2    203.0.113.2:60040
		//	  4   inactive |
		//
		// This number is small enough to be reached in ordinary use, and a
		// saturated switch does NOT refuse the login — it authenticates and then
		// refuses the session channel, which surfaces as "the device refused a
		// pty". Anything holding one of these slots (an abandoned tunnel stream,
		// a forgotten terminal) costs a quarter of the switch's administration
		// capacity until the TCP connection dies.
		MaxSessions: 4,
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
				"Management address is set under \"vlan <id>\" with \"ip address\".",
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
		Channels: []driver.MonitorChannel{
			{
				Kind:    "snmp",
				Metrics: []string{"if_state", "if_counters"},
				// A 2610 is a 2007-era switch with a modest CPU — the reference
				// unit sits at 13% doing nothing — so polling it as hard as a
				// current one costs more than the resolution is worth.
				MinInterval: 30 * time.Second,
				Note: "standard IF-MIB, served as soon as a v2c community is set " +
					"(ApplySNMP). ifDescr on this platform is the BARE PORT NUMBER — " +
					"\"47\", not an interface name — which the connector translates " +
					"through driver.PortNamer",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"lldp"},
				MinInterval: 30 * time.Second,
				// This is the opposite of the Cisco and FS ranges, where the
				// topology view stays empty until "lldp run" is configured.
				Note: "standard LLDP-MIB (1.0.8802). This range ships with LLDP ON " +
					"and records only a departure from that default, so the branch is " +
					"populated on an untouched switch — CAPTURED 2026-08-03: port 47 " +
					"sees \"salon-sw01\" on a device whose configuration carries no " +
					"LLDP line at all",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"health"},
				MinInterval: 5 * time.Minute,
				Note: "sysUpTime (1.3.6.1.2.1.1.3) is served by every unit of this " +
					"range. CPU and memory are NOT claimed here — cf. the Unavailable " +
					"declaration below",
			},
		},
		// No trap receiver is declared. The agent HAS one — "show snmp-server"
		// prints an empty "Trap Receivers" table with its Address / Community /
		// Events columns (CAPTURED) — and this connector does not write it.
		// Declaring the capability would open 162/udp on a proxy to receive
		// nothing.
		Traps: false,
		Unavailable: []driver.MetricUnavailable{
			{
				Metric:   "cpu_memory",
				Observed: true,
				Source: "\"show system-information\" carries CPU Util (%) and a full " +
					"memory breakdown (captured 2026-08-03: 13%, 72,603,456 free of " +
					"83,988,864) — but the periodic collector speaks only SNMP, and this " +
					"range publishes them under HP's private STATISTICS-MIB (enterprise " +
					"11), which the poller does not walk. The data EXISTS on the device " +
					"and meshcraft cannot yet collect it: a poller gap, not a platform one",
			},
			{
				Metric:   "optical_power",
				Observed: true,
				Source: "the two mini-GBIC cages of a 2610-48 report no DDM at all: this " +
					"is a 2007 platform and \"show interfaces brief\" prints a BLANK type " +
					"for an empty cage rather than any optical figure. Absent on the " +
					"hardware, not merely uncollected",
			},
		},
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
			"the CLI opens at the operator prompt \">\"; \"enable\" reaches manager \"#\" " +
				"with NO password on a factory unit",
			"SSH is NOT available until \"crypto key generate ssh\" and \"ip ssh\" are " +
				"run: a factory unit answers telnet and the web interface only",
		},
		Pitfalls: []string{
			"\"Clear\" ALONE, held briefly, resets only the passwords — not the " +
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
