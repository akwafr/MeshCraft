package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/snmpprov"
	"meshcraft/internal/tunnel"
)

// Diagnostic SNMP walk, from the API perspective.
//
// What these tests lock in: the four decisions POST
// /api/devices/{id}/snmp-walk makes BEFORE sending a single packet into
// a client's network — the limit, the OID, the proxy, and the SNMP identity. Each
// one is decided in a pure function, exactly like judgeCollector and
// effectiveInterval before them, because the `api` package has no database in tests.
//
// What a test in this package CANNOT cover, and must therefore be stated:
//
//   - that Store.ProxiesCoveringSite returns covering proxies. Its `where`
//     clause is a word-for-word transcription of DevicesWatchedBy (cf. its
//     comment) and that requires PostgreSQL;
//   - that the community actually comes out of the vault (askCommunityFor reads the store
//     and decrypts): only the DECISION, judgeAskSNMP, is exercised here;
//   - the full HTTP path — site_admin rights, global-admin reserve, log,
//     and the yamux stream itself. Exercising Hub.Ask would require a pair of
//     yamux sessions, hence a test file in `internal/tunnel` that does not
//     belong to this batch; what is verifiable from here for its limits,
//     namely the two pure rejections, is.

// --- The hard ceiling ---

// TestHardRowLimitCannotBeExceeded: MaxAskRows is a ceiling, not a default. A caller
// who asks for more gets clamped down, without error — the question remains useful —
// but it is NEVER raised.
//
// The limit is checked at both ends (the Hub before send, the handler to announce
// the retained value) and both call this same function: a limit applied on only one
// side is not a limit, because the proxy may be older than the controller.
func TestHardRowLimitCannotBeExceeded(t *testing.T) {
	var cas = []struct {
		request  int
		expected int
		why      string
	}{
		{0, tunnel.DefaultAskRows, "0 = default value"},
		{-1, tunnel.DefaultAskRows, "a negative request is not a request"},
		{50, 50, "a reasonable request is honored as-is"},
		{tunnel.MaxAskRows, tunnel.MaxAskRows, "the ceiling itself is reachable"},
		{tunnel.MaxAskRows + 1, tunnel.MaxAskRows, "one more is clamped down"},
		{50_000_000, tunnel.MaxAskRows, "a walk under 1.3.6.1 must not fill memory"},
	}
	var c int
	for c = range cas {
		var got = tunnel.ClampAskRows(cas[c].request)
		if got != cas[c].expected {
			t.Errorf("ClampAskRows(%d) = %d, expected %d — %s",
				cas[c].request, got, cas[c].expected, cas[c].why)
		}
	}
	if tunnel.DefaultAskRows > tunnel.MaxAskRows {
		t.Fatalf("DefaultAskRows (%d) exceeds MaxAskRows (%d)",
			tunnel.DefaultAskRows, tunnel.MaxAskRows)
	}
}

// TestHardWaitLimitCannotBeExceeded: an HTTP request must not be able to block
// for ten minutes because a proxy is struggling, even if the caller asks for it.
func TestHardWaitLimitCannotBeExceeded(t *testing.T) {
	if tunnel.ClampAskWait(0) != tunnel.DefaultAskWait {
		t.Errorf("0 must equal DefaultAskWait, got %v", tunnel.ClampAskWait(0))
	}
	if tunnel.ClampAskWait(-5) != tunnel.DefaultAskWait {
		t.Errorf("a negative wait must fall back to the default, got %v",
			tunnel.ClampAskWait(-5))
	}
	if tunnel.ClampAskWait(3000) != 3*time.Second {
		t.Errorf("a wait under the ceiling is honored, got %v",
			tunnel.ClampAskWait(3000))
	}
	var tooLong = int(tunnel.MaxAskWait.Milliseconds()) + 1
	if tunnel.ClampAskWait(tooLong) != tunnel.MaxAskWait {
		t.Errorf("ClampAskWait(%d ms) = %v, expected ceiling %v",
			tooLong, tunnel.ClampAskWait(tooLong), tunnel.MaxAskWait)
	}
	if tunnel.ClampAskWait(600_000) != tunnel.MaxAskWait {
		t.Errorf("ten minutes must be clamped to %v, got %v",
			tunnel.MaxAskWait, tunnel.ClampAskWait(600_000))
	}
}

// --- OID validation ---

func TestPlausibleOIDAccepted(t *testing.T) {
	var cas = map[string]string{
		"1.3.6.1.2.1.2.2":                 "1.3.6.1.2.1.2.2",
		" 1.3.6.1.2.1.1.1.0 ":             "1.3.6.1.2.1.1.1.0",
		".1.3.6.1.4.1.11.2.14.11.5.1":     "1.3.6.1.4.1.11.2.14.11.5.1",
		"1.3.6.1.2.1.31.1.1.1.18.10":      "1.3.6.1.2.1.31.1.1.1.18.10",
		"1.0":                             "1.0",
		"1.3.6.1.2.1.2.2.1.10.4294967295": "1.3.6.1.2.1.2.2.1.10.4294967295",
	}
	var raw, expected string
	for raw, expected = range cas {
		var got, err = normalizeOID(raw)
		if err != nil {
			t.Errorf("normalizeOID(%q) rejected: %v", raw, err)
			continue
		}
		if got != expected {
			t.Errorf("normalizeOID(%q) = %q, expected %q", raw, got, expected)
		}
	}
}

func TestImplausibleOIDRejected(t *testing.T) {
	var cas = map[string]string{
		"":                                "empty",
		"   ":                             "whitespace only",
		"ifDescr":                         "MIB name",
		"IF-MIB::ifDescr":                 "qualified name",
		"1.3.6.1.2.1.2.2.1.*":             "wildcard",
		"1.3.6.1.2.1.2.2.":                "trailing dot",
		"1.3..6.1":                        "empty arc in the middle",
		"1":                               "single arc",
		"3.6.1.2":                         "first arc outside 0-2",
		"192.168.1.10":                    "IP address in the OID field",
		"1.3.06.1":                        "leading zero",
		"1.3.6.1.2.1.2.2.1.10.4294967296": "arc beyond 2^32-1",
		"1.3.6.1.2.1.-2":                  "negative arc",
		"1.3.6.1.2.1.2 .2":                "space in the middle",
	}
	var raw, what string
	for raw, what = range cas {
		var got, err = normalizeOID(raw)
		if err == nil {
			t.Errorf("normalizeOID(%q) accepted (%s) although it is a %s",
				raw, got, what)
			continue
		}
		if len(err.Error()) < 20 {
			t.Errorf("rejection of %q (%s) does not say what to fix: %v", raw, what, err)
		}
	}
	var long = "1" + strings.Repeat(".1", maxOIDArcs)
	var _, longErr = normalizeOID(long)
	if longErr == nil {
		t.Errorf("an OID of %d arcs must be rejected", maxOIDArcs+1)
	}
	var limit = "1" + strings.Repeat(".1", maxOIDArcs-1)
	var _, limitErr = normalizeOID(limit)
	if limitErr != nil {
		t.Errorf("an OID of %d arcs must pass: %v", maxOIDArcs, limitErr)
	}
}

var askCouvrantsSiteA = []model.Proxy{
	{ID: "local", Name: "Local", Global: true},
	{ID: "branch-a", Name: "Branch A"},
}

func TestAttachedProxyChoiceIsItsCollector(t *testing.T) {
	var connected = func(id string) bool { return id == "local" }
	var verdict, p = chooseAskProxy(askCouvrantsSiteA, "branch-a", connected)
	if verdict != askProxyAttached {
		t.Fatalf("an attached device is queried through its collector, verdict %v", verdict)
	}
	if p.ID != "branch-a" {
		t.Errorf("proxy chosen %q, expected declared collector", p.ID)
	}
}

func TestUnattachedProxyChoiceIsCovering(t *testing.T) {
	var verdict, p = chooseAskProxy(askCouvrantsSiteA, "",
		func(id string) bool { return id == "branch-a" })
	if verdict != askProxyCovering {
		t.Fatalf("an unattached device is queried through a covering proxy, verdict %v", verdict)
	}
	if p.ID != "branch-a" {
		t.Errorf("the CONNECTED proxy must be preferred, got %q", p.ID)
	}
	verdict, p = chooseAskProxy(askCouvrantsSiteA, "", func(string) bool { return false })
	if verdict != askProxyCovering || p.ID == "" {
		t.Fatalf("without a tunnel, a covering proxy must still be designated (%v, %q)",
			verdict, p.ID)
	}
	verdict, p = chooseAskProxy(askCouvrantsSiteA, "", nil)
	if verdict != askProxyCovering || p.ID != "local" {
		t.Fatalf("without a predicate, the first covering proxy must be kept (%v, %q)",
			verdict, p.ID)
	}
}

func TestNoCoveringProxyRefused(t *testing.T) {
	var verdict, p = chooseAskProxy(nil, "", func(string) bool { return true })
	if verdict != askProxyNoCoverage {
		t.Fatalf("without coverage, the question is impossible, verdict %v", verdict)
	}
	if p.ID != "" {
		t.Errorf("a refusal must designate no proxy, got %q", p.ID)
	}
	var msg = noAskProxyMessage(askProxyNoCoverage, "sw-a1", "")
	var word string
	for _, word = range []string{"sw-a1", "covers", "proxy"} {
		if !strings.Contains(msg, word) {
			t.Errorf("the refusal must mention %q: %s", word, msg)
		}
	}
}

func TestNonCoveringAttachmentRefused(t *testing.T) {
	var couvrantsSiteB = []model.Proxy{{ID: "local", Name: "Local", Global: true}}
	var verdict, p = chooseAskProxy(couvrantsSiteB, "branch-a",
		func(string) bool { return true })
	if verdict != askProxyNotCovering {
		t.Fatalf("a non-covering collector must be refused, verdict %v", verdict)
	}
	if p.ID != "" {
		t.Errorf("no substitution must occur, got %q", p.ID)
	}
	var msg = noAskProxyMessage(askProxyNotCovering, "sw-a1", "Branch A")
	var word string
	for _, word = range []string{"Branch A", "sw-a1", "nobody", "coverage", "detach"} {
		if !strings.Contains(msg, word) {
			t.Errorf("the refusal must mention %q: %s", word, msg)
		}
	}
	if strings.Contains(noAskProxyMessage(askProxyNotCovering, "sw-a1", ""), "\"\"") {
		t.Error("a collector without a name must not leave empty quotes")
	}
}

func planV2c() snmpprov.Plan {
	return snmpprov.Plan{Version: "v2c", Kind: snmpprov.KindV2c,
		Reason: "connector limited to v2c"}
}

func planV3() snmpprov.Plan {
	return snmpprov.Plan{Version: "v3", Kind: snmpprov.KindV3,
		Tag: "snmpv3:sha256-aes128", Auth: "sha256", Priv: "aes128", Conform: true}
}

// THE V3 VERDICT SPLIT ON 2026-08-13, when the collector learned authNoPriv:
// a v3 plan WITH privacy is still refused (nothing speaks priv yet, and
// half-speaking it reads as a mute device), while an authNoPriv plan — the
// S3400 family, the reason the capability exists — walks like any v2c device.
func TestWalkRejectsV3PrivButAcceptsAuthNoPriv(t *testing.T) {
	var verdict, msg = judgeAskSNMP("sw-a1", planV3(), "cred-v3")
	if verdict != askSNMPv3Only {
		t.Fatalf("a v3-with-privacy device is not queryable, verdict %v", verdict)
	}
	var word string
	for _, word = range []string{"sw-a1", "SNMPv3", "privacy", "authNoPriv"} {
		if !strings.Contains(msg, word) {
			t.Errorf("the priv refusal must mention %q: %s", word, msg)
		}
	}
	verdict, _ = judgeAskSNMP("sw-a1", planV3(), "")
	if verdict != askSNMPv3Only {
		t.Errorf("v3-priv is refused for the protocol, not for the vault, verdict %v", verdict)
	}

	// authNoPriv — Priv empty is the S3400's measured plan shape.
	var nopriv = snmpprov.Plan{Version: "v3", Kind: snmpprov.KindV3,
		Tag: "snmpv3-sha-nopriv", Auth: "sha", Conform: false,
		Reason: "platform without a privacy user form"}
	verdict, msg = judgeAskSNMP("sw-a1", nopriv, "cred-v3")
	if verdict != askSNMPReady {
		t.Fatalf("an authNoPriv device must be walkable since the collector "+
			"speaks it, verdict %v: %s", verdict, msg)
	}
	verdict, _ = judgeAskSNMP("sw-a1", nopriv, "")
	if verdict != askSNMPNoCredential {
		t.Errorf("authNoPriv without a vault secret is a CREDENTIAL refusal "+
			"(fixable by snmp-apply), not a protocol one, verdict %v", verdict)
	}
}

func TestWalkAcceptsV2cWithSecret(t *testing.T) {
	var verdict, msg = judgeAskSNMP("sw-a1", planV2c(), "cred-v2c")
	if verdict != askSNMPReady {
		t.Fatalf("a v2c device with its community is queryable, verdict %v", verdict)
	}
	if msg != "" {
		t.Errorf("success must carry no refusal: %s", msg)
	}
}

func TestWalkRefusesMissingVaultCommunity(t *testing.T) {
	var verdict, msg = judgeAskSNMP("sw-a1", planV2c(), "")
	if verdict != askSNMPNoCredential {
		t.Fatalf("without a vault credential, the walk is impossible, verdict %v", verdict)
	}
	var word string
	for _, word = range []string{"vault", "snmp-apply"} {
		if !strings.Contains(msg, word) {
			t.Errorf("the refusal must name %q: %s", word, msg)
		}
	}
}

func TestWalkRefusesDeviceWithoutSNMPPlan(t *testing.T) {
	var withoutPlan = snmpprov.Plan{Reason: "No connector recognizes this device."}
	var verdict, msg = judgeAskSNMP("unknown-box", withoutPlan, "")
	if verdict != askSNMPUnsupervised {
		t.Fatalf("without an SNMP plan, the walk is impossible, verdict %v", verdict)
	}
	if !strings.Contains(msg, "No connector") {
		t.Errorf("the plan reason must be reused: %s", msg)
	}
	verdict, msg = judgeAskSNMP("unknown-box", snmpprov.Plan{}, "")
	if verdict != askSNMPUnsupervised || len(msg) < 20 {
		t.Errorf("a mute plan must still produce a readable refusal: %q", msg)
	}
}

func TestAskFailuresAreDistinct(t *testing.T) {
	var codeNoTunnel, msgNoTunnel = askFailure(tunnel.ErrNoTunnel)
	if codeNoTunnel != http.StatusServiceUnavailable {
		t.Errorf("an absent proxy is a 503, got %d", codeNoTunnel)
	}
	if msgNoTunnel != errNoTunnel.Error() {
		t.Errorf("a proxy without a tunnel must read the same as everywhere else: %s", msgNoTunnel)
	}

	var codeRemote, msgRemote = askFailure(
		&tunnel.AskRemoteError{Detail: "SNMP timeout on 10.20.0.4"})
	if codeRemote != http.StatusBadGateway {
		t.Errorf("an error RETURNED by the proxy is a 502, got %d", codeRemote)
	}
	if !strings.Contains(msgRemote, "SNMP timeout on 10.20.0.4") {
		t.Errorf("proxy detail must surface: %s", msgRemote)
	}

	var codeTimeout, msgTimeout = askFailure(tunnel.ErrAskTimeout)
	if codeTimeout != http.StatusGatewayTimeout {
		t.Errorf("a exceeded deadline is a 504 not a 502, got %d", codeTimeout)
	}
	if !strings.Contains(msgTimeout, "161") {
		t.Errorf("the timeout must suggest where to look: %s", msgTimeout)
	}

	var codeUnreadable, msgUnreadable = askFailure(tunnel.ErrAskUnreadable)
	if codeUnreadable != http.StatusBadGateway {
		t.Errorf("an unreadable response is a 502, got %d", codeUnreadable)
	}
	if !strings.Contains(msgUnreadable, "protocol") {
		t.Errorf("an unreadable response must read as a protocol fault: %s", msgUnreadable)
	}

	var codeClosed, msgClosed = askFailure(tunnel.ErrAskClosed)
	if codeClosed != http.StatusBadGateway {
		t.Errorf("a closed stream is a 502, got %d", codeClosed)
	}
	if !strings.Contains(msgClosed, "version") {
		t.Errorf("a closed stream must name the version hypothesis: %s", msgClosed)
	}

	var seen = map[string]bool{}
	var msg string
	for _, msg = range []string{msgNoTunnel, msgRemote, msgTimeout, msgUnreadable, msgClosed} {
		if seen[msg] {
			t.Errorf("two failures render the same message: %s", msg)
		}
		seen[msg] = true
	}

	var codeOther, msgOther = askFailure(errors.New("stream open: session shutdown"))
	if codeOther != http.StatusBadGateway || !strings.Contains(msgOther, "session shutdown") {
		t.Errorf("an uncatalogued failure must stay readable: %d %s", codeOther, msgOther)
	}
}

func TestProxyErrorUsesFirstLine(t *testing.T) {
	var _, msg = askFailure(&tunnel.AskRemoteError{
		Detail: "walk interrupted\ngoroutine 42 [running]:\n…",
	})
	if strings.Contains(msg, "goroutine") {
		t.Errorf("only the first line must surface: %s", msg)
	}
	if !strings.Contains(msg, "walk interrupted") {
		t.Errorf("the first line must surface: %s", msg)
	}
}

func TestReadLimitCoversRowCeiling(t *testing.T) {
	const readLimit = 8 << 20
	var ordinary = len(`{"oid":"1.3.6.1.2.1.31.1.1.1.18.1000001",` +
		`"type":"OctetString","value":"Server room - rack B12 - uplink 42"},`)
	var heavy = ordinary + 1024
	var worst = tunnel.MaxAskRows * heavy
	t.Logf("%d rows: %d KiB at ordinary rate (%d B/row), %d KiB with one-kilobyte "+
		"values — read limit at %d KiB",
		tunnel.MaxAskRows, tunnel.MaxAskRows*ordinary/1024, ordinary,
		worst/1024, readLimit/1024)
	if worst > readLimit {
		t.Fatalf("a full response of long values weighs %d B: Hub.Ask read limit would "+
			"cut a LEGITIMATE response", worst)
	}
	if worst*4 < readLimit {
		t.Errorf("a full response weighs only %d B: the %d B limit is too wide", worst, readLimit)
	}
}

func TestEmptyRowsAreNotNull(t *testing.T) {
	var empty = rowsOrEmpty(nil)
	if empty == nil || len(empty) != 0 {
		t.Errorf("a response with no rows must be an empty list, got %v", empty)
	}
	var one = []tunnel.AskRow{{OID: "1.3.6.1.2.1.1.5.0", Type: "OctetString", Value: "sw-a1"}}
	if len(rowsOrEmpty(one)) != 1 {
		t.Error("a non-empty response must pass through unchanged")
	}
}
