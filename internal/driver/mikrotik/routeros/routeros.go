// Package routeros drives MikroTik switches running RouterOS.
//
// The capabilities in this file are not copied from a product datasheet:
// they were OBSERVED on a CRS326-24G-2S+ running RouterOS 7.21beta3 (testing),
// serial number 945509EF00B0, reached through a meshcraft proxy tunnel.
// Each value carries the URL and response that establish it, or, failing that,
// states explicitly that it is DERIVED from the documentation.
//
// The distinction is not decorative. On this platform, a wrongly assumed
// capability is costly: RouterOS applies and persists immediately, and the VLAN
// model of a CRS3xx (bridge VLAN filtering with hardware offload) cuts off
// administration access at the first oversight. See the Mgmt comment, which is
// the part of this file to read before writing anything.
//
// What could NOT be verified, and must be kept in mind while reading: the only
// path to this device from the development host is a read-only tool restricted
// to GET/HEAD. No writes were therefore exercised on hardware. Write paths are
// coded, tested against captures stored in testdata/, and marked UNVERIFIED
// wherever they appear.
package routeros

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

func init() { driver.Register(&Driver{}) }

// Driver covers the CRS/CCR/RB range under RouterOS.
type Driver struct{}

func (d *Driver) Info() driver.Info {
	return driver.Info{
		Name:   "mikrotik-routeros",
		Vendor: "MikroTik",
		Family: "RouterOS",
		// The REST API (`/rest/...`, Basic authentication) is the reference
		// transport: structured JSON, where the CLI would require parsing output
		// formatted for a human — and the project's generic SSH probe fails on
		// this shell (see the header of rest.go).
		Transport: []string{"api", "ssh", "telnet", "serial", "snmp"},
		Doc: "MikroTik RouterOS documentation (help.mikrotik.com) — capabilities " +
			"observed on CRS326-24G-2S+ / RouterOS 7.21beta3 (testing)",
	}
}

// RouterOS names itself in its SSH banner (`SSH-2.0-ROSSSH`), in its web page
// title (`RouterOS`), and in sysDescr.
//
// The first two are OBSERVED on 10.99.0.54: the controller probe returns
// `"http_title":"RouterOS"` and `"ssh_banner":"SSH-2.0-ROSSSH"`. The title is
// served WITHOUT authentication, on the login page — so this is recognition that
// requires no credentials.
var (
	reRouterOS = regexp.MustCompile(`(?i)RouterOS|ROSSSH`)
	// SwOS shares the vendor but NOT the system: « MikroTik » alone does not
	// allow a conclusion, and picking the wrong connector means speaking REST to
	// a device that only has forms.
	reSwOS     = regexp.MustCompile(`(?i)SwOS`)
	reMikroTik = regexp.MustCompile(`(?i)MikroTik`)
)

func (d *Driver) Fingerprint(ev driver.Evidence) int {
	var haystack = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if reSwOS.MatchString(haystack) {
		return 0 // neighbor system, not ours
	}
	if reRouterOS.MatchString(haystack) {
		return 90
	}
	if reMikroTik.MatchString(haystack) {
		// Recognized vendor, undetermined system: candidate to confirm after
		// connection, never a certainty.
		return 55
	}
	return 0
}

// --- MikroTik nomenclature, and what we derive from it ---

// reDesignation splits the port groups of a MikroTik model number.
//
// The nomenclature is regular and documented in vendor product sheets: after the
// family, each group is written `<count><type>` — `CRS326-24G-2S+` is
// « 24 copper gigabit ports » then « 2 SFP+ ports ». Groups may be adjacent
// (`CRS354-48G-4S+2Q+`), hence a search for ALL occurrences rather than splitting
// on hyphens.
//
// Alternative order matters: `S\+` must be tried before `S`, otherwise
// « 2S+ » would be read as « 2 SFP » and throughput would be divided by ten.
var reDesignation = regexp.MustCompile(`(\d+)(XS\+|XQ\+|Q\+|S\+|XG|G|P|S)`)

// designation is what a product model number says about the front panel.
type designation struct {
	// Ports is the TOTAL number of switching ports.
	Ports int
	// PoE: the model number advertises PoE-out ports (group `P`, for example
	// `CRS328-24P-4S+`).
	PoE bool
	// Parsed says whether the model number was understood. False ⇒ we know
	// nothing about the front panel, and everything that depends on it must fall
	// back to a floor value.
	Parsed bool
}

// parseDesignation reads a MikroTik product model number.
//
// The leading family is SKIPPED before the search: « CRS326 » contains « 326 »,
// and counting it would yield 326 ports. The first hyphen separates family from
// port groups in the entire nomenclature.
func parseDesignation(model string) designation {
	var out designation
	var dash = strings.Index(model, "-")
	if dash < 0 {
		return out
	}
	var groups = reDesignation.FindAllStringSubmatch(model[dash:], -1)
	var g []string
	for _, g = range groups {
		var n, err = strconv.Atoi(g[1])
		if err != nil || n <= 0 || n > 256 {
			// A model number we cannot read must not spawn an imaginary front
			// panel: better to conclude nothing.
			continue
		}
		out.Ports += n
		out.Parsed = true
		if g[2] == "P" {
			out.PoE = true
		}
	}
	// Chassis suffix (`RM`, `IN`, `+RM`) does not change the front panel, but
	// some ranges mark PoE elsewhere than with a `P` group.
	if strings.Contains(strings.ToUpper(model), "POE") {
		out.PoE = true
	}
	return out
}

// RouterOS capabilities on a CRS-range switch.
//
// Convention for reading the comments that follow:
//
//	OBSERVED — seen on CRS326-24G-2S+ / 7.21beta3, with URL and response
//	DERIVED  — taken from RouterOS documentation, NOT seen on this hardware
//
// Captures backing OBSERVED values are in testdata/: they are the evidence for
// what is declared here, and tests replay them.
func (d *Driver) Capabilities(model string) driver.Capabilities {
	var face = parseDesignation(model)

	// Aggregation: both bounds depend on the front panel.
	//
	// MaxLAGs is DERIVED from port count, and it is an arithmetic bound, not a
	// vendor limit: an aggregate needs at least two ports, so 26 ports never
	// allow more than 13. When the model number is not understood, we fall back
	// to 4 — the prudent floor of the rule « an unknown capability is assumed
	// absent ».
	var maxLAGs = 4
	if face.Parsed && face.Ports >= 4 {
		maxLAGs = face.Ports / 2
	}
	// PortsPerLAG: 8, and we must state honestly what this number is worth.
	//
	// RouterOS publishes NO limit on the number of ports in a `/interface/bonding`,
	// and nothing in the observations allows measuring it — the menu is present and
	// EMPTY on this device. 8 is therefore a prudent FLOOR, the usual aggregation
	// group width of Marvell chips in this family (this one is a
	// `Marvell-98DX3236`, OBSERVED in `/interface/ethernet/switch`). A wider
	// intent will be refused: that is exactly what a floor means, and refusing
	// what the device would have accepted is only an inconvenience.
	var portsPerLAG = 8

	var features = map[string]bool{
		// OBSERVED — `/interface/bridge` : `"vlan-filtering":"true"`, and
		// `/interface/bridge/vlan` holds three entries with `tagged` lists.
		// This IS the VLAN model of this range (cf. Mgmt.Notes).
		"Trunk8021Q": true,
		// OBSERVED — `/interface/bridge/port` : `"pvid":"100"` on ether1 through ether24,
		// `"pvid":"1"` on both SFP+ ports.
		"PVID": true,
		// RouterOS does NOT have voice VLAN, and this absence must not be masked.
		//
		// What exists, OBSERVED in `/ip/neighbor/discovery-settings` :
		// `"lldp-med-net-policy-vlan":"disabled"`. This setting ANNOUNCES a VLAN to
		// LLDP-MED endpoints — it classifies no traffic, sets no PVID, and
		// recognizes no phone. A « voice VLAN » intent would expect the device to
		// place phone traffic in a VLAN without touching the port; this one merely
		// tells the phone which VLAN to use, and relies on it to comply. Declaring
		// the capability true on that basis would imply classification that will
		// not happen.
		"VoiceVLAN": false,
		// OBSERVED on this model: PoE-out ABSENT, and establishing that took two
		// requests rather than one (cf. restStatusMeaning) :
		//
		//	/rest/interface/ethernet/poe          → 500 Internal Server Error
		//	/rest/interface/ethernet/poe/settings → 400 « no such command prefix »
		//
		// Only the second concludes. The 500 alone proves nothing: `/interface/dot1x`
		// also returns 500 while `/interface/dot1x/server` returns `[]`. The 400
		// « no such command prefix », however, is the device response when the menu
		// PREFIX does not exist — the same class of response as an invented path.
		// Independent confirmation: `/system/health` publishes only
		// `cpu-temperature`, with no power rail.
		//
		// DERIVED for the rest of the range: PoE-out variants are named by a
		// `P` group (CRS328-24P-4S+). Nomenclature decides, for lack of having
		// such a model on hand.
		"PoE": face.PoE,
		// OBSERVED — `/ip/neighbor/discovery-settings` : `"protocol":"cdp,lldp,mndp"`,
		// `"mode":"tx-and-rx"`. And proof that it WORKS rather than being only
		// configured: `/ip/neighbor` contains a neighbor
		// `"discovered-by":"lldp"` with its full `system-description`
		// (« HPE Networking Instant On Switch 48p ... JL685A »).
		"LLDP": true,
		// /interface/bridge/port `edge` = auto|yes|no (spanning-tree edge).
		driver.FeatSTPEdge:     true,
		driver.FeatSTPEdgeAuto: true,
		// OBSERVED — `/interface/bridge` : `"igmp-snooping":"false"` (the field exists,
		// so the function does too), plus `"multicast-router":"temporary-query"` and
		// `"fast-leave":"false"` per port in `/interface/bridge/port`.
		"IGMPSnooping": true,
		// OBSERVED — `/interface/bonding` present and empty (`[]`). The menu exists, so
		// aggregation does too; RouterOS offers mode `802.3ad` there.
		"LACP": true,
		// OBSERVED — `/interface/bridge` : `"dhcp-snooping":"false"`, and per port
		// `/interface/bridge/port` : `"trusted":"false"`. Both halves of the
		// mechanism are there (the global switch and trusted-port marking).
		"DHCPSnooping": true,
		// OBSERVED — `/ip/dhcp-relay` present and empty (`[]`).
		"DHCPRelay": true,
		// RouterOS does NOT have dynamic ARP inspection, and this key names
		// precisely that.
		//
		// What it offers instead, OBSERVED: `arp` on each interface
		// (`"arp":"enabled"`, with other values `reply-only`, `proxy-arp`,
		// `local-proxy-arp`) and a `/ip/arp` table where entries can be pinned. By
		// combining `arp=reply-only` and static entries one obtains ARP locking —
		// but that is manual configuration, port by port, not the automatic
		// protection backed by DHCP snooping that the key denotes and that meshcraft
		// would apply. Declaring true here would accept an intent that nothing would
		// apply.
		"ARPProtection": false,
		// No function named « DoS protection » on this platform.
		//
		// The firewall exists (`/ip/firewall/filter` OBSERVED present and empty) but it
		// does NOT see switched traffic: `/interface/bridge/settings` returns
		// `"use-ip-firewall":"false"`, and enabling it would lift all traffic to the
		// CPU by DISABLING hardware offload — on a 24-port gigabit switch, that is
		// throughput collapse. A capability obtainable only by sacrificing switching
		// is not a capability we advertise.
		"DoSProtection": false,
		// No per-port MAC address limit on this platform.
		//
		// OBSERVED: `max-learned-entries` exists on the BRIDGE
		// (`"max-learned-entries":"auto"`) but appears in NONE of the 26
		// records in `/interface/bridge/port`. What exists per port is
		// `"learn":"auto"`, which disables learning entirely — that is not a limit,
		// it is a switch. Port security in the sense of the key
		// (« at most N MACs on this port ») has no setting here.
		"PortSecurity": false,
		// OBSERVED — `/interface/bridge/port` : `"horizon":"none"`. Bridge horizon
		// IS RouterOS port isolation: two ports sharing the same horizon exchange
		// nothing with each other while still talking to the rest of the bridge.
		// Complemented by `"auto-isolate":"false"` on the same record.
		"ProtectedPorts": true,
		// OBSERVED — `/interface/dot1x/server` and `/interface/dot1x/client` present and
		// empty (`[]`). Note, and this is the trap of this API: the parent menu
		// `/interface/dot1x` returns 500. Concluding absence from that 500 would
		// have been wrong.
		"Dot1X": true,
		// OBSERVED — `/radius` present and empty (`[]`), and `/user/aaa` returns
		// `"use-radius":"false"`: RADIUS authentication exists for 802.1X clients
		// as well as administration logins.
		"RADIUS": true,
		// OBSERVED — `/interface/ethernet/switch` : `"mirror-target":"none"`,
		// `"rspan":"false"`, `"rspan-ingress-vlan-id":"1"`, and per port
		// `"mirror-ingress":"false"`, `"mirror-egress":"false"`. Mirroring is in the
		// chip, so at no CPU cost.
		"PortMirroring": true,
		// OBSERVED — `/interface/ethernet` : `"bandwidth":"unlimited/unlimited"`, a
		// per-port rx/tx limiter. Complemented by `/queue/simple` present and empty.
		"IngressRate": true,
		// OBSERVED, ceiling included — `/interface` returns `"l2mtu":"1592"` and
		// `"max-l2mtu":"10218"` on all 26 ports. The current setting is therefore
		// already beyond the 1514 bytes of a standard Ethernet frame, and the device
		// PUBLISHES its own upper bound: 10218 bytes of layer-2 MTU.
		//
		// A methodological note, because it nearly cost a false declaration:
		// `max-l2mtu` is NOT served by `/interface/ethernet`, whose 87 fields were
		// enumerated first — it is served by `/interface`. Concluding « the ceiling
		// is not published » from examining only the ethernet menu was an error,
		// caught by testdata/ captures. That is exactly why this package stores
		// observations in files rather than comments: a test replaying the capture
		// showed the contradiction; a comment never would have.
		"JumboFrames": true,
		// OBSERVED — `/ip/route` carries a real static route:
		// `{"dst-address":"0.0.0.0/0","gateway":"10.99.0.1","static":"true"}`. This
		// is not an empty menu, it is routing in service.
		"StaticRouting": true,
		// OBSERVED — `/system/ntp/client` present: `{"enabled":"false",
		// "mode":"unicast","status":"stopped"}`. And the clock is correct
		// (`/system/clock`: Europe/Paris, dst-active), so synchronization works
		// elsewhere.
		"SNTP": true,
		// OBSERVED — `/interface/ethernet` : `"loop-protect":"default"`,
		// `"loop-protect-send-interval":"5s"`, `"loop-protect-disable-time":"5m"`,
		// `"loop-protect-status":"off"`. Also present on `/interface/vlan`.
		"LoopProtection": true,
		// No EEE (802.3az) on this platform.
		//
		// OBSERVED by ENUMERATION: the 87 fields of a `/interface/ethernet` record
		// were listed, none mention EEE, `green`, or 802.3az. Absence is established
		// on the full field set, not assumed from documentation silence.
		"EEE": false,
		// No RMON. RouterOS implements none of the RMON groups: there is no menu,
		// history table, or alarm in the RFC 2819 sense. Per-port counters exist and
		// are OBSERVED (rx-fcs-error, rx-fragment, rx-jabber, tx-collision, breakdown
		// by frame size…), but counters are not RMON — RMON is the device sampling
		// and alarming on its own, and this one does not.
		"RMON": false,
		// OBSERVED — and PER PORT, which is the important part.
		// `/interface/ethernet/switch/port` returns, for each port:
		// `"limit-broadcasts":"true"`, `"limit-unknown-multicasts":"false"`,
		// `"limit-unknown-unicasts":"false"`, `"storm-rate":"100"`.
		// Unlike the Instant On range, where storm control is GLOBAL, an intent that
		// sets it port by port is applicable here.
		"StormControl":     true,
		"StormControlPort": true,
		// OBSERVED — `/ip/service` : `ssh` on 22, not disabled; port 22 open;
		// banner `SSH-2.0-ROSSSH`.
		"SSHMgmt": true,
		// OBSERVED — `/ip/service` : `telnet` on 23, not disabled; port 23 open.
		"TelnetMgmt": true,
		// OBSERVED, and this is a MAJOR DIFFERENCE from the Instant On range:
		// `/port` returns `{"name":"serial0","baud-rate":"115200","data-bits":"8",
		// "parity":"none","stop-bits":"1","used-by":"Serial Console(#0)",
		// "line-state":"dtr,rts,dcd,dsr"}`. A serial console exists, it is armed,
		// and its line state shows a physical connector.
		//
		// The consequence goes beyond the checkbox: serial-console adoption, one of
		// the three discovery entry points, is POSSIBLE here. A RouterOS device
		// whose network access was lost does not require factory reset — a cable
		// suffices.
		"SerialCLI":  true,
		"SerialPort": true,
		// OBSERVED — `/ip/service` : `www` on 80, not disabled. And `www-ssl` is
		// `"disabled":"true"` WITH `"invalid":"true"` and `"certificate":"none"`: no
		// HTTPS until a certificate is installed (cf. Open in rest.go).
		"HTTPMgmt": true,

		// --- What RouterOS brings on its own, named and observed ---
		//
		// These keys fall outside the common connector vocabulary. They are added
		// because they describe REAL platform capabilities that no existing key
		// carries, and each is observed like the others. None is declared on
		// documentation faith alone.

		// OBSERVED — it is the transport of this entire connector, and the
		// `rest-api` policy appears in all three factory groups of `/user/group`.
		"RESTAPI": true,
		// OBSERVED — `/interface/bridge` : `"vlan-filtering":"true"`. Named separately from
		// Trunk8021Q because it is not the same thing: Trunk8021Q says « can carry
		// tagged VLANs », BridgeVLANFiltering says « does so through a bridge table,
		// with a CPU pseudo-port named `bridge` ». It is this second property that
		// governs operation order and cuts access when ignored.
		"BridgeVLANFiltering": true,
		// OBSERVED — `/interface/bridge/port` : `"hw":"true"` and `"hw-offload":"true"`
		// on all 26 ports. Switching is in the chip; any configuration that forces
		// traffic to the CPU (cf. DoSProtection) cancels it.
		"HardwareOffload": true,
		// OBSERVED — `/interface/ethernet/switch` : `"l3-hw-offloading":"false"` at chip
		// level, `"l3-hw-offloading":"true"` per port, and
		// `"l3-hw-offloading":"true"` on `/interface/vlan`. Inter-VLAN routing can
		// therefore stay in hardware.
		"L3HardwareOffload": true,
		// OBSERVED — `/interface/ethernet/switch/rule` present and empty (`[]`): an ACL
		// IN the chip, distinct from the CPU firewall. This is what would someday allow
		// filtering without destroying hardware offload. Neither its depth nor syntax
		// is established (cf. MaxACLs).
		"SwitchACL": true,
		// OBSERVED — `/interface/bridge/port` : `"tag-stacking":"false"`, plus
		// `"ether-type":"0x8100"` on the bridge and `"use-service-tag":"false"` on
		// `/interface/vlan`. 802.1ad double tagging is configurable.
		"QinQ": true,
		// OBSERVED — `/interface/bridge/port` : `"bpdu-guard":"false"`,
		// `"restricted-role":"false"`, `"restricted-tcn":"false"`.
		// Written via ApplySpanningTree (bpdu-guard=yes|no).
		driver.FeatBPDUGuard: true,
		// OBSERVED — `/interface/bridge` : `"mvrp":"false"`, and per port
		// `"mvrp-applicant-state":"normal-participant"`,
		// `"mvrp-registrar-state":"normal"`. Dynamic VLAN registration (802.1ak)
		// exists on this firmware.
		"MVRP": true,
		// OBSERVED — `/ip/service` : `winbox` on 8291, not disabled.
		"Winbox": true,
		// OBSERVED — `/ip/service` : `api` on 8728 and `api-ssl` on 8729, not
		// disabled. Both ports accept the TCP connection but do not speak HTTP: this
		// is MikroTik's binary protocol.
		//
		// This comment used to say « that this connector does not use »; api.go made
		// that false. The channel is implemented from MikroTik's published
		// specification, and it brings the TWO things REST cannot render: negotiated
		// link speed, and SFP cage optical diagnostics — both from
		// `/interface/ethernet/monitor`, which is a command and not a menu.
		// Do not confuse with `Winbox` just above: 8291 is a DIFFERENT protocol
		// still, encrypted and unspecified, that this connector does not implement.
		//
		// What is NOT verified: no byte of this protocol was exchanged with the
		// reference device; the tool available from the development host cannot open
		// a raw socket. The channel declared below reflects that in its floor and note.
		"BinaryAPI": true,
		// OBSERVED, and this is the fallback that changes everything compared to a
		// web-only switch — `/tool/mac-server` : `{"allowed-interface-list":"all"}`.
		// MAC-telnet and MAC-winbox reach the device on the same L2 segment WITHOUT
		// any IP address. A mis-addressed RouterOS is not lost (cf. Mgmt.Notes).
		"MACAccess": true,
		// OBSERVED — `/system/history` carries an undo journal with, for each entry,
		// `"undoable":"true"` and the reverse command in plain text
		// (`"undo":"/system package update set channel=development"`). It is the only
		// safety net this platform offers, and it is READABLE from REST; triggering
		// it is not (cf. Commit).
		"ConfigUndoJournal": true,
		// OBSERVED — `/system/scheduler` present and empty (`[]`), `/system/script`
		// present and empty, `/file` accessible. Together these three are what would
		// allow BUILDING a commit-confirmed workflow that the platform does not offer
		// natively (cf. Commit).
		"Scheduler": true,
	}

	return driver.Capabilities{
		// OBSERVED — `/interface/bridge/msti` returns `"vlan-mapping":"1-4094"`: the
		// device itself publishes the extent of its VLAN space.
		VlanRange: [2]int{1, 4094},
		// 4094, and this number means « no restriction beyond the identifier space »,
		// not « 4094 measured entries ».
		//
		// What is OBSERVED is the IDENTIFIER space (1-4094, above). The number of
		// SIMULTANEOUS entries the bridge table accepts is not measurable through a
		// read-only channel, and RouterOS does not publish it. Since MaxVlans only
		// serves to refuse before writing, and no plan can anyway exceed the
		// identifier space, keeping 4094 only refuses the impossible. Setting less
		// would refuse plans the device could hold.
		MaxVlans: 4094,
		Features: features,
		// OBSERVED for `mstp` — `/interface/bridge` : `"protocol-mode":"mstp"`, and
		// `/interface/bridge/msti` exists with one instance. DERIVED for `stp`,
		// `rstp`, and `none`: these are the other documented values of the same
		// `protocol-mode` field. RouterOS can disable spanning tree (`none`) or
		// run STP / RSTP / MSTP on the bridge.
		STPModes: []string{"none", "stp", "rstp", "mstp"},
		// 8 instances, DERIVED — treat with caution.
		//
		// OBSERVED: only ONE instance exists on this device,
		// `{"identifier":"0","dynamic":"true","vlan-mapping":"1-4094"}` — the common
		// instance, created by the device itself. DERIVED: the `identifier` field of
		// `/interface/bridge/msti` is documented on 0..7, i.e. 8 instances.
		//
		// Nothing in the observations confirms that a second instance is accepted, or
		// that it remains offloaded in hardware if it is. A plan requesting more than
		// one MST instance must therefore be proven on hardware before production
		// deployment: this is a documentation number in a field used to refuse, the
		// only category of value that can let through an intent the device will reject.
		MSTInstances: 8,
		// 0 for both, and this is an assumed refusal rather than ignorance.
		//
		// RouterOS has THREE filtering planes, all three OBSERVED present and empty:
		// `/ip/firewall/filter` (CPU, IP), `/interface/bridge/filter` (CPU, L2), and
		// `/interface/ethernet/switch/rule` (in the chip). Neither CPU plane has a
		// fixed limit — they are bounded by memory, of which 432 MiB remain free
		// (OBSERVED). Hardware TCAM depth is published neither by MikroTik nor by the
		// device, and cannot be measured through a read-only channel.
		//
		// The contract has no value for « no fixed limit »: 0 means refuse everything.
		// That is what we keep, and it is the right choice HERE because this connector
		// writes NO ACL — advertising a number would advertise a capability nobody
		// honors, exactly the « capability that lies » that capabilities.go describes.
		// The contract gap is flagged in the delivery report, not filled with an
		// invented number.
		MaxACLs: 0,
		MaxACEs: 0,
		// 1000, and the asymmetry with MaxACLs above is INTENTIONAL.
		//
		// Same starting situation — RouterOS imposes no fixed limit, routing is
		// bounded by memory — but the conclusion differs because StaticRouting is
		// declared TRUE and OBSERVED in service (`/ip/route` carries a default static
		// route to 10.99.0.1). Keeping 0 would refuse every route on a device that
		// carries one before our eyes: that would contradict the feature just above.
		//
		// 1000 is therefore an OPERATIONAL prudence bound, not a device limit: beyond
		// that, look at the site plan, not switch capacity. Do not read it as an
		// observation.
		MaxStaticRoutes: 1000,
		MaxLAGs:         maxLAGs,
		PortsPerLAG:     portsPerLAG,
		// SNMP v1 and v2c ONLY, and not because v3 does not exist.
		//
		// What is OBSERVED, and argues FOR v3:
		//
		//	/snmp            → "enabled":"true", and especially "engine-id":"80003a8c04".
		//	                   An engine ID only makes sense for SNMPv3: the machinery
		//	                   is there, initialized, on this exact firmware.
		//	/snmp/community  → fields "security":"none",
		//	                   "authentication-protocol":"MD5",
		//	                   "encryption-protocol":"DES" exist on the served entry.
		//	                   This is indeed the menu through which RouterOS carries its
		//	                   v3 users (there is no /snmp/user: that path returns 500).
		//
		// Yet we declare v1/v2c. Two reasons, in this order:
		//
		//  1. NO write could be exercised on this device. Yet the SNMPVersions comment
		//     in the contract is unambiguous: advertising v3 would lead provisioning to
		//     generate a v3 label and secret that deployment might reject, and « an
		//     identity that exists in the database and nowhere else is worse than a
		//     missing identity ».
		//  2. The algorithms actually accepted could not be established. The MD5 and DES
		//     values observed are field DEFAULTS, not the list of what it admits; this
		//     API's enumeration validation was probed in read-only mode (GET filters on
		//     absurd values) and it answers nothing — it filters without validating.
		//     Declaring SNMPv3Auth: ["MD5"] and SNMPv3Priv: ["DES"] on that basis alone
		//     would be worse than staying silent: policy would choose label
		//     `snmpv3-md5-des`, therefore the WEAKEST algorithm pair, because of our
		//     under-declaration. A capability advertised too low is not always on the
		//     safe side.
		//
		// This is therefore the same trade-off the Instant On connector made, for the
		// same reason, and it is REVISABLE: this is the only declaration in this file
		// that a successful write campaign must change. The exact procedure is in the
		// delivery report.
		SNMPVersions: []string{"v1", "v2c"},
		Monitoring:   monitoringFor(face),
		// 20 sessions, OBSERVED — `/ip/service` : `"max-sessions":"20"` on the `www`
		// entry, the same bound on `ssh`, `telnet`, `winbox`, and `api`.
		//
		// That is four times an Instant On budget, but it is NOT an invitation to
		// spend it. Observed during the exploration campaign: `/ip/service` listed up
		// to TWELVE simultaneous dynamic `www` connections from the proxy alone,
		// while only one read was requested at a time — connections accumulate faster
		// than they close. The connector must therefore reuse its connection (as
		// NewWebSession does, capped at 2) rather than counting on the 20.
		MaxSessions: 20,
		Commit:      driver.CommitImmediate,
		Mgmt: driver.MgmtCapabilities{
			// OBSERVED, and in the most direct way possible: administration of this
			// device arrives on a non-trivial TAGGED VLAN. `/ip/address` carries
			// `10.99.0.54/24` on interface `vlan99`, `/interface/vlan` returns
			// `{"name":"vlan99","vlan-id":"99","interface":"bridge"}`, and
			// `/interface/bridge/vlan` places VLAN 99 in `"tagged":
			// "bridge,sfp-sfpplus2"`. All reads of this connector took this path: the
			// capability is not derived, it is the support of the evidence.
			VlanIDs:   driver.VlanAny,
			VlanRange: [2]int{1, 4094},
			TaggedOK:  true,
			// OBSERVED — `/ip/address` : the management entry is `"dynamic":"false"`,
			// therefore static.
			Static: true,
			// OBSERVED — `/ip/dhcp-client` present and empty (`[]`): the DHCP client
			// exists, it is simply not used here.
			DHCP: true,
			// EMPTY, and this is an important difference from the Instant On range.
			//
			// RouterOS has NO fallback address when DHCP does not answer: the interface
			// stays without address, period. The `192.168.88.1/24` that `/ip/address`
			// carries on the bridge (OBSERVED, with `"comment":"defconf"`) is the
			// FACTORY CONFIGURATION address, not a DHCP fallback — it is there because
			// nobody removed it, and it would disappear if we did. Confusing them would
			// make us look for a device at an address it never took.
			DHCPFallback: "",
			// GATEWAY AND RESOLVERS: the platform carries them, and this is what
			// radically distinguishes it from SwOS — a CRS326 on RouterOS ROUTES, so it
			// is not trapped on its own subnet. Menus are `/ip/route` (default route)
			// and `/ip/dns` (`servers` and `dynamic-servers`).
			//
			// DERIVED FROM DOCUMENTATION, not observed: the exploration campaign captured
			// neither on the reference device. That is a strong source — both menus have
			// been part of RouterOS forever — but the distinction is held through to
			// mgmtaddr.go, where their read is optional and failure is reported as a
			// note instead of failing the call.
			Gateway: true,
			DNS:     true,
			Routed:  true,
			IPARP:   true,
			// ONE OUT-OF-BAND FALLBACK EXISTS, and this range is the only one of the
			// three delivered in that case — which changes the GRAVITY of a management
			// addressing mistake, without changing caution.
			//
			// Two paths, both OBSERVED on the reference device:
			//
			//   - SERIAL CONSOLE. `/system/routerboard` and `/system/console` describe it,
			//     and the CRS326 has a serial connector on the front panel;
			//   - MAC-WINBOX. `/tool/mac-server` and `/tool/mac-server/mac-winbox`
			//     respond with an active entry on `all` interfaces: the device is then
			//     administered BY ITS MAC ADDRESS, at layer 2, without any IP address or
			//     management VLAN needing to be correct. That is exactly the fallback
			//     Instant On and SwOS lack.
			//
			// Alongside, the undo journal `/system/history` carries entries with
			// `undoable: true` and the reverse command in plain text — readable from REST,
			// but its TRIGGER was not exercised, so this connector does not offer it
			// (cf. the section on [driver.Persister] in contract.go).
			//
			// This field relaxes NO safeguard: it only stops the UI from reciting « the
			// only fallback would be factory reset » to a platform that recovers with a
			// console cable.
			OutOfBand: true,
			// Same REST Add/Set/Remove channel as port apply (proven). Same-VLAN
			// addressing and dual-path VLAN migrate follow Mgmt.Notes order;
			// OutOfBand (serial / MAC-winbox) is the recovery net.
			ApplySameVLAN: true,
			MigrateVLAN:   true,
			Notes: []string{
				"VLAN MODEL: on a CRS3xx, the management VLAN is not a separate " +
					"interface — it is an `/interface/vlan` placed ON the bridge, plus " +
					"an `/interface/bridge/vlan` entry listing member ports. Observed: " +
					"VLAN 99 `tagged: bridge,sfp-sfpplus2`, then `vlan99` on `bridge`, then " +
					"the address on `vlan99`. Three objects, three menus.",
				"THE WORD `bridge` IN THE TAGGED LIST IS THE CPU. Forgetting it is THE mistake that " +
					"mutes the device: frames reach the chip and keep being switched between ports — " +
					"site traffic does not stop — but they NEVER reach administration. The failure " +
					"therefore shows only on access, which is the hardest to diagnose remotely. " +
					"Observed: `\"tagged\":\"bridge,sfp-sfpplus2\"` on VLAN 99, the one carrying " +
					"the session.",
				"OPERATION ORDER, non-negotiable: (1) create the `/interface/bridge/vlan` entry " +
					"with the uplink port AND `bridge` in `tagged`; (2) create `/interface/vlan` on " +
					"the bridge; (3) set `/ip/address`; (4) verify that the new address responds; " +
					"(5) ONLY THEN remove the old one. Each step before 4 leaves current access " +
					"intact; reversing 4 and 5 sacrifices it before knowing whether the replacement works.",
				"Setting `vlan-filtering` BEFORE `/interface/bridge/vlan` entries cuts everything " +
					"instantly, tagged or untagged. Observed on this device: `vlan-filtering` is " +
					"ALREADY `true` — so any missing entry is paid for immediately, there is no grace period.",
				"FALLBACK WITHOUT IP, and this is what distinguishes this platform from a web-only " +
					"switch: MAC-winbox and MAC-telnet reach the device on the same L2 segment without " +
					"any address (observed `/tool/mac-server`: `allowed-interface-list: all`), and the " +
					"serial console exists (observed `/port`: `serial0`, 115200 8N1). An addressing " +
					"mistake here is not worth factory reset.",
				"REST is in CLEAR on port 80: `www-ssl` is disabled AND `invalid` for lack of a " +
					"certificate (observed), and port 443 is not open. The Basic password holds only " +
					"through the proxy tunnel.",
			},
		},
		Admin: driver.AdminAccount{
			// OBSERVED — `/user` is a LIST of records carrying a `name` field
			// (`{"name":"admin","group":"full","comment":"system default user"}`), and
			// `/user/group` offers three. RouterOS is a multi-account system: placing a
			// named `mcadmin` there is the normal case, not an exception.
			Custom: true,
			// OBSERVED — the factory account is named `admin` and declares itself
			// « system default user ».
			FixedName: "admin",
			// 0 for both bounds, and here 0 is an OBSERVED value poorly served by the contract.
			//
			// OBSERVED — `/user/settings` : `{"minimum-password-length":"0",
			// "minimum-categories":"0"}`. RouterOS therefore imposes NO factory minimum:
			// it even accepts an empty password. The contract, however, reads 0 as
			// « unknown » — the two are indistinguishable here, and that is flagged in the
			// delivery report.
			//
			// What matters more than the value: these two bounds are a SITE SETTING, not a
			// hardware property. An operator who raised `minimum-password-length` to 12
			// will reject a secret meshcraft considers good. That is why EnsureAccount READS
			// `/user/settings` before setting, instead of assuming 0 still holds.
			PasswordMin: 0,
			PasswordMax: 0,
			// OBSERVED in the best way: the password that authenticated the entire
			// observation campaign contains an at-sign. Punctuation passes; this is not
			// deduction.
			AlphanumericOnly: false,
			Notes: []string{
				"THE ACCOUNT MUST CARRY THE `rest-api` POLICY, otherwise meshcraft cuts its own " +
					"access path by placing the account meant to open it. Observed: all three factory " +
					"groups (`read`, `write`, `full`) carry it — but a custom group built by the " +
					"operator may not.",
				"Writing configuration requires the `write` policy; touching accounts and groups " +
					"requires `policy`. Observed: only the `full` group carries `policy`. A meshcraft " +
					"account placed in `write` can therefore set VLANs but NEITHER create the next " +
					"account NOR remove `admin` — and it learns that from a 403, not when choosing the group.",
				"No factory password minimum (observed `minimum-password-length: 0`), but this is a site " +
					"setting: read `/user/settings` before setting.",
				"The factory `admin` account is removed only AFTER a new account has proven it opens a " +
					"REST session — not after a return code, after a real authenticated read. The " +
					"difference from the Instant On range is that here the mistake is not fatal: serial " +
					"console and MAC-winbox remain.",
				"RouterOS does not expose a maximum account name length in its observations: this " +
					"connector therefore does not bound the name, and lets the device refuse what it " +
					"refuses rather than inventing a validation rule.",
			},
		},
	}
}

// monitoringFor describes platform observability.
//
// The floors below are MEASURED on hardware, not copied from another connector.
// The measurement campaign, done through the proxy tunnel on the CRS326-24G-2S+
// running 7.21beta3:
//
//	at idle, no request              → cpu-load 1 %
//	5 chained /rest/interface reads  → cpu-load 7 %
//	  with no pause, repeated        → cpu-load 25 %, stable
//	median of /rest/interface        → 111 ms (29 interfaces, 12.7 KiB)
//	median of /rest/interface/bridge/port → 108 ms (26 ports)
//	median of /rest/system/resource  → 58 ms
//
// What follows from this, and what fixes the numbers: a full interface read costs
// about 111 ms end to end and about 3 % of a CPU-second on the device (25 % load
// for about nine requests per second). At a 5 s cadence, that is less than 1 % of
// switch CPU for all counters on its 26 ports. That ratio allows 5 s — and the 25 %
// measured in burst forbids going much lower: a fleet of two hundred devices pushed
// to a tenth of a second would consume a quarter of each device's CPU permanently
// to reread values that, for the most part, do not change.
func monitoringFor(face designation) driver.MonitoringCapabilities {
	var out = driver.MonitoringCapabilities{
		Channels: []driver.MonitorChannel{
			{
				// The reference channel of this platform, and its structural advantage:
				// ONE request returns the state of ALL interfaces. Observed —
				// `/rest/interface` serves all 29 interfaces at once, each with its
				// `running`. Where an SNMP walk queries column by column, here the full
				// switch state fits in one 111 ms GET.
				Kind:        "web",
				Metrics:     []string{"if_state"},
				MinInterval: 5 * time.Second,
				Note: "A single REST request returns the state of all 29 interfaces " +
					"(observed: 111 ms median through the tunnel).",
			},
			{
				// `if_counters` separately, with its own floor, and not by mimicking the
				// Instant On connector: because the SOURCE is the same request as
				// `if_state`.
				//
				// Observed — `/rest/interface` already carries, for each interface,
				// rx-byte/tx-byte/rx-packet/tx-packet, and `/interface/ethernet` adds
				// error counters (rx-fcs-error, rx-fragment, rx-jabber, tx-collision,
				// breakdown by frame size). Counters therefore cost NOTHING more than
				// state: it is the 111 ms measurement above, for both.
				//
				// The channel stays separate anyway, and that is deliberate: the day
				// detailed error counters are read through `/interface/ethernet` — a second
				// request — this channel will carry its own cost, and must be able to move
				// without dragging `if_state`. A shared channel imposes its floor on
				// everything it groups; that is exactly how a metric gets throttled
				// unintentionally.
				Kind:        "web",
				Metrics:     []string{"if_counters"},
				MinInterval: 5 * time.Second,
			},
			{
				// LLDP at 30 s, and the number comes from the DEVICE, not from us.
				//
				// Observed — `/ip/neighbor/discovery-settings` :
				// `"discover-interval":"30s"`. That is the cadence at which RouterOS
				// rebuilds its neighbor table itself. Reading it more often can reveal
				// NOTHING new: we would pay for a request to reread a table the device has
				// not yet touched.
				//
				// This is the best-founded floor in this file — it is neither negotiated
				// nor merely prudent; it is imposed by how the source works. Anyone who
				// wanted to lower it should first lower `discover-interval` on the device.
				Kind:        "web",
				Metrics:     []string{"lldp"},
				MinInterval: 30 * time.Second,
				Note: "Cadence imposed by the device: its own discovery runs at 30 s " +
					"(`/ip/neighbor/discovery-settings`, discover-interval). Polling faster " +
					"rereads an unchanged table.",
			},
			{
				// Health at 60 s, and we must say what « health » means here — because it
				// is little.
				//
				// Observed — `/system/health` returns ONLY ONE value on this board:
				// `[{"name":"cpu-temperature","type":"C","value":"73"}]`. No voltage, no
				// fan, no power rail. The rest of what deserves the name health comes from
				// `/system/resource`: cpu-load, free-memory, uptime.
				//
				// 60 s because chip temperature is thermal mass — it cannot move usefully
				// in one minute — and because cpu-load and free-memory are INSTANTANEOUS
				// samples: reading them every five seconds would give noise, not a curve.
				// Design places health at 5 min; 60 s already leaves five times too much
				// margin, and costs two 58 ms requests.
				Kind:        "web",
				Metrics:     []string{"health"},
				MinInterval: 60 * time.Second,
				Note: "On this board, `/system/health` publishes only processor temperature " +
					"(observed); cpu-load and memory come from `/system/resource`.",
			},
			{
				// The BINARY API (8728/8729), the channel of api.go.
				//
				// It is NOT a duplicate of REST: it brings the only two things REST cannot
				// render, both from `/interface/ethernet/monitor`, which is a COMMAND and
				// not a menu.
				//
				//  1. NEGOTIATED link speed. Seen on captures: neither the 87 fields of
				//     `/interface/ethernet` nor those of `/interface` carry `speed`. There is
				//     only `advertise`, which says what the port OFFERS. A port fallen to 100
				//     Mbit/s because of a bad pair is therefore invisible to REST — the most
				//     insidious copper failure mode, since everything works, ten times slower.
				//  2. SFP+ cage OPTICAL diagnostics: module temperature, voltage, bias
				//     current, transmitted and received power.
				//
				// FLOOR AT 30 s, and it is HIGHER than REST although the channel is more
				// capable. Two cumulative reasons:
				//
				//   - COST is PER PORT, not per device. `monitor` queries one port at a time,
				//     and optical diagnostics are read on the module I²C bus — the slowest on
				//     the board. Where one REST read returns all 29 interfaces in 111 ms,
				//     four cages need four commands;
				//   - NO BYTE OF THIS PROTOCOL WAS EXCHANGED with the reference device: the
				//     tool available from the development host cannot open a raw socket. What
				//     is observed is that the service listens.
				//
				// The split is REAL, as on web channels: this channel carries state and
				// counters, both from an `/interface/print` — therefore the same cost.
				// Health and optics each have their own, just below, because they do not cost
				// the same: grouping would have given temperature the floor of a traffic
				// counter, and that is exactly the defect the Instant On connector paid for.
				Kind:        "api",
				Metrics:     []string{"if_state", "if_counters"},
				MinInterval: 30 * time.Second,
				Note: "Binary API 8728 (8729 with TLS). ONLY channel on this platform that " +
					"returns NEGOTIATED link speed and SFP cage optical diagnostics, both via " +
					"`/interface/ethernet/monitor` — a command, therefore outside menu read scope. " +
					"Implemented from the vendor specification and tested on simulated equipment: " +
					"no byte was exchanged with hardware.",
			},
			{
				// OPTICAL diagnostics through the binary API.
				//
				// 30 s, and the number comes from the PHENOMENON, not the protocol: transceiver
				// diagnostic registers are not in switch memory, they are read on the module
				// I²C bus — the slowest on the board. And an optical budget drifts over days:
				// a read every half minute is already far finer than what the quantity demands.
				// Same floor, for the same physical reason, as the neighboring SwOS connector
				// on its /sfp.b page.
				//
				// Cost is PER CAGE: one `monitor` command per optical port. Two requests on
				// this model, four on a CRS354.
				Kind:        "api",
				Metrics:     []string{"optical_power"},
				MinInterval: 30 * time.Second,
				Note: "One `/interface/ethernet/monitor` command PER CAGE. Returns module " +
					"temperature, supply voltage, bias current, and both optical powers — which " +
					"RouterOS already serves converted, in dBm, unlike SNMP and SwOS.",
			},
			{
				// HEALTH through the binary API: `/system/resource` and `/system/health`, two
				// commands for the whole device.
				//
				// 60 s, the same floor as the web channel, for the same reasons — chip
				// temperature is thermal mass, cpu-load and free-memory are instantaneous
				// samples that would make noise at five seconds. The channel exists separately
				// because TRANSPORT differs, not because cost changes.
				Kind:        "api",
				Metrics:     []string{"health"},
				MinInterval: 60 * time.Second,
				Note: "Same values as the web channel (`/system/resource` and `/system/health`), " +
					"via the binary API. Useful when that is already the open session for reading " +
					"speed or optics: two extra commands on an existing connection.",
			},
			{
				// SNMP: the channel that PERIODIC COLLECTION actually uses.
				//
				// What is OBSERVED: `/snmp` returns `"enabled":"true"`,
				// `/snmp/community` carries an entry with `"read-access":"true"` open to
				// `"addresses":"::/0"`, and `/ip/service` shows service `snmp` listening on
				// 161/udp. Configuration is therefore there, complete and usable.
				//
				// What is NOT observed, and would be dishonest to hide: NO OID was read on
				// this device. The only channel available from the development host is
				// read-only HTTP; SNMP is UDP, it does not pass through. This channel is
				// therefore declared on CONFIGURATION faith, not on collection.
				//
				// # THE FLOOR WAS CHANGED FROM 60 s TO 10 s, and we must say why
				//
				// The original 60 s were NOT a cost measurement. The comment they carried
				// said frankly: they were high « so that `Best` always returns the REST
				// channel », i.e. to DELIBERATELY LOSE an arbitration race, an unproven
				// channel must not become the main path of a fleet « by the mere effect of a
				// smaller number ». The intent was right; the means produced a defect.
				//
				// The defect: product periodic collection runs on the proxy and speaks ONLY
				// SNMP. The REST channel therefore won no race — it was never used — it only
				// LENT its 5 s floor to SNMP polling. One channel floor was pitted against
				// another, which is exactly what a floor is not, and the consequence is that
				// the CRS326 was polled in SNMP twelve times faster than this line allowed.
				//
				// The means is replaced by the mechanism that fits:
				// `MonitoringCapabilities.BestOfKind` queries the channel BY KIND, so SNMP
				// poll scheduling reads the SNMP floor and that alone. The original intent
				// is preserved without the detour — a channel can no longer win a race that
				// does not exist.
				//
				// 10 s, and this is a DERIVED-BY-COMPARISON floor, assumed as such since
				// nothing could be measured here: the Instant On connector declares 5 s on
				// the faith of a measurement on a 1930 48-port (« a six-column walk on 52
				// interfaces every five seconds does not move its supervision CPU »). This
				// device is a 26-port dual-core ARM at 800 MHz (observed `cpu-count: 2`,
				// `cpu-frequency: 800`): the walk is twice as short on a more capable CPU.
				// 5 s would therefore be defensible; 10 s is kept because derivation is not
				// measurement, and margin costs two curve points per minute.
				Kind:        "snmp",
				Metrics:     []string{"if_state", "if_counters"},
				MinInterval: 10 * time.Second,
				Note: "Periodic collection channel: it is the only one the proxy knows how to " +
					"use. Declared from observed configuration (snmp enabled, read community, service " +
					"161/udp listening) and not from collection — NO OID could be read from the " +
					"development host. Floor DERIVED by comparison with the 1930, where 5 s was " +
					"measured on a walk twice as long.",
			},
			{
				// HEALTH through SNMP: `sysUpTime`, `hrProcessorLoad`, `hrStorage`, and the
				// `mtxrHealth` branch of MIKROTIK-MIB.
				//
				// 60 s, and this is a floor that protects for a reason specific to this
				// channel: MIKROTIK-MIB sensors are SCALARS in the same branch, which the
				// poller must therefore walk one by one — sixteen requests per cycle (cf.
				// internal/snmppoll/health.go, which numbers the split). Grouping this metric
				// with counters would have given it their 10 s floor, therefore allowing one
				// hundred sixty requests per minute per device for values that do not move.
				Kind:        "snmp",
				Metrics:     []string{"health"},
				MinInterval: 60 * time.Second,
				Note: "Temperatures, voltages, fans, and power supplies come from the " +
					"PROPRIETARY `mtxrHealth` branch — there is no standardized switch health MIB. " +
					"Their SCALE FACTORS are DERIVED (the divisor of 10 comes from two reference " +
					"collectors, not a read): processor temperature cross-checks at a glance with " +
					"the `72` REST returns from `/system/health`. Processor load and memory go through " +
					"HOST-RESOURCES, sixteen walks total.",
			},
			{
				// OPTICAL diagnostics through SNMP: `mtxrOpticalTable`.
				//
				// 30 s, the same floor as through the binary API for the same physical reason —
				// the module I²C bus. This channel however has a structural advantage over its
				// neighbor: it is a real TABLE, indexed by interface index, so one column is
				// walked at once for ALL cages, whereas `monitor` needs one command per port.
				Kind:        "snmp",
				Metrics:     []string{"optical_power"},
				MinInterval: 30 * time.Second,
				Note: "PROPRIETARY table `mtxrOpticalTable`, indexed by interface index: one walked " +
					"column returns all cages at once. Its scale factors, however, are PUBLISHED by " +
					"the MIB (IDiv1000 on powers, GDiv100 on wavelength) — therefore of a different " +
					"nature than those of mtxrHealth. Powers are SIGNED integers in thousandths of dBm: " +
					"optical power is almost always negative. NO OID was read on the reference device: " +
					"this channel is declared from the vendor MIB and observed configuration.",
			},
			{
				// NEIGHBORHOOD through SNMP, and this channel is MEASURED — the first in this
				// list to be so OID by OID.
				//
				// It adds to the `web` channel that already declared `lldp` (higher in this same
				// list, on the faith of `/ip/neighbor`), and does not replace it: both exist
				// for real. This one was nevertheless needed, because product periodic collection
				// runs on the PROXY and speaks only SNMP — an `lldp` declared only as `web` was
				// therefore collected by nobody. That is exactly the defect `BestOfKind` fixed
				// for `if_state`: a channel does not lend its floor to another, and it does not
				// lend its metric either.
				//
				// OBSERVED on 29 July 2026 on `lab-crs326` (10.99.0.54, RouterOS 7.21beta3),
				// by read-only walk through the proxy:
				//
				//	lldpLocChassisId    « B8:69:F4:5B:A0:D2 » — TEXT, not bytes
				//	lldpLocSysName      « lan-r4 »
				//	lldpLocPortTable    2 entries (26 « sfp-sfpplus2 », 28 « vlan99 »)
				//	lldpRemTable        5 neighbors, columns 5, 6, 7, 9, 10, 11, 12
				//	lldpRemPortDesc     ABSENT — column 8 walk empty
				//
				// Two facts from this observation decide how to read this MIB, and they are
				// written in detail in docs/01-Architecture/Voisinage LLDP.md: chassis-id is
				// served as TEXT here and as raw bytes by the 1930 for the same chassis, and
				// `lldpLocPortTable` contains only interfaces where LLDP runs — two out of
				// twenty-nine. « No neighbor on ether3 » is therefore not « nothing is plugged
				// into ether3 ».
				//
				// 30 s, and this is the best-founded floor in the file — the same as the `web`
				// channel, for the same reason and it is not merely prudent:
				// `/ip/neighbor/discovery-settings` announces `discover-interval: 30s`, so the
				// device rebuilds its table only at that rate. Reading more often can reveal
				// NOTHING new.
				Kind:        "snmp",
				Metrics:     []string{"lldp"},
				MinInterval: 30 * time.Second,
				Note: "MEASURED channel (walk on 29/07/2026 on 10.99.0.54), unlike the three other " +
					"SNMP channels of this connector. `lldpRemPortDesc` is ABSENT from this agent: the " +
					"remote port is read only in `lldpRemPortId`. And `lldpLocPortTable` lists only " +
					"interfaces where LLDP runs — two out of the twenty-nine declared by `ifTable` — so " +
					"absence of a neighbor on a port says nothing about what is plugged into it. Cadence " +
					"imposed by the device (`discover-interval` 30 s).",
			},
		},
		// OBSERVED — `/snmp` carries `trap-target`, `trap-version` (`"1"`),
		// `trap-community`, and `trap-generators`. The mechanism exists.
		//
		// BUT read the value: `"trap-generators":"temp-exception"`. On this device, the
		// only traps generated are TEMPERATURE alerts — link state changes are NOT among
		// them, and `"trap-target":""` means no trap leaves anyway. Obtaining linkUp/linkDown
		// requires WRITING `trap-generators=interfaces` and a `trap-target`.
		//
		// Direct consequence on the floors above: unlike the 1930, `if_state` cadence cannot
		// rely on traps today. The 5 s floor of the REST channel is therefore what provides
		// responsiveness, not a safety net under pushed notification.
		Traps: true,
		// OBSERVED — `/system/logging/action` carries a `remote` action:
		// `{"target":"remote","remote":"0.0.0.0","remote-port":"514",
		// "remote-protocol":"udp"}`. It only remains to give it a destination.
		Syslog: true,
		// No gNMI or equivalent on RouterOS: no push telemetry menu, no subscription.
		// Supervision is by polling (REST or SNMP) or traps.
		Streaming: false,
	}

	// What this board does NOT EXPOSE.
	//
	// ONE entry only, and conditional. This is not the list of everything missing from
	// the UI: health, LLDP, state, and counters have their channel above, the device
	// serves them, and if no curve comes out it is meshcraft that does not read them
	// yet — a gap on OUR side, that a version will close. Listing them here would
	// declare them impossible.
	//
	// A word on what is STILL NOT here and one might expect to find: optical power.
	// This slot was occupied by a paragraph explaining why it had « neither channel nor
	// entry here » — `monitor` being a POST, outside GET-only tool reach, it could not
	// be exercised. This batch opened the channel (api.go, telemetry.go): it is now
	// served by the `api` channel AND the `snmp` channel (mtxrOpticalTable), and its
	// place here would be a contradiction that `Contradictions()` would reject.
	//
	// What remains true from the old paragraph and deserves keeping: the declaration
	// rests on the vendor specification and on a read of fields `sfp-rate-select`,
	// `sfp-ignore-rx-los`, and `sfp-shutdown-temperature` observed on `sfp-sfpplus2` —
	// therefore on proof that the platform TRACKS its transceivers — and not on a read
	// optical value. The nuance now lives in the `Note` of both channels, where a
	// scheduler reads it.
	if !face.PoE {
		out.Unavailable = append(out.Unavailable, driver.MetricUnavailable{
			Metric: "poe",
			// Observed, not derived — and it took two requests to be able to state it,
			// the first not being enough.
			Observed: true,
			Source: "REST observation on CRS326-24G-2S+ / RouterOS 7.21beta3 (testing), " +
				"serial 945509EF00B0: GET /rest/interface/ethernet/poe → 500, then " +
				"GET /rest/interface/ethernet/poe/settings → 400 « no such command " +
				"prefix » — the same response as an invented path (/rest/nimportequoi → " +
				"400 « no such command or directory »), so the menu prefix does not exist. " +
				"The second request is the one that concludes: the 500 alone proves nothing; " +
				"/rest/interface/dot1x returns 500 while /rest/interface/dot1x/server returns []. " +
				"Independent confirmation: GET /rest/system/health publishes only cpu-temperature, " +
				"with no power rail.",
		})
	}
	return out
}

// ResetProcedure — sourced from the device itself as much as possible.
//
// A pleasant particularity of this platform: it PUBLISHES the parameters of its
// own reset button. `/system/routerboard/settings` returns `enable-jumper-reset`,
// `reformat-hold-button`, and `protected-routerboot`, which allows describing the
// gesture with the device's real values rather than a manual's.
//
// Confirmed stays FALSE, and that is deliberate: no reset was EXECUTED on this
// hardware. Parameters are observed, the procedure that chains them is documentary.
// Presenting that as verified would be exactly the confusion this field exists to
// prevent.
func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Hardware: "Reset button (or reset jumper) pressed DURING power-on, then released " +
			"according to desired duration: a few seconds restore factory configuration, a hold " +
			"up to 20 SECONDS switches the board to network boot for Netinstall. Both durations " +
			"are published by the device itself: `/system/routerboard/settings` returns " +
			"`enable-jumper-reset: true` (the button is armed) and `reformat-hold-button: 20s`. " +
			"Boot delay is short — `boot-delay: 2s` — so press BEFORE powering on, not after.",
		Software: "REST API: POST /rest/system/reset-configuration. Options change the " +
			"result radically: `keep-users=yes` keeps accounts and passwords, `no-defaults=yes` does " +
			"NOT restore factory configuration (the device starts completely bare, with no bridge or " +
			"address), `skip-backup=yes` skips the automatic archive. By CLI, the same command exists " +
			"under `/system reset-configuration`. UNVERIFIED: this write was not exercised on the " +
			"reference hardware.",
		Erases: []string{
			"all configuration: bridge, VLAN, IP addresses, routes, services",
			"accounts and passwords, EXCEPT if `keep-users=yes`",
			"SNMP settings, including communities",
			"system name (`/system/identity` reverts to board name)",
		},
		AfterReset: []string{
			"MikroTik factory configuration: all ports in one bridge, address 192.168.88.1/24 " +
				"on that bridge. This is not deduction — this configuration is still IN PLACE on the " +
				"reference device, and is recognized by its marking: `/ip/address` carries " +
				"`192.168.88.1/24` with `comment: defconf`, and the bridge and its 26 ports carry the " +
				"same `defconf` comment",
			"`admin` account WITHOUT password; RouterOS 7 requires setting one on first login",
			"services `www` (80), `ssh` (22), `telnet` (23), `winbox` (8291), and `api` (8728) " +
				"active; `www-ssl` disabled for lack of certificate",
			"MAC-winbox and MAC-telnet reachable on the L2 segment without any address " +
				"(`/tool/mac-server`: `allowed-interface-list: all`)",
			"RouterOS version is UNCHANGED: reset erases configuration, not the system",
		},
		Pitfalls: []string{
			"`protected-routerboot` is `disabled` on the reference device (observed). A site that " +
				"ENABLES it closes the door to button reset: the RouterBOOT password will be required, " +
				"and losing it makes the board unrecoverable without vendor intervention. It is a door " +
				"one can only lock on oneself.",
			"Without `keep-users`, reset leaves the `admin` account WITHOUT password: the device is " +
				"then open to anyone reaching its L2 segment, MAC-winbox included. The window must be " +
				"closed immediately.",
			"Do not confuse the two versions the device publishes: `/system/resource` gives RouterOS " +
				"(observed `7.21beta3 (testing)`) and `/system/routerboard` gives RouterBOOT (observed " +
				"`current-firmware: 6.42.5`). A configuration reset touches neither.",
			"BEFORE resetting a device whose access was « lost », try MAC-winbox and the serial console " +
				"(both observed available): on this platform, a lost address almost never justifies " +
				"factory reset.",
			"The reference device runs firmware from the `testing` branch (observed `7.21beta3 (testing)`, " +
				"and `/system/history` shows the update channel going from `development` to `stable`). " +
				"Capabilities observed here may change from one version to another on that branch.",
		},
		// False: parameters observed, sequence not executed. See header.
		Confirmed: false,
		Source: "Observations on CRS326-24G-2S+ / RouterOS 7.21beta3 (testing), serial " +
			"945509EF00B0: /rest/system/routerboard/settings (enable-jumper-reset: true, " +
			"reformat-hold-button: 20s, protected-routerboot: disabled, boot-delay: 2s), " +
			"/rest/ip/address and /rest/interface/bridge (`defconf` marking of factory " +
			"configuration), /rest/tool/mac-server, /rest/port. The hardware gesture sequence " +
			"comes from MikroTik RouterBOOT documentation and was NOT executed on this hardware.",
	}
}
