package eos

// The "port_admin" capture, and the harness that would finish the job.
//
// # What was captured, and what it does NOT cover
//
// CAPTURED ON HARDWARE — core01.lab, DCS-7050SX-64-R, EOS 4.28.13.1M,
// 192.0.2.5, 2026-08-10, over the controller's SOCKS route as `mcadmin`.
// Ethernet1 is a genuine dark port: "show interfaces status" reports it
// `notconnect` with `Not Present` in the Type column — no transceiver is fitted
// at all — so "shutdown" there cuts nothing whatever the cabling does. The
// port was shut from `configure` / `interface Ethernet1` / `shutdown` / `end`,
// read back, then reverted with "no shutdown" and read back again. Running
// configuration only; nothing was copied to startup.
//
// That capture settles the WORD, which is the thing this connector's verify path
// keys on and the thing no fixture could establish: EOS prints "disabled" for a
// port carrying "shutdown", and it prints it in the same column, with the same
// short port spelling (`Et1`, never `Ethernet1`), that the dark port printed
// "notconnect" into a minute earlier.
//
// IT WAS TYPED AT A CLI. It was not driven through [Client.ApplyPortAdmin], and
// TestLiveWritePortAdmin below is what closed that gap — RUN on 2026-08-10
// against the same core01 with EOS_LIVE_WRITE=1 and EOS_LIVE_SCRATCH_PORT=ET1.
// Every leg passed: DOWN through the applier (PortsWritten=[ET1], nothing
// skipped), an independent re-read agreeing on "disabled", a second identical
// plan writing nothing, and UP again leaving the port at "notconnect". The
// applier's plan/verify path is no longer fake-CLI only. contract.go carries the
// same statement where the flag is flipped, and names the one branch that stays
// fake-CLI only: the management-VLAN refusal, which needs a LINKED port and so
// cannot be proven without shutting the session's own jack.
//
// # Running the harness
//
//	EOS_LIVE_HOST=192.0.2.5 EOS_LIVE_USER=… EOS_LIVE_PASS=… EOS_LIVE_WRITE=1 \
//	EOS_LIVE_SOCKS=127.0.0.1:1080 EOS_LIVE_SOCKS_USER=… EOS_LIVE_SOCKS_PASS=… \
//	EOS_LIVE_SCRATCH_PORT=ET1 \
//	go test ./internal/driver/arista/eos -run TestLiveWritePortAdmin -v
//
// With EOS_LIVE_HOST unset the whole file skips, which is how CI sees it. With
// EOS_LIVE_HOST set and EOS_LIVE_WRITE unset it opens the session, performs the
// read-only reconnaissance and the port refusals, and skips before writing.

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

// --- the capture, verbatim -----------------------------------------------

// paCapturedBefore is Ethernet1 as the switch printed it BEFORE the shutdown:
// a dark cage, no transceiver, no link, and NOT administratively down.
const paCapturedBefore = "Port       Name   Status       Vlan     Duplex Speed  Type         Flags Encapsulation\n" +
	"Et1               notconnect   trunk    full   10G    Not Present\n"

// paCapturedAfter is the SAME port after "shutdown", and the one word in it
// that this connector's verify path depends on.
const paCapturedAfter = "Port       Name   Status       Vlan     Duplex Speed  Type         Flags Encapsulation\n" +
	"Et1               disabled     trunk    full   10G    Not Present\n"

// TestCapturedPortAdminWordsAreReadApart is the assertion the capture was taken
// for.
//
// "notconnect" and "disabled" are both "no link", they arrive in the same
// column of the same table, and one of them means "nobody plugged anything in"
// while the other means "an operator shut this port". [statusPort.adminDown] is
// the only thing in this package that tells them apart, and until 2026-08-10
// both words were only ever read from a fixture this repository wrote itself.
//
// Conflating them is not a cosmetic error. ApplyPortAdmin's re-read (portadmin.go
// § "The re-read is what says the ports moved") decides PortsWritten on this
// predicate alone: a parser that read "notconnect" as administratively down
// would report a dark port as successfully shut without a command ever having
// been accepted, and one that read "disabled" as merely unplugged would report
// every successful shutdown as unconfirmed.
func TestCapturedPortAdminWordsAreReadApart(t *testing.T) {
	var before = parseInterfacesStatus(paCapturedBefore)
	if len(before) != 1 {
		t.Fatalf("the captured BEFORE table parsed to %d faceplate port(s), want 1: %+v",
			len(before), before)
	}
	// The status column spells the port short. A reader that only knew
	// "Ethernet1" would drop this row and report the port absent from the
	// device, which ApplyPortAdmin turns into a skip rather than a write.
	if before[0].Name != "ET1" || before[0].CLIName != "Ethernet1" {
		t.Errorf("\"Et1\" parsed to name=%q cliname=%q, want ET1/Ethernet1",
			before[0].Name, before[0].CLIName)
	}
	if before[0].Status != "notconnect" {
		t.Errorf("captured BEFORE status = %q, want \"notconnect\"", before[0].Status)
	}
	if before[0].adminDown() {
		t.Error("the captured \"notconnect\" line reads as ADMINISTRATIVELY DOWN — a dark " +
			"cage with no transceiver would be reported as successfully shut, and " +
			"ApplyPortAdmin would confirm a write it never made")
	}
	if before[0].LinkUp {
		t.Error("the captured \"notconnect\" line reads as LINKED — the guardrail spares " +
			"linked ports, so this would spare every dark port from being shut at all")
	}
	if before[0].errDisabled() {
		t.Error("\"notconnect\" read as errdisabled: the operator would be told the switch " +
			"itself is holding a port down that simply has nothing plugged into it")
	}

	var after = parseInterfacesStatus(paCapturedAfter)
	if len(after) != 1 {
		t.Fatalf("the captured AFTER table parsed to %d faceplate port(s), want 1: %+v",
			len(after), after)
	}
	if after[0].Status != "disabled" {
		t.Errorf("captured AFTER status = %q, want \"disabled\"", after[0].Status)
	}
	if !after[0].adminDown() {
		t.Error("the captured \"disabled\" line — the real output of a real \"shutdown\" on " +
			"a real 7050SX — does NOT read as administratively down. This is the word the " +
			"whole gesture verifies against")
	}
	if after[0].errDisabled() {
		t.Error("\"disabled\" read as errdisabled: an operator shutdown would be reported " +
			"as the SWITCH refusing the port, and the note would send them hunting a cause " +
			"that does not exist")
	}
	if eosLinkState(after[0].Status) != driver.LinkDisabled {
		t.Errorf("eosLinkState(%q) = %q, want %q", after[0].Status,
			eosLinkState(after[0].Status), driver.LinkDisabled)
	}
	if eosLinkState(before[0].Status) != driver.LinkUnplugged {
		t.Errorf("eosLinkState(%q) = %q, want %q", before[0].Status,
			eosLinkState(before[0].Status), driver.LinkUnplugged)
	}

	// The two captures differ in ONE word and must not agree on the predicate.
	if before[0].adminDown() == after[0].adminDown() {
		t.Fatal("the before and after captures read the same administrative state: the " +
			"shutdown that happened between them is invisible to this connector")
	}
}

// --- the live harness ----------------------------------------------------

// paLiveTarget builds the target from the environment, SOCKS route included.
// Shape borrowed from stormcontrol_live_test.go, which shares its socksDialer.
func paLiveTarget(t *testing.T, host string) driver.Target {
	t.Helper()
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("EOS_LIVE_SOCKS")
	if sa == "" {
		return tgt
	}
	var auth *proxy.Auth
	var u = os.Getenv("EOS_LIVE_SOCKS_USER")
	if u != "" {
		auth = &proxy.Auth{User: u, Password: os.Getenv("EOS_LIVE_SOCKS_PASS")}
	}
	var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
	if derr != nil {
		t.Fatalf("socks: %v", derr)
	}
	tgt.Dialer = socksDialer{d: d}
	return tgt
}

// paReadStatus re-reads "show interfaces status" and keys it by standard name,
// exactly as ApplyPortAdmin does.
func paReadStatus(ctx context.Context, t *testing.T, c *Client) map[string]statusPort {
	t.Helper()
	var out, err = c.cli.run(ctx, "show interfaces status", cmdTimeout)
	if err != nil && out == "" {
		t.Fatalf("show interfaces status: %v", err)
	}
	var byName = map[string]statusPort{}
	var p statusPort
	for _, p = range parseInterfacesStatus(out) {
		byName[p.Name] = p
	}
	return byName
}

// paEmptyCage answers "is there a transceiver in this port at all?" from the
// Type column of "show interfaces status".
//
// EOS prints "Not Present" there for a cage with nothing fitted — not "no
// link", not "link down", but no optic and no copper module in the slot. That is
// a PHYSICAL fact and it outranks every configuration one: whatever VLAN list
// the running configuration allows on such a port, no frame has ever entered or
// left it, so "shutdown" there cannot interrupt anybody — including this
// session.
//
// It exists because the configuration test alone made this harness unrunnable on
// the very range it was written for. On core01.lab every one of the 48
// faceplate ports carries "switchport mode trunk" with
// "switchport trunk allowed vlan 1,42,98-100", and 99 is the management VLAN —
// so carriesMgmtVLAN is true for ALL of them and paPickScratchPort refused the
// entire faceplate. That is the normal shape of an Arista, not a peculiarity of
// this lab: a trunk that allows the management VLAN is what an uplink and an
// unused port look like alike.
func paEmptyCage(p statusPort) bool {
	return strings.EqualFold(strings.TrimSpace(p.Type), "Not Present")
}

// paPickScratchPort chooses a port that is safe to shut, from the device's own
// state and never from an assumption.
//
// FOUR conditions, and each one has a failure behind it:
//
//   - no LINK — a port with a link is carrying somebody's traffic, and on the
//     unlucky one it is carrying this very session;
//   - not the management VLAN WHILE A TRANSCEIVER IS FITTED — see below;
//   - not already "disabled" — the DOWN leg would then write nothing, and the
//     UP leg at the end would leave the port in a state the lab did not start
//     in, which is a silent change to somebody else's switch;
//   - not "errdisabled" — the switch is holding that port down itself, and
//     "no shutdown" at the end would not restore what was found.
//
// # Why the management-VLAN test is qualified and the applier's is not
//
// ApplyPortAdmin's guardrail (portadmin.go, "THE GUARDRAIL") fires on the
// CONJUNCTION: a port is spared when it has a LIVE LINK *and* carries the
// management VLAN. This harness used to apply the VLAN half on its own, on the
// stated principle that it must not lean on the code it is proving. The
// principle is right and the implementation was strictly stronger than the
// applier's — so strong that on the lab 7050SX it refused all 48 ports and the
// exercise could never run at all. A safety check that no device can satisfy
// does not protect anything; it just leaves the applier unproven, which is the
// state this whole file exists to end.
//
// So the extra strictness is kept where it can bite and dropped where it cannot:
// a port carrying the management VLAN is still refused outright when a
// transceiver is FITTED — link-down modules come back up, and this harness will
// not gamble on when — and admitted only when the cage is EMPTY, which is a
// physical observation ([paEmptyCage]) and not a re-derivation of the applier's
// logic. Ethernet1 on core01 is such a cage, and it is the port the 2026-08-10
// hand capture used for exactly this reason.
//
// Lowest port number first, deterministically: on this range Ethernet1 is the
// dark cage the 2026-08-10 capture used.
func paPickScratchPort(status map[string]statusPort, rc runConfig, mgmtVLAN int) (string, []string) {
	var names []string
	var name string
	for name = range status {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return stdPortLess(names[i], names[j]) })

	var why []string
	for _, name = range names {
		var p = status[name]
		if p.LinkUp {
			why = append(why, name+": live link")
			continue
		}
		if carriesMgmtVLAN(name, rc, mgmtVLAN) && !paEmptyCage(p) {
			why = append(why, name+": carries the management VLAN with a transceiver fitted ("+
				p.Type+")")
			continue
		}
		if p.adminDown() {
			why = append(why, name+": already shut, so the exercise would not restore it")
			continue
		}
		if p.errDisabled() {
			why = append(why, name+": errdisabled, so \"no shutdown\" would not restore it")
			continue
		}
		return name, why
	}
	return "", why
}

// TestLiveWritePortAdmin drives [Client.ApplyPortAdmin] DOWN and back UP against
// a real EOS switch, and asserts the port ends where it started.
//
// # Why this is the test that matters, and not the capture
//
// The 2026-08-10 capture (top of this file) proved the FORMS: "shutdown" is
// accepted, and "show interfaces status" answers "disabled". It was typed at
// a CLI. Nothing in it exercised the applier — not the three pre-write reads,
// not the management-VLAN guardrail, not the "already where the plan wants it"
// short-circuit, and above all not the re-read that decides PortsWritten. The
// EdgeSwitch case is the precedent: a hand-sent command was mistaken for a proven
// connector for most of a day.
//
// # Why the revert is deferred
//
// Leaving a lab port shut is the failure mode that costs somebody a trip to the
// site. The revert is armed BEFORE the first write and runs whatever the
// assertions do, and it is sent as raw configuration rather than through the
// applier — the applier is the thing under test, and a revert that depends on it
// is no revert at all when it is the applier that broke.
func TestLiveWritePortAdmin(t *testing.T) {
	var host = os.Getenv("EOS_LIVE_HOST")
	if host == "" {
		t.Skip("EOS_LIVE_HOST not set — live port-admin exercise skipped")
	}
	var tgt = paLiveTarget(t, host)
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("EOS_LIVE_USER"), Secret: os.Getenv("EOS_LIVE_PASS"),
		Why: "live port-admin exercise",
	}})
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v — transcript %+v", err, adm.Transcript())
	}
	defer c.Close()

	// --- 0) Read first, and choose from what the device says.
	var runOut string
	runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		t.Fatalf("show running-config: %v — the management VLAN cannot be read, so no "+
			"port can be declared safe to shut", err)
	}
	var rc = parseRunningConfig(runOut, c.host)
	if rc.MgmtVLAN <= 0 {
		t.Fatalf("no SVI on %s carries an address that is administratively up: meshcraft "+
			"cannot tell which VLAN administration arrives on, and this harness refuses to "+
			"shut a port it cannot clear of carrying the session", host)
	}
	if len(rc.Ports) == 0 {
		t.Fatalf("\"show running-config\" announced VLAN %d as the management VLAN and "+
			"carried NO interface block: that is a truncated read, not a device, and the "+
			"membership test below would wave every port through", rc.MgmtVLAN)
	}
	var before = paReadStatus(ctx, t, c)
	if len(before) == 0 {
		t.Fatal("\"show interfaces status\" reported no faceplate port at all")
	}
	t.Logf("device: mgmt VLAN %d, %d faceplate port(s) reported, %d interface block(s)",
		rc.MgmtVLAN, len(before), len(rc.Ports))

	// --- 1) The scratch port, and the refusals. These are Fatalf and not Skip:
	// a lab with no safe port is something to be told about, loudly.
	var port = os.Getenv("EOS_LIVE_SCRATCH_PORT")
	var rejected []string
	if port == "" {
		port, rejected = paPickScratchPort(before, rc, rc.MgmtVLAN)
		if port == "" {
			t.Fatalf("no safe scratch port on %s — every port was refused: %s",
				host, strings.Join(rejected, "; "))
		}
		t.Logf("auto-picked %s; refused %s", port, strings.Join(rejected, "; "))
	}
	var prior, known = before[port]
	if !known {
		t.Fatalf("%s is not reported by \"show interfaces status\" on this device — "+
			"refusing to write to a port meshcraft cannot read back", port)
	}
	if prior.LinkUp {
		t.Fatalf("%s has a LIVE LINK — refusing to shut it. Something is plugged into it, "+
			"and on the unlucky port that something is this session", port)
	}
	if carriesMgmtVLAN(port, rc, rc.MgmtVLAN) && !paEmptyCage(prior) {
		t.Fatalf("%s carries the management VLAN %d AND has a transceiver fitted (%q) — "+
			"refusing to shut it. The way back would be the console port or a cabled "+
			"Management1, on site", port, rc.MgmtVLAN, prior.Type)
	}
	if carriesMgmtVLAN(port, rc, rc.MgmtVLAN) {
		t.Logf("%s is in the management VLAN's allowed list, and its cage is EMPTY (Type %q): "+
			"no transceiver is fitted, so nothing has ever passed through it and \"shutdown\" "+
			"here interrupts nobody (cf. paEmptyCage)", port, prior.Type)
	}
	if prior.adminDown() {
		t.Fatalf("%s already reads \"disabled\" — refusing to use a port that is ALREADY "+
			"shut as scratch, because bringing it up at the end would leave this switch in "+
			"a state it did not start in", port)
	}
	if prior.errDisabled() {
		t.Fatalf("%s reads \"errdisabled\": the switch is holding it down itself, and "+
			"\"no shutdown\" at the end would not put it back where it was found", port)
	}
	t.Logf("scratch port %s (status %q, type %q, no link, and safe to shut)",
		port, prior.Status, prior.Type)

	if os.Getenv("EOS_LIVE_WRITE") != "1" {
		t.Skipf("EOS_LIVE_WRITE not set — reconnaissance done (%s is a safe scratch port), "+
			"nothing written", port)
	}

	// --- 2) THE REVERT, armed before the first write.
	//
	// Raw configuration on purpose (cf. the doc comment): "no shutdown" is
	// idempotent on a port that was never shut, so this is also correct when the
	// apply below failed halfway or never ran.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var rerr = c.runConfigBlock(rctx, []string{
			"interface " + cliPortName(port), "no shutdown", "exit",
		})
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s MAY STILL BE SHUT on %s. Clear it by hand: "+
				"configure / interface %s / no shutdown", rerr, port, host, cliPortName(port))
			return
		}
		var back = paReadStatus(rctx, t, c)
		var p, ok = back[port]
		if !ok {
			t.Errorf("REVERT NOT VERIFIED: %s is absent from the re-read", port)
			return
		}
		if p.adminDown() {
			t.Errorf("REVERT INCOMPLETE: %s still reads \"disabled\" on %s. Clear it by "+
				"hand", port, host)
			return
		}
		if p.Status != prior.Status {
			t.Errorf("%s reads %q after the revert, having read %q before the exercise",
				port, p.Status, prior.Status)
			return
		}
		t.Logf("revert: verified, %s back to %q", port, p.Status)
	}()

	// --- 3) DOWN, through the applier.
	var res ApplyResult
	res, err = c.ApplyPortAdmin(ctx, driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: port, Enabled: false}},
	})
	if err != nil {
		t.Fatalf("ApplyPortAdmin (down): %v — transcript %+v", err, c.Transcript())
	}
	t.Logf("down: written=%v skipped=%v notes=%v", res.PortsWritten, res.Skipped, res.Notes)
	if len(res.Skipped) > 0 {
		t.Errorf("the port was chosen to be safe and the applier refused it anyway: %v",
			res.Skipped)
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != port {
		t.Errorf("PortsWritten = %v, want [%s] — the applier's own re-read is what fills "+
			"this, so an empty one says it could not confirm the port moved", res.PortsWritten, port)
	}

	// --- 4) The device must AGREE, read independently of the applier's own
	// verification. "disabled" is the word, and the capture at the top of this
	// file is where it comes from.
	var mid = paReadStatus(ctx, t, c)
	var now, seen = mid[port]
	if !seen {
		t.Fatalf("%s vanished from \"show interfaces status\" after the shutdown", port)
	}
	if !now.adminDown() {
		t.Errorf("%s reads %q after \"shutdown\", want \"disabled\"", port, now.Status)
	} else {
		t.Logf("%s reads \"disabled\" — the applier drove the gesture the 2026-08-10 "+
			"capture only typed", port)
	}

	// --- 5) A SECOND identical plan must write nothing: the port is already
	// where the plan wants it, and a connector that rewrote it would flap a port
	// on every automatic pass.
	var again ApplyResult
	again, err = c.ApplyPortAdmin(ctx, driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: port, Enabled: false}},
	})
	if err != nil {
		t.Fatalf("ApplyPortAdmin (second down): %v", err)
	}
	if len(again.PortsWritten) > 0 {
		t.Errorf("a second identical plan rewrote %v — the apply does not converge",
			again.PortsWritten)
	}

	// --- 6) UP again, through the applier, and back where it started.
	res, err = c.ApplyPortAdmin(ctx, driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: port, Enabled: true}},
	})
	if err != nil {
		t.Fatalf("ApplyPortAdmin (up): %v — transcript %+v", err, c.Transcript())
	}
	t.Logf("up: written=%v skipped=%v notes=%v", res.PortsWritten, res.Skipped, res.Notes)
	if len(res.Skipped) > 0 {
		t.Errorf("bringing a port back up cuts nothing and must never be refused: %v",
			res.Skipped)
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != port {
		t.Errorf("PortsWritten = %v on the way back up, want [%s]", res.PortsWritten, port)
	}

	var after = paReadStatus(ctx, t, c)
	var end, there = after[port]
	if !there {
		t.Fatalf("%s vanished from \"show interfaces status\" after \"no shutdown\"", port)
	}
	if end.adminDown() {
		t.Errorf("%s still reads \"disabled\" after \"no shutdown\"", port)
	}
	if end.Status != prior.Status {
		t.Errorf("%s reads %q at the end, having read %q at the start — the exercise did "+
			"not put this switch back where it found it", port, end.Status, prior.Status)
	} else {
		t.Logf("%s back to %q, where the exercise found it", port, end.Status)
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}
