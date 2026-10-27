// Package swos drives MikroTik switches running SwOS — the system of the CSS
// range, not to be confused with RouterOS.
//
// SwOS is WEB-ONLY, and more narrowly so than Instant On: no SSH, no Telnet,
// no HTTPS, no serial port. On the reference device, a single TCP port
// answers: 80. All administration goes through `*.b` pages served behind HTTP
// Digest authentication, which return objects in JavaScript syntax with
// hexadecimal values (cf. parse.go).
//
// WHERE THIS FILE'S CAPABILITIES COME FROM
//
// No vendor guide gives them. MikroTik documents RouterOS extensively and
// SwOS barely; the platform's reference is the JAVASCRIPT THE DEVICE SERVES
// ITSELF — an `index.html` page of 44,807 bytes, script fully inline, that
// defines every tab, every field, its type, its encoding, its bounds, and the
// request by which the UI writes it. That is the method the Instant On
// connector used on its own range, and here it is the only source there is.
//
// Three independent observations corroborate it, all attached to the package
// (testdata/):
//
//   - the device's live data pages — /sys.b, /link.b, /fwd.b, /vlan.b,
//     /rstp.b, /lacp.b, /sfp.b, /snmp.b, /acl.b, /host.b, /!dhost.b,
//     /stats.b;
//   - its CONFIGURATION BACKUP, /backup.swb, which enumerates page by page the
//     fields it actually stores — hence the exact inventory of what is
//     writable;
//   - the challenge of its authentication, readable WITHOUT credentials,
//     which carries the model reference.
//
// Reference hardware: **MikroTik CSS326-24G-2S+, SwOS 2.17** (serial
// 945809C232D5, MAC b8:69:f4:5f:14:a8), reached through a proxy's tunnel.
// Every figure below names the observation that establishes it, or says
// explicitly that it is inferred.
//
// # WHAT SwOS CANNOT DO IS A DELIVERABLE
//
// SwOS is deliberately poor next to RouterOS, and half of this connector's
// value is to SAY so: no routing, no clock, no syslog, no traps, no LLDP, no
// named account, no rollback. Those `false` values and those unavailability
// reasons are not holes to fill in a later version: they are facts of the
// platform, and displaying them is what will stop meshcraft from promising an
// operator what a CSS will not deliver.
package swos

import (
	"regexp"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

func init() { driver.Register(&Driver{}) }

// Driver covers the CSS range running SwOS.
type Driver struct{}

func (d *Driver) Info() driver.Info {
	return driver.Info{
		Name:   "mikrotik-swos",
		Vendor: "MikroTik",
		Family: "SwOS",
		// WEB for ADMINISTRATION, SNMP for COLLECTION — and the two are not
		// interchangeable: everything this connector WRITES goes through the
		// `*.b` pages, and the SNMP agent is read-only.
		//
		// "snmp" was added on an OBSERVATION, not on an inference. The previous
		// comment said "nothing this connector could establish says which
		// protocol version it answers, and the available exploration tool does
		// not speak UDP": the proxy's walk does speak UDP, and the agent answered
		// in v2c (cf. SNMPVersions for the values).
		Transport: []string{"web", "snmp"},
		Doc: "Surveyed on hardware (MikroTik CSS326-24G-2S+, SwOS 2.17): *.b pages, " +
			"configuration backup /backup.swb, and the JavaScript of the embedded " +
			"UI, which is the platform's only documentation",
	}
}

// Recognition patterns.
//
// SwOS names itself three times, which makes it a comfortable platform to
// recognise:
//
//   - the TITLE of its web page is "MikroTik SwOS" (observed on the reference
//     device);
//   - the REALM of its Digest challenge carries the exact model reference
//     (`realm="CSS326-24G-2S+"`), and it is readable WITHOUT credentials;
//   - an SNMP sysDescr, when the agent answers, carries "SwOS".
var (
	reSwOS = regexp.MustCompile(`(?i)SwOS`)
	// reRealmModel catches a MikroTik switching reference wherever it appears
	// in a page body or a header copied into the Evidence.
	reRealmModel = regexp.MustCompile(`(?i)\b(CSS[0-9]{3}|CRS[0-9]{3})[0-9A-Za-z+\-]*`)
	// reRouterOS excludes RouterOS. CRS models exist in both worlds — the same
	// chassis boots SwOS or RouterOS — and their capabilities have nothing in
	// common. Taking a CRS on RouterOS for a SwOS would refuse half of what it
	// can do, and write through the wrong path.
	reRouterOS = regexp.MustCompile(`(?i)RouterOS`)
)

// Fingerprint: "is this device mine?"
//
// The platform name is a SAFE sign: "SwOS" appears nowhere else. A realm
// carrying a CSS/CRS reference is another, independent one, and it is the one
// you get without any credentials at all.
//
// Excluding RouterOS comes BEFORE everything else: a CRS can boot either
// system, and it is the only case in this project where two connectors
// compete for the same chassis. The page title settles it — it is the only
// clue the RUNNING system produces itself, whereas a model reference says
// nothing about what booted.
func (d *Driver) Fingerprint(ev driver.Evidence) int {
	var haystack = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.HTTPBody, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if reRouterOS.MatchString(haystack) && !reSwOS.MatchString(ev.HTTPTitle) {
		return 0
	}
	switch {
	case reSwOS.MatchString(haystack) && reRealmModel.MatchString(haystack):
		// The platform AND the model: we cannot do better without logging in.
		return 98
	case reSwOS.MatchString(haystack):
		return 95
	case reRealmModel.MatchString(haystack):
		// A CSS/CRS reference without "SwOS" appearing: a good candidate, to
		// be confirmed. A CRS may be running RouterOS, and discovery may not
		// have read the realm.
		return 55
	}
	return 0
}

// Capabilities of the SwOS range, observed on hardware.
func (d *Driver) Capabilities(model string) driver.Capabilities {
	// PoE is inferred from the reference, for lack of anything better when
	// the device is not at hand: MikroTik's naming convention marks PoE with
	// a `P` ("CRS328-24P-4S+") where models without PoE carry a `G`
	// ("CSS326-24G-2S+"). The reference CSS326 has none, and its own firmware
	// confirms it three times: `npoe:0x00` and `poe:0x00` in /sys.b, and NO
	// /poe.b page — the path renders the web UI, i.e. does not exist.
	var poe = ModelHasPoE(model)

	return driver.Capabilities{
		// VLAN bounds, observed in the device's table definition:
		// `{n:"VLAN ID", t:I, id:"vid", min:1, max:4094}`. Not to be confused
		// with the per-port "Default VLAN ID", which the same UI bounds at 4095
		// — the TABLE is what constrains, since a port can only belong to a
		// declared VLAN.
		VlanRange: [2]int{1, 4094},
		// 250 entries: the bound the device's UI imposes on itself (`Z:250` on
		// the VLAN table, with the refusal "Max entries (250)"). That is the
		// constraint that holds for a connector replaying this UI, and it is
		// the only one the platform puts a number on.
		MaxVlans: 250,
		// What the platform can do, and above all what it cannot.
		//
		// The keys are the Instant On connector's, to the letter: that is what
		// lets a CSS and a 1930 be compared in the same table. Every `true`
		// names the device field that establishes it; every `false` is an
		// observed ABSENCE across the full inventory of its UI and its
		// configuration backup, not a cautious assumption.
		Features: map[string]bool{
			// /vlan.b (VLAN table + member mask) and /fwd.b (`vlan`, `vlni`).
			"Trunk8021Q": true,
			// /fwd.b `dvid` — "Default VLAN ID", one per port.
			"PVID": true,
			// No notion of a voice VLAN: nothing in the UI, nothing in the
			// backup. An intent asking for one is refused, which is better than
			// setting it into the void.
			"VoiceVLAN": false,
			// /poe.b and the "PoE Out" group of /sys.b, on models that have it:
			// the UI shows its PoE tab only when the hardware is present.
			"PoE": poe,
			// NO LLDP. It is the absence with the heaviest consequences on the
			// whole platform, and it is final: no 802.1AB, no LLDP-MED, no
			// field, no neighbour table, in any tab or in the backup. SwOS only
			// announces and listens to MikroTik's proprietary protocol, MNDP
			// (/sys.b `pdsc`, "Mikrotik Discovery Protocol", one mask per port).
			// PoE models do carry an `lldp` field, but that is the LLDP-MED power
			// negotiation of a powered port: it builds no neighbour table.
			"LLDP": false,
			// Per-port RSTP participation (/rstp.b `ena`). The `edge` bit on the
			// live page is operational (edge detected), not an admin policy —
			// POST edge:0x… is accepted then cleared on read-back (CSS326 lab).
			driver.FeatSTPPortEnable: true,
			// /sys.b `igmp`, `igmq` (querier), `igfl` (fast leave), `igve` (v2/v3),
			// plus the learned group table /!igmp.b.
			"IGMPSnooping": true,
			// /lacp.b — per-port `mode` as "passive", "active" or "static".
			"LACP": true,
			// /sys.b, "DHCP & PPPoE Snooping" group: `dtrp` (trusted ports) and
			// `ainf` (insertion of the information option, a.k.a. option 82).
			"DHCPSnooping": true,
			// No DHCP relay: SwOS does not route, so it does not relay. The
			// option 82 insertion above is not a relay and must not be read as
			// one.
			"DHCPRelay": false,
			// Neither ARP inspection nor denial-of-service protection: nothing
			// of the kind in the UI. The closest thing is the ACL, which filters
			// but does not monitor.
			"ARPProtection": false,
			"DoSProtection": false,
			// /fwd.b `lck` ("Port Lock") and `lckf` ("Lock On First"), with the
			// static host table /host.b and its `drp` flag.
			"PortSecurity": true,
			// /fwd.b `fp1`…`fp26` — a complete isolation matrix, one mask of
			// allowed destinations per SOURCE port. Much finer than ordinary
			// "protected ports".
			"ProtectedPorts": true,
			// Neither 802.1X nor RADIUS: no field, no notion of network access
			// authentication on this platform.
			"Dot1X":  false,
			"RADIUS": false,
			// /fwd.b `imr` (ingress mirror), `omr` (egress), `mrto` (destination
			// port), plus `mrr` per VLAN and `mirr` per ACL rule.
			"PortMirroring": true,
			// /fwd.b `ir` — one ingress rate per port.
			"IngressRate": true,
			// No jumbo frame setting anywhere. The hardware may accept them by
			// default, but a capability describes what can be CONFIGURED and
			// therefore guaranteed: an intent "enable jumbo frames" has no write
			// to make here, and declaring it available would suggest it had been
			// applied.
			"JumboFrames": false,
			// SwOS DOES NOT ROUTE. No routing table, no gateway, no IP interface
			// other than management's. That is the fundamental difference with
			// RouterOS on the same chassis, and it must be visible before an
			// operator builds a plan on it.
			"StaticRouting": false,
			// NO CLOCK. No NTP, no SNTP, no time zone, no date anywhere in the
			// UI. The knock-on consequence: no timestamp is available on this
			// device, which is also why it has no syslog (cf. Monitoring.Syslog).
			"SNTP": false,
			// No dedicated loop protection. RSTP exists, and that is all — it is
			// not the same thing: RSTP assumes a neighbour that speaks RSTP.
			"LoopProtection": false,
			// Neither 802.3az energy saving nor RMON.
			"EEE":  false,
			"RMON": false,
			// /fwd.b `srt` ("Storm Rate", as a percentage), `suni` ("Limit
			// Unknown Unicast") and `fmc` ("Flood Unknown Multicast").
			"StormControl": true,
			// PER PORT, and it is a strength of this platform where Instant On
			// can only do it globally: `srt` is an ARRAY, one percentage per
			// port (26 values at 0x64 on the reference device).
			"StormControlPort": true,
			// NO command line of any kind, and no serial port on this model. On
			// the reference device, a single TCP port answers: 80. Neither 22,
			// nor 23, nor 443.
			//
			// Consequence to keep in mind before any write: adoption over a
			// serial console, one of discovery's three entry points, is
			// IMPOSSIBLE here. A device whose access is lost is recovered with
			// the factory reset button, hence with a site visit.
			"SSHMgmt":    false,
			"TelnetMgmt": false,
			"SerialCLI":  false,
			"SerialPort": false,
			// The only path. In CLEARTEXT: SwOS does not listen on HTTPS, and
			// confidentiality rests entirely on the proxy's tunnel.
			"HTTPMgmt": true,
		},
		// RSTP, and RSTP only — no protocol selector, no "none".
		//
		// The UI exposes a per-port RSTP switch (/rstp.b `ena`), bridge priority
		// and cost mode (/sys.b `prio`, `cost`). The `rstp` field on /rstp.b that
		// shows "STP" or "RSTP" is READ-ONLY: automatic fallback when a
		// neighbor speaks only 802.1D, not a mode we choose. Declaring "stp" or
		// "none" would accept intents no write can honor. Site preference
		// "none" is therefore skipped for this platform (ResolveSTPMode).
		STPModes: []string{"rstp"},
		// No MSTP, so no instances.
		MSTInstances: 0,
		// The SwOS ACL is a FLAT LIST: there are no named ACLs attached to
		// interfaces, there is a single rule table, each rule carrying its own
		// ports of application (`frm`). meshcraft's contract distinguishes the
		// two numbers, so the honest translation is "one ACL, 32 rules".
		//
		// 32 is the bound the UI imposes on itself (`Z:32`, refusal "Max entries
		// (32)"). The criteria are rich for the range — source and destination
		// MAC with masks, ethertype, VLAN and priority, source and destination
		// IP with prefixes and ports, protocol, DSCP — and the actions are
		// "drop", "mirror", "redirect", rate limiting and VLAN rewrite.
		MaxACLs: 1,
		MaxACEs: 32,
		// No routing, hence no routes.
		MaxStaticRoutes: 0,
		// 15 aggregates: /lacp.b bounds `sgrp` at 15 (`{n:"Group", t:I,
		// id:"sgrp", max:15}`), and the UI names the aggregates it can display
		// "Trunk1" to "Trunk15".
		MaxLAGs: 15,
		// UNKNOWN, and said so rather than assumed.
		//
		// The device's UI imposes NO bound on the number of ports in one
		// aggregate, and no observation can establish it read-only — you would
		// have to build an aggregate and watch where the device refuses. 0 reads
		// as "unknown" and therefore causes a refusal, as the project rule
		// requires: that has no consequence today, since meshcraft does not set
		// aggregates, and it is the value to survey on the day it does.
		PortsPerLAG: 0,
		// v2c AND NOTHING ELSE, and each of the two terms is a decision.
		//
		// What /snmp.b PROVES: an agent exists, it is enabled (`en:0x01`), it
		// carries ONE community (`com`, at most 64 bytes), a contact and a
		// location. What it proves just as well, through its complete
		// inventory: there is NO SNMPv3 on this platform — no user, no
		// authentication algorithm, no encryption, no field, in any tab or in
		// the configuration backup. And no trap receiver.
		//
		// What that page could never prove: which protocol version the agent
		// answers. The UI has no selector, and a community does not say whether
		// the agent serves v1, v2c or both. This list therefore stayed EMPTY
		// while the answer was missing — the only tool available to reach the
		// device through the tunnel spoke read-only HTTP, so no SNMP request
		// could be sent. That is no longer the case, and it was the network
		// that settled it, not the page.
		//
		// ┌── v2c: OBSERVED, the agent answered ───────────────────────────────────┐
		//
		// This list was EMPTY, and the comment it carried said: "the list will
		// gain a line the day an `snmpget` has answered from a proxy". That
		// happened on 29 July 2026, on the reference device 192.0.2.36
		// (CSS326-24G-2S+, SwOS 2.17), through the proxy's diagnostic walk —
		// which speaks **hard-coded v2c** (`gosnmp.Version2c`,
		// internal/snmppoll), which makes every answer obtained a proof of v2c
		// and of nothing else:
		//
		//	sysDescr.0     CSS326-24G-2S+ SwOS v2.17
		//	sysObjectID.0  1.3.6.1.4.1.14988.2
		//	sysName.0      lan-m6      ← the `id` field of /sys.b, decoded
		//	ifNumber.0     26          ← the 26 ports of /link.b (`prt:0x1a`)
		//
		// The last two lines are not decoration: they CROSS-CHECK SNMP against
		// what the connector reads over the web on the same device. An agent
		// answering for other hardware — a real case when an address has been
		// recycled — would not return that name and that port count.
		//
		// v1 IS NOT ADVERTISED, and the omission is deliberate: the agent
		// probably serves it (SwOS has no version selector), but nothing has
		// shown it — the project's only query channel is v2c. A capability is
		// observed, it is not inferred from plausibility, and advertising v1
		// would bring nothing: v2c is what the collector uses.
		//
		// v3 stays ABSENT, and that is a platform fact established by exhaustive
		// enumeration: /snmp.b fits in four fields (enable, community, contact,
		// location), there is no user, no algorithm, no encryption, in any tab
		// or in the configuration backup. `snmpprov.For` therefore concludes
		// "v2c, NOT compliant with the policy floor", which is exactly the
		// truth to display: monitored, and in cleartext.
		//
		// └───────────────────────────────────────────────────────────────────────┘
		SNMPVersions: []string{"v2c"},
		// Platform observability.
		//
		// EVERYTHING is web polling, and that is where to start: SwOS pushes
		// NOTHING. No trap receiver, no syslog server, no telemetry — and it
		// could not timestamp a message even if it sent one, having no clock.
		// Where an Instant On's trap channel allows SLOWING state polling down
		// to 30 s while keeping reactivity, here polling IS the reactivity: a
		// link that drops is seen on the next cycle, never earlier.
		//
		// What a read costs on this platform, and this is what sets the floors
		// below:
		//
		//   - TWO HTTP requests per page, always. SwOS has no session: Digest is
		//     renegotiated every time, so a 401 then the authenticated request.
		//     A "one read every 10 s" collection is in reality twelve requests
		//     a minute;
		//   - the ENTIRE page body, without exception. SwOS offers no way to ask
		//     for a subset: not a port, not a column. Where an SNMP walk can be
		//     limited to four columns, here /stats.b serialises its 26 ports and
		//     its forty-odd counters on every call;
		//   - the CPU doing the serialising is that of an access-range switch,
		//     and it is the same one that serves the UI to the human
		//     administrator.
		//
		// Measurements on the reference device, through the proxy's tunnel
		// (average of five reads, controller round trip included, hence an
		// upper bound): /sys.b 780 bytes in 49 ms, /link.b 2,026 bytes in 46 ms,
		// /stats.b 12,694 bytes in 50 ms, /backup.swb 2,996 bytes in 51 ms.
		//
		// # THE KIND OF THE CHANNELS, AND WHY THERE ARE NOW TWO
		//
		// Four `web` channels, then ONE `snmp` channel at the end of the list.
		// This spot long said the opposite — "no snmp channel here, and it is
		// not an oversight" — and it was right as long as `SNMPVersions` was
		// empty: a connector does not declare a collection whose agent it has
		// never seen answer. The walk of 29 July 2026 on 192.0.2.36 lifted the
		// lock; the `v2c` declaration is thirty lines above, and the channel it
		// authorises carries, OID by OID, what that walk actually obtained.
		//
		// The kind is not decoration: the controller's scheduling queries the
		// channel BY KIND (`MonitoringCapabilities.BestOfKind("snmp", …)`),
		// because periodic collection runs on the proxy and speaks only that
		// protocol. As long as this platform advertised only `web`, it received
		// no SNMP cadence — consistent, and without consequence since it
		// appeared in no target. That is exactly what changes here.
		//
		// The two kinds do not overlap, and neither subsumes the other. What
		// web scraping returns is in telemetry.go: 64-bit counters, rates
		// computed by the device, chip temperature, uptime, COMPLETE optical
		// diagnostics of the cages — only the web carries the powers, the
		// agent's optical table stopping at bias current. SNMP, for its part,
		// costs little enough to hold a 10 s floor where /stats.b serialises
		// 12,694 bytes on every call.
		//
		// What is missing, on the other hand, is a PLATFORM gap and not a
		// protocol one: neither CPU load nor memory is published on either
		// side, and `Unavailable` is what tells the device page so — not a
		// channel note nobody reads.
		Monitoring: driver.MonitoringCapabilities{
			Channels: []driver.MonitorChannel{
				{
					// Port state, from /link.b: link, enable and duplex masks, rate
					// index and name per port. 2,026 bytes.
					//
					// FLOOR AT 10 s, and it is not copied from elsewhere.
					//
					// The device's UI refreshes this page every 3 s (`refresh:3E3`):
					// the platform therefore demonstrably handles that pace for one
					// client. It is still not the chosen floor, and the reason is a
					// change of REGIME, not of client — a page an administrator watches
					// for a minute and a collection that never stops, across a whole
					// fleet, for months, do not ask the same thing of the device. 10 s
					// is what we accept to do PERMANENTLY.
					//
					// Going lower would moreover be pointless: the reactivity one would
					// seek there is not obtained by polling on this platform, it would
					// be obtained by a trap, and SwOS emits none. Whoever wants to lower
					// it must have measured it on hardware, as this one was.
					Kind:        "web",
					Metrics:     []string{"if_state"},
					MinInterval: 10 * time.Second,
					Note: "SwOS emits no trap: this polling is the ONLY source for " +
						"detecting a link that goes down, and a change is seen at latest " +
						"on the next cycle.",
				},
				{
					// Counters and rates, from /stats.b. 12,694 bytes — six times the
					// next page by size, and by far the most expensive.
					//
					// FLOOR AT 15 s, HIGHER than the port state one, the opposite of
					// what the Instant On connector does. The reason is cost, and cost
					// alone: on every call this page serialises the forty counters of
					// the twenty-six ports, with no way to ask for less. At 15 s that
					// stays under a kilobyte per second per device; at 5 s, a fleet of
					// two hundred CSS would mean half a megabyte per second of permanent
					// serialisation on access-switch CPUs.
					//
					// And this floor does NOT cost the curve its resolution, which is
					// the decisive point: SwOS computes the snapshot rates itself (`rrb`
					// and `trb`, in hundredths of a bit per second). meshcraft therefore
					// does not need to derive two counter reads to plot a rate, and a
					// wider sample interval does not smooth it — unlike a platform where
					// the rate is only obtained by differentiating `ifHCInOctets`. That
					// is what makes 15 s acceptable here where it would not be elsewhere.
					Kind:        "web",
					Metrics:     []string{"if_counters"},
					MinInterval: 15 * time.Second,
					Note: "The entire page (26 ports × ~40 counters) is serialized on " +
						"every read: SwOS cannot serve a subset. Snapshot rates are " +
						"computed by the device, so a wider sample interval does not " +
						"smooth them.",
				},
				{
					// Health, from /sys.b. 780 bytes, the cheapest page.
					//
					// FLOOR AT 30 s. A temperature does not move below the minute, and
					// the monitoring design puts health at five minutes: 30 s already
					// leaves ten times the necessary margin, and there is no reason to
					// go lower. There is one not to — it is also the page the device's
					// UI refreshes every second when an administrator is watching it,
					// and the connector has no business competing with them for their
					// own switch.
					Kind:        "web",
					Metrics:     []string{"health"},
					MinInterval: 30 * time.Second,
					Note: "On this model, health reduces to ONE chip temperature " +
						"(/sys.b `temp`, in °C): no fan, no redundant PSU, no board " +
						"sensor — the page serves them as zero and the UI does not " +
						"display them.",
				},
				{
					// Optical diagnostics, from /sfp.b.
					//
					// SwOS SERVES optical power, and that is a real difference from
					// Instant On, which exposes it nowhere: /sfp.b returns, per cage,
					// temperature, voltage, bias current, transmitted power and
					// received power. Surveyed on the reference device, cage 2
					// holding an SFP-10G-BX: `tpw:0x12ba`, `rpw:0x0ad2`, live values.
					//
					// FLOOR AT 30 s. A transceiver's diagnostic registers are not in
					// the switch's memory: they are read over the module's I²C bus,
					// the slowest on the board. And an optical budget drifts over
					// days, not seconds — one read every half minute is already much
					// finer than the phenomenon calls for.
					Kind:        "web",
					Metrics:     []string{"optical_power"},
					MinInterval: 30 * time.Second,
					Note: "Only covers SFP cages (2 of 26 ports on a CSS326-24G-2S+). " +
						"An empty cage is recognized by its temperature, which the " +
						"device returns as -128 °C. ONLY channel that serves optical " +
						"POWERS: the SNMP agent serves the optical table WITHOUT its " +
						"power columns (observed; see the snmp channel).",
				},
				{
					// ┌── THE SNMP CHANNEL: OBSERVED ON HARDWARE, AND IT IS NEW ─────────┐
					//
					// This channel did not exist, because `SNMPVersions` was empty:
					// the connector could not declare a collection whose agent it had
					// not seen answer. The walk of 29 July 2026 on 192.0.2.36 settled
					// it, and this channel carries ONLY what that walk actually
					// obtained — not the list of what a MIB would let one hope for.
					//
					// WHAT THE AGENT SERVES, verified OID by OID:
					//
					//	ifDescr        "Port1"… "SFP1", "SFP2" — the /link.b names, and
					//	               therefore THIS CONNECTOR'S since it names its ports
					//	               like the device does (cf. naming.go). A series
					//	               collected over SNMP by the proxy and a configuration
					//	               read over the web by the controller designate the
					//	               same socket by the same name, with no mapping table
					//	               anywhere.
					//	ifOperStatus   served (1 on cage SFP2, the only one carrying a link)
					//	ifHCInOctets   189,323,935 on that cage: LIVE 64-bit counters
					//	ifHCOutOctets  1,666,156
					//	ifHighSpeed    10000 (Mbit/s) — and THAT is the one to read (see below)
					//	sysUpTime      served
					//	mtxrHealth.11  690, i.e. 69.0 °C — the ONLY health quantity published
					//
					// WHAT THE AGENT DOES NOT SERVE, which decides this channel as much
					// as the rest: `hrProcessorLoad` and `hrStorage` return NO value —
					// no CPU, no memory over SNMP on this platform. And the optical
					// table stops at column 8 (`txBias`): neither `txPower` (column 9)
					// nor `rxPower` (column 10). That is why `optical_power` is NOT in
					// the list below although the web channel serves it — the powers
					// are in /sfp.b (`tpw`, `rpw`) and nowhere else. Declaring here a
					// metric the agent does not return would promise a curve no
					// collection would ever fill.
					//
					// THE TRAP NOT TO REPRODUCE, observed on the active cage: `ifSpeed`
					// returns **0** on a 10 Gbit/s link, where `ifHighSpeed` returns
					// 10000. A collector reading `ifSpeed` would therefore display a
					// zero rate on the fastest links in the fleet. The poller does read
					// `ifHighSpeed` (internal/snmppoll); it is noted here because it is
					// the kind of thing someone "fixes" one day in the wrong direction.
					//
					// FLOOR AT 10 s, against 15 s for web counters: a GETBULK on six
					// columns costs this device infinitely less than serialising the
					// 12,694 bytes of /stats.b, and that is the whole point of having
					// this channel. It does not REPLACE the web for all that — /stats.b
					// carries the snapshot rates computed by the device, which SNMP does
					// not expose.
					//
					// └────────────────────────────────────────────────────────────────┘
					Kind:        "snmp",
					Metrics:     []string{"if_state", "if_counters", "health"},
					MinInterval: 10 * time.Second,
					Note: "Read-only v2c agent, no trap destination: reactivity stays " +
						"that of polling. Health here reduces to ONE temperature and " +
						"uptime — neither CPU nor memory; the agent does not serve " +
						"HOST-RESOURCES-MIB (observed). Optical POWERS do not go through " +
						"this channel: this agent's optical table stops at bias current.",
				},
				{
					Kind:        "snmp",
					Metrics:     []string{"fdb"},
					MinInterval: 30 * time.Second,
					Note: "MEASURED (walk 2026-08-13 on the CSS326): the FLAT BRIDGE-MIB only — " +
						"dot1dTpFdbPort answers (12 rows), dot1qTpFdbPort serves NOTHING, so a " +
						"learned MAC arrives WITHOUT its VLAN (recorded as unknown, never guessed). " +
						"dot1dBasePortIfIndex maps the 26 bridge ports.",
				},
			},
			// SwOS PUSHES NOTHING.
			//
			// No trap receiver: /snmp.b has only four fields — enable, community,
			// contact, location. No syslog server: no field in any tab, and in any
			// case no clock to timestamp a message. No streaming telemetry,
			// obviously.
			//
			// This is not a table detail: it is what makes this device's
			// monitoring ENTIRELY polling, and an operator who relies on pushed
			// alerts must know it before building on it.
			Traps:     false,
			Syslog:    false,
			Streaming: false,
			// What the platform does NOT expose, with its reason.
			//
			// Only these cases, and deliberately: this is not the list of
			// everything the UI lacks. Metrics served by a channel above have no
			// place here, and a metric meshcraft does not read yet has no place
			// here either — putting it here would declare it impossible and make
			// an operator give up on what only awaits a collection batch.
			Unavailable: unavailableFor(poe),
		},
		// ONE session at a time, and it is a deliberate safety floor.
		//
		// SwOS has no session at all: authentication is an HTTP header replayed
		// on every request, and nothing counts clients. There is therefore no
		// documented limit to advertise, and no "too many sessions" error
		// message in its UI.
		//
		// What HAS been established: dozens of SEQUENTIAL reads go through
		// without incident. What has NOT been established: what the device does
		// under concurrent reads — that was not attempted, an access-switch CPU
		// not being a place to discover that kind of thing on a device in
		// service. Declaring 1 serialises the connector's accesses, which is
		// exactly the safe behaviour, and it is the rule "an unknown capability
		// is assumed absent" applied to parallelism.
		MaxSessions: 1,
		// IMMEDIATE, and it is the heaviest constraint of this connector: there
		// is NO free safety net on apply.
		//
		// The UI inventory is unambiguous. Its only buttons are "Apply All",
		// which posts the page and nothing else, and "Discard Changes", which
		// merely RE-READS the page from the device — a purely local undo, which
		// undoes nothing that has gone out. There is no "Save Configuration"
		// anywhere, no separate startup configuration, no commit to confirm:
		// what is posted is written, and a reboot will not undo it. It is the
		// exact opposite of Instant On, whose "apply then save" offers a way
		// back through a power cut.
		//
		// The only way back is therefore to RESTORE a backup file. That is why
		// Admin.Backup exists and why ApplyPorts takes one before writing: on
		// this platform, caution is not in the commit strategy, it is in what
		// one took the trouble to copy beforehand.
		Commit: driver.CommitImmediate,
		Mgmt: driver.MgmtCapabilities{
			// The management VLAN is FREE: /sys.b `avln` ("Allow From VLAN"),
			// bounded at 4095 by the UI, 0 meaning "from any VLAN".
			VlanIDs: driver.VlanAny,
			// 1 to 4094 and not 1 to 4095: the UI's bound on `avln` is indeed
			// 4095, but a management VLAN must exist in the VLAN TABLE for the
			// port carrying it to be a member, and the table stops at 4094.
			// Keeping the LOWER of the two bounds is the only safe choice.
			VlanRange: [2]int{1, 4094},
			// TAGGED: OBSERVED, not inferred.
			//
			// The reference device is reachable at 192.0.2.36 with `avln:0x0063`
			// (VLAN 99), while the ONLY port carrying a link is cage SFP2
			// (/link.b `lnk:0x02000000`, bit 25) and that port's default VLAN is
			// 1 (/fwd.b `dvid[25]=0x01`). Administration traffic therefore
			// necessarily arrives TAGGED in 99 on that port. The learned address
			// table confirms it, showing hosts in VLAN 99 on that same port
			// (/!dhost.b, `vid:0x0063, prt:0x19`).
			TaggedOK: true,
			// The three modes of the UI's "Address Acquisition" selector, in its
			// order: "DHCP with fallback", "static", "DHCP only". The reference
			// device is static (`iptp:0x01`), which is observed; the other two
			// modes exist in its UI code, which is a strong source but has not
			// been exercised.
			Static: true,
			DHCP:   true,
			// NEITHER GATEWAY NOR RESOLVER — and it is an ESTABLISHED ABSENCE, not
			// a "not found". The writable field set of /sys.b is enumerated by
			// the device's own configuration backup (testdata/backup.swb): `ip`
			// and `iptp` are there, no gateway or DNS field appears, and the
			// platform's complete web UI fits in a single file that defines all
			// its fields. The enumeration exhausts the question, as it exhausted
			// it for the trap receiver.
			//
			// Operational consequence: without a gateway, off-subnet egress
			// (traps, DNS) has no route here. Inbound administration from
			// another subnet remains possible as long as the L2 return path
			// exists — this is not "unreachable off-subnet". The collector still
			// benefits from being in the right network.
			Gateway: false,
			DNS:     false,
			// No L3 route off-subnet (same fact as Gateway=false). Inbound still
			// works over L2/ARP when a return path exists — place the collector
			// in the right network rather than relying on a gateway that is not there.
			Routed: false,
			IPARP:  true,
			// INFERRED from MikroTik documentation, not observed: the factory
			// fallback address of a MikroTik device is 192.168.88.1. The UI's
			// "DHCP with fallback" mode does imply a fallback address, but the
			// reference device being static, no read gives it. To be verified
			// before relying on it to recover a lost device.
			DHCPFallback: "192.168.88.1",
			// NO out-of-band recovery path, and that is ESTABLISHED by
			// enumeration, not assumed: on the reference device, a single TCP
			// port answers — 80. Neither 22 nor 23, and this model has no serial
			// connector.
			//
			// The difference with RouterOS is instructive and worth writing down
			// here, because both platforms come from the same vendor: RouterOS
			// keeps MAC-winbox, i.e. layer-2 administration that survives any
			// addressing mistake. SwOS has nothing of the kind — its web UI is
			// its only door, and that door depends on the VLAN declared in
			// `avln` (/sys.b). Getting it wrong locks the device out for good.
			//
			// Consequence, to be read with [driver.CommitImmediate] just above:
			// no commit safety net, no recovery path. The ONLY way back on this
			// platform is the configuration copy taken before writing
			// ([driver.ApplyResult.Backup]).
			OutOfBand: false,
			// Same Digest POST channel as port apply / SNMP. Same-VLAN apply and
			// jump both rewrite /sys.b after GET /backup.swb; JumpVLAN is the
			// honest single-address leap (never MgmtMover).
			ApplySameVLAN: true,
			JumpVLAN:      true,
			// THE ORDER OF OPERATIONS. This model has NO serial port: each of the
			// lines below is a documented way to lock yourself out, and the only
			// recourse would be a site visit with a paper clip.
			Notes: []string{
				"NO out-of-band console: no SSH, no Telnet, no serial port. On the " +
					"reference device, a single TCP port answers — 80. A management " +
					"addressing mistake is fixed on site, at the factory-reset button.",
				"Create the VLAN in the table (/vlan.b) AND make the port that carries " +
					"administration a member of it BEFORE selecting that VLAN in \"Allow " +
					"From VLAN\" (/sys.b `avln`). The reverse order points administration " +
					"at a VLAN that never reaches the CPU.",
				"\"Allow From VLAN\" accepts ONLY one VLAN, and there is no second " +
					"management address: unlike Instant On, you cannot add the new path, " +
					"verify it, then remove the old one. The cutover is a jump, which is " +
					"why a configuration backup must be taken beforehand.",
				"Three other /sys.b fields cut access just as surely: `allp` " +
					"(\"Allow From Ports\", mask of ports allowed to administer), `alla` " +
					"and `allm` (allowed subnet). They mean \"allow everything\" when zero " +
					"on the reference device; writing them without understanding them is " +
					"the shortest path to a silent device.",
				"The management port VLAN mode (/fwd.b `vlan`) must stay compatible " +
					"with the management VLAN. Setting it to \"strict\" on a port that " +
					"is not a member of the management VLAN drops frames before they " +
					"reach the CPU.",
				"SwOS configuration is persistent IMMEDIATELY (see Commit): a power " +
					"cut does not undo a failed cutover.",
				"The address is little-endian in the page (`0x240200c0` = 192.0.2.36). " +
					"Reversing it writes 36.2.0.192 while believing you wrote 192.0.2.36.",
			},
		},
		// The administration account: there is only one, and it has no name of
		// its own.
		Admin: driver.AdminAccount{
			// NO named account, and that is a FACT, not caution.
			//
			// SwOS has no notion of an account. The password change form of its
			// UI carries only three fields — old, new, confirmation; the page
			// receiving it holds a single key, `{pwd:''}`; and the device's ENTIRE
			// configuration backup mentions no user name. There is literally
			// nothing to name.
			//
			// Consequence for meshcraft: the fleet's unified name CANNOT be
			// applied here. That matters and is not anecdotal — it means direct
			// access to a CSS will never be predictable in the same way as on the
			// rest of the fleet, and the only thing provisioning can unify there
			// is the SECRET.
			Custom:    false,
			FixedName: FixedAdminName,
			// 0 reads as "unknown" in this contract, and here it is the exact
			// value: SwOS imposes no minimum length, and even accepts an EMPTY
			// password — the reference device carries one, and every read of this
			// connector was validated with an empty secret. Saying so in the
			// notes rather than letting a 1 lie.
			PasswordMin: 0,
			// 15 characters: the bound the device's UI imposes on itself, for the
			// old secret as for the new one ("Bad old password" beyond 15 on the
			// old one, "New password is too long" on the new one).
			PasswordMax: maxPasswordLen,
			// Punctuation goes through: the only constraint is ASCII (cf. Notes).
			AlphanumericOnly: false,
			Notes: []string{
				"An EMPTY password is accepted by SwOS, and that is the factory state: " +
					"the reference device carries one, and the Digest computes normally " +
					"with it. A freshly factory-reset CSS is therefore taken over with no " +
					"initial secret.",
				"ASCII ONLY. The device UI builds secret bytes with `charCodeAt`, which " +
					"returns UTF-16 code units: beyond character 127, what the browser " +
					"sends and what another client would send diverge, and the applied " +
					"secret is not the one you think. The connector therefore refuses " +
					"before sending.",
				"The secret is not sent in cleartext but in a homemade digest " +
					"(RC4 keyed by the OLD password, see PasswordBlob). Practical " +
					"consequence: changing the password REQUIRES knowing the previous " +
					"one, and the device answers HTTP 405 when it is wrong — the only " +
					"usable error message on the whole platform.",
				"The secret is NOT recoverable: losing it forces an on-site factory " +
					"reset; this model has no serial port.",
				"The change takes effect IMMEDIATELY and without a save: the next " +
					"request must present the new secret, otherwise it gets a 401 that " +
					"looks like an outage.",
			},
		},
	}
}

// unavailableFor returns what the platform does not expose, depending on
// whether the model carries PoE.
//
// Each entry answers "why don't I have this curve?" by saying whether there
// is anything to wait for — the only thing these reasons give an operator.
//
// CPU LOAD AND MEMORY WERE ADDED together with the UI that displays them.
// They used to be stated in two places nobody reads: a monitoring channel
// `Note`, and the sentences `ReadTelemetry` attaches to every snapshot. Yet
// what reaches the device page is THIS list, and it alone — the telemetry
// panel therefore displayed "the connector does not say whether the platform
// publishes it" for an absence the connector had documented since its first
// delivery. Knowledge that does not reach the UI where the question arises
// does not exist.
//
// The granularity is that of the QUESTION, not of the channel: `health`
// remains a declared channel — this platform does serve a temperature — and
// it is the two missing quantities that are named. Declaring `health`
// unavailable would make the temperature disappear, and it is the only
// measure of wear available here.
func unavailableFor(poe bool) []driver.MetricUnavailable {
	var out = []driver.MetricUnavailable{
		{
			Metric:   "cpu",
			Observed: true,
			Source: "Observed on MikroTik CSS326-24G-2S+ running SwOS 2.17 (192.0.2.36): " +
				"none of the served pages (/sys.b, /link.b, /fwd.b, /vlan.b, /stats.b, " +
				"/sfp.b, /rstp.b, /lacp.b, /snmp.b, /acl.b, /host.b, /dhost.b) carries a " +
				"load field, and neither does the full configuration backup (/backup.swb). " +
				"The device SNMP agent serves nothing on hrProcessorLoad " +
				"(1.3.6.1.2.1.25.3.3.1.2) — walk of 29 July 2026 through the tunnel.",
		},
		{
			Metric:   "memory",
			Observed: true,
			Source: "Observed on MikroTik CSS326-24G-2S+ running SwOS 2.17 (192.0.2.36): " +
				"no memory field in the served pages or in /backup.swb, and the SNMP " +
				"agent returns no value on hrStorage (1.3.6.1.2.1.25.2.3) — walk of " +
				"29 July 2026.",
		},
		{
			Metric: "lldp",
			// Established by ENUMERATION, not probing: this is the strongest form of
			// proof. A SwOS web UI fits in a single file that defines ALL its tabs and
			// ALL its fields — there is no hidden page to discover, unlike Instant On
			// where a section could exist without being shown. Not finding LLDP there,
			// and not finding it in the configuration backup either, exhausts the
			// question.
			Observed: true,
			Source: "Observed on MikroTik CSS326-24G-2S+ running SwOS 2.17 (192.0.2.36): " +
				"the entire web UI (index.html, 44,807 bytes, fully inline script) " +
				"defines its tabs and every field — no LLDP occurrence outside PoE " +
				"power negotiation on a port, no neighbor table, no discovery tab " +
				"besides \"Mikrotik Discovery Protocol\" (/sys.b `pdsc`). The full " +
				"configuration backup (/backup.swb, 2,996 bytes, attached to the " +
				"package) carries no LLDP field. Pages /lldp.b and /neighbor.b do not " +
				"exist: they render the web UI, as every unknown path does on this " +
				"platform.",
		},
	}
	if !poe {
		out = append(out, driver.MetricUnavailable{
			Metric:   "poe",
			Observed: true,
			Source: "Observed on MikroTik CSS326-24G-2S+ running SwOS 2.17 (192.0.2.36): " +
				"/sys.b returns `npoe:0x00`, `poe:0x00` and `poes:0x00`, and page /poe.b " +
				"does not exist — it renders the web UI. The UI also gates its PoE tab " +
				"on the presence of that hardware.",
		})
	}
	return out
}

// ResetProcedure of the range.
//
// `Confirmed` is FALSE, and it is worth reading why: the two halves of this
// procedure are not known with the same certainty, and the contract has only
// one boolean for both. The software path is solidly established — it is
// observed in the code of the device's own UI. The hardware gesture, for its
// part, comes from MikroTik documentation and has not been exercised on this
// model. Since it is precisely the gesture an operator will attempt once
// access is lost, i.e. at the worst moment, presenting it as verified would be
// the very mistake this field exists to prevent.
func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Hardware: "Front-panel Reset button. Cut power, hold the button, restore power " +
			"while still holding until the status LED starts blinking (about 5 seconds), " +
			"then release: SwOS configuration returns to factory state. Releasing BEFORE " +
			"the blink is only a normal boot and erases nothing. PROCEDURE UNVERIFIED on " +
			"this model: it comes from vendor documentation, not a trial.",
		Software: "Web UI, System tab, Backup section, \"Reset Configuration\" button " +
			"(confirmation required). The gesture is a POST to /reset, observed in the " +
			"device UI code. Administrator access required, so unusable when access is " +
			"exactly what was lost.",
		Erases: []string{
			"the VLAN table and port membership",
			"management addressing (address, mode, VLAN and access restrictions)",
			"the administration password",
			"system name, port names, aggregation, ACLs, static hosts",
			"the SNMP community",
		},
		AfterReset: []string{
			"user \"admin\", EMPTY password — observed on the reference device, which " +
				"carries that state",
			"fallback address 192.168.88.1 (MikroTik documentation, NOT verified)",
			"web UI on port 80, cleartext, no HTTPS",
		},
		Pitfalls: []string{
			"NO serial port on this model, and neither SSH nor Telnet: the Reset button " +
				"is the ONLY recourse after loss of access, and it requires a site visit.",
			"The hardware gesture requires cutting then restoring power while holding " +
				"the button: it is not a simple long press on a powered-on device.",
			"SwOS configuration is persistent immediately: unlike a platform that " +
				"\"apply then save\", a power cut undoes no configuration mistake. Only " +
				"restoring a backup (/backup.swb) does.",
			"On CRS chassis that can boot under RouterOS or SwOS, a factory reset is " +
				"not an OS switch: verify which of the two answers before concluding the " +
				"reset failed.",
		},
		// False: see the comment on this method.
		Confirmed: false,
		Source: "Software path: observed in the embedded UI JavaScript of a " +
			"CSS326-24G-2S+ on SwOS 2.17 (\"Reset Configuration\" button → POST /reset). " +
			"Hardware gesture and fallback address: MikroTik documentation, not " +
			"exercised on this model.",
	}
}
