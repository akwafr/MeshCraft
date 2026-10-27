package swos

// What this connector can WRITE, and especially what it cannot.
//
// Declared HERE, in the package that writes, not in the API layer: the day a
// new intent becomes writable, the declaration and the code that writes it
// change in the same directory. A capability declared far from the write path
// always ends up claiming something other than what actually happens — and a
// capability that lies is worse than no capability at all, because it gives
// the operator configuration they believe is applied.
//
// Same mechanism and reason as instanton.WritableExtras: meshcraft APPLIES
// everything supported, considers the device up to date for that scope, and
// SAYS what was left behind.

// WritableExtras: intents OTHER than VLAN membership that this connector
// actually applies (cf. recon.ExtraIntents for what the model can carry:
// `poe`, `stp`, `dhcp_snooping`, `port_security`).
//
// `stp` is applied via ApplySpanningTree (/rstp.b `ena`, /sys.b `prio`). DHCP
// snooping and port security remain absent on purpose: they still share pages that
// also carry management allow-from fields, and those writes are not proven on
// hardware yet.
//
// # `port_admin` — DECLARED 2026-08-06, WITH THE RISK ACCEPTED RATHER THAN CLOSED
//
// This line previously argued AGAINST declaring, and the argument was not wrong:
// it is repeated below because it is still the truth about this platform, and it
// is the warning an operator has to have read. What changed is not the evidence
// but the decision — the operator asked for the shut / un-shut gesture across the
// whole fleet and accepted this platform's exposure explicitly.
//
// THE WARNING, UNCHANGED:
//
//   - SwOS PERSISTS IMMEDIATELY. There is no running/startup split, no save step,
//     and NO REBOOT THAT UNDOES A SHUT PORT. Every CLI platform in this codebase
//     can decline to save so that a reload brings the port back; here that recovery
//     does not exist;
//   - this model has NEITHER SSH, NOR TELNET, NOR A SERIAL PORT. If the write cuts
//     administration, the only way back is the reset button, ON SITE, which also
//     loses the configuration;
//   - and the write has never been sent to a device. What is proven is the PAGE
//     SHAPE — `en` and `lnk` in /link.b, from backup.swb — not the gesture.
//
// WHAT STANDS BETWEEN THAT AND AN OUTAGE, per gesture, and it is why the risk is
// bearable rather than merely accepted:
//
//   - [PlanPortAdmin] is PURE and tested against the device's own captures. A port
//     with a live link is spared when it carries the management VLAN, when
//     /sys.b `allp` lists it among the few ports administration is accepted from,
//     or — on a device whose "Allow From VLAN" is 0, the CSS factory state —
//     whenever it has a link at all. That last fallback is STRICTER than the
//     nominal case, not looser;
//   - the page is REFUSED rather than truncated when any writable field is missing:
//     posting /link.b without `nm` would rename twenty-six ports to nothing while
//     shutting one;
//   - `GET /backup.swb` is taken immediately before the first post and returned in
//     [driver.ApplyResult.Backup]. On this platform that copy is the ONLY rollback
//     there is, which is exactly why it is taken here and not left to the caller;
//   - nothing is posted when the ports already carry the wanted state, and the
//     re-read — never the HTTP status — decides what moved.
//
// TODO.md still names the live exercise (a spare port on the lab CSS326, with the
// twenty-six names in `nm` checked across the round trip): declaring the capability
// does not retire that item, it raises its urgency.
//
// # `storm_control` — DECLARED 2026-08-08, ON A GESTURE THAT WAS ACTUALLY SENT
//
// Unlike the two above, this one is not a declaration made on a page shape. The
// write was performed on the lab CSS326 (192.0.2.38, SwOS 2.17) against Port2, a
// dark port, and the round trip is in live_stormcontrol_test.go:
//
//   - `suni` moved 0 → 2 (bit 1 = Port2) and `srt[1]` 100 → 10, both observed by a
//     SECOND session that did not perform the write, so no cached page could
//     account for it;
//   - a second identical apply posted nothing and left the device unchanged, so
//     the applier converges rather than rewriting;
//   - the whole of /fwd.b was compared field by field, every kind rendered, before
//     and after: the twenty-six `fp` isolation masks, `vlan`, `vlni`, `dvid`,
//     `fvid` and the rest were untouched. This is the assertion that matters here,
//     because the page is posted WHOLE and the failure mode is zeroing a field
//     nobody mentioned;
//   - and the page came back exactly as found.
//
// TWO DEFECTS THE EXERCISE FOUND, both fixed before this line was written. The
// verify step matched the re-read by port NAME, so a plan using meshcraft's
// numbered family ("GE2") matched nothing against a device that answers
// "Port2": the applier disowned a write it had really performed, reported "the
// device did not take it", and returned an empty PortsWritten — on which a
// caller's revert would reasonably decline to undo anything. It now matches by
// faceplate index. And reverting the bit does NOT revert the page, because `srt`
// is a standing value the enable bit does not own (see stormRateNoLimit).
// # `dhcp_snooping` — DECLARED 2026-08-09, ON THE PAGE THAT CARRIES ACCESS
//
// The one field is `dtrp`, a trusted-port MASK on /sys.b. There is no global
// enable and no per-VLAN dimension on this platform — established by exhaustive
// enumeration of the device's own backup, the same method mgmtaddr.go uses to
// prove there is no gateway field — so a port asked to be untrusted has its bit
// cleared, which is the PROTECTED state. This connector cannot turn the feature
// off, structurally rather than by discipline.
//
// WHY THIS IS THE MOST DANGEROUS WRITE ON THE PLATFORM, and why it was still
// worth declaring. /fwd.b can cost port isolation and VLAN modes. /sys.b can cost
// ACCESS: it carries `ip`, `avln` ("Allow From VLAN"), `allp` ("Allow From
// Ports") and `mgmt` beside `dtrp`, it is posted WHOLE like every SwOS page, and
// this model has neither SSH, nor Telnet, nor a serial port. A field dropped from
// the body is a field the device zeroes, and the way back from a zeroed
// allow-list is the reset button, on site, which also loses the configuration.
//
// THE EVIDENCE, on the lab CSS326 (192.0.2.38, SwOS 2.17), Port2, a dark port:
//
//   - `dtrp` moved 0x03FFFFFF → 0x03FFFFFD — bit 1 cleared, and nothing else;
//   - EXACTLY ONE of the twenty-two fields this connector posts changed. `ip`,
//     `avln`, `allp` and `mgmt` were asserted by name, not merely swept up in a
//     diff, so a failure would say which access field moved;
//   - a second identical apply posted nothing;
//   - the page came back equal, field by field, across the whole posted set.
//
// The POST machinery itself was not new — management addressing already writes
// this page on hardware. What had never been sent was `dtrp`.
var WritableExtras = map[string]bool{
	// Spanning-tree: /rstp.b `ena` (+ optional edge) and /sys.b `prio`, applied by
	// ApplySpanningTree. Proven writable fields come from backup.swb.
	"stp": true,
	// Administrative state: the `en` mask of /link.b, applied by ApplyPortAdmin.
	// READ THE SECTION ABOVE BEFORE TOUCHING THIS LINE — on this platform the write
	// is instantly persistent and no reboot undoes it.
	"port_admin": true,
	// Unknown-unicast storm limiter: /fwd.b `suni` + `srt`, applied by
	// ApplyStormControl. DECLARED 2026-08-08 on the evidence below.
	"storm_control": true,
	// DHCP snooping trust: the /sys.b `dtrp` mask, applied by ApplyDHCPSnooping.
	// DECLARED 2026-08-09. READ THE SECTION ABOVE — this is the page that carries
	// access, and it is the only write on this platform that could end the session.
	"dhcp_snooping": true,
}

// The READ counterpart of this emptiness is not here but in
// Capabilities().Monitoring.Unavailable (swos.go): what the platform does not
// expose for monitoring, and why. It lives there because it must be readable
// from driver.Capabilities for a given model, whereas WritableExtras is
// consulted only at apply time.
//
// There is a third counterpart, specific to this platform, that fits neither
// contract field: the membership REMOVAL policy. Apply now removes memberships
// the plan no longer asks for, with four refusals that protect the management
// VLAN, the effective native VLAN, ports in "all", and active ports on a device
// whose administration VLAN is unknown. Each reason is written at length in
// apply.go (removalPolicy, planLeaves), next to the code that decides.
//
// There is a FOURTH counterpart, and it is the only remaining write refusal on
// this platform's VLAN model: neither "VLAN Receive" (/fwd.b `vlni`) nor "Force
// VLAN ID" (/fwd.b `fvid`) are written, even though both are writable and are
// already echoed back to the device on every apply with their read value. The
// reason is in ports.go (receivePolicy): tightening them DROPS traffic, and the
// standard model does not express what would be needed to decide which. They
// are READ, however, and flagged when they contradict the port role
// (vlanAnomalies).

// WritesExtra reports whether this connector applies the named intent.
func WritesExtra(name string) bool { return WritableExtras[name] }

// RemovalPolicy returns, in one sentence, what apply removes and what it
// refuses to remove. Meant for the UI: the operator must be able to read the
// limit without opening the code.
func RemovalPolicy() string { return removalPolicy() }

// ReceivePolicy returns, in one sentence, the two per-port VLAN fields this
// connector never writes, and why.
//
// Exported for the same reason as RemovalPolicy — a third form of the same
// principle: what we do not do must be readable from outside the package.
// `WritableExtras` names intents not applied, `Monitoring.Unavailable` names
// missing metrics, and these two sentences say what apply forbids INSIDE an
// intent it otherwise honors — a nuance no contract field carries.
func ReceivePolicy() string { return receivePolicy() }
