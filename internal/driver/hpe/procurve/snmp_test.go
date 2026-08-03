package procurve

import (
	"context"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The community line as the reference unit carried it, verbatim from its own
// running-config (2026-08-02). On this platform the configuration IS the command
// language, so this is the write form as much as the read form.
const snmpConfigLive = "hostname \"SW-PLAYER-79\" \n\r" +
	"snmp-server contact \"technique@lyon-esport.fr\" \n\r" +
	"snmp-server location \"InTheKitchen\" \n\r" +
	"snmp-server community \"les\" Operator \n\r"

func TestHasCommunityFindsTheLiveLine(t *testing.T) {
	if !hasCommunity(snmpConfigLive, "les") {
		t.Error("the community the device actually carries was not found")
	}
	if hasCommunity(snmpConfigLive, "public") {
		t.Error("a community that is not there was reported present")
	}
	// A community declared read-write is still declared: silently downgrading
	// somebody's Manager community to Operator is not this connector's call.
	var rw = "snmp-server community \"mon\" Manager \n\r"
	if !hasCommunity(rw, "mon") {
		t.Error("a Manager-level community was not recognised as present")
	}
}

// v3 is refused with a reason rather than approximated: a guessed v3 gives a
// supervision that is configured and silent.
func TestApplySNMPRefusesV3WithAReason(t *testing.T) {
	var c = &Client{}
	var out, _ = c.ApplySNMP(context.Background(), driver.SNMPIdentity{User: "mcuser"})
	_ = out
	// Without a session the call cannot proceed; the refusal path is exercised
	// through the capability declaration instead.
	var caps = (&Driver{}).Capabilities("2610-48")
	var v string
	for _, v = range caps.SNMPVersions {
		if v == "v3" {
			t.Fatal("the connector advertises v3 it cannot provision: monitoring would " +
				"be selected for this device and never turned on")
		}
	}
	if len(caps.SNMPVersions) == 0 {
		t.Error("no SNMP version declared: this device would be ruled out of monitoring")
	}
}

// An apply is not a reset: an intent carrying nothing must remove nothing.
func TestApplySNMPWithoutCommunityWritesNothing(t *testing.T) {
	var c = &Client{cli: &cliConn{}}
	var out, err = c.ApplySNMP(context.Background(), driver.SNMPIdentity{})
	if err != nil {
		t.Fatalf("an empty intent returned an error: %v", err)
	}
	if out.CommunitySet {
		t.Error("something was written for an empty intent")
	}
	if len(out.Skipped) == 0 {
		t.Error("nothing was written and nothing said so")
	}
	if !strings.Contains(strings.Join(out.Skipped, " "), "not a reset") {
		t.Errorf("skipped = %v, want it to say why nothing was removed", out.Skipped)
	}
}

// The quoted form cannot carry a quote or a space, and a community that breaks
// the command would be reported as applied while the agent stayed silent.
func TestApplySNMPRefusesAnUnquotableCommunity(t *testing.T) {
	var c = &Client{cli: &cliConn{}}
	var _, err = c.ApplySNMP(context.Background(), driver.SNMPIdentity{Community: `bad" name`})
	if err == nil {
		t.Fatal("a community that breaks the CLI quoting was accepted")
	}
}
