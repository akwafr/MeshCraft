package eos

import "testing"

// The capture of 2026-08-13 on core01.lab (EOS 4.28.13.1M), verbatim —
// including the MULTICAST table whose rows share the unicast shape and must
// not become clients.
const showMACAddressTableCapture = `show mac address-table
          Mac Address Table
------------------------------------------------------------------

Vlan    Mac Address       Type        Ports      Moves   Last Move
----    -----------       ----        -----      -----   ---------
   1    04f4.1c1d.7458    DYNAMIC     Et48       1       0:05:40 ago
   1    bcc4.93e5.7f9c    DYNAMIC     Et34       1       13:13:34 ago
  99    649d.9916.3410    DYNAMIC     Et48       1       13:13:27 ago
  99    9c05.d6f5.f137    DYNAMIC     Et37       1       13:13:33 ago
Total Mac Addresses for this criterion: 4

          Multicast Mac Address Table
------------------------------------------------------------------

Vlan    Mac Address       Type        Ports
----    -----------       ----        -----
   1    0100.5e00.0001    STATIC      Et1
Total Mac Addresses for this criterion: 1

core01-lab>`

func TestParseShowMACAddressTable(t *testing.T) {
	var obs = parseShowMACAddressTable(showMACAddressTableCapture)
	if len(obs.Entries) != 4 {
		t.Fatalf("entries = %d, want 4 (the DYNAMIC unicast rows, and nothing "+
			"from the multicast table)", len(obs.Entries))
	}
	var first = obs.Entries[0]
	if first.MAC != "04:f4:1c:1d:74:58" {
		t.Errorf("MAC = %q, want canonical colon spelling of the dotted form", first.MAC)
	}
	if first.VLAN != 1 || first.LocalPort != "ET48" {
		t.Errorf("entry = %+v, want VLAN 1 on ET48 (standard-model name, not Et48)", first)
	}
	// The STATIC multicast group must be absent even though its row matches
	// the column shape: STATIC is not learned.
	var i int
	for i = range obs.Entries {
		if obs.Entries[i].MAC == "01:00:5e:00:00:01" {
			t.Error("the multicast STATIC row became a client")
		}
	}
}

func TestDottedMACRejectsWrongShapes(t *testing.T) {
	if dottedMACToCanonical("not.a.mac") != "" {
		t.Error("garbage accepted")
	}
	if dottedMACToCanonical("04f4.1c1d.74") != "" {
		t.Error("short form accepted")
	}
}
