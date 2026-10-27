package procurve

import (
	"context"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The HTTP title below is VERBATIM from the reference unit, read through a
// meshcraft proxy on 2026-08-02:
//
//	SW-PLAYER-79 -
//	    ProCurve Switch 2610-48 (J9088A)
//
// It is kept with its line break and indentation because that is what the device
// serves, and a fingerprint that only matches the tidied-up version matches
// nothing in the field.
const liveHTTPTitle = "SW-PLAYER-79 - \n      ProCurve Switch 2610-48 (J9088A)"

const liveHTTPServer = "eHTTP v2.0"

func TestFingerprintRecognisesTheReferenceUnit(t *testing.T) {
	var d = &Driver{}
	var score = d.Fingerprint(driver.Evidence{
		HTTPTitle: liveHTTPTitle, HTTPServer: liveHTTPServer,
		OpenPorts: []int{22, 23, 80},
	})
	if score < 80 {
		t.Fatalf("score = %d on the exact evidence the device serves, want a confident match", score)
	}
}

// The part number alone is enough, and should be: it names one product where the
// brand name covers two decades of switches with three different CLIs.
func TestFingerprintOnPartNumberAlone(t *testing.T) {
	var d = &Driver{}
	var got = d.Fingerprint(driver.Evidence{SysDescr: "J9088A"})
	if got < 80 {
		t.Errorf("score on the part number = %d, want a confident match", got)
	}
}

// THE CLAIM THIS MUST NOT MAKE. An HPE switch outside this range must not be
// filed as a 2610, because being filed here is what would later apply this
// command set to it.
func TestFingerprintIsCautiousOutsideTheRange(t *testing.T) {
	var d = &Driver{}
	var score = d.Fingerprint(driver.Evidence{
		SysDescr: "ProCurve Switch 5406zl (J8697A)",
	})
	if score >= 80 {
		t.Errorf("a 5400 scored %d: this connector would claim hardware it cannot drive", score)
	}
	if score == 0 {
		t.Error("a ProCurve should still score something — filing it as an unknown HPE is useful")
	}
}

func TestFingerprintIgnoresOtherVendors(t *testing.T) {
	var d = &Driver{}
	var cases = []string{
		"CSS326-24G-2S+ SwOS v2.17",
		"Cisco IOS Software, C2960X Software",
		"S3900-24T4S",
		"HPE Networking Instant On 1930 48G",
	}
	var s string
	for _, s = range cases {
		var got = d.Fingerprint(driver.Evidence{SysDescr: s})
		if got != 0 {
			t.Errorf("claimed %q with score %d", s, got)
		}
	}
}

// The capability that will bite a real deployment: this range holds 256 VLANs,
// not 4094. Declaring the ID space as the count would move that discovery to the
// moment a write fails.
func TestCapabilitiesStateTheVlanCeiling(t *testing.T) {
	var caps = (&Driver{}).Capabilities("2610-48")
	if caps.MaxVlans != 256 {
		t.Errorf("MaxVlans = %d, want the range's real ceiling of 256", caps.MaxVlans)
	}
	if caps.VlanRange != [2]int{1, 4094} {
		t.Errorf("VlanRange = %v, want the full 802.1Q id space", caps.VlanRange)
	}
}

// It can join an MST region, unlike SwOS. That is what decides whether a site
// running MSTP can include this switch or has to treat it as a boundary.
func TestCapabilitiesDeclareMSTP(t *testing.T) {
	var caps = (&Driver{}).Capabilities("2610-48")
	var found bool
	var m string
	for _, m = range caps.STPModes {
		if m == "mstp" {
			found = true
		}
	}
	if !found {
		t.Errorf("STPModes = %v, want mstp among them", caps.STPModes)
	}
}

// Nothing is claimed as verified. The whole package rests on the vendor's
// documentation plus one identification, and the UI has to be able to say so.
func TestResetProcedureIsNotClaimedAsConfirmed(t *testing.T) {
	var rp = (&Driver{}).ResetProcedure("2610-48")
	if rp.Confirmed {
		t.Error("the reset procedure claims to be confirmed and has never been run")
	}
	if !strings.Contains(strings.ToLower(rp.Source), "not captured") {
		t.Errorf("Source = %q, want it to say the procedure was not captured", rp.Source)
	}
	if rp.Hardware == "" {
		t.Error("no hardware gesture: this is what an operator reads in front of a rack")
	}
	// The trap that costs a configuration: Clear alone resets only passwords.
	var joined = strings.Join(rp.Pitfalls, " ")
	if !strings.Contains(joined, "Clear") {
		t.Error("the Clear-versus-Clear+Reset trap is not warned about")
	}
}

// Writes need a session, and say so rather than pretending.
func TestWritesRefuseWithoutASession(t *testing.T) {
	var c = &Client{}
	var _, err = c.ApplyPorts(context.Background(), PortPlan{})
	if err == nil {
		t.Fatal("ApplyPorts reported success with no session open")
	}
	err = c.SetSecret(context.Background(), "old", "new")
	if err == nil {
		t.Fatal("SetSecret reported success with no session open")
	}
}

// An empty secret would leave the account open on a switch whose other door is
// telnet across the management VLAN.
func TestSetSecretRefusesAnEmptySecret(t *testing.T) {
	var c = &Client{User: "manager"}
	var err = c.SetSecret(context.Background(), "old", "   ")
	if err == nil {
		t.Fatal("an empty secret was accepted")
	}
	if !strings.Contains(err.Error(), "empty secret") {
		t.Errorf("error = %v, want it to name the refusal", err)
	}
}

// WritesExtra is a PROMISE that an apply carries that configuration. "stp" is
// kept in step with ApplySpanningTree existing and "port_admin" with
// ApplyPortAdmin (declared 2026-08-06, on a DEDUCED "enable" verb — contract.go
// says on what terms); everything else must stay undeclared until something
// writes it, because a name here that resolves to nothing reports a configuration
// as applied that never arrived.
func TestDriverDeclaresOnlyTheExtrasItWrites(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("stp") {
		t.Error("ApplySpanningTree exists and \"stp\" is not declared: the UI would " +
			"never offer the gesture this connector implements")
	}
	if !d.WritesExtra("port_admin") {
		t.Error("ApplyPortAdmin exists and the gesture was asked for fleet-wide: " +
			"api/portadmin_apply.go gates on this name and would refuse it")
	}
	var name string
	for _, name = range []string{"poe", "dhcp_snooping", "port_security", "lldp",
		"port_power_cycle"} {
		if d.WritesExtra(name) {
			t.Errorf("declares %q writable, and nothing writes it", name)
		}
	}
}
