package main

// The vault key guard: decide WHICH key is in use, and refuse to start
// when it isn't the right one.
//
// THE TRAP THIS DISARMS. The controller used to pick up MESHCRAFT_SECRET_KEY
// as soon as it was set and never looked at the key stored in the database
// again. On a brand-new instance, that's fine. On an instance ALREADY IN
// SERVICE, setting that variable made the whole vault unreadable — silently,
// which is the real problem: the controller would start, serve its pages,
// answer its healthcheck, and the product looked like it was working. What
// stopped working wasn't visible right away:
//
//   - SNMP monitoring vanished without a single message, because
//     snmpWatchesFor (internal/api/watch_handlers.go) silently drops any
//     device whose community string fails to decrypt;
//   - fleet administration passwords became inaccessible, including the ones
//     meshcraft itself had applied to the devices — meaning, on a product
//     line with no serial port, a factory reset;
//   - the generation history, the only way to reach a device left behind,
//     went down with it.
//
// And discovery would happen one site at a time, days later, often after
// backups still containing the old key had already run.
//
// THE REASONING. The controller stores in the database a one-way FINGERPRINT
// of the key that sealed it (never the key itself, never anything that could
// recover it — see secretbox.Fingerprint), then compares it on every startup:
//
//   - equal → nothing to say, start up;
//   - different → REFUSE to start, naming both the cause and the fix.
//     Starting would be WORSE than refusing: a refusal costs a few minutes of
//     noisy downtime, whereas starting costs monitoring, fleet access, and
//     the contents of the vault, discovered one site at a time;
//   - missing (instance predates migration 0024) → record it, quietly... but
//     only after VERIFYING that the active key does open what is already
//     sealed. Recording the fingerprint without that check would replay
//     exactly the original trap: someone who updates the code AND sets the
//     variable in the same move would get the fingerprint of the wrong key
//     etched in stone, and the guard would bless the disaster.
//
// THE REVERSE CASE, just as dangerous, that this same comparison catches: an
// instance switched over to the environment and then restarted WITHOUT the
// variable would draw a fresh key and reseal on top of everything. Here we go
// further than just refusing — we also do NOT draw a fresh key (see
// InstanceSecretValue): leaving a bogus key in instance_secrets means letting
// the next reader mistake it for the real one.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"meshcraft/internal/config"
	"meshcraft/internal/secretbox"
	"meshcraft/internal/store"
)

// keyStore is the strict minimum the guard asks of the database.
//
// The interface exists for a precise reason: the decision to REFUSE is the
// part that absolutely must be tested, and testing it against a real
// PostgreSQL would produce a test nobody runs. With it, the four refusal
// paths are verified in memory.
type keyStore interface {
	CredentialsKeyFingerprint(ctx context.Context) ([]byte, error)
	SetCredentialsKeyFingerprint(ctx context.Context, fingerprint []byte) error
	InstanceSecretValue(ctx context.Context, name string) ([]byte, error)
	PutInstanceSecret(ctx context.Context, name string, value []byte) error
	SealedSample(ctx context.Context, perColumn int) ([][]byte, error)
}

// ErrKeyMismatch: the key presented is not the one that sealed this database.
// It is a refusal to start, not a warning.
var ErrKeyMismatch = errors.New("encryption key incompatible with this database")

// sampleSize: how many sealed values to probe per column when the database
// does not yet carry a fingerprint. Three is enough — the point is to know
// whether the key opens the vault, not to validate it row by row (that is the
// job of the rekey process, which rereads everything).
const sampleSize = 3

// resolveVaultKey picks the vault key and verifies it is the right one.
//
// Returns the key (32 bytes) and whether it came from the environment. Any
// error returned here must fail startup: that is the whole point of the
// guard.
func resolveVaultKey(ctx context.Context, ks keyStore, envKey string) ([]byte, bool, error) {
	var stored, err = ks.CredentialsKeyFingerprint(ctx)
	if err != nil {
		return nil, false, err
	}

	var key []byte
	var fromEnv bool
	if envKey != "" {
		if key, err = secretbox.ParseHexKey(envKey); err != nil {
			return nil, false, err
		}
		fromEnv = true
	} else {
		key, err = resolveKeyFromDB(ctx, ks, stored != nil)
		if err != nil {
			return nil, false, err
		}
	}

	var fingerprint = secretbox.Fingerprint(key)

	if stored != nil {
		if secretbox.SameFingerprint(stored, fingerprint) {
			return key, fromEnv, nil
		}
		return nil, false, mismatchError(ctx, ks, stored, fingerprint, fromEnv)
	}

	// No fingerprint on record: there is nothing to compare against, so we
	// interrogate the vault itself. It is the only reliable judge — if the
	// active key opens what is already sealed, it is indeed the key that
	// sealed it.
	if err = probeVault(ctx, ks, key, fromEnv); err != nil {
		return nil, false, err
	}
	if err = ks.SetCredentialsKeyFingerprint(ctx, fingerprint); err != nil {
		return nil, false, err
	}
	slog.Info("vault key fingerprint recorded: starting up with a different "+
		"key will now be refused instead of making the secrets unreadable",
		"key_id", secretbox.ShortID(fingerprint), "key_external", fromEnv)
	return key, fromEnv, nil
}

// resolveKeyFromDB reads the key stored in the database, and only CREATES one
// if its absence is legitimate: the first startup of a virgin database.
//
// `hasFingerprint` says that this database has already been sealed by a
// known key. If the row is missing while a fingerprint exists, the
// conclusion is clear: the switchover to the environment happened (the
// rekey process removes the key) and we just rebooted without the variable.
// Drawing a fresh key here would be the reverse scenario of the original
// trap, with the same result.
func resolveKeyFromDB(ctx context.Context, ks keyStore, hasFingerprint bool) ([]byte, error) {
	var key, err = ks.InstanceSecretValue(ctx, store.CredentialsKeyName)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	if hasFingerprint {
		return nil, fmt.Errorf(`%w

MESHCRAFT_SECRET_KEY is not set, and there is no longer a key in the database.

This instance has been switched over to a key supplied by the environment:
the rekey process removed the old key from the instance_secrets table,
because a key that has become useless is nothing but a secret waiting to
leak.

The controller REFUSES to start rather than draw a fresh key: that fresh key
would decrypt nothing, and the first secret saved afterward would reseal on
top of the fingerprint — the database would become an irreparable mix.

WHAT TO DO: set MESHCRAFT_SECRET_KEY (64 hexadecimal characters) back to the
key used during the switchover, then restart. It is in the instance's .env
file, and in the vault wherever you stored it. Without it, only restoring a
backup from before the switchover recovers the secrets.`, ErrKeyMismatch)
	}

	// Neither fingerprint nor key. A virgin database? We verify before
	// assuming so: a non-empty vault with no key to open it is not a first
	// startup, and drawing a random key would condemn it.
	var sample [][]byte
	if sample, err = ks.SealedSample(ctx, 1); err != nil {
		return nil, err
	}
	if len(sample) > 0 {
		return nil, fmt.Errorf(`%w

MESHCRAFT_SECRET_KEY is not set, the instance_secrets table contains no key,
and yet the vault contains sealed secrets.

No key in this database can therefore open them. Drawing a fresh key would
condemn them permanently, so the controller refuses to start.

WHAT TO DO: set MESHCRAFT_SECRET_KEY to the key that sealed these values, or
restore a backup that contains the instance_secrets/credentials-key row.`, ErrKeyMismatch)
	}

	// A genuine first startup: draw the key and store it. Rereading it after
	// the write is not a mere formality — `on conflict do nothing` means that
	// two simultaneous startups will write only one, and that is the ONE
	// that must be used.
	var fresh []byte
	if fresh, err = secretbox.NewRandomKey(); err != nil {
		return nil, err
	}
	if err = ks.PutInstanceSecret(ctx, store.CredentialsKeyName, fresh); err != nil {
		return nil, err
	}
	return ks.InstanceSecretValue(ctx, store.CredentialsKeyName)
}

// probeVault verifies that `key` opens at least one already-sealed value.
//
// An empty vault passes: there is nothing to contradict. A non-empty vault
// where nothing opens is the original trap caught red-handed — typically a
// code update and a MESHCRAFT_SECRET_KEY assignment happening in the same
// move, on an instance already in service.
func probeVault(ctx context.Context, ks keyStore, key []byte, fromEnv bool) error {
	var sample, err = ks.SealedSample(ctx, sampleSize)
	if err != nil {
		return err
	}
	if len(sample) == 0 {
		return nil
	}
	var box *secretbox.Service
	if box, err = secretbox.New(key, fromEnv); err != nil {
		return err
	}
	var enc []byte
	var probeErr error
	for _, enc = range sample {
		_, probeErr = box.Open(enc)
		if probeErr == nil {
			return nil
		}
	}
	return fmt.Errorf(`%w

The secrets in this database were sealed with a DIFFERENT key than the one
supplied. None of the %d probed values open with the active key (fingerprint
%s).

This database did not yet have a key fingerprint on record, so the
controller queried the vault itself — and the vault says no.

WHAT LIKELY HAPPENED: MESHCRAFT_SECRET_KEY was just set on an existing
instance whose secrets were sealed with the key stored in the database
(instance_secrets/credentials-key). Starting up this way would cause far more
damage than refusing: the product would APPEAR to work, then lose SNMP
monitoring (devices whose community string fails to decrypt are dropped
without a message), fleet administration passwords, and the entire contents
of the vault — one site at a time, over several days.

WHAT TO DO, one of the following two:

 1. COMMIT TO THE SWITCHOVER — rekey the vault with the new key. Back up the
    database first, stop the controller, then run the rekey process with the
    NEW key in the environment:

        docker compose stop controller
        docker compose run --rm controller -rekey

    The rekey process reads each value with the old key (the one in the
    database), reseals it with the new one, rereads everything to verify,
    and writes only as a single transaction: an interruption partway through
    leaves nothing half-done.

 2. ROLL BACK — remove MESHCRAFT_SECRET_KEY from the environment and reboot.
    The instance goes back to using the key in the database, exactly as
    before, and nothing has been lost: the controller refused to start
    BEFORE writing anything at all.`,
		ErrKeyMismatch, len(sample), secretbox.KeyShortID(key))
}

// mismatchError builds the refusal when a fingerprint EXISTS and differs.
//
// The message states which direction this happened in, because the fix is
// not the same: if the key is still in the database, switching over is
// still possible; otherwise, the key needs to be recovered.
func mismatchError(ctx context.Context, ks keyStore, stored, active []byte, fromEnv bool) error {
	var keyInDB = true
	var _, err = ks.InstanceSecretValue(ctx, store.CredentialsKeyName)
	if errors.Is(err, store.ErrNotFound) {
		keyInDB = false
	}

	var source = "the key stored in the database (instance_secrets/credentials-key)"
	if fromEnv {
		source = "MESHCRAFT_SECRET_KEY"
	}

	var remedy string
	switch {
	case fromEnv && keyInDB:
		remedy = `The old key is STILL in the database: switching over is therefore still
possible with no risk of loss. Back up the database, stop the controller,
then rekey the vault with the new key in the environment:

    docker compose stop controller
    docker compose run --rm controller -rekey

Or, to get back into service immediately: remove MESHCRAFT_SECRET_KEY and
restart. Nothing will have been lost.`
	case fromEnv:
		remedy = `There is no longer a key in the database: this instance has already been
switched over, and the value of MESHCRAFT_SECRET_KEY is not the one that was
used.

Restore the switchover key (the one noted at the time of the rekey) and
restart. If it is permanently lost, only restoring a backup from before the
switchover recovers the secrets — the rekey process is impossible without a
readable key.`
	default:
		remedy = `MESHCRAFT_SECRET_KEY is not set: the controller started up using the key in
the database, while the vault was sealed with a different one — typically a
switchover to the environment, followed by a reboot without the variable.

Set MESHCRAFT_SECRET_KEY back to the switchover key and restart.`
	}

	return fmt.Errorf(`%w

Fingerprint recorded in the database: %s
Fingerprint of the active key       : %s   (source: %s)

The secrets in this database were sealed with a different key than the one
the controller just received. It REFUSES to start.

This refusal is deliberate, and is the opposite of a regression: starting up
would leave the product apparently working while losing, one site at a
time, SNMP monitoring (devices whose community string fails to decrypt are
dropped without a message), fleet administration passwords, and the contents
of the vault. Worse still, the first secret saved afterward would be sealed
with the new key: the database would become a mix of the two that no rekey
process can ever untangle.

%s`,
		ErrKeyMismatch, secretbox.ShortID(stored), secretbox.ShortID(active), source, remedy)
}

// initSecrets prepares encryption for the vault's credentials.
//
// MESHCRAFT_SECRET_KEY (64 hexadecimal characters) keeps the key OUT of the
// database: that is the preferred mode. Without it, a key is drawn on first
// startup and stored in the database — the controller says so clearly
// rather than letting anyone believe in a protection it doesn't offer.
//
// The choice of key and the refusal to start on the wrong one are handled
// in resolveVaultKey above, with the full reasoning.
func initSecrets(ctx context.Context, cfg config.Config, st *store.Store) (*secretbox.Service, error) {
	var key, fromEnv, err = resolveVaultKey(ctx, st, cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	if !fromEnv {
		slog.Warn("MESHCRAFT_SECRET_KEY not set: the encryption key for the " +
			"credentials is stored in the database. A leak of the dump would " +
			"expose the secrets. Set MESHCRAFT_SECRET_KEY (64 hex characters) in " +
			"production, then rekey the vault with \"controller -rekey\" — " +
			"setting the variable ALONE switches nothing over and will be " +
			"refused at startup.")
	}
	return secretbox.New(key, fromEnv)
}
