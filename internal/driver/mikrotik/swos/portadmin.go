package swos

// Shutting a port and bringing it back — the `en` MASK of /link.b
// (driver/portadmin.go).
//
// # There is no per-port write on this platform, only a whole page
//
// SwOS has no command for one port. Administrative state is ONE BIT in ONE MASK
// (`en`, [FieldEnabled]) inside a page the device serves and expects back WHOLE:
// its own UI rebuilds every modifiable field of /link.b and POSTs the lot, and
// pages.go's writable-field list exists for exactly that reason. So shutting a
// single port here means reading eleven fields, changing one bit, and posting all
// eleven back.
//
// Which is why this file REFUSES rather than truncates. pages.go states the
// consequence in the entry for [PageLink] and it is the whole risk of this write:
// riding along with `en` are `nm` (the twenty-six port names), `an`
// (autonegotiation), `dpxc`/`fctc`/`fctr` (duplex and flow control) and `spdc`
// (the forced rate). Posting without `nm` would RENAME TWENTY-SIX PORTS TO NOTHING
// while shutting one. So when the page the device served is missing any field of
// the writable set, nothing is posted at all — the same refusal
// [Admin.ApplyPorts] makes on /fwd.b, for the same reason and with the same words.
//
// # THE BACKUP IS THE ONLY WAY BACK, so it is taken before the first write
//
// SwOS persists immediately (Capabilities.Commit is `immediate`): there is no
// running/startup split, no save step, and — unlike every CLI platform in this
// codebase — NO REBOOT THAT UNDOES THIS. cisco/ios can decline to save so that a
// reload brings a port back; here that recovery does not exist. `GET /backup.swb`
// taken just before posting is therefore the only rollback, exactly as in
// ApplyPorts, and it goes back to the caller in [driver.ApplyResult.Backup]. A
// backup that could not be taken does not block the write — blocking a gesture for
// a safety net would be excessive — but it is said, because the operator must know
// they are working without one.
//
// # The guardrail, and the zero that means the OPPOSITE of Instant On's
//
// A port carrying a LIVE LINK is not shut without [driver.PortAdminPlan.Force]
// when it may be the one administration arrives on. This model has neither SSH,
// nor Telnet, nor a serial port: the only way back from cutting administration is
// a reset button on site, which also loses the configuration.
//
// [State.MgmtVLAN] is `avln` — "Allow From VLAN" — and ZERO IS A LEGITIMATE STATE
// HERE, not ignorance: it means "administration accepted from ANY VLAN", which is
// the factory state of a CSS (driver/state.go says it explicitly, and stateOf
// notes it on every read). Refusing to write on that basis would make adopting a
// brand-new switch impossible. So protection FALLS BACK TO LINK STATE ALONE: every
// port with a live link is spared, which is stricter than the nominal case, not
// looser. The same fallback covers a firmware that does not serve `avln` at all.
//
// One extra protection, and it only ever ADDS: `allp` — "Allow From Ports" — is a
// mask of the ports administration is accepted from. On the reference capture it is
// 0x03ffffff, i.e. all twenty-six, and says nothing useful. On a HARDENED unit it
// names a handful, and then a linked port inside it is administration whatever its
// VLAN membership says. It is never read the other way round: a port's absence from
// `allp` does not make it safe to shut, because that mask may be stale in ways the
// VLAN table is not.
//
// # PoE power cycling is NOT implemented, and the missing piece is a READ
//
// /sys.b carries a `poe` field (it is in the writable set, echoed back untouched on
// every /sys.b write) and that is ALL this connector knows about PoE on this
// platform: one field of unverified meaning, no per-port class, no reader. There is
// nothing to read the class from and therefore nothing to restore, and
// [driver.PortPowerCycler] is exactly the interface that must not be answered
// without a proven restore. The prerequisite is a survey of that field on a
// PoE-capable SwOS model and a [driver.PoEReader].

import (
	"fmt"
	"sort"
	"strings"

	"context"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin shuts and un-shuts ports.
//
// [driver.PortPowerCycler] is DELIBERATELY ABSENT (cf. the header): there is no
// per-port PoE class to read on this platform, so nothing could be restored.
var _ driver.PortAdminApplier = (*Admin)(nil)

// PortAdminChange is what a run would change on ONE port. Nothing that does not
// move: on a platform that persists immediately and has no way back, a POST that
// changes nothing is a risk taken for nothing — and it would be taken on every
// reconciliation pass across the whole fleet.
type PortAdminChange struct {
	Port string `json:"port"`
	// Index is the port's SwOS rank, 0-based — the bit position in the mask.
	Index int `json:"index"`
	// Enabled is the wanted administrative state.
	Enabled bool `json:"enabled"`
}

// PortAdminInput gathers what [PlanPortAdmin] needs to decide.
//
// AN AGGREGATE AND NOT SIX PARAMETERS, same reason as [PortsInput]: the day this
// decision needs one more constraint the signature changes in a single place, and
// a positional parameter slipped into the middle of a list of six is exactly how
// `ports` ends up where the code expected `prefix` — on a platform that NUMBERS
// its ports, that is a write to the wrong port.
type PortAdminInput struct {
	// Link is /link.b as the device served it: `en` says what is enabled now,
	// `lnk` which ports carry a link.
	Link *Page
	// Sys is /sys.b, for `allp` — "Allow From Ports". Nil is tolerated: the
	// guardrail then rests on the VLAN membership and the link alone.
	Sys *Page
	// Facade is the geometry AND the naming, read from the device, never from the
	// caller (cf. Facade).
	Facade Facade
	// Current is the state that was read: VLAN membership and the management VLAN.
	Current State
	// Intents is what the caller asks, by port name — device naming ("Port7",
	// "SFP2") or the legacy numbered family, both accepted by Facade.Index.
	Intents []driver.PortAdminIntent
	// Force lifts the guardrail. An explicit operator gesture, never a default.
	Force bool
}

// PortAdminDecision is what a run would do, and what it refuses to do.
type PortAdminDecision struct {
	Changes []PortAdminChange
	// Skipped carries the refusals in sentences meant for the operator: a
	// safeguard that protects without saying so looks like a bug.
	Skipped []string
	// Notes carries facts with no decision attached — chiefly which fallback the
	// guardrail is running on.
	Notes []string
}

// PlanPortAdmin decides, WITHOUT WRITING ANYTHING, which bits of the `en` mask
// move and which ports are spared.
//
// PURE, and for the same reason [PlanPorts] is: this is the function that decides
// whether the jack carrying the session gets shut, on a model with neither SSH,
// Telnet nor a serial port. Taking it out of the network path is what allows
// testing it against the device's own captures instead of against live hardware.
//
// An error is returned only when the DECISION ITSELF is impossible — /link.b
// without its `en` or `lnk` mask. Everything else is a per-port refusal.
func PlanPortAdmin(in PortAdminInput) (PortAdminDecision, error) {
	var out PortAdminDecision
	if in.Link == nil || !in.Link.Has(FieldEnabled) {
		return out, fmt.Errorf("/link.b does not carry the mask %q: the administrative state "+
			"of the ports is unknown, and assuming it would either shut a port twice or "+
			"report a shut port as brought up", FieldEnabled)
	}
	if !in.Link.Has(FieldLink) {
		return out, fmt.Errorf("/link.b does not carry the mask %q: link state is unknown, and "+
			"supposing it empty would make meshcraft believe no port carries the current "+
			"session — the guardrail would protect nothing", FieldLink)
	}

	var mgmt = in.Current.MgmtVLAN
	if mgmt <= 0 {
		out.Notes = append(out.Notes, "no management VLAN to spare: the device accepts "+
			"administration from ANY VLAN (/sys.b \""+FieldAllowVLAN+"\" = 0, its factory "+
			"state) or this firmware does not serve that field. This is NOT ignorance the way "+
			"the same zero would be on an Instant On — protection falls back to LINK STATE "+
			"ALONE, so EVERY port carrying a link is spared, which is stricter than the "+
			"nominal case")
	}

	var intents = make([]driver.PortAdminIntent, len(in.Intents))
	copy(intents, in.Intents)
	sort.Slice(intents, func(i, j int) bool {
		var a, aok = in.Facade.Index(intents[i].Port)
		var b, bok = in.Facade.Index(intents[j].Port)
		if !aok || !bok {
			return intents[i].Port < intents[j].Port
		}
		return a < b
	})

	var it driver.PortAdminIntent
	for _, it = range intents {
		var idx, known = in.Facade.Index(it.Port)
		if !known {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: this device has no port by that name — it numbers %d port(s), "+
					"named \"%s\" … \"%s\"", it.Port, in.Facade.Ports(),
				in.Facade.Name(0), in.Facade.Name(in.Facade.Ports()-1)))
			continue
		}
		// The device's OWN name for the port, whatever family the caller used: the
		// operator reads this against the device's UI.
		var name = in.Facade.Name(idx)
		var enabled, okEn = in.Link.Bit(FieldEnabled, idx)
		var linked, okLnk = in.Link.Bit(FieldLink, idx)
		if !okEn || !okLnk {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: the /link.b masks are too short to carry a bit for port %d — "+
					"this firmware's page shape is not the one this connector reads", name, idx+1))
			continue
		}

		// THE GUARDRAIL. Disabling only — bringing a port back up cuts nothing.
		if !it.Enabled {
			var why = whyShutIsUnsafe(name, idx, linked, mgmt, in.Current, in.Sys, in.Facade)
			if why != "" && !in.Force {
				out.Skipped = append(out.Skipped, why)
				continue
			}
		}

		if enabled == it.Enabled {
			continue // already where the plan wants it: nothing is posted
		}
		out.Changes = append(out.Changes, PortAdminChange{Port: name, Index: idx, Enabled: it.Enabled})
	}
	return out, nil
}

// whyShutIsUnsafe returns the reason a port must not be shut, or the empty string.
//
// LINK-GATED FIRST, like every other guardrail in this codebase: a port with no
// cable cannot carry the session that would be cut, and refusing it anyway would
// stop an operator shutting an unused jack — the safest write there is on this
// device.
func whyShutIsUnsafe(name string, idx int, linked bool, mgmt int,
	current State, sys *Page, f Facade) string {

	if !linked {
		return ""
	}
	var tail = " This model has NEITHER SSH, NOR TELNET, NOR A SERIAL PORT, and SwOS " +
		"persists immediately: no reboot undoes this write. The only way back from cutting " +
		"administration is the reset button, on site, which also loses the configuration. " +
		"Force only knowing why"

	// `allp` — administration accepted only from certain ports. Consulted BEFORE the
	// VLAN test and only ever to ADD protection (cf. the file header): on a hardened
	// unit this is the sharpest signal there is, and on a default one it says nothing.
	if allowsOnlySomePorts(sys, f.Ports()) {
		var allowed, ok = sys.Bit(FieldAllowPorts, idx)
		if ok && allowed {
			return fmt.Sprintf("%s NOT shut: it carries an ACTIVE link and /sys.b \"%s\" "+
				"(\"Allow From Ports\") lists it among the FEW ports administration is accepted "+
				"from — whatever its VLAN membership, this is a door to this device.%s",
				name, FieldAllowPorts, tail)
		}
	}

	if mgmt <= 0 {
		return fmt.Sprintf("%s NOT shut: it carries an ACTIVE link and this device accepts "+
			"administration from ANY VLAN (/sys.b \"%s\" = 0, its factory state), so there is no "+
			"management VLAN to reason about and no way to tell which port carries the session. "+
			"Protection rests on the link alone here.%s", name, FieldAllowVLAN, tail)
	}
	var port, isKnown = current.Ports[name]
	if isKnown && portCarriesVLAN(port, mgmt) {
		return fmt.Sprintf("%s NOT shut: it carries an ACTIVE link and the MANAGEMENT VLAN %d "+
			"— as its native VLAN or in its membership — so this is very likely the port this "+
			"session arrives on.%s", name, mgmt, tail)
	}
	return ""
}

// allowsOnlySomePorts reports whether `allp` NARROWS administration to a subset of
// the faceplate.
//
// An all-ones mask (0x03ffffff on the twenty-six-port reference capture) is the
// default and means "from anywhere": treating that as "this port is a door" would
// spare every linked port on every device and turn the guardrail into a refusal to
// work.
func allowsOnlySomePorts(sys *Page, ports int) bool {
	if sys == nil || !sys.Has(FieldAllowPorts) || ports <= 0 {
		return false
	}
	var i int
	for i = 0; i < ports; i++ {
		var on, ok = sys.Bit(FieldAllowPorts, i)
		if !ok {
			return false
		}
		if !on {
			return true // at least one port is excluded: the mask says something
		}
	}
	return false
}

// portCarriesVLAN: is this VLAN the port's native one, or in its membership?
//
// Both are consulted because shutting a port cuts EVERY VLAN on it, not just the
// native one — a trunk merely tagged into the management VLAN is as much the
// session's path as an access port whose PVID is it, and it is usually the uplink.
func portCarriesVLAN(port PortState, vlan int) bool {
	if vlan <= 0 {
		return false
	}
	if port.PVID == vlan {
		return true
	}
	var v int
	for _, v = range port.Tagged {
		if v == vlan {
			return true
		}
	}
	return false
}

// ApplyPortAdmin shuts and un-shuts ports by rewriting /link.b.
//
// The order is the one this platform forces: read everything in ONE pass (each
// page costs two HTTP requests here and accesses are serialized), decide with a
// pure function, refuse if the page shape is not the one that can be safely posted
// back, take the backup, post ONCE for all the ports, then re-read to see what
// actually moved.
func (a *Admin) ApplyPortAdmin(ctx context.Context,
	plan driver.PortAdminPlan) (ApplyResult, error) {

	var out ApplyResult
	if len(plan.Ports) == 0 {
		return out, nil
	}

	// Never against a cached state, and in one pass: /link.b carries the masks this
	// write moves, /vlan.b and /fwd.b the membership the guardrail reads, /sys.b the
	// management VLAN and the "Allow From Ports" mask.
	var snap, err = a.readAll(ctx)
	if err != nil {
		return out, err
	}
	var current State
	current, err = a.stateOf(snap)
	if err != nil {
		return out, err
	}

	var decided PortAdminDecision
	decided, err = PlanPortAdmin(PortAdminInput{
		Link: snap.link, Sys: snap.sys, Facade: snap.facade,
		Current: current, Intents: plan.Ports, Force: plan.Force,
	})
	if err != nil {
		a.step(false, "administrative state not settable", err.Error())
		return out, err
	}
	out.Skipped = append(out.Skipped, decided.Skipped...)
	out.Notes = append(out.Notes, decided.Notes...)
	var s string
	for _, s = range decided.Skipped {
		a.step(false, "port spared by the safeguard", s)
	}
	if len(decided.Changes) == 0 {
		a.step(true, "nothing to write",
			"the ports already carry the requested administrative state — no page is posted, "+
				"which is the intended behaviour on a platform with no way back")
		return out, nil
	}

	// The page to post, BEFORE touching a bit: refuse a shape that cannot be sent
	// back whole. pages.go's PageLink entry names what rides along — posting without
	// "nm" would rename twenty-six ports to nothing while shutting one.
	var fields, known = WritableFields(PageLink)
	if !known {
		return out, fmt.Errorf("no writable field set is declared for %s: an unknown page "+
			"must not be written", PageLink)
	}
	var body, missing = snap.link.Only(fields)
	if len(missing) > 0 {
		var e = fmt.Errorf("%s: this firmware does not serve the fields %s the connector "+
			"expects — write refused, posting the page without them would let the device "+
			"zero them (\"%s\" alone carries the twenty-six port names)",
			PageLink, strings.Join(missing, ", "), FieldPortName)
		a.step(false, "unexpected /link.b field set", e.Error())
		return out, e
	}

	var ch PortAdminChange
	for _, ch = range decided.Changes {
		if !body.SetBit(FieldEnabled, ch.Index, ch.Enabled) {
			var e = fmt.Errorf("%s: the mask %q is not modifiable (unexpected shape) — "+
				"nothing is posted", PageLink, FieldEnabled)
			a.step(false, "administrative mask not settable", e.Error())
			return out, e
		}
	}

	// The backup, right before the first write and only because there IS something to
	// write. On this platform it is the ONLY possible way back: the configuration is
	// persistent by default and no power cut undoes it. Its failure does not block the
	// write, but it is said.
	out.Backup, err = a.Backup(ctx)
	if err != nil {
		a.step(false, "NO backup available before write",
			"the run continues, but with no way back: "+err.Error())
	}

	var names []string
	for _, ch = range decided.Changes {
		names = append(names, ch.Port)
	}
	if err = a.postRaw(ctx, PageLink, body.Encode(),
		fmt.Sprintf("administrative state written on %d port(s): %s",
			len(names), strings.Join(names, ", "))); err != nil {
		return out, err
	}

	// The re-read is what says the ports moved — never the HTTP status. One page, one
	// more pair of requests, and it is the only thing that distinguishes a write the
	// device applied from one it accepted and ignored.
	var after *Page
	after, err = a.Get(ctx, PageLink)
	if err != nil {
		out.Unconfirmed("administrative state", names,
			"the confirming re-read of "+PageLink+" failed: "+err.Error())
		return out, nil
	}
	for _, ch = range decided.Changes {
		var now, ok = after.Bit(FieldEnabled, ch.Index)
		if ok && now == ch.Enabled {
			out.PortsWritten = append(out.PortsWritten, ch.Port)
			continue
		}
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"%s: the device accepted the page and bit %d of \"%s\" still reads %v — nothing on "+
				"this port changed", ch.Port, ch.Index, FieldEnabled, now))
	}

	a.step(true, "write complete",
		"SwOS persists immediately: there is no save step, and no reboot that undoes this — "+
			"the configuration copy taken just before the write is the only way back")
	out.Notes = append(out.Notes, "this took effect immediately AND is already persistent "+
		"(SwOS has no running/startup split): unlike on the CLI platforms, a reboot does NOT "+
		"bring these ports back up. The configuration backup returned with this result is the "+
		"way back")
	return out, nil
}
