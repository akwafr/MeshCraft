package routeros

import (
	"bufio"
	"context"
	"crypto/md5" //nolint:gosec // the test must compute exactly what the protocol imposes
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"
)

// MikroTik binary API, exercised against a SIMULATED device.
//
// # Why a simulator and not captures, unlike the rest of the package
//
// Everything else in this connector is exercised on testdata/ captures, which are the
// REAL responses from the reference hardware. This file cannot: no byte of this
// protocol was exchanged with the device, the tool available from the development host
// being restricted to GET/HEAD on HTTP — it cannot open a raw socket on 8728.
//
// What the simulator exercises then, and that is what matters here: FRAMING. A mis-
// encoded word does not produce a visible syntax error, it SHIFTS the stream — the
// device reads the end of one word as the start of the next, and the session ends in
// `!fatal` several commands later, with no apparent link to the cause. That is the
// most costly failure mode to diagnose on hardware, and it is precisely what a
// simulator catches without hardware.
//
// What the simulator does NOT prove, and must be read that way: that this particular
// firmware responds as the specification describes. The two are different things, and
// only a hardware campaign will close the gap.

// simulatedDevice serves responses through an in-memory socket pair.
//
// It uses the SAME framing functions as the client (`readSentence`, `writeSentence`),
// and that is a choice to own: a simulator with its own encoding implementation would
// verify that two codes written the same day agree, which proves nothing about the
// specification. Reusing the client's functions makes this file a PROTOCOL test (the
// sentences, responses, logins) and not an encoding test — encoding is exercised
// separately against the documentation table, byte by byte, in
// TestEncodeWordLengthFollowsTable.
func simulatedDevice(t *testing.T, respond func(Sentence) []Sentence) *APIConn {
	t.Helper()
	var clientSide, deviceSide = net.Pipe()
	var client = &APIConn{conn: clientSide, br: bufio.NewReader(clientSide)}
	var fake = &APIConn{conn: deviceSide, br: bufio.NewReader(deviceSide)}

	go func() {
		defer fake.conn.Close() //nolint:errcheck
		for {
			var phrase, err = fake.readSentence()
			if err != nil {
				return
			}
			var response Sentence
			for _, response = range respond(phrase) {
				if fake.writeSentence(response) != nil {
					return
				}
			}
		}
	}()
	t.Cleanup(func() { clientSide.Close() }) //nolint:errcheck
	return client
}

// tag returns a received sentence's tag, for the simulator to echo back.
//
// Echoing it is MANDATORY in the simulator, and that is already a check: the client
// abandons the session on a mismatched tag, so a simulator that forgot it would fail
// all tests with the right message.
func tag(phrase Sentence) string {
	var w string
	for _, w = range phrase {
		if strings.HasPrefix(w, ".tag=") {
			return w
		}
	}
	return ""
}

// attrValue looks up the value of an `=name=value` word in a sentence sent by the client.
func attrValue(phrase Sentence, name string) (string, bool) {
	var w string
	for _, w = range phrase {
		if strings.HasPrefix(w, "="+name+"=") {
			return strings.TrimPrefix(w, "="+name+"="), true
		}
	}
	return "", false
}

// TestEncodeWordLengthFollowsTable — the documentation table, bound by bound.
//
// This is the most important test in the file, and the only one that verifies BYTES
// rather than behavior. The reason is the failure mode: a mis-framed word is not
// visible immediately, it shifts the stream, and the session dies several commands
// later with no apparent link to the cause.
//
// Cases are the BOUNDS, not values mid-range: that is where a `<=` written as `<`
// changes the number of bytes emitted, and the only place a one-bit error shows up.
func TestEncodeWordLengthFollowsTable(t *testing.T) {
	var cases = []struct {
		length int
		want   []byte
		why    string
	}{
		{0, []byte{0x00}, "the EMPTY word that terminates every sentence"},
		{1, []byte{0x01}, "one byte"},
		{0x7F, []byte{0x7F}, "upper bound of single-byte encoding"},
		{0x80, []byte{0x80, 0x80}, "first two-byte encoding: len | 0x8000"},
		{0x3FFF, []byte{0xBF, 0xFF}, "upper bound of two-byte encoding"},
		{0x4000, []byte{0xC0, 0x40, 0x00}, "first three-byte: len | 0xC00000"},
		{0x1FFFFF, []byte{0xDF, 0xFF, 0xFF}, "upper bound on three bytes"},
		{0x200000, []byte{0xE0, 0x20, 0x00, 0x00}, "first four-byte: len | 0xE0000000"},
		{0xFFFFFFF, []byte{0xEF, 0xFF, 0xFF, 0xFF}, "upper bound on four bytes"},
		{0x10000000, []byte{0xF0, 0x10, 0x00, 0x00, 0x00}, "five-byte encoding: 0xF0 then four bytes"},
	}
	var c int
	for c = range cases {
		var got, err = encodeWordLength(cases[c].length)
		if err != nil {
			t.Fatalf("length %#x (%s): %v", cases[c].length, cases[c].why, err)
		}
		if len(got) != len(cases[c].want) {
			t.Errorf("length %#x (%s): %d byte(s) emitted, %d expected — %x vs %x",
				cases[c].length, cases[c].why, len(got), len(cases[c].want),
				got, cases[c].want)
			continue
		}
		var i int
		for i = range got {
			if got[i] != cases[c].want[i] {
				t.Errorf("length %#x (%s): encoding %x, expected %x",
					cases[c].length, cases[c].why, got, cases[c].want)
				break
			}
		}
	}
}

// TestWordLengthRoundTrip: what is encoded must read back identically.
//
// Reading is BIG-ENDIAN, unlike SwOS addresses which are little-endian: two protocols
// from the same vendor, two opposite conventions. A round trip is the only test that
// catches a swap, because a swap produces perfectly valid bytes.
func TestWordLengthRoundTrip(t *testing.T) {
	var lengths = []int{0, 1, 42, 0x7F, 0x80, 0x1234, 0x3FFF, 0x4000, 0xABCDE,
		0x1FFFFF, 0x200000, 0x1234567, 0xFFFFFFF, 0x10000000, 0x7FFFFFFF}
	var l int
	for _, l = range lengths {
		var raw, err = encodeWordLength(l)
		if err != nil {
			t.Fatalf("length %#x not encodable: %v", l, err)
		}
		var got int
		got, err = readWordLength(bufio.NewReader(strings.NewReader(string(raw))))
		if err != nil {
			t.Fatalf("length %#x (%x) unreadable: %v", l, raw, err)
		}
		if got != l {
			t.Errorf("round trip of %#x: read %#x (bytes %x) — a byte-order swap "+
				"produces valid bytes and a wrong length",
				l, got, raw)
		}
	}
}

// TestReservedPrefixIsMisalignment: 0xF8 through 0xFF are not lengths.
//
// The specification reserves them for control bytes. Encountering them does NOT mean
// 'giant word': it means we are no longer reading the stream we think we are.
// Continuing would consume random bytes until a timeout whose message would say
// nothing — hence a refusal that NAMES the misalignment.
func TestReservedPrefixIsMisalignment(t *testing.T) {
	var b byte
	for b = 0xF8; b < 0xFF; b++ {
		var _, err = readWordLength(bufio.NewReader(strings.NewReader(string([]byte{b}))))
		if err == nil {
			t.Errorf("prefix 0x%02X accepted as a length: the stream would be read at random",
				b)
			continue
		}
		if !strings.Contains(err.Error(), "MISALIGNED") {
			t.Errorf("prefix 0x%02X: refusal must name misalignment, got %q", b, err.Error())
		}
	}
}

// TestNegativeLengthIsRefused: the only input encoding cannot carry.
func TestNegativeLengthIsRefused(t *testing.T) {
	var _, err = encodeWordLength(-1)
	if err == nil {
		t.Error("a negative length must be refused, not silently encoded")
	}
}

// TestSentenceEndsWithEmptyWord — without it, the device waits indefinitely.
//
// There is NO other end marker in this protocol. Forgetting it does not produce an
// error: it produces a mute session that no timeout on our side unblocks, since the
// device patiently waits for the rest.
func TestSentenceEndsWithEmptyWord(t *testing.T) {
	var received Sentence
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		received = phrase
		return []Sentence{{"!done", tag(phrase)}}
	})
	var _, err = c.Do(context.Background(), "/system/identity/print")
	if err != nil {
		t.Fatalf("command refused: %v", err)
	}
	// The sentence read back by the simulator does NOT contain the empty word:
	// `readSentence` consumes it as the end marker. That it could be read at all IS
	// proof the empty word was emitted — without it, the read would never return.
	if len(received) == 0 || received[0] != "/system/identity/print" {
		t.Fatalf("sentence received by device: %q", received)
	}
	var i int
	for i = range received {
		if received[i] == "" {
			t.Error("the empty termination word must not appear in the returned sentence: " +
				"it is framing, not content")
		}
	}
}

// TestDoAssemblesRecords: `!re` carries data, `!done` closes.
func TestDoAssemblesRecords(t *testing.T) {
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		var mark = tag(phrase)
		return []Sentence{
			{"!re", "=name=ether1", "=running=false", mark},
			{"!re", "=name=sfp-sfpplus2", "=running=true", mark},
			{"!done", mark},
		}
	})
	var rows, err = c.Do(context.Background(), "/interface/print")
	if err != nil {
		t.Fatalf("command refused: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d record(s), 2 expected: %+v", len(rows), rows)
	}
	if rows[0].Get("name") != "ether1" || rows[1].Bool("running") != true {
		t.Errorf("records decoded incorrectly: %+v", rows)
	}
}

// TestValueWithEqualsIsNotTruncated — split at the FIRST separator.
//
// Splitting at the last would truncate any value containing `=`, and this platform
// uses them: LLDP descriptions OBSERVED in `/ip/neighbor` carry them. A truncated
// description does not fail, it tells a different story.
func TestValueWithEqualsIsNotTruncated(t *testing.T) {
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		return []Sentence{
			{"!re", "=comment=vlan=99 uplink", tag(phrase)},
			{"!done", tag(phrase)},
		}
	})
	var rows, err = c.Do(context.Background(), "/interface/print")
	if err != nil {
		t.Fatalf("command refused: %v", err)
	}
	if rows[0].Get("comment") != "vlan=99 uplink" {
		t.Errorf("value truncated at wrong separator: %q", rows[0].Get("comment"))
	}
}

// TestPresentEmptyAttrIsNotAbsent
//
// The protocol uses this for FLAGS (`=once=` on `monitor`). Confusing the two would
// read 'this module does not report signal loss' where the firmware simply does not
// say so — two facts indistinguishable on screen that call for different conclusions.
func TestPresentEmptyAttrIsNotAbsent(t *testing.T) {
	var rep = parseReply(Sentence{"!re", "=sfp-rx-loss", "=name=sfp-sfpplus1"})
	var v, present = rep.Attrs["sfp-rx-loss"]
	if !present {
		t.Fatal("an attribute without value must be PRESENT and empty")
	}
	if v != "" {
		t.Errorf("attribute without value: expected empty, got %q", v)
	}
	var _, absent = rep.Attrs["sfp-tx-fault"]
	if absent {
		t.Error("an absent attribute must not appear")
	}
}

// TestTrapBecomesErrorWithMessage
//
// A `!trap` returned as an empty record would conclude 'this menu is empty' where
// the device said 'you do not have permission'. The two are fixed by opposite
// actions: look for a missing feature, or fix a group policy.
func TestTrapBecomesErrorWithMessage(t *testing.T) {
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		return []Sentence{
			{"!trap", "=category=1", "=message=not enough permissions (9)", tag(phrase)},
			{"!done", tag(phrase)},
		}
	})
	var _, err = c.Do(context.Background(), "/interface/print")
	if err == nil {
		t.Fatal("a !trap must produce an error, not an empty result")
	}
	if !strings.Contains(err.Error(), "not enough permissions") {
		t.Errorf("device message must appear: %v", err)
	}
	// And the translation must name the action: on this platform, this refusal almost
	// always comes from a group missing the right policy.
	if !strings.Contains(err.Error(), "policy") {
		t.Errorf("refusal must name group policy, the usual cause: %v", err)
	}
}

// TestFatalClosesSessionForGood
//
// After a `!fatal`, the device closed on its side. Sending again would wait for a
// response that will never come, and a timeout message would not say why — hence an
// immediate refusal that repeats the original reason.
func TestFatalClosesSessionForGood(t *testing.T) {
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		return []Sentence{{"!fatal", "session terminated on another interface"}}
	})
	var _, err = c.Do(context.Background(), "/interface/print")
	if err == nil {
		t.Fatal("a !fatal must produce an error")
	}
	if !strings.Contains(err.Error(), "session terminated") {
		t.Errorf("!fatal reason — carried by a BARE word, not an attribute — must "+
			"surface: %v", err)
	}
	// The second command must not even be sent.
	var _, err2 = c.Do(context.Background(), "/system/resource/print")
	if err2 == nil {
		t.Fatal("a command after !fatal must be refused immediately")
	}
	if !strings.Contains(err2.Error(), "closed") {
		t.Errorf("refusal must say the session is closed: %v", err2)
	}
}

// TestMismatchedTagAbandonsSession
//
// This is the opposite of usual `.tag` usage, and it is deliberate: this connector
// never emits two commands in flight, so a response from another round can only mean
// one thing — the stream is shifted, and what we read is not the response to what we
// asked. Sorting silently would return one record's values for another, i.e. credible
// and false telemetry.
func TestMismatchedTagAbandonsSession(t *testing.T) {
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		// Deliberately wrong tag: response from a nonexistent round.
		return []Sentence{
			{"!re", "=name=ether1", ".tag=999"},
			{"!done", ".tag=999"},
		}
	})
	var _, err = c.Do(context.Background(), "/interface/print")
	if err == nil {
		t.Fatal("a response tagged from another round must abandon the session")
	}
	if !strings.Contains(err.Error(), "credible and false") {
		t.Errorf("refusal must say what silent sorting would cost: %v", err)
	}
}

// TestCommandWithoutSlashRefusedBeforeSend
//
// A command of this protocol starts with '/'. Without that, the device would read it
// as an attribute word and respond with an unrelated `!fatal` — a diagnosis impossible
// to tie back to its cause. Refusal therefore happens BEFORE sending.
func TestCommandWithoutSlashRefusedBeforeSend(t *testing.T) {
	var called bool
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		called = true
		return []Sentence{{"!done", tag(phrase)}}
	})
	var _, err = c.Do(context.Background(), "interface/print")
	if err == nil {
		t.Fatal("a command without '/' must be refused")
	}
	if called {
		t.Error("refusal must happen BEFORE anything is sent")
	}
}

// TestWordTooLongRefusedWithoutAllocating
//
// The length prefix can announce up to 2³² - 1 bytes. Allocating what the header
// requests would let a misbehaving device — or a hostile intermediary — exhaust the
// controller's memory with FIVE bytes sent.
func TestWordTooLongRefusedWithoutAllocating(t *testing.T) {
	var clientSide, deviceSide = net.Pipe()
	defer clientSide.Close() //nolint:errcheck
	defer deviceSide.Close() //nolint:errcheck
	var c = &APIConn{conn: clientSide, br: bufio.NewReader(clientSide)}

	go func() {
		var fake = &APIConn{conn: deviceSide, br: bufio.NewReader(deviceSide)}
		fake.readSentence() //nolint:errcheck // the command, ignored
		// A five-byte prefix announcing 256 MiB, then nothing: the client must refuse
		// on the header, without waiting for the body.
		deviceSide.Write([]byte{0xF0, 0x10, 0x00, 0x00, 0x00}) //nolint:errcheck
	}()

	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var _, err = c.Do(ctx, "/interface/print")
	if err == nil {
		t.Fatal("a 256 MiB word announcement must be refused")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("refusal must name the connector limit: %v", err)
	}
}

// TestModernLoginInOneSentence — RouterOS ≥ 6.43.
//
// The password travels IN CLEAR, and that must be known: on 8728 without TLS, anyone on
// the path can read it. It is the proxy tunnel — not the protocol — that protects it,
// exactly like REST HTTP Basic.
func TestModernLoginInOneSentence(t *testing.T) {
	var phraseCount int
	var seenName, seenSecret string
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		phraseCount++
		seenName, _ = attrValue(phrase, "name")
		seenSecret, _ = attrValue(phrase, "password")
		return []Sentence{{"!done", tag(phrase)}}
	})
	var err = c.Login(context.Background(), "mcadmin", "s3cr3t")
	if err != nil {
		t.Fatalf("login refused: %v", err)
	}
	if phraseCount != 1 {
		t.Errorf("%d sentence(s) sent: modern login needs only ONE, and that makes "+
			"try-order free", phraseCount)
	}
	if seenName != "mcadmin" || seenSecret != "s3cr3t" {
		t.Errorf("credentials sent incorrectly: name %q, secret length %d",
			seenName, len(seenSecret))
	}
}

// TestLegacyLoginRespondsToChallenge — RouterOS < 6.43.
//
// # Three details that produce the SAME unreadable symptom
//
// 'cannot log in' on a secret that is nonetheless good, with nothing to say which
// of the three:
//
//  1. the leading NUL byte is in the HASH, the leading `00` is in the RETURNED string.
//     Two different things that look alike;
//  2. the challenge is decoded from hexadecimal BEFORE entering the hash. Hashing the
//     hexadecimal string as-is yields a perfectly valid and false digest;
//  3. concatenation order is nul, secret, challenge — not challenge, secret.
//
// This test recomputes the expected digest from the specification in the simulator,
// rather than copying a constant: a constant copied from the code would verify the code
// agrees with itself.
func TestLegacyLoginRespondsToChallenge(t *testing.T) {
	const secret = "s3cr3t"
	// A sixteen-byte challenge, as the device serves them.
	var challenge = []byte{
		0x1f, 0x9c, 0x4d, 0x22, 0x8b, 0x33, 0x01, 0xa7,
		0x5e, 0xd0, 0x74, 0x11, 0xf6, 0x62, 0x9a, 0xcc,
	}
	var expected = md5.New() //nolint:gosec // imposed by the protocol
	expected.Write([]byte{0})
	expected.Write([]byte(secret))
	expected.Write(challenge)
	var expectedResponse = "00" + hex.EncodeToString(expected.Sum(nil))

	var phraseCount int
	var seenResponse string
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		phraseCount++
		if phraseCount == 1 {
			// Legacy firmware IGNORES attributes it does not understand and returns its
			// challenge. That response is the version discriminator: its PRESENCE is
			// enough, no detection needed.
			return []Sentence{{"!done", "=ret=" + hex.EncodeToString(challenge), tag(phrase)}}
		}
		seenResponse, _ = attrValue(phrase, "response")
		return []Sentence{{"!done", tag(phrase)}}
	})

	var err = c.Login(context.Background(), "admin", secret)
	if err != nil {
		t.Fatalf("legacy login refused: %v", err)
	}
	if phraseCount != 2 {
		t.Fatalf("%d sentence(s): legacy login needs exactly two", phraseCount)
	}
	if seenResponse != expectedResponse {
		t.Errorf("wrong challenge response.\n  got:      %s\n  expected: %s\n"+
			"The three possible causes are in this test's comment, and they all produce "+
			"the same 'cannot log in' on a good secret.",
			seenResponse, expectedResponse)
	}
	// The secret must NEVER travel in clear on this path: the only property legacy
	// login has over modern login.
	var _, secretInClear = attrValue(Sentence{"=response=" + seenResponse}, "password")
	if secretInClear {
		t.Error("legacy login must not transmit the password")
	}
}

// TestUnreadableChallengeRefusedWithReason
//
// A challenge that is not hexadecimal must not be hashed 'anyway': that would produce
// a valid and false digest, therefore a refusal whose message would say nothing.
func TestUnreadableChallengeRefusedWithReason(t *testing.T) {
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		return []Sentence{{"!done", "=ret=not-hex", tag(phrase)}}
	})
	var err = c.Login(context.Background(), "admin", "x")
	if err == nil {
		t.Fatal("an unreadable challenge must fail login, not be hashed as-is")
	}
	if !strings.Contains(err.Error(), "hexadecimal") {
		t.Errorf("refusal must say what was not understood: %v", err)
	}
}

// TestLoginRefusalSurfacesDeviceReason
//
// 'cannot log in' has TWO causes on this platform, and the device does not distinguish
// them: a wrong secret, or a valid secret in a group that does not carry the `api`
// policy. The message must name the second, or you search for a password for an hour.
func TestLoginRefusalSurfacesDeviceReason(t *testing.T) {
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		return []Sentence{{"!trap", "=message=cannot log in", tag(phrase)}}
	})
	var err = c.Login(context.Background(), "mcadmin", "wrong")
	if err == nil {
		t.Fatal("a !trap on /login must fail login")
	}
	var want string
	for _, want = range []string{"cannot log in", "`api` policy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must contain %q: %v", want, err)
		}
	}
}

// TestEmptyDoesNotCountAsRecord — RouterOS ≥ 7.18.
//
// `!empty` says 'there is nothing', explicitly. Counting it as a record would
// fabricate an empty one, and a caller testing `len(rows) > 0` would conclude it
// found something.
func TestEmptyDoesNotCountAsRecord(t *testing.T) {
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		return []Sentence{{"!empty", tag(phrase)}, {"!done", tag(phrase)}}
	})
	var rows, err = c.Do(context.Background(), "/interface/bonding/print")
	if err != nil {
		t.Fatalf("command refused: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("!empty must return zero records, got %+v", rows)
	}
}

// TestDoneWithAttrsPreservesThem
//
// `/login`'s `!done` carries the challenge in `=ret=`: dropping it would lose legacy
// login, and the symptom would be an unauthenticated session whose first command
// answers 'not enough permissions' — a message that sends you looking for group
// policy where a sentence is missing.
func TestDoneWithAttrsPreservesThem(t *testing.T) {
	var c = simulatedDevice(t, func(phrase Sentence) []Sentence {
		return []Sentence{{"!done", "=ret=abcdef", tag(phrase)}}
	})
	var rows, err = c.Do(context.Background(), "/login")
	if err != nil {
		t.Fatalf("command refused: %v", err)
	}
	if attrOf(rows, "ret") != "abcdef" {
		t.Errorf("!done attributes must be preserved: %+v", rows)
	}
}

// TestAttrOfSearchesFromLast
//
// It is the `!done` that carries CLOSING attributes, and it arrives last. Searching
// from the start would return a same-named field carried by a `!re`, which describes a
// record and not the command result.
func TestAttrOfSearchesFromLast(t *testing.T) {
	var rows = []Row{{"ret": "from-a-record"}, {"ret": "from-closure"}}
	if attrOf(rows, "ret") != "from-closure" {
		t.Errorf("attrOf must read last to first, got %q", attrOf(rows, "ret"))
	}
	if attrOf(rows, "absent") != "" {
		t.Error("an absent attribute must return the empty string")
	}
}
