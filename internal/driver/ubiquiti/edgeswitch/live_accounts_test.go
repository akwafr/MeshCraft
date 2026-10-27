package edgeswitch

// LIVE account exercises on the lab EdgePoint S16 (unifimoule1, 192.0.2.4).
//
// # THIS RANGE HAS NO SERIAL CONSOLE
//
// A lockout on this hardware is repaired with the front-panel reset button, on
// site, and a reset loses the configuration. So every gesture here is written
// so that the ways back in are PROVEN BEFORE anything is removed, and so that a
// failed check stops the test rather than being reported after the fact.
//
// That is also why the deletion below re-reads the account list INSIDE the same
// session that will send "no username": a list read minutes earlier in another
// session is a claim about the past, and the whole risk lives in the gap.
//
//	ES_LIVE_HOST=192.0.2.4 ES_LIVE_USER=mcadmin ES_LIVE_PASS=… \
//	ES_LIVE_SOCKS=127.0.0.1:1080 \
//	go test ./internal/driver/ubiquiti/edgeswitch -run TestLiveAccounts -v
//
// The deletion is gated SEPARATELY and names its target explicitly — there is
// no default, because "which account" is exactly the decision that must not be
// carried by a fallback value:
//
//	… ES_LIVE_WRITE=1 ES_LIVE_DELETE_ACCOUNT=admin \
//	go test ./internal/driver/ubiquiti/edgeswitch -run TestLiveAccountDelete -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

// liveAccountsClient opens ONE session. The session budget on this platform is
// small (two), so every test here opens one and closes it, and none of them
// runs in parallel with another.
func liveAccountsClient(ctx context.Context, t *testing.T, why string) *Client {
	t.Helper()
	var tgt = driver.Target{Host: os.Getenv("ES_LIVE_HOST")}
	var sa = os.Getenv("ES_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("ES_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("ES_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("ES_LIVE_USER"), Secret: os.Getenv("ES_LIVE_PASS"), Why: why,
	}})
	if err != nil {
		t.Fatalf("authenticate as %q: %v", os.Getenv("ES_LIVE_USER"), err)
	}
	var c, _ = adm.(*Client)
	return c
}

// logInventory prints an inventory the way an operator needs to read it: every
// account with its privilege and the source that stated it, then the notes.
func logInventory(t *testing.T, label string, inv AccountInventory) {
	t.Helper()
	t.Logf("=== %s: %d account(s), source \"%s\", compared=%v ===",
		label, len(inv.Accounts), inv.Source, inv.Compared)
	var i int
	for i = range inv.Accounts {
		t.Logf("  %s", inv.Accounts[i].Describe())
	}
	for i = range inv.Hidden {
		t.Logf("  HIDDEN FROM RUNNING-CONFIG: %s", inv.Hidden[i].Describe())
	}
	for i = range inv.Notes {
		t.Logf("  note: %s", inv.Notes[i])
	}
}

// fullAuthority names the accounts that can administer the device, which is the
// only count that matters when deciding whether a way back in survives.
//
// It keys on [driver.AuthorityFull] — the connector's own verdict — rather than
// on the number 15, which is TODO.md's standing instruction for anything that
// reasons about privilege: the number is FASTPATH's spelling of the answer, not
// the answer. On this range the two coincide; writing it this way means the
// check does not have to be re-derived when the same guard is wanted elsewhere.
func fullAuthority(accounts []Account) []string {
	var out []string
	var i int
	for i = range accounts {
		if accounts[i].Authority == driver.AuthorityFull {
			out = append(out, accounts[i].Name)
		}
	}
	return out
}

func hasAccount(accounts []Account, name string) bool {
	var i int
	for i = range accounts {
		if strings.EqualFold(accounts[i].Name, name) {
			return true
		}
	}
	return false
}

// TestLiveAccountsAudit is READ ONLY and is the reconnaissance the deletion
// below depends on. It authenticates with whatever credential the environment
// carries — which is itself the proof that this credential opens the device —
// and reads BOTH sources so the factory account this range hides from its own
// running configuration appears.
func TestLiveAccountsAudit(t *testing.T) {
	if os.Getenv("ES_LIVE_HOST") == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var c = liveAccountsClient(ctx, t, "account audit (read only)")
	defer c.Close()
	t.Logf("authenticated as %q on %s", c.User, c.host)

	// The RAW capture, verbatim: the parsers below are the thing under test on a
	// live device, so the text they were given has to be in the record too.
	var raw, rawErr = c.cli.run(ctx, accountsFromDetail, cmdTimeout)
	if rawErr != nil {
		t.Errorf("\"%s\": %v", accountsFromDetail, rawErr)
	}
	t.Logf("=== %s (raw, %d bytes) ===\n%s", accountsFromDetail, len(raw), raw)

	var inv, err = c.AuditAccounts(ctx)
	if err != nil {
		t.Fatalf("AuditAccounts: %v", err)
	}
	logInventory(t, "audit", inv)
	t.Logf("full-authority accounts: %v", fullAuthority(inv.Accounts))
}

// TestLiveAccountDelete removes ONE named account through [Client.DeleteAccount]
// — the connector's own path, so what ships is what gets exercised — and only
// after the device itself has confirmed, in this same session, that a way back
// in survives it.
//
// # THE CHECKS ARE THE TEST
//
// The deletion is one line. Everything around it is the part that matters on a
// switch with no console:
//
//   - the session's own account is NOT the target (the connector refuses this
//     too; asserting it here means the refusal is exercised rather than trusted);
//   - the target actually EXISTS, so a typo fails loudly instead of succeeding
//     vacuously against an account that was never there;
//   - at least one privilege-15 account OTHER than the session's own survives
//     the deletion. One way in, with no console, is not a safe resting state,
//     and this test refuses to create that state whatever the caller asked for.
func TestLiveAccountDelete(t *testing.T) {
	if os.Getenv("ES_LIVE_WRITE") != "1" {
		t.Skip("ES_LIVE_WRITE not set — this test removes an administration account")
	}
	var target = os.Getenv("ES_LIVE_DELETE_ACCOUNT")
	if target == "" {
		t.Skip("ES_LIVE_DELETE_ACCOUNT not set — no default: naming the account IS the decision")
	}
	if os.Getenv("ES_LIVE_HOST") == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var c = liveAccountsClient(ctx, t, "account removal, operator-directed")
	defer c.Close()
	t.Logf("authenticated as %q on %s; target for removal: %q", c.User, c.host, target)

	// --- 0) The state this gesture is being decided against, read HERE.
	var before, err = c.AuditAccounts(ctx)
	if err != nil {
		t.Fatalf("AuditAccounts before: %v", err)
	}
	logInventory(t, "BEFORE", before)
	if before.Source != accountsFromDetail {
		t.Fatalf("the authoritative read \"%s\" did not answer (source was \"%s\") — "+
			"refusing to delete an account from a list that is known-incomplete on "+
			"this range", accountsFromDetail, before.Source)
	}
	if !hasAccount(before.Accounts, target) {
		t.Fatalf("account %q is not on this device: %v — refusing, because a delete "+
			"that matches nothing would report success", target, accountNames(before.Accounts))
	}
	if strings.EqualFold(target, c.User) {
		t.Fatalf("%q is the account of this session — refusing", target)
	}

	// --- 1) WHAT SURVIVES. The session's own account does not count as the
	// answer to "is there a way back in": it is the way in we already have, and
	// the question is whether a second one exists.
	var survivors []string
	var name string
	for _, name = range fullAuthority(before.Accounts) {
		if strings.EqualFold(name, target) {
			continue
		}
		survivors = append(survivors, name)
	}
	t.Logf("full-authority accounts surviving the deletion: %v", survivors)
	var others []string
	for _, name = range survivors {
		if !strings.EqualFold(name, c.User) {
			others = append(others, name)
		}
	}
	if len(others) == 0 {
		t.Fatalf("deleting %q would leave %q as the ONLY full-authority account on a "+
			"device with no serial console — refusing. Survivors would be: %v",
			target, c.User, survivors)
	}
	t.Logf("besides the session's own %q, these full-authority accounts remain: %v", c.User, others)

	// --- 2) The gesture, through the connector.
	if err = c.DeleteAccount(ctx, target); err != nil {
		t.Fatalf("DeleteAccount(%q): %v — transcript %+v", target, err, c.Transcript())
	}
	t.Logf("DeleteAccount(%q) returned without error", target)

	// --- 3) Read back in the SAME session, before saving: a delete that the
	// running configuration did not take must not be saved to flash.
	var mid AccountInventory
	mid, err = c.AuditAccounts(ctx)
	if err != nil {
		t.Fatalf("AuditAccounts after delete: %v", err)
	}
	logInventory(t, "AFTER DELETE (running, not yet saved)", mid)
	if hasAccount(mid.Accounts, target) {
		t.Fatalf("%q is STILL present after DeleteAccount returned success — NOT saving", target)
	}
	var survivor string
	for _, survivor = range survivors {
		if !hasAccount(mid.Accounts, survivor) {
			t.Errorf("account %q disappeared and was not the target — the delete took "+
				"more than it was asked for", survivor)
		}
	}

	// --- 4) Persist. accounts.go's header is explicit that these gestures do not
	// persist themselves, so without this the switch comes back with the account
	// after a reload and the audit that says otherwise is wrong.
	if err = c.Persist(ctx); err != nil {
		t.Fatalf("Persist: %v — %q is gone from the running configuration and flash "+
			"still carries it; save it by hand or the next reload brings it back", err, target)
	}

	// --- 5) And once more after the save.
	var after AccountInventory
	after, err = c.AuditAccounts(ctx)
	if err != nil {
		t.Fatalf("AuditAccounts after save: %v", err)
	}
	logInventory(t, "AFTER SAVE", after)
	if hasAccount(after.Accounts, target) {
		t.Errorf("%q reappeared after \"write memory confirm\"", target)
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}
