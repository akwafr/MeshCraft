package instanton

// Shutting a port and bringing it back, on the range where that is the most
// dangerous write in the product (driver/portadmin.go).
//
// # Why this file is written differently from every other connector's
//
// This product line has NO SSH, NO Telnet and NO serial port. apply.go states the
// consequence in its header and it applies here twice over: shutting the jack that
// carries the administration session ends the conversation for good, and the only
// recovery is a paperclip in the reset button, ON SITE — which also means losing
// the whole configuration, not just the port. There is no console to plug into and
// no second path to try.
//
// So this file does not guess. Two rules, and they are what the rest of it
// implements:
//
//  1. THE FIELD NAME IS NEVER INVENTED. wcd's characteristic failure is a
//     `statusCode 0` on a request it did not understand — apply.go was caught by
//     it once already (`VLANInterfaceMembershipTable` accepted a write and changed
//     nothing), and admin.go was caught by the sibling case (a section named
//     `UpdatePassword` that does not exist, answering `statusCode -1`). A guessed
//     field would therefore report a port as shut while it goes on forwarding, or
//     write into a NEIGHBOURING field that does exist. This applier reads the
//     device's own `Standard802_3List` response first and only uses an
//     administrative field the device ITSELF printed. When it recognizes none, it
//     writes NOTHING and says so.
//  2. THE VALUE VOCABULARY IS DERIVED FROM THE DEVICE, not from a manual. This
//     range spells booleans as 1/2 and not 1/0 — `linkState` is 1 up and 2 down
//     (state.go), `adminStatus` is 1 for an interface that is up (mgmtwrite.go),
//     `filterEnabled` is 2 for "no filter" (snmp.go). So "enabled" is read off a
//     port that CURRENTLY HAS A LINK: a live link cannot exist on an
//     administratively shut port, which makes that port's own value the definition
//     of enabled, observed rather than assumed. "Disabled" is then the other member
//     of the 1/2 pair — and if the observed value is outside that pair, this
//     connector refuses instead of inverting a vocabulary it has never seen.
//
// # NOTHING HERE HAS BEEN SENT TO A 1930, and the REFUSAL is what is declared
//
// [WritableExtras] declares "port_admin" since 2026-08-06, so the API layer offers
// the gesture (api/portadmin_apply.go gates on that name). What is being declared
// is not a proven write — it is a probe that either finds its field or refuses
// legibly.
//
// The SECTION is captured: this connector has been reading `Standard802_3List` for
// link state since it was written, on sw-labo-1930 (JL685A, firmware 3.4.0.0).
//
// THE FIELD IS CAPTURED TOO, SINCE 2026-08-11, and the old sentence here — "no
// capture in this package shows an administrative field in that section" — is no
// longer true. A raw GET of `wcd?{Standard802_3List}` on the lab 1930 serves
// `<adminState>1</adminState>` on EVERY port, alongside `interfaceName`,
// `linkState`, `linkStateTrapEnabled`, `mediaType` and `speedAdmin`. The values
// are this framework's own `_TRUE='1'` / `_FALSE='2'`, and the local UI writes
// them back as `{Standard802_3List:{set:[{Entry:{interfaceName, adminState, …}}]}}`
// — the same section, the same `<Entry>` wrapper and the same numeric
// interfaceName [portAdminEntry] builds. [adminFieldCandidates] leads with
// `adminState` for that reason.
//
// WHAT IS STILL UNPROVEN IS THE WRITE. No port on a 1930 has ever been shut by
// this applier, so what the section publishes is settled and what the device does
// when it is POSTed is not.
//
// SO ON A FIRMWARE SERVING NO KNOWN ADMINISTRATIVE FIELD, THIS GESTURE STILL
// REFUSES, and the refusal names the candidates it looked for
// ([adminFieldCandidates], carried verbatim into [portAdminVocabulary.Why] and
// from there into every port's Skipped line). That machinery stays: one firmware
// serving `adminState` is not every firmware serving it, and an operator who
// tries the button on a release this connector does not recognize gets back the
// exact list of names that were looked for and did not appear.
//
// # NOT SAVED, and here that is a safety net rather than a caveat
//
// Every other write in this connector ends with [Admin.SaveConfig], because
// nothing is in flash until it does. This one deliberately does not, and on THIS
// range that is the strongest recovery available: a shut port that was never
// written to flash comes back on the next power cycle of the switch. On a device
// whose only other recourse is a factory reset, "unplug it and plug it back in"
// is worth more than any note — so [driver.ApplyResult.Notes] says it explicitly.
//
// # PoE power cycling is NOT implemented, and the missing piece is a READ
//
// Part of this range is PoE-capable, and this connector reads no PoE at all: no
// [driver.PoEReader], no PoE section anywhere in the package. [driver.PortPowerCycler]
// must put back the exact class a port was configured with, and cutting the volts
// with nothing to restore is an outage rather than a reboot. So this connector must
// not answer [driver.AsPortPowerCycler].

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin shuts and un-shuts ports.
//
// [driver.PortPowerCycler] is DELIBERATELY ABSENT (cf. the header): no PoE read
// exists in this package, so the restore half cannot be promised.
var _ driver.PortAdminApplier = (*Admin)(nil)

// sectionPorts is the per-port section this connector already reads for link
// state. It is the ONLY per-port section proven to exist on this range, which is
// why the administrative field is looked for HERE and nowhere else: asking for a
// section that does not exist answers `statusCode -1` and teaches nothing (cf.
// admin.go's sectionAdminUsers note).
const sectionPorts = "Standard802_3List"

// adminFieldCandidates are the field names this connector will RECOGNIZE in the
// device's own response, in order of preference. None is ever SENT unless the
// device printed it first (cf. the header, rule 1).
//
// `adminState` LEADS BECAUSE IT IS THE CAPTURED NAME — the lab 1930 serves it on
// every port of `Standard802_3List` (2026-08-11) and the product's own UI writes
// it back. It used to sit LAST, behind `adminStatus`, which was reasoned from
// mgmtwrite.go sending `<adminStatus>1</adminStatus>` on an IP interface that is
// up — a good guess about a framework reusing its vocabulary, and the wrong one.
// Same discipline as dhcpsnoop.go's section list: the captured name goes first,
// the guesses are KEPT because one firmware answering is not every firmware
// answering, and a wrong guess costs nothing here since only a name the device
// itself printed is ever used.
//
// "linkStateTrapEnabled" IS DELIBERATELY NOT IN THIS LIST, even though it
// contains the word "Enabled" and appears in this very section (snmp.go documents
// reading it: all 68 interfaces carry 1). It controls whether the port REPORTS its
// link changes by trap. Writing 2 into it in the belief that it shuts a port would
// leave the port forwarding AND silence the monitoring that would have shown the
// operator it had not moved — a worse outcome than doing nothing, which is why it
// is excluded by name rather than filtered out by a pattern that might not catch
// the next such field.
var adminFieldCandidates = []string{
	"adminState",
	"adminStatus",
	"portAdminStatus",
	"portEnabled",
	"portEnable",
}

// portAdminVocabulary is what one read of [sectionPorts] establishes: which field
// carries administrative state, and which of its values means enabled.
type portAdminVocabulary struct {
	// Field is the name the DEVICE printed. Empty means none was recognized, and
	// therefore that nothing may be written.
	Field string
	// Enabled / Disabled are the two values, in the device's own spelling.
	Enabled  string
	Disabled string
	// Admin is each port's current value of Field, keyed by standard port name.
	Admin map[string]string
	// LinkUp is each port's link state from the same read — the same source and
	// the same instant as Admin, so the guardrail and the "already done" test can
	// never disagree about a port.
	LinkUp map[string]bool
	// Why explains an empty Field: it is what the operator is shown, and what the
	// next capture has to answer.
	Why string
}

// readPortAdminVocabulary reads [sectionPorts] and derives everything this applier
// is allowed to assume — nothing more.
//
// Split out and pure over `body` so the derivation can be exercised against
// constructed fixtures: the whole safety of this file is in what it refuses, and a
// refusal that is not tested is a refusal that quietly stops happening.
func parsePortAdminVocabulary(body, prefix string) portAdminVocabulary {
	var v = portAdminVocabulary{Admin: map[string]string{}, LinkUp: map[string]bool{}}

	// 1. Which field does the device actually print? The first candidate that
	// appears in ANY entry wins, and a section that prints none leaves Field empty.
	var block string
	var blocks = reEntryBlock.FindAllString(body, -1)
	var cand string
	for _, cand = range adminFieldCandidates {
		for _, block = range blocks {
			if xmlField(block, cand) != "" {
				v.Field = cand
				break
			}
		}
		if v.Field != "" {
			break
		}
	}
	if v.Field == "" {
		v.Why = "no administrative field was found in the device's own \"" + sectionPorts +
			"\" response (looked for: " + strings.Join(adminFieldCandidates, ", ") +
			"): this connector will not invent a field name on a range whose reset button is " +
			"the only way back"
		return v
	}

	// 2. Per-port values, from the one read.
	var linkedValue string
	for _, block = range blocks {
		var iface = xmlField(block, "interfaceName")
		var _, err = strconv.Atoi(iface)
		if err != nil {
			continue // an aggregate (TRK…), not a physical port
		}
		var name = prefix + iface
		var val = xmlField(block, v.Field)
		if val == "" {
			continue
		}
		v.Admin[name] = val
		if xmlField(block, "linkState") == linkStateUp {
			v.LinkUp[name] = true
			// A LIVE LINK cannot exist on an administratively shut port, so this
			// port's value IS the enabled one. Observed, not assumed.
			if linkedValue == "" {
				linkedValue = val
			}
		}
	}

	// 3. The value vocabulary, derived from that observation.
	if linkedValue == "" {
		v.Field = ""
		v.Why = "the device reports \"" + sectionPorts + "\" but NO port with a link, so the " +
			"value that means \"administratively enabled\" cannot be derived from an " +
			"observation — and guessing it is how a port gets shut on a range with no console"
		return v
	}
	switch linkedValue {
	case "1":
		v.Enabled, v.Disabled = "1", "2"
	case "2":
		v.Enabled, v.Disabled = "2", "1"
	default:
		v.Why = "a port with a link reports " + v.Field + " = " + linkedValue + ", outside the " +
			"1/2 pair this range uses for every other boolean (linkState, adminStatus, " +
			"filterEnabled): the opposite value cannot be inferred, so nothing is written"
		v.Field = ""
		return v
	}
	return v
}

// portAdminEntry is the write body for ONE port.
//
// Deliberately MINIMAL — the interface name and the one field being changed. This
// range "rejects partial sections far more often than it accepts them"
// ([Admin.SetManagementVLAN]'s comment), so a refusal here is a plausible outcome
// and it is reported as one; the alternative, echoing back a whole entry read a
// moment earlier, would risk re-asserting a neighbouring field with a stale value
// on a section this connector has never written.
func portAdminEntry(num, field, value string) string {
	return "<Entry><interfaceName>" + num + "</interfaceName>" +
		"<" + field + ">" + value + "</" + field + "></Entry>"
}

// portAdminGuardBlocks decides whether a port may be shut.
//
// STRICTER THAN [Admin.ApplyPorts]'s guardrail, on purpose. That one protects a
// port whose NATIVE VLAN is a management VLAN, because that is all a membership
// change can break. Shutting a port breaks EVERY VLAN on it, so a port merely
// TAGGED in the management VLAN — an uplink — is protected here too. The narrower
// test would have waved the uplink through.
//
// mgmt <= 0 means IGNORANCE on this range, never permission: driver/state.go says
// it in as many words ("On Instant On, zero means ignorance and writes are
// refused"), and ApplyPorts refuses to write at all in that case. This applier
// spares every LINKED port instead of refusing outright, because the same refusal
// would also block bringing a port back UP — which cuts nothing, and is the
// gesture an operator needs precisely when something has already gone wrong.
//
// AN EMPTY MEMBERSHIP IS THE SECOND FORM OF THAT ZERO, and it is the one a zero
// value hides: knowing the management VLAN proves nothing if the read that says
// which ports carry it came back empty. Both are handled below, and the refusal
// says which one fired.
func portAdminGuardBlocks(have PortState, mgmt int, linked, force bool) (block bool, reason string) {
	if force || !linked {
		return false, ""
	}
	if mgmt <= 0 {
		return true, "it carries an ACTIVE link and the device did not report a management " +
			"VLAN, so meshcraft cannot rule out that this is the port the session is riding"
	}
	// A KNOWN management VLAN is not enough: the membership that decides whether
	// THIS port carries it comes from a different section of the device
	// (VLANInterfaceMembershipTable, read by ReadState) than the link state and the
	// administrative field (Standard802_3List, read here), and wcd answers an empty
	// 200 on a request it did not understand — apply.go's header records exactly
	// that failure on exactly that section. An empty membership answer leaves this
	// port's PortState at its zero value, and a zero value read as "in no VLAN, so
	// not in the management one" would wave the uplink through while the notes
	// claimed the management VLAN was known.
	//
	// On this range that reading is never right: every physical port is untagged in
	// some VLAN — VLAN 1 at worst, which is the factory state — so a port the
	// membership table places NOWHERE was not covered by the read. Ignorance again,
	// and the same answer.
	if have.PVID <= 0 && len(have.Tagged) == 0 {
		return true, "it carries an ACTIVE link and the device's membership table places it in " +
			"NO VLAN at all — every port on this range is untagged somewhere (VLAN 1 at worst), " +
			"so that is a membership read this port was missing from, not a port outside every " +
			"VLAN. The test that would clear it cannot be run"
	}
	if have.PVID == mgmt {
		return true, fmt.Sprintf("it carries an ACTIVE link and is an UNTAGGED member of the "+
			"management VLAN %d", mgmt)
	}
	var id int
	for _, id = range have.Tagged {
		if id == mgmt {
			return true, fmt.Sprintf("it carries an ACTIVE link and is a TAGGED member of the "+
				"management VLAN %d — shutting a port cuts every VLAN on it, not just its "+
				"native one, so an uplink is as much the session's path as an access port",
				mgmt)
		}
	}
	return false, ""
}

// ApplyPortAdmin shuts and un-shuts ports, NOW, and deliberately does not save.
//
// The order is: read the membership and the management VLAN (ReadState), read the
// port section RAW and derive what may be assumed from it, refuse everything if
// that derivation failed, then one write per port, then ONE re-read that decides
// what actually moved. A `statusCode 0` is never taken as proof on this range.
func (a *Admin) ApplyPortAdmin(ctx context.Context,
	plan driver.PortAdminPlan) (ApplyResult, error) {

	var out = ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}
	if len(plan.Ports) == 0 {
		return out, nil
	}
	var prefix = a.portPrefix()

	// Never diff against cached state: the device comes first.
	var current, err = a.ReadState(ctx)
	if err != nil {
		return out, fmt.Errorf("pre-write read failed: %w — the guardrail cannot tell which port "+
			"carries administration, so nothing is shut", err)
	}

	var body string
	body, err = a.RawSection(ctx, sectionPorts)
	if err != nil {
		return out, fmt.Errorf("reading \"%s\": %w — the administrative state of a port cannot "+
			"be read, so nothing is written", sectionPorts, err)
	}
	var vocab = parsePortAdminVocabulary(body, prefix)
	if vocab.Field == "" {
		// NOT a Go error: the plan was understood, the device answered, and
		// meshcraft is declining. That is exactly what Skipped is for, one line per
		// port so the operator sees which gesture did not happen.
		var it driver.PortAdminIntent
		for _, it = range plan.Ports {
			out.Skipped = append(out.Skipped, it.Port+" not touched: "+vocab.Why)
		}
		a.step(false, "port administrative state not writable on this device", vocab.Why)
		return out, nil
	}

	if current.MgmtVLAN <= 0 {
		out.Notes = append(out.Notes, "the device did not report a management VLAN, so meshcraft "+
			"cannot tell which port the session arrives on: every port with a LINK is spared "+
			"unless the plan forces it")
	}

	var intents = make([]driver.PortAdminIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(i, j int) bool {
		return portNumOf(intents[i].Port, prefix) < portNumOf(intents[j].Port, prefix)
	})

	var attempted []driver.PortAdminIntent
	var it driver.PortAdminIntent
	for _, it = range intents {
		var num = strings.TrimPrefix(it.Port, prefix)
		var _, convErr = strconv.Atoi(num)
		if num == it.Port || num == "" || convErr != nil {
			out.Skipped = append(out.Skipped, it.Port+
				" not touched: this name does not carry a port number in this device's numbering "+
				"— sending a wrong port number is a write to the wrong place")
			continue
		}
		var now, known = vocab.Admin[it.Port]
		if !known {
			out.Skipped = append(out.Skipped, it.Port+
				" not touched: the device does not report this port in \""+sectionPorts+"\"")
			continue
		}

		// THE GUARDRAIL. Disabling only — bringing a port back up cuts nothing.
		if !it.Enabled {
			var block, why = portAdminGuardBlocks(current.Ports[it.Port], current.MgmtVLAN,
				vocab.LinkUp[it.Port], plan.Force)
			if block {
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s NOT shut: %s. This product line has NO SSH, NO Telnet and NO serial port "+
						"— the only way back from cutting administration is a paperclip in the "+
						"reset button, on site, which also loses the configuration. Force only "+
						"knowing why", it.Port, why))
				continue
			}
		}

		var want = vocab.Disabled
		if it.Enabled {
			want = vocab.Enabled
		}
		// Already where the plan wants it: nothing is sent, and the port does not
		// appear in PortsWritten (contract).
		if now == want {
			continue
		}

		var werr = a.writeSection(ctx, sectionPorts, "set",
			portAdminEntry(num, vocab.Field, want),
			fmt.Sprintf("%s %s (%s=%s)", it.Port, adminVerb(it.Enabled), vocab.Field, want))
		if werr != nil {
			// The write path REFUSED, which on this range is the honest failure —
			// far better than the silent one. Reported per port; the loop goes on,
			// because a refusal on one port says nothing about the next.
			out.Skipped = append(out.Skipped, fmt.Sprintf("%s not %s: %v", it.Port,
				adminVerb(it.Enabled), werr))
			continue
		}
		attempted = append(attempted, it)
	}

	if len(attempted) == 0 {
		return out, nil
	}

	// THE RE-READ IS THE ONLY PROOF. `statusCode 0` on this framework means "the
	// request was well formed", not "the device changed" — apply.go's header records
	// the write that returned 0 and did nothing, and this is the same section family.
	body, err = a.RawSection(ctx, sectionPorts)
	if err != nil {
		out.Unconfirmed("administrative state", driver.PortAdminIntentPorts(attempted),
			err.Error())
	} else {
		var after = parsePortAdminVocabulary(body, prefix)
		for _, it = range attempted {
			var want = vocab.Disabled
			if it.Enabled {
				want = vocab.Enabled
			}
			if after.Field == vocab.Field && after.Admin[it.Port] == want {
				out.PortsWritten = append(out.PortsWritten, it.Port)
				continue
			}
			// In Skipped and not merely in Notes: this is the device DECLINING while
			// answering "code 0", and a caller with an empty Skipped is entitled to
			// believe every port in the plan moved.
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: the device answered \"code 0\" and %s still reads %q instead of %q — this is "+
					"this framework's silent failure, and it means the field written is not the "+
					"one that carries administrative state on this firmware. Nothing on this port "+
					"changed", it.Port, vocab.Field, after.Admin[it.Port], want))
		}
	}

	out.Notes = append(out.Notes, "this was NOT saved to flash, deliberately: shutting a port is "+
		"an action, not an intent meshcraft stores — and on a range with no console that is also "+
		"the way back, since a power cycle of the switch restores every port these writes touched")
	return out, nil
}

// portNumOf gives a sortable port number, or a value that sorts last for a name
// this device's numbering does not carry.
func portNumOf(name, prefix string) int {
	var n, err = strconv.Atoi(strings.TrimPrefix(name, prefix))
	if err != nil {
		return 1 << 30
	}
	return n
}

func adminVerb(enabled bool) string {
	if enabled {
		return "brought up"
	}
	return "shut"
}
