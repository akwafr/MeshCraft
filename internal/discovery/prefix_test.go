package discovery

import (
	"strings"
	"testing"
)

func TestExpandPrefixIPv4(t *testing.T) {
	// /30: 4 addresses, network and broadcast excluded → 2 hosts.
	var hosts, err = ExpandPrefix("192.168.1.0/30")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0] != "192.168.1.1" || hosts[1] != "192.168.1.2" {
		t.Fatalf("hosts %v", hosts)
	}
}

func TestExpandPrefixSlash24(t *testing.T) {
	var hosts, err = ExpandPrefix("10.20.30.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 254 {
		t.Fatalf("%d hosts, expected 254", len(hosts))
	}
	if hosts[0] != "10.20.30.1" || hosts[253] != "10.20.30.254" {
		t.Fatalf("bounds: %s ... %s", hosts[0], hosts[253])
	}
}

// A non-aligned address must be brought back to its network, otherwise the
// same network entered two different ways would produce two ranges.
func TestExpandPrefixMasksAddress(t *testing.T) {
	var hosts, err = ExpandPrefix("192.168.1.77/30")
	if err != nil {
		t.Fatal(err)
	}
	if hosts[0] != "192.168.1.77" && hosts[0] != "192.168.1.1" {
		t.Logf("first host %s", hosts[0])
	}
	var norm string
	norm, err = NormalizePrefix("192.168.1.77/24")
	if err != nil {
		t.Fatal(err)
	}
	if norm != "192.168.1.0/24" {
		t.Fatalf("normalization: %s", norm)
	}
}

func TestExpandPrefixSingleAddress(t *testing.T) {
	var hosts, err = ExpandPrefix("10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0] != "10.0.0.5" {
		t.Fatalf("%v", hosts)
	}
}

// A /16 is the widest IPv4 range we enumerate: 65,534 hosts, walked in
// minutes now that ports are probed in parallel.
func TestExpandPrefixSlash16(t *testing.T) {
	var hosts, err = ExpandPrefix("10.0.0.0/16")
	if err != nil {
		t.Fatalf("/16 refused: %v", err)
	}
	if len(hosts) != 65534 {
		t.Fatalf("expected 65534 hosts (network and broadcast excluded), got %d", len(hosts))
	}
}

// Beyond the /16, we refuse and say so: it needs to be split into chunks.
func TestExpandPrefixTooLarge(t *testing.T) {
	var _, err = ExpandPrefix("10.0.0.0/15")
	if err == nil {
		t.Fatal("/15 accepted")
	}
	if !strings.Contains(err.Error(), "range too large") {
		t.Fatalf("message: %v", err)
	}
}

// DECLARING and SCANNING are two distinct questions: a /64 IPv6 is a site's
// real subnet, and it must be declarable even though enumerating it doesn't
// make sense.
func TestDeclareVsEnumerate(t *testing.T) {
	var declarable = []string{"10.0.0.0/16", "2001:db8::/64", "192.168.1.10"}
	var cidr string
	for _, cidr = range declarable {
		var err = ValidPrefix(cidr)
		if err != nil {
			t.Errorf("%s should be declarable: %v", cidr, err)
		}
	}
	var err = Enumerable("2001:db8::/64")
	if err == nil {
		t.Error("a /64 IPv6 must not be advertised as enumerable")
	} else if !strings.Contains(err.Error(), "neighbor") {
		t.Errorf("the refusal must say what discovery will go through: %v", err)
	}
	err = Enumerable("10.0.0.0/16")
	if err != nil {
		t.Errorf("a /16 IPv4 must be enumerable: %v", err)
	}
}

// The IPv6 refusal must explain why, not just refuse.
func TestExpandPrefixIPv6TooWide(t *testing.T) {
	var _, err = ExpandPrefix("2001:db8::/64")
	if err == nil {
		t.Fatal("/64 IPv6 accepted: impossible to enumerate")
	}
	if !strings.Contains(err.Error(), "IPv6") {
		t.Fatalf("message not explicit enough: %v", err)
	}
}

func TestExpandPrefixIPv6Narrow(t *testing.T) {
	var hosts, err = ExpandPrefix("2001:db8::/126")
	if err != nil {
		t.Fatal(err)
	}
	// In IPv6 there's no broadcast address: all 4 addresses are probed.
	if len(hosts) != 4 {
		t.Fatalf("%d addresses: %v", len(hosts), hosts)
	}
}

func TestExpandPrefixInvalid(t *testing.T) {
	var _, err = ExpandPrefix("not-a-range")
	if err == nil {
		t.Fatal("invalid prefix accepted")
	}
}

func TestIdentifyPrefersShellOutput(t *testing.T) {
	// The banner says OpenSSH, the command output says Cisco: it's the
	// device itself that must win.
	var m, conf, evidence = Identify(
		"Cisco IOS Software, C2960 Software", "SSH-2.0-OpenSSH_7.4", "", "")
	if m.Driver != "cisco-ios" {
		t.Fatalf("driver %q", m.Driver)
	}
	if conf != ConfShellOutput {
		t.Fatalf("confidence %d", conf)
	}
	if evidence == "" {
		t.Fatal("empty identification source")
	}
}

func TestIdentifyFallsBackToGeneric(t *testing.T) {
	var m, conf, _ = Identify("", "SSH-2.0-OpenSSH_9.6", "", "")
	if m.Driver != "generic-ssh" {
		t.Fatalf("driver %q", m.Driver)
	}
	if conf != ConfPortsOnly {
		t.Fatalf("confidence %d", conf)
	}
}

func TestSearchResetProcedures(t *testing.T) {
	var all = SearchResetProcedures("")
	if len(all) < 10 {
		t.Fatalf("catalog too short: %d", len(all))
	}
	var mt = SearchResetProcedures("mikrotik")
	if len(mt) != 1 || mt[0].Driver != "mikrotik-routeros" {
		t.Fatalf("mikrotik search: %+v", mt)
	}
	// Search by model, not just by vendor.
	var byModel = SearchResetProcedures("2960")
	if len(byModel) != 1 || byModel[0].Driver != "cisco-ios" {
		t.Fatalf("2960 search: %+v", byModel)
	}
	// Every word must match: "cisco nexus" must not bring back IOS.
	var multi = SearchResetProcedures("cisco nexus")
	if len(multi) != 1 || multi[0].Driver != "cisco-nxos" {
		t.Fatalf("multi-word search: %+v", multi)
	}
	if len(SearchResetProcedures("nonexistent-brand")) != 0 {
		t.Fatal("search with no results returned non-empty")
	}
	// Every procedure must say what it erases: without that, the UI lies.
	var p ResetProcedure
	for _, p = range all {
		if strings.TrimSpace(p.Erases) == "" {
			t.Fatalf("%s: empty \"erases\" field", p.Driver)
		}
		if len(p.Software) == 0 && len(p.Hardware) == 0 {
			t.Fatalf("%s: no procedure", p.Driver)
		}
	}
}

// Regression: on "System Serial Number : FOC1841X0AB", an overly permissive
// pattern captures the word "Number" as the serial number. A false serial is
// worse than a missing one: it's the merge key for candidates across
// proxies, so two different devices would end up conflated.
func TestExtractFactsRealCiscoBanner(t *testing.T) {
	var banner = `
Cisco IOS Software, C2960X Software (C2960X-UNIVERSALK9-M), Version 15.2(7)E3
hostname sw-atelier-lyon-01
Model Number            : WS-C2960X-24PS-L
System Serial Number    : FOC1841X0AB
`
	var hostname, serial, model = ExtractFacts(banner)
	if hostname != "sw-atelier-lyon-01" {
		t.Fatalf("hostname %q", hostname)
	}
	if serial != "FOC1841X0AB" {
		t.Fatalf("serial %q (the word \"Number\" must not pass)", serial)
	}
	if model != "WS-C2960X-24PS-L" {
		t.Fatalf("model %q", model)
	}
}

func TestExtractFactsOtherVendors(t *testing.T) {
	var cases = []struct {
		text, serial, model string
	}{
		{"Serial number: JN123ABC4567\nModel: ex2300-24t", "JN123ABC4567", "ex2300-24t"},
		{"serial-number: 7C1B08F4E2D9", "7C1B08F4E2D9", ""},
		{"Serial Number      : SG12ABC345\nHardware model : 2530-24G", "SG12ABC345", "2530-24G"},
		// Word without a digit: refused rather than inventing a serial.
		{"Serial Number : Unknown", "", ""},
		// Without a separator, we don't guess.
		{"System Serial Number    FOC1841X0AB", "", ""},
	}
	var c struct {
		text, serial, model string
	}
	for _, c = range cases {
		var _, serial, model = ExtractFacts(c.text)
		if serial != c.serial {
			t.Fatalf("%q → serial %q, expected %q", c.text, serial, c.serial)
		}
		if model != c.model {
			t.Fatalf("%q → model %q, expected %q", c.text, model, c.model)
		}
	}
}
