// Package instanton drives Aruba Instant On switches — first connector in the
// project, and first WEB-ONLY connector: these switches have no SSH, Telnet,
// or serial console. All administration goes through their web UI, plus SNMP
// for read.
//
// Capabilities and constraints below are not guesses: they are taken from the
// vendor guide (Aruba Instant On 1930 Switch Series — Management and Configuration
// Guide, ref. 5200-5879). Every numeric value is annotated with its source, because
// an invented capability eventually bricks a device.
package instanton

import (
	"regexp"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

func init() { driver.Register(&Driver1930{}) }

// Driver1930 covers the 1930 range (8, 24 and 48 ports, with or without PoE).
type Driver1930 struct{}

func (d *Driver1930) Info() driver.Info {
	return driver.Info{
		Name:   "aruba-instant-on-1930",
		Vendor: "Aruba (HPE)",
		Family: "Instant On 1930",
		// Web first: the only configuration path. SNMP is read-only, and only on
		// the management VLAN (cf. Mgmt.Notes).
		Transport: []string{"web", "snmp"},
		Doc:       "Aruba Instant On 1930 Management and Configuration Guide (5200-5879)",
	}
}

// Recognition patterns. A 1930 web page title is
// « Instant On - Hewlett Packard Enterprise » (observed on real hardware);
// product references JL68xA/JL80xA identify the range in sysDescr or a banner.
var (
	reInstantOn = regexp.MustCompile(`(?i)instant\s?on`)
	re1930      = regexp.MustCompile(`(?i)\b1930\b|JL68[0-9]A|JL80[0-9]A`)
	// The 1830 range shares the same web skin: it must be excludable, with different
	// capabilities.
	re1830 = regexp.MustCompile(`(?i)\b1830\b`)
)

// Fingerprint: « is this device for me? »
//
// Web title alone does not distinguish 1930 from 1830 — both ranges serve the
// same UI. We therefore match the FAMILY with moderate confidence, and the exact
// model is refined after login (dashboard exposes the reference). Claiming to
// recognize a 1930 from title alone would be wrong, and the resulting capabilities
// are too dangerous for that.
func (d *Driver1930) Fingerprint(ev driver.Evidence) int {
	var haystack = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if !reInstantOn.MatchString(haystack) {
		return 0
	}
	if re1830.MatchString(haystack) {
		return 0 // neighboring range, not ours
	}
	if re1930.MatchString(haystack) {
		return 90 // model named explicitly
	}
	// « Instant On » without range number: good candidate, to confirm.
	return 60
}

// 1930 capabilities, from the vendor guide.
//
// Limits that depend on port count (LAG) are derived from the model when known;
// otherwise we keep the LOWEST value in the range — refusing an intent the device
// would accept is an annoyance, accepting one it refuses is an outage.
func (d *Driver1930) Capabilities(model string) driver.Capabilities {
	var maxLAGs, portsPerLAG = 4, 4 // 8 ports: floor of the range
	switch {
	case strings.Contains(model, "48"):
		maxLAGs, portsPerLAG = 16, 8
	case strings.Contains(model, "24"):
		maxLAGs, portsPerLAG = 8, 4
	}

	return driver.Capabilities{
		// « IEEE 802.1Q VLANs up to 256 VLANs, VLANs 4094 and 4093 reserved »
		VlanRange: [2]int{1, 4092},
		MaxVlans:  256,
		Features: map[string]bool{
			"Trunk8021Q": true,
			"PVID":       true,
			"VoiceVLAN":  true, // « Auto Voice VLAN »
			"PoE":        strings.Contains(strings.ToUpper(model), "PoE") || strings.Contains(model, "P"),
			"LLDP":       true, // 802.1AB + LLDP-MED
			// CST Edge Port: Auto (default) / Enable / Disable — AdminEdgePort +
			// AutoEdgePort. STP State disables protocol participation per port.
			driver.FeatSTPEdge:       true,
			driver.FeatSTPEdgeAuto:   true,
			driver.FeatSTPPortEnable: true,
			// CST Port « BPDU Guard » (guide 5200-5879) — suspends port on BPDU.
			driver.FeatBPDUGuard: true,
			"IGMPSnooping":       true, // v1/v2
			"LACP":               true, // 802.3ad + static trunks
			"DHCPSnooping":       true,
			"DHCPRelay":          true,
			"ARPProtection":      true,
			"DoSProtection":      true,
			"PortSecurity":       true,
			"ProtectedPorts":     true,
			"Dot1X":              true, // 802.1x + guest VLAN
			"RADIUS":             true,
			"PortMirroring":      true,
			"IngressRate":        true, // per port
			"JumboFrames":        true, // 9216 L2 / 9000 routed
			"StaticRouting":      true, // IPv4, 32 entries
			"SNTP":               true,
			"LoopProtection":     true,
			"EEE":                true, // 802.3az
			"RMON":               true,
			// Storm control exists but is GLOBAL, not per port: an intent that
			// sets it per port is not applicable here.
			"StormControl":     true,
			"StormControlPort": false,
			// No CLI at all: no SSH, Telnet, or serial console. This product line
			// has no physical SERIAL PORT either — serial-console adoption, one of
			// discovery's three entry points (docs/01-Architecture/Discovery), is
			// therefore IMPOSSIBLE here. A device whose access is lost is recovered
			// via the reset button, not a console cable.
			"SSHMgmt":    false,
			"TelnetMgmt": false,
			"SerialCLI":  false,
			"SerialPort": false,
			"HTTPMgmt":   true,
		},
		// SpanningTreeGlobalParam.STPOperationMode: 0=STP, 2=RSTP, 3=MSTP;
		// enabled=2 turns the tree off (« none »). MSTP radio is hidden on k9
		// Instant On, but the code still answers when set.
		STPModes:     []string{"none", "stp", "rstp", "mstp"},
		MSTInstances: 8,
		// « ACLs (named ACL - IPv4 and MAC). Up to 50 ACLs. Up to 480 ACEs »
		MaxACLs:         50,
		MaxACEs:         480,
		MaxStaticRoutes: 32,
		MaxLAGs:         maxLAGs,
		PortsPerLAG:     portsPerLAG,
		// SNMPv2c ONLY on this product line, and that is an EXCEPTION.
		//
		// The vendor guide describes SNMPv3 (MD5/SHA-1, DES), and the firmware
		// embeds the code — sections `SNMPUserISList` and `SNMPGroupList` exist
		// and respond. But the page that would expose it is HIDDEN on this model
		// (`hidden:_top.isDeviceTypek9` in its bundle): the device offers operators
		// only v2c from « Get Connected », and nothing guarantees v3 writes are accepted.
		//
		// Announcing v3 would lead provisioning to generate a v3 label and secret that
		// apply would then refuse — an identity that exists in the database and nowhere
		// else is worse than a missing identity, because the screen shows it. We
		// therefore declare what the device ACTUALLY offers.
		//
		// The v3 mechanism (negotiation, labels per algorithm pair, secrets per label)
		// remains intact: it serves the rest of the fleet. This product line is the
		// exception, not the other way around.
		SNMPVersions: []string{"v1", "v2c"},
		// Product-line observability (cf. docs/01-Architecture/Unified monitoring and
		// pipelines §1). Three facts, all established above in this file: SNMP is
		// read-only and confined to the management VLAN; web UI exists but accepts
		// only five sessions, so polling it is out of the question; and there is no
		// streaming telemetry on this hardware.
		Monitoring: driver.MonitoringCapabilities{
			Channels: []driver.MonitorChannel{
				{
					Kind:        "snmp",
					Metrics:     []string{"if_state"},
					MinInterval: 5 * time.Second,
					Note: "SNMP is reachable only from the management VLAN: " +
						"the collector must be there.",
				},
				{
					// `if_counters` SEPARATELY, with its own 5 s floor. This is not a
					// guardrail workaround: it is the declaration becoming accurate.
					//
					// The former 30 s came from no measurement, but from GROUPING — four
					// metrics declared together all inherited the most expensive floor.
					// Nothing in guide 5200-5879 mandates 30 s for `ifHCInOctets`, and
					// this product line's « fragile under burst » note applies to the WEB
					// channel and its five sessions (cf. MaxSessions and channel below),
					// not SNMP, which is read-only and opens no admin session.
					//
					// 5 s is an OPERATOR decision, proven in the field on a 1930 48-port:
					// a six-column walk on 52 interfaces every five seconds does not move
					// the device's supervision CPU, whereas polling its web UI would evict
					// its own sessions. This is therefore NOT a value copied from another
					// product line — and anyone wanting to lower it further must measure it
					// themselves on hardware, as this one was.
					Kind:        "snmp",
					Metrics:     []string{"if_counters"},
					MinInterval: 5 * time.Second,
				},
				{
					// Health, LLDP and PoE KEEP 30 s, and that is the whole reason for the
					// split. Leaving them in the same channel as counters would poll them
					// at 5 s — the exact opposite of the design, which places health and
					// PoE at 5 min and network tables at 5-15 min
					// (docs/01-Architecture/Monitoring.md). A 30 s floor already gives
					// six times too much margin; there was no reason to loosen it further.
					//
					// `lldp` IS NOW READ, and the split bet paid off: policy asks for 60 s
					// (cf. api.wantedLLDPInterval), so this floor does not bite and did not
					// need renegotiation on collection delivery day. Measured since 29 July
					// 2026 — read-only walk on `sw-labo-1930` (10.99.200.101): LLDP-MIB is
					// complete there, `lldpRemPortDesc` included (which CRS326 does not
					// serve), 52 entries in `lldpLocPortTable`, three neighbors. Two traps
					// of this agent are documented in docs/01-Architecture/Voisinage LLDP.md:
					// its ports 50 and 52 advertise `lldpLocPortId` as `macAddress(3)` not
					// `interfaceName(5)`, and the chassis-id it EMITS is not the one it
					// DECLARES. Health and PoE are still not collected on this channel.
					Kind:        "snmp",
					Metrics:     []string{"health", "lldp", "poe"},
					MinInterval: 30 * time.Second,
				},
				{
					// Last resort when SNMP is unreachable. Floor is high because each read
					// consumes one of the device's FIVE sessions: polling its UI evicts the
					// human administrator.
					Kind:        "web",
					Metrics:     []string{"if_state"},
					MinInterval: 30 * time.Second,
					Note: "Each read consumes one session out of the five this " +
						"product line accepts — use only when SNMP is unavailable.",
				},
			},
			Traps:  true,
			Syslog: true,
			// No gNMI or equivalent on this product line.
			Streaming: false,
			// What this product line DOES NOT EXPOSE — declared here, next to the
			// channels it contradicts, so both can be read at a glance.
			//
			// ONE case only, deliberately: not the list of everything missing from the
			// UI. Health, LLDP and PoE have their channel above, the device serves them,
			// and if no curve appears it is meshcraft not reading them yet — a gap on our
			// side that a version fills. Listing them here would declare them impossible:
			// it would make operators give up on what only awaits a collection batch.
			Unavailable: []driver.MetricUnavailable{
				{
					Metric:   "optical_power",
					Observed: true,
					Source: "SNMP v2c observation on 1930 JL685A (firmware " +
						"InstantOn_1930_3.4.0.0, sysObjectID 1.3.6.1.4.1.11.2.3.7.11.199): " +
						"ENTITY-SENSOR-MIB (1.3.6.1.2.1.99) EMPTY — standard sensor path not served; " +
						"enterprise tree enumerated in full, 5001 OIDs under 1.3.6.1.4.1, all under " +
						"1.3.6.1.4.1.11.2, without one table indexed by SFP+ ifIndex (49 to 52); " +
						"1.3.6.1.4.1.89 (Radlan/Marvell, declared by sysORID) returns zero " +
						"rows — MIB capability declared, no readable data. Three of four SFP+ " +
						"(ifIndex 49, 50, 52) were up at observation time: optics present and links " +
						"up, so not an empty table for lack of module.",
				},
			},
		},
		// « HTTP and HTTPS sessions (5 sessions total) » — major operational
		// constraint: the connector must serialize access, otherwise it evicts the
		// human administrator from their own UI.
		MaxSessions: 5,
		// « Changes take effect immediately but are not retained across a switch
		// reset unless you click Save Configuration »: free rollback, provided you
		// apply, verify, THEN save.
		Commit: driver.CommitApplyThenSave,
		// Administration account.
		//
		// The product line can create named accounts, and meshcraft can now apply them:
		// `AdminUserSetting` with `add` to create, `set` to modify, `delete` to remove —
		// validated on 1930 JL685A (firmware 3.4.0.0).
		//
		// RSA encryption with `crypto_!` prefix feared by the previous note is NOT
		// mandatory: it is only one of three password field encodings, and the device
		// accepts plain `$` text, including at creation (cf. adminUserEntry).
		//
		// Renaming requires two gestures, the device indexing accounts by name: create
		// the new one, then delete the old. Order is not negotiable — see DeleteAccount.
		Admin: driver.AdminAccount{
			Custom:    true,
			FixedName: "admin",
			// « Password length: 8-64 characters » (guide 5200-5879).
			PasswordMin: 8,
			PasswordMax: 64,
			Notes: []string{
				"Account name: 1 to 20 characters (device form validation rule).",
				"Factory `admin` account is removed only AFTER the site account has " +
					"proven it opens a session: without serial port, reverse order would " +
					"mean factory reset.",
				"Password is NOT recoverable: losing it requires factory reset, " +
					"and this product line has no serial fallback.",
				"Changing password closes other sessions: with 5 sessions maximum, " +
					"this also avoids evicting the human administrator without warning.",
			},
		},
		Mgmt: driver.MgmtCapabilities{
			// « By default, the management VLAN ID is 1. The management VLAN can
			// be any value between 1 and 4092 »
			VlanIDs:   driver.VlanAny,
			VlanRange: [2]int{1, 4092},
			TaggedOK:  true,
			Static:    true,
			DHCP:      true,
			// « If DHCP is enabled but the DHCP server does not respond, the
			// default static IP address 192.168.1.1 is used »
			DHCPFallback: "192.168.1.1",
			// GATEWAY AND RESOLVERS: carried by the platform. Gateway is readable from
			// IPv4RouteList on adopted devices; DNS list write is not proven yet.
			// Addressing mode is read from IPv4InterfaceList owner (2=DHCP, 1=static).
			Gateway: true,
			DNS:     true,
			Routed:  true,
			IPARP:   true,
			// NO out-of-band fallback, and that fact governs all caution in this connector.
			//
			// Observed and documented: this product line has no SSH, Telnet, or serial
			// port — the installation guide mentions no console connector, and recovery
			// is the RESET BUTTON held five seconds, on site (cf. ResetProcedure below,
			// which offers no other `Hardware`). A management addressing mistake therefore
			// costs a site visit with a paperclip AND loss of all configuration.
			//
			// That is also why this platform's free rollback (`apply_then_save`) matters:
			// the only safety net is NOT saving.
			OutOfBand: false,
			// Dual-path VLAN migrate is SHIPPED and proven (prepare / commit / persist).
			MigrateVLAN: true,
			// Same-VLAN apply: DHCP and static (owner=1 observed on lab) via wcd.
			ApplySameVLAN: true,
			Notes: []string{
				"A VLAN with no member port (tagged or untagged) cannot become management VLAN: " +
					"create the VLAN and assign ports BEFORE designating it as management.",
				"SNMP is allowed only on the management VLAN: the telemetry collector " +
					"must be reachable in that VLAN.",
				"In DHCP, changing management VLAN acquires a new address: " +
					"the current session drops (documented by vendor).",
				"Only a static address is written to flash; a DHCP lease is not.",
			},
		},
	}
}

// ResetProcedure — from the product line installation guide.
func (d *Driver1930) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Hardware: "Recessed Reset button on faceplate, press with a thin object (paperclip): " +
			"press of 5 SECONDS OR MORE restores factory configuration. " +
			"Press under 5 seconds only reboots with self-test — it erases nothing.",
		Software: "Web UI: Maintenance > Reset to Factory Defaults (administrator access required).",
		Erases: []string{
			"all configuration made via web UI",
			"administrator password",
			"configured IP address",
		},
		AfterReset: []string{
			"user « admin », NO password (set on first access)",
			"DHCP client active; without DHCP response, fallback to 192.168.1.1/24",
			"home page offering choice between local management and Instant On cloud portal",
		},
		Pitfalls: []string{
			"NO serial port on this product line: no out-of-band console. " +
				"Reset button is the only recovery if access is lost.",
			"Power cycle does NOT reset IP address: if unknown, hardware reset is the only path.",
			"Administrator password is not recoverable: only factory reset removes it.",
			"After reset, device may switch to cloud mode if portal is followed: " +
				"to stay in local management, explicitly choose local management.",
		},
		Confirmed: true,
		Source:    "Aruba Instant On 1930 Series Installation and Getting Started Guide",
	}
}

// --- Onboarding: from factory-reset device to manageable device ---

// Stage describes where the device is in onboarding. Determining it requires only
// reads: nothing is modified until the operator chooses.
type Stage string

const (
	// StageChooseMode: post-reset home page, management mode not yet chosen.
	// Local management is triggered here.
	StageChooseMode Stage = "choose_mode"
	// StageSetPassword: local management chosen, administrator password to set
	// (device requires it after first login).
	StageSetPassword Stage = "set_password"
	// StageLogin: local UI in place, login requested.
	StageLogin Stage = "login"
	// StageCloudManaged: device attached to Instant On cloud portal.
	// meshcraft does not manage it in this state.
	StageCloudManaged Stage = "cloud_managed"
	// StageUnknown: unrecognized page — say so rather than guess.
	StageUnknown Stage = "unknown"
)

// Detect classifies the device home page. Deliberately conservative: better
// « unknown » and a question to the operator than a guess that triggers a POST
// to the wrong place.
func Detect(body string) Stage {
	var low = strings.ToLower(body)
	switch {
	case strings.Contains(low, "configure locally") ||
		strings.Contains(low, "manage locally") ||
		// 2020 guide names this button « CONNECT »; recent firmware shows
		// « Configure Locally ». Both exist in the field.
		(strings.Contains(low, "connect") && strings.Contains(low, "cloud")):
		return StageChooseMode
	case strings.Contains(low, "set password") || strings.Contains(low, "new password"):
		return StageSetPassword
	case strings.Contains(low, "password") && strings.Contains(low, "log in"):
		return StageLogin
	case strings.Contains(low, "cloud managed") || strings.Contains(low, "instant on portal"):
		return StageCloudManaged
	}
	return StageUnknown
}
