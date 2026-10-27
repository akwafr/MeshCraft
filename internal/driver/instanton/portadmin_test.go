package instanton

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// portSectionXML builds a "Standard802_3List" response. CONSTRUCTED: the shape
// (Entry / interfaceName / linkState) is the one this connector has been reading on
// sw-labo-1930 since it was written. The `extra` argument adds a field per port —
// used both for the CAPTURED administrative field (`adminState`, cf.
// [TestPortAdminVocabularyPrefersTheCapturedField]) and for the names that are
// still only candidates.
func portSectionXML(extra map[string]string, linked map[string]bool, ports ...string) string {
	var out = `<?xml version="1.0" encoding="UTF-8" ?><ResponseData><DeviceConfiguration>` +
		`<Standard802_3List type="section">`
	var p string
	for _, p = range ports {
		out += "<Entry><interfaceName>" + p + "</interfaceName>"
		if linked[p] {
			out += "<linkState>1</linkState>"
		} else {
			out += "<linkState>2</linkState>"
		}
		var k, v string
		for k, v = range extra {
			if strings.HasPrefix(v, p+"=") {
				out += "<" + k + ">" + strings.TrimPrefix(v, p+"=") + "</" + k + ">"
			}
		}
		out += "</Entry>"
	}
	return out + `</Standard802_3List></DeviceConfiguration></ResponseData>`
}

// THE REFUSAL THAT MATTERS MOST: the section as this package has actually captured
// it carries no administrative field, so nothing may be written — on a range whose
// only way back is a factory reset.
func TestPortAdminVocabularyRefusesWhenNoAdminFieldIsPublished(t *testing.T) {
	var body = portSectionXML(nil, map[string]bool{"1": true}, "1", "12")
	var v = parsePortAdminVocabulary(body, "GE")
	if v.Field != "" {
		t.Fatalf("no administrative field is present, yet %q was selected", v.Field)
	}
	if !strings.Contains(v.Why, "will not invent") {
		t.Errorf("the refusal must say that no field name is invented: %q", v.Why)
	}
}

// "linkStateTrapEnabled" is present in this section on real hardware (snmp.go:
// all 68 interfaces carry 1) and it contains the word "Enabled". Writing to it
// would leave the port forwarding AND silence the traps that would have revealed
// it, so it must NEVER be selected as the administrative field.
func TestPortAdminVocabularyIgnoresLinkStateTrapEnabled(t *testing.T) {
	var body = portSectionXML(
		map[string]string{"linkStateTrapEnabled": "1=1"},
		map[string]bool{"1": true}, "1")
	var v = parsePortAdminVocabulary(body, "GE")
	if v.Field != "" {
		t.Fatalf("selected %q — a trap-enable flag is not administrative state", v.Field)
	}
}

// When the device DOES publish an administrative field, the "enabled" value is
// derived from a port that has a LINK: a live link cannot exist on a shut port, so
// that port's value defines enabled. CONSTRUCTED — port 1 is linked and carries
// adminStatus 1, port 12 is dark and carries 2.
func TestPortAdminVocabularyDerivesEnabledFromALinkedPort(t *testing.T) {
	var body = portSectionXML(
		map[string]string{"adminStatus": "1=1"},
		map[string]bool{"1": true}, "1")
	body = strings.Replace(body, "</Standard802_3List>",
		"<Entry><interfaceName>12</interfaceName><linkState>2</linkState>"+
			"<adminStatus>2</adminStatus></Entry></Standard802_3List>", 1)

	var v = parsePortAdminVocabulary(body, "GE")
	if v.Field != "adminStatus" {
		t.Fatalf("field = %q, want adminStatus", v.Field)
	}
	if v.Enabled != "1" || v.Disabled != "2" {
		t.Errorf("enabled/disabled = %q/%q — derived from the linked port, and this range "+
			"spells booleans 1/2", v.Enabled, v.Disabled)
	}
	if v.Admin["GE1"] != "1" || v.Admin["GE12"] != "2" {
		t.Errorf("per-port values = %v", v.Admin)
	}
	if !v.LinkUp["GE1"] || v.LinkUp["GE12"] {
		t.Errorf("link map = %v", v.LinkUp)
	}
}

// THE CAPTURED SHAPE — a raw GET of `wcd?{Standard802_3List}` on the lab 1930,
// 2026-08-11. Every port serves `adminState` 1 alongside `interfaceName`,
// `linkState`, `linkStateTrapEnabled`, `mediaType` and `speedAdmin`, and the
// values are this framework's `_TRUE='1'` / `_FALSE='2'`.
//
// TWO THINGS ARE PINNED HERE. That `adminState` is SELECTED at all — the entry
// this test replaces the premise of said "no capture shows one" — and that it
// WINS over `adminStatus`, the guess that used to lead the candidate list: a
// firmware serving both must be written in the vocabulary the hardware was
// observed using, not in the one that was reasoned about.
func TestPortAdminVocabularyPrefersTheCapturedField(t *testing.T) {
	var body = `<?xml version="1.0" encoding="UTF-8" ?><ResponseData>` +
		`<DeviceConfiguration><Standard802_3List type="section">` +
		`<Entry><interfaceName>1</interfaceName><linkState>1</linkState>` +
		`<linkStateTrapEnabled>1</linkStateTrapEnabled><mediaType>2</mediaType>` +
		`<speedAdmin>1</speedAdmin><adminState>1</adminState></Entry>` +
		`<Entry><interfaceName>12</interfaceName><linkState>2</linkState>` +
		`<linkStateTrapEnabled>1</linkStateTrapEnabled><mediaType>2</mediaType>` +
		`<speedAdmin>1</speedAdmin><adminState>1</adminState></Entry>` +
		`</Standard802_3List></DeviceConfiguration></ResponseData>`

	var v = parsePortAdminVocabulary(body, "GE")
	if v.Field != "adminState" {
		t.Fatalf("field = %q, want adminState — the name the hardware serves (%q)",
			v.Field, v.Why)
	}
	if v.Enabled != "1" || v.Disabled != "2" {
		t.Errorf("enabled/disabled = %q/%q, want 1/2 — _TRUE='1', _FALSE='2'",
			v.Enabled, v.Disabled)
	}
	if v.Admin["GE1"] != "1" || v.Admin["GE12"] != "1" {
		t.Errorf("per-port values = %v: the lab unit carries 1 on every port", v.Admin)
	}
	if !v.LinkUp["GE1"] || v.LinkUp["GE12"] {
		t.Errorf("link map = %v", v.LinkUp)
	}
	// The captured name must win over the old leading guess when both are served.
	var both = strings.ReplaceAll(body, "<adminState>",
		"<adminStatus>2</adminStatus><adminState>")
	if parsePortAdminVocabulary(both, "GE").Field != "adminState" {
		t.Errorf("a firmware serving both fields was written in %q, not the captured "+
			"adminState", parsePortAdminVocabulary(both, "GE").Field)
	}
}

// No port with a link means the enabled value cannot be OBSERVED — and guessing it
// is how a port gets shut on a range with no console.
func TestPortAdminVocabularyRefusesWithoutALinkedPort(t *testing.T) {
	var body = portSectionXML(
		map[string]string{"adminStatus": "12=1"}, map[string]bool{}, "12")
	var v = parsePortAdminVocabulary(body, "GE")
	if v.Field != "" {
		t.Fatalf("field = %q: with no linked port the vocabulary is unknown", v.Field)
	}
	if !strings.Contains(v.Why, "NO port with a link") {
		t.Errorf("the refusal must name what is missing: %q", v.Why)
	}
}

// A value outside the 1/2 pair cannot be inverted: the opposite is not knowable.
func TestPortAdminVocabularyRefusesAnUnknownValuePair(t *testing.T) {
	var body = portSectionXML(
		map[string]string{"adminStatus": "1=up"}, map[string]bool{"1": true}, "1")
	var v = parsePortAdminVocabulary(body, "GE")
	if v.Field != "" {
		t.Fatalf("field = %q: \"up\" is outside the 1/2 pair this range uses", v.Field)
	}
}

// An aggregate (TRK…) is not a physical port and must not appear.
func TestPortAdminVocabularySkipsAggregates(t *testing.T) {
	var body = `<ResponseData><DeviceConfiguration><Standard802_3List type="section">` +
		`<Entry><interfaceName>1</interfaceName><linkState>1</linkState>` +
		`<adminStatus>1</adminStatus></Entry>` +
		`<Entry><interfaceName>TRK1</interfaceName><linkState>1</linkState>` +
		`<adminStatus>1</adminStatus></Entry>` +
		`</Standard802_3List></DeviceConfiguration></ResponseData>`
	var v = parsePortAdminVocabulary(body, "GE")
	var name string
	for name = range v.Admin {
		if strings.Contains(name, "TRK") {
			t.Errorf("aggregate reported as a port: %q", name)
		}
	}
	if len(v.Admin) != 1 {
		t.Errorf("only the physical port must be reported: %v", v.Admin)
	}
}

func TestPortAdminEntryIsMinimal(t *testing.T) {
	var got = portAdminEntry("12", "adminStatus", "2")
	var want = "<Entry><interfaceName>12</interfaceName><adminStatus>2</adminStatus></Entry>"
	if got != want {
		t.Errorf("entry = %q, want %q", got, want)
	}
}

// THE GUARDRAIL, and the way it is STRICTER than ApplyPorts': shutting a port cuts
// every VLAN on it, so a port merely TAGGED in the management VLAN — an uplink — is
// protected too, where a membership change only had to protect the native VLAN.
func TestPortAdminGuardBlocksProtectsTaggedUplinksToo(t *testing.T) {
	var uplink = PortState{Mode: "trunk", PVID: 1, Tagged: []int{99}}
	var block, reason = portAdminGuardBlocks(uplink, 99, true, false)
	if !block {
		t.Fatal("a linked uplink tagged in the management VLAN must be spared")
	}
	if !strings.Contains(reason, "TAGGED") {
		t.Errorf("the refusal must say which membership fired: %q", reason)
	}

	var access = PortState{Mode: "access", PVID: 99}
	if block, _ = portAdminGuardBlocks(access, 99, true, false); !block {
		t.Error("a linked untagged member of the management VLAN must be spared")
	}
	if block, _ = portAdminGuardBlocks(access, 99, false, false); block {
		t.Error("no cable, no session: an unlinked port must not be spared")
	}
	if block, _ = portAdminGuardBlocks(access, 99, true, true); block {
		t.Error("Force must lift the refusal")
	}
	if block, _ = portAdminGuardBlocks(PortState{Mode: "access", PVID: 100}, 99, true, false); block {
		t.Error("a linked port outside the management VLAN is not the session's path")
	}
	// ZERO IS IGNORANCE on this range (driver/state.go), never permission.
	block, reason = portAdminGuardBlocks(PortState{Mode: "access", PVID: 100}, 0, true, false)
	if !block {
		t.Error("with no management VLAN reported, every LINKED port must be spared")
	}
	if !strings.Contains(reason, "management") {
		t.Errorf("the refusal must name what is unknown: %q", reason)
	}
}

// A KNOWN MANAGEMENT VLAN IS NOT ENOUGH. The link state and the administrative
// field come from Standard802_3List; the membership that decides whether this port
// carries the management VLAN comes from VLANInterfaceMembershipTable — a different
// GET, and the one wcd is known to answer with an empty 200 (apply.go's header).
// That leaves the port's PortState at its ZERO VALUE, and a zero value read as "in
// no VLAN, therefore not in the management one" would shut the uplink of a switch
// whose only way back is the reset button, on site.
//
// It cannot be a real answer either: every port on this range is untagged
// somewhere, VLAN 1 at worst. So it is ignorance, and it is spared.
func TestPortAdminGuardBlocksSparesAPortTheMembershipReadMissed(t *testing.T) {
	var unknown = PortState{}
	var block, reason = portAdminGuardBlocks(unknown, 99, true, false)
	if !block {
		t.Fatal("the management VLAN is known but this port's membership is not: a zero " +
			"PortState is an empty read, not a port outside every VLAN")
	}
	if !strings.Contains(reason, "NO VLAN at all") {
		t.Errorf("the refusal must name what is missing rather than blaming the VLAN: %q", reason)
	}
	// Still LINK-GATED, and Force still lifts it: this widens the refusal, it does
	// not turn the gesture off.
	if block, _ = portAdminGuardBlocks(unknown, 99, false, false); block {
		t.Error("no cable, no session: an unlinked port must not be spared for an unknown " +
			"membership either")
	}
	if block, _ = portAdminGuardBlocks(unknown, 99, true, true); block {
		t.Error("Force must lift this refusal like every other one")
	}
	// And a port the read DID cover is untouched by it.
	if block, _ = portAdminGuardBlocks(PortState{Mode: "access", PVID: 100}, 99, true, false); block {
		t.Error("a port with a known PVID outside the management VLAN must still be shuttable")
	}
}

// End to end against the package's OWN device fixture — whose port section carries
// no administrative field, exactly as the real captures do. Nothing may be POSTed,
// and every port asked for must come back in Skipped: a caller with an empty
// Skipped is entitled to believe the plan was carried out.
//
// THIS REFUSAL IS WHAT THE CAPABILITY DECLARES (capabilities.go, 2026-08-06). The
// connector announces "port_admin" without any capture of the field it writes, and
// what makes that honest rather than reckless is that a firmware publishing no
// known field gets this — nothing sent, every port reported, AND THE CANDIDATE
// NAMES SPELLED OUT. That last part is the survey: it is how the open question gets
// answered from a screen instead of a lab session, so it is asserted here rather
// than left to the wording of a comment.
func TestApplyPortAdminWritesNothingWhenTheFieldIsUnknown(t *testing.T) {
	var seen []captured
	var srv = newSwitchWithState(t, &seen, 1)

	var res, err = adminOn(srv).ApplyPortAdmin(context.Background(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{
			{Port: "GE12", Enabled: false},
			{Port: "GE13", Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("a refusal is not a Go error on this path: %v", err)
	}
	if len(writes(seen)) != 0 {
		t.Errorf("nothing may be sent when the administrative field is unknown: %+v", writes(seen))
	}
	if len(res.Skipped) != 2 {
		t.Errorf("both ports must be reported as not touched: %+v", res.Skipped)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("nothing was written: %+v", res.PortsWritten)
	}
	// The refusal must NAME what it looked for, on every port, and name the section
	// it looked in — a refusal that says only "not supported" teaches the next
	// firmware nothing.
	var s string
	for _, s = range res.Skipped {
		if !strings.Contains(s, sectionPorts) {
			t.Errorf("the refusal must name the section it read: %q", s)
		}
		var cand string
		for _, cand = range adminFieldCandidates {
			if !strings.Contains(s, cand) {
				t.Errorf("the refusal must name candidate %q — that list IS the survey this "+
					"gesture performs on an unknown firmware: %q", cand, s)
			}
		}
	}
}

// The DECLARATION, and it is declared on a PROBE rather than on a captured field
// (capabilities.go says on what terms). Asserted next to the refusal above, because
// the two only make sense together: api/portadmin_apply.go gates the gesture on
// this name, and the gesture it lets through is one that either finds its field in
// the device's own response or declines in writing.
//
// dhcp_snooping must stay false — not one section body has ever carried it, and
// dhcpsnoop_test.go guards that separately.
func TestWritableExtrasDeclaresPortAdmin(t *testing.T) {
	if !WritesExtra("port_admin") {
		t.Error("ApplyPortAdmin exists and the gesture was asked for fleet-wide: the API " +
			"layer would refuse what this connector implements")
	}
	if !(&Driver1930{}).WritesExtra("port_admin") {
		t.Error("the CONNECTOR form must answer the same as the package function: the API " +
			"layer asks the connector, before any session exists")
	}
	var name string
	for _, name = range []string{"poe", "dhcp_snooping", "port_security",
		"port_power_cycle"} {
		if WritesExtra(name) {
			t.Errorf("declares %q writable, and nothing in this package writes it", name)
		}
	}
}

// The same fixture, plus an administrative field: the linked port GE1 is untagged in
// the device's management VLAN (1) and must be SPARED, while the dark port GE12 is
// shut for real — and the write must carry the section list in the query string,
// without which this framework answers 200 with an empty body and changes nothing.
func TestApplyPortAdminSparesTheSessionPortAndWritesTheOther(t *testing.T) {
	var seen []captured
	var admin = map[string]string{"1": "1", "12": "1"}
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var body = readAll(r)
			seen = append(seen, captured{
				method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, body: body,
			})
			if r.Method != http.MethodGet {
				// The device applies the write, the way a firmware that publishes the
				// field would: this is what lets the re-read confirm.
				if strings.Contains(r.URL.RawQuery, "Standard802_3List") &&
					strings.Contains(body, "<interfaceName>12</interfaceName>") &&
					strings.Contains(body, "<adminStatus>2</adminStatus>") {
					admin["12"] = "2"
				}
				_, _ = w.Write([]byte(okResponse))
				return
			}
			switch {
			case strings.Contains(r.URL.RawQuery, "ManagementInterfaceGlobalSetting"):
				_, _ = w.Write([]byte(`<ResponseData><DeviceConfiguration>` +
					`<ManagementInterfaceGlobalSetting type="section"><managementVLAN>1` +
					`</managementVLAN></ManagementInterfaceGlobalSetting>` +
					`</DeviceConfiguration></ResponseData>`))
			case strings.Contains(r.URL.RawQuery, "VLANList"):
				_, _ = w.Write([]byte(`<ResponseData><DeviceConfiguration>` +
					`<VLANList type="section"><VLAN><VLANID>1</VLANID><VLANName></VLANName>` +
					`</VLAN></VLANList></DeviceConfiguration></ResponseData>`))
			case strings.Contains(r.URL.RawQuery, "Standard802_3List"):
				_, _ = w.Write([]byte(`<ResponseData><DeviceConfiguration>` +
					`<Standard802_3List type="section">` +
					`<Entry><interfaceName>1</interfaceName><linkState>1</linkState>` +
					`<adminStatus>` + admin["1"] + `</adminStatus></Entry>` +
					`<Entry><interfaceName>12</interfaceName><linkState>2</linkState>` +
					`<adminStatus>` + admin["12"] + `</adminStatus></Entry>` +
					`</Standard802_3List></DeviceConfiguration></ResponseData>`))
			default:
				_, _ = w.Write([]byte(membershipXML))
			}
		}))
	t.Cleanup(srv.Close)

	var res, err = adminOn(srv).ApplyPortAdmin(context.Background(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{
			{Port: "GE1", Enabled: false},
			{Port: "GE12", Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("ApplyPortAdmin: %v", err)
	}

	var w captured
	for _, w = range writes(seen) {
		if strings.Contains(w.body, "<interfaceName>1</interfaceName>") {
			t.Errorf("the port carrying administration was shut: %s", w.body)
		}
		if !strings.Contains(w.rawQuery, "{Standard802_3List}") {
			t.Errorf("the POST must carry the section list in its query string, or this "+
				"framework answers 200 with an empty body: %q", w.rawQuery)
		}
	}
	var sparedGE1 bool
	var s string
	for _, s = range res.Skipped {
		if strings.HasPrefix(s, "GE1 ") {
			sparedGE1 = true
		}
	}
	if !sparedGE1 {
		t.Errorf("GE1 must be reported as spared, with the reason: %+v", res.Skipped)
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != "GE12" {
		t.Errorf("GE12 has no cable and does not carry the session: it must be shut and "+
			"confirmed by the re-read, got %+v (skipped %+v)", res.PortsWritten, res.Skipped)
	}
}
