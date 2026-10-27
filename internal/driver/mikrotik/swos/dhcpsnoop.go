package swos

import (
	"context"
	"fmt"
	"sort"

	"meshcraft/internal/driver"
)

// DHCP snooping on SwOS, and the shape of the feature here is genuinely
// different from every CLI platform in the fleet — different enough that
// implementing the shared contract faithfully means REFUSING half of it.
//
// # There is ONE level, not three
//
// The contract (internal/driver/dhcpsnoop.go) describes three levels: a
// device-wide enable, a per-VLAN enable, and a per-port trust flag. SwOS has
// only the third. What exists is `dtrp` on /sys.b — a MASK of trusted ports, one
// bit per port, exactly like `en` on /link.b — and nothing else:
//
//   - NO global on/off statement. The set of trusted ports IS the configuration;
//   - NO per-VLAN dimension. Snooping is not scoped to VLANs anywhere in the UI.
//
// This is an ENUMERATION, not a failed search, and that is what makes the
// absence reportable rather than merely unobserved: the device's own
// configuration backup (testdata/backup.swb, CSS326-24G-2S+, SwOS 2.17) lists
// every field /sys.b stores, and neither a snooping enable nor a VLAN list is
// among them. mgmtaddr.go leans on the same source to establish that SwOS has no
// gateway field at all — same method, same standing.
//
// # The missing per-VLAN dimension is NOT a shortfall (settled 2026-08-08)
//
// It reads like one against a three-level contract, and this file used to carry
// it as something to apologise for. The product decision says otherwise: DHCP
// snooping is a PER-PORT gesture. What an operator does is protect an access
// port toward a client, or trust the uplink toward the DHCP server — and the
// per-VLAN enable exists on other platforms as the plumbing that makes the
// per-port flag take effect there, not as a thing anybody sets for its own sake.
// The API layer now derives that VLAN list from the ports a gesture names rather
// than asking a caller to supply one (api/dhcpsnoop_apply.go, dhcpSnoopVLANsFor).
//
// Read that way, SwOS's single `dtrp` mask expresses the whole gesture directly,
// with no plumbing to arrange. This range is not behind the others here; it is
// the one where the model happens to match what is being offered.
//
// The `dtrp` WRITE HAS NOW BEEN SENT — 2026-08-09, on the lab CSS326 against
// Port2 — and [Driver.WritesExtra] declares "dhcp_snooping". capabilities.go
// holds the evidence; live_stormcontrol_test.go holds the exercise.
//
// It is the most dangerous write this connector makes, and that is worth
// carrying here rather than only next to the declaration: /sys.b is posted WHOLE
// and also carries `ip`, `avln` ("Allow From VLAN"), `allp` ("Allow From
// Ports") and `mgmt`. A field dropped from the body is a field the device
// zeroes, this model has neither SSH nor Telnet nor a serial port, and the way
// back from a zeroed allow-list is the reset button on site. Hence the refusal
// below when the served field set does not match [WritableFields], and hence the
// exercise asserting those four fields BY NAME rather than trusting a diff.
//
// The consequence for [driver.DHCPSnoopPlan.VLANs] is the one thing this file
// must not get wrong. A caller hands us the site's VLANs; we cannot honour them;
// and the honest answer is to SAY so in [driver.ApplyResult.Notes] and
// [driver.DHCPSnoopState.Notes] rather than return an empty VLAN list. Those are
// two different facts — "this platform has no per-VLAN dimension" and
// "snooping covers no VLAN yet" — and a screen that conflated them would tell
// an operator to go fix a setting that does not exist.
//
// Symmetrically, [driver.DHCPSnoopState.Enabled] is reported TRUE whenever the
// platform carries the feature at all. That is not optimism: on a platform with
// no global switch, there is nothing that could be off, and reporting false
// would make every UI print "the device-wide feature is off" next to a trust
// flag that is in fact fully in effect — the precise inversion of the failure
// the contract's three-level model exists to prevent.
//
// # Option 82 is adjacent, and deliberately untouched
//
// /sys.b's "DHCP & PPPoE Snooping" group also carries `ainf`, the information
// option (option 82). It rides in the writable set because a read-modify-rewrite
// must echo it back untouched, and for no other reason — SwOS does not route,
// therefore does not relay, therefore has nothing to insert option 82 into (cf.
// the "DHCPRelay: false" capability in swos.go). Nothing here sets it.
//
// # The write is read-modify-rewrite, and a partial page is REFUSED
//
// Same rule and same reason as writeSysMgmt: /sys.b is posted whole, so every
// field in [WritableFields] must come back from the read or the device would
// zero the ones we omitted. A firmware serving an unexpected field set gets a
// refusal, not a best-effort write — on a platform whose capability model is
// `immediate`, a write that clears a field cannot be taken back.

// Compile-time: this Admin both reads and writes DHCP snooping trust.
var (
	_ driver.DHCPSnoopReader  = (*Admin)(nil)
	_ driver.DHCPSnoopApplier = (*Admin)(nil)
)

// noVLANDimension is the sentence returned to the caller whenever VLANs enter
// the picture. Written once, used on both the read and the write path, because
// the fact does not change between them and two phrasings of one platform truth
// would read as two different findings.
const noVLANDimension = "SwOS has no per-VLAN DHCP snooping dimension and no " +
	"device-wide enable: the set of trusted ports (/sys.b `dtrp`) is the whole " +
	"configuration. The field set of /sys.b is enumerated exhaustively by the " +
	"device's own configuration backup, so this is an absence, not an unread value."

// ReadDHCPSnooping reports the trust mask as the contract's port list.
//
// Every port of the faceplate is reported, and that is deliberate: `dtrp` is a
// mask, so a port's bit is READ whether it is set or not — there is no such
// thing here as a port whose flag could not be determined. That makes this one
// of the rare platforms where "absent from Ports" never happens, and the
// distinction the contract preserves (untrusted versus not read) simply has no
// ambiguous case to carry.
func (a *Admin) ReadDHCPSnooping(ctx context.Context) (driver.DHCPSnoopState, error) {
	var out driver.DHCPSnoopState
	var sys, err = a.Get(ctx, PageSys)
	if err != nil {
		return out, err
	}
	if !sys.Has(FieldDHCPTrusted) {
		// The firmware does not serve the field at all. Reporting "nothing
		// trusted" would be a fabrication: an absent field is an unknown, and
		// this is the one case where this platform CAN be ignorant.
		out.Notes = append(out.Notes, fmt.Sprintf(
			"%s does not carry \"%s\" on this firmware: DHCP snooping trust "+
				"could not be read, and is not reported as untrusted",
			PageSys, FieldDHCPTrusted))
		a.step(false, "DHCP snooping read", "no "+FieldDHCPTrusted+" field on "+PageSys)
		return out, nil
	}

	// TRUE, not false: with no global switch on this platform there is nothing
	// that could be off (cf. the file header).
	out.Enabled = true
	out.Notes = append(out.Notes, noVLANDimension)

	var names = a.portNames(ctx)
	var facade = a.facadeFor(ctx)
	var name string
	var trustedCount int
	for _, name = range names {
		var idx, ok = facade.Index(name)
		if !ok {
			continue
		}
		var trusted, readOK = sys.Bit(FieldDHCPTrusted, idx)
		if !readOK {
			continue
		}
		if trusted {
			trustedCount++
		}
		out.Ports = append(out.Ports, driver.DHCPSnoopPortState{
			Port: name, Trusted: trusted,
		})
	}

	a.step(true, "DHCP snooping read", fmt.Sprintf(
		"%d port(s), %d trusted", len(out.Ports), trustedCount))
	return out, nil
}

// ApplyDHCPSnooping sets the trust bits named in the plan and nothing else.
//
// NEVER TURNS THE FEATURE OFF, per the contract — and here that guarantee is
// structural rather than a matter of discipline: there is no switch to turn off.
// A port asked to be untrusted has its bit cleared, which is the protected
// state, not a disabling of anything.
//
// Ports absent from the plan keep their bit, because the mask is read before it
// is painted. That is the contract's "a port absent is NOT MANAGED" rule
// getting satisfied by construction.
func (a *Admin) ApplyDHCPSnooping(ctx context.Context,
	plan driver.DHCPSnoopPlan) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if len(plan.Ports) == 0 {
		return out, nil
	}
	out.Notes = append(out.Notes, noVLANDimension)
	if len(plan.VLANs) > 0 {
		// The caller asked for VLAN coverage we cannot install. Saying which
		// VLANs were ignored matters more than saying that some were: an
		// operator comparing this against a CLI platform's result needs to see
		// the same list they sent.
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"VLAN scoping of DHCP snooping (%s): not settable on SwOS — the "+
				"trust mask applies to all traffic on the port", intsList(plan.VLANs)))
	}

	var sys, err = a.Get(ctx, PageSys)
	if err != nil {
		return out, err
	}
	if !sys.Has(FieldDHCPTrusted) {
		var e = fmt.Errorf("%s does not carry \"%s\" on this firmware: "+
			"DHCP snooping trust cannot be written", PageSys, FieldDHCPTrusted)
		a.step(false, "DHCP snooping write refused", e.Error())
		return out, e
	}

	// The backup is taken BEFORE the first write, and it is the only way back on
	// this platform: SwOS applies and persists in one gesture, so a power outage
	// undoes nothing (cf. driver.ApplyResult.Backup). Its failure does NOT block
	// the write — refusing a trust change for want of a safety net would be
	// excessive — but it is logged, because the operator must know they are
	// working without one. Same handling as apply.go's.
	var backupErr error
	out.Backup, backupErr = a.Backup(ctx)
	if backupErr != nil {
		a.step(false, "NO backup available before write",
			"the run continues, but with no possible way back: "+backupErr.Error())
	}

	var facade = a.facadeFor(ctx)
	var wrote []snoopWrite
	var changed bool
	var intent driver.DHCPSnoopPortIntent
	for _, intent = range plan.Ports {
		var idx, ok = facade.Index(intent.Port)
		if !ok {
			// A name the faceplate does not carry is REFUSED, never guessed: an
			// invented index writes to the wrong port, and on this platform that
			// is the one that cuts the uplink.
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: unknown port on this device, trust flag not written", intent.Port))
			continue
		}
		var current, readOK = sys.Bit(FieldDHCPTrusted, idx)
		if readOK && current == intent.Trusted {
			// Already conformant: sending nothing beats a round trip that
			// changes nothing (cf. ApplyResult.PortsWritten).
			continue
		}
		if !sys.SetBit(FieldDHCPTrusted, idx, intent.Trusted) {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: trust bit %d not settable on the served %s page",
				intent.Port, idx, PageSys))
			continue
		}
		changed = true
		wrote = append(wrote, snoopWrite{Name: intent.Port, Idx: idx, Trusted: intent.Trusted})
	}
	if !changed {
		a.step(true, "DHCP snooping already conformant", "no bit to change")
		return out, nil
	}

	var fields, known = WritableFields(PageSys)
	if !known {
		var e = fmt.Errorf("writable field set for %s unknown: write refused", PageSys)
		a.step(false, "DHCP snooping write refused", e.Error())
		return out, e
	}
	var body *Page
	var missing []string
	body, missing = sys.Only(fields)
	if len(missing) > 0 {
		// Identical refusal to writeSysMgmt's, and for the identical reason:
		// posting /sys.b without a field the connector expects lets the device
		// zero it. On a `dtrp` write that would silently clear the management
		// allow-list next to it.
		var e = fmt.Errorf("%s: firmware does not serve fields %v expected by the "+
			"connector — write refused; posting without them would let the device "+
			"zero them", PageSys, missing)
		a.step(false, "unexpected /sys.b field set", e.Error())
		return out, e
	}
	if err = a.postRaw(ctx, PageSys, body.Encode(),
		"DHCP snooping trust written on "+PageSys); err != nil {
		return out, err
	}

	// Verified by RE-READING, not by the POST's return: a 2xx that changed
	// nothing is the failure mode worth catching, and on a platform whose whole
	// UI is a handful of pages the re-read costs one request.
	var after driver.DHCPSnoopState
	after, err = a.ReadDHCPSnooping(ctx)
	if err != nil {
		// The POST went out and this platform persists in the same gesture, so the
		// bits are very probably set — but "probably" is not what PortsWritten
		// says (cf. driver.ApplyResult.PortsWritten). The names ride in the note.
		out.Unconfirmed("DHCP snooping trust", snoopWriteNames(wrote), err.Error())
		return out, nil
	}
	var confirmed, disagreed, unseen = verifySnoopWrites(after, facade, wrote)
	out.Skipped = append(out.Skipped, disagreed...)
	out.Notes = append(out.Notes, unseen...)
	out.PortsWritten = confirmed
	a.step(true, "DHCP snooping written", fmt.Sprintf(
		"%d port(s) confirmed", len(confirmed)))
	return out, nil
}

// snoopWrite is one port whose trust bit the applier actually changed: the name
// the CALLER used, the faceplate index it resolved to, and what was asked for.
type snoopWrite struct {
	Name    string
	Idx     int
	Trusted bool
}

func snoopWriteNames(ws []snoopWrite) []string {
	var out = make([]string, 0, len(ws))
	var w snoopWrite
	for _, w = range ws {
		out = append(out, w.Name)
	}
	return out
}

// verifySnoopWrites decides which trust flags read back as asked.
//
// MATCHED BY FACEPLATE INDEX, for the reason spelled out at length beside
// [verifyStormWrites]: [Facade.Index] accepts both "GE2" and "Port2" while
// [Facade.Name] only ever emits the second, so comparing the caller's spelling
// against the device's makes a legacy-family plan confirm nothing. This applier
// had the identical defect — found by reading, on 2026-08-08, after the storm
// exercise on the lab CSS326 exposed it there — and it is worse here: DHCP
// snooping trust is what stops a rogue server, so reporting an applied trust flag
// as unconfirmed sends someone to re-apply a bit that is already correct.
//
// The three return values are three different claims and must not be merged:
// `confirmed` read back as asked, `disagreed` read back differently (the device
// declined), and `unseen` did not appear in the re-read at all — which says
// nothing about the write and everything about the read.
func verifySnoopWrites(after driver.DHCPSnoopState, facade Facade,
	wrote []snoopWrite) (confirmed, disagreed, unseen []string) {

	var observedBy = map[int]driver.DHCPSnoopPortState{}
	var seen = map[int]bool{}
	var i int
	for i = range after.Ports {
		var idx, ok = facade.Index(after.Ports[i].Port)
		if !ok {
			continue
		}
		observedBy[idx] = after.Ports[i]
		seen[idx] = true
	}
	var w snoopWrite
	for _, w = range wrote {
		if !seen[w.Idx] {
			unseen = append(unseen, fmt.Sprintf(
				"%s: written, but absent from the re-read — not confirmed", w.Name))
			continue
		}
		if observedBy[w.Idx].Trusted == w.Trusted {
			confirmed = append(confirmed, w.Name)
			continue
		}
		disagreed = append(disagreed, fmt.Sprintf(
			"%s: trust still %v after the write — the device did not take it",
			w.Name, observedBy[w.Idx].Trusted))
	}
	sort.Strings(confirmed)
	return confirmed, disagreed, unseen
}

// facadeFor rebuilds the port geometry from /link.b, with the same fallback
// portNames uses. Two calls on the same session read the same page twice, which
// is the price of not caching geometry on a platform that has no session to
// cache it in (cf. admin.go: "There is NO session").
func (a *Admin) facadeFor(ctx context.Context) Facade {
	var link, err = a.Get(ctx, PageLink)
	var ports, sfp, sfpFirst int
	if err == nil {
		ports, sfp, sfpFirst, err = geometryOf(link)
	}
	if err != nil || ports <= 0 {
		ports = 26
		sfp, sfpFirst = 0, ports
	}
	return NewFacade(ports, sfp, sfpFirst, a.portPrefix())
}

// intsList renders a VLAN list for an operator-facing sentence: "10, 20, 30".
// Sorted, so the same request twice produces the same sentence.
func intsList(ids []int) string {
	var sorted = append([]int{}, ids...)
	sort.Ints(sorted)
	var out = ""
	var v int
	for _, v = range sorted {
		if out != "" {
			out += ", "
		}
		out += fmt.Sprintf("%d", v)
	}
	return out
}
