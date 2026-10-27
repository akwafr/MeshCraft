package instanton

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Setting the device's OWN name — "System Name" on this range, `sysName` in the
// firmware's own vocabulary.
//
// # The section name is NOT CAPTURED, and this file is built around that fact
//
// Everything else this connector writes goes to a `wcd` section whose name was
// OBSERVED in the scripts the device serves: `AdminUserSetting`,
// `SNMPGlobalSetting`, `IPv4InterfaceList`, `SpanningTreeGlobalParam`,
// `MSTPVLANList`… For the system name there is nothing. A repository-wide search
// (this package, and the vault under docs/) turns up no system/identification
// section at all — the closest thing is the provisioning API's `/api/systemInfo`,
// which carries model, serial, MAC and firmware and NO name field.
//
// What IS captured is the FIELD name. The classic login page serves the value in a
// hidden input literally called `sysName` (cf. reSysName in inspect.go, observed
// on a JL685A running InstantOn_1930_3.4.0.0 — and inspect.go also records that
// by default the device names itself from its serial number). That is this
// firmware's own word for the fact, which is why `sysName` heads the alias list
// below.
//
// # So the section is DISCOVERED at write time, and refused when it is not found
//
// Guessing a section name on this platform produces its characteristic failure:
// `statusCode 0`, no change, no message — a success that did nothing. The
// mechanism used here is the one this package already uses for the same problem
// (cf. dhcpsnoop.go's candidate probe): READ the candidates first, keep the one
// the device actually serves, and write ONLY a field seen in the device's own
// response. Nothing is ever posted to a name or a field that has not just
// answered.
//
// When no candidate answers, this REFUSES and names every name it tried. That
// message is not an apology — on a platform with no documentation it is the survey
// that makes the next attempt certain, and it is the only way the shape ever gets
// learned without a second trip on site.
//
// # Consequence for the caller: this connector DOES declare the capability, since 2026-08-11
//
// [driver.HostnameSetter] is satisfied — [Admin.SetHostname] carries the
// interface's name — and api.hostnameWritableConnectors says `true` for this
// range. The two edits landed together, which is what the staging rule required,
// and what unblocked them is a HARDWARE RUN rather than a re-reading: on
// sw-labo-1930 (JL685A, InstantOn_1930_3.4.0.0) the write went out, came back on
// an INDEPENDENT re-read, was saved to flash and was reverted, all through this
// method. Before that the connector kept a private method name for exactly the
// reason the § on SetHostname still records, and the alternative — advertise the
// gesture and let the probe refuse loudly at write time — was rejected.
//
// # What is NOT done
//
//   - No write to `/api/systemInfo`: that endpoint is the PROVISIONING API on port
//     4443, it closes once the device is in local administration, and it has no
//     name field to write.
//   - No ceiling of our own beyond driver.ValidHostname's 63 bytes: this range's
//     real limit is not captured anywhere, and the re-read below compares byte for
//     byte, which is what catches a device that shortens what it took.
//   - Still NO `var _ driver.HostnameSetter = (*Admin)(nil)` assertion, and it
//     would change nothing if there were: the capability is advertised by the
//     METHOD NAME, which api.pushHostname resolves at runtime. See the comment on
//     SetHostname.

// The candidate section names, in probe order. Each is a HYPOTHESIS built on the
// naming habits this package has OBSERVED on this firmware — that is the only
// thing that makes one candidate better than another here:
//
//	…Setting        as in `AdminUserSetting`, `EncryptionSetting` — this UI's name
//	                for a small group of switch-wide fields;
//	…GlobalSetting  as in `SNMPGlobalSetting`, `IPv4GlobalSetting`,
//	                `ManagementInterfaceGlobalSetting` — the switch-wide half of a
//	                feature.
//
// The list is short on purpose, and for the same reason dhcpsnoop.go's is: every
// candidate costs a round trip on a device that grants five sessions in total, and
// a long list of guesses is not closer to the truth than a short one — it is just
// more expensive.
var systemNameSections = []string{
	"SystemSetting", "SystemGlobalSetting", "SystemInfoSetting", "SystemNameSetting",
}

// Field aliases, the CAPTURED one first.
//
// `sysName` is not a guess: it is the name of the hidden input the login page
// serves this exact value in (inspect.go). The rest are fallbacks in this
// framework's habitual spellings. `name` comes LAST, not first: it is what this UI
// calls a dozen unrelated things, so a specific match must win when the firmware
// serves both.
var systemNameFields = []string{"sysName", "systemName", "hostName", "deviceName", "name"}

// sysNameLocation is what a probe found on THIS device. An empty section means
// "no candidate answered with a usable field", which is a fact to report — never
// a reason to write somewhere else.
type sysNameLocation struct {
	section string
	field   string
	// value is the name the device carries right now, as the probe read it. Used
	// to skip a write that would change nothing, and to tell the operator what to
	// put back when a write goes wrong.
	value string
	// tried is every name put in a URL, in order — the message an operator needs
	// when the answer is "none of them".
	tried []string
}

// found reports whether a section AND a field were located.
func (l sysNameLocation) found() bool { return l.section != "" && l.field != "" }

// locateSystemName probes the candidate sections and keeps the first that serves
// one of the known field names.
//
// A section that answers but carries none of the aliases is NOT good enough:
// writing an invented field into a real section is the same silent failure as
// writing to an invented section, and it is harder to notice because the section
// name looked right.
func (a *Admin) locateSystemName(ctx context.Context) sysNameLocation {
	var out sysNameLocation
	var name string
	for _, name = range systemNameSections {
		out.tried = append(out.tried, name)
		var body, err = a.RawSection(ctx, name)
		if err != nil {
			continue
		}
		if sectionMissing(body, name) {
			continue
		}
		var field = ionPickField(body, systemNameFields)
		if field == "" {
			// Report what the section DOES carry: on a platform with no
			// documentation, that list is how the next attempt gets it right.
			a.step(false, "section "+name+" carries no system-name field",
				"fields served: "+strings.Join(ionFieldsPresent(body), ", "))
			continue
		}
		out.section = name
		out.field = field
		out.value = xmlField(body, field)
		return out
	}
	return out
}

// hostnameMismatch explains a re-read that does not carry the requested name.
//
// `before` travels in the message because this platform's silent failure leaves
// the old value in place: an operator reading this needs to know the device is
// still called what it was called, not go looking for it.
func hostnameMismatch(want, got, before string) string {
	if got == "" {
		return "the device answered code 0 but the section re-reads with no system " +
			"name: a 2xx that changes nothing is this platform's characteristic " +
			"failure, and the re-read is the only thing that catches it. The name was " +
			"\"" + before + "\" before this attempt"
	}
	if len(got) < len(want) && strings.HasPrefix(want, got) {
		return fmt.Sprintf("the device stored %q — the requested name cut at %d "+
			"bytes. That is this range's own ceiling, learned here rather than guessed; "+
			"the switch now answers to a name meshcraft does not carry. Send a name of "+
			"at most %d bytes, or set %q back", got, len(got), len(got), before)
	}
	return fmt.Sprintf("the section re-reads its system name as %q, not %q (it was %q "+
		"before this attempt): the write did not take the way it was sent",
		got, want, before)
}

// SetHostname sets the device's own system name.
//
// Probe, write, re-read, save — in that order, and none of the four is optional.
// The save matters more here than on most platforms: this range is
// `apply_then_save`, so a name that is only applied is a name the next power cut
// removes, while every meshcraft screen goes on claiming it.
//
// # THE NAME OF THIS METHOD IS THE DECLARATION, and it was earned on 2026-08-11
//
// It was called `setHostnameProbe` until then. PROMOTED on a hardware run, not on
// a reading: sw-labo-1930 (JL685A, InstantOn_1930_3.4.0.0) answered
// `SystemGlobalSetting` on the SECOND candidate — `SystemSetting` answers too, in
// 306 bytes, and carries no name field, which is exactly the case
// locateSystemName exists to reject — the write returned code 0, an INDEPENDENT
// re-read of the section carried the new value, [Admin.SaveConfig] persisted it,
// and the serial the unit shipped with (`CN0EXAMPL1`) was put back the same way.
// Both edits landed together; the paragraphs below are kept because they are why
// the gate existed, and they are the rule for the NEXT capability staged this way.
//
// Naming this method `SetHostname` makes *Admin satisfy
// [driver.HostnameSetter] — and satisfying it is not a documentation choice, it
// is the ADVERTISEMENT itself: api.pushHostname resolves the capability with
// driver.AsHostnameSetter, a runtime type assertion that succeeds on the
// method's mere existence. A compile-time `var _` line has nothing to do with
// it; removing one while keeping the method would change exactly nothing about
// what the caller can reach.
//
// SINCE THE NAME IS RECONCILED (2026-08-06), the stake is higher than a button
// that fails. The API layer answers "can this device converge?" WITHOUT a
// session, from api.hostnameWritableConnectors, and this range was the single
// `false` entry in that map. Satisfying the interface while the map said `false`
// would have made the map disagree with reality and every screen would report
// `drift` — "a repair is coming" — for a device nothing ever pushed to.
// Promoting this method therefore meant TWO edits, and a test in
// api/hostnameheal_test.go fails if only one is done.
//
// While the write was unproven the method kept a private name and this connector
// did NOT declare the capability. That is the same staging rule this codebase
// applies to
// [driver.Connector.WritesExtra] — ship the code, declare nothing until a
// capture proves the form — transposed onto an optional interface, where the
// only available switch is the method's name. It is also what connector.go's
// § on the mandatory/optional boundary requires: "a type assertion happens
// BEFORE the gesture begins… the caller can refuse cleanly and avoid offering a
// button that leads nowhere", and an unknown capability is assumed ABSENT.
//
// PROMOTING IT TOOK TWO EDITS, NOT ONE, and doing half the job is the
// interesting way to get this wrong: renaming this method to `SetHostname` AND
// flipping this connector's entry in api.hostnameWritableConnectors. Rename only,
// and the reconciliation still believes the range cannot converge, so nothing
// ever pushes; flip only, and every screen promises a repair no code performs.
// Neither half fails to compile — the test in api/hostnameheal_test.go is what
// catches the mismatch, by asserting the map against each connector's real
// access type. That is the recipe for the NEXT capability staged behind a method
// name, and it is why this paragraph stays.
//
// The rule it was gated on was "do it once a `wcd` section carrying the system
// name has actually been observed AND WRITTEN". The section can be hunted
// WITHOUT advertising anything, through the raw-section reconnaissance path this
// package exposes ([RawReader], served by GET /api/devices/{id}/wcd/*) — read the
// candidates listed in systemNameSections above and see which one answers with a
// field from systemNameFields, `sysName` first; the write itself is exercised by
// `live_hostname_test.go`, which drives THIS method rather than a lookalike.
func (a *Admin) SetHostname(ctx context.Context, name string) error {
	// The shared floor first, before a single request: an invalid name must not
	// cost a round trip on a device that grants five sessions.
	if !driver.ValidHostname(name) {
		return fmt.Errorf("system name %q refused: it must carry only letters, "+
			"digits and hyphens, must not start or end with a hyphen, and must fit "+
			"in 63 bytes — this connector does not normalize it, because a device "+
			"answering to a name meshcraft did not choose breaks the LLDP "+
			"correlation this gesture exists to enable", name)
	}

	var loc = a.locateSystemName(ctx)
	a.step(loc.found(), "system-name section probed",
		fmt.Sprintf("tried %s → section %q, field %q",
			strings.Join(loc.tried, ", "), loc.section, loc.field))
	if !loc.found() {
		return fmt.Errorf("no system-name section answered on this firmware (tried %s): "+
			"the field exists — the login page serves it as \"sysName\" — but the `wcd` "+
			"section that WRITES it has never been captured on a 1930. This connector "+
			"reports that rather than posting to a guessed section, which on this "+
			"platform answers code 0 and changes nothing",
			strings.Join(loc.tried, ", "))
	}
	if loc.value == name {
		// A write that changes nothing is a risk taken for nothing, and on this
		// range it would also spend the save that follows it.
		a.step(true, "system name already \""+name+"\"", "no write sent")
		return nil
	}

	var err = a.writeSection(ctx, loc.section, "set",
		"<"+loc.field+">"+xmlEscape(name)+"</"+loc.field+">",
		"system name set to "+name)
	if err != nil {
		return err
	}

	// THE RE-READ IS THE ONLY PROOF. `statusCode 0` on this interface means "the
	// request was understood", not "something changed" (cf. ApplyPorts, which
	// caught exactly that on the VLAN membership section).
	var body string
	body, err = a.RawSection(ctx, loc.section)
	if err != nil {
		a.step(false, "system name not re-read", err.Error())
		return fmt.Errorf("system name %q sent to %s but NOT re-read (%v): nothing was "+
			"saved to flash, and meshcraft cannot confirm the device carries it — it was "+
			"%q before this attempt", name, loc.section, err, loc.value)
	}
	var got = xmlField(body, loc.field)
	if got != name {
		var why = hostnameMismatch(name, got, loc.value)
		a.step(false, "system name not confirmed", why)
		return fmt.Errorf("system name on %s: %s", loc.section, why)
	}
	a.step(true, "system name confirmed by re-read", loc.field+" = "+got)

	// PERSIST. Unlike the management-addressing writes — which deliberately do NOT
	// save, because a power cut is their recovery net — a hostname must survive the
	// reboot: it was OBSERVED as set, and every screen will keep claiming it.
	err = a.SaveConfig(ctx)
	if err != nil {
		return fmt.Errorf("system name %q is set on the device but NOT saved to flash "+
			"(%v): a reboot would bring %q back while every screen kept showing the new "+
			"name", name, err, loc.value)
	}
	return nil
}
