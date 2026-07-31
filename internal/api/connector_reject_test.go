package api

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/driver/instanton"
	"meshcraft/internal/driver/mikrotik/routeros"
	"meshcraft/internal/driver/mikrotik/swos"
	"meshcraft/internal/model"
	"meshcraft/internal/snmpprov"
)

// THE CENTRAL INVARIANT OF THIS BATCH: a gesture addressed to a device
// whose connector doesn't know how to do it is REFUSED BEFORE EMITTING A
// SINGLE BYTE toward it.
//
// # Why this file exists
//
// This is the OBSERVED defect that motivated the whole project, and it's
// worth recalling in full because it's the only benchmark that matters: the
// API layer was written against the Instant On connector's types and
// called `instanton.OpenAdmin` directly. Adopting two MikroTik switches on
// a site therefore sent an SNMP push IN INSTANT ON PROTOCOL toward one of
// them \u2014 `https://10.99.0.54/`, connection refused since that device
// doesn't listen on 443, `snmp.apply_failed` at severity 3. Nothing was
// written because TCP failed. The gesture itself had indeed gone out, and
// that's what must be made impossible.
//
// Without a test, this defect WILL COME BACK: it's invisible on a
// single-vendor fleet, it breaks no compilation, and its symptom \u2014 a
// transport error \u2014 sends you looking for the fault on the device.
//
// # What "no byte" means here, and how it's proven
//
// The three connectors only reach a device through the [driver.Dialer]
// they're given. A dialer that COUNTS its calls and always fails is
// therefore an exact measurement: if the counter stays at zero, no
// connection was attempted, hence no byte could have gone out. This is
// stronger than a test of the error message \u2014 a refusal can be
// correctly worded AFTER having hit the device, and that's precisely the
// behavior we refuse.
//
// # Without a database, like the rest of the package
//
// The decisions verified here were written to be made on DECLARED facts
// \u2014 type assertion, capabilities, connector registry \u2014 and not on
// a device response. That's exactly what makes them testable without
// PostgreSQL, and it's not a coincidence: a decision that would require
// reaching the device to be made could not, by construction, be made
// beforehand.

// rejectDialer counts connection attempts and refuses all of them.
//
// `atomic` because a connector is allowed to be concurrent: an
// unsynchronized counter would make the test falsely pass under `-race`.
type rejectDialer struct{ calls atomic.Int64 }

func (d *rejectDialer) DialContext(_ context.Context, _, addr string) (net.Conn, error) {
	d.calls.Add(1)
	return nil, &net.OpError{Op: "dial", Net: "tcp",
		Err: errRejectDial{addr: addr}}
}

type errRejectDial struct{ addr string }

func (e errRejectDial) Error() string {
	return "NO connection must go out to " + e.addr + ": an unsupported " +
		"gesture is refused before the first byte"
}

// TestCapabilityRefusalEmitsNoByte: the heart of this file.
//
// For each of the three shipped connectors, we build an admin access \u2014
// which emits nothing, none of the three talk to the device at
// construction \u2014 then we query EVERY optional capability the API layer
// offers. Each answer must match what the connector declares in its
// `contract.go`, and the dialer's counter must stay at ZERO.
//
// The table is written by hand rather than derived from `contract.go`'s
// assertions: it's the CALLER'S POINT OF VIEW that's locked down here. If a
// connector loses a capability, this test says so with the name of the
// gesture the screen will stop offering \u2014 where a compile-time
// assertion would only say a signature moved.
func TestCapabilityRefusalEmitsNoByte(t *testing.T) {
	var d = &rejectDialer{}

	// The three accesses, built WITHOUT reaching the device. The address
	// is the MikroTik's from the incident, precisely because that's where
	// the faulty push had gone out in Instant On protocol.
	var cases = []struct {
		connector string
		adm       driver.Admin
		// What the platform can do, as seen from the API layer.
		accounts, persist, snmp, traps, mgmt, raw bool
	}{
		{
			// Everything is implemented: this is the range that was used
			// to design the contract.
			connector: "aruba-instant-on-1930",
			adm:       instanton.OpenAdmin("10.99.0.54", d),
			accounts:  true, persist: true, snmp: true, traps: true, mgmt: true, raw: true,
		},
		{
			// No Persister (`immediate`: nothing to save). MgmtMover is
			// implemented (dual-path; OutOfBand recovers on this platform).
			connector: "mikrotik-routeros",
			adm:       routeros.Open("10.99.0.54", d, "mcadmin", "secret"),
			accounts:  true, persist: false, snmp: true, traps: true, mgmt: true, raw: true,
		},
		{
			// Five absences, and they are platform FACTS, not delays: no
			// notion of an account, no trap-destination field, nothing to
			// persist, only one management address at a time.
			connector: "mikrotik-swos",
			adm:       swos.OpenAdmin("10.99.0.36", "admin", "", d),
			accounts:  false, persist: false, snmp: true, traps: false, mgmt: false, raw: true,
		},
	}

	var c int
	for c = range cases {
		var k = cases[c]
		var _, ok = k.adm.(driver.AccountManager)
		if ok != k.accounts {
			t.Errorf("%s: AccountManager = %v, expected %v \u2014 the screen must stop "+
				"offering account creation when this is false", k.connector, ok, k.accounts)
		}
		if _, ok = k.adm.(driver.Persister); ok != k.persist {
			t.Errorf("%s: Persister = %v, expected %v", k.connector, ok, k.persist)
		}
		if _, ok = k.adm.(driver.SNMPConfigurator); ok != k.snmp {
			t.Errorf("%s: SNMPConfigurator = %v, expected %v", k.connector, ok, k.snmp)
		}
		if _, ok = k.adm.(driver.TrapPusher); ok != k.traps {
			t.Errorf("%s: TrapPusher = %v, expected %v", k.connector, ok, k.traps)
		}
		if _, ok = k.adm.(driver.MgmtMover); ok != k.mgmt {
			t.Errorf("%s: MgmtMover = %v, expected %v", k.connector, ok, k.mgmt)
		}
		if _, ok = k.adm.(driver.RawReader); ok != k.raw {
			t.Errorf("%s: RawReader = %v, expected %v", k.connector, ok, k.raw)
		}
	}

	// THE MEASUREMENT THAT MATTERS. Building three accesses and querying
	// eighteen capabilities attempted not a single connection: the API
	// layer's refusals are therefore indeed made on declared facts, not
	// by trying.
	if d.calls.Load() != 0 {
		t.Fatalf("%d connection attempt(s) to DECIDE capabilities: an unsupported "+
			"gesture must be refused before the first byte", d.calls.Load())
	}
}

// TestMgmtMoveRefusalNamesThePlatform: arbitration E, as seen from the
// screen.
//
// The management VLAN move becomes Instant-On only, EXPLICITLY. Nothing
// regresses \u2014 it never worked elsewhere \u2014 but the absence becomes
// VISIBLE instead of presenting itself as a transport failure. What this
// test locks down is the one thing that makes this refusal useful: the
// sentence must name the connector AND say whether there is anything to
// expect, otherwise it sends people looking for a checkbox that doesn't
// exist.
func TestMgmtMoveRefusalNamesThePlatform(t *testing.T) {
	var d = &rejectDialer{}
	var conn, err = driver.ConnectorFor("mikrotik-swos")
	if err != nil {
		t.Fatalf("the SwOS connector must be in this binary: %v", err)
	}
	var adm = swos.OpenAdmin("10.99.0.36", "admin", "", d)

	var _, canMove = mgmtMoverOf(conn, adm)
	if canMove {
		t.Fatal("SwOS carries only one management address: the contract's " +
			"three-step move isn't tenable on it, and declaring it would jump " +
			"in without looking")
	}
	var refusal = mgmtMoverRefusal(conn)
	if !strings.Contains(refusal, "mikrotik-swos") {
		t.Errorf("the refusal must name the connector, otherwise the screen talks about "+
			"a platform nobody identifies: %q", refusal)
	}
	if !strings.Contains(refusal, "two addresses") {
		t.Errorf("the refusal must say WHY it's hopeless \u2014 the platform does not "+
			"carry two addresses at once: %q", refusal)
	}
	if d.calls.Load() != 0 {
		t.Errorf("%d connection(s) attempted to refuse an absent capability",
			d.calls.Load())
	}

	// And the reverse: on ranges that CAN, the same path must say yes.
	// A test that only checked the refusal would still pass the day
	// nothing is offered anymore.
	var io, ioErr = driver.ConnectorFor("aruba-instant-on-1930")
	if ioErr != nil {
		t.Fatalf("the Instant On connector must be in this binary: %v", ioErr)
	}
	var _, ioCanMove = mgmtMoverOf(io, instanton.OpenAdmin("10.99.200.101", d))
	if !ioCanMove {
		t.Error("the Instant On range can move its management addressing: " +
			"it's the one that was used to write this contract")
	}
	var ros, rosErr = driver.ConnectorFor("mikrotik-routeros")
	if rosErr != nil {
		t.Fatalf("the RouterOS connector must be in this binary: %v", rosErr)
	}
	var _, rosCanMove = mgmtMoverOf(ros, routeros.Open("10.99.0.54", d, "mcadmin", "secret"))
	if !rosCanMove {
		t.Error("RouterOS implements dual-path MgmtMover (OutOfBand recovers)")
	}
}

// TestTrapRefusalBeforeAnyContact: the refusal is made on the CAPABILITY,
// hence before having an access \u2014 hence before having reached the
// device.
//
// This is the strongest form of the invariant, and the only one the
// contract makes possible for traps: `Monitoring.Traps` is declared by the
// connector and read without opening anything. On SwOS it's false, and
// that's ESTABLISHED by enumeration \u2014 its complete web interface fits
// in a 44,807-byte file that defines all its tabs, and none carries a
// destination field.
//
// The stakes are concrete: without this refusal, meshcraft would write a
// line into a device that would never use it AND would have port 162/udp
// opened on the proxy to listen to silence.
func TestTrapRefusalBeforeAnyContact(t *testing.T) {
	var swosDrv, known = driver.Get("mikrotik-swos")
	if !known {
		t.Fatal("the SwOS connector must be registered")
	}
	var caps = swosDrv.Capabilities("CSS326-24G-2S+")
	if caps.Monitoring.Traps {
		t.Fatal("SwOS has no trap-destination field: declaring it would " +
			"write into the void and open a port nobody feeds")
	}

	// The whole decision, replayed exactly as trapDestToPose makes it:
	// declared and covering collector, v2c plan, community in the vault
	// \u2014 everything is gathered, and it's the only push capability
	// missing.
	var in = trapDestInputs{
		deviceName: "sw-css-1", mgmtIP: "10.99.0.36",
		collector: "agence-a", collectorName: "Branch A",
		plan:         snmpprov.Plan{Version: "v2c", Kind: snmpprov.KindV2c},
		credentialID: "cred-1",
		trapsPush:    caps.Monitoring.Traps,
	}
	var verdict, _ = judgeTrapDest(in, couvrantsSiteA)
	if verdict != trapDestNoTrapPush {
		t.Fatalf("verdict %v: a platform with no destination field must be "+
			"discarded BEFORE we reach it", verdict)
	}
	var refusal = trapDestRefusal(verdict, in)
	if !strings.Contains(refusal, "polling") {
		t.Errorf("the refusal must say what remains \u2014 monitoring by polling, "+
			"hence correct but not reactive: %q", refusal)
	}
}

// TestSNMPRefusalOnVersionlessPlatform: the door the founding fault came
// through, closed where it opened.
//
// The INVARIANT is unchanged: a platform that declares NO SNMP version
// produces no supervised plan, so `snmpIdentityToPose` returns false and
// the push stops at `errNothingToPose` \u2014 without a dialer, without
// transport, without a single packet. This is the first step of the
// gesture that, one day, went out in Instant On protocol toward a
// MikroTik.
//
// THE SUBJECT, HOWEVER, HAS CHANGED, and it's worth saying why rather than
// letting someone restore it. This test used to take SwOS as its example,
// relying on the fact that "no SNMP request has ever received a response
// from this platform." That is no longer true: the v2c agent of the
// reference CSS326-24G-2S+ answered on July 29, 2026 (sysDescr,
// sysObjectID, sysName, ifNumber), so the connector now declares `v2c`, and
// SwOS has become the wrong example \u2014 it is now supervised.
//
// Replacing it with another connector from the repo would have reproduced
// the fragility: every connector eventually gets its capabilities surveyed.
// BARE capabilities hold the invariant without depending on the state of a
// survey, and that's also the real case they describe \u2014 a device whose
// connector knows nothing.
func TestSNMPRefusalOnVersionlessPlatform(t *testing.T) {
	// No version declared: the case of a connector that couldn't
	// establish anything, and that of any device no connector recognizes.
	var plan = snmpprov.For(driver.Capabilities{}, snmpprov.DefaultPolicy())
	if plan.Supervised() {
		t.Fatal("no SNMP version declared: there must be no plan to push")
	}
	if plan.Reason == "" {
		t.Error("an unsupervised device must say why, otherwise the screen " +
			"presents a blank as a failure")
	}
}

// TestSwOSNowSupervisedInV2c: the counterpart of the test above, on the
// survey that made it change subject.
//
// It's not enough to remove SwOS from the previous example: what changed
// deserves to be locked down, because it's a NEW capability and losing it
// would be silent \u2014 a CSS326 would simply stop being collected, with
// no error anywhere.
//
// "NOT compliant" is the EXPECTED verdict and not a defect: this platform
// has no v3 at all, the default policy has a floor, and the gap is what the
// product must DISPLAY rather than hide. A compliant plan here would be the
// sign of a bug.
func TestSwOSNowSupervisedInV2c(t *testing.T) {
	var swosDrv, known = driver.Get("mikrotik-swos")
	if !known {
		t.Fatal("the SwOS connector must be registered")
	}
	var plan = snmpprov.For(swosDrv.Capabilities("CSS326-24G-2S+"), snmpprov.DefaultPolicy())
	if !plan.Supervised() {
		t.Fatalf("SwOS must be supervised in v2c since the July 29, 2026 survey: %q",
			plan.Reason)
	}
	if plan.Kind != snmpprov.KindV2c {
		t.Errorf("kind = %q, expected v2c: it's the only version surveyed, and the only "+
			"one the proxy's collector uses", plan.Kind)
	}
	if plan.Conform {
		t.Error("the plan must NOT be announced compliant: SwOS has no SNMPv3 at all, " +
			"the gap to the policy floor is real and must be visible on screen")
	}
}

// TestResolvedConnectorIsIndeedTheDevices: the most direct anti-regression
// for the founding fault.
//
// Three names, three connectors, and none must fall back to the historical
// connector. The fourth case is the one that was missing: a device WITHOUT
// a connector \u2014 entered by hand, or one discovery failed to classify
// \u2014 must NOT be handled by whichever one happens to be there, it must
// be refused with a sentence.
func TestResolvedConnectorIsIndeedTheDevices(t *testing.T) {
	var names = []string{"aruba-instant-on-1930", "mikrotik-routeros", "mikrotik-swos"}
	var n string
	for _, n = range names {
		var conn, err = driver.ConnectorFor(n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if conn.Info().Name != n {
			t.Errorf("resolving %q \u2192 connector %q: a gesture would go out in the "+
				"protocol of another platform", n, conn.Info().Name)
		}
	}

	// No connector associated: refusal, and the message must say the
	// repair GESTURE (redo a discovery), not just observe.
	var _, empty = driver.ConnectorFor("")
	if empty == nil {
		t.Fatal("a device without a connector must not be handled by whichever " +
			"connector happens to be there")
	}
	if !strings.Contains(empty.Error(), "discovery") {
		t.Errorf("the refusal must say what to do: %q", empty.Error())
	}

	// Connector unknown to this binary: this is the symptom of a version
	// rollback, and there is nothing to do on the device. The sentence
	// must say so.
	var _, unknown = driver.ConnectorFor("cisco-catalyst-9300")
	if unknown == nil {
		t.Fatal("a connector name absent from this binary must be refused")
	}
	if !strings.Contains(unknown.Error(), "controller") {
		t.Errorf("the refusal must point at the binary, not the device: %q",
			unknown.Error())
	}
}

// TestOutOfBandChangesTheSentenceNotTheVerdict: arbitration C.
//
// The move refusal used to state "this range has neither SSH nor a serial
// port: the only recourse would be a factory reset." True of the two
// web-only ranges, FALSE of RouterOS, which keeps a serial console AND
// MAC-winbox (both surveyed). A message that overstates severity gets
// learned to be ignored, and the day it's true, nobody listens to it
// anymore.
//
// What's locked down: the VERDICT does not change ("absent" in both cases
// \u2014 a fallback path does not make the maneuver acceptable), only the
// sentence changes.
func TestOutOfBandChangesTheSentenceNotTheVerdict(t *testing.T) {
	var d = deviceObserving(t,
		map[string]driver.PortState{"GE1": {Mode: "trunk", Tagged: []int{99}}},
		map[string]bool{}, // no active link: the path is ABSENT
	)

	var stateWithout, reasonWithout = mgmtPathOf(d, planTagged, false)
	var stateWith, reasonWith = mgmtPathOf(d, planTagged, true)
	if stateWithout != "absent" || stateWith != "absent" {
		t.Fatalf("a fallback path does not make the move acceptable: %s / %s",
			stateWithout, stateWith)
	}
	if reasonWithout == reasonWith {
		t.Fatal("the two platforms are not repaired at the same cost: the sentence must " +
			"differ")
	}
	if !strings.Contains(reasonWithout, "factory reset") {
		t.Errorf("without a fallback path, the real cost is the factory reset: %q", reasonWithout)
	}
	if strings.Contains(reasonWith, "factory reset") {
		t.Errorf("with a serial console, promising a factory reset is WRONG: %q",
			reasonWith)
	}
	if !strings.Contains(reasonWith, "OUT OF BAND") {
		t.Errorf("the fallback path must be named to be useful: %q", reasonWith)
	}
}

// TestOutOfBandComesFromTheDevicesConnector: the value isn't guessed, it's
// read from the connector discovery designated \u2014 and FALSE when
// unknown.
//
// The defect to prevent is precise: assuming a fallback path that doesn't
// exist would make us attempt an irreversible maneuver on a device we
// thought recoverable. An unknown capability is therefore assumed ABSENT,
// here as everywhere.
func TestOutOfBandComesFromTheDevicesConnector(t *testing.T) {
	var cases = []struct {
		extra string
		want  bool
		why   string
	}{
		{`{"driver":"mikrotik-routeros"}`, true,
			"serial console AND MAC-winbox, both surveyed"},
		{`{"driver":"aruba-instant-on-1930"}`, false,
			"no SSH, no Telnet, no serial port: the reset button is the only recourse"},
		{`{"driver":"mikrotik-swos"}`, false,
			"only one TCP port responds on the reference device, port 80"},
		{`{"driver":"cisco-catalyst-9300"}`, false,
			"connector unknown to this binary: assumed without a fallback path"},
		{`{}`, false, "no connector: assumed without a fallback path"},
		{`not json`, false, "unreadable extra: assumed without a fallback path"},
	}
	var i int
	for i = range cases {
		var got = outOfBandOf(model.Device{Extra: []byte(cases[i].extra)})
		if got != cases[i].want {
			t.Errorf("%s \u2192 %v, expected %v (%s)", cases[i].extra, got, cases[i].want, cases[i].why)
		}
	}
}
