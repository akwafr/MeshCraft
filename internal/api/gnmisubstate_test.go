package api

import (
	"strings"
	"testing"

	"meshcraft/internal/tunnel"
)

// What this file tests: the controller half of "a gNMI subscription that cannot
// authenticate must not read as healthy".
//
// Two things, and they are separable on purpose:
//
//   - the SCOPE check. The event's DeviceID is chosen by the PROXY, and these
//     two event types write a line into a site's fleet log naming a credential
//     problem. Left ungated, a proxy could put that line on another site's
//     device — a false alarm and a disclosure that the device exists at all. The
//     gate is the same one every other collection fact goes through, and this
//     verifies it actually applies to the new types rather than that they were
//     added to the switch and forgotten in the table;
//   - the SENTENCE. `gnmiCausePhrase` is where the distinction the transport
//     worked to preserve finally becomes something an operator can act on, and
//     it is pure so it can be read here without a database.
//
// No database, like the rest of the package: what a test here cannot cover is
// the write itself (`applyGNMISubState` needs a Store), and that is stated
// rather than pretended — cf. the header of proxyscope_test.go, which draws the
// same line for the three write paths that preceded this one.

// gnmiScopeConfig: a proxy that collects one device over gNMI and one over SNMP,
// pings a third, and has never heard of a fourth.
//
// The gNMI device is the point: `poll` is built from cfg.SNMP UNION cfg.GNMI, and
// a version of that union that forgot the second list would refuse every fact
// about a device collected over gNMI — which is to say it would refuse exactly
// the events this batch adds, and the feature would fail silently in the way the
// incident already taught us not to accept.
func gnmiScopeConfig() tunnel.WatchConfig {
	return tunnel.WatchConfig{
		Targets: []tunnel.WatchTarget{
			{DeviceID: "dev-core01", IP: "192.0.2.5", Name: "core01.lab"},
			{DeviceID: "sw-a1", IP: "10.0.0.1"},
			{DeviceID: "sw-a2", IP: "10.0.0.2"},
		},
		SNMP: []tunnel.SNMPWatch{
			{DeviceID: "sw-a1", IP: "10.0.0.1", Community: "s3cr3t"},
		},
		GNMI: []tunnel.GNMIWatch{
			{DeviceID: "dev-core01", IP: "192.0.2.5", Username: "mcadmin",
				Password: "no-business-being-compared", CountersIntervalS: 5},
		},
	}
}

// TestGNMIEventScopeRefusesAForeignDevice: a proxy may report a broken
// subscription only for a device it was actually told to subscribe to.
//
// The three refusals are checked separately because they are not repaired the
// same way and do not weigh the same — a proxy naming a device from another site
// is the case the whole check exists to catch, while one naming a device it only
// pings has merely overstepped.
func TestGNMIEventScopeRefusesAForeignDevice(t *testing.T) {
	var s = &Server{proxyScopes: newProxyScopes()}
	s.proxyScopes.record("px-a", gnmiScopeConfig(), scopeT0, nil)

	var evType string
	for _, evType = range []string{tunnel.EventGNMIFailing, tunnel.EventGNMIRecovered} {
		var need, gated = authorityFor(evType)
		if !gated {
			t.Fatalf("%s passes the scope check ungated: a proxy could write this "+
				"line on any site's device", evType)
		}
		// The device this proxy actually subscribes to: accepted.
		if !s.allowProxyFact("px-a", need, "dev-core01") {
			t.Errorf("%s refused for the device this proxy collects over gNMI — the "+
				"authority set must be cfg.SNMP UNION cfg.GNMI", evType)
		}
		// Pinged but not collected: it holds no subscription to sw-a2 and cannot
		// have watched one fail.
		if s.allowProxyFact("px-a", need, "sw-a2") {
			t.Errorf("%s accepted for a device this proxy only PINGS", evType)
		}
		// Another site entirely. THE refusal.
		if s.allowProxyFact("px-a", need, "sw-b1") {
			t.Errorf("%s accepted for a device NEVER entrusted to this proxy: it "+
				"could name a credential problem on someone else's fleet log", evType)
		}
		// And a proxy nothing was ever pushed to.
		if s.allowProxyFact("px-never-seen", need, "dev-core01") {
			t.Errorf("%s accepted from a proxy entrusted with nothing", evType)
		}
	}

	// The verdicts must stay DISTINCT, since they send a reader to different
	// places: an attachment to fix, versus a proxy running someone else's
	// configuration.
	var sc = scopeOf(t, s.proxyScopes, "px-a")
	if judgeProxyScope(sc, true, authorityPoll, "sw-a2") != scopeNotCollected {
		t.Error("a pinged-only device must refuse as pinged_not_collected")
	}
	if judgeProxyScope(sc, true, authorityPoll, "sw-b1") != scopeNotEntrusted {
		t.Error("a foreign device must refuse as never_entrusted")
	}
}

// TestGNMICausePhraseNamesTheGesture: the two causes read differently, and the
// unknown one does not accuse anybody.
//
// This is where the distinction the transport preserved is finally spent. If the
// `auth` sentence did not name the credential, an operator reading
// "authentication failed" about a switch they can log into by hand would go and
// look at the switch — the credential is a separate site_credentials row holding
// its own copy of the admin password, and nobody guesses that on their own.
func TestGNMICausePhraseNamesTheGesture(t *testing.T) {
	var auth = gnmiCausePhrase(tunnel.CauseAuth)
	if !strings.Contains(auth, "credential") {
		t.Errorf("the auth phrase must send the reader to the credential: %q", auth)
	}
	if !strings.Contains(strings.ToLower(auth), "rotat") {
		t.Errorf("the auth phrase must name the rotation that causes it: %q", auth)
	}

	var unreach = gnmiCausePhrase(tunnel.CauseUnreachable)
	if unreach == auth {
		t.Fatal("a refused login and a silent device must not read the same: they " +
			"are repaired by different people")
	}
	if strings.Contains(strings.ToLower(unreach), "credential") {
		t.Errorf("the unreachable phrase must not send anyone to a credential: %q",
			unreach)
	}

	// The honest default. It must not be empty — a cause meshcraft could not
	// classify still deserves a sentence — and must not accuse a credential.
	var unknown = gnmiCausePhrase(tunnel.CauseUnknown)
	if unknown == "" {
		t.Fatal("an unclassified failure must still say something")
	}
	if strings.Contains(strings.ToLower(unknown), "credential") {
		t.Errorf("uncertainty must not be rounded up into an accusation: %q", unknown)
	}
	// A cause from a proxy newer than this controller reads as the default rather
	// than as silence.
	if gnmiCausePhrase("some-new-class") != unknown {
		t.Error("an unrecognised cause must fall to the honest default")
	}
}
