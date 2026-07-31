package routeros

import (
	"bufio"
	"context"
	"crypto/md5" //nolint:gosec // imposed by legacy login protocol, cf. loginLegacy
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// MikroTik BINARY API — the channel Winbox uses, and the THIRD on this platform.
//
// # Three channels, three protocols: do not confuse them
//
// This is the most common confusion on this product line, and it is costly because
// all three answer on adjacent ports, on the same device, with the same account:
//
//	80 / 443     REST. HTTP, JSON, Basic on every request. This is the transport
//	             for everything else in this connector (rest.go). It cannot execute
//	             a command that is not exposed as a menu GET/POST, and the
//	             controller's read tool is restricted to GET/HEAD.
//	8728 / 8729  BINARY API, this file. Proprietary protocol with « sentences »,
//	             words prefixed by their length, login by password or
//	             challenge/response. This is what MikroTik documentation calls
//	             « the API », and it is the channel through which a third-party
//	             client runs CONSOLE COMMANDS — `print`, `monitor`, `set` — without
//	             opening a terminal.
//	8291         WINBOX. Yet ANOTHER protocol, encrypted, undocumented, whose
//	             payload is an object serialization specific to the GUI tool. This
//	             file does NOT implement it and does not pretend to: saying « the
//	             Winbox API » in practice means the binary API above, because it is
//	             the only one of the two that is specified.
//
// The capability that distinguishes them already exists and is OBSERVED (routeros.go):
// `Winbox` for 8291, `BinaryAPI` for 8728/8729, `RESTAPI` for the web. The
// `BinaryAPI` comment used to say « that this connector does not use »; this file
// makes that false, and the comment was updated with it.
//
// # What this channel provides that REST does not
//
// One thing, and it is worth the file: commands that are not menu reads. The case
// in point is `/interface/ethernet/monitor`, the ONLY source of SFP+ optical
// diagnostics on RouterOS (module temperature, supply voltage, bias current,
// transmitted and received power). It is out of reach for a GET-only tool — that is
// stated plainly in `monitoringFor` — and the connector therefore declared optical
// power « neither channel nor unavailability entry ». This file gives it a channel.
//
// # What is NOT verified, and must be known before relying on it
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ NO BYTE OF THIS PROTOCOL HAS BEEN EXCHANGED WITH THE REFERENCE DEVICE.      │
// │                                                                            │
// │ The only path available to the CRS326 from the development host is an HTTP  │
// │ tool restricted to GET/HEAD (cf. the header of rest.go): it cannot open   │
// │ a raw TCP socket on 8728. What IS OBSERVED is that the service listens —    │
// │ `/ip/service` returns `api` on 8728 and `api-ssl` on 8729, not disabled —   │
// │ and that both ports accept the TCP connection without speaking HTTP. The    │
// │ PROTOCOL itself is implemented from the specification published by          │
// │ MikroTik (help.mikrotik.com, « API » page), and exercised against a         │
// │ SIMULATED device in api_test.go.                                           │
// │                                                                            │
// │ Consequence on declarations: the `api` channel is declared with a floor     │
// │ DELIBERATELY higher than REST's, so `Best` does not make this never-used    │
// │ channel the main path for an entire fleet by the sole effect of a smaller   │
// │ number. Same rule, and for the same reason, as this connector's SNMP        │
// │ channel.                                                                   │
// └─────────────────────────────────────────────────────────────────────────────┘

// Binary API ports, OBSERVED in `/ip/service` on the reference device.
const (
	// APIPort: cleartext API. The login password travels in plain text on modern
	// firmware (cf. Login): confidentiality relies entirely on the proxy tunnel,
	// exactly like REST over HTTP.
	APIPort = 8728
	// APITLSPort: API over TLS. See the DialAPI comment for why listening on this
	// port is NOT enough to make it usable.
	APITLSPort = 8729
)

// maxWordLen bounds a word returned by the device.
//
// The length prefix can announce up to 2³² - 1 bytes (five-byte encoding,
// cf. encodeWordLength). Allocating what the header requests would let a
// misbehaving device — or a hostile intermediary on the path — exhaust the
// controller's memory with FIVE bytes sent. The bound is therefore set at read
// time, not write time: we do not protect against what we emit ourselves.
//
// 1 MiB: the largest response measured on this platform is 12.7 KiB
// (`/interface`, 29 interfaces), and an API word carries ONE attribute value, not
// an entire response. Three orders of magnitude of margin, and an explicit refusal
// beyond rather than a silent allocation.
const maxWordLen = 1 << 20

// maxWordsPerSentence bounds the number of words in a sentence.
//
// Same reasoning as above, applied to count rather than size: a sentence with no
// empty termination word would grow the slice indefinitely, one zero-length byte at
// a time. The largest RouterOS record OBSERVED carries 87 fields
// (`/interface/ethernet`); 4096 leaves room to see it coming without room to
// overflow.
const maxWordsPerSentence = 4096

// apiTimeout bounds a round trip on this channel.
//
// 20 s, the same value as `restTimeout` and for the same reason: the median
// measured on this platform is on the order of a hundred milliseconds, but a
// RouterOS recalculating its bridge table or a tunnel that just came back takes
// much longer. Here there is an additional reason specific to this channel:
// `monitor` queries an transceiver's I²C bus, which is the slowest on the board.
const apiTimeout = 20 * time.Second

// Sentence is a protocol sentence: a sequence of words, terminated by a zero-length
// word.
//
// The termination word is NOT in the slice. That is deliberate: it is framing, not
// content, and leaving it visible would force every caller to skip it — and
// therefore forget it once.
type Sentence []string

// Reply is a RESPONSE sentence, decomposed.
//
// Decomposition is done here rather than at the caller because the shape of response
// words is a protocol property, not a command property: `=name=value` for an
// attribute, `.tag=…` for an API attribute, and BARE words for the rest.
type Reply struct {
	// Type is the first word: `!re`, `!done`, `!trap`, `!fatal`, `!empty`.
	Type string
	// Attrs carries `=name=value` words, in the same form REST returns ([Row]).
	// That is not a convenient coincidence: it is the SAME data model on the device
	// side, and sharing it lets telemetry read a record without knowing which channel
	// it arrived on.
	Attrs Row
	// Tag is the `.tag` value, empty if the sentence does not carry one.
	Tag string
	// Bare carries words that are NEITHER an attribute NOR an API attribute.
	//
	// This field is not decorative: `!fatal` carries its reason exactly this way, as
	// one bare word (« cannot log in », « session terminated on another interface »).
	// Dropping it would surface a session closure without a reason to the operator —
	// the worst message, the one that forces a full retry to understand.
	Bare []string
}

// Binary API response sentence types.
const (
	replyData  = "!re"
	replyDone  = "!done"
	replyTrap  = "!trap"
	replyFatal = "!fatal"
	// replyEmpty: introduced by RouterOS 7.18. A distinct « there is nothing »
	// response, separate from a data-less `!done`. Treated as a `!re` without
	// attributes, which is exactly what it means.
	replyEmpty = "!empty"
)

// APIConn is an open session on the binary API.
//
// « Session » is NOT loose wording here, and that is the most important difference
// from REST: the binary API is a LONG TCP connection, authenticated ONCE via
// `/login`, and authentication state lives in the connection. Hence two consequences
// REST does not have:
//
//   - there is something to CLOSE, and forgetting it leaves a session open on the
//     device. `/ip/service` OBSERVES `max-sessions: 20` on the `api` entry: that is
//     not an invitation to waste them, the exploration campaign having shown that
//     connections accumulate there faster than they close;
//   - exchanges are NOT concurrent. The protocol is a stream of sentences on one
//     socket: two commands emitted at once would mix their responses. The `.tag`
//     marking exists precisely to allow the opposite, and this connector does NOT
//     use it — see Do, which explains why a response tagged from another round is
//     REFUSED rather than sorted.
type APIConn struct {
	conn net.Conn
	br   *bufio.Reader
	// Steps is this session's transcript, in order. Same form as the [Client]'s:
	// the caller displays both without distinguishing them.
	Steps []Step
	// tag numbers commands. See Do: it is not used to multiplex, it is used to
	// DETECT stream misalignment.
	tag int
	// dead records that a `!fatal` has passed. The device closed on its side; any
	// following command would wait for a response that will never come, and a timeout
	// message would not say why.
	dead string
}

func (c *APIConn) step(ok bool, what string, detail ...string) {
	c.Steps = append(c.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// --- Framing: length encoding ---

// encodeWordLength encodes a word length per the MikroTik specification.
//
// This is the part of the protocol where an error does NOT show up immediately: a
// mis-framed word does not produce a syntax error, it SHIFTS the stream — the device
// reads the end of one word as the start of the next, and the session ends in
// `!fatal` several commands later, with no apparent link to the cause. Hence a table
// copied verbatim from the documentation, and a test that exercises it bound by
// bound.
//
//	Length                              Bytes   Encoding
//	0     ≤ len ≤ 0x7F                    1       len
//	0x80  ≤ len ≤ 0x3FFF                  2       len | 0x8000
//	0x4000 ≤ len ≤ 0x1FFFFF               3       len | 0xC00000
//	0x200000 ≤ len ≤ 0xFFFFFFF            4       len | 0xE0000000
//	len ≥ 0x10000000                      5       0xF0 then len on four bytes
//
// The point that reads wrong in the table and decides everything: length is encoded
// BIG-ENDIAN (most significant byte first), the opposite of SwOS addresses which are
// little-endian (cf. `DecodeIPv4` in the neighboring connector). Two protocols from
// the same vendor, two opposite conventions.
func encodeWordLength(n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("negative word length (%d): cannot encode", n)
	}
	switch {
	case n <= 0x7F:
		return []byte{byte(n)}, nil
	case n <= 0x3FFF:
		return []byte{byte(n>>8) | 0x80, byte(n)}, nil
	case n <= 0x1FFFFF:
		return []byte{byte(n>>16) | 0xC0, byte(n >> 8), byte(n)}, nil
	case n <= 0xFFFFFFF:
		return []byte{byte(n>>24) | 0xE0, byte(n >> 16), byte(n >> 8), byte(n)}, nil
	case int64(n) <= 0xFFFFFFFF:
		return []byte{0xF0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}, nil
	}
	return nil, fmt.Errorf("word length of %d bytes: beyond the 2^32-1 the protocol "+
		"encoding can carry", n)
}

// readWordLength reads a length prefix.
//
// Returns the length, and an error that NAMES the misalignment when the first byte
// is not a valid prefix. Values 0xF8 through 0xFF are reserved by the specification
// for control bytes: encountering them does not mean « giant word », it means we are
// no longer reading the stream we think we are — and continuing would consume random
// bytes until a timeout whose message would say nothing.
func readWordLength(r *bufio.Reader) (int, error) {
	var b0, err = r.ReadByte()
	if err != nil {
		return 0, err
	}
	var extra int
	var acc int
	switch {
	case b0&0x80 == 0x00:
		return int(b0), nil
	case b0&0xC0 == 0x80:
		extra, acc = 1, int(b0&0x3F)
	case b0&0xE0 == 0xC0:
		extra, acc = 2, int(b0&0x1F)
	case b0&0xF0 == 0xE0:
		extra, acc = 3, int(b0&0x0F)
	case b0 == 0xF0:
		extra, acc = 4, 0
	default:
		return 0, fmt.Errorf("invalid length prefix (0x%02X): values 0xF8-0xFF are "+
			"reserved by the protocol for control bytes. The stream is MISALIGNED — a "+
			"previous word was mis-framed — and continuing would consume random bytes", b0)
	}
	var i int
	for i = 0; i < extra; i++ {
		var b byte
		b, err = r.ReadByte()
		if err != nil {
			return 0, err
		}
		acc = acc<<8 | int(b)
	}
	return acc, nil
}

// --- Framing: sentences ---

// writeSentence emits a sentence and its empty termination word.
//
// Writing is done in ONE network call, buffer composed first. Two reasons, and the
// second is the real one: one word per packet would multiply round trips over a WAN
// tunnel, and especially an interrupted write mid-sentence would leave the device
// waiting for the rest — a mute session that no timeout on our side unblocks.
func (c *APIConn) writeSentence(words Sentence) error {
	if c.dead != "" {
		return fmt.Errorf("binary API session already closed by the device (%s): "+
			"nothing more can be sent on it", c.dead)
	}
	var buf []byte
	var w string
	for _, w = range words {
		var prefix, err = encodeWordLength(len(w))
		if err != nil {
			return err
		}
		buf = append(buf, prefix...)
		buf = append(buf, w...)
	}
	// The EMPTY word terminates the sentence. Without it, the device waits
	// indefinitely: there is no other end marker in this protocol.
	buf = append(buf, 0x00)
	var _, err = c.conn.Write(buf)
	return err
}

// readSentence reads a sentence until its empty word.
func (c *APIConn) readSentence() (Sentence, error) {
	var out Sentence
	for {
		var n, err = readWordLength(c.br)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return out, nil
		}
		if n > maxWordLen {
			return nil, fmt.Errorf("word of %d bytes announced by the device: "+
				"beyond the %d-byte limit this connector accepts. Either the stream is "+
				"misaligned, or something on the path is not serving the binary API — in "+
				"either case, allocating what the header requests would be a way to exhaust "+
				"the controller's memory with five bytes", n, maxWordLen)
		}
		if len(out) >= maxWordsPerSentence {
			return nil, fmt.Errorf("more than %d words in a single sentence without "+
				"termination: the stream is misaligned", maxWordsPerSentence)
		}
		var raw = make([]byte, n)
		if _, err = io.ReadFull(c.br, raw); err != nil {
			return nil, err
		}
		out = append(out, string(raw))
	}
}

// parseReply decomposes a response sentence. PURE function, exercised on hand-written
// sentences: all reads depend on this translation, and it must not be verifiable only
// with hardware at hand.
func parseReply(s Sentence) Reply {
	var out = Reply{Attrs: Row{}}
	if len(s) == 0 {
		return out
	}
	out.Type = s[0]
	var w string
	for _, w = range s[1:] {
		switch {
		case strings.HasPrefix(w, "="):
			// `=name=value`, and the value may contain `=` characters: split at the FIRST
			// separator after the name. Splitting at the last would truncate any value
			// that contains them — LLDP descriptions OBSERVED on this platform do.
			var body = w[1:]
			var eq = strings.Index(body, "=")
			if eq < 0 {
				// `=name` without value: a PRESENT and empty attribute. The protocol uses
				// this for flags (`=once=` on `monitor`), and distinguishing it from an
				// absent attribute is what keeps Row.Get honest.
				out.Attrs[body] = ""
				continue
			}
			out.Attrs[body[:eq]] = body[eq+1:]
		case strings.HasPrefix(w, ".tag="):
			out.Tag = strings.TrimPrefix(w, ".tag=")
		default:
			out.Bare = append(out.Bare, w)
		}
	}
	return out
}

// --- Commands ---

// Do executes a command and returns the records it produces.
//
// READ OR WRITE: this function does not know which. The caller chooses a command, and
// this connector's commands are for now all `print` and `monitor` — therefore reads.
// Naming that honestly beats pretending a `Do` is harmless.
//
// `!re` sentences ARE the records, `!done` closes the exchange. The `!done` itself
// may carry attributes (`=ret=` on `/login`, cf. Login), returned in the last slice
// position when it carries any: dropping them would lose the login challenge, which
// arrives by no other path.
//
// # `.tag` marking is for DETECTION, not multiplexing
//
// Each command carries an incremented `.tag`, and a response whose tag does not match
// is REFUSED — the session is declared lost rather than sorted. That is the opposite
// of what this field is usually for, and it is deliberate: this connector never emits
// two commands in flight, so a response from another round can only mean one thing —
// the stream is shifted, and what we read is not the response to what we asked. Sorting
// silently would then return one record's values for another, i.e. credible and false
// telemetry, exactly what the SNMP poller's strict indexing rule exists to avoid.
//
// `!trap` is returned as an ERROR carrying the device's message, never as an empty
// record: on a read command, a refusal that read as « no data » would conclude « this
// menu is empty » where the device said « you do not have permission ».
func (c *APIConn) Do(ctx context.Context, command string, args ...string) ([]Row, error) {
	if c.dead != "" {
		return nil, fmt.Errorf("binary API session closed by the device (%s)", c.dead)
	}
	if !strings.HasPrefix(command, "/") {
		return nil, fmt.Errorf("binary API command %q refused: a command of this "+
			"protocol starts with \u00ab / \u00bb — otherwise the device would read it as "+
			"an attribute word and respond with an unrelated `!fatal`", command)
	}
	var err = c.deadline(ctx)
	if err != nil {
		return nil, err
	}

	c.tag++
	var tag = strconv.Itoa(c.tag)
	var phrase = append(Sentence{command}, args...)
	phrase = append(phrase, ".tag="+tag)
	err = c.writeSentence(phrase)
	if err != nil {
		c.step(false, "send of "+command+" (binary API)", err.Error())
		return nil, err
	}

	var rows []Row
	for {
		var raw Sentence
		raw, err = c.readSentence()
		if err != nil {
			c.step(false, "read of response to "+command, err.Error())
			return rows, err
		}
		var rep = parseReply(raw)
		if rep.Tag != "" && rep.Tag != tag {
			c.dead = "response tagged \u00ab " + rep.Tag + " \u00bb received while \u00ab " + tag +
				"\u00bb was expected"
			c.step(false, "binary API stream misaligned", c.dead)
			return nil, fmt.Errorf("binary API response tagged \u00ab %s \u00bb while command %s "+
				"carried \u00ab %s \u00bb: this connector never emits two commands in flight, so "+
				"this shift cannot be interleaving. The session is abandoned rather than sorted — "+
				"returning one record's values for another would produce credible and false "+
				"telemetry", rep.Tag, command, tag)
		}
		switch rep.Type {
		case replyData:
			rows = append(rows, rep.Attrs)
		case replyEmpty:
			// « there is nothing », stated explicitly (RouterOS ≥ 7.18). No record to
			// add, and especially not an empty record, which would count as one.
		case replyDone:
			if len(rep.Attrs) > 0 {
				rows = append(rows, rep.Attrs)
			}
			c.step(true, "command "+command+" (binary API)",
				fmt.Sprintf("%d record(s)", len(rows)))
			return rows, nil
		case replyTrap:
			var msg = rep.Attrs.Get("message")
			if msg == "" {
				msg = strings.Join(rep.Bare, " ")
			}
			c.step(false, command+" refused by the device (binary API)", msg)
			return rows, fmt.Errorf("%s refused by the device: %s", command,
				trapMeaning(msg, rep.Attrs.Get("category")))
		case replyFatal:
			c.dead = strings.Join(append(rep.Bare, rep.Attrs.Get("message")), " ")
			if strings.TrimSpace(c.dead) == "" {
				c.dead = "no reason returned"
			}
			c.step(false, "binary API session closed by the device", c.dead)
			return rows, fmt.Errorf("the device closed the binary API session: %s",
				c.dead)
		default:
			// An unknown first word is not data: it is a protocol version that moved, or
			// a shifted stream. We SAY so rather than ignore it — ignoring would loop until
			// timeout on a sentence that never closes.
			c.dead = "sentence of unknown type \u00ab " + rep.Type + " \u00bb"
			c.step(false, "binary API response not understood", c.dead)
			return rows, fmt.Errorf("binary API sentence of unknown type \u00ab %s \u00bb in "+
				"response to %s: neither !re, !done, !trap, nor !fatal. The stream is shifted, "+
				"or this firmware speaks a protocol version this connector does not know",
				rep.Type, command)
		}
	}
}

// trapMeaning translates what a refusal on this channel means.
//
// Three messages only, but they are the three that send you looking in the wrong
// place. « no such command » on the binary API does NOT mean the same thing as REST's
// 400 « no such command or directory »: here there is no submenu 500 trap (cf.
// restStatusMeaning), the response is frank.
func trapMeaning(message, category string) string {
	var low = strings.ToLower(message)
	switch {
	case strings.Contains(low, "cannot log in"), strings.Contains(low, "invalid user"):
		return message + " — credentials refused by the device. On this platform, the " +
			"account must also carry the `api` policy in its group: a valid secret in a " +
			"group that lacks it produced this same refusal"
	case strings.Contains(low, "no such command"), strings.Contains(low, "no such item"):
		return message + " — this menu or command does not exist on this model or " +
			"firmware. Unlike REST, this channel does not confuse an absent menu with " +
			"a menu that contains only sub-menus"
	case strings.Contains(low, "not enough permissions"):
		return message + " — the account is authenticated but its group does not carry " +
			"the required policy (`read` to read, `write` to write, `api` for this " +
			"channel)"
	}
	if category != "" {
		return message + " (category " + category + ")"
	}
	return message
}

// deadline sets the socket deadline from the context, bounded by apiTimeout.
//
// It is set on every command, not once at open: an absolute deadline set at the start
// would expire mid-way through the third read of a campaign, and the error message
// would then speak of a timeout on a command that was not slow.
func (c *APIConn) deadline(ctx context.Context) error {
	var until = time.Now().Add(apiTimeout)
	var fromCtx, ok = ctx.Deadline()
	if ok && fromCtx.Before(until) {
		until = fromCtx
	}
	return c.conn.SetDeadline(until)
}

// Close closes the session.
//
// `/quit` is sent BEFORE closing the socket, and that is not politeness: a socket cut
// without quitting leaves the device counting the session until its own inactivity
// timeout, and `/ip/service` OBSERVES `max-sessions: 20` on the `api` entry. A
// connector that opens and closes twenty times in a row shuts its own door, and the
// symptom — « cannot log in » on a secret that is nonetheless good — gives nothing
// that points to the cause.
//
// The `/quit` error is IGNORED: the device responds to `/quit` with a `!fatal`, which
// is normal behavior and not a failure. What matters is closing the socket, and that
// is the error returned.
func (c *APIConn) Close() error {
	if c.conn == nil {
		return nil
	}
	if c.dead == "" {
		// Short deadline: do not make close wait. Errors are deliberately not handled,
		// cf. the comment above.
		c.conn.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
		c.writeSentence(Sentence{"/quit"})                  //nolint:errcheck
	}
	return c.conn.Close()
}

// --- Login ---

// APIOptions configures session opening.
type APIOptions struct {
	// Port: 0 means [APIPort] cleartext, [APITLSPort] when TLS is requested.
	Port int
	// TLS: talk to `api-ssl` rather than `api`. Read the DialAPI comment before using
	// it — listening on the port is not enough to make it usable.
	TLS bool
	// ServerName: name presented in the TLS negotiation. Empty = the host.
	ServerName string
}

// DialAPI opens a session on the binary API, without authenticating.
//
// Authentication is a SEPARATE call ([APIConn.Login]) and not a parameter here, for a
// reason that matters on this platform: an accepted TCP connection and a refused
// `/login` are two different diagnoses — « the service is not listening / the tunnel
// is down » on one side, « the secret is wrong or the group does not carry the `api`
// policy » on the other. Merging them into one call would force guessing which of the
// two we have.
//
// # THE TLS CASE: the port listens, and that is not enough
//
// This is the most counter-intuitive limit of this channel, and it is structural
// rather than temporary.
//
// OBSERVED on the reference device: `/ip/service` serves `api-ssl` on 8729, NOT
// disabled — unlike `www-ssl`, which is `disabled` AND `invalid` for lack of a
// certificate. One might conclude 8729 is usable as-is. That is probably false, and
// here is why: RouterOS historically served `api-ssl` WITHOUT a certificate by
// negotiating an anonymous Diffie-Hellman suite (`ADH-…`). Anonymous suites are absent
// from Go's `crypto/tls`, by standard library design — they authenticate neither
// party, so they offer encryption without identity, which an active intermediary
// defeats easily.
//
// Practical consequence, and better read here than discover it: on a RouterOS where
// nobody installed a certificate, `TLS: true` will fail at negotiation. This file's
// TLS path is therefore useful only for a device whose operator installed a
// certificate — and it has NOT been exercised on hardware, any more than the rest of
// this file.
//
// `InsecureSkipVerify` is set, and that is a choice that must be stated aloud: a
// RouterOS certificate is self-signed by construction, and meshcraft holds no authority
// to validate it. Verifying it would fail 100% of opens; not verifying it reduces TLS
// to encryption without authentication, therefore the same trust level as cleartext
// REST — i.e. that of the proxy tunnel, which remains this connector's trust perimeter.
func DialAPI(ctx context.Context, host string, d driver.Dialer,
	opt APIOptions) (*APIConn, error) {
	var port = opt.Port
	if port == 0 {
		port = APIPort
		if opt.TLS {
			port = APITLSPort
		}
	}
	var addr = net.JoinHostPort(host, strconv.Itoa(port))

	var dctx, cancel = context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	var conn net.Conn
	var err error
	if d != nil {
		conn, err = d.DialContext(dctx, "tcp", addr)
	} else {
		var plain net.Dialer
		conn, err = plain.DialContext(dctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("binary API at %s unreachable: %w — `/ip/service` must "+
			"serve `api` (OBSERVED on 8728, not disabled on the reference device) and the "+
			"path to the port must be open", addr, err)
	}

	if opt.TLS {
		var name = opt.ServerName
		if name == "" {
			name = host
		}
		// See the « THE TLS CASE » section above for InsecureSkipVerify and what will
		// fail on a device without a certificate.
		var tc = tls.Client(conn, &tls.Config{
			ServerName:         name,
			InsecureSkipVerify: true, //nolint:gosec // self-signed cert, cf. TLS section
			MinVersion:         tls.VersionTLS12,
		})
		conn.SetDeadline(time.Now().Add(apiTimeout)) //nolint:errcheck
		if err = tc.HandshakeContext(dctx); err != nil {
			conn.Close() //nolint:errcheck
			return nil, fmt.Errorf("TLS negotiation refused by %s: %w — on a RouterOS "+
				"without an installed certificate, `api-ssl` offers only an anonymous "+
				"Diffie-Hellman suite, which Go's TLS library does not implement. Fall back "+
				"to cleartext API (port %d) through the proxy tunnel",
				addr, err, APIPort)
		}
		conn = tc
	}

	var out = &APIConn{conn: conn, br: bufio.NewReader(conn)}
	out.step(true, "binary API session opened to "+addr,
		map[bool]string{true: "TLS", false: "cleartext — confidentiality relies on " +
			"the proxy tunnel"}[opt.TLS])
	return out, nil
}

// Login authenticates the session, trying BOTH protocol login forms.
//
// # Why two, when one would suffice on the reference hardware
//
// MikroTik changed login in 6.43, and both forms coexist in the field:
//
//	≥ 6.43 (« modern »)     `/login =name=… =password=…` in ONE sentence. The password
//	                         travels IN CLEAR: on 8728 without TLS, anyone on the path
//	                         can read it, and it is the proxy tunnel — not the protocol
//	                         — that protects it.
//	< 6.43 (« legacy »)     TWO sentences. Bare `/login` returns a `!done` carrying
//	                         `=ret=<challenge, 16 bytes in hexadecimal>`; the client
//	                         responds `/login =name=… =response=00<md5(0x00 || secret || challenge)>`.
//	                         The secret does not travel, but unsalted MD5 over a device
//	                         challenge does not authenticate the SERVER: it is not « more
//	                         secure », it is differently weak.
//
// # Order, and why it is cost-free
//
// The modern form is tried FIRST, and a legacy firmware does not fail for that: it
// IGNORES attributes it does not understand and returns its challenge. The `=ret=` on
// `!done` is therefore both « here is the challenge » and « I am pre-6.43 firmware »,
// and its PRESENCE is the only discriminator we need. No version detection, no round
// trip spent probing: one sentence in the modern case, two in the legacy case, and
// never a wasted attempt.
//
// Trying the other way would cost one sentence for everyone and would send a bare
// `/login` — therefore without account — to modern firmware that refuse it with a
// `!trap`, producing a refusal to explain on a perfectly healthy device.
//
// # `=ret=` on modern firmware
//
// The case does not exist in the documentation, and it is handled anyway: if a
// firmware responded `!done` WITH a `=ret=` after accepting the credentials, chaining
// into the challenge response is harmless — the device sees a second correct `/login`.
// The opposite (ignoring a legacy firmware's `=ret=`) would leave a UNAUTHENTICATED
// session whose first command would answer « not enough permissions », a message that
// sends you looking for a group policy where the second login sentence is simply missing.
func (c *APIConn) Login(ctx context.Context, user, secret string) error {
	var rows, err = c.Do(ctx, "/login", "=name="+user, "=password="+secret)
	if err != nil {
		return fmt.Errorf("session login refused for \u00ab %s \u00bb: %w", user, err)
	}
	var challenge = attrOf(rows, "ret")
	if challenge == "" {
		c.step(true, "binary API session authenticated as \u00ab "+user+" \u00bb",
			"modern login (cleartext password, RouterOS ≥ 6.43)")
		return nil
	}
	return c.loginLegacy(ctx, user, secret, challenge)
}

// loginLegacy responds to the challenge from pre-6.43 firmware.
//
// The response is `00` followed by the MD5 digest of concatenating a NUL byte, the
// secret, and the challenge DECODED from hexadecimal. Three details, each producing
// the same unreadable symptom — « cannot log in » on a secret that is nonetheless good:
//
//   - the leading NUL byte is in the HASH, the leading `00` is in the RETURNED string.
//     Two different things that look alike, and confusing them is the classic mistake
//     in this implementation;
//   - the challenge is decoded from hexadecimal BEFORE entering the hash. Hashing the
//     hexadecimal string as-is yields a perfectly valid and false digest;
//   - MD5 is imposed by the protocol. That is not this connector's choice, and there
//     is nothing to harden: it is the login of firmware we do not update from here. A
//     fleet on 6.43 or later never takes this path.
func (c *APIConn) loginLegacy(ctx context.Context, user, secret, challenge string) error {
	var raw, err = hex.DecodeString(strings.TrimSpace(challenge))
	if err != nil {
		return fmt.Errorf("unreadable login challenge (\u00ab %s \u00bb): the legacy "+
			"protocol serves it in hexadecimal, and hashing it as-is would yield a valid "+
			"and false digest — therefore a refusal whose message would say nothing: %w",
			challenge, err)
	}
	var sum = md5.New() //nolint:gosec // imposed by pre-6.43 login protocol
	sum.Write([]byte{0})
	sum.Write([]byte(secret))
	sum.Write(raw)
	var response = "00" + hex.EncodeToString(sum.Sum(nil))

	_, err = c.Do(ctx, "/login", "=name="+user, "=response="+response)
	if err != nil {
		return fmt.Errorf("legacy login refused for \u00ab %s \u00bb: %w — this firmware "+
			"predates RouterOS 6.43 (it answered with a challenge instead of accepting "+
			"the cleartext password)", user, err)
	}
	c.step(true, "binary API session authenticated as \u00ab "+user+" \u00bb",
		"legacy challenge/response login (RouterOS < 6.43)")
	return nil
}

// attrOf looks up an attribute in records returned by a command, from last to first.
//
// From last to first because it is the `!done` that carries closing attributes
// (`=ret=`), and it arrives last. Searching from the start would return a same-named
// field carried by a `!re`, which describes a record and not the command result.
func attrOf(rows []Row, name string) string {
	var i int
	for i = len(rows) - 1; i >= 0; i-- {
		var v, ok = rows[i][name]
		if ok {
			return v
		}
	}
	return ""
}
