package store

// Credentials vault key rotation: fingerprint of the active key, and transactional
// re-encryption of everything sealed.
//
// WHY THIS FILE EXISTS. The key that seals the vault lived in the same database as the
// secrets it protects (instance_secrets/credentials-key). Moving it to the environment is
// real progress — a dump leak delivers nothing — but rotation was a minefield: setting
// MESHCRAFT_SECRET_KEY on an existing instance switched the controller to the new key
// without ever looking at the old one, and made the ENTIRE vault silently unreadable. Two
// pieces fix that: the fingerprint (the controller refuses to start on the wrong key) and
// re-encryption (the only path that completes rotation for good).
//
// WHY RE-ENCRYPTION LIVES IN THE STORE. Because the batch's most important property is
// ATOMICITY: an interruption must never leave half the vault in one key and half in the
// other — a state recovered only by hand, secret by secret, on a database holding real
// production passwords. That requires read, re-seal, verification re-read, fingerprint
// update, and old key deletion in ONE transaction. Moving the transaction out of the store
// package to drive it elsewhere would mean exposing pgx.Tx, i.e. making it possible not to
// close it.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"meshcraft/internal/secretbox"
)

// --- Inventory of sealed columns ---

// SealedColumn is a column whose contents go through secretbox.Seal.
type SealedColumn struct {
	Table  string
	Column string
}

// SealedColumns is the EXHAUSTIVE inventory of what the vault key protects.
//
// Built by cross-checking three sources, and all three must yield the same list: Secrets.Seal
// calls (internal/api), `bytea` columns in the schema (internal/store/migrations), and queries
// that re-read those columns. Forgetting a column here permanently loses the secrets it holds
// at rotation time.
//
// What is sealed:
//
//	site_credentials.secret_enc        — admin password, SNMP community…
//	site_credentials.secret2_enc       — second phrase of an SNMPv3 identity (nullable, migration 0017)
//	credential_generations.secret_enc  — rotation history, required to reach a device left behind
//	credential_generations.secret2_enc — v3 pair for that generation (nullable)
//	known_credentials.secret_enc       — "standard" credentials tried by discovery
//
// What is `bytea` but NOT ciphertext, and must not be touched — confusing them breaks auth
// without gaining anything:
//
//	enroll_tokens.token_hash  — fingerprint, not encrypted
//	sessions.token_hash       — fingerprint, not encrypted
//	proxies.secret_hash       — fingerprint, not encrypted
//	instance_secrets.value    — IS the key (32 bytes), not something sealed with it
//	credentials_key_fingerprint.fingerprint — key fingerprint
//
// Neighbor case deliberately left aside: proxies.migrate_token is plaintext `text`, not sealed.
// Not our concern here, but noted so the next inventory review does not stop on it twice.
func SealedColumns() []SealedColumn {
	return []SealedColumn{
		{Table: "site_credentials", Column: "secret_enc"},
		{Table: "site_credentials", Column: "secret2_enc"},
		{Table: "credential_generations", Column: "secret_enc"},
		{Table: "credential_generations", Column: "secret2_enc"},
		{Table: "known_credentials", Column: "secret_enc"},
	}
}

// sealedTables returns inventory tables, each once, sorted — to always lock in the SAME
// order. Two lock acquisitions on the same table set in different orders is a deadlock
// waiting for concurrency.
func sealedTables() []string {
	var seen = map[string]bool{}
	var out []string
	var c SealedColumn
	for _, c = range SealedColumns() {
		if !seen[c.Table] {
			seen[c.Table] = true
			out = append(out, c.Table)
		}
	}
	sort.Strings(out)
	return out
}

// --- Active key fingerprint ---

// CredentialsKeyName is the name under which the vault key is stored in instance_secrets
// when not provided by the environment.
const CredentialsKeyName = "credentials-key"

// CredentialsKeyFingerprint reads the stored fingerprint. Returns (nil, nil) when none
// yet: a pre-migration-0024 instance never wrote one, and that is not an error.
func (s *Store) CredentialsKeyFingerprint(ctx context.Context) ([]byte, error) {
	var fp []byte
	var algo string
	var err = s.pool.QueryRow(ctx,
		`select fingerprint, algo from credentials_key_fingerprint where one`).Scan(&fp, &algo)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// A fingerprint computed by another derivation cannot be compared to ours: treating it
	// as "different" would refuse to start a perfectly healthy instance. Say so rather than guess.
	if algo != secretbox.FingerprintAlgo {
		return nil, fmt.Errorf("key fingerprint computed by unknown derivation (%q): "+
			"this database was written by a newer version of meshcraft", algo)
	}
	return fp, nil
}

// SetCredentialsKeyFingerprint stores (or replaces) the fingerprint.
func (s *Store) SetCredentialsKeyFingerprint(ctx context.Context, fingerprint []byte) error {
	var _, err = s.pool.Exec(ctx, fingerprintUpsertSQL,
		fingerprint, secretbox.FingerprintAlgo)
	return err
}

const fingerprintUpsertSQL = `insert into credentials_key_fingerprint (one, fingerprint, algo, updated_at)
	 values (true, $1, $2, now())
	 on conflict (one) do update set fingerprint = $1, algo = $2, updated_at = now()`

// --- Key stored in database ---

// InstanceSecretValue reads an instance secret WITHOUT creating it, and returns ErrNotFound
// if missing.
//
// The distinction from InstanceSecret is the guard itself. InstanceSecret creates what is
// missing, needed on first startup — but after rotation to the environment, the old row was
// deleted: a restart without the variable, with InstanceSecret, would draw a NEW key and
// write it to the database. Startup refusal would catch it, but after leaving a dummy key in
// instance_secrets that the next reader would take for the real one. Not creating is safer
// than repairing.
func (s *Store) InstanceSecretValue(ctx context.Context, name string) ([]byte, error) {
	var value []byte
	var err = s.pool.QueryRow(ctx,
		`select value from instance_secrets where name = $1`, name).Scan(&value)
	if err != nil {
		return nil, notFound(err)
	}
	return value, nil
}

// PutInstanceSecret writes an instance secret if not present yet. `do nothing` keeps
// InstanceSecret's concurrency idempotence: two simultaneous startups do not produce two keys.
func (s *Store) PutInstanceSecret(ctx context.Context, name string, value []byte) error {
	var _, err = s.pool.Exec(ctx,
		`insert into instance_secrets (name, value) values ($1, $2)
		 on conflict (name) do nothing`, name, value)
	return err
}

// --- Probe: does the active key open the vault? ---

// SealedSample returns a few sealed values from inventory columns.
//
// Purpose: when the database has NO fingerprint yet, comparison is impossible and another
// decision is needed. A new instance has nothing to lose; a live one does. The only reliable
// judge is the vault itself: if the active key opens what is already sealed, it is the right one.
func (s *Store) SealedSample(ctx context.Context, perColumn int) ([][]byte, error) {
	var out [][]byte
	var col SealedColumn
	for _, col = range SealedColumns() {
		var rows, err = s.pool.Query(ctx, fmt.Sprintf(
			`select %s from %s where %s is not null and octet_length(%s) > 0 limit %d`,
			col.Column, col.Table, col.Column, col.Column, perColumn))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v []byte
			if err = rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, v)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CountSealed counts sealed values, column by column. For the operator: re-encryption count
// must compare to an inventory taken BEFORE the operation.
func (s *Store) CountSealed(ctx context.Context) ([]RekeyCount, error) {
	var out []RekeyCount
	var col SealedColumn
	for _, col = range SealedColumns() {
		var n int
		var err = s.pool.QueryRow(ctx, fmt.Sprintf(
			`select count(*) from %s where %s is not null and octet_length(%s) > 0`,
			col.Table, col.Column, col.Column)).Scan(&n)
		if err != nil {
			return nil, err
		}
		out = append(out, RekeyCount{Table: col.Table, Column: col.Column, Rows: n})
	}
	return out, nil
}

// OtherConnections counts connections to THIS database other than ours.
//
// Re-encryption guard: a controller left running keeps the OLD key in memory. It would not
// break the transaction — locks handle that — but after commit it would keep sealing with a
// key the database no longer recognizes, and every secret written in between would be lost.
// The controller's pgx pool keeps connections open even at rest: counting them is a simple
// reliable way to notice we forgot to stop it.
func (s *Store) OtherConnections(ctx context.Context) (int, error) {
	var n int
	var err = s.pool.QueryRow(ctx,
		`select count(*) from pg_stat_activity
		 where datname = current_database() and pid <> pg_backend_pid()`).Scan(&n)
	return n, err
}

// --- Re-encryption ---

// RekeyCount is one column's count. The operator must compare line by line with the
// inventory taken before the operation: re-encryption announcing "done" without saying HOW
// MANY is not verifiable.
type RekeyCount struct {
	Table  string
	Column string
	// Rows: non-empty values found in the column.
	Rows int
	// Resealed: values read with the old key and re-sealed with the new one.
	Resealed int
	// Already: values that ALREADY opened with the new key, therefore left unchanged. This
	// makes the operation safely replayable.
	Already int
}

// RekeyReport is the complete report of a re-encryption.
type RekeyReport struct {
	Columns []RekeyCount
	// Verified: values re-read FROM THE DATABASE and decrypted with the new key before commit.
	// Must equal the sum of Rows.
	Verified int
	// OldKeyDeleted: was the instance_secrets/credentials-key row deleted. A key no longer
	// needed that stays in the database is just a leak waiting to happen.
	OldKeyDeleted bool
	// NoOp: nothing to re-seal, the database was already entirely on the new key.
	NoOp bool
}

// Total sums the columns.
func (r RekeyReport) Total() (rows, resealed, already int) {
	var c RekeyCount
	for _, c = range r.Columns {
		rows += c.Rows
		resealed += c.Resealed
		already += c.Already
	}
	return rows, resealed, already
}

// ErrUndecryptable: a value opens with NEITHER the old key NOR the new one. The transaction
// is rolled back: we do not rotate a database whose part we cannot read, otherwise rotation
// would sign permanent loss of what we failed to read.
var ErrUndecryptable = errors.New("sealed value unreadable with both keys")

// RekeyOptions configures operation details.
type RekeyOptions struct {
	// LockTimeout bounds table lock wait. Without a bound, re-encryption started while
	// another write drags would hang silently.
	LockTimeout time.Duration

	// AfterRow is a TEST hook called after each write. It exists because atomicity is this
	// file's most important property, and a property you cannot fail is untested: returning an
	// error here simulates a mid-operation cut, and the test verifies NO row moved. Nil in
	// production.
	AfterRow func(n int) error
}

// RekeyCredentials re-seals the entire vault: read with `oldKey`, write with `newKey`, IN
// ONE TRANSACTION.
//
// Flow, and the reason for each step:
//
//  1. Lock sealed tables in ACCESS EXCLUSIVE. A concurrent insert during the operation would
//     be sealed with the old key and miss the rotation — a lost secret, invisible until needed.
//  2. Read each non-empty value, open with old key. A value that already opens with the NEW
//     key is counted separately and left alone: that makes a second pass harmless. A value
//     opening with neither aborts everything.
//  3. Re-seal and write.
//  4. RE-READ FROM THE DATABASE and decrypt with the new key, before commit. Re-encryption
//     declaring success without re-read is exactly what this project refuses elsewhere
//     (instanton connector pushes always re-read what they wrote). Here the stakes are even
//     higher: the error does not fix itself on the next try.
//  5. Update fingerprint, then delete old key.
//  6. Commit — one, all or nothing.
//
// Rollback is the DEFAULT behavior: `defer tx.Rollback` runs on all error paths, and a
// transaction never committed leaves nothing behind. That guarantees a power cut at any moment
// leaves the vault entirely on the OLD key — a fully functional state from which the operation
// can be retried. Keys are passed as RAW BYTES and crypto services built here: this is the
// only function in the program that must hold both keys at once, visible in its signature.
func (s *Store) RekeyCredentials(ctx context.Context, oldRaw, newRaw []byte,
	opts RekeyOptions) (RekeyReport, error) {

	var report RekeyReport
	var oldKey, err = secretbox.New(oldRaw, false)
	if err != nil {
		return report, fmt.Errorf("old key: %w", err)
	}
	var newKey *secretbox.Service
	if newKey, err = secretbox.New(newRaw, true); err != nil {
		return report, fmt.Errorf("new key: %w", err)
	}

	var tx pgx.Tx
	if tx, err = s.pool.Begin(ctx); err != nil {
		return report, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if opts.LockTimeout > 0 {
		if _, err = tx.Exec(ctx, fmt.Sprintf(`set local lock_timeout = '%dms'`,
			opts.LockTimeout.Milliseconds())); err != nil {
			return report, err
		}
	}
	var t string
	for _, t = range sealedTables() {
		if _, err = tx.Exec(ctx, `lock table `+t+` in access exclusive mode`); err != nil {
			return report, fmt.Errorf("locking %s: %w "+
				"(concurrent write in progress — is the controller stopped?)", t, err)
		}
	}

	// readPlain remembers what we READ, by column then row id. Final verification compares
	// re-read to this: comparing to ciphertext we just built proves nothing, only plaintext counts.
	var readPlain = map[string]map[string]string{}
	var written = 0
	var col SealedColumn
	for _, col = range SealedColumns() {
		var key = col.Table + "." + col.Column
		readPlain[key] = map[string]string{}
		var count = RekeyCount{Table: col.Table, Column: col.Column}

		// `for update` on top of table lock: belt and suspenders cost nothing on ten rows,
		// and the day table lock is relaxed the read rows stay protected.
		var rows pgx.Rows
		rows, err = tx.Query(ctx, fmt.Sprintf(
			`select id::text, %s from %s
			 where %s is not null and octet_length(%s) > 0
			 order by id for update`,
			col.Column, col.Table, col.Column, col.Column))
		if err != nil {
			return report, err
		}
		// Everything fetched BEFORE writing: writing while a cursor on the same table is open
		// in the same transaction is a classic trap.
		type sealedRow struct {
			id  string
			enc []byte
		}
		var batch []sealedRow
		for rows.Next() {
			var r sealedRow
			if err = rows.Scan(&r.id, &r.enc); err != nil {
				rows.Close()
				return report, err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return report, err
		}

		var r sealedRow
		for _, r = range batch {
			count.Rows++
			var plain string
			var openErr error
			plain, openErr = oldKey.Open(r.enc)
			if openErr != nil {
				// Already on the new key? Then this is a second pass, and it must break nothing.
				var alt string
				var altErr error
				alt, altErr = newKey.Open(r.enc)
				if altErr != nil {
					return report, fmt.Errorf("%w : %s id=%s", ErrUndecryptable, key, r.id)
				}
				count.Already++
				readPlain[key][r.id] = alt
				continue
			}
			readPlain[key][r.id] = plain

			var fresh []byte
			if fresh, err = newKey.Seal(plain); err != nil {
				return report, err
			}
			if _, err = tx.Exec(ctx, fmt.Sprintf(
				`update %s set %s = $2 where id = $1::uuid`, col.Table, col.Column),
				r.id, fresh); err != nil {
				return report, err
			}
			count.Resealed++
			written++
			if opts.AfterRow != nil {
				if err = opts.AfterRow(written); err != nil {
					return report, err
				}
			}
		}
		report.Columns = append(report.Columns, count)
	}

	// --- Verification: re-read the DATABASE, not our memory ---
	for _, col = range SealedColumns() {
		var key = col.Table + "." + col.Column
		if len(readPlain[key]) == 0 {
			continue
		}
		var rows pgx.Rows
		rows, err = tx.Query(ctx, fmt.Sprintf(
			`select id::text, %s from %s
			 where %s is not null and octet_length(%s) > 0`,
			col.Column, col.Table, col.Column, col.Column))
		if err != nil {
			return report, err
		}
		var seen = map[string]bool{}
		for rows.Next() {
			var id string
			var enc []byte
			if err = rows.Scan(&id, &enc); err != nil {
				rows.Close()
				return report, err
			}
			var want, known = readPlain[key][id]
			if !known {
				// A row appearing mid-flight: impossible under the lock, so if it happens the
				// lock did not hold. Refuse.
				rows.Close()
				return report, fmt.Errorf("verify %s: unexpected row id=%s (concurrent write?)", key, id)
			}
			var got string
			if got, err = newKey.Open(enc); err != nil {
				rows.Close()
				return report, fmt.Errorf("verify %s id=%s: unreadable with new key: %w", key, id, err)
			}
			if got != want {
				rows.Close()
				return report, fmt.Errorf("verify %s id=%s: re-read plaintext differs from read plaintext", key, id)
			}
			seen[id] = true
			report.Verified++
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return report, err
		}
		var id string
		for id = range readPlain[key] {
			if !seen[id] {
				return report, fmt.Errorf("verify %s: row id=%s disappeared", key, id)
			}
		}
	}

	var rowsTotal, resealed, _ = report.Total()
	report.NoOp = resealed == 0
	if report.Verified != rowsTotal {
		return report, fmt.Errorf("incomplete verification: %d re-read for %d values",
			report.Verified, rowsTotal)
	}

	// --- Fingerprint, then remove old key ---
	//
	// In the SAME transaction, and that is essential: fingerprint updated without re-sealed
	// secrets (or the reverse) would refuse startup on a healthy database, or allow startup on
	// an unreadable one.
	if _, err = tx.Exec(ctx, fingerprintUpsertSQL,
		secretbox.Fingerprint(newRaw), secretbox.FingerprintAlgo); err != nil {
		return report, err
	}
	// Old key goes away: it serves only to leak. The only reader of this row is controller
	// initialization (initSecrets / keyguard), which reads it only for lack of MESHCRAFT_SECRET_KEY.
	var cmd, delErr = tx.Exec(ctx,
		`delete from instance_secrets where name = $1`, CredentialsKeyName)
	if delErr != nil {
		return report, delErr
	}
	report.OldKeyDeleted = cmd.RowsAffected() > 0

	if err = tx.Commit(ctx); err != nil {
		return report, err
	}
	return report, nil
}
