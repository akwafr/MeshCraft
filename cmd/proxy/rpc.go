package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/snmppoll"
	"meshcraft/internal/tunnel"
)

// Answering the controller's one-off questions: the PROXY half of
// tunnel.OpAsk.
//
// The two questions in the contract share only one reason for being grouped
// together, and it alone justifies the mechanism: these facts are knowable
// ONLY from here. SNMP is UDP and the tunnel only relays TCP — there is
// nothing to relay; and the routing table that picks a source address is
// THIS kernel's, not something the controller could deduce from an address
// inventory.
//
// Three rules govern this file.
//
//  1. READ-ONLY, permanently. There is no write question in the contract
//     and there won't be one: configuration goes through the connectors,
//     which have their own guard rails (plan, dry-run, review). Nothing here
//     is prepared "for later" — a write extension would first require
//     redoing this reasoning.
//  2. NOTHING SECRET IN THE LOGS. AskRequest.Community is an operational
//     secret, same as for connectors: it never appears in a slog call, not
//     even at DEBUG, and no more so in the text of an error — a response's
//     Err field travels all the way up to an operator's UI and to the
//     controller's logs.
//  3. EVERYTHING IS BOUNDED, and by the proxy. Both the row count and the
//     wait: the question comes from elsewhere, the memory is here.
//
// Worth noting, about what this file does NOT check: the dial allowlist
// (MESHCRAFT_DIAL_ALLOW) does not apply to the walk. It bounds the TCP
// RELAY, the only path through which the controller routes arbitrary
// traffic; an SNMP read behaves exactly like an ordinary poll or a range
// sweep, neither of which consults it either. Two different rules for
// reaching the same device would end up allowing its monitoring while
// refusing its diagnosis.

// handleAsk dispatches a question. This is the function wired into the
// tunnel (see cmd/proxy/tunnel.go).
//
// An UNKNOWN question returns an explicit Err, never silence: a proxy older
// than its controller is a normal case in a fleet being upgraded
// progressively — that's exactly the reason the Ask field exists. The
// operator must read "this proxy doesn't know this question", not "no
// response", which would be confused with a broken tunnel.
func handleAsk(ctx context.Context, req tunnel.AskRequest) tunnel.AskResponse {
	switch req.Ask {
	case tunnel.AskSNMPWalk:
		return askSNMPWalk(ctx, req)
	case tunnel.AskSourceAddr:
		return askSourceAddr(req)
	}
	return tunnel.AskResponse{
		Err: "question unknown to this proxy: " + quotedAsk(req.Ask) +
			" (proxy older than the controller?)",
	}
}

// quotedAsk renders the question's name in a form fit for logging and
// display: quoted, bounded, escaped.
//
// The name comes FROM THE WIRE. Copying it verbatim into an Err would put
// arbitrary text — a thousand characters, newlines, terminal escape
// sequences — into a log and into a UI.
func quotedAsk(ask string) string {
	if ask == "" {
		return "(empty)"
	}
	const maxLen = 40
	if len(ask) > maxLen {
		ask = ask[:maxLen] + "…"
	}
	return strconv.Quote(ask)
}

// --- AskSNMPWalk ---

// askWalker: the strict minimum needed to walk. Isolated behind an
// interface for the same reason as snmppoll.Client — the part that gets
// things wrong is the PDU TRANSLATION, and it must be testable without a
// device or a network.
//
// *snmppoll.Session satisfies it as-is (see dialAskSNMP): the interface
// describes a need, it does not describe a local implementation.
type askWalker interface {
	BulkWalk(rootOID string, walkFn gosnmp.WalkFunc) error
	Close() error
}

// askDial is replaceable by tests. Nil = real SNMP.
var askDial func(tunnel.AskRequest) (askWalker, error)

// errAskTruncated stops the walk at the row limit. An INTERNAL sentinel:
// reaching an intended limit is not a failure, and it never escapes this file.
var errAskTruncated = errors.New("row limit reached")

// askSNMPWalk walks req.OID on req.IP and returns the rows read.
func askSNMPWalk(ctx context.Context, req tunnel.AskRequest) tunnel.AskResponse {
	var ip, err = askIP(req.IP)
	if err != nil {
		return tunnel.AskResponse{Err: err.Error()}
	}
	var oid string
	oid, err = askOID(req.OID)
	if err != nil {
		return tunnel.AskResponse{Err: err.Error()}
	}

	var dial = askDial
	if dial == nil {
		dial = dialAskSNMP
	}
	// The session dials the VALIDATED address, not the raw string: what was
	// verified and what gets contacted must be the same thing, otherwise
	// the check checks nothing.
	req.IP = ip
	var w askWalker
	w, err = dial(req)
	if err != nil {
		return tunnel.AskResponse{Err: "could not open SNMP session to " + ip + ": " + err.Error()}
	}
	defer w.Close() //nolint:errcheck

	var rows, truncated, walkErr = askRows(ctx, w, oid, askMaxRows(req.MaxRows))
	var resp = tunnel.AskResponse{Rows: rows, Truncated: truncated}
	if walkErr != nil {
		// The rows ALREADY read go out along with the error, they are not
		// discarded: for a device that responds and then goes silent, "here
		// are the first 40 rows and here's why it stopped" is infinitely
		// better than nothing at all.
		resp.Err = "walk interrupted after " + strconv.Itoa(len(rows)) +
			" row(s): " + walkErr.Error()
	}
	// Never the community — neither here nor anywhere else (see this file's header).
	slog.Debug("snmp_walk question", "ip", ip, "oid", oid,
		"rows", len(rows), "truncated", truncated)
	return resp
}

// askRows runs the walk and returns the rows, the truncation flag, and any
// error.
func askRows(ctx context.Context, w askWalker, oid string,
	maxRows int) ([]tunnel.AskRow, bool, error) {
	var rows []tunnel.AskRow
	var truncated bool
	var err = w.BulkWalk(oid, func(pdu gosnmp.SnmpPDU) error {
		// ctx is checked INSIDE the walk callback: gosnmp doesn't take one,
		// and this is the only point between two PDUs where we can bail out
		// (same pattern as the metrics side, snmppoll.pollClient.BulkWalk).
		// Without this, the deadline granted by the controller would just be
		// a wish.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(rows) >= maxRows {
			// The ONE ROW TOO MANY isn't kept: it only serves to establish
			// that there was more. Truncated therefore says exactly "there
			// was more", never "maybe" — a walk that lands exactly on the
			// limit is not flagged as truncated, because it isn't.
			truncated = true
			return errAskTruncated
		}
		rows = append(rows, askRow(pdu))
		return nil
	})
	if errors.Is(err, errAskTruncated) {
		err = nil
	}
	// Truncated speaks ONLY of the MaxRows limit, never of a walk cut short
	// by a timeout or a silent device: those two are conveyed in Err, with
	// their cause. Mixing the two would make an outage look like a bounded
	// response.
	return rows, truncated, err
}

// askMaxRows applies the contract's limit: default and HARD CEILING.
//
// The ceiling is never relaxed based on the request. A walk under `1.3.6.1`
// on a switch returns tens of thousands of rows: the memory holding them
// belongs to the proxy, which runs on a minimal VM or a Raspberry Pi.
func askMaxRows(want int) int {
	if want <= 0 {
		return tunnel.DefaultAskRows
	}
	if want > tunnel.MaxAskRows {
		return tunnel.MaxAskRows
	}
	return want
}

// askRow translates a PDU into a response row.
func askRow(pdu gosnmp.SnmpPDU) tunnel.AskRow {
	return tunnel.AskRow{
		// gosnmp prefixes OIDs with a dot; the controller, for its part,
		// sends them without one. Returning either form depending on the
		// source would force the caller to normalize before any comparison,
		// and it wouldn't do so everywhere.
		OID:   strings.TrimPrefix(pdu.Name, "."),
		Type:  pduTypeLabel(pdu.Type),
		Value: pduValueText(pdu),
	}
}

// pduTypeLabel names an ASN.1 type the way a MIB would name it.
//
// An operator must be able to understand what they're reading: "Counter64"
// says a value is interpreted as a delta and can wrap around, "Gauge32" says
// it reads as-is, "TimeTicks" says it counts hundredths of a second. The
// numeric tag says none of that. An unknown type renders its tag in
// hexadecimal rather than a false label.
func pduTypeLabel(t gosnmp.Asn1BER) string {
	switch t {
	case gosnmp.Boolean:
		return "Boolean"
	case gosnmp.Integer:
		return "Integer"
	case gosnmp.BitString:
		return "BitString"
	case gosnmp.OctetString:
		return "OctetString"
	case gosnmp.Null:
		return "Null"
	case gosnmp.ObjectIdentifier:
		return "ObjectIdentifier"
	case gosnmp.ObjectDescription:
		return "ObjectDescription"
	case gosnmp.IPAddress:
		return "IPAddress"
	case gosnmp.Counter32:
		return "Counter32"
	case gosnmp.Gauge32:
		return "Gauge32"
	case gosnmp.TimeTicks:
		return "TimeTicks"
	case gosnmp.Opaque:
		return "Opaque"
	case gosnmp.NsapAddress:
		return "NsapAddress"
	case gosnmp.Counter64:
		return "Counter64"
	case gosnmp.Uinteger32:
		return "Uinteger32"
	case gosnmp.OpaqueFloat:
		return "OpaqueFloat"
	case gosnmp.OpaqueDouble:
		return "OpaqueDouble"
	// The three SNMPv2 exceptions aren't values but RESPONSES: "this object
	// doesn't exist", "this instance doesn't exist", "end of view". Rendering
	// them as-is is the whole point of the diagnostic — it's exactly what
	// someone is trying to find out by requesting a walk.
	case gosnmp.NoSuchObject:
		return "NoSuchObject"
	case gosnmp.NoSuchInstance:
		return "NoSuchInstance"
	case gosnmp.EndOfMibView:
		return "EndOfMibView"
	}
	return "Type0x" + strconv.FormatUint(uint64(t), 16)
}

// pduValueText renders the value AS TEXT.
//
// This conversion stays HERE and does NOT join snmppoll, whose session is
// now shared (see dialAskSNMP). This isn't an oversight: snmppoll.toFloat
// and snmppoll.asString serve a different need and must stay there. They
// produce metrics — a float64, and the REFUSAL of anything that isn't one,
// because a counter converted to zero would read as a dead link. The
// diagnostic, on the other hand, must render EXACTLY everything the device
// said: a Counter64 beyond 2^53 doesn't survive a float64, six raw bytes of
// a MAC address don't survive json.Marshal, and "NoSuchObject" is precisely
// the answer that was being sought — right where the metric path must
// ignore it.
//
// Two needs, hence two conversions, and it's the CONNECTION that had to be
// unified: a connection convention written twice ends up silently describing
// two different devices; two deliberate conversions, each one cross-checking
// the other, fool no one.
//
// This is the contract's decision, and it's structural: a 64-bit counter
// doesn't fit into a float without loss, and it's precisely counters that
// get queried here. `ifHCInOctets` on a busy link exceeds 2^53 within a few
// months, and a JSON payload that rounded it would show no sign of it — the
// reader would see a wrong rate with no way to know why. Hence going
// through big.Int (gosnmp.ToBigInt) for integers: exact, regardless of
// whichever Go type the library chose.
func pduValueText(pdu gosnmp.SnmpPDU) string {
	switch pdu.Type {
	case gosnmp.Boolean, gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32,
		gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32:
		return gosnmp.ToBigInt(pdu.Value).String()
	case gosnmp.OpaqueFloat:
		var f, ok = pdu.Value.(float32)
		if ok {
			return strconv.FormatFloat(float64(f), 'g', -1, 32)
		}
	case gosnmp.OpaqueDouble:
		var f, ok = pdu.Value.(float64)
		if ok {
			return strconv.FormatFloat(f, 'g', -1, 64)
		}
	case gosnmp.ObjectIdentifier:
		var s, ok = pdu.Value.(string)
		if ok {
			return strings.TrimPrefix(s, ".")
		}
	case gosnmp.Null, gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		// Nothing to render: the information is ENTIRELY in the type.
		// Fabricating a textual "null" or "0" would read as a value.
		return ""
	}
	// OctetString, IPAddress, NsapAddress, Opaque, and any unexpected type:
	// gosnmp returns either raw bytes or an already formatted string.
	if pdu.Value == nil {
		return ""
	}
	var raw, isBytes = pdu.Value.([]byte)
	if isBytes {
		return octetStringText(raw)
	}
	var text, isText = pdu.Value.(string)
	if isText {
		return octetStringText([]byte(text))
	}
	return gosnmp.ToBigInt(pdu.Value).String()
}

// octetStringText renders an OctetString without ever corrupting the response.
//
// The implementation MOVED into `snmppoll.OctetText`, and what remains here
// is the delegation. The original comment explained why this conversion
// didn't join `snmppoll` — because `toFloat` and `asString` serve a
// different need, the metric one, where a counter converted to zero would
// read as a dead link. That reasoning still holds and those two functions
// stay separate there.
//
// What changed is that a SECOND caller appeared inside `snmppoll` itself:
// LLDP neighbor collection has exactly the same fidelity requirement as the
// diagnostic, and on the same kind of bytes — a `lldpRemChassisId` made of
// six raw bytes. Duplicating it would have produced two implementations of
// the same rule, whose divergence would have produced NO error at all: a
// chassis seen by the diagnostic simply not comparable to the same chassis
// seen by collection. The local name is kept because it's the one
// `pduValueText` reads, and because this package's tests already verify the
// rule against real bytes.
func octetStringText(b []byte) string {
	return snmppoll.OctetText(b)
}

// askOID validates the root to walk.
//
// The string comes from the wire and heads into a networking library: we
// check ourselves that it has the shape of an OID, so as to return "invalid
// oid" rather than a gosnmp encoding error that nobody will be able to
// interpret. An empty OID is not replaced by a default root: walking `1.3`
// because the request was incomplete would produce tens of thousands of
// rows that nobody asked for.
func askOID(oid string) (string, error) {
	// The leading dot is accepted and stripped: both notations circulate
	// (gosnmp returns ".1.3.6...", MIBs are written "1.3.6...") and refusing
	// one of the two would be a gratuitous trap.
	var clean = strings.TrimPrefix(strings.TrimSpace(oid), ".")
	if clean == "" {
		return "", errors.New("missing oid: there is no default root to walk")
	}
	var seg string
	for _, seg = range strings.Split(clean, ".") {
		// A segment is an unsigned 32-bit integer, and its notation is
		// UNIQUE: "1.00.3" designates the same object as "1.0.3" but doesn't
		// compare equal as text, so it's refused instead of being left to
		// sow confusion.
		var _, err = strconv.ParseUint(seg, 10, 32)
		if err != nil || (len(seg) > 1 && seg[0] == '0') {
			return "", errors.New("invalid oid: " + strconv.Quote(clean))
		}
	}
	return clean, nil
}

// askIP validates the targeted device.
//
// An ADDRESS, never a name: the contract makes it an address (the proxy
// doesn't know the controller's identifiers), and letting a name through
// would resolve DNS against an arbitrary string coming off the wire. Unmap
// brings a "::ffff:..." form back to its v4 notation, so the rendered text
// is comparable to the inventory's.
func askIP(ip string) (string, error) {
	var addr, err = netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", errors.New("invalid device address: " + strconv.Quote(ip))
	}
	return addr.Unmap().String(), nil
}

// dialAskSNMP opens the SNMP session for a question.
//
// NO connection convention here, and that's the whole point of this
// function: snmppoll.Dial is the ONLY place where the project writes down
// how a device is reached (v2c, 2 s timeout, a single retry, empty
// community = public, port 161). A diagnostic question and a monitoring
// poll must describe the SAME device: if a manual walk succeeded with wider
// parameters than the poll, one would conclude that the device exposes OIDs
// that monitoring is "losing", when in fact it would be the question that
// cheated.
//
// These five values used to be copied here as a batch. The copy itself
// wasn't the danger — its INVISIBILITY was: nothing would have flagged that
// a timeout observed on only one side made the two sides describe different
// devices, and the outage would have been chased inside the device instead.
//
// *snmppoll.Session satisfies askWalker with no adapter: its BulkWalk method
// has gosnmp's signature, the one that passes RAW PDUs in walk order — the
// diagnostic must render exactly what it read. snmppoll.Client, the other
// view of the same session, aggregates by interface index for metrics: that
// is not what's needed here, a table rendered to an operator must not be
// deduplicated.
func dialAskSNMP(req tunnel.AskRequest) (askWalker, error) {
	// DeviceID stays empty: the proxy doesn't know the controller's
	// identifiers (see tunnel.AskRequest.IP) and Dial has no use for it —
	// it doesn't tag anything with it.
	var s, err = snmppoll.Dial(snmppoll.Target{
		IP:        req.IP,
		Port:      req.Port,
		Community: req.Community,
	})
	if err != nil {
		// EXPLICIT nil, not "return s, err": a nil *Session boxed into an
		// interface produces a non-nil askWalker, which a future "if w !=
		// nil" would get wrong. The current form forbids that trap instead
		// of relying on nobody hitting it.
		return nil, err
	}
	return s, nil
}

// --- AskSourceAddr ---

// askSourceAddr returns the local address the kernel WOULD USE to reach req.IP.
//
// The obvious method — looking, among the proxy's declared addresses, for
// the one on the device's subnet — is WRONG whenever the proxy reaches it
// via routing. Observed on the real installation: the proxy exposes
// 10.42.0.67/24 and 172.17.0.1/16, the lab device sits at 10.99.200.101 — no
// match at all, and yet it reaches it. The obvious algorithm would return
// nothing, and an operator would conclude the proxy can't see the device.
//
// Only the routing table knows. We query it by "connecting" a UDP socket to
// the target: no packet is sent (UDP doesn't establish anything), the
// kernel simply resolves the route to pick a source address, and LocalAddr
// reports it. The port therefore plays no role at all — we use 161 just to
// keep the log readable.
func askSourceAddr(req tunnel.AskRequest) tunnel.AskResponse {
	var ip, err = askIP(req.IP)
	if err != nil {
		return tunnel.AskResponse{Err: err.Error()}
	}
	var c net.Conn
	c, err = net.Dial("udp", net.JoinHostPort(ip, "161"))
	if err != nil {
		// No route: SAY SO. Returning an empty address would look like
		// success, and we'd end up configuring a trap destination the
		// device would never reach.
		return tunnel.AskResponse{
			Err: "no route to " + ip + " from this proxy: " + err.Error(),
		}
	}
	defer c.Close() //nolint:errcheck

	var local, ok = c.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil {
		return tunnel.AskResponse{Err: "unreadable local address for " + ip}
	}
	var addr netip.Addr
	addr, ok = netip.AddrFromSlice(local.IP)
	if !ok {
		return tunnel.AskResponse{Err: "unreadable local address for " + ip}
	}
	// Unmap: a dual-stack socket returns "::ffff:10.42.0.67" depending on
	// the mood of the host's stack, and the controller compares this
	// response against inventory addresses written in v4 (same precaution
	// as classifyTrap).
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() {
		return tunnel.AskResponse{
			Err: "the kernel picked no source address for " + ip,
		}
	}
	slog.Debug("source_addr question", "ip", ip, "source", addr.String())
	// String(): no mask, no port, and no zone — this is exactly what the
	// controller will write into a trap destination.
	return tunnel.AskResponse{SourceAddr: addr.String()}
}
