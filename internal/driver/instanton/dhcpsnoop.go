package instanton

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// DHCP snooping on the Instant On 1930 — read, and a write that CANNOT claim
// something the device did not do.
//
// # What is known, and what is NOT — REVISED 2026-08-11, the row shapes are in
//
// KNOWN: the product carries the feature. `Capabilities().Features` declares
// `DHCPSnooping: true` from guide 5200-5879, and the local UI has the three
// screens the contract describes (global enable, per-VLAN enable, per-port
// trust) — cf. driver/dhcpsnoop.go on why those three levels are not one.
//
// KNOWN SINCE 2026-08-09: the three SECTION NAMES, and the global section's
// FIELD names, both off the lab 1930 rather than reasoned about.
// [TestLiveProbeSnoopSections] ran read-only against sw-labo-1930 and the
// captured body is pinned verbatim in the tests (`snoopGlobalCaptured`,
// dhcpsnoop_test.go). `DHCPSnoopingEnable` was already this connector's leading
// alias and is right; the 1/2 encoding is right; and the firmware serves three
// fields nobody had guessed (`backupDatabaseEnable`, `verifyMACAddressEnable`,
// `passThroughOption82Enable` — note that the constructed fixtures' `verifyMAC`
// is NOT one of them).
//
// # KNOWN SINCE 2026-08-11, AND IT REFUTED WHAT THIS FILE USED TO WRITE
//
// PRESENCE IS MEMBERSHIP, at BOTH list levels. The two list sections carry NO
// enable field and NO trust field: the local UI SYNTHESISES both columns from
// the row being there at all —
//
//	snoopingDHCP   = found in DHCPSnoopingVLANList       ? _TRUE : _FALSE
//	trustInterface = found in DHCPSnoopingInterfaceList  ? _TRUE : _FALSE
//
// and the writes the product's own UI issues are
//
//	{DHCPSnoopingVLANList      : {set|delete: [{VLAN      : {VLANID:        n}}]}}
//	{DHCPSnoopingInterfaceList : {set|delete: [{Interface : {interfaceName: n}}]}}
//
// so the ROW WRAPPERS are `VLAN` and `Interface`, not this framework's usual
// `Entry`, and UNTRUSTING A PORT IS A `delete` — not a field written false.
//
// THIS FILE USED TO DO NEITHER, and both halves of the mistake are worth naming
// because they are the two failure modes the header below describes. It built
// `<Entry>` rows carrying a trust flag chosen from `trustInterface` / `trustPort`
// / `trustState` / `trustMode` / `trusted` — the UI's own COLUMN names, which the
// firmware never serves. On an enabled device that produced one of exactly two
// outcomes: [Admin.applySnoopPort] refusing every port for want of a flag the
// device does not publish, or the silent `statusCode 0` no-op this whole
// connector is built around. The write side is now STRICT on the captured shapes
// (one spelling, no candidates), the read side stays TOLERANT of an explicit flag
// in case a firmware does serve one — tolerating a second spelling on a read
// cannot misconfigure anything, and being strict there would make the applier's
// own re-read deny a write that landed.
//
// The comparison that used to sit here is still the right one, only the side of
// it this file is on has changed: spanning tree got its section names out of
// `pages1.js STP.initWcd` before a byte was written (stp.go), and the trap
// receivers' write gesture was reconstructed from `modules.js` / `pages2.js`
// (snmp.go). This feature got its names the other way round — by probing the
// device — which is why [Admin.locateSnoopSections] exists at all.
//
// # Hence the rule of this file: never write into a section that has not
// answered a READ on THIS device
//
// This platform has two distinct failure modes and they need two distinct
// answers:
//
//   - a section name that does not exist answers `statusCode -1`, exactly like
//     `UpdatePassword` did during the provisioning failure (admin.go) and like
//     `SNMPCommunityList` does (snmp.go). Loud, and cheap to detect;
//   - a section that DOES exist, written with a field name it does not know,
//     answers `statusCode 0` and changes nothing. Silent, and it is the one
//     failure this whole connector is built around — it is what
//     `VLANInterfaceMembershipTable` did to the first version of ApplyPorts.
//
// [Admin.locateSnoopSections] closes the first: the candidate names below are
// PROBED by read, and only a name the device actually served is ever put in a
// write URL. That is the [Admin.ProbeStaticOwner] tradition — try, observe,
// keep what the device accepted — moved from field values to section names.
//
// The second is closed differently at each level now that the shapes are known.
// The GLOBAL enable still goes through [ionPickField] — that section serves its
// fields, so the write can mirror the name the device itself printed. The two
// LISTS cannot: they are empty whenever the feature is off, which is precisely
// when an apply is wanted, so there is nothing to mirror and a probe-then-write
// would refuse for ever on the one device state that needs it. They are written
// with the captured spelling instead, one form and no candidates — this
// package's rule for a captured form (cf. fs/s3900/dhcpsnoop.go: "Captured forms
// carry only one alternative; deduced forms carry several"). And the apply
// RE-READS regardless: a row that is not there afterwards is reported in
// [driver.ApplyResult.Skipped], never as a success.
//
// # Consequence for WritesExtra
//
// `dhcp_snooping` stays FALSE in WritableExtras (capabilities.go), and the bar
// has been RESTATED twice, each time because the old wording was met without the
// thing it stood for being true.
//
//   - the first bar was "until a live run pastes a captured section body into
//     this file". The global section was pasted on 2026-08-09, and reading the
//     sentence literally would have flipped the declaration while two thirds of
//     the write were still invented;
//   - the second bar was THE ENABLED SHAPE — the two lists serving a real row.
//     That is now answered, and not by a service-affecting write: the UI's own
//     code says there is no row FIELD to read, because presence is the value.
//
// THE BAR IS NOW THE ORDINARY ONE, the same as every other range in this
// feature: a live run of [Admin.ApplyDHCPSnooping] itself. The forms are proven,
// the APPLIER is not — it has still only ever run against the fake device in
// dhcpsnoop_test.go, and on this range the run is service-affecting (enabling
// snooping with no trusted port drops the DHCP replies of everything behind the
// switch, including this device's own lease renewal). That run is what earns the
// declaration.
//
// # What is NOT here, and why
//
// The binding table (the leases snooping learned) is telemetry, and belongs to
// a monitoring path — cf. driver/dhcpsnoop.go. Rate limiting and option-82 are
// adjacent features on the same screens; nothing reads them yet, and inventing
// fields for them here would add three more unproven shapes to a file that
// already carries too many.
//
// NOTHING IS SAVED TO FLASH by [Admin.ApplyDHCPSnooping], deliberately, and it
// is not an oversight: enabling snooping on the VLAN through which this device
// gets its OWN DHCP lease, with no port yet trusted toward the server, drops the
// replies to its own renewal. On a range with no SSH, no Telnet and no serial
// port, an address lost that way costs a site visit with a paperclip. Leaving
// the write out of flash keeps this platform's free rollback — a power cycle
// undoes it — and the operator chains [Admin.Persist] once the device is
// confirmed still reachable. Same reasoning as [Admin.ApplyMgmtAddressing]
// (mgmtwrite.go), for the same absence of an out-of-band console.

var (
	_ driver.DHCPSnoopReader  = (*Admin)(nil)
	_ driver.DHCPSnoopApplier = (*Admin)(nil)
)

// The candidate section names, in probe order.
//
// # THE THREE NAMES ARE NOW CAPTURED, not guessed — 2026-08-09
//
// Probed on the lab 1930 (sw-labo-1930, JL685A, 48p + 4 SFP+) by
// [TestLiveProbeSnoopSections], which is READ ONLY. All three answered:
//
//	DHCPSnoopingGlobalSetting   ← first candidate, correct
//	DHCPSnoopingVLANList        ← first candidate, correct
//	DHCPSnoopingInterfaceList   ← SECOND candidate. See below.
//
// The port list is the one that moved. `DHCPSnoopingInterfaceISList` was the
// leading guess, on the reasoning below about interface-scoped lists, and this
// firmware DOES answer that URL — with 334 bytes that `sectionMissing` correctly
// rejects. Only `DHCPSnoopingInterfaceList` serves the real section. It is now
// first, which saves a round trip on a device that grants five sessions in
// total, and removes a dependency on `sectionMissing` getting a near-miss right.
//
// The other candidates are KEPT rather than deleted: one firmware answering is
// not every firmware answering, and the cost of a wrong guess is one request on
// a device we are already talking to.
//
// THE EMPTY LISTS ARE NOT A GAP ANY MORE — 2026-08-11. Snooping is off on the
// lab unit (`DHCPSnoopingEnable` = 2) and both list sections come back with no
// children, which is exactly what PRESENCE IS MEMBERSHIP predicts: an empty list
// means no VLAN inspected and no port trusted. The row shapes came from the
// product's own UI rather than from a device state nobody may create — the same
// provenance as stp.go's section names (pages1.js) and snmp.go's trap write
// (modules.js / pages2.js). See the file header for the two write documents.
//
// The original reasoning, kept because it is what made the guesses good and is
// still what a fourth name would be judged on:
//
//	…GlobalSetting  as in `SNMPGlobalSetting`, `IPv4GlobalSetting`,
//	                `ManagementInterfaceGlobalSetting` — this UI's name for the
//	                switch-wide half of a feature;
//	…List           as in `VLANList`, `CommunityList`, `MSTPInstanceList` — a
//	                table of entries. Note `CommunityList`, NOT
//	                `SNMPCommunityList`: this dispatcher does not always carry
//	                the feature prefix, which is why the short forms are in the
//	                list too;
//	…ISList         as in `VLANInterfaceISList`, `SNMPUserISList` — this
//	                framework's name for an INTERFACE-SCOPED list, and the shape
//	                ApplyPorts writes per port.
//
// The lists are short on purpose: every candidate costs a round trip on a
// device that grants five sessions in total, and a long list of guesses is not
// closer to the truth than a short one — it is just more expensive.
var (
	dhcpSnoopGlobalSections = []string{
		"DHCPSnoopingGlobalSetting", "DHCPSnooping", "DHCPSnoopGlobalSetting",
	}
	dhcpSnoopVLANSections = []string{
		"DHCPSnoopingVLANList", "DHCPSnoopVLANList",
	}
	// CAPTURED name first. `DHCPSnoopingInterfaceISList` answers on this
	// firmware but serves no section, so probing it first costs a round trip and
	// leans on sectionMissing to reject a near-miss.
	dhcpSnoopPortSections = []string{
		"DHCPSnoopingInterfaceList", "DHCPSnoopingInterfaceISList", "DHCPSnoopInterfaceList",
	}
)

// THE WRITE SHAPES, captured 2026-08-11 from the product's own UI (cf. the file
// header). ONE spelling each, no candidates: that is what this codebase reserves
// for a captured form, and a candidate list here would only reintroduce the
// guesses the capture just removed.
//
// Note what is NOT here: a per-VLAN enable field and a per-port trust field.
// Neither exists on this firmware. The row's PRESENCE is the value, which is why
// [snoopVLANInner] and [snoopPortInner] carry an identifier and nothing else, and
// why untrusting is a `delete` rather than a field written false.
const (
	snoopVLANWrapper = "VLAN"
	snoopVLANIDField = "VLANID"
	snoopPortWrapper = "Interface"
	snoopPortIfField = "interfaceName"
)

// Field aliases FOR THE READ, most specific FIRST. `enabled` and `trusted` come
// last in each list, not first: they are the names this UI uses for a dozen
// unrelated things (`SpanningTreeGlobalParam.enabled`, `ifEntry` status…), so a
// specific match must win when the firmware serves both.
//
// `snoopEnableFields` is also a WRITE list — the global section serves its
// fields, so the switch-wide enable is written with the name the device itself
// printed. The other three are READ-ONLY tolerance, and two of them are known
// NOT to exist on this firmware:
//
//   - `snoopTrustFields` are the UI's synthesised COLUMN names. The firmware
//     serves none of them (2026-08-11), and this file used to WRITE them, which
//     is the defect the header describes. They are kept here only so that a
//     firmware which does publish an explicit flag is believed rather than read
//     as "trusted because the row exists";
//   - the alias forms of the VLAN id and the interface name are the same kind of
//     tolerance. The captured spellings — [snoopVLANIDField], [snoopPortIfField]
//     — lead, and they are the only ones ever SENT.
var (
	snoopEnableFields = []string{
		"DHCPSnoopingEnable", "dhcpSnoopingEnable", "snoopingEnabled",
		"adminMode", "enabled",
	}
	snoopVLANIDFields = []string{snoopVLANIDField, "vlanID", "VLANId"}
	snoopTrustFields  = []string{
		"trustInterface", "trustPort", "trustState", "trustMode", "trusted",
	}
	snoopIfNameFields = []string{snoopPortIfField, "InterfaceName", "interfaceID"}
)

// reSnoopEntry matches the row wrappers this framework uses, in one pass.
//
// `VLAN` and `Interface` are the CAPTURED wrappers of the two list sections;
// `Entry` and the rest are this framework's usual ones, kept because the same
// helper reads a child list served inside the global section and because a
// tolerant read cannot misconfigure anything.
//
// Go's regexp has no backreference, so the closing tag cannot be tied to the
// opening one: on a well-formed body the non-greedy match still ends at the row's
// own closing tag, and a document malformed enough to defeat that is one we would
// refuse to write against anyway.
var reSnoopEntry = regexp.MustCompile(
	`(?is)<(?:Entry|VLANEntry|InterfaceEntry|ifEntry|VLAN|Interface)>` +
		`(.*?)</(?:Entry|VLANEntry|InterfaceEntry|ifEntry|VLAN|Interface)>`)

// reElementName lists the element names a served block carries.
var reElementName = regexp.MustCompile(`(?is)<([A-Za-z_][A-Za-z0-9_.\-]*)>`)

// ionBoolCode renders this UI's boolean: _TRUE='1', _FALSE='2' (modules.js).
// The read-side inverse is [ionTrue].
func ionBoolCode(on bool) string {
	if on {
		return "1"
	}
	return "2"
}

// ionFieldsPresent names the elements a block actually carries, sorted and
// deduplicated.
//
// EXISTS FOR THE ERROR MESSAGES, and that is not decoration. When a write cannot
// be built because none of the expected field names is there, printing what IS
// there turns a failed attempt into the survey that makes the next attempt
// certain — on a platform with no documentation, that message is the only way
// the shape ever gets learned without a second trip on site.
func ionFieldsPresent(block string) []string {
	var seen = map[string]bool{}
	var out []string
	var m []string
	for _, m = range reElementName.FindAllStringSubmatch(block, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// ionPickField returns the first alias the block ACTUALLY carries, or "".
//
// The write then speaks the device's own vocabulary rather than ours. Returning
// "" rather than a default is the whole point: a default would produce exactly
// the `statusCode 0` with no change that this file exists to avoid.
func ionPickField(block string, aliases []string) string {
	var name string
	for _, name = range aliases {
		if xmlField(block, name) != "" {
			return name
		}
	}
	return ""
}

// snoopSections is what a probe found on THIS device. Empty names mean "no
// candidate answered", which is a fact to report, never a reason to write
// somewhere else.
type snoopSections struct {
	global     string
	globalBody string
	vlan       string
	vlanBody   string
	port       string
	portBody   string
	// tried is every name put in a URL, in order — the message an operator
	// needs when the answer is "none of them".
	tried []string
}

// found reports whether anything at all answered.
func (s snoopSections) found() bool { return s.global != "" || s.vlan != "" || s.port != "" }

// vlanSource / portSource fall back to the global section's body for READING.
//
// A firmware that serves the whole feature as one section with child lists is a
// shape this framework does use (cf. `SNMPTrap`, whose `SNMPTrapReceiverList` is
// a CHILD and not a section name — snmp.go). Reading it costs nothing and tells
// the truth. WRITING it is another matter: the write targets only a section the
// device served under its own name, because a child list needs its wrapper and
// we have never seen this one's.
func (s snoopSections) vlanSource() string {
	if s.vlanBody != "" {
		return s.vlanBody
	}
	return s.globalBody
}

func (s snoopSections) portSource() string {
	if s.portBody != "" {
		return s.portBody
	}
	return s.globalBody
}

// locateSnoopSections probes the candidate names, READ ONLY.
func (a *Admin) locateSnoopSections(ctx context.Context) snoopSections {
	var out snoopSections
	out.global, out.globalBody = a.firstAnsweringSection(ctx, dhcpSnoopGlobalSections, &out.tried)
	out.vlan, out.vlanBody = a.firstAnsweringSection(ctx, dhcpSnoopVLANSections, &out.tried)
	out.port, out.portBody = a.firstAnsweringSection(ctx, dhcpSnoopPortSections, &out.tried)
	a.step(out.found(), "DHCP snooping sections probed",
		fmt.Sprintf("tried %s → global %q, VLAN %q, port %q",
			strings.Join(out.tried, ", "), out.global, out.vlan, out.port))
	return out
}

// firstAnsweringSection returns the first candidate the device serves.
//
// A read failure and a missing section are both "not this one": neither is
// grounds to stop, because the next candidate is exactly what the list is for.
func (a *Admin) firstAnsweringSection(ctx context.Context, names []string,
	tried *[]string) (string, string) {
	var name string
	for _, name = range names {
		*tried = append(*tried, name)
		var body, err = a.RawSection(ctx, name)
		if err != nil {
			continue
		}
		if sectionMissing(body, name) {
			continue
		}
		return name, body
	}
	return "", ""
}

// ReadDHCPSnooping reports the three levels the contract asks for.
//
// It returns an ERROR when no candidate section answered — never a zero
// [driver.DHCPSnoopState]. The zero value reads as "snooping is off on this
// device", and that sentence on a screen, about a device we simply could not
// question, is the worst possible answer: it is the one an operator acts on.
func (a *Admin) ReadDHCPSnooping(ctx context.Context) (driver.DHCPSnoopState, error) {
	var out driver.DHCPSnoopState
	var loc = a.locateSnoopSections(ctx)
	if !loc.found() {
		return out, fmt.Errorf("no DHCP snooping section answered on this firmware "+
			"(tried %s): the range carries the feature (guide 5200-5879) but its wcd "+
			"section names have never been captured on a 1930 — this connector reports "+
			"that rather than reporting the feature off",
			strings.Join(loc.tried, ", "))
	}
	out = snoopStateOf(loc, a.portPrefix())
	a.step(true, "DHCP snooping read",
		fmt.Sprintf("enabled=%v, %d VLAN(s), %d port(s) with a readable trust flag",
			out.Enabled, len(out.VLANs), len(out.Ports)))
	return out, nil
}

// snoopStateOf turns probed bodies into the contract state. Pure: everything
// this file guesses is testable without a device.
func snoopStateOf(loc snoopSections, prefix string) driver.DHCPSnoopState {
	var out driver.DHCPSnoopState
	var enabled, seen = parseSnoopEnabled(loc.globalBody)
	out.Enabled = enabled
	if loc.global == "" {
		out.Notes = append(out.Notes,
			"no switch-wide DHCP snooping section answered: the per-port trust flags "+
				"below may be inert, and this connector cannot say")
	} else if !seen {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"section %s answered but carries none of the expected enable fields (%s): "+
				"it serves %s — snooping reported OFF because nothing said otherwise",
			loc.global, strings.Join(snoopEnableFields, ", "),
			strings.Join(ionFieldsPresent(loc.globalBody), ", ")))
	}
	out.VLANs = parseSnoopVLANs(loc.vlanSource())
	if loc.vlan == "" && len(out.VLANs) == 0 {
		out.Notes = append(out.Notes,
			"no per-VLAN DHCP snooping section answered: this connector cannot tell "+
				"whether this firmware has a per-VLAN dimension at all, so an empty "+
				"VLAN list here means UNREAD, not \"none\"")
	}
	// PRESENCE IS MEMBERSHIP only inside the port SECTION, and the flag says so.
	// In the child-list fallback (loc.port == "" and the rows came out of the
	// global body) nothing establishes that a row means "trusted", so an explicit
	// flag is required there — the contract's rule that "untrusted" and "not read"
	// are different answers applies to every shape this connector has not
	// captured.
	out.Ports = parseSnoopPorts(loc.portSource(), prefix, loc.port != "")
	if loc.port != "" {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"per-port trust on this firmware is the ROW'S PRESENCE in %s — there is no trust "+
				"field to read. The ports listed above are the trusted ones; every other port "+
				"is untrusted", loc.port))
	}
	return out
}

// parseSnoopEnabled reads the switch-wide flag, and says whether it FOUND one.
// The two are not the same fact and the caller must be able to tell them apart.
func parseSnoopEnabled(body string) (enabled, found bool) {
	var raw = xmlFieldAny(body, snoopEnableFields...)
	if raw == "" {
		return false, false
	}
	return ionTrue(raw), true
}

// parseSnoopVLANs reads the VLANs snooping inspects, in either of the two shapes
// this framework uses for a VLAN set:
//
//	<VLANList>20,30-32</VLANList>   compressed ranges, as MSTPInstanceList serves
//	<Entry><VLANID>20</VLANID>…     one row per VLAN, as VLANList itself serves
//
// A row with NO enable field counts as enabled: on the row-shaped tables this UI
// serves, presence is membership (cf. VLANInterfaceMembershipTable). A row that
// DOES carry one is believed.
func parseSnoopVLANs(body string) []int {
	var seen = map[int]bool{}
	var out []int
	var add = func(ids []int) {
		var v int
		for _, v = range ids {
			if seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	add(parseVLANRanges(xmlFieldAny(body, "VLANList", "vlanList", "snoopingVLANs")))

	var m []string
	for _, m = range reSnoopEntry.FindAllStringSubmatch(body, -1) {
		var block = m[1]
		var raw = xmlFieldAny(block, snoopVLANIDFields...)
		if raw == "" {
			continue
		}
		var ids = parseVLANRanges(raw)
		if len(ids) == 0 {
			continue
		}
		var flag = xmlFieldAny(block, snoopEnableFields...)
		if flag != "" && !ionTrue(flag) {
			continue
		}
		add(ids)
	}
	sort.Ints(out)
	return out
}

// parseSnoopPorts reads per-port TRUST.
//
// `presenceIsMembership` is what the caller establishes and this function may
// not: TRUE when the rows came from `DHCPSnoopingInterfaceList` itself, where a
// row being there IS the trust flag (captured 2026-08-11 — the section carries
// no trust field, and the UI synthesises its `trustInterface` column exactly this
// way). The trusted ports are then the ones listed, and a port absent from the
// list is untrusted; it is absent from the returned state too, because this
// section alone does not enumerate the faceplate.
//
// FALSE for any other body — the child-list fallback. There a row with no
// readable trust field is LEFT OUT, as the contract requires: "untrusted" and
// "not read" call for different things on screen, and untrusted is the protected
// state, so reporting it for a port we failed to read would announce a protection
// nobody installed.
//
// An EXPLICIT flag is believed in both cases, exactly as [parseSnoopVLANs]
// believes a served enable field: this firmware does not publish one, another
// might, and inventing "trusted" over a served "false" is the one reading that
// could not be defended.
func parseSnoopPorts(body, prefix string, presenceIsMembership bool) []driver.DHCPSnoopPortState {
	var out []driver.DHCPSnoopPortState
	var m []string
	for _, m = range reSnoopEntry.FindAllStringSubmatch(body, -1) {
		var block = m[1]
		var raw = xmlFieldAny(block, snoopIfNameFields...)
		if raw == "" {
			continue
		}
		var trusted = presenceIsMembership
		var field = ionPickField(block, snoopTrustFields)
		switch {
		case field != "":
			trusted = ionTrue(xmlField(block, field))
		case !presenceIsMembership:
			continue
		}
		out = append(out, driver.DHCPSnoopPortState{
			Port:    facadePortName(raw, prefix),
			Trusted: trusted,
		})
	}
	sort.Slice(out, func(i, j int) bool { return snoopPortLess(out[i].Port, out[j].Port) })
	return out
}

// snoopPortLess orders facade names the way the FACEPLATE does: prefix, then
// NUMERICALLY.
//
// `out[i].Port < out[j].Port` was here, and plain string order puts GE10 between
// GE1 and GE2. The device serves its rows in faceplate order; a lexical sort
// takes a list an operator reads against a physical panel and shuffles it, so
// the trust flag they check on socket 2 is the tenth line down. Numeric, like
// cisco/ios/dhcpsnoop.go's stdPortLess and for the same reason.
func snoopPortLess(a, b string) bool {
	var pa, na = splitSnoopPortName(a)
	var pb, nb = splitSnoopPortName(b)
	if na < 0 || nb < 0 || pa != pb {
		return a < b
	}
	return na < nb
}

// splitSnoopPortName cuts a facade name into its media prefix and its socket
// number. n is -1 for a name that does not end in digits.
func splitSnoopPortName(s string) (string, int) {
	var i = len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	if i == len(s) {
		return s, -1
	}
	var n, err = strconv.Atoi(s[i:])
	if err != nil {
		return s, -1
	}
	return s[:i], n
}

// --- Write ---

// snoopGlobalInner builds the switch-wide enable body.
func snoopGlobalInner(field string, on bool) string {
	return "<" + field + ">" + ionBoolCode(on) + "</" + field + ">"
}

// snoopVLANInner builds one per-VLAN row, in the captured shape:
//
//	{DHCPSnoopingVLANList:{set:[{VLAN:{VLANID:n}}]}}
//
// PRESENCE IS MEMBERSHIP — the row exists, therefore the VLAN is inspected — so
// there is no enable field to send. The builder has no `off` form at all, which
// is the prohibition [driver.DHCPSnoopApplier] states: this connector never
// removes a VLAN from snooping.
func snoopVLANInner(vlan int) string {
	return "<" + snoopVLANWrapper + "><" + snoopVLANIDField + ">" +
		strconv.Itoa(vlan) + "</" + snoopVLANIDField + "></" + snoopVLANWrapper + ">"
}

// snoopPortInner builds one per-port row, in the captured shape:
//
//	{DHCPSnoopingInterfaceList:{set|delete:[{Interface:{interfaceName:n}}]}}
//
// The row carries the interface and NOTHING ELSE — trust is the row's presence,
// so the direction is in the ACTION ([snoopPortAction]) and not in a field. The
// numeric interfaceName is the one every other write in this package uses (cf.
// ApplyPorts, applySTPPort).
func snoopPortInner(iface string) string {
	return "<" + snoopPortWrapper + "><" + snoopPortIfField + ">" +
		xmlEscape(iface) + "</" + snoopPortIfField + "></" + snoopPortWrapper + ">"
}

// snoopPortAction maps a trust intent onto the wcd action that expresses it.
//
// UNTRUST IS A `delete`, and that is the half a reader is most likely to get
// wrong: writing the row with a flag set false is what this file used to do, and
// on a firmware that has no such flag it is either refused or accepted with no
// effect. Removing the row is how the product's own UI untrusts a port.
func snoopPortAction(trusted bool) string {
	if trusted {
		return "set"
	}
	return "delete"
}

// ApplyDHCPSnooping sets per-port trust, and turns the feature ON above it.
//
// THE ORDER IS GLOBAL, THEN VLANS, THEN PORTS, and it is the safe one: a trusted
// port on a device whose global switch is off is an exact configuration with zero
// protection, and it is what an operator would be shown as done. Enabling first
// means every intermediate state is at least as protected as the one before it.
//
// NOTHING IS EVER TURNED OFF here — not the global switch, not a VLAN this apply
// found already enabled. Untrusting a port is a per-port write and stays one;
// stripping protection from a whole device because a plan stopped naming a VLAN
// is a decision no screen asked for (cf. driver/dhcpsnoop.go).
//
// AND NOTHING IS SAVED TO FLASH (cf. the file header): on a range with no
// out-of-band console, snooping enabled on the management VLAN with no trusted
// uplink can cost this device its own DHCP lease. The operator chains
// [Admin.Persist] once the device has been confirmed still reachable.
func (a *Admin) ApplyDHCPSnooping(ctx context.Context, plan driver.DHCPSnoopPlan) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if !WritesExtra("dhcp_snooping") {
		// THE SENTENCE HAS BEEN WRONG TWICE, AND BOTH TIMES IT SENT A READER TO DO
		// WORK THAT WAS ALREADY DONE. It first said the section names were
		// hypotheses after the probe had captured all three; it then said the row
		// shapes were hypotheses after the UI's own code settled them. What is left
		// is neither: it is the APPLIER, which has never run against a 1930.
		out.Notes = append(out.Notes,
			"Instant On DHCP snooping apply is not declared writable (WritableExtras): "+
				"the three wcd section names AND both row shapes are captured, but this "+
				"APPLIER has never run against a 1930 — the live run is what earns the "+
				"declaration, and on this range it is service-affecting, so "+
				"nothing was sent to the device")
		a.step(true, "DHCP snooping apply deferred", out.Notes[0])
		return out, nil
	}

	var loc = a.locateSnoopSections(ctx)
	if !loc.found() {
		return out, fmt.Errorf("no DHCP snooping section answered on this firmware "+
			"(tried %s): nothing was written — this connector never posts to a section "+
			"name the device has not served", strings.Join(loc.tried, ", "))
	}
	var before = snoopStateOf(loc, a.portPrefix())

	// 1. The switch-wide enable, and SAY SO. A device that arrived with snooping
	//    off and leaves with it on has been changed in a way that affects every
	//    port on the box, not only the ports this plan named.
	if !before.Enabled {
		var field = ionPickField(loc.globalBody, snoopEnableFields)
		switch {
		case loc.global == "":
			out.Skipped = append(out.Skipped,
				"switch-wide DHCP snooping NOT enabled: no global section answered "+
					"(tried "+strings.Join(dhcpSnoopGlobalSections, ", ")+"). Any trust "+
					"flag written below is inert until the feature is on.")
		case field == "":
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"switch-wide DHCP snooping NOT enabled: section %s carries none of %s "+
					"— it serves %s. Any trust flag written below is inert.",
				loc.global, strings.Join(snoopEnableFields, ", "),
				strings.Join(ionFieldsPresent(loc.globalBody), ", ")))
		default:
			var err = a.writeSection(ctx, loc.global, "set",
				snoopGlobalInner(field, true),
				"DHCP snooping enabled switch-wide via "+loc.global)
			if err != nil {
				// AN ORDINARY REFUSAL, not a Go error. The two branches above report
				// exactly this situation in Skipped and carry on; returning an error
				// here threw away the whole gesture — including the port refusals
				// already collected, which are the survey the next run needs. The
				// contract puts a platform's refusal in Skipped (driver/dhcpsnoop.go).
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"switch-wide DHCP snooping NOT enabled: %s.%s was refused (%v). Any "+
						"trust flag written below is inert until the feature is on.",
					loc.global, field, err))
				break
			}
			out.Notes = append(out.Notes, fmt.Sprintf(
				"switch-wide DHCP snooping was OFF and this apply turned it ON "+
					"(%s.%s): EVERY port on this device is now inspected, not only the "+
					"ports named in the plan", loc.global, field))
		}
	}

	// 2. The VLANs, ADDITIVELY. A VLAN found already enabled is protection someone
	//    else installed, and this apply does not remove it.
	var vnotes, claimedVLANs = a.applySnoopVLANs(ctx, loc, plan, before, &out)
	out.Notes = append(out.Notes, vnotes...)

	// 3. The ports.
	var intents = append([]driver.DHCPSnoopPortIntent{}, plan.Ports...)
	sort.Slice(intents, func(i, j int) bool { return snoopPortLess(intents[i].Port, intents[j].Port) })
	var written []string
	var intent driver.DHCPSnoopPortIntent
	for _, intent = range intents {
		var note, ok = a.applySnoopPort(ctx, loc, intent)
		if !ok {
			out.Skipped = append(out.Skipped, note)
			continue
		}
		written = append(written, intent.Port)
	}

	// 4. RE-READ, because on this platform a `statusCode 0` is not evidence.
	var after = snoopStateOf(a.locateSnoopSections(ctx), a.portPrefix())
	out.PortsWritten, out.Skipped = verifySnoopPorts(written, intents, after, out.Skipped)
	// The VLAN rows get the SAME treatment as the port rows, and they did not
	// before: their "enabled on VLAN n" note was written on the strength of a
	// statusCode 0, which this file's own header calls the one failure this
	// connector is built around. A VLAN claimed and not read back is a VLAN
	// nothing inspects, with a trusted port pointing at it.
	out.Skipped = append(out.Skipped, verifySnoopVLANs(claimedVLANs, after, loc.vlan)...)
	if !after.Enabled {
		out.Notes = append(out.Notes,
			"RE-READ AFTER APPLY: switch-wide DHCP snooping still reads OFF. The device "+
				"accepted the write and nothing moved — this firmware's global enable "+
				"field is not among the names this connector knows.")
	}
	out.Notes = append(out.Notes, snoopMgmtVLANWarning(after, plan)...)
	out.Notes = append(out.Notes,
		"NOT saved to flash: a power cycle undoes this apply. Persist once the device "+
			"is confirmed still reachable — this range has no out-of-band console, and "+
			"snooping on the management VLAN with no trusted uplink drops this device's "+
			"own DHCP renewal.")
	a.step(true, "DHCP snooping applied",
		fmt.Sprintf("%d port(s) verified, %d refusal(s)", len(out.PortsWritten), len(out.Skipped)))
	return out, nil
}

// applySnoopVLANs enables snooping on the plan's VLANs, and never on anything
// else. Returns the notes to report and the VLANs it CLAIMS to have enabled —
// the second is what the re-read then has to confirm. Refusals go straight
// into `out`.
func (a *Admin) applySnoopVLANs(ctx context.Context, loc snoopSections,
	plan driver.DHCPSnoopPlan, before driver.DHCPSnoopState,
	out *driver.ApplyResult) ([]string, []int) {

	var notes []string
	if len(plan.VLANs) == 0 {
		// Nothing was derivable from the named ports and the site declares none
		// either (api/dhcpsnoop_apply.go, dhcpSnoopVLANsFor). The feature is on and
		// inspects nothing — an operator reading "trusted" on a port deserves to
		// know that.
		return append(notes,
			"this plan names no VLAN to inspect, and none could be derived from the ports "+
				"it trusts: the feature is on and covers no VLAN yet, so no traffic is "+
				"inspected"), nil
	}
	// SORTED, and it was not. `missing` preserves the caller's order, so the same
	// gesture arriving as {99,10} and as {10,99} produced two different sequences
	// of writes — two transcripts that cannot be compared, on a platform where
	// comparing transcripts is how a shape gets learned.
	var wanted = snoopSortedVLANs(missing(plan.VLANs, before.VLANs))
	if len(wanted) == 0 {
		return append(notes, fmt.Sprintf(
			"DHCP snooping already covers the VLAN(s) this plan asks for (%s): nothing added",
			vlanCSV(snoopSortedVLANs(plan.VLANs)))), nil
	}
	if loc.vlan == "" {
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"VLANs %s NOT enabled for DHCP snooping: no per-VLAN section answered "+
				"(tried %s). Snooping is on switch-wide and inspects whatever this "+
				"firmware already had.",
			vlanCSV(wanted), strings.Join(dhcpSnoopVLANSections, ", ")))
		return notes, nil
	}
	// NO ROW-SHAPE PROBE HERE, and its absence is deliberate. This section is
	// EMPTY on every device whose snooping is off — which is every device an
	// enable is wanted on — so a write gated on reading a row back would refuse
	// for ever in exactly the situation it exists for. The shape is captured
	// instead ([snoopVLANInner]), and the re-read below is what keeps it honest.
	var claimed []int
	var v int
	for _, v = range wanted {
		var err = a.writeSection(ctx, loc.vlan, "set", snoopVLANInner(v),
			fmt.Sprintf("DHCP snooping enabled on VLAN %d via %s", v, loc.vlan))
		if err != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"VLAN %d NOT enabled for DHCP snooping: %v", v, err))
			continue
		}
		claimed = append(claimed, v)
		notes = append(notes, fmt.Sprintf(
			"DHCP snooping enabled on VLAN %d (%s): this covers traffic on that VLAN "+
				"across the whole device", v, loc.vlan))
	}
	return notes, claimed
}

// snoopSortedVLANs returns a sorted, deduplicated COPY. A copy because the plan
// belongs to the caller and an apply that reorders its argument makes a second
// apply on the same plan behave differently from the first.
func snoopSortedVLANs(ids []int) []int {
	var seen = map[int]bool{}
	var out []int
	var v int
	for _, v = range ids {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// verifySnoopVLANs keeps the VLAN writes honest, the way verifySnoopPorts keeps
// the port writes honest.
//
// The per-VLAN write claimed success from a `statusCode 0`, which on this
// framework is what a section returns when it exists and the field name written
// into it does not (cf. the file header: `VLANInterfaceMembershipTable` did this
// to the first ApplyPorts). A VLAN claimed and not read back inspects nothing,
// and the trust flags pointing at it protect nothing.
func verifySnoopVLANs(claimed []int, after driver.DHCPSnoopState, section string) []string {
	if len(claimed) == 0 {
		return nil
	}
	var lost = missing(claimed, after.VLANs)
	if len(lost) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"VLAN(s) %s: section %s answered code 0 and the re-read does not show DHCP "+
			"snooping covering them. Nothing is inspected on those VLANs — this "+
			"firmware's per-VLAN row shape is not the one written. The fields it does "+
			"serve are in the read note above.", vlanCSV(snoopSortedVLANs(lost)), section)}
}

// applySnoopPort writes ONE port's trust. The bool reports whether the device
// accepted the request; the string is the refusal to report when it did not.
//
// TRUST IS A `set` OF THE ROW AND UNTRUST IS A `delete` OF IT — there is no
// trust field on this firmware, so nothing here picks one (cf. [snoopPortInner]).
func (a *Admin) applySnoopPort(ctx context.Context, loc snoopSections,
	intent driver.DHCPSnoopPortIntent) (string, bool) {
	if loc.port == "" {
		return fmt.Sprintf("%s: trust NOT written — no per-port DHCP snooping section "+
			"answered (tried %s)", intent.Port, strings.Join(dhcpSnoopPortSections, ", ")), false
	}
	var iface = devicePortName(intent.Port, a.portPrefix())
	var action = snoopPortAction(intent.Trusted)
	var err = a.writeSection(ctx, loc.port, action, snoopPortInner(iface),
		fmt.Sprintf("%s DHCP snooping trust=%v via %s (%s)",
			intent.Port, intent.Trusted, loc.port, action))
	if err != nil {
		return fmt.Sprintf("%s: trust NOT written — %v", intent.Port, err), false
	}
	return "", true
}

// verifySnoopPorts keeps only the ports whose trust ACTUALLY MOVED.
//
// This is where this platform's silent failure is caught. A port the device
// accepted a write for, and whose re-read does not show the intent, did not
// change: it goes in Skipped with that sentence, because a caller receiving an
// empty Skipped is entitled to believe every port in the plan moved.
//
// PRESENCE IS THE EVIDENCE, in both directions: a trusted port must appear in
// the re-read's port list, and an untrusted one must be gone from it. That is
// why there is no "unreadable" branch any more — on a firmware where the row IS
// the flag, "no row" is not a failed read, it is the answer "untrusted".
func verifySnoopPorts(written []string, intents []driver.DHCPSnoopPortIntent,
	after driver.DHCPSnoopState, skipped []string) ([]string, []string) {
	var observed = map[string]bool{}
	var p driver.DHCPSnoopPortState
	for _, p = range after.Ports {
		observed[p.Port] = p.Trusted
	}
	var want = map[string]bool{}
	var intent driver.DHCPSnoopPortIntent
	for _, intent = range intents {
		want[intent.Port] = intent.Trusted
	}
	var out []string
	var name string
	for _, name = range written {
		if observed[name] == want[name] {
			out = append(out, name)
			continue
		}
		if want[name] {
			skipped = append(skipped, name+": the device answered code 0 and the re-read "+
				"still does not list this port as trusted — the row was not created. "+
				"Nothing else was changed.")
			continue
		}
		skipped = append(skipped, name+": the device answered code 0 to the row's DELETE "+
			"and the re-read still lists this port as trusted — the row is still there. "+
			"Nothing else was changed.")
	}
	return out, skipped
}

// snoopMgmtVLANWarning states the one hazard this gesture carries on a range
// with no out-of-band console.
//
// Snooping enabled on the VLAN this device is administered through, with NO
// trusted port anywhere on the box, drops the DHCP replies to its own lease
// renewal. Nothing breaks at apply time — it breaks at renewal, hours later,
// and the recovery is a paperclip on site. A note, not a refusal: the contract
// asks the connector to enable the feature, and the operator may well have a
// trusted uplink in mind that this plan does not name.
func snoopMgmtVLANWarning(after driver.DHCPSnoopState, plan driver.DHCPSnoopPlan) []string {
	if !after.Enabled {
		return nil
	}
	var anyTrusted bool
	var p driver.DHCPSnoopPortState
	for _, p = range after.Ports {
		if p.Trusted {
			anyTrusted = true
			break
		}
	}
	var intent driver.DHCPSnoopPortIntent
	for _, intent = range plan.Ports {
		if intent.Trusted {
			anyTrusted = true
			break
		}
	}
	if anyTrusted {
		return nil
	}
	return []string{
		"DHCP snooping is ON and NO port on this device is trusted. If this switch " +
			"takes its own management address by DHCP, its next lease renewal will be " +
			"dropped by the protection just installed — and this range has no console " +
			"to recover through. Trust the uplink toward the DHCP server before saving.",
	}
}
