package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The rule that decides WHICH stored password a rotation is allowed to rewrite.
//
// WHAT THESE TESTS REACH, said plainly so a green run is not over-read. `Store` holds a
// concrete `*pgxpool.Pool`, so `AddGeneration` itself cannot be entered without
// PostgreSQL. What is asserted instead is everything it delegates:
//
//   - the SCOPING RULE, in full, as a pure function (resyncTargets) — the part where a
//     wrong answer either leaves the defect armed or overwrites a password nobody asked
//     to touch;
//   - the WRITES per selected row and their ORDER, and the fact that the first failure
//     stops the fan-out dead, through a fake `credTx` that records statements.
//
// WHAT STAYS OUT OF REACH: that the rotation and the fan-out share ONE transaction, and
// that an error therefore rolls back both. The fake proves the fan-out returns the error
// rather than swallowing it, and reading `AddGeneration` shows the error path returns
// before `tx.Commit` under a deferred `tx.Rollback` — but "PostgreSQL discarded the
// partial work" is a property of the database, and only a database can prove it. It is
// not asserted here and must not be read into a green run.
//
// Also out of reach: the `for update` lock in `credSiblingRows`, and the SQL text of
// every statement below beyond the substring each test keys on.

// --- the scoping rule ---

// The credential being rotated in every case below: the site's administration password
// for the account the vault manages.
var rotatedAdmin = credRow{ID: "cred-admin", Kind: "admin", Username: "mcadmin"}

// THE DEFECT ITSELF. A `gnmi` row naming the managed account is a COPY of that account's
// password. Before this rule it kept generation 4 while the fleet moved to 5, telemetry
// died, and `gnmi_targets` still read 1 because the credential RESOLVED — it was merely
// wrong.
func TestGNMISiblingOnTheSameAccountIsResynced(t *testing.T) {
	var got = resyncTargets(rotatedAdmin, "", []credRow{
		{ID: "cred-gnmi", Kind: "gnmi", Username: "mcadmin"},
	})
	if len(got) != 1 || got[0].ID != "cred-gnmi" {
		t.Fatalf("the gnmi copy of mcadmin must follow the rotation, selected %+v", got)
	}
}

// THE OTHER HALF OF THE SAME RULE, and the one that must never be weakened. A telemetry
// credential naming a DEDICATED account with its own password is not a copy of anything:
// rewriting its secret would break gNMI on a site that had done everything right, which
// is a worse outcome than the defect being fixed.
func TestGNMISiblingOnAnotherAccountIsNeverTouched(t *testing.T) {
	var got = resyncTargets(rotatedAdmin, "", []credRow{
		{ID: "cred-gnmi", Kind: "gnmi", Username: "telemetry"},
	})
	if len(got) != 0 {
		t.Fatalf("a gnmi credential naming its own account must not follow, selected %+v", got)
	}
}

// Case is not folded, and that is a decision rather than an oversight. Folding is right
// when TRYING a login and is a guess when it AUTHORISES A WRITE: if two rows disagree
// about the spelling of an account, the honest outcome is that one does not move and an
// operator sees it, not that meshcraft picks a meaning and overwrites a password.
func TestUsernameMatchIsExactNotFolded(t *testing.T) {
	var got = resyncTargets(rotatedAdmin, "", []credRow{
		{ID: "cred-gnmi", Kind: "gnmi", Username: "MCADMIN"},
	})
	if len(got) != 0 {
		t.Fatalf("username matching must be exact, selected %+v", got)
	}
}

// An `admin` row is the KIND being rotated. The one carrying the rotation is handled by
// the rotation itself, and the OTHERS are a group's or a technical tag's password —
// different secrets on purpose (pwpolicy_creds.go creates them precisely because one
// password does not fit every platform). Rewriting either would be a fresh defect.
func TestAdminRowsAreNeverRewritten(t *testing.T) {
	var got = resyncTargets(rotatedAdmin, "", []credRow{
		rotatedAdmin,
		{ID: "cred-admin-len15", Kind: "admin", Username: "mcadmin"},
	})
	if len(got) != 0 {
		t.Fatalf("no admin row may be rewritten by the fan-out, selected %+v", got)
	}
}

// An SNMPv3 security name that happens to spell the managed account is NOT a copy of its
// login password: it is one half of a key PAIR derived from two phrases, which rotate
// together or not at all. Writing the admin password into it would leave the encryption
// key derived from the old phrase, and the device would silently drop what it cannot
// decrypt — this defect's mirror image. A v2c community has no account at all.
func TestSNMPCredentialsAreNeverResynced(t *testing.T) {
	var got = resyncTargets(rotatedAdmin, "", []credRow{
		{ID: "cred-v3", Kind: "snmp_v3", Username: "mcadmin"},
		{ID: "cred-v2c", Kind: "snmp_v2c", Username: ""},
	})
	if len(got) != 0 {
		t.Fatalf("SNMP identities must not follow an admin rotation, selected %+v", got)
	}
}

// A GROUP'S OR A TECHNICAL TAG'S ADMIN PASSWORD IS A DIFFERENT PASSWORD FOR A SUBSET, on
// purpose — `pwpolicy-len15` exists because some platforms refuse the site secret. Those
// rows name the same account, so without this guard, rotating one would rewrite the
// site-wide telemetry row to a secret only a handful of switches carry: the defect being
// fixed, re-created by its own fix, on a fleet that was working.
func TestASubsetAdminPasswordDoesNotSpeakForTheSite(t *testing.T) {
	var sibling = []credRow{{ID: "cred-gnmi", Kind: "gnmi", Username: "mcadmin"}}
	var tagged = credRow{ID: "cred-len15", Kind: "admin", Username: "mcadmin",
		Tag: "pwpolicy-len15"}
	if len(resyncTargets(tagged, "", sibling)) != 0 {
		t.Error("a technical-tag admin rotation must move nothing")
	}
	var grouped = credRow{ID: "cred-grp", Kind: "admin", Username: "mcadmin",
		GroupName: "arista-7050"}
	if len(resyncTargets(grouped, "", sibling)) != 0 {
		t.Error("a group-scoped admin rotation must move nothing")
	}
	// And the site default still does, which is the case observed in production.
	if len(resyncTargets(rotatedAdmin, "", sibling)) != 1 {
		t.Error("the site's default admin rotation must still carry its copies")
	}
}

// THE DIRECTION IS NOT SYMMETRIC. Rotating the telemetry row must not move the admin
// row: that would let a telemetry form set the password meshcraft believes the whole
// fleet carries, and the next deployment run would push it to every switch.
func TestOnlyAnAdminRotationFansOut(t *testing.T) {
	var got = resyncTargets(credRow{ID: "cred-gnmi", Kind: "gnmi", Username: "mcadmin"}, "",
		[]credRow{rotatedAdmin})
	if len(got) != 0 {
		t.Fatalf("a gnmi rotation must not move the admin row, selected %+v", got)
	}
}

// Under `per_device` there is no single secret for the account at this site, so there is
// nothing one shared sibling row could be re-synced TO. Pinning it to whichever device
// rotated last would be worse than leaving it stale: it would look current.
func TestPerDeviceRotationFansOutToNothing(t *testing.T) {
	var got = resyncTargets(rotatedAdmin, "dev-1", []credRow{
		{ID: "cred-gnmi", Kind: "gnmi", Username: "mcadmin"},
	})
	if len(got) != 0 {
		t.Fatalf("a per-device rotation must not move a shared sibling, selected %+v", got)
	}
}

// A credential that vanished under the transaction reads back as a zero row, and the
// rule must refuse it rather than treat an empty account name as matching the other
// empty account names on the site.
func TestAMissingRotatedRowSelectsNothing(t *testing.T) {
	var got = resyncTargets(credRow{}, "", []credRow{
		{ID: "cred-v2c", Kind: "snmp_v2c", Username: ""},
		{ID: "cred-gnmi", Kind: "gnmi", Username: ""},
	})
	if len(got) != 0 {
		t.Fatalf("an unknown rotated row must select nothing, selected %+v", got)
	}
}

// Selection order is the input order, so the journal line lists the same rows the same
// way twice and a run is reproducible.
func TestSelectionKeepsInputOrder(t *testing.T) {
	var got = resyncTargets(rotatedAdmin, "", []credRow{
		{ID: "cred-b", Kind: "gnmi", Username: "mcadmin"},
		{ID: "cred-a", Kind: "netconf", Username: "mcadmin"},
	})
	if len(got) != 2 || got[0].ID != "cred-b" || got[1].ID != "cred-a" {
		t.Fatalf("selection must preserve input order, got %+v", got)
	}
}

// --- what the fan-out writes, and where it stops ---

// fakeTx records the statements a fan-out issues and can be told to fail at one of them.
//
// It implements [credTx], which is the two methods `addGenerationTx` and
// `resyncSiblingsTx` use. Everything a real [pgx.Tx] does beyond that — the transaction
// itself — is deliberately absent: this fake exists to pin control flow, not SQL.
type fakeTx struct {
	stmts []string
	// failAt: 1-based index of the statement that returns an error, 0 for none.
	failAt int
	calls  int
	// gen is what the "next generation" query answers with.
	gen int
}

func (f *fakeTx) note(sql string) error {
	f.calls++
	f.stmts = append(f.stmts, sql)
	if f.failAt == f.calls {
		return errors.New("write refused")
	}
	return nil
}

func (f *fakeTx) Exec(ctx context.Context, sql string,
	args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, f.note(sql)
}

func (f *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return &fakeRow{err: f.note(sql), gen: f.gen}
}

// fakeRow fills whatever the caller scans into, by destination type. The only value that
// carries meaning is the generation number; the rest exist so the scan succeeds.
type fakeRow struct {
	err error
	gen int
}

func (r *fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	var i int
	for i = range dest {
		var pi, isInt = dest[i].(*int)
		if isInt {
			*pi = r.gen
			continue
		}
		var ps, isStr = dest[i].(*string)
		if isStr {
			*ps = ""
			continue
		}
		var pt, isTime = dest[i].(*time.Time)
		if isTime {
			*pt = time.Time{}
			continue
		}
		var ptp, isTimePtr = dest[i].(**time.Time)
		if isTimePtr {
			*ptp = nil
			continue
		}
	}
	return nil
}

func countStmts(stmts []string, needle string) int {
	var n int
	var s string
	for _, s = range stmts {
		if strings.Contains(s, needle) {
			n++
		}
	}
	return n
}

// A selected sibling gets a REAL GENERATION, not a bare head update — its previous
// secret must stay usable as a `retiring` candidate, and `RevealSiteCredential` reads the
// active generation in preference to the head column, so a head-only write would be read
// back as the old secret by the very consumer this fixes.
func TestEachSiblingGetsAGenerationAndAHeadUpdate(t *testing.T) {
	var tx = &fakeTx{gen: 3}
	var moved, err = resyncSiblingsTx(context.Background(), tx,
		[]credRow{{ID: "cred-gnmi", Kind: "gnmi", Username: "mcadmin"}},
		[]byte("sealed"), "user-1")
	if err != nil {
		t.Fatalf("fan-out failed: %v", err)
	}
	if len(moved) != 1 || moved[0].CredentialID != "cred-gnmi" || moved[0].Generation != 3 {
		t.Fatalf("report = %+v", moved)
	}
	if countStmts(tx.stmts, "insert into credential_generations") != 1 {
		t.Errorf("a sibling must get its own generation row, statements: %v", tx.stmts)
	}
	if countStmts(tx.stmts, "set state = 'retiring'") != 1 {
		t.Errorf("the sibling's previous generation must be retired, not dropped: %v", tx.stmts)
	}
	if countStmts(tx.stmts, "update site_credentials set secret_enc") != 1 {
		t.Errorf("the sibling's head must follow its generation: %v", tx.stmts)
	}
}

// A FAILURE LEAVES NEITHER MOVED. The fan-out stops at the first refusal and returns it,
// so the rotation's transaction is rolled back with it — a rotation that advanced the
// admin generation and half the copies is exactly the state this mechanism exists to
// make impossible.
//
// What is proven here is that nothing is attempted on the SECOND sibling and that the
// error reaches the caller. That the database then discards the first sibling's writes is
// the transaction's job, and it is not asserted without one (cf. this file's header).
func TestTheFanOutStopsAtTheFirstFailure(t *testing.T) {
	var targets = []credRow{
		{ID: "cred-gnmi", Kind: "gnmi", Username: "mcadmin"},
		{ID: "cred-other", Kind: "netconf", Username: "mcadmin"},
	}
	// Statement 4 is the first sibling's head update: its generation is already written
	// at that point, which is the shape that would strand a half-applied fan-out.
	var tx = &fakeTx{gen: 3, failAt: 4}
	var moved, err = resyncSiblingsTx(context.Background(), tx, targets,
		[]byte("sealed"), "user-1")
	if err == nil {
		t.Fatal("a refused write must abort the whole rotation, not be reported as success")
	}
	if moved != nil {
		t.Errorf("a failed fan-out must report nothing as moved, got %+v", moved)
	}
	if countStmts(tx.stmts, "insert into credential_generations") != 1 {
		t.Errorf("the second sibling must not be touched after the first failed: %v", tx.stmts)
	}
}

// A rotation with no copy of its account writes nothing extra: the mechanism is invisible
// on the sites it does not concern, which is most of them.
func TestNoTargetsWritesNothing(t *testing.T) {
	var tx = &fakeTx{gen: 2}
	var moved, err = resyncSiblingsTx(context.Background(), tx, nil, []byte("sealed"), "")
	if err != nil || len(moved) != 0 {
		t.Fatalf("moved = %+v, err = %v", moved, err)
	}
	if len(tx.stmts) != 0 {
		t.Errorf("nothing must be written when nothing follows: %v", tx.stmts)
	}
}

// THE SECOND PHRASE IS NOT PROPAGATED. It belongs to `snmp_v3` alone, and v3 is a kind
// the rule cannot select; passing nil is what makes both the generation insert and the
// head update leave whatever is stored alone instead of asserting an empty pair.
func TestSiblingsNeverReceiveASecondPhrase(t *testing.T) {
	var seen []any
	var tx = &recordingTx{fakeTx: fakeTx{gen: 1}, args: &seen}
	var _, err = resyncSiblingsTx(context.Background(), tx,
		[]credRow{{ID: "cred-gnmi", Kind: "gnmi", Username: "mcadmin"}},
		[]byte("sealed"), "user-1")
	if err != nil {
		t.Fatalf("fan-out failed: %v", err)
	}
	// The generation insert takes secret2_enc as its sixth parameter (cf.
	// addGenerationTx): it must arrive nil.
	var a any
	var found bool
	for _, a = range seen {
		var b, isBytes = a.([]byte)
		if isBytes && string(b) == "sealed" {
			found = true
		}
	}
	if !found {
		t.Fatal("the new secret must reach the sibling")
	}
	var i int
	for i = range seen {
		var b, isBytes = seen[i].([]byte)
		if isBytes && len(b) > 0 && string(b) != "sealed" {
			t.Fatalf("a sibling received a second secret it has no use for: %q", b)
		}
	}
}

// recordingTx is fakeTx plus the arguments, for the one assertion that is about a
// parameter rather than a statement.
type recordingTx struct {
	fakeTx
	args *[]any
}

func (f *recordingTx) Exec(ctx context.Context, sql string,
	args ...any) (pgconn.CommandTag, error) {
	*f.args = append(*f.args, args...)
	return f.fakeTx.Exec(ctx, sql, args...)
}

func (f *recordingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	*f.args = append(*f.args, args...)
	return f.fakeTx.QueryRow(ctx, sql, args...)
}
