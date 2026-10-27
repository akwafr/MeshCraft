package api

// Hostname reconciliation — meshcraft OWNS the device's own name, pushes it when
// the operator renames, and repairs the drift it finds afterwards.
//
// # What this file replaced, and the defect that shape had (2026-08-06)
//
// Until today the system name was a button: POST /api/devices/{id}/hostname/apply,
// one shot, on demand, and internal/driver/hostname.go argued at length that this
// was correct — that a hostname changed at the console was somebody's deliberate
// decision. That argument is now recorded in that same file as WRONG in an
// instructive way. What it protected was an edit nobody can find later; what it
// cost was the three consumers of the name — console banner, LLDP system-name TLV,
// every syslog line — going stale fleet-wide, silently, on exactly the devices
// nobody had thought to click.
//
// So the name is reconciled, and this file is the reconciliation: the INTENT is
// `devices.name` normalized (hostnameFromDeviceName), the OBSERVATION is what the
// device announces, a difference is DRIFT, and drift is repaired with a per-device
// cooldown that doubles on failure. An operator who renames at the console gets
// overruled on the next pass — deliberately, and VISIBLY, because the repair emits
// an event naming both names.
//
// # THE OBSERVATION IS FREE, which is the only reason periodic checking is sane
//
// The device's own system name already arrives on every SNMP poll cycle as
// `lldpLocSysName` and is stored in `lldp_self.sys_name`. The drift comparison
// therefore opens NO SESSION AT ALL: a pass over two hundred devices costs one
// query per site plus pure arithmetic, and a session is spent only where a repair
// is actually due. That matters on the ranges that grant five sessions (Instant On)
// or four (ProCurve), where a loop that connected to look would starve the
// operators' own gestures.
//
// `extra.identity.hostname` — written by the read-state identity self-heal, cf.
// identityFacts — is the SECOND source, for devices whose LLDP or SNMP is not
// answering.
//
// A device where NEITHER source has reported is `not_observed`, and that is NOT
// `drift`: nothing is repaired on the strength of an absent reading. Without that
// rule every unreachable device would queue a write forever, and the log would
// fill with attempts that never had an observation to justify them.
//
// # Structure borrowed from snmpheal.go, on purpose
//
// Same problem — a write whose only chance to succeed used to be one instant — and
// therefore the same shape: one pass at startup (the "this capability just arrived
// in this binary" case), a periodic pass, a per-device cooldown that doubles on
// failure so a durably unreachable device costs about one attempt a day, and
// SILENT skips for everything already explained where an operator looks for it (no
// address, no connector, apply in flight).
//
// # What this file does NOT do
//
// It does not invent an observation. A successful SetHostname is not recorded as
// "the device now announces this": convergence is judged from the sources above,
// on the next pass, exactly as drift was. It does not touch `devices.name` — the
// name flows one way only, from the inventory to the equipment. And it never
// truncates or rewrites a name to make it pushable: an unnameable `devices.name`
// is reported to the operator (`unnameable`), because that is a decision only they
// can make (cf. hostnameFromDeviceName's own header).

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

const (
	// hostnameHealStartupDelay: 90 s of it is the reason snmpHealStartupDelay uses
	// that figure — let the proxies' tunnels come back up, so a push does not fail
	// for a cause that disappears on its own. The extra 30 s is one LLDP poll
	// cycle, so the FIRST pass judges on an observation refreshed since this binary
	// started rather than on whatever the previous one left in `lldp_self`.
	hostnameHealStartupDelay = 2 * time.Minute
	// hostnameHealEvery: how long a console rename survives. Deliberately SLOWER
	// than snmpHealEvery (15 min): supervision not starting is a hole in what
	// meshcraft can see, while a stale name is a legibility defect — nothing
	// depends on it working, three things depend on it being right. Thirty minutes
	// is also the honest ceiling on the observation's own freshness: the value
	// compared here is refreshed by the poll cycle, not by this loop.
	hostnameHealEvery = 30 * time.Minute
	// Cooldown after a failed repair, doubled on each subsequent failure. Same
	// figures as the SNMP catch-up and for the same reason: a device that is
	// durably unreachable, or whose platform refuses the name for its own limit,
	// must end up costing ONE attempt a day — enough that a fixed device
	// converges without a restart, little enough that nothing drowns.
	hostnameHealBackoffMin = 1 * time.Hour
	hostnameHealBackoffMax = 24 * time.Hour
	// hostnameHealDeviceBudget caps one device's session + write. A hostname is
	// one command on every platform that carries the gesture; this ceiling exists
	// so one wedged transport cannot stall the devices queued behind it.
	hostnameHealDeviceBudget = 90 * time.Second
)

// The five values of HostnameConformity.State. Exactly five, and the two that
// must never be conflated are `unsupported` and `drift`: the first says this can
// NEVER converge, the second promises a repair is coming. A screen showing the
// second for the first would promise a repair that never arrives, which is the
// failure internal/driver/hostname.go's last paragraph names.
const (
	hostnameOK          = "ok"
	hostnameDrift       = "drift"
	hostnameNotObserved = "not_observed"
	hostnameUnsupported = "unsupported"
	hostnameUnnameable  = "unnameable"
)

// Where an observation came from. Reported to the UI so a surprising verdict can
// be traced to the reading that produced it — "lldp" is refreshed every poll
// cycle, "read_state" is as old as the last click on this device.
const (
	hostnameSourceLLDP      = "lldp"
	hostnameSourceReadState = "read_state"
)

// hostnameWritableConnectors says, per connector, whether the system-name WRITE
// has been proven on that platform.
//
// # Why a table, when the capability is an interface
//
// [driver.HostnameSetter] is resolved by a type assertion on a live
// [driver.Admin] — which is to say AFTER a session has been opened. That is the
// right contract for the gesture and the wrong one for a verdict: the drift state
// is computed on lists of two hundred devices, on every fleet screen, and it must
// distinguish `unsupported` from `drift` WITHOUT touching a single device. There
// is no session-free path to the assertion, so the answer is declared here, keyed
// on the connector name that `devices.extra.driver` already carries.
//
// # What keeps it honest
//
// Two guards, because a hand-kept table of capabilities is exactly the kind of
// thing that rots. hostnameheal_test.go is the build-time detector — the same role
// portnamer_consistency_test.go plays for the other capability nothing in the type
// system enforces: it asserts every entry against the connector's real Admin type,
// and it FAILS on any registered connector missing from this map, so shipping a
// new connector forces a line here. And pushHostname logs loudly when a session
// contradicts the table, which is the runtime half.
//
// An unknown connector is treated as CANNOT — the project rule that an
// undeclared capability is absent, applied where erring the other way would
// promise a repair that never comes.
var hostnameWritableConnectors = map[string]bool{
	// PROVEN ON HARDWARE, both by a live rename observed converging:
	//   cisco-ios         — "hostname X", 2026-08-05 21:48 on CiscoLabFirst1;
	//                       the device announces "CiscoLabFirst1.meshcraft.local"
	//                       and hostnameMatches' DNS-suffix tolerance is what makes
	//                       that read `ok` instead of looping forever.
	//   mikrotik-routeros — POST /rest/system/identity/set, 2026-08-05 23:24 on
	//                       lab-crs326, which was answering to "lan-r4"; both
	//                       observation sources agree afterwards.
	"cisco-ios":         true,
	"mikrotik-routeros": true,
	// PROVEN ON HARDWARE, 2026-08-10 — each renamed THROUGH meshcraft (the
	// healer's own push, not a console), watched converge on `device.hostname_set`,
	// and reverted:
	//   fs-s3400 / fs-s3900 — and the identity read added the same day OBSERVED
	//                       both, where they had been permanently `not_observed`.
	//                       Persistence confirmed by REBOOT ("show startup-config
	//                       | include hostname"); mind that the verb on these is
	//                       `reboot`, not `reload`, and that an S3900's prompt
	//                       appends the stack unit (`s3900lab-1#`) so the prompt is
	//                       NOT the hostname.
	//   mikrotik-swos     — the `/sys.b` `id` write, verified field by field
	//                       afterwards: 26 port names identical, address, static
	//                       mode and four VLANs unchanged, SFP2 still up. SwOS has
	//                       NO running/startup split, so a value read back IS the
	//                       persisted value — which is just as well, because
	//                       lab-css326 has no SSH, no Telnet and no serial, and a
	//                       reboot that does not come back is recovered with the
	//                       front-panel button, on site.
	"fs-s3400":      true,
	"fs-s3900":      true,
	"mikrotik-swos": true,
	// STILL DEDUCED, and now the only one — the form has never been sent to this
	// hardware, and the loop will be what tries it first, because the lab 2610 is
	// OFFLINE. It verifies by re-reading and refuses to claim an unconfirmed
	// write, so a wrong form surfaces as `device.hostname_push_failed` at severity
	// 3 rather than as a silent lie.
	"hpe-procurve": true,
	// arista-eos: PROVEN ON HARDWARE, 2026-08-10 — core01 was renamed through
	// meshcraft and reverted, and the verdict CONVERGED. It was deduced for four
	// days on the grounds that EOS takes "hostname X" in global configuration,
	// the same line as IOS, and that this is one of the few gestures where the two
	// CLIs genuinely agree (the four places they do not are named in the
	// connector's package doc). The deduction was right; it is no longer what
	// holds this entry up.
	//
	// One thing to expect on this platform that the others do not have: a switch
	// fresh out of ZTP answers to "localhost". That is not a neutral default —
	// an unprovisioned fleet is a dozen distinct switches all announcing the same
	// name, which is exactly the correlation failure this healer exists to end,
	// so these devices are the ones it will have the most work to do on.
	"arista-eos": true,
	// ubiquiti-edgeswitch: one verb REFUTED, the right one CAPTURED — the entry
	// has been both values and the history is why it is trusted now.
	//
	// REFUTED, 2026-08-05 23:24: the deduced "hostname <name>" in Global Config
	// drew "% Invalid input detected at '^' marker." from the lab EdgePoint S16.
	// This FASTPATH-derived CLI has no `hostname` command, the entry went false,
	// and the fleet correctly reported `unsupported` rather than emitting a
	// severity-3 event a day on a gesture that could not work.
	//
	// CAPTURED AND VERIFIED, 2026-08-10, on the same unit over a console session:
	// "snmp-server sysname mctest1" is accepted, and "show lldp local-device
	// detail 0/18" then reads "System Name: mctest1" where it read "System
	// Name: UBNT EdgeSwitch" before. So the WRITE is captured AND the TLV THIS
	// HEALER COMPARES — `lldpLocSysName`, via `lldp_self.sys_name` — is captured
	// moving with it. That second half is what the entry turns on: the CLI prompt
	// stayed "(UBNT EdgeSwitch) #" throughout, so `set prompt` would have been a
	// form that converges on screen and changes nothing this loop can observe.
	//
	// True, therefore — with two things NOT yet proven and stated here because
	// this is the table an operator reads: no device has been renamed through the
	// connector's own SetHostname (the capture was typed at a console), and the
	// change was never saved to startup-config, so persistence across a reload is
	// unobserved. The connector re-reads the TLV and refuses to claim an
	// unconfirmed write, so either surfaces as `device.hostname_push_failed` at
	// severity 3 rather than as a silent lie (cf. edgeswitch/hostname.go).
	"ubiquiti-edgeswitch": true,
	// aruba-instant-on-1930: PROVEN ON HARDWARE, 2026-08-11, and it was the last
	// `false` in this map. The read had been settled since 2026-08-10 — section
	// `SystemGlobalSetting`, field `systemName`, the same spelling the login page's
	// hidden input carries — while the WRITE had never been sent, which is the only
	// thing this entry has ever been about.
	//
	// THE RUN: on sw-labo-1930 the probe tried `SystemSetting` (which answers, in
	// 306 bytes, with no name field) and settled on `SystemGlobalSetting`; the POST
	// returned code 0; an INDEPENDENT section re-read carried the new name; the
	// configuration was saved to flash; and the serial the unit shipped with
	// (`CN0EXAMPL1`) was written back the same way. Both edits landed together —
	// the method is named SetHostname now — because either alone fails
	// hostnameheal_test.go, which is what makes them one change.
	//
	// TWO RANGE RULES THAT DID NOT GO AWAY. Nothing else on this platform persists
	// (a power cycle is its only rollback), so the hostname write is one of the few
	// gestures here that deliberately DOES save. And the session budget is FIVE
	// simultaneous sessions: a burst of probes comes back as "too many open
	// sessions on device (5 maximum)" on the NEXT gesture rather than as a queue,
	// so this loop's one-session-per-push discipline is load-bearing on this range.
	//
	// WHAT THIS RETIRES: the unit answered to its SERIAL in the LLDP system-name
	// TLV, which is one of the two systematic name mismatches that made a naive
	// SQL asymmetry join report one-sided pairs that are not one-sided. With the
	// range writable, the healer converges that TLV onto the meshcraft name.
	"aruba-instant-on-1930": true,
}

// hostnameWriteSupport reports whether meshcraft's name can EVER reach this
// device, and says why not in the operator's terms when it cannot.
//
// PURE and database-free, like outOfBandOf and for the same reason: it is called
// once per device on a list of two hundred, and going back through the discovery
// candidate would cost a query per row.
//
// The connector resolution is folded in on purpose. "No connector associated",
// "connector unknown to this binary" and "connector cannot reach this platform"
// are three different repairs, but from this verdict's point of view they share
// the only property that matters: the name will not converge as things stand, so
// announcing a coming repair would be a lie. [driver.ConnectorFor]'s own sentence
// travels into Because, which is where the three stay distinguishable.
func hostnameWriteSupport(d model.Device) (bool, string) {
	var name = driver.DriverNameFromExtra(d.Extra)
	var _, err = driver.ConnectorFor(name)
	if err != nil {
		return false, err.Error()
	}
	var writes, classified = hostnameWritableConnectors[name]
	if !classified {
		return false, "connector \"" + name + "\" is not classified for the " +
			"system-name write: meshcraft treats an undeclared capability as ABSENT " +
			"rather than discovering it on a production device"
	}
	if !writes {
		return false, "connector \"" + name + "\" cannot set this platform's system " +
			"name: the field exists on every range surveyed, the WRITE has not been " +
			"proven on this one. meshcraft's name will not converge here, and it does " +
			"not pretend a repair is coming"
	}
	return true, ""
}

// hostnameObserved returns the name the DEVICE announces, and where that reading
// came from. Empty name and empty source mean "not observed", which is not
// "conforming" and above all not "drifted".
//
// LLDP FIRST because it is the free and fresh one: `lldp_self.sys_name` is
// rewritten on every poll cycle. `extra.identity.hostname` is the fallback for a
// device whose LLDP or SNMP is silent, and it is only as fresh as the last
// read-state on that device — hence Source travelling to the UI.
//
// An LLDP row with an EMPTY sys_name falls through to the second source rather
// than counting as an observation: a device that serves the LLDP-MIB without
// publishing a system name has told us nothing about its name (cf.
// model.LLDPSelf.Served, same distinction). That is the FS S3400/S3900 shape, and
// it is a state to pass through, not an error — since 2026-08-10 the fall-through
// actually finds something on those two ranges (below), where before it found
// nothing and they read `not_observed` for ever.
//
// THE SECOND SOURCE IS THINLY FED, and a reader must not assume otherwise. This
// paragraph said "only mikrotik-routeros populates driver.Identity.Hostname"
// until 2026-08-09, which is the same wrong claim refreshIdentityAfterHostname
// carried next door: it counted ONE of the two paths that can fill the field, and
// "one of nine ranges" is a different fleet from "four of nine". Corrected,
// with the three levels kept apart because they behave differently:
//
// CONTINUOUSLY, on every read-state — mikrotik-routeros, fs-s3400 and fs-s3900,
// by two different mechanisms that must not be confused.
//
// RouterOS carries the name inside its [driver.IdentityReader] itself
// (routeros/identityread.go:22 → Identify → DeviceIdentity.Standard,
// inspect.go:586): the REST objects it re-reads there hold it, so any caller of
// ReadIdentity gets it.
//
// The two FS ranges joined on 2026-08-10 by the other route, and the difference
// is load-bearing. Their name lives in the running configuration and NOWHERE
// cheaper — no banner, no `show version` — and reading it costs a full
// configuration read (ninety seconds of paged output on the S3400, whose pager
// cannot be disabled). That read is NOT on the identity path: ReadState performs
// it as part of its own work and hands the parsed name to the connector, whose
// ReadIdentity then reports it from memory at zero round trips (each package's
// identityread.go states the cadence and the cost). So they answer on the
// read-state gesture — which always reads state before self-healing identity —
// and answer NOTHING on an access where read-state has not run. The rename path
// is exactly that case, deliberately: see refreshIdentityAfterHostname.
//
// cisco-ios and arista-eos implement the same interface and still carry no name:
// they build an identity with model, serial, MAC and firmware and no Hostname
// (each package's identityread.go).
//
// ONCE PER DEVICE AND NEVER AGAIN — hpe-procurve (inspect.go:119, the "System
// Name" row of `show system-information`), aruba-instant-on-1930
// (inspect.go:140, the login page's `sysName` hidden input) and mikrotik-swos
// (inspect.go:287, `/sys.b`'s `id`). None of the three is an IdentityReader, so
// they are reached only through observeIdentity's [driver.Identifier] FALLBACK,
// which runs exactly when `devices.model` is EMPTY (identity_selfheal.go:119) —
// at most once, on the first read-state after adoption. The value is then FROZEN.
//
// SINCE 2026-08-10 THIS FUNCTION REFUSES TO REPORT THAT VALUE AT ALL, and the
// refusal is the point rather than an omission. Until then it returned the frozen
// name tagged `read_state`, which is a reading taken at adoption presented as an
// observation of the device now — so a switch renamed at its console reported
// AGREEMENT with a name it had not answered to for months, and a switch whose
// stored name disagreed reported `drift`, got pushed, and never converged, because
// hostnameConverged re-reads this same unmovable fact. One push a day, for ever, on
// the platforms with the smallest session budgets. The cost of refusing is that
// these ranges lose the identity source and are judged by LLDP or not at all
// (`not_observed`, which repairs nothing); the alternative was re-identifying on a
// cadence, which was weighed and refused — a session per device per pass, on the
// two smallest session budgets in the fleet, to detect a rename nobody has made.
// identityRefreshingConnectors holds the test and the full argument.
//
// NEVER, on three ranges — cisco-ios, arista-eos and ubiquiti-edgeswitch.
// Nothing on either path fills Hostname for them. It was FIVE until 2026-08-10,
// and the two that left were the ones it hurt most: combined with the blank
// `lldpLocSysName` above, neither FS range had any observation source at all, so
// both sat at `not_observed` for ever — and since only `drift` is repairable, a
// name meshcraft had written could never be checked, only rewritten at the next
// rename. For the remaining three the gap is milder: ubiquiti-edgeswitch reads
// `unsupported` before any observation is consulted (the write itself was refuted
// on hardware), and cisco-ios and arista-eos are judged for exactly as long as
// their LLDP row carries a system name — nothing here checks that it does.
func hostnameObserved(d model.Device, self model.LLDPSelf, announced bool) (string, string) {
	if announced {
		var sysName = strings.TrimSpace(self.SysName)
		if sysName != "" {
			return sysName, hostnameSourceLLDP
		}
	}
	var stored = strings.TrimSpace(storedIdentityFacts(d.Extra)["hostname"])
	// A stored name is an OBSERVATION only where it is re-taken on every read-state.
	// Where it cannot be, it is a fact about the device at adoption, and this axis
	// has a word for having nothing to say about the present — it is "not
	// observed", the same answer as for a device no source has ever reported.
	if stored != "" && identityCanBeReObserved(d) {
		return stored, hostnameSourceReadState
	}
	return "", ""
}

// hostnameConformityOf is the verdict, PURE: no device session, no database.
//
// # The order of the tests IS the design
//
// `unsupported` is decided FIRST, before the name is even judged pushable. On a
// platform meshcraft cannot write, telling the operator to rename the device
// (`unnameable`) would send them to a gesture that changes nothing on the
// equipment — the honest sentence there is that this range cannot be renamed by
// meshcraft at all. `Desired` is still filled in when the name normalizes, so the
// screen can show what WOULD be pushed.
//
// `drift` does not promise the repair SUCCEEDS — only that meshcraft will attempt
// it, and keep attempting it on a doubling cooldown. The two states that promise
// nothing at all are `unsupported` and `unnameable`, and that is exactly why they
// are separate values rather than a flag on `drift`.
func hostnameConformityOf(d model.Device, observed, source string) HostnameConformity {
	var c = HostnameConformity{Observed: observed, Source: source}
	var desired, refusal = hostnameFromDeviceName(d.Name)
	c.Desired = desired

	var writable, why = hostnameWriteSupport(d)
	if !writable {
		c.State, c.Because = hostnameUnsupported, why
		return c
	}
	if refusal != "" {
		// Desired stays EMPTY here — that is the DTO's contract, and it is what
		// lets the UI show "nothing can be pushed" rather than a name it would
		// then have to explain away.
		c.State, c.Because = hostnameUnnameable, refusal
		return c
	}
	if observed == "" {
		c.State = hostnameNotObserved
		c.Because = hostnameNotObservedBecause(d)
		return c
	}
	if hostnameMatches(observed, desired) {
		c.State = hostnameOK
		if observed != desired {
			// Same name, different presentation. Say so, or the screen shows two
			// visibly different strings and a green verdict with no explanation.
			c.Because = "this device announces \"" + observed + "\" — the same name as " +
				"\"" + desired + "\" up to letter case and DNS domain, which is not drift"
		}
		return c
	}
	c.State = hostnameDrift
	c.Because = "this device announces \"" + observed + "\" while the inventory calls it " +
		"\"" + desired + "\". meshcraft owns the name and will push it back: a device " +
		"answering to a name the inventory does not carry cannot be correlated at three " +
		"in the morning"
	return c
}

// hostnameNotObservedBecause writes the sentence for `not_observed`, and it has
// TWO because the state has two causes that look identical from the verdict and
// call for opposite reactions from the operator.
//
// "Nothing has ever reported" is a device to make answer — enable LLDP, read its
// state, check it is reachable. "Something reported once, at adoption, and can
// never report again" is not a device problem at all: meshcraft holds a name for
// this switch and is declining to compare against it, because a reading from
// adoption cannot say what the box answers to today (cf.
// identityRefreshingConnectors). Printing the first sentence there would send an
// operator hunting a fault that is not on the equipment — and it would be false on
// its face, since a read-state identity DID report.
//
// The stored name travels into the sentence deliberately. It is the one thing that
// makes the distinction checkable from a screen: the operator can compare it with
// the device's console themselves, which is exactly the gesture meshcraft is
// refusing to do on a cadence.
func hostnameNotObservedBecause(d model.Device) string {
	var frozen = strings.TrimSpace(storedIdentityFacts(d.Extra)["hostname"])
	if frozen != "" && !identityCanBeReObserved(d) {
		return "this device's own name is not observable: its LLDP system name is " +
			"absent, and the \"" + frozen + "\" its identity carries was read ONCE, at " +
			"adoption — this connector cannot re-read it, so meshcraft does not know " +
			"whether the device still answers to it. A stale reading is not evidence " +
			"of agreement, and nothing is repaired on one"
	}
	return "no source has reported this device's own name yet: neither its " +
		"LLDP system name nor a read-state identity. meshcraft repairs nothing on " +
		"an absent reading — a name is pushed here when the device is renamed, or " +
		"once it starts answering"
}

// hostnameMatches compares an OBSERVED name with the desired one.
//
// TWO TOLERANCES, and each one closes a write loop that would otherwise never
// stop — the failure mode to weigh here is not "a stale name survives" but
// "meshcraft re-pushes the same name every day forever, emitting an event each
// time, on a device that is already correct".
//
//   - CASE-INSENSITIVE: RFC 1123 names are, and a platform that stores the name in
//     its own case (or uppercases it in the LLDP TLV) would otherwise read as
//     permanently drifted. The push would succeed, change nothing observable, and
//     be due again on the next cooldown;
//   - FIRST DNS LABEL ONLY: a Cisco IOS with `ip domain-name` set announces
//     `sw-lab.example.com` as its system name. That IS our name — the domain is
//     the device's own suffix, not a difference meshcraft could fix by writing the
//     hostname again.
//
// The cost is accepted and small: a device whose name differs from the inventory's
// ONLY by case is left alone. Nothing reads a hostname case-sensitively, and the
// alternative is the loop above.
func hostnameMatches(observed, desired string) bool {
	return desired != "" && strings.EqualFold(hostnameFirstLabel(observed), desired)
}

func hostnameFirstLabel(name string) string {
	var cut = strings.IndexByte(name, '.')
	if cut < 0 {
		return name
	}
	return name[:cut]
}

// --- The catch-up loop ---

// hostnameHealState: memory of attempts, IN MEMORY ONLY. A restart clears it, and
// that is intended — a fresh binary is exactly the case where a connector just
// learned the write, and it deserves an immediate retry.
//
// Deliberately NOT shared with snmpHealState, which has the same shape: the two
// are thirty lines of map arithmetic, and a common type would have to carry both
// policies' constants as fields — more machinery than the duplication costs. If a
// third loop needs it, THAT is when it becomes a type.
type hostnameHealState struct {
	mu   sync.Mutex
	next map[string]time.Time     // device -> not before this instant
	wait map[string]time.Duration // device -> current cooldown
}

func (h *hostnameHealState) due(id string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.next == nil {
		return true
	}
	return now.After(h.next[id])
}

func (h *hostnameHealState) failed(id string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.next == nil {
		h.next = map[string]time.Time{}
		h.wait = map[string]time.Duration{}
	}
	var w = h.wait[id]
	if w == 0 {
		w = hostnameHealBackoffMin
	} else {
		w *= 2
		if w > hostnameHealBackoffMax {
			w = hostnameHealBackoffMax
		}
	}
	h.wait[id] = w
	h.next[id] = now.Add(w)
}

func (h *hostnameHealState) succeeded(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.next, id)
	delete(h.wait, id)
}

// RunHostnameHeal runs the reconciliation until the controller stops.
func (s *Server) RunHostnameHeal(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(hostnameHealStartupDelay):
	}
	var t = time.NewTicker(hostnameHealEvery)
	defer t.Stop()
	for {
		s.hostnameHealPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// hostnameHealPass runs ONE pass. One device's failure stops neither the pass nor
// the loop.
//
// The LLDP self-rows are read ONCE PER SITE and cached for the pass, the way
// withConformity caches the management plan: the row is per device but the query
// is per site, and a fleet of two hundred must not cost two hundred round trips
// to answer a question that repairs nothing most of the time.
func (s *Server) hostnameHealPass(ctx context.Context) {
	var devices, err = s.allDevices(ctx)
	if err != nil {
		slog.Warn("hostname heal: device list unreadable", "err", err)
		return
	}
	var now = time.Now()
	var selves = map[string]map[string]model.LLDPSelf{}
	var repaired = 0
	var d model.Device
	for _, d = range devices {
		if ctx.Err() != nil {
			return
		}
		if !s.hostnameHeal.due(d.ID, now) {
			continue
		}
		var bySite, cached = selves[d.SiteID]
		if !cached {
			bySite = s.lldpSelfBySite(ctx, d.SiteID)
			selves[d.SiteID] = bySite
		}
		var self, announced = bySite[d.ID]
		var observed, source = hostnameObserved(d, self, announced)
		var verdict = hostnameConformityOf(d, observed, source)
		if !s.hostnameRepairable(d, verdict) {
			continue
		}
		s.pushAndVerifyHostname(ctx, d, verdict.Desired, observed, true, now)
		repaired++
	}
	if repaired > 0 {
		slog.Info("hostname heal: pass done", "repaired", repaired)
	}
}

// hostnameRepairable: only push where there is a drift AND a way to reach the
// device. Everything refused here is refused SILENTLY, because every one of these
// states is already explained where an operator goes looking for it — the four
// non-drift verdicts travel on the device payload as `hostname.state`, a missing
// management address is the device page's own headline, and an apply in flight has
// its own badge.
//
// The apply guard is the PoE sampler's, for its reason: session budgets are small
// (Instant On five, ProCurve four), and opening one behind a three-minute apply
// would starve the operator's own gestures. Skipping rather than queuing — the
// next pass comes soon enough for a name.
func (s *Server) hostnameRepairable(d model.Device, verdict HostnameConformity) bool {
	if verdict.State != hostnameDrift {
		return false
	}
	if d.MgmtIP == "" {
		return false
	}
	return !s.applyQueuedOrRunning(d.ID)
}

// pushAndVerifyHostname performs ONE push and updates the device's cooldown.
//
// Shared by the periodic pass and by the rename hook, so a name that reaches a
// device leaves the SAME footprint whichever brought it: one event, one pipeline,
// one backoff entry. `repair` only selects the wording — "somebody renamed this
// at the console and was overruled" is a different sentence from "the operator
// renamed it in meshcraft", and both must be readable a month later.
//
// CONVERGENCE IS JUDGED FROM THE OBSERVATION, never from the return code, and
// that is the same stance healOneDeviceSNMP takes on snmp_status: the connectors
// re-read after writing, but the fact every screen displays is the one the poll
// cycle brings back. A push that succeeded while the observation has not caught up
// yet therefore lands in the cooldown — the next pass sees it converged and clears
// it, and meanwhile that same cooldown bounds the genuine failures.
func (s *Server) pushAndVerifyHostname(ctx context.Context, d model.Device,
	desired, observed string, repair bool, now time.Time) {

	slog.Info("hostname: pushing the inventory name", "device", d.Name,
		"ip", d.MgmtIP, "desired", desired, "observed", observed, "repair", repair)

	var devCtx, cancel = context.WithTimeout(ctx, hostnameHealDeviceBudget)
	defer cancel()
	var push = s.pushHostname(devCtx, d, desired)
	s.recordPipeline(ctx, pipelineForHostname(d, push, repair), "")
	if push.Err != nil {
		s.emitDeviceBG(d, "device.hostname_push_failed", model.SevWarning,
			"System name \""+desired+"\" not set on \""+d.Name+"\": "+push.Err.Error())
		s.hostnameHeal.failed(d.ID, now)
		return
	}
	s.emitDeviceBG(d, hostnameEventType(repair), model.SevNotice,
		hostnameEventMessage(d, desired, observed, repair))

	if s.hostnameConverged(ctx, d, desired) {
		s.hostnameHeal.succeeded(d.ID)
		return
	}
	s.hostnameHeal.failed(d.ID, now)
}

func hostnameEventType(repair bool) string {
	if repair {
		return "device.hostname_repaired"
	}
	return "device.hostname_set"
}

// hostnameEventMessage: the sentence that makes the overrule VISIBLE.
//
// This is the half of the design that keeps automatic repair defensible. Somebody
// standing at a console renamed a switch and meshcraft undid it; that person must
// be able to find out why, from the device's own history, without reading Go. So
// the message names BOTH names and says who won.
func hostnameEventMessage(d model.Device, desired, observed string, repair bool) string {
	if !repair {
		return "System name set on \"" + d.Name + "\": \"" + desired + "\" — pushed " +
			"because the device was renamed in meshcraft."
	}
	if observed == "" {
		return "System name repaired on \"" + d.Name + "\": \"" + desired + "\"."
	}
	return "System name repaired on \"" + d.Name + "\": the device was answering to " +
		"\"" + observed + "\", meshcraft owns the name and set it back to \"" + desired +
		"\". A rename made on the device itself is overruled — rename it in meshcraft " +
		"instead, and the new name is pushed."
}

// hostnameConverged re-reads the two observation sources and says whether the
// device now answers to the name we sent.
//
// Costs one device row plus one `lldp_self` row, and only ever runs after an
// actual push — never in the verdict path, which must stay free.
func (s *Server) hostnameConverged(ctx context.Context, d model.Device, desired string) bool {
	var fresh, err = s.Store.GetDevice(ctx, d.ID)
	if err != nil {
		return false
	}
	var self, selfErr = s.Store.GetLLDPSelf(ctx, d.ID)
	var observed, _ = hostnameObserved(fresh, self, selfErr == nil)
	return hostnameMatches(observed, desired)
}

// lldpSelfBySite indexes a site's announced identities by device.
//
// A read failure yields an EMPTY map, not an error: without observations every
// device reads `not_observed`, the pass repairs nothing, and the next one tries
// again. Turning a transient database hiccup into a fleet-wide write would be the
// exact inversion of this file's central rule.
func (s *Server) lldpSelfBySite(ctx context.Context, siteID string) map[string]model.LLDPSelf {
	var out = map[string]model.LLDPSelf{}
	var rows, err = s.Store.ListLLDPSelf(ctx, siteID)
	if err != nil {
		slog.Warn("hostname heal: LLDP self rows unreadable", "site", siteID, "err", err)
		return out
	}
	var i int
	for i = range rows {
		out[rows[i].DeviceID] = rows[i]
	}
	return out
}

// --- Rename hook ---

// hostnameAfterRename pushes the new name onto the equipment, AFTER the rename is
// already committed.
//
// THE RENAME NEVER FAILS BECAUSE THE DEVICE IS UNREACHABLE. That is provisionSNMP's
// promise as snmpheal.go's header states it, applied to the name: the inventory
// write is done and answered before this runs, the push happens in the background
// on its own context, and if it fails the healer catches up on its own cadence. An
// operator correcting a typo on a switch that is powered off must not be told the
// rename failed — it did not.
//
// AN UNNAMEABLE NAME IS REPORTED, NOT SWALLOWED. "Switch Zürich 1" is a perfectly
// good meshcraft name and cannot be a hostname; the rename stands, and the event
// plus the blocked pipeline say what could not be pushed and why. Reporting it
// with the REQUEST — actor = the operator who renamed — is deliberate: this one is
// their doing, unlike everything the healer emits as "meshcraft".
// RETURNS NOTHING, and that signature is the promise: there is no error for
// handleUpdateDevice to propagate, so no failure here can turn a committed rename
// into an HTTP error.
func (s *Server) hostnameAfterRename(r *http.Request, d model.Device) {
	var desired, action = hostnameRenameAction(d)
	switch action {
	case hostnameRenameReport:
		s.emitDeviceEvent(r, d, "device.hostname_unnameable", model.SevWarning,
			"\""+d.Name+"\" was saved as this device's meshcraft name, but it cannot be "+
				"pushed to the equipment as a system name: "+desired)
		s.recordPipeline(r.Context(), pipelineForUnnameable(d, desired), "")
	case hostnameRenamePush:
		s.kickHostnamePush(d, desired)
	}
}

// What a rename should do about the device's own name. Three outcomes, and
// SEPARATE FROM THE ACTING so the rule is testable without a database — the
// existing tests in this package run without PostgreSQL, so any decision that
// matters has to be a pure function they can call.
const (
	// hostnameRenamePush: push it, in the background.
	hostnameRenamePush = "push"
	// hostnameRenameReport: the new name cannot be a hostname. The rename STANDS
	// — it is already committed — and the refusal is reported instead of being
	// swallowed, because "Switch Zürich 1" is a perfectly good meshcraft name and
	// only the operator can decide what the equipment should be called instead.
	hostnameRenameReport = "report"
	// hostnameRenameSkip: nothing to do, silently. Both cases are PERMANENT facts
	// already carried where an operator looks — `hostname.state = unsupported` on
	// the device payload, and a missing management address is the device page's own
	// headline. An event per rename restating a fact that never changes is how a
	// fleet log becomes unreadable.
	hostnameRenameSkip = "skip"
)

// hostnameRenameAction decides what a rename owes the equipment. PURE.
//
// The first return value carries the NAME on `push` and the REFUSAL SENTENCE on
// `report` — the two are never both meaningful, and one string keeps the caller
// from having to check which of two it got.
//
// THE ORDER MATCHES hostnameConformityOf's, and it has to: an unwritable platform
// is decided BEFORE the name is judged, so renaming an Instant On to "Switch
// Zürich 1" stays silent rather than warning about a push that was never going to
// happen. Two precedences would put a warning in the log for a device whose own
// payload says the whole axis does not apply to it.
func hostnameRenameAction(d model.Device) (string, string) {
	var writable, _ = hostnameWriteSupport(d)
	if !writable {
		return "", hostnameRenameSkip
	}
	var desired, refusal = hostnameFromDeviceName(d.Name)
	if refusal != "" {
		return refusal, hostnameRenameReport
	}
	if d.MgmtIP == "" {
		return "", hostnameRenameSkip
	}
	return desired, hostnameRenamePush
}

// kickHostnamePush pushes in the BACKGROUND, on a fresh context.
//
// Fresh because the request's context dies with the response, and this write talks
// to hardware through a proxy (seconds). Same shape as KickSNMPHeal, including the
// cooldown reset: the operator just acted, and must not silently inherit the
// backoff of an earlier failure on this device.
func (s *Server) kickHostnamePush(d model.Device, desired string) {
	go func() {
		var ctx, cancel = context.WithTimeout(context.Background(),
			hostnameHealDeviceBudget+30*time.Second)
		defer cancel()
		s.hostnameHeal.succeeded(d.ID)
		s.pushAndVerifyHostname(ctx, d, desired, "", false, time.Now())
	}()
}

// --- Pipelines ---

// pipelineForHostname records the push where every other device write is
// reviewable afterwards. Kind "config": this IS device configuration, and a
// hostname push landing in a different bucket than the port writes would hide it
// from the one screen an operator opens after the fact.
func pipelineForHostname(d model.Device, push hostnamePush, repair bool) store.Pipeline {
	var p = store.Pipeline{
		SiteID: d.SiteID, Kind: "config", DeviceID: &d.ID,
		Steps: stepsFrom(push.Steps),
	}
	if push.Err != nil {
		p.State = store.PipelineFailed
		p.Label = "System name \"" + push.Name + "\" on \"" + d.Name + "\""
		p.BlockedReason = push.Err.Error()
		return p
	}
	p.State = store.PipelineDone
	if repair {
		p.Label = "System name repaired on \"" + d.Name + "\" — \"" + push.Name + "\""
		return p
	}
	p.Label = "System name set on \"" + d.Name + "\" — \"" + push.Name + "\""
	return p
}

// pipelineForUnnameable records a name that CANNOT be pushed.
//
// BLOCKED and not FAILED, the distinction pipelineForApply already draws: nothing
// failed and a retry would do exactly the same thing. It waits on an arbitration
// only the operator can make — rename the device to something a hostname can
// carry, or accept that this one's console banner keeps whatever it has.
func pipelineForUnnameable(d model.Device, refusal string) store.Pipeline {
	return store.Pipeline{
		SiteID: d.SiteID, Kind: "config", DeviceID: &d.ID,
		State:         store.PipelineBlocked,
		Label:         "System name not pushed on \"" + d.Name + "\"",
		BlockedReason: refusal,
	}
}
