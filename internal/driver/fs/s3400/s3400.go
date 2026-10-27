// Package s3400 is the FS.com S3400 PoE+ series knowledge base
// (S3400-48T4SP and family mates sharing the same BDCOM-style CLI).
//
// # The status of what is written here, said before anything else
//
// The base forms come from the official FS PDFs (CLI reference guide,
// configuration preparation, PoE+ series reset & recovery guide), CORRECTED
// by lab sessions on a 48T4SP running Version 2.0.2J — serial on 2026-08-03,
// then SSH the same day once the unit was adopted. What those sessions
// settled: the SFP+ cages are TGigaEthernet0/1-4 (tg0/N), the factory login
// is admin/admin, boot takes ~2 minutes, "ip route default GW" is the
// accepted route form, and "write" answers "OK!".
//
// TWO EARLIER CLAIMS HERE WERE WRONG and are corrected: "ip http server"
// and "aaa authentication login default local" were recorded as refused on
// the strength of the serial run's log. They are not. Both sit in the
// device's running configuration, "show ip http" answers "Http server is
// running", and the errors came from a bug in the soft-reset script that
// attributed one command's error to the next command. A connector's notes are
// only worth what their evidence is worth — that one cost a real capability.
//
// # This is NOT the S3900 CLI
//
// The S3400 runs a BDCOM-style IOS-like CLI that disagrees with the FSOS 1.7
// of the S3900 on almost every form:
//
//   - config mode is entered with "config" (prompt `Switch_config#`),
//     not "conf t" (`Switch(config)#`);
//   - interfaces are `g<slot>/<port>` — "GigaEthernet0/1", NOT
//     "GigabitEthernet" and NOT "ethernet 1/1";
//   - the access VLAN of a port is "switchport pvid N", not
//     "switchport access vlan N";
//   - the SVI takes "ip address A.B.C.D M.M.M.M" (dotted mask), not the
//     "ip add A.B.C.D/nn" CIDR form of the S3900;
//   - VLANs are created with a bare "vlan N" (+ optional "name X"), no
//     "vlan database" and no "media ethernet";
//   - SSH is "ip sshd enable", the exact form the S3900 refuses;
//   - saving is "write" (EXEC or global config), the exact verb the S3900
//     refuses — and there is no startup file-name prompt documented;
//   - factory reset is "delete startup-config" → y → "reboot", the Cisco
//     shape the S3900 does not have.
package s3400

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

func init() { driver.Register(&Driver{}) }

// Driver identifies the FS S3400 PoE+ range and reaches it over SSH
// (contract.go). It reads VLANs, ports, links, spanning tree and LLDP, and
// writes VLAN membership, SNMP, LLDP and the spanning-tree bridge. The Web
// Serial soft-reset script (web/src/pages/add/fsS3400SoftReset.ts) brings a
// factory unit to that point and emits the same forms as [MgmtPushCmds].
type Driver struct{}

func (d *Driver) Info() driver.Info {
	return driver.Info{
		Name:      "fs-s3400",
		Vendor:    "FS.com",
		Family:    "S3400 PoE+ (BDCOM-style CLI)",
		Transport: []string{"ssh", "serial", "http"},
		// The CLI sits behind SSH authentication: nothing is identifiable
		// without a credential.
		IdentifyNeedsCredentials: true,
		Doc: "FS S3400-48T4SP CLI reference guide + PoE+ series reset guide " +
			"(https://img-en.fs.com/file/user_manual/s3400-48t4sp-switch-cli-reference-guide.pdf, " +
			"https://img-en.fs.com/file/user_manual/poe+-series-switches-reset-and-recovery-system-configuration-guide.pdf)",
	}
}

var (
	// The model string appears in "show version" and in the firmware file
	// names of the reset guide (FS-S3400-…). The other alternatives are the
	// family mates the same PoE+ guide covers with the same CLI — recognized
	// so a candidate lands on this dialect rather than on generic-ssh, at a
	// score that still loses to a more specific future driver.
	reS3400 = regexp.MustCompile(`(?i)S3400|S3150-8T2FP|S3260-(8|16)T`)
	// BDCOM is the software vendor of this CLI family; a weak clue on its own.
	reBDCOM = regexp.MustCompile(`(?i)\bBDCOM\b`)
	reFS    = regexp.MustCompile(`(?i)\bFS\.?com\b|Fiberstore`)
)

func (d *Driver) Fingerprint(ev driver.Evidence) int {
	var hay = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if reS3400.MatchString(hay) {
		return 88
	}
	if reBDCOM.MatchString(hay) {
		return 60
	}
	// Below the S3900's weak FS score (50): on a bare "FS.com" clue the S3900
	// stays the better guess — it is the range this product actually has a
	// connector for.
	if reFS.MatchString(hay) {
		return 45
	}
	return 0
}

// monitoring: through which channels this platform is observed.
//
// The product's periodic collector speaks only SNMP. Nothing has been walked
// on an S3400 — the channels below rest on the STANDARD MIBs the poller
// (internal/snmppoll) queries and on the CLI guide documenting an SNMP agent
// (snmp-server chapter), which is not the same as verified: each note says
// what remains to be confirmed. Floors are conservative for the same reason
// as everywhere else — an access switch's v2c agent polled too hard answers
// less and less.
func monitoring() driver.MonitoringCapabilities {
	return driver.MonitoringCapabilities{
		Channels: []driver.MonitorChannel{
			{
				Kind:        "snmp",
				Metrics:     []string{"if_state", "if_counters"},
				MinInterval: 15 * time.Second,
				Note: "standard IF-MIB. The agent answers as soon as a v2c community is set " +
					"(ApplySNMP) — \"show snmp\" was already counting input packets before " +
					"one existed (captured 2026-08-03). ifIndex is 128 + the port number the " +
					"CLI calls \"unique port number\", so GE1 is 129 and XG2 is 178 " +
					"(both captured); driver.PortNamer carries that translation",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"lldp"},
				MinInterval: 30 * time.Second,
				Note: "standard LLDP-MIB (1.0.8802). LLDP is OFF on a factory unit — \"show " +
					"lldp neighbor\" answers \"LLDP is not enabled\" — so this branch stays " +
					"empty until \"lldp run\" is configured, which provisioning writes " +
					"(cf. Client.EnsureLLDP). Neighbours appeared within one advertisement " +
					"interval once it was set (captured 2026-08-03)",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"health"},
				MinInterval: 5 * time.Minute,
				Note: "sysUpTime is served (\"show version\" carries the uptime and the agent " +
					"answers). CPU and memory are NOT claimed: the poller's OIDs for them are " +
					"HOST-RESOURCES or vendor-private, and neither has been walked here",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"fdb"},
				MinInterval: 30 * time.Second,
				Note: "MEASURED (walk 2026-08-13 on poefs3400lab, over the collector's own " +
					"SNMPv3 authNoPriv conventions — the first evidence this route produced " +
					"once it learned v3): Q-BRIDGE-MIB dot1qTpFdbPort serves the learned " +
					"table WITH its VLAN arc (14 rows), dot1dBasePortIfIndex maps all 52 " +
					"bridge ports as the IDENTITY (bridge port = ifIndex = 128 + port). " +
					"A result worth stating against this agent's record: LLDP-MIB is absent " +
					"here while the bridge MIBs are complete.",
			},
		},
		// TRAPS: MEASURED, not assumed, on poefs3400lab on 2026-08-10. The
		// device's trap counter moved 0 → 3 → 7 PDUs while a destination was
		// declared, the controller logged "trap accepted outside stored
		// destination", and the counter froze at 7 across a six-minute recheck
		// once the destination was removed — so this platform emits, and the
		// product's collector accepts what it emits.
		//
		// It flips WITH [Client.ApplyTrapReceiver] and never before: this flag is
		// what opens 162/udp on the proxy and what lets api/trapdest.go reach the
		// device, and without the applier `trap_dest_proxy_id` stays NULL, so
		// ConfirmTrapPath judges every arriving trap `trapProofForeign` — a
		// channel that works and can never be confirmed. The two halves are the
		// same capability seen from two sides (cf. driver.TrapPusher).
		Traps:  true,
		Syslog: false,
		// NOTE the asymmetry with the S3900 next door, which declares
		// optical_power ABSENT. This platform DOES serve the optics — but on
		// the CLI, not through the OIDs the poller queries:
		//
		//   DDM info:
		//       TX power:-1.61 dBm, RX power:-1.51 dBm
		//       SFP temperature:47.00 C, supply voltage :3.29V, Bias Current.:42.84mA
		//
		// captured on TGigaEthernet0/2, 2026-08-03. Until the poller can take a
		// CLI-sourced sample, the metric stays undeclared rather than promised
		// through a channel that would collect nothing.
		Unavailable: []driver.MetricUnavailable{
			{
				Metric:   "optical_power",
				Observed: true,
				Source: "the DDM block of \"show interface tg0/N\" carries TX/RX power, " +
					"temperature, voltage and bias current (captured 2026-08-03) — but the " +
					"periodic collector speaks only SNMP, and this platform's enterprise " +
					"trees answer empty (walked live 2026-08-05: 1.0.8802, 3320 and 52642 " +
					"all serve nothing). Since 2026-08-05 the readings ARE collected at " +
					"READ-STATE cadence over the admin session (driver.OpticsReader, " +
					"persisted as extra.optics_observed) — what stays unavailable is the " +
					"PERIODIC sample this monitoring declaration is about: still a poller " +
					"gap, not a platform one",
			},
		},
	}
}

func (d *Driver) Capabilities(model string) driver.Capabilities {
	return driver.Capabilities{
		VlanRange: [2]int{1, 4094},
		MaxVlans:  4094,
		Features: map[string]bool{"ssh": true, "http": true, "serial": true,
			// TRAFFIC DECOMPOSED BY CLASS (ADR-0028), same basis and same
			// caveat as instanton: collected over SNMP, filled from the
			// ifXTable per-class columns.
			//
			// DEDUCED. The evidence is sw01-arena's own in-octet series, which
			// come from ifHCInOctets (…31.1.1.1.6) and carry `ifindex` plus a
			// resolved port name (`TGigaEthernet0/49`, checked 2026-10-25) —
			// so this firmware's agent serves the ifXTable. No walk was
			// attempted: SNMP is UDP and the lab route is a TCP SOCKS5.
			driver.FeatTrafficClassMulticast: true,
			driver.FeatTrafficClassBroadcast: true,
			// FALSE — no standard MIB object counts flooded unicast per port.
			driver.FeatTrafficClassUnknownUnicast: false,
			// LACP: ABSENT on this train, established by probing rather than
			// assumed (2026-08-13, poefs3400lab, Version 2.0.2J): nine command
			// families all answer "Unknown command" — show port-group /
			// aggregator-group [brief|1 brief] / link-aggregation / lacp /
			// trunk / aggregation / interface port-aggregator. The datasheet
			// speaks of link aggregation; this firmware's CLI carries none of
			// its spellings, and false here is what keeps the panel from
			// offering a write nothing can execute.
			"LACP": false,
			// DHCP snooping exists here, GLOBALLY and PER VLAN — both forms were
			// sent to the lab unit on 2026-08-10, accepted, and read back.
			"DHCPSnooping": true,
			// The PER-PORT TRUST level does not, and the pair is declared the way
			// instanton declares "StormControlPort: false": an operator's screen
			// should say "this hardware cannot" rather than offer a control that is
			// always skipped. "ip dhcp-relay snooping trust" is answered "Unknown
			// command" on OpCode 2.0.2J and the CLI's completion carries no such
			// leaf anywhere (dhcpsnoop.go, snoopTrustRefusal). No capture will change
			// this one — it is not "unproven", it is absent.
			"DHCPSnoopingPortTrust": false,
			// MST REGION: FALSE, declared rather than left absent, because this
			// connector DOES declare WritesExtra("stp") and the two were being read
			// as one thing (ADR-0022). An apply reaches this device, writes the RSTP
			// bridge, and leaves it carrying its own bridge MAC as a region name —
			// measured on sw01-arena on 2026-10-25, "show spanning-tree mst region"
			// answering "Name: [ 649D9929F706 ]", revision 0, instance 0 holding
			// 1-4094. A region of one, with a clean ApplyResult behind it.
			//
			// Two things stand in the way, and neither is a missing line of code.
			// The mode CHANGE closes the SSH session before the CLI answers and
			// leaves "show spanning-tree" reporting no instance for ~45 s
			// (2026-08-11, stp.go's header): the connector would error over a device
			// it had changed and verify against an empty tree. And the region's
			// WRITE grammar is uncaptured here — the read form is this range's own
			// ("show spanning-tree mst region"; "show spanning-tree mst
			// configuration" is answered "Too many parameters"), and this
			// platform has already demonstrated what guessing costs: "spanning-tree
			// mstp instance 0 priority 36864", the form deduced from a neighbouring
			// vendor's grammar, is accepted SILENTLY and configures nothing.
			driver.FeatMSTRegion: false,
		},
		// RSTP is the factory default and the ONLY mode exercised here: the
		// bridge priority was written and read back on hardware
		// ("spanning-tree rstp priority 36864" → "Bridge ID Priority
		// 36864", 2026-08-03). "sstp" and "mstp" exist in this CLI and are
		// deliberately NOT declared — the priority command is mode-prefixed, so
		// announcing a mode this connector cannot write correctly would offer a
		// gesture that silently configures the wrong protocol.
		STPModes: []string{"rstp"},
		// v3 SINCE 2026-08-12, at authNoPriv and declared as such.
		//
		// The three reads and two ladders behind this line are argued in snmpv3.go.
		// What the declaration itself has to carry is the honesty of the pair:
		//
		//   - SNMPv3Auth is {md5, sha} because those two, and ONLY those two, were
		//     accepted on hardware. "sha256" and "sha-256" were both answered
		//     "Unknown command" on Version 2.0.2J, so this range can never meet
		//     the SHA-256/AES-128 policy floor and `snmpprov` will mark every S3400
		//     NON-conformant. That is the correct outcome and it belongs on the
		//     operator's screen, not in a comment;
		//   - SNMPv3Priv IS EMPTY, and that emptiness is a MEASUREMENT, not caution.
		//     Four privacy user forms were sent whole to poefs3400lab and all four
		//     were answered "Unknown command": there is no slot for a second
		//     passphrase on this firmware. An empty list is what makes
		//     `snmpv3.Negotiate` yield the `snmpv3-sha-nopriv` tag and stops any
		//     screen from offering encryption this platform cannot express.
		//
		// The pair is what decides the LABEL, therefore the shared secret: an S3400
		// cannot carry the same v3 credential as a device that does encrypt.
		SNMPVersions: []string{"v1", "v2c", "v3"},
		SNMPv3Auth:   snmpV3AuthAlgs(),
		SNMPv3Priv:   nil,
		MaxSessions:  5,
		// "write" persists the running config: apply, verify, THEN save.
		Commit:     driver.CommitApplyThenSave,
		Monitoring: monitoring(),
		Mgmt: driver.MgmtCapabilities{
			VlanIDs:   driver.VlanAny,
			VlanRange: [2]int{1, 4094},
			TaggedOK:  true,
			Static:    true, // "interface vlan N" + "ip address A.B.C.D M.M.M.M" (documented)
			DHCP:      true, // "ip address dhcp" on the SVI (documented)
			Gateway:   true, // form to be settled on hardware — see MgmtPushCmds
			OutOfBand: true, // RJ45 console, 115200 8N1
			// ApplySameVLAN: EXERCISED ON HARDWARE 2026-08-03 on the lab
			// 48T4SP. driver.MgmtAddrWriter re-applied the address the switch
			// already carried (192.0.2.3/16 on vlan 99, with "ip route default
			// 192.0.2.1") through the real command path, and the re-read came
			// back identical on a surviving session. A move to a DIFFERENT
			// subnet was not run: the session travels on the address that would
			// move, so the caller verifies through the new address.
			ApplySameVLAN: true,
			Notes: []string{
				"\"ip address A.B.C.D M.M.M.M\" (dotted mask) — the CIDR form of the " +
					"S3900 does not exist here",
				"default route: \"ip route default GW\" in config mode (captured " +
					"2026-08-03; \"ip default-gateway\" is refused)",
				"changing the address of the SVI the session came in through cuts the " +
					"session: verify afterward through the NEW address",
			},
		},
		Admin: driver.AdminAccount{
			Custom: true,
			Notes: []string{
				"username U password SECRET (no privilege keyword: privilege is a " +
					"property of enable levels here)",
				"\"aaa authentication login default local\" is what makes the account " +
					"guard anything — captured present in the running configuration",
				"enable password 0 SECRET (level 15 by default)",
			},
		},
	}
}

func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Software: "delete startup-config → \"this file will be erased, are you sure? (y/n)\" → y; " +
			"reboot — if the reboot prompt offers to SAVE the current configuration, answer NO " +
			"(saving would rewrite the startup-config that was just deleted)",
		Hardware: "monitor mode: Ctrl+P during power-on → \"monitor#\" (ip address / copy tftp " +
			"flash / reboot) — firmware recovery, not a configuration reset",
		Erases: []string{"startup configuration (startup-config file)"},
		AfterReset: []string{
			"115200 8N1 console (same speed as the FS S3900, NOT the Cisco 9600)",
			"boot takes ~2 MINUTES to a real \"Username:\" (captured 2026-08-03), and " +
				"the boot output contains lines ending in '#' that read like a prompt — " +
				"validate any post-reboot prompt with a quiet window + CR round-trip",
			"factory login admin / admin — NOT admin / blank; a console line without " +
				"authentication lands straight at \"Switch>\" and \"enable\" (or " +
				"\"enter\") reaches \"Switch#\"",
			"push management via MgmtPushCmds / soft-reset",
		},
		Pitfalls: []string{
			"config mode is \"config\" (prompt Switch_config#), not \"conf t\"",
			"interfaces are \"g0/1\" / \"GigaEthernet0/1\" — GigabitEthernet is refused; " +
				"the 4 SFP+ cages are \"TGigaEthernet0/1-4\", short tg0/1-4 " +
				"(CAPTURED 2026-08-03 — absent from the CLI guide)",
			"port ranges need the \"range\" keyword: \"int range tg0/1-4\" enters " +
				"Switch_config_if_range#, \"int tg0/1-4\" answers \"Parameter invalid\"; " +
				"and no trailing spaces — \"show run int tg0/3\" with one answers " +
				"\"Incomplete command\" (both captured 2026-08-03)",
			"access VLAN of a port is \"switchport pvid N\", not \"switchport access vlan N\"",
			"\"ip address A.B.C.D M.M.M.M\" (dotted mask), not the /nn form",
			"SSH is \"ip sshd enable\", not \"ip ssh server enable\"",
			"save with \"write\" (EXEC or global config) — not \"copy running-config " +
				"startup-config\" and no file-name prompt documented",
			"a reboot prompt that mentions saving must be answered NO after a factory " +
				"reset, or the deleted startup-config is written right back",
			"\"enable\" asks for NO password: the factory configuration carries " +
				"\"aaa authentication enable default none\", and it stays that way even " +
				"once \"enable password\" is set — removing that password is not what " +
				"locks this platform down (captured 2026-08-03)",
			"\"terminal length 0\" is accepted SILENTLY and suppresses nothing: the " +
				"pager still stops every screen on \"--More--\", over SSH and over " +
				"serial, from EXEC and from config mode. Feed it a space; an unfed pager " +
				"also EATS THE FIRST CHARACTER of the next command",
			"\"| include\" is unreliable: \"show running-config | include snmp\" returns " +
				"nothing on a configuration that demonstrably contains \"snmp-server " +
				"community\", while \"| include community\" returns it. Parse the whole " +
				"configuration rather than trusting a filter",
			"\"ip sshd enable\" can keep the console silent well past 10 s (host-key " +
				"generation) — give it its own budget or every later reply shifts by one",
			"the uplink must carry the management VLAN or the probe that follows the " +
				"push cannot reach the new address — when no link is up at push time, " +
				"trunk all four SFP+ cages (range tg0/1-4) rather than trunking nothing " +
				"(run log 2026-08-03)",
			"pager pauses at 24 lines — \"terminal length 0\" first (documented in " +
				"global config mode, the guide's own example runs it from EXEC)",
		},
		// The software path (delete startup-config → y → reboot → factory
		// admin/admin → push → write "OK!") RAN end to end on a 48T4SP on
		// 2026-08-03; the pitfalls above say per line what that run corrected.
		Confirmed: true,
		Source: "FS S3400-48T4SP CLI reference + PoE+ series reset guide, corrected by " +
			"the 2026-08-03 lab serial session (48T4SP)",
	}
}

// --- CLI builders (reference for the Web Serial soft-reset script) ---

// MgmtOpts is what soft-reset / onboarding needs on the wire.
type MgmtOpts struct {
	VlanID   int
	VlanName string // default LAN<id>_Management
	IP       string
	Mask     string // dotted decimal — this CLI has no /nn form
	// Gateway: pushed as the ORDERED candidates of GatewayCmdCandidates —
	// the config-mode form is undocumented (see that function).
	Gateway        string
	Username       string
	Password       string
	EnablePassword string
	// Hostname is the device's MESHCRAFT NAME, not an already-normalized system
	// name: [MgmtPushCmds] runs it through [driver.HostnameFromDeviceName] — the
	// same function api.hostnameheal uses — so a switch provisioned from a
	// console and the same switch reconciled half an hour later cannot end up
	// carrying two different names. A name that cannot be normalized is REFUSED,
	// never mangled: MgmtPushCmds returns the reason and omits the line.
	Hostname string
	// RemoveVlan1 drops the factory SVI ("no interface vlan1") once the
	// management address lives on another VLAN.
	RemoveVlan1 bool
	// TrunkPorts e.g. "range tg0/1-4" (the SFP+ uplinks, captured form) or a
	// single "tg0/1" / "g0/48" — empty skips trunk membership.
	TrunkPorts string
}

// FactoryResetCmds is the soft factory-reset sequence (privileged EXEC, per
// the PoE+ series reset guide). The caller answers the
// "this file will be erased, are you sure? (y/n)" confirm with y, and
// answers NO to any reboot prompt that offers to save the running config.
func FactoryResetCmds() []string {
	return []string{
		"delete startup-config",
		"reboot",
	}
}

// GatewayCmd returns the default-route form. CAPTURED 2026-08-03 on the
// 48T4SP: "ip route default GW" is accepted in global config mode
// ("ip route 0.0.0.0 0.0.0.0 GW" also passes; "ip default-gateway"
// answers "Unknown command").
func GatewayCmd(gw string) string {
	var g = strings.TrimSpace(gw)
	if g == "" {
		return ""
	}
	return "ip route default " + g
}

// MgmtPushCmds returns the lines that bring a factory S3400 to a reachable
// management state. All of it runs from global config ("config"); the final
// "write" persists from there (documented: EXEC or global config mode).
//
// THE ERROR IS THE SYSTEM NAME'S REFUSAL AND NOTHING ELSE, and the rest of the
// push is returned alongside it: [driver.HostnameFromDeviceName] refuses a name
// it cannot normalize, a builder returning a bare []string could only honour
// that refusal by dropping the line in silence, and a silently dropped hostname
// is the same lie as a mangled one. The argument is written out once, over
// cisco/ios's MgmtPushCmds. The mirror is web/src/pages/add/fsS3400SoftReset.ts,
// which logs the reason and pushes everything else.
func MgmtPushCmds(opts MgmtOpts) ([]string, error) {
	var vlanID = opts.VlanID
	if vlanID < 1 {
		vlanID = 1
	}
	var mask = strings.TrimSpace(opts.Mask)
	if mask == "" {
		mask = "255.255.255.0"
	}
	var name = strings.TrimSpace(opts.VlanName)
	if name == "" {
		name = fmt.Sprintf("LAN%d_Management", vlanID)
	}
	name = sanitizeVlanName(name)

	var out []string
	out = append(out, "config")

	// The system name, through the ONE normalizer — see the note where this
	// package's sanitizeHostname used to be.
	var hostErr error
	if strings.TrimSpace(opts.Hostname) != "" {
		var host, refusal = driver.HostnameFromDeviceName(opts.Hostname)
		if refusal != "" {
			hostErr = errors.New(refusal)
		} else {
			out = append(out, "hostname "+host)
		}
	}

	if vlanID != 1 {
		out = append(out,
			fmt.Sprintf("vlan %d", vlanID),
			"name "+name,
			"exit",
		)
	}

	out = append(out,
		// "interface vlan1" and "interface vlan 1" both appear in the
		// guide's own examples; the spaced form is used for uniformity.
		fmt.Sprintf("interface vlan %d", vlanID),
		fmt.Sprintf("ip address %s %s", opts.IP, mask),
		"exit",
	)

	var gwCmd = GatewayCmd(opts.Gateway)
	if gwCmd != "" {
		out = append(out, gwCmd)
	}

	// Both captured working in the running configuration of the provisioned
	// unit. "ip sshd enable" generates the host key on first use and can
	// stay silent well past 10 s, which is why the caller gives it its own
	// budget rather than a shared one.
	out = append(out,
		"ip sshd enable",
		"ip http server",
		// LLDP is OFF on a factory unit: "show lldp neighbor" answers "LLDP
		// is not enabled" and the running-config carries no "lldp run"
		// (captured 2026-08-03). Without it the switch is invisible to every
		// neighbour AND sees none, so the topology view stays empty on a
		// perfectly healthy device. It goes in by default.
		"lldp run",
	)

	var ports = strings.TrimSpace(opts.TrunkPorts)
	if ports != "" && vlanID != 1 {
		out = append(out,
			"interface "+ports,
			"switchport mode trunk",
			// Explicit rather than trusting an undocumented default allowed
			// list; on a freshly reset unit "all VLANs" is what factory state
			// meant anyway.
			"switchport trunk vlan-allowed 1-4094",
			"exit",
		)
	}

	if opts.RemoveVlan1 && vlanID != 1 {
		out = append(out, "no interface vlan 1")
	}

	// Credentials LAST — same discipline as the S3900: if the platform drops
	// the console session on the logged-in account's change, everything else
	// is already in place and the caller re-logins with the NEW secret to
	// finish the save.
	var user = strings.TrimSpace(opts.Username)
	var pass = opts.Password
	if user != "" && pass != "" {
		out = append(out,
			fmt.Sprintf("username %s password %s", user, pass),
			// Captured in the running configuration of the provisioned unit —
			// this line is what makes the local account guard anything. It is
			// also the factory default, so sending it is idempotent.
			"aaa authentication login default local",
		)
	}
	var en = opts.EnablePassword
	if en == "" {
		en = pass
	}
	if en != "" {
		out = append(out, fmt.Sprintf("enable password 0 %s", en))
	}

	out = append(out,
		// "write" is valid from global config mode (configuration
		// preparation guide §1.6) — no EXEC round-trip needed before saving.
		"write",
	)
	return out, hostErr
}

// rePortUp matches the header line of "show interface" for a port whose
// link AND protocol are up: "GigaEthernet0/1 is up, line protocol is up".
// The four SFP+ cages of the 48T4SP are "TGigaEthernet0/1-4" — CAPTURED on
// hardware (2026-08-03 session), despite the CLI guide never naming them.
var rePortUp = regexp.MustCompile(`(?im)^((?:T?Giga|Fast)?Ethernet)(\d+/\d+)\s+is\s+up,\s*line\s+protocol\s+is\s+up`)

// ParseUpPorts extracts short port names ("tg0/3", "g0/1") for every
// Ethernet port reported up/up by "show interface" (or its "| include
// line protocol" filter). SVIs ("VLAN1 is up") never match: the pattern
// wants a slot/port.
func ParseUpPorts(show string) []string {
	var seen = map[string]bool{}
	var out []string
	var matches = rePortUp.FindAllStringSubmatch(show, -1)
	var i int
	for i = range matches {
		var m = matches[i]
		var key = shortPortName(m[1], m[2])
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// shortPortName maps a long type name to the CLI short prefix: e/f/g are
// documented by the configuration-preparation guide, tg was CAPTURED on
// hardware (2026-08-03: "show run int tg0/3" accepted for
// TGigaEthernet0/3, one of the four SFP+ cages).
func shortPortName(kind, slotPort string) string {
	var k = strings.ToLower(kind)
	switch {
	case strings.HasPrefix(k, "tgiga"):
		return "tg" + slotPort
	case strings.HasPrefix(k, "giga"):
		return "g" + slotPort
	case strings.HasPrefix(k, "fast"):
		return "f" + slotPort
	case k == "ethernet":
		return "e" + slotPort
	}
	return ""
}

// CompactPortRange turns ["tg0/1","tg0/2","tg0/3"] into "range tg0/1-3"
// when the ports are contiguous with one prefix and slot; otherwise "".
// CAPTURED 2026-08-03: "int range tg0/1-4" enters Switch_config_if_range#,
// and the "range" keyword is MANDATORY — "int tg0/1-4" answers
// "Parameter invalid".
func CompactPortRange(ports []string) string {
	if len(ports) == 0 {
		return ""
	}
	var prefix = ""
	var slot = -1
	var nums []int
	var p string
	for _, p = range ports {
		var m = regexp.MustCompile(`(?i)^(tg|g|f|e)(\d+)/(\d+)$`).FindStringSubmatch(p)
		if m == nil {
			return ""
		}
		var pr = strings.ToLower(m[1])
		var s, _ = strconv.Atoi(m[2])
		var n, _ = strconv.Atoi(m[3])
		if prefix == "" {
			prefix = pr
		} else if pr != prefix {
			return ""
		}
		if slot < 0 {
			slot = s
		} else if s != slot {
			return ""
		}
		nums = append(nums, n)
	}
	var i int
	for i = 1; i < len(nums); i++ {
		if nums[i] != nums[i-1]+1 {
			return ""
		}
	}
	if len(nums) == 1 {
		return fmt.Sprintf("%s%d/%d", prefix, slot, nums[0])
	}
	return fmt.Sprintf("range %s%d/%d-%d", prefix, slot, nums[0], nums[len(nums)-1])
}

func sanitizeVlanName(s string) string {
	var b strings.Builder
	var r rune
	for _, r = range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' {
			b.WriteByte('_')
		}
	}
	var out = b.String()
	if out == "" {
		return "Management"
	}
	// "name" takes at most 32 characters on this CLI (reference §14.1.2);
	// a longer one is refused outright rather than truncated by the device.
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

// There is no sanitizeHostname here any more, and its removal is not tidying.
// It kept the `_` [driver.ValidHostname] forbids, silently dropped every other
// character it did not recognise, never trimmed the hyphens RFC 1123 rejects at
// the ends, and fell back to the literal `switch` on an empty result — four ways
// of disagreeing with the name the reconciliation loop pushes to the same device
// through hostname.go. [MgmtPushCmds] now calls
// [driver.HostnameFromDeviceName], the loop's own normalizer, which refuses
// rather than mangling. Do not re-introduce a local one: this drift began as
// exactly one such function per connector, each reasonable on its own.
