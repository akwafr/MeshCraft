package s3400

// Tests for the trap destination (trap.go).
//
// The device below is a FAKE. Nothing about its rendering of "snmp-server
// host" was captured — that is precisely the deduction trap.go declares — so
// what these tests establish is NOT that an S3400 behaves this way. They
// establish that the applier's ORDER, its refusals, its idempotence and above
// all its verifying re-read hold, and that the ONE captured pathology of this
// range is handled: a removal in the short form is answered "Incomplete
// command" and LEAVES THE HOST DECLARED.

import (
	"context"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// --- the fake device -------------------------------------------------------

// fakeHost is one "snmp-server host" entry the device carries.
//
// `raw` exists so a test can seed a line whose RENDERING this connector cannot
// spell in full — a legacy entry printed without its version token. That is the
// only way to reach trapRemoveLine's refusal, and it is the case the captured
// pathology is about.
type fakeHost struct {
	host      string
	version   string
	community string
	raw       string // printed verbatim when set
}

func (h fakeHost) render() string {
	if h.raw != "" {
		return h.raw
	}
	return "snmp-server host " + h.host + " version " + h.version + " " + h.community
}

type trapDevice struct {
	out chan byte

	// sent is every line the connector wrote, in order.
	sent []string

	communities []string
	hosts       []fakeHost

	// swallowHost: the host line is ACCEPTED and configures nothing — the
	// silent-success shape the verifying re-read exists to catch.
	swallowHost bool
	// swallowRemoval: the FULL removal form is accepted with no error and the
	// host stays declared. The purest form of the false success this range
	// produces, modelled without the "Incomplete command" that normally
	// accompanies it.
	swallowRemoval bool
	// refuseSave: "write" is refused.
	refuseSave bool
	// dieOnRead: the session dies on the Nth "show running-config" (1-based).
	// 0 = never.
	dieOnRead int

	saved int
	reads int
	dead  bool

	line  strings.Builder
	stack []string
}

func newTrapDevice() (*Client, *trapDevice) {
	var d = &trapDevice{
		out:         make(chan byte, 1<<20),
		communities: []string{"mcpoll"},
	}
	var conn = &cliConn{out: d.out, stdin: d}
	var c = &Client{cli: conn, host: "192.0.2.3", known: true, privileged: true}
	return c, d
}

func (d *trapDevice) Write(p []byte) (int, error) {
	var i int
	for i = 0; i < len(p); i++ {
		if p[i] == '\r' || p[i] == '\n' {
			var l = d.line.String()
			d.line.Reset()
			d.handle(l)
			continue
		}
		d.line.WriteByte(p[i])
	}
	return len(p), nil
}

func (d *trapDevice) Close() error { return nil }

func (d *trapDevice) handle(raw string) {
	var cmd = strings.TrimSpace(raw)
	d.sent = append(d.sent, cmd)
	if d.dead {
		return
	}
	switch {
	case cmd == "":
		d.reply(cmd, "")
	case cmd == "config":
		d.stack = append(d.stack, "config")
		d.reply(cmd, "")
	case cmd == "exit":
		if len(d.stack) > 0 {
			d.stack = d.stack[:len(d.stack)-1]
		}
		d.reply(cmd, "")
	case cmd == "show running-config":
		d.reads++
		if d.dieOnRead > 0 && d.reads >= d.dieOnRead {
			d.dead = true
			close(d.out)
			return
		}
		d.reply(cmd, d.runningConfig())
	case cmd == "write":
		if d.refuseSave {
			d.replyUnknown(cmd)
			return
		}
		d.saved++
		d.reply(cmd, "OK!")
	case strings.HasPrefix(cmd, "no snmp-server host"):
		d.removeHost(cmd)
	case strings.HasPrefix(cmd, "snmp-server host"):
		d.addHost(cmd)
	default:
		d.replyUnknown(cmd)
	}
}

// addHost takes "snmp-server host <ip> version <v> <community>" and nothing
// else: the captured form has version BEFORE the community, so a line built the
// Cisco way ("snmp-server host <ip> <community>") is refused here, which is
// what pins the correction the 2026-08-10 survey made.
func (d *trapDevice) addHost(cmd string) {
	var f = strings.Fields(cmd)
	if len(f) != 6 || !strings.EqualFold(f[3], "version") {
		d.replyUnknown(cmd)
		return
	}
	if !d.swallowHost {
		d.hosts = append(d.hosts, fakeHost{host: f[2], version: f[4], community: f[5]})
	}
	d.reply(cmd, "")
}

// removeHost is THE CAPTURED PATHOLOGY, modelled: only the FULL form removes.
// The short one answers "Incomplete command" — which reCLIError catches — and
// the host STAYS DECLARED.
func (d *trapDevice) removeHost(cmd string) {
	var f = strings.Fields(cmd)
	if len(f) != 7 || !strings.EqualFold(f[4], "version") {
		d.emit(cmd + "\n^\nIncomplete command\n" + d.prompt())
		return
	}
	if d.swallowRemoval {
		d.reply(cmd, "")
		return
	}
	var kept []fakeHost
	var h fakeHost
	for _, h = range d.hosts {
		if h.host == f[3] && h.version == f[5] && h.community == f[6] {
			continue
		}
		kept = append(kept, h)
	}
	d.hosts = kept
	d.reply(cmd, "")
}

func (d *trapDevice) runningConfig() string {
	var b strings.Builder
	b.WriteString("Building configuration...\n\nCurrent configuration:\n!\n")
	b.WriteString("!version 2.0.2J build 95262\n!\nhostname poefs3400lab\n!\n")
	var s string
	for _, s = range d.communities {
		b.WriteString("snmp-server community 0 " + s + " RO\n")
	}
	var h fakeHost
	for _, h = range d.hosts {
		b.WriteString(h.render() + "\n")
	}
	b.WriteString("!\ninterface GigaEthernet0/1\n!\n")
	return b.String()
}

func (d *trapDevice) prompt() string {
	if len(d.stack) == 0 {
		return "Switch#"
	}
	return "Switch_config#"
}

func (d *trapDevice) reply(echo, body string) {
	var b strings.Builder
	b.WriteString(echo + "\n")
	if body != "" {
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString(d.prompt())
	d.emit(b.String())
}

func (d *trapDevice) replyUnknown(echo string) {
	d.emit(echo + "\n^\nUnknown command\n" + d.prompt())
}

func (d *trapDevice) emit(s string) {
	var i int
	for i = 0; i < len(s); i++ {
		d.out <- s[i]
	}
}

func (d *trapDevice) sentContains(sub string) bool {
	var s string
	for _, s = range d.sent {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// enteredConfig: was configuration mode entered at all? EXACT match, because
// "show running-config" contains the word.
func (d *trapDevice) enteredConfig() bool {
	var s string
	for _, s = range d.sent {
		if s == "config" {
			return true
		}
	}
	return false
}

func wantDest() driver.TrapDestination {
	return driver.TrapDestination{Address: "192.0.2.2", Community: "mcpoll"}
}

func notesJoined(res driver.TrapResult) string { return strings.Join(res.Notes, " ") }

// --- the captured forms ----------------------------------------------------

// TestTrapHostLineIsTheCapturedForm: "version" BEFORE the community, and the
// token "v2c" and not the Cisco "2c". Both are corrections the 2026-08-10
// survey made to the form this connector would otherwise have borrowed.
func TestTrapHostLineIsTheCapturedForm(t *testing.T) {
	var got = trapHostLine(wantDest())
	if got != "snmp-server host 192.0.2.2 version v2c mcpoll" {
		t.Fatalf("trapHostLine = %q — the captured form is \"snmp-server host <ip> "+
			"version v2c <community>\"", got)
	}
	if strings.Contains(got, " 2c ") {
		t.Error("\"2c\" is the Cisco spelling; this CLI takes \"v2c\"")
	}
}

// TestTrapRemoveLineIsFullOrNothing is the guard around the captured pathology.
// A line whose version or community was not read back has NO safe removal
// command on this range, and building the short one would send something the
// CLI refuses in a way that reads like success.
func TestTrapRemoveLineIsFullOrNothing(t *testing.T) {
	var full, ok = trapRemoveLine(TrapReceiver{
		Host: "192.0.2.2", Version: "v2c", Community: "old"})
	if !ok || full != "no snmp-server host 192.0.2.2 version v2c old" {
		t.Fatalf("full removal = %q, ok=%v", full, ok)
	}
	var cases = []TrapReceiver{
		{Host: "192.0.2.2", Community: "old"}, // no version
		{Host: "192.0.2.2", Version: "v2c"},   // no community
		{Version: "v2c", Community: "old"},    // no host
		{Host: "192.0.2.2"},                   // nothing but the address
	}
	var r TrapReceiver
	for _, r = range cases {
		var line, built = trapRemoveLine(r)
		if built {
			t.Errorf("built %q from %+v — the short form is answered \"Incomplete "+
				"command\" and LEAVES THE HOST", line, r)
		}
	}
}

// --- parsing ---------------------------------------------------------------

func TestParseTrapHostLines(t *testing.T) {
	var cfg = ParseRunningConfig(strings.Join([]string{
		"snmp-server community 0 mcpoll RO",
		"snmp-server host 192.0.2.2 version v2c mcpoll",
		"snmp-server host 192.0.2.9 version v1 legacy",
		"snmp-server host 192.0.2.7 bare",
		"snmp-server host 192.0.2.8",
		"snmp-server keep-alive 60",
	}, "\n"))
	if len(cfg.TrapReceivers) != 4 {
		t.Fatalf("%d receivers parsed, want 4: %+v — \"keep-alive\" is not a host",
			len(cfg.TrapReceivers), cfg.TrapReceivers)
	}
	var r = cfg.TrapReceivers[0]
	if r.Host != "192.0.2.2" || r.Version != "v2c" || r.Community != "mcpoll" {
		t.Errorf("first receiver = %+v", r)
	}
	if cfg.TrapReceivers[1].Version != "v1" {
		t.Errorf("the v1 line must read as v1, not be folded onto v2c: %+v",
			cfg.TrapReceivers[1])
	}
	// A rendering without the version keyword: the community is still read, the
	// version reads EMPTY, and empty is what stops trapRemoveLine.
	if cfg.TrapReceivers[2].Version != "" || cfg.TrapReceivers[2].Community != "bare" {
		t.Errorf("versionless line = %+v", cfg.TrapReceivers[2])
	}
	if cfg.TrapReceivers[3].Community != "" {
		t.Errorf("an address-only line must not invent a community: %+v",
			cfg.TrapReceivers[3])
	}
	if cfg.TrapReceivers[0].Raw != "snmp-server host 192.0.2.2 version v2c mcpoll" {
		t.Errorf("Raw must carry the device's own words: %q", cfg.TrapReceivers[0].Raw)
	}
}

// TestParseTrapHostAcceptsTheKeywordlessRendering: "version" may or may not be
// echoed back — that is the DEDUCED half — and both must read as v2c rather than
// one of them silently reading as "no version".
func TestParseTrapHostAcceptsTheKeywordlessRendering(t *testing.T) {
	var cfg = ParseRunningConfig("snmp-server host 192.0.2.2 v2c mcpoll\n")
	if len(cfg.TrapReceivers) != 1 {
		t.Fatalf("parsed %+v", cfg.TrapReceivers)
	}
	if cfg.TrapReceivers[0].Version != "v2c" || cfg.TrapReceivers[0].Community != "mcpoll" {
		t.Errorf("receiver = %+v", cfg.TrapReceivers[0])
	}
}

// --- the write -------------------------------------------------------------

func TestApplyTrapReceiverWritesTheCapturedFormAndConfirms(t *testing.T) {
	var c, d = newTrapDevice()
	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Set || res.Version != "v2c" || res.Port != 162 {
		t.Fatalf("result = %+v", res)
	}
	if !d.sentContains("snmp-server host 192.0.2.2 version v2c mcpoll") {
		t.Fatalf("sent = %v", d.sent)
	}
	if d.sentContains("snmp-server enable traps") {
		t.Error("this range has NO \"snmp-server enable traps\" — declaring a host " +
			"is what enables emission (captured 2026-08-10)")
	}
	if d.saved != 1 {
		t.Errorf("saved %d times: a destination a reboot forgets ends supervision "+
			"silently", d.saved)
	}
	if len(d.hosts) != 1 {
		t.Errorf("device carries %+v", d.hosts)
	}
	// The claim is bounded: the line is in the device, which is not proof its
	// traps arrive.
	if !strings.Contains(notesJoined(res), "first trap received") {
		t.Errorf("notes do not say what remains unproven: %v", res.Notes)
	}
}

func TestApplyTrapReceiverIsIdempotent(t *testing.T) {
	var c, d = newTrapDevice()
	d.hosts = []fakeHost{{host: "192.0.2.2", version: "v2c", community: "mcpoll"}}

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Set {
		t.Error("Set on a device that already carried the recipient: Set means a " +
			"write HAPPENED and was confirmed")
	}
	if res.Known != 1 {
		t.Errorf("Known = %d, want 1", res.Known)
	}
	if d.enteredConfig() || d.saved != 0 {
		t.Errorf("an already conformant device was written to: sent=%v saved=%d",
			d.sent, d.saved)
	}
}

// TestApplyTrapReceiverDetectsAWriteWithoutEffect: the CLI accepts and the
// configuration does not carry it. A return code is not an observation.
func TestApplyTrapReceiverDetectsAWriteWithoutEffect(t *testing.T) {
	var c, d = newTrapDevice()
	d.swallowHost = true

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err == nil {
		t.Fatal("a write the re-read does not confirm was reported as done — on this " +
			"range that would make api.SetTrapDestination record a destination the " +
			"device never took")
	}
	if res.Set {
		t.Error("Set on an unconfirmed write")
	}
	if d.saved != 0 {
		t.Error("an unconfirmed write was persisted")
	}
	if !strings.Contains(err.Error(), "running-config") {
		t.Errorf("the error must point at the read that decided: %v", err)
	}
}

// --- THE captured pathology ------------------------------------------------

// TestApplyTrapReceiverNeverSendsTheShortRemovalForm is the test this file
// exists for.
//
// A divergent line toward our collector is present, and the device rendered it
// WITHOUT a version — so the full removal form cannot be built. The connector
// must send NOTHING rather than the short form, because the short form answers
// "Incomplete command" and leaves the host declared: a removal that reports
// success while the host stays is the false-success class this codebase refuses.
func TestApplyTrapReceiverNeverSendsTheShortRemovalForm(t *testing.T) {
	var c, d = newTrapDevice()
	d.hosts = []fakeHost{{host: "192.0.2.2", raw: "snmp-server host 192.0.2.2 stale"}}

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var s string
	for _, s = range d.sent {
		if strings.HasPrefix(s, "no snmp-server host") {
			t.Fatalf("sent %q — the only removal command buildable from a line with no "+
				"version is the SHORT one, which is answered \"Incomplete command\" and "+
				"LEAVES THE HOST DECLARED (captured 2026-08-10)", s)
		}
	}
	if res.Replaced {
		t.Error("Replaced on a line nothing was sent for")
	}
	if !res.Set {
		t.Error("the wanted recipient must still be declared: leaving a device with a " +
			"stale line and no working one would be strictly worse")
	}
	var notes = notesJoined(res)
	if !strings.Contains(notes, "Incomplete command") || !strings.Contains(notes, "stale") {
		t.Errorf("the surviving line is not named with the reason: %v", res.Notes)
	}
}

// TestApplyTrapReceiverCatchesARemovalThatLeftTheHost: the full form was sent,
// the CLI answered nothing at all, and the host is STILL THERE. Replaced must be
// judged by the re-read and never by the CLI's answer.
func TestApplyTrapReceiverCatchesARemovalThatLeftTheHost(t *testing.T) {
	var c, d = newTrapDevice()
	d.hosts = []fakeHost{{host: "192.0.2.2", version: "v2c", community: "rotated"}}
	d.swallowRemoval = true

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !d.sentContains("no snmp-server host 192.0.2.2 version v2c rotated") {
		t.Fatalf("the FULL removal form was not sent: %v", d.sent)
	}
	if res.Replaced {
		t.Fatal("Replaced although the divergent line is still in the configuration: " +
			"the CLI's answer to a removal is not evidence that the host is gone")
	}
	if !res.Set {
		t.Error("the wanted recipient was written and confirmed; Set must say so")
	}
	if !strings.Contains(notesJoined(res), "SURVIVED") {
		t.Errorf("the survivor is not reported: %v", res.Notes)
	}
}

func TestApplyTrapReceiverReplacesADivergentLine(t *testing.T) {
	var c, d = newTrapDevice()
	d.hosts = []fakeHost{{host: "192.0.2.2", version: "v2c", community: "rotated"}}

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Replaced || !res.Set {
		t.Fatalf("result = %+v", res)
	}
	if len(d.hosts) != 1 || d.hosts[0].community != "mcpoll" {
		t.Errorf("device carries %+v", d.hosts)
	}
}

// TestApplyTrapReceiverLeavesThirdPartiesAlone: a recipient toward ANOTHER
// address is somebody else's supervision. Counted, never touched.
func TestApplyTrapReceiverLeavesThirdPartiesAlone(t *testing.T) {
	var c, d = newTrapDevice()
	d.hosts = []fakeHost{{host: "192.0.2.50", version: "v2c", community: "theirs"}}

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Known != 1 {
		t.Errorf("Known = %d — it is the only measure of this table's ceiling anyone "+
			"will ever have", res.Known)
	}
	if d.sentContains("no snmp-server host 192.0.2.50") {
		t.Fatal("a third party's supervision was cut to install ours")
	}
	if !strings.Contains(notesJoined(res), "another address") {
		t.Errorf("the third party is not reported: %v", res.Notes)
	}
}

// --- refusals --------------------------------------------------------------

func TestApplyTrapReceiverRefusesWhatThisRangeCannotExpress(t *testing.T) {
	var cases = []struct {
		name string
		want driver.TrapDestination
		says string
	}{
		{"v3 destination",
			driver.TrapDestination{Address: "192.0.2.2", User: "mcprobe"}, "v3"},
		{"non standard port",
			driver.TrapDestination{Address: "192.0.2.2", Community: "mcpoll", Port: 5162},
			"port"},
		{"no address",
			driver.TrapDestination{Community: "mcpoll"}, "address"},
		{"a name where the form takes A.B.C.D",
			driver.TrapDestination{Address: "collector.lan", Community: "mcpoll"}, "IPv4"},
		{"no community",
			driver.TrapDestination{Address: "192.0.2.2"}, "community"},
		{"a community this CLI cannot spell",
			driver.TrapDestination{Address: "192.0.2.2", Community: "two words"}, "bare word"},
	}
	var i int
	for i = range cases {
		var c, d = newTrapDevice()
		var _, err = c.ApplyTrapReceiver(context.Background(), cases[i].want)
		if err == nil {
			t.Errorf("%s: accepted", cases[i].name)
			continue
		}
		if !strings.Contains(err.Error(), cases[i].says) {
			t.Errorf("%s: %v", cases[i].name, err)
		}
		if len(d.sent) != 0 {
			t.Errorf("%s: the device was contacted before the refusal: %v",
				cases[i].name, d.sent)
		}
	}
}

// TestApplyTrapReceiverRefusesAnAgentWithNoCommunity: the collector accepts a
// trap only if its community equals the one it POLLS with. On a device polled by
// nothing, the line would be exact and every trap dropped.
func TestApplyTrapReceiverRefusesAnAgentWithNoCommunity(t *testing.T) {
	var c, d = newTrapDevice()
	d.communities = nil

	var _, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err == nil {
		t.Fatal("a destination was declared on a device with no community")
	}
	if !strings.Contains(err.Error(), "ApplySNMP") {
		t.Errorf("the refusal must name the gesture to make first: %v", err)
	}
	if d.enteredConfig() {
		t.Error("configuration mode was entered anyway")
	}
}

// TestApplyTrapReceiverNotesACommunityNobodyPollsWith: not a refusal — the
// device takes any string there — but the collector will drop those traps, and
// nothing else on screen would say why.
func TestApplyTrapReceiverNotesACommunityNobodyPollsWith(t *testing.T) {
	var c, d = newTrapDevice()
	d.communities = []string{"somethingelse"}

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Set {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(notesJoined(res), "dropped on arrival") {
		t.Errorf("notes = %v", res.Notes)
	}
}

// TestApplyTrapReceiverRefusesATruncatedPreRead: this pager cuts long output and
// the "snmp-server" block sits at the end of the configuration. A prefix that
// never reached it is indistinguishable from a device carrying no destination.
func TestApplyTrapReceiverRefusesATruncatedPreRead(t *testing.T) {
	var c, d = newTrapDevice()
	d.dieOnRead = 1

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err == nil {
		t.Fatal("a truncated pre-read was parsed as the whole configuration")
	}
	if res.Set || d.enteredConfig() {
		t.Errorf("something was written on a read that never landed: %+v %v", res, d.sent)
	}
}

// TestApplyTrapReceiverSaysSentAndNotConfirmedWhenTheReReadDies: the distinction
// that matters to whoever reads the failure — the command DID go out.
func TestApplyTrapReceiverSaysSentAndNotConfirmedWhenTheReReadDies(t *testing.T) {
	var c, d = newTrapDevice()
	d.dieOnRead = 2

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err == nil {
		t.Fatal("an unverified write was reported as done")
	}
	if res.Set {
		t.Error("Set without an observation")
	}
	if !strings.Contains(err.Error(), "DID go out") {
		t.Errorf("the error does not tell a failed write from a failed verification: %v", err)
	}
}

// TestApplyTrapReceiverFailedSaveIsANoteAndNotAFailure: the running
// configuration really does carry the destination, so the gesture succeeded —
// what is lost is only its survival across a reboot, and that is said.
func TestApplyTrapReceiverFailedSaveIsANoteAndNotAFailure(t *testing.T) {
	var c, d = newTrapDevice()
	d.refuseSave = true

	var res, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Set {
		t.Error("the destination is in the running configuration and Set says otherwise")
	}
	if !strings.Contains(notesJoined(res), "NOT persisted") {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestApplyTrapReceiverRefusesAnUnwritableSession(t *testing.T) {
	var c, d = newTrapDevice()
	c.known = false

	var _, err = c.ApplyTrapReceiver(context.Background(), wantDest())
	if err == nil {
		t.Fatal("wrote to a device whose CLI was never recognized")
	}
	if len(d.sent) != 0 {
		t.Errorf("the device was contacted: %v", d.sent)
	}
}

// --- the capability and the interface are one thing ------------------------

// TestTrapsAndTheTrapPusherLandTogether: `Monitoring.Traps` is what opens
// 162/udp on the proxy and what lets api/trapdest.go reach the device;
// driver.TrapPusher is what can point the device at a collector. Declaring the
// first without the second promises a channel nothing can aim, and leaves
// `trap_dest_proxy_id` NULL so ConfirmTrapPath judges every arriving trap
// foreign — which is exactly what the 2026-08-10 probe measured.
func TestTrapsAndTheTrapPusherLandTogether(t *testing.T) {
	if !monitoring().Traps {
		t.Fatal("Traps was measured on poefs3400lab on 2026-08-10 — 0 → 3 → 7 PDUs, " +
			"accepted by the controller, frozen at 7 once reverted")
	}
	var c, _ = newTrapDevice()
	var _, ok = any(c).(driver.TrapPusher)
	if !ok {
		t.Fatal("Traps is declared and this connector cannot declare a destination")
	}
}
