package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// The controller's one-off questions, tested WITHOUT a network or any device.
//
// What's under test here is what tends to go wrong: the row limit (a
// truncated response that passes for complete is exactly the bug this
// mechanism exists to prevent), the translation of SNMP types, and the wire
// format.

// --- Fake counterpart ---

// fakeWalker replays a list of PDUs, then an optional error. The walk
// stops dead as soon as the callback returns an error, exactly like gosnmp.
type fakeWalker struct {
	pdus []gosnmp.SnmpPDU
	err  error
	// seenPDUs: PDUs actually passed to the callback. This is what lets us
	// verify that the limit cuts off the WALK itself, not just the
	// response — a walk that keeps going while discarding its rows would
	// cost the device for nothing.
	seenPDUs int
	root     string
	closed   bool
}

func (w *fakeWalker) BulkWalk(rootOID string, walkFn gosnmp.WalkFunc) error {
	w.root = rootOID
	var pdu gosnmp.SnmpPDU
	for _, pdu = range w.pdus {
		w.seenPDUs++
		var err = walkFn(pdu)
		if err != nil {
			return err
		}
	}
	return w.err
}

func (w *fakeWalker) Close() error {
	w.closed = true
	return nil
}

// pduCounter builds n rows of a 64-bit counter table.
func pduCounter(n int) []gosnmp.SnmpPDU {
	var out = make([]gosnmp.SnmpPDU, 0, n)
	var i int
	for i = 1; i <= n; i++ {
		out = append(out, gosnmp.SnmpPDU{
			Name:  ".1.3.6.1.2.1.31.1.1.1.6." + strconv.Itoa(i),
			Type:  gosnmp.Counter64,
			Value: uint64(i),
		})
	}
	return out
}

// withWalker installs a fake counterpart for the duration of a test.
func withWalker(t *testing.T, w *fakeWalker, err error) {
	t.Helper()
	askDial = func(tunnel.AskRequest) (askWalker, error) { return w, err }
	t.Cleanup(func() { askDial = nil })
}

// --- Limits ---

func TestAskMaxRowsAppliesDefaultAndCeiling(t *testing.T) {
	type cas struct {
		name            string
		requested, want int
	}
	var cases = []cas{
		{name: "absent", requested: 0, want: tunnel.DefaultAskRows},
		{name: "negative", requested: -1, want: tunnel.DefaultAskRows},
		{name: "reasonable", requested: 12, want: 12},
		{name: "at the ceiling", requested: tunnel.MaxAskRows, want: tunnel.MaxAskRows},
		// A limit the caller can raise isn't a limit.
		{name: "beyond the ceiling", requested: 10 * tunnel.MaxAskRows,
			want: tunnel.MaxAskRows},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.name, func(t *testing.T) {
			var got = askMaxRows(k.requested)
			if got != k.want {
				t.Errorf("askMaxRows(%d) = %d, expected %d", k.requested, got, k.want)
			}
		})
	}
}

func TestAskRowsCutsAndReportsTruncation(t *testing.T) {
	var w = &fakeWalker{pdus: pduCounter(10)}
	var rows, truncated, err = askRows(context.Background(), w, "1.3.6.1.2.1", 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(rows))
	}
	if !truncated {
		t.Error("Truncated must be set: without it, a truncated table " +
			"would pass for complete")
	}
	// The limit stops the WALK: only one extra PDU was read, the one that
	// established there was more.
	if w.seenPDUs != 5 {
		t.Errorf("the walk must stop at the 5th PDU, %d read", w.seenPDUs)
	}
	if rows[0].Value != "1" || rows[3].Value != "4" {
		t.Errorf("expected rows in walk order, got %+v", rows)
	}
}

func TestAskRowsNoTruncationExactlyAtTheBound(t *testing.T) {
	// A walk that lands exactly on the limit is NOT truncated: the one row
	// too many is what proves there was more, and there wasn't any.
	var w = &fakeWalker{pdus: pduCounter(4)}
	var rows, truncated, err = askRows(context.Background(), w, "1.3.6.1.2.1", 4)
	if err != nil || len(rows) != 4 || truncated {
		t.Fatalf("expected 4 untruncated rows, got %d rows, truncated=%v, err=%v",
			len(rows), truncated, err)
	}
}

func TestAskWalkHardCeilingCannotBeBypassed(t *testing.T) {
	// The controller asks for ten times the ceiling; the proxy returns the ceiling.
	var w = &fakeWalker{pdus: pduCounter(tunnel.MaxAskRows + 1)}
	withWalker(t, w, nil)

	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: tunnel.AskSNMPWalk, IP: "198.51.100.101", OID: "1.3.6.1.2.1",
		MaxRows: 10 * tunnel.MaxAskRows,
	})
	if resp.Err != "" {
		t.Fatalf("expected no error, got %q", resp.Err)
	}
	if len(resp.Rows) != tunnel.MaxAskRows || !resp.Truncated {
		t.Errorf("expected %d truncated rows, got %d (truncated=%v)",
			tunnel.MaxAskRows, len(resp.Rows), resp.Truncated)
	}
	if !w.closed {
		t.Error("the SNMP session must be closed: a proxy that leaks its sockets " +
			"eventually can't query anyone anymore")
	}
}

func TestAskWalkReturnsWhatItReadBeforeAFailure(t *testing.T) {
	// A device that responds and then goes silent: the rows already read go
	// out WITH the error. "Here are the first two rows and here's why it
	// stopped" is fixable; an empty response isn't.
	var w = &fakeWalker{pdus: pduCounter(2), err: errors.New("request timeout")}
	withWalker(t, w, nil)

	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: tunnel.AskSNMPWalk, IP: "198.51.100.101", OID: "1.3.6.1.2.1",
	})
	if len(resp.Rows) != 2 {
		t.Errorf("expected 2 rows despite the failure, got %d", len(resp.Rows))
	}
	if !strings.Contains(resp.Err, "request timeout") {
		t.Errorf("the cause must surface plainly, got %q", resp.Err)
	}
	// Truncated speaks only of the limit: a walk cut short by a failure is
	// not a bounded response, and conflating the two would mask the failure.
	if resp.Truncated {
		t.Error("Truncated must not signal a failure")
	}
}

func TestAskWalkHonoursTheContext(t *testing.T) {
	var ctx, cancel = context.WithCancel(context.Background())
	cancel()
	var w = &fakeWalker{pdus: pduCounter(10)}
	var rows, truncated, err = askRows(ctx, w, "1.3.6.1.2.1", tunnel.DefaultAskRows)
	if err == nil {
		t.Fatal("a canceled context must stop the walk")
	}
	if len(rows) != 0 || truncated {
		t.Errorf("expected no rows, got %d (truncated=%v)", len(rows), truncated)
	}
}

// --- Connection conventions ---

// TestAskDialInheritsPollConventions: the diagnostic no longer has its
// OWN connection conventions.
//
// This file used to duplicate snmppoll's (v2c, 2 s timeout, a single retry,
// empty community = public, port 161). The copy wasn't the danger, its
// invisibility was: a timeout tweaked on only one side, and a diagnostic
// walk describes a different device than an ordinary poll with nothing to
// flag it — the outage then gets chased inside the device. So the session's
// TYPE is checked here: it comes from snmppoll.Dial, and nowhere else. A
// local dialer reintroduced here would fail this test before it could ever
// diverge.
func TestAskDialInheritsPollConventions(t *testing.T) {
	// No packet is sent: in UDP, "connecting" a socket only resolves the
	// route. This test therefore talks to no device at all.
	var w, err = dialAskSNMP(tunnel.AskRequest{IP: "127.0.0.1", Port: 1161})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer w.Close() //nolint:errcheck

	var _, ok = w.(*snmppoll.Session)
	if !ok {
		t.Fatalf("the walk's session must come from snmppoll.Dial — the only place "+
			"where the project's connection conventions are written down (see "+
			"snmppoll.TestConventionsDeConnexionUniques); got %T", w)
	}
}

// TestAskWalkOnTheWireCarriesPollConventions verifies these conventions ON
// THE WIRE, from this side, with no device.
//
// A UDP socket that never replies is enough: in v2c, the datagram carries
// the version and the community IN THE CLEAR — that's the entirety of
// SNMPv2c's "security" — and the number of datagrams tells the number of
// attempts. The previous test checks where the session comes from; this one
// checks what it actually puts on the network, so it holds even if the
// session is obtained some other way one day.
//
// It runs for the ~4 s of the retry sequence, and that's the exact price of
// what it measures: you can't observe a timeout without waiting for it.
func TestAskWalkOnTheWireCarriesPollConventions(t *testing.T) {
	var conn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listening socket: %v", err)
	}
	var local = conn.LocalAddr().(*net.UDPAddr)

	// A single reader, and its termination serves as a barrier: the
	// datagrams are only read after <-finished, with no lock and no race.
	var datagrams [][]byte
	var finished = make(chan struct{})
	go func() {
		defer close(finished)
		var tampon = make([]byte, 4096)
		for {
			var n, _, errLecture = conn.ReadFromUDP(tampon)
			if errLecture != nil {
				return
			}
			var copied = make([]byte, n)
			copy(copied, tampon[:n])
			datagrams = append(datagrams, copied)
		}
	}()

	// Community ABSENT from the request: that's the fallback we want to see
	// go out on the wire, because it's the one someone would be tempted to
	// rewrite elsewhere.
	var w, errDial = dialAskSNMP(tunnel.AskRequest{IP: "127.0.0.1", Port: local.Port})
	if errDial != nil {
		_ = conn.Close()
		<-finished
		t.Fatalf("session: %v", errDial)
	}
	var start = time.Now()
	var walkErr = w.BulkWalk("1.3.6.1.2.1.1", func(gosnmp.SnmpPDU) error { return nil })
	var elapsed = time.Since(start)
	_ = w.Close()
	_ = conn.Close()
	<-finished

	if walkErr == nil {
		t.Fatal("a device that never replies must return an error")
	}
	// Retries: 1 — the initial attempt plus ONE retry. One more retry would
	// cost a full collection cycle for every silent device; one fewer and
	// the slightest dropped datagram would pass for an outage.
	if len(datagrams) != 2 {
		t.Fatalf("expected 2 sends (attempt + 1 retry), got %d", len(datagrams))
	}
	// "02 01 01": INTEGER version = 1, i.e. v2c. "04 06 public": OCTET
	// STRING of the fallback community. The two follow each other in the
	// message header, so a single search attests to both together.
	var header = []byte("\x02\x01\x01\x04\x06public")
	if !bytes.Contains(datagrams[0], header) {
		t.Errorf("expected v2c + \"public\" community on the wire, datagram: %x",
			datagrams[0])
	}
	// Timeout: 2 s per attempt, so ~4 s for the sequence. The window is
	// deliberately wide — what must fail is a timeout that has become
	// something genuinely different: too short, a slow device would pass
	// for silent; too long, a silent device would hold up a collection
	// cycle.
	if elapsed < 3*time.Second || elapsed > 8*time.Second {
		t.Errorf("expected a ~4 s retry sequence (2 s x 2), got %v", elapsed)
	}
}

// --- Unknown question ---

func TestAskUnknownQuestion(t *testing.T) {
	// A proxy older than its controller is a NORMAL case: it must say so.
	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: "arp_table", IP: "198.51.100.101",
	})
	if resp.Err == "" {
		t.Fatal("an unknown question must return an Err, not silence")
	}
	if !strings.Contains(resp.Err, "arp_table") || !strings.Contains(resp.Err, "unknown") {
		t.Errorf("Err must name the unknown question, got %q", resp.Err)
	}
	if resp.Rows != nil || resp.SourceAddr != "" || resp.Truncated {
		t.Errorf("no data must accompany a refusal: %+v", resp)
	}

	// An empty field (a controller talking gibberish) follows the same path.
	if handleAsk(context.Background(), tunnel.AskRequest{}).Err == "" {
		t.Error("an empty question must return an Err")
	}

	// The name comes from the wire: it is bounded and escaped before
	// reaching a log.
	var long = handleAsk(context.Background(),
		tunnel.AskRequest{Ask: strings.Repeat("x", 500) + "\n\x1b[2J"})
	if len(long.Err) > 200 || strings.ContainsAny(long.Err, "\n\x1b") {
		t.Errorf("Err must stay bounded and escaped, got %d bytes: %q",
			len(long.Err), long.Err)
	}
}

// --- Type and value translation ---

func TestAskTypeTranslation(t *testing.T) {
	type cas struct {
		name       string
		pdu        gosnmp.SnmpPDU
		typ, value string
	}
	var cases = []cas{
		{name: "port label", typ: "OctetString", value: "GigabitEthernet1/0/1",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.OctetString,
				Value: []byte("GigabitEthernet1/0/1")}},
		// The case that justifies rendering as TEXT: this counter doesn't
		// survive a float64, and it's precisely counters that get queried.
		{name: "64-bit counter at maximum", typ: "Counter64",
			value: "18446744073709551615",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Counter64,
				Value: uint64(18446744073709551615)}},
		{name: "64-bit counter beyond 2^53", typ: "Counter64",
			value: "9007199254740993",
			pdu:   gosnmp.SnmpPDU{Type: gosnmp.Counter64, Value: uint64(9007199254740993)}},
		{name: "port status", typ: "Integer", value: "1",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 1}},
		{name: "gauge", typ: "Gauge32", value: "1000",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Gauge32, Value: uint(1000)}},
		{name: "uptime", typ: "TimeTicks", value: "4294967295",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.TimeTicks, Value: uint32(4294967295)}},
		{name: "32-bit counter", typ: "Counter32", value: "17",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Counter32, Value: uint(17)}},
		// gosnmp prefixes OIDs with a dot, the controller doesn't: only one
		// notation comes out of here, otherwise nothing compares.
		{name: "object identifier", typ: "ObjectIdentifier",
			value: "1.3.6.1.4.1.11.2.3.7.11.253",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.ObjectIdentifier,
				Value: ".1.3.6.1.4.1.11.2.3.7.11.253"}},
		{name: "address", typ: "IPAddress", value: "198.51.100.101",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.IPAddress, Value: "198.51.100.101"}},
		// The three SNMPv2 exceptions are responses, not values: all the
		// information is in the type, and inventing a "0" would read as a
		// measurement.
		{name: "missing object", typ: "NoSuchObject", value: "",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.NoSuchObject, Value: nil}},
		{name: "missing instance", typ: "NoSuchInstance", value: "",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.NoSuchInstance, Value: nil}},
		{name: "end of view", typ: "EndOfMibView", value: "",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.EndOfMibView, Value: nil}},
		{name: "null", typ: "Null", value: "",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Null, Value: nil}},
		{name: "opaque float", typ: "OpaqueFloat", value: "36.75",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.OpaqueFloat, Value: float32(36.75)}},
		{name: "opaque double", typ: "OpaqueDouble", value: "-3.5",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.OpaqueDouble, Value: float64(-3.5)}},
		// An unrecognized type renders its TAG, not a false label: an
		// operator reading "Type0x2b" knows to go check the MIB, whereas a
		// made-up label would send them straight into a wall.
		{name: "unexpected type", typ: "Type0x2b", value: "doesn't matter",
			pdu: gosnmp.SnmpPDU{Type: gosnmp.Asn1BER(0x2b), Value: "doesn't matter"}},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.name, func(t *testing.T) {
			k.pdu.Name = ".1.3.6.1.2.1.1.1.0"
			var row = askRow(k.pdu)
			if row.OID != "1.3.6.1.2.1.1.1.0" {
				t.Errorf("expected OID without leading dot, got %q", row.OID)
			}
			if row.Type != k.typ {
				t.Errorf("expected type %q, got %q", k.typ, row.Type)
			}
			if row.Value != k.value {
				t.Errorf("expected value %q, got %q", k.value, row.Value)
			}
		})
	}
}

func TestAskBinaryOctetString(t *testing.T) {
	// A MAC address: six raw bytes. json.Marshal would silently replace any
	// non-UTF-8 byte with U+FFFD, which would make it unreadable FOREVER
	// with nothing to signal the substitution. Hence the prefixed hex.
	var mac = gosnmp.SnmpPDU{
		Name:  ".1.3.6.1.2.1.2.2.1.6.1",
		Type:  gosnmp.OctetString,
		Value: []byte{0x00, 0x1b, 0x2f, 0xff, 0x00, 0x0a},
	}
	var row = askRow(mac)
	if row.Value != "0x001b2fff000a" {
		t.Errorf("expected prefixed hex, got %q", row.Value)
	}

	// The prefix isn't decorative: without it, "001b2f" would be confused
	// with a port label that genuinely happened to be named that.
	if !strings.HasPrefix(row.Value, "0x") {
		t.Error("the binary rendering must be recognizable")
	}

	// And the rendered value travels through JSON unaltered, which is the goal.
	var data, err = json.Marshal(row)
	if err != nil {
		t.Fatalf("serialization: %v", err)
	}
	if !strings.Contains(string(data), "0x001b2fff000a") {
		t.Errorf("value altered by JSON: %s", data)
	}

	// Multiline text (sysDescr on most vendors' devices) stays TEXT:
	// json.Marshal escapes it correctly, and hex-encoding it would make it
	// unreadable for nothing.
	var descr = askRow(gosnmp.SnmpPDU{Type: gosnmp.OctetString,
		Value: []byte("Instant On 1930\r\nfirmware 2.7.0")})
	if descr.Value != "Instant On 1930\r\nfirmware 2.7.0" {
		t.Errorf("expected multiline text preserved, got %q", descr.Value)
	}
	// Valid accented UTF-8 too: it isn't binary just because it isn't ASCII.
	var accent = askRow(gosnmp.SnmpPDU{Type: gosnmp.OctetString,
		Value: []byte("backup link — Zürich, floor 2")})
	if accent.Value != "backup link — Zürich, floor 2" {
		t.Errorf("expected UTF-8 preserved, got %q", accent.Value)
	}
	// A lone 0x80 byte isn't UTF-8: hex, even when the rest of the string is
	// perfectly readable — a half-translated value would be worse than
	// unreadable.
	var invalid = askRow(gosnmp.SnmpPDU{Type: gosnmp.OctetString,
		Value: []byte{'p', 'o', 'r', 't', 0x80}})
	if invalid.Value != "0x706f727480" {
		t.Errorf("expected invalid UTF-8 -> hex, got %q", invalid.Value)
	}
	// Empty string: empty, not "0x".
	if askRow(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte{}}).Value != "" {
		t.Error("an empty OctetString renders an empty value")
	}
}

// --- Input validation ---

func TestAskInvalidOIDAndIP(t *testing.T) {
	var w = &fakeWalker{pdus: pduCounter(3)}
	withWalker(t, w, nil)

	type cas struct {
		name, ip, oid string
	}
	var cases = []cas{
		{name: "missing oid", ip: "198.51.100.101", oid: ""},
		{name: "non-numeric oid", ip: "198.51.100.101", oid: "1.3.6.sysDescr"},
		{name: "oid with an empty segment", ip: "198.51.100.101", oid: "1.3..6"},
		{name: "missing address", ip: "", oid: "1.3.6.1"},
		{name: "name instead of an address", ip: "switch-labo", oid: "1.3.6.1"},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.name, func(t *testing.T) {
			var resp = handleAsk(context.Background(), tunnel.AskRequest{
				Ask: tunnel.AskSNMPWalk, IP: k.ip, OID: k.oid,
			})
			if resp.Err == "" {
				t.Fatalf("expected an explicit Err, got %+v", resp)
			}
			if len(resp.Rows) != 0 {
				t.Errorf("no rows must accompany a refusal: %+v", resp.Rows)
			}
		})
	}
	if w.seenPDUs != 0 {
		t.Errorf("an invalid request must not query the device at all, %d PDUs read",
			w.seenPDUs)
	}

	// The leading dot is accepted in both notations, and only one comes out.
	var oid, err = askOID(".1.3.6.1.2.1")
	if err != nil || oid != "1.3.6.1.2.1" {
		t.Errorf("askOID(\".1.3.6.1.2.1\") = %q, %v", oid, err)
	}
}

// --- AskSourceAddr ---

func TestAskSourceAddrReturnsABareAddress(t *testing.T) {
	// 127.0.0.1 is enough to exercise the method without knowing anything about the
	// test machine's network: loopback is routed everywhere.
	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: tunnel.AskSourceAddr, IP: "127.0.0.1",
	})
	if resp.Err != "" {
		t.Fatalf("no error expected, got %q", resp.Err)
	}
	if resp.SourceAddr != "127.0.0.1" {
		t.Errorf("source address 127.0.0.1 expected, got %q", resp.SourceAddr)
	}
	// No mask, no port, no zone: the controller writes this string as is into a
	// trap destination.
	if strings.ContainsAny(resp.SourceAddr, "/:%") {
		t.Errorf("bare address expected, got %q", resp.SourceAddr)
	}
	if net.ParseIP(resp.SourceAddr) == nil {
		t.Errorf("%q is not an address", resp.SourceAddr)
	}
	// No walk row must accompany this response.
	if resp.Rows != nil {
		t.Errorf("polluted response: %+v", resp.Rows)
	}
}

func TestAskSourceAddrRefusesWhatIsNotAnAddress(t *testing.T) {
	var resp = handleAsk(context.Background(), tunnel.AskRequest{
		Ask: tunnel.AskSourceAddr, IP: "198.51.100.101/24",
	})
	if resp.Err == "" || resp.SourceAddr != "" {
		t.Fatalf("an explicit refusal is expected, got %+v", resp)
	}
}

// --- Format de fil ---

// TestAskFullExchange locks the pattern: preamble, ONE request, ONE response,
// then close. Over a pair of in-memory connections, so without yamux or
// WebSocket: what is exercised here is the format, not the transport.
func TestAskFullExchange(t *testing.T) {
	var client, server = net.Pipe()
	defer client.Close() //nolint:errcheck

	go tunnel.ServeAskStream(server, handleAsk)

	var err = json.NewEncoder(client).Encode(tunnel.DialRequest{Op: tunnel.OpAsk})
	if err != nil {
		t.Fatalf("preamble: %v", err)
	}
	err = json.NewEncoder(client).Encode(tunnel.AskRequest{
		Ask: tunnel.AskSourceAddr, IP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var br = bufio.NewReader(client)
	var line string
	line, err = br.ReadString('\n')
	if err != nil {
		t.Fatalf("response: %v", err)
	}
	var resp tunnel.AskResponse
	if err = json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unreadable response %q: %v", line, err)
	}
	if resp.SourceAddr != "127.0.0.1" || resp.Err != "" {
		t.Errorf("unexpected response: %+v", resp)
	}
	// Fields that do not apply are not on the wire: a source_addr response carries
	// neither "rows" nor "truncated".
	if strings.Contains(line, "rows") || strings.Contains(line, "truncated") {
		t.Errorf("polluted response: %s", line)
	}
	// One question, one answer, the stream closes: nothing after the line.
	var rest []byte
	rest, err = br.ReadBytes('\n')
	if err == nil || len(rest) != 0 {
		t.Errorf("the stream must close after the response, got %q / %v", rest, err)
	}
}

func TestAskWalkExchangeOnTheWire(t *testing.T) {
	var w = &fakeWalker{pdus: pduCounter(3)}
	withWalker(t, w, nil)

	var client, server = net.Pipe()
	defer client.Close() //nolint:errcheck
	go tunnel.ServeAskStream(server, handleAsk)

	var enc = json.NewEncoder(client)
	var err = enc.Encode(tunnel.DialRequest{Op: tunnel.OpAsk})
	if err == nil {
		err = enc.Encode(tunnel.AskRequest{
			Ask: tunnel.AskSNMPWalk, IP: "198.51.100.101",
			OID: "1.3.6.1.2.1.31.1.1.1.6", Community: "secret-de-labo", MaxRows: 2,
		})
	}
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var line string
	line, err = bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("response: %v", err)
	}
	var resp tunnel.AskResponse
	if err = json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unreadable response %q: %v", line, err)
	}
	if len(resp.Rows) != 2 || !resp.Truncated {
		t.Fatalf("2 truncated rows expected, got %+v", resp)
	}
	if resp.Rows[0].OID != "1.3.6.1.2.1.31.1.1.1.6.1" || resp.Rows[0].Type != "Counter64" {
		t.Errorf("unexpected row: %+v", resp.Rows[0])
	}
	if w.root != "1.3.6.1.2.1.31.1.1.1.6" {
		t.Errorf("walked root %q", w.root)
	}
	// The community is an operating secret: it has no business in a response, any
	// more than in a log.
	if strings.Contains(line, "secret-de-labo") {
		t.Errorf("the community went back out in the response: %s", line)
	}
}

// TestAskWaitBounded: the controller proposes the wait, the proxy decides. A
// bound a caller can raise is not a bound.
func TestAskWaitBounded(t *testing.T) {
	var seenWait = make(chan time.Duration, 1)
	var handler = func(ctx context.Context, req tunnel.AskRequest) tunnel.AskResponse {
		var deadline, ok = ctx.Deadline()
		if !ok {
			seenWait <- 0
			return tunnel.AskResponse{Err: "no deadline"}
		}
		seenWait <- time.Until(deadline)
		return tunnel.AskResponse{SourceAddr: "127.0.0.1"}
	}

	type cas struct {
		name      string
		timeoutMS int
		wantWait  time.Duration
	}
	var cases = []cas{
		{name: "absent", timeoutMS: 0, wantWait: tunnel.DefaultAskWait},
		{name: "reasonable", timeoutMS: 3000, wantWait: 3 * time.Second},
		{name: "beyond the ceiling", timeoutMS: 600_000, wantWait: tunnel.MaxAskWait},
	}
	var k cas
	for _, k = range cases {
		t.Run(k.name, func(t *testing.T) {
			var client, server = net.Pipe()
			defer client.Close() //nolint:errcheck
			go tunnel.ServeAskStream(server, handler)

			var enc = json.NewEncoder(client)
			var err = enc.Encode(tunnel.DialRequest{Op: tunnel.OpAsk})
			if err == nil {
				err = enc.Encode(tunnel.AskRequest{
					Ask: tunnel.AskSourceAddr, IP: "127.0.0.1", TimeoutMS: k.timeoutMS,
				})
			}
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, err = bufio.NewReader(client).ReadString('\n'); err != nil {
				t.Fatalf("response: %v", err)
			}

			var remaining = <-seenWait
			// One-second margin: crossing the pipe does not take zero time.
			if remaining > k.wantWait || remaining < k.wantWait-time.Second {
				t.Errorf("wait ≈ %v expected, got %v", k.wantWait, remaining)
			}
		})
	}
}

// TestAskWithoutResponder: a proxy whose responder is not wired SAYS SO. A stream
// that closes silently cannot be told apart from a tunnel going down.
func TestAskWithoutResponder(t *testing.T) {
	var client, server = net.Pipe()
	defer client.Close() //nolint:errcheck
	go tunnel.ServeAskStream(server, nil)

	var enc = json.NewEncoder(client)
	var err = enc.Encode(tunnel.DialRequest{Op: tunnel.OpAsk})
	if err == nil {
		err = enc.Encode(tunnel.AskRequest{Ask: tunnel.AskSourceAddr, IP: "127.0.0.1"})
	}
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var line string
	line, err = bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("response: %v", err)
	}
	var resp tunnel.AskResponse
	if err = json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unreadable response %q: %v", line, err)
	}
	if resp.Err == "" {
		t.Error("an Err is expected when no responder is wired")
	}
}
