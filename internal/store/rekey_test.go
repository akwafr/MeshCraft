package store

// Re-encryption tests.
//
// Two families, separation deliberate:
//
//   - Without database: sealed column inventory. The list whose omission costs secrets lost
//     forever, and it must be locked by a test run on every `go test`, not the next reviewer's
//     vigilance.
//
//   - Requires database: atomicity, verify-by-re-read, and counts. These properties depend on
//     PostgreSQL behavior — rollback, lock, RETURNING — and simulating them proves nothing.
//     They run against a DISPOSABLE database named by MESHCRAFT_TEST_DATABASE_URL, and are
//     skipped otherwise, so `make test` stays runnable without infrastructure.
//
// How to run the integration family (disposable database, delete after):
//
//	docker network create meshcraft-test
//	docker run -d --name meshcraft-testdb --network meshcraft-test \
//	  -e POSTGRES_USER=t -e POSTGRES_PASSWORD=t -e POSTGRES_DB=t postgres:17-alpine
//	docker run --rm --network meshcraft-test -v $PWD:/src -w /src \
//	  -e MESHCRAFT_TEST_DATABASE_URL='postgres://t:t@meshcraft-testdb:5432/t?sslmode=disable' \
//	  golang:1.24-alpine go test -count=1 ./internal/store/...
//	docker rm -f meshcraft-testdb && docker network rm meshcraft-test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/secretbox"
)

// --- Without database: inventory ---

// The inventory is the piece whose error cannot be repaired. A forgotten column is not
// re-encrypted: after rotation and old key deletion, its contents are lost forever. An EXTRA
// column is almost as bad the other way: re-encrypting a fingerprint (token_hash, secret_hash)
// would destroy it, and authentication would stop working.
//
// This test freezes the exact list, both ways.
func TestSealedColumnsInventory(t *testing.T) {
	var want = []string{
		"site_credentials.secret_enc",
		"site_credentials.secret2_enc",
		"credential_generations.secret_enc",
		"credential_generations.secret2_enc",
		"known_credentials.secret_enc",
		// Added with migration 0046: the API token of a foreign controller a
		// site is backed by (ADR-0015). It is a per-SITE secret, so it belongs
		// in the vault and therefore in this inventory — a sealed column
		// missing here is a secret destroyed at the next key rotation.
		"site_extensions.token_enc",
		// Added with migration 0048: the Telegram bot token, or the whole Slack
		// webhook URL, of a notification channel (ADR-0024). Same reasoning as
		// 0046 — a per-channel secret an operator edits on screen belongs in the
		// vault — and the same trap: absent from this inventory, the token is
		// destroyed at the next key rotation without a word.
		"notify_channels.token_enc",
	}
	var got []string
	var c SealedColumn
	for _, c = range SealedColumns() {
		got = append(got, c.Table+"."+c.Column)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("inventory changed.\nexpected: %v\ngot:      %v", want, got)
	}

	// bytea columns that are NOT ciphertext must never enter here.
	var forbidden = []string{"token_hash", "secret_hash", "value", "fingerprint",
		"pw_hash", "migrate_token"}
	var col string
	for _, col = range forbidden {
		for _, c = range SealedColumns() {
			if c.Column == col {
				t.Fatalf("%s.%s is a fingerprint or key, not a sealed secret: re-encrypting "+
					"would destroy it", c.Table, c.Column)
			}
		}
	}
}

// Locks are always taken in the same order, otherwise two concurrent operations deadlock — and
// a deadlock on this operation leaves the operator facing a hung process, unsure whether to
// kill it.
func TestStableLockOrder(t *testing.T) {
	var a = strings.Join(sealedTables(), ",")
	var b = strings.Join(sealedTables(), ",")
	if a != b {
		t.Fatal("lock order is not deterministic")
	}
	if a != "credential_generations,known_credentials,notify_channels,site_credentials,site_extensions" {
		t.Fatalf("unexpected order: %s", a)
	}
	// Each table once: locking the same twice is not a SQL error, but would signal a duplicated
	// inventory entry.
	if len(sealedTables()) != 5 {
		t.Fatalf("%d tables, expected 5", len(sealedTables()))
	}
}

// --- With database: re-encryption ---

// testStore opens the disposable database and applies migrations, or skips the test.
func testStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	var url = os.Getenv("MESHCRAFT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MESHCRAFT_TEST_DATABASE_URL missing: integration test skipped " +
			"(see file header to start a disposable database)")
	}
	var ctx = context.Background()
	var st, err = New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(st.Close)
	if err = st.Migrate(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	// Clean slate between tests: each counts what it placed itself, and residue would pass a
	// false test.
	var stmt string
	for _, stmt = range []string{
		`delete from credential_generations`,
		`delete from device_credential_state`,
		`delete from site_credentials`,
		`delete from known_credentials`,
		`delete from sites`,
		`delete from instance_secrets`,
		`delete from credentials_key_fingerprint`,
	} {
		if _, err = st.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("cleanup (%s): %v", stmt, err)
		}
	}
	return st, ctx
}

// vault describes content placed by a test, for later verification.
type vault struct {
	// plaintexts: "table.column" -> row id -> expected plaintext.
	plaintexts map[string]map[string]string
	// ignores: deliberately unreadable rows, placed by a test to verify refusal. Key
	// "table.column|id".
	ignores map[string]bool
}

// ignore marks a row verification must leave alone.
func (v *vault) ignore(col, id string) {
	if v.ignores == nil {
		v.ignores = map[string]bool{}
	}
	v.ignores[col+"|"+id] = true
}

func (v *vault) note(col, id, plaintext string) {
	if v.plaintexts == nil {
		v.plaintexts = map[string]map[string]string{}
	}
	if v.plaintexts[col] == nil {
		v.plaintexts[col] = map[string]string{}
	}
	v.plaintexts[col][id] = plaintext
}

// seedVault fills the vault with a realistic shape: one site, one admin password (single
// phrase), one SNMPv3 identity (TWO phrases), one prior generation for each, and two standard
// discovery credentials. Production instance shape, in miniature.
func seedVault(t *testing.T, st *Store, ctx context.Context, key []byte) *vault {
	t.Helper()
	var box, err = secretbox.New(key, false)
	if err != nil {
		t.Fatal(err)
	}
	var seal = func(plaintext string) []byte {
		var enc, sealErr = box.Seal(plaintext)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return enc
	}
	var v = &vault{}

	var siteID string
	if err = st.pool.QueryRow(ctx,
		`insert into sites (name, slug) values ('Test site', 'test-site') returning id::text`).Scan(&siteID); err != nil {
		t.Fatalf("site: %v", err)
	}

	// Admin password: secret2_enc stays NULL, as in production.
	var adminID string
	if err = st.pool.QueryRow(ctx,
		`insert into site_credentials (site_id, group_name, kind, username, secret_enc)
		 values ($1::uuid, '', 'admin', 'mcadmin', $2) returning id::text`,
		siteID, seal("MotDePasseAdmin42")).Scan(&adminID); err != nil {
		t.Fatalf("admin credential: %v", err)
	}
	v.note("site_credentials.secret_enc", adminID, "MotDePasseAdmin42")

	// SNMPv3 identity: the PAIR.
	var v3ID string
	if err = st.pool.QueryRow(ctx,
		`insert into site_credentials (site_id, group_name, kind, username, secret_enc, secret2_enc)
		 values ($1::uuid, '', 'snmp_v3', 'mcmon', $2, $3) returning id::text`,
		siteID, seal("PhraseAuth9000"), seal("PhraseChiffrement7777")).Scan(&v3ID); err != nil {
		t.Fatalf("v3 identity: %v", err)
	}
	v.note("site_credentials.secret_enc", v3ID, "PhraseAuth9000")
	v.note("site_credentials.secret2_enc", v3ID, "PhraseChiffrement7777")

	// Generations: history, without which a device left behind becomes unreachable. Two for
	// admin (one retiring), one pair for v3.
	var genID string
	if err = st.pool.QueryRow(ctx,
		`insert into credential_generations (credential_id, generation, secret_enc, state)
		 values ($1::uuid, 1, $2, 'retiring') returning id::text`,
		adminID, seal("AncienMotDePasse01")).Scan(&genID); err != nil {
		t.Fatalf("generation 1: %v", err)
	}
	v.note("credential_generations.secret_enc", genID, "AncienMotDePasse01")

	if err = st.pool.QueryRow(ctx,
		`insert into credential_generations (credential_id, generation, secret_enc, state)
		 values ($1::uuid, 2, $2, 'active') returning id::text`,
		adminID, seal("MotDePasseAdmin42")).Scan(&genID); err != nil {
		t.Fatalf("generation 2: %v", err)
	}
	v.note("credential_generations.secret_enc", genID, "MotDePasseAdmin42")

	if err = st.pool.QueryRow(ctx,
		`insert into credential_generations (credential_id, generation, secret_enc, secret2_enc, state)
		 values ($1::uuid, 1, $2, $3, 'active') returning id::text`,
		v3ID, seal("PhraseAuth9000"), seal("PhraseChiffrement7777")).Scan(&genID); err != nil {
		t.Fatalf("v3 generation: %v", err)
	}
	v.note("credential_generations.secret_enc", genID, "PhraseAuth9000")
	v.note("credential_generations.secret2_enc", genID, "PhraseChiffrement7777")

	// Standard discovery credentials.
	var kc string
	var pairs = [][2]string{{"Cisco factory", "cisco"}, {"HPE factory", "admin-hpe"}}
	var p [2]string
	for _, p = range pairs {
		if err = st.pool.QueryRow(ctx,
			`insert into known_credentials (name, kind, username, secret_enc)
			 values ($1, 'ssh', 'admin', $2) returning id::text`,
			p[0], seal(p[1])).Scan(&kc); err != nil {
			t.Fatalf("standard credential: %v", err)
		}
		v.note("known_credentials.secret_enc", kc, p[1])
	}

	// The key, where it lives today: in the database, next to what it protects. Exactly what
	// rotation fixes.
	if err = st.PutInstanceSecret(ctx, CredentialsKeyName, key); err != nil {
		t.Fatalf("instance key: %v", err)
	}
	return v
}

// assertOpensWith re-reads the ENTIRE vault and verifies each value opens with `key` and
// yields expected plaintext. `forbiddenKey` must open nothing.
func assertOpensWith(t *testing.T, st *Store, ctx context.Context, v *vault,
	key, forbiddenKey []byte) {
	t.Helper()
	var box, err = secretbox.New(key, false)
	if err != nil {
		t.Fatal(err)
	}
	var forbiddenBox *secretbox.Service
	if forbiddenKey != nil {
		if forbiddenBox, err = secretbox.New(forbiddenKey, false); err != nil {
			t.Fatal(err)
		}
	}
	var col SealedColumn
	var n int
	for _, col = range SealedColumns() {
		var name = col.Table + "." + col.Column
		var rows, qErr = st.pool.Query(ctx,
			`select id::text, `+col.Column+` from `+col.Table+
				` where `+col.Column+` is not null and octet_length(`+col.Column+`) > 0`)
		if qErr != nil {
			t.Fatal(qErr)
		}
		for rows.Next() {
			var id string
			var enc []byte
			if err = rows.Scan(&id, &enc); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if v.ignores[name+"|"+id] {
				continue
			}
			var got string
			if got, err = box.Open(enc); err != nil {
				rows.Close()
				t.Fatalf("%s id=%s: unreadable with expected key: %v", name, id, err)
			}
			var want = v.plaintexts[name][id]
			if got != want {
				rows.Close()
				t.Fatalf("%s id=%s: plaintext %q, expected %q", name, id, got, want)
			}
			if forbiddenBox != nil {
				if _, err = forbiddenBox.Open(enc); err == nil {
					rows.Close()
					t.Fatalf("%s id=%s: still opens with key that should no longer work — "+
						"re-encryption did not happen", name, id)
				}
			}
			n++
		}
		rows.Close()
	}
	var want int
	var m map[string]string
	for _, m = range v.plaintexts {
		want += len(m)
	}
	if n != want {
		t.Fatalf("%d value(s) re-read, %d expected", n, want)
	}
}

// Nominal path: after re-encryption, everything reads with the NEW key and nothing with the
// old; counts match; fingerprint is the new key's; old key gone from database.
func TestRekeyNominalPath(t *testing.T) {
	var st, ctx = testStore(t)
	var oldKey, _ = secretbox.NewRandomKey()
	var newKey, _ = secretbox.NewRandomKey()
	var v = seedVault(t, st, ctx, oldKey)

	// Inventory BEFORE, the one the operator compares to the report.
	var before, err = st.CountSealed(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var report RekeyReport
	report, err = st.RekeyCredentials(ctx, oldKey, newKey, RekeyOptions{
		LockTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("re-encryption: %v", err)
	}

	// Exact counts, column by column: 2 credentials -> 2 secret_enc, 1 secret2_enc; 3
	// generations -> 3 secret_enc, 1 secret2_enc; 2 standard credentials.
	var want = map[string]int{
		"site_credentials.secret_enc":        2,
		"site_credentials.secret2_enc":       1,
		"credential_generations.secret_enc":  3,
		"credential_generations.secret2_enc": 1,
		"known_credentials.secret_enc":       2,
	}
	var c RekeyCount
	for _, c = range report.Columns {
		var name = c.Table + "." + c.Column
		if c.Rows != want[name] {
			t.Errorf("%s: %d row(s), %d expected", name, c.Rows, want[name])
		}
		if c.Resealed != want[name] {
			t.Errorf("%s: %d re-sealed, %d expected", name, c.Resealed, want[name])
		}
		if c.Already != 0 {
			t.Errorf("%s: %d unexpected already-up-to-date", name, c.Already)
		}
	}
	var rows, resealed, already = report.Total()
	if rows != 9 || resealed != 9 || already != 0 {
		t.Errorf("totals: %d rows, %d re-sealed, %d already up to date (expected 9/9/0)",
			rows, resealed, already)
	}
	// Verify-by-re-read is the central promise: it must cover ALL values, not a sample.
	if report.Verified != rows {
		t.Errorf("%d value(s) re-read and decrypted before commit, %d expected",
			report.Verified, rows)
	}
	if !report.OldKeyDeleted {
		t.Error("old key was not deleted from instance_secrets")
	}
	if report.NoOp {
		t.Error("NoOp while 9 values were re-sealed")
	}
	// Report must match pre-operation inventory.
	var i int
	for i = range before {
		if before[i].Rows != report.Columns[i].Rows {
			t.Errorf("%s.%s: inventory %d, report %d",
				before[i].Table, before[i].Column, before[i].Rows, report.Columns[i].Rows)
		}
	}

	// Vault: readable with new, not at all with old.
	assertOpensWith(t, st, ctx, v, newKey, oldKey)

	// Old key no longer in database — a key no longer needed is just a leak waiting.
	var _, keyErr = st.InstanceSecretValue(ctx, CredentialsKeyName)
	if keyErr != ErrNotFound {
		t.Errorf("key still in database (err=%v)", keyErr)
	}
	// Fingerprint is the new key's: it will authorize next startup and refuse the old one.
	var fp []byte
	if fp, err = st.CredentialsKeyFingerprint(ctx); err != nil {
		t.Fatal(err)
	}
	if !secretbox.SameFingerprint(fp, secretbox.Fingerprint(newKey)) {
		t.Error("stored fingerprint is not the new key's")
	}
}

// THE batch's most important property: interruption mid-way must never leave half the vault in
// one key and half in the other. That state is recovered only by hand, secret by secret, on a
// database holding real production passwords.
//
// Cut deliberately after the third write — mid-operation, rows written behind and intact rows
// ahead — and verify the database did NOT move one byte.
func TestRekeyIsAtomic(t *testing.T) {
	var st, ctx = testStore(t)
	var oldKey, _ = secretbox.NewRandomKey()
	var newKey, _ = secretbox.NewRandomKey()
	var v = seedVault(t, st, ctx, oldKey)

	var boom = errors.New("simulated cut mid-operation")
	var _, err = st.RekeyCredentials(ctx, oldKey, newKey, RekeyOptions{
		LockTimeout: 10 * time.Second,
		AfterRow: func(n int) error {
			if n == 3 {
				return boom
			}
			return nil
		},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("simulated failure did not propagate: %v", err)
	}

	// Everything must still open with the OLD key, nothing with the new: database exactly as
	// before, fully functional, operation can be retried.
	assertOpensWith(t, st, ctx, v, oldKey, newKey)

	// Side effects must be rolled back too: fingerprint written without secrets would refuse
	// startup on a healthy database.
	var fp []byte
	if fp, err = st.CredentialsKeyFingerprint(ctx); err != nil {
		t.Fatal(err)
	}
	if fp != nil {
		t.Error("fingerprint was stored despite rollback")
	}
	var key []byte
	if key, err = st.InstanceSecretValue(ctx, CredentialsKeyName); err != nil {
		t.Fatalf("old key was deleted despite rollback: %v", err)
	}
	if string(key) != string(oldKey) {
		t.Error("database key changed despite rollback")
	}
}

// Replaying on an ALREADY rotated database must be harmless. That allows relaunching calmly
// when unsure whether the first attempt succeeded — common after a terminal disconnect.
func TestRekeyIsIdempotent(t *testing.T) {
	var st, ctx = testStore(t)
	var oldKey, _ = secretbox.NewRandomKey()
	var newKey, _ = secretbox.NewRandomKey()
	var v = seedVault(t, st, ctx, oldKey)

	var _, err = st.RekeyCredentials(ctx, oldKey, newKey, RekeyOptions{})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// Key gone from database; put the SAME old one back to replay exactly the call, as if the
	// operator reran the command.
	if err = st.PutInstanceSecret(ctx, CredentialsKeyName, oldKey); err != nil {
		t.Fatal(err)
	}

	var report RekeyReport
	report, err = st.RekeyCredentials(ctx, oldKey, newKey, RekeyOptions{})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	var rows, resealed, already = report.Total()
	if resealed != 0 {
		t.Errorf("%d value(s) re-sealed on second pass, expected 0", resealed)
	}
	if already != rows || rows != 9 {
		t.Errorf("second pass: %d rows of which %d already up to date (expected 9/9)", rows, already)
	}
	if !report.NoOp {
		t.Error("NoOp should be true: nothing needed re-sealing")
	}
	if report.Verified != rows {
		t.Errorf("incomplete verification: %d/%d", report.Verified, rows)
	}
	// Vault intact.
	assertOpensWith(t, st, ctx, v, newKey, oldKey)
}

// A value unreadable with BOTH keys must abort the entire operation. Skipping it would be the
// worst choice: rotation would sign permanent loss of what we failed to read, since the old
// key disappears.
func TestRekeyRefusesAnUnreadableValue(t *testing.T) {
	var st, ctx = testStore(t)
	var oldKey, _ = secretbox.NewRandomKey()
	var newKey, _ = secretbox.NewRandomKey()
	var v = seedVault(t, st, ctx, oldKey)

	// Value sealed by a THIRD key, neither opens.
	var lost, _ = secretbox.NewRandomKey()
	var box, _ = secretbox.New(lost, false)
	var enc, _ = box.Seal("secret from elsewhere")
	var orphan string
	var err = st.pool.QueryRow(ctx,
		`insert into known_credentials (name, kind, username, secret_enc)
		 values ('From elsewhere', 'ssh', 'admin', $1) returning id::text`,
		enc).Scan(&orphan)
	if err != nil {
		t.Fatal(err)
	}
	// Global verification must ignore this row: readable by neither key, that is its point.
	v.ignore("known_credentials.secret_enc", orphan)

	_, err = st.RekeyCredentials(ctx, oldKey, newKey, RekeyOptions{})
	if !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("expected ErrUndecryptable, got: %v", err)
	}
	// Nothing moved: rest of vault still opens with old key.
	assertOpensWith(t, st, ctx, v, oldKey, nil)
	var fp []byte
	if fp, err = st.CredentialsKeyFingerprint(ctx); err != nil {
		t.Fatal(err)
	}
	if fp != nil {
		t.Error("fingerprint was stored despite failure")
	}
}

// Empty vault (new instance) re-encrypts without error and does nothing: operation must be a
// non-event, not a special case that crashes.
func TestRekeyOnEmptyVault(t *testing.T) {
	var st, ctx = testStore(t)
	var oldKey, _ = secretbox.NewRandomKey()
	var newKey, _ = secretbox.NewRandomKey()
	var seedErr = st.PutInstanceSecret(ctx, CredentialsKeyName, oldKey)
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	var report, err = st.RekeyCredentials(ctx, oldKey, newKey, RekeyOptions{})
	if err != nil {
		t.Fatalf("re-encryption of empty vault: %v", err)
	}
	var rows, _, _ = report.Total()
	if rows != 0 || report.Verified != 0 {
		t.Errorf("empty vault: %d row(s), %d verified", rows, report.Verified)
	}
	if !report.OldKeyDeleted {
		t.Error("old key should have been removed")
	}
	var fp []byte
	if fp, err = st.CredentialsKeyFingerprint(ctx); err != nil {
		t.Fatal(err)
	}
	if !secretbox.SameFingerprint(fp, secretbox.Fingerprint(newKey)) {
		t.Error("fingerprint not stored on empty vault")
	}
}

// Fingerprint: one row, no matter what. Two rows would mean two contradictory truths about
// what sealed the database — and the guard would read one at random.
func TestFingerprintStaysUnique(t *testing.T) {
	var st, ctx = testStore(t)
	var a, _ = secretbox.NewRandomKey()
	var b, _ = secretbox.NewRandomKey()
	var err = st.SetCredentialsKeyFingerprint(ctx, secretbox.Fingerprint(a))
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetCredentialsKeyFingerprint(ctx, secretbox.Fingerprint(b)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = st.pool.QueryRow(ctx,
		`select count(*) from credentials_key_fingerprint`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d fingerprint row(s), expected 1", n)
	}
	var fp []byte
	if fp, err = st.CredentialsKeyFingerprint(ctx); err != nil {
		t.Fatal(err)
	}
	if !secretbox.SameFingerprint(fp, secretbox.Fingerprint(b)) {
		t.Fatal("last written fingerprint is not what we read back")
	}
}

// Missing fingerprint is not an error: state of every pre-migration-0024 instance, and
// treating it as error would refuse to start the entire installed fleet.
func TestMissingFingerprintIsNotAnError(t *testing.T) {
	var st, ctx = testStore(t)
	var fp, err = st.CredentialsKeyFingerprint(ctx)
	if err != nil {
		t.Fatalf("missing fingerprint treated as error: %v", err)
	}
	if fp != nil {
		t.Fatal("unexpected fingerprint")
	}
}
