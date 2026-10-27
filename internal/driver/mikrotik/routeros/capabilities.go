package routeros

// What this connector can WRITE, and especially what it cannot.
//
// Declared HERE, in the package that writes, not in the API layer: the day a new
// intent becomes writable, the declaration and the code that writes it change in the
// same directory. A capability declared far from the write path always ends up claiming
// something other than what actually happens — and a capability that lies is worse than
// no capability at all, because it gives the operator configuration they believe is in
// place.
//
// Purpose: the meshcraft UI offers intents this connector does not apply. Without this
// declaration, they would be saved then silently forgotten — apply would report success
// and the screen would show configuration that exists nowhere. Rather than block the
// operator or lie, we APPLY everything supported, consider the device up to date for
// that scope, and SAY what was left behind.

// WritableExtras: intents OTHER than VLAN membership that this connector actually
// applies (cf. recon.ExtraIntents for the list the model can carry:
// `poe`, `stp`, `dhcp_snooping`, `port_security`).
//
//	stp            → ApplySpanningTree: `/interface/bridge` `protocol-mode`
//	                 (none|stp|rstp|mstp) and `priority`; `/interface/bridge/port`
//	                 `edge` and `path-cost`. Same REST Set path already used for
//	                 vlan-filtering on the bridge.
//	poe            → NOT APPLICABLE on this model: menu `/interface/ethernet/poe`
//	                 does not exist (observed, cf. Capabilities).
//	dhcp_snooping  → `/interface/bridge` carries `dhcp-snooping`, and
//	                 `/interface/bridge/port` carries `trusted` — not applied yet.
//	port_security  → NO per-port equivalent on this platform (observed:
//	                 `max-learned-entries` exists on the bridge, not on its ports).
//	port_admin     → ApplyPortAdmin: `disabled` on `/interface`, written through the
//	                 same REST `Set` path as `stp`. The FIELD, the MENU and both of
//	                 its values are in the capture this connector was built on
//	                 (testdata/interface.json, `"disabled":"false"` on all 29 rows),
//	                 the write is a POST carrying a `.id` read a moment earlier, and
//	                 the applier re-reads the same field to decide what moved. What
//	                 is NOT proven is the gesture itself on hardware — no write of
//	                 this connector has been exercised on a real device yet
//	                 (cf. apply.go's Backup note), which is what TODO.md asks for.
//	                 The failure mode if the payload were wrong is a REST error, not
//	                 a silent success: this API refuses an unknown argument.
//
//	                 SINCE 2026-08-06 THIS IS NO LONGER THE ONLY CONNECTOR TO DECLARE
//	                 IT. The operator asked for the gesture across the whole fleet,
//	                 and all eight connectors now declare it — each recording, next
//	                 to its own declaration, whether its verb is CAPTURED or DEDUCED.
//	                 This one sits at the captured-field end of that range: what is
//	                 written and what is read back both come from this device's own
//	                 JSON. It is worth reading the others' notes before assuming the
//	                 same is true there — hpe/procurve's `enable` and fs/s3900's
//	                 `shutdown` are deduced verbs, and mikrotik/swos cannot be undone
//	                 by a reboot.
//	storm_control  → ApplyStormControl: `limit-broadcasts` /
//	                 `limit-unknown-unicasts` and the shared `storm-rate`, on
//	                 `/interface/ethernet/switch/port`. DECLARED 2026-08-09 on a
//	                 gesture that was SENT — unlike the two above, which are still
//	                 declarations made on captured fields.
//
//	                 THE EVIDENCE: exercised on the lab CRS326-24G-2S+
//	                 (192.0.2.54, RouterOS 7.21beta3) against ether2, a dark port.
//	                 Broadcast at 10 % was written and confirmed from a SECOND
//	                 session; a second identical apply wrote nothing; the WHOLE
//	                 twenty-seven-record table was compared field by field before
//	                 and after, and came back equal.
//
//	                 MULTICAST STAYS REFUSED: `limit-unknown-multicasts` is
//	                 narrower than the contract's class and its coverage turns on
//	                 IGMP snooping, so the limiter's meaning would depend on a
//	                 setting nothing on the screen mentions.
//
//	                 READ THIS BEFORE JUDGING A READING: `limit-broadcasts=true`
//	                 beside `storm-rate=100` is the FACTORY state of every port on
//	                 this hardware — armed at a threshold equal to line rate, which
//	                 polices nothing. Twenty-five of the lab unit's ports were in
//	                 exactly that state, untouched. The class boolean alone does not
//	                 say a port is policed; see stormRateNoLimit.
var WritableExtras = map[string]bool{
	"stp":           true,
	"port_admin":    true,
	"storm_control": true,
	// dhcp_snooping: DECLARED 2026-08-10 on a live round trip through
	// [Client.ApplyDHCPSnooping], not on the strength of the REST fields existing.
	//
	// Exercised on the lab CRS326-24G-2S+ (192.0.2.54) by
	// TestLiveDHCPSnoopingRoundTrip, after a read-only reconnaissance pass had
	// established that the plan was safe to run at all: ONE cabled port
	// (sfp-sfpplus2, the uplink), no /ip/dhcp-client — so the switch's own
	// address is static and cannot be lost — and no /ip/dhcp-server, i.e. DHCP
	// arrives from upstream through that same uplink, which is therefore the port
	// that had to be trusted.
	//
	// The run: apply trusting sfp-sfpplus2 → PortsWritten=[sfp-sfpplus2], and an
	// INDEPENDENT re-read showed bridge `dhcp-snooping` false→true with that port
	// trusted false→true; a SECOND identical apply wrote nothing; teardown put the
	// bridge back and all 26 trust flags compared EQUAL to the opening snapshot.
	//
	// THE BLAST RADIUS IS REAL AND THE APPLIER SAYS SO, which is why this is
	// declared rather than hidden: turning the bridge feature on is a BRIDGE-WIDE
	// change — every untrusted port on it starts dropping DHCP OFFER/ACK,
	// including ports the apply was never asked about — and RouterOS persists
	// immediately, with no running/startup split to fall back on.
	"dhcp_snooping": true,
}

// The READ counterpart of this emptiness is not here but in
// Capabilities().Monitoring.Unavailable (routeros.go): what the platform does not expose
// for monitoring, and why. It lives there because it must be readable from
// driver.Capabilities for a given model, whereas WritableExtras is consulted only at
// apply time. Same principle in both cases — absence is declared by the package that
// KNOWS, not by the API layer.

// WritesExtra reports whether this connector applies the named intent.
func WritesExtra(name string) bool { return WritableExtras[name] }
