package main

// The controller's `-rekey` mode: switches the vault key from the database
// to the environment, rekeying everything that is sealed.
//
// WHY A BINARY MODE, AND NOT SOMETHING ELSE. Three forms were possible, and
// the choice is not cosmetic:
//
//   - An API route would be the WORST choice. It would permanently leave, on
//     an exposed product, an ingress point capable of rewriting the entire
//     vault in a single request. The switchover is an act that happens once
//     in an instance's lifetime; dedicating a permanent surface to it means
//     paying a permanent risk for a one-time use. It also could not
//     guarantee the thing that matters most here: that nobody else writes
//     during the operation, since it would precisely be served by the
//     process that writes.
//
//   - One more binary (cmd/rekey) would require touching the Dockerfile,
//     distributing and versioning an extra tool, and above all DUPLICATING
//     the key logic. The day the two copies diverge, the switchover tool
//     switches over something other than what the controller reads.
//
//   - A controller mode, the one chosen: the key logic is already here
//     (keyguard.go), there is nothing to distribute, the existing image
//     already knows how to do it (`docker compose run --rm controller
//     -rekey`), and the process doing the rekeying is exactly the one that
//     will verify the fingerprint on the next startup. The mode does NOT
//     start the HTTP server: it does its job, prints its report, and exits.
//
// The output is deliberately verbose and figure-heavy: an operator must be
// able to compare it line by line against the inventory of sealed columns.
// "Done" with no count is not verifiable.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"meshcraft/internal/config"
	"meshcraft/internal/secretbox"
	"meshcraft/internal/store"
)

// runRekey performs the switchover. Returns an error; the caller exits with
// code 1.
//
// force: bypasses the "other connections to the database are open"
// safeguard. It exists because that safeguard can be wrong (a pgbouncer, a
// psql observation session), but its default is to refuse: the fault it
// catches — a controller left running, which keeps the old key in memory
// and will keep sealing with it after the commit — costs lost secrets.
func runRekey(ctx context.Context, cfg config.Config, st *store.Store, force bool) error {
	fmt.Println("=== meshcraft — rekeying the credentials vault ===")
	fmt.Println()

	// The NEW key comes from the environment. Refuse early and clearly:
	// running the rekey process without knowing what we're switching to
	// makes no sense.
	if cfg.SecretKey == "" {
		return errors.New("MESHCRAFT_SECRET_KEY is empty.\n\n" +
			"The rekey process switches the vault FROM the key stored in the database\n" +
			"TO a key supplied by the environment: the new key must therefore be in\n" +
			"MESHCRAFT_SECRET_KEY (64 hexadecimal characters).\n\n" +
			"    openssl rand -hex 32\n\n" +
			"WRITE IT DOWN BEFORE STARTING THE OPERATION, in your secrets vault. After\n" +
			"the switchover, it is the ONLY way to read this database's secrets: the\n" +
			"rekey process removes the old key, and no copy remains server-side.")
	}
	var newKey, err = secretbox.ParseHexKey(cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("MESHCRAFT_SECRET_KEY: %w", err)
	}

	// The OLD key is read from the database, and never created: if it is no
	// longer there, it means either a switchover already happened, or a
	// database that can no longer be read. Both are handled, but not the
	// same way.
	var oldKey []byte
	oldKey, err = st.InstanceSecretValue(ctx, store.CredentialsKeyName)
	if errors.Is(err, store.ErrNotFound) {
		return alreadySwitched(ctx, st, newKey)
	}
	if err != nil {
		return err
	}

	if secretbox.SameFingerprint(secretbox.Fingerprint(oldKey), secretbox.Fingerprint(newKey)) {
		return errors.New("MESHCRAFT_SECRET_KEY ALREADY contains the key stored in the database.\n\n" +
			"Rekeying would achieve nothing: the key would be the same on both sides, and\n" +
			"taking it out of the database without changing it would leave a copy of the\n" +
			"production key in existing backups.\n\n" +
			"Generate a FRESH key (openssl rand -hex 32) and try again.")
	}

	// Safeguard: a controller left running keeps the old key in memory. The
	// operation itself would remain atomic (the locks take care of that),
	// but any secret saved AFTER the commit by that other process would be
	// sealed with a key the database no longer recognizes.
	var others int
	if others, err = st.OtherConnections(ctx); err != nil {
		return err
	}
	if others > 0 && !force {
		return fmt.Errorf(`%d other connection(s) are open on this database.

The controller is probably still in service. It keeps the OLD key in
memory: after the rekey process, every secret it saves would be sealed with
a key the database no longer recognizes — and lost.

    docker compose stop controller
    docker compose run --rm controller -rekey

If you know these connections are harmless (an observation psql session, a
pgbouncer), rerun with -rekey-force.`, others)
	}
	if others > 0 {
		fmt.Printf("WARNING: %d other connection(s) on the database, ignored (-rekey-force).\n\n", others)
	}

	// Inventory BEFORE. This is what the operator compares against the
	// report: a total that matches against an inventory taken afterward
	// proves nothing.
	var before []store.RekeyCount
	if before, err = st.CountSealed(ctx); err != nil {
		return err
	}
	fmt.Println("Inventory BEFORE (non-empty sealed values):")
	printCounts(before, false)
	fmt.Println()
	fmt.Printf("Old key (database)      : fingerprint %s\n", secretbox.KeyShortID(oldKey))
	fmt.Printf("New key (environment)    : fingerprint %s\n", secretbox.KeyShortID(newKey))
	fmt.Println()

	var report store.RekeyReport
	report, err = st.RekeyCredentials(ctx, oldKey, newKey, store.RekeyOptions{
		// 30s: enough to let a lingering normal write go through, too little
		// to hang without explanation if the controller is still writing.
		LockTimeout: 30 * time.Second,
	})
	if err != nil {
		// The partial report is printed anyway: knowing where it stopped
		// helps, even if nothing was written.
		if len(report.Columns) > 0 {
			fmt.Println("Progress at the time of failure (NOTHING WAS WRITTEN):")
			printCounts(report.Columns, true)
			fmt.Println()
		}
		return fmt.Errorf(`rekeying failed: %w

THE TRANSACTION WAS ROLLED BACK: the database is in exactly the state it was
in before the operation, fully readable with the old key. The instance
restarts without MESHCRAFT_SECRET_KEY, as before.`, err)
	}

	fmt.Println("Report (transaction committed):")
	printCounts(report.Columns, true)
	var rows, resealed, already = report.Total()
	fmt.Println()
	fmt.Printf("  total            : %d sealed value(s)\n", rows)
	fmt.Printf("  resealed         : %d\n", resealed)
	fmt.Printf("  already current  : %d\n", already)
	fmt.Printf("  REREAD AND DECRYPTED with the new key before commit: %d\n", report.Verified)
	if report.OldKeyDeleted {
		fmt.Println("  old key        : DELETED from instance_secrets " +
			"(a key that has become useless is nothing but a leak waiting to happen)")
	}
	fmt.Printf("  fingerprint recorded: %s\n", secretbox.KeyShortID(newKey))
	fmt.Println()

	// The before/after comparison is done HERE, not left to the operator's
	// eye: a total that doesn't match must be visible on its own.
	var mismatch = compareInventories(before, report.Columns)
	if mismatch != "" {
		fmt.Println("COUNT ANOMALY: " + mismatch)
		fmt.Println("The transaction is committed and verified; report this discrepancy anyway.")
		fmt.Println()
	}

	fmt.Println("Done. MESHCRAFT_SECRET_KEY is now REQUIRED at startup:")
	fmt.Println("it is the only way to read this vault. Make sure it is in the instance's")
	fmt.Println(".env AND in your secrets vault, then restart:")
	fmt.Println()
	fmt.Println("    docker compose up -d controller")
	return nil
}

// alreadySwitched handles the "no more key in the database" case.
//
// Replaying the switchover must be safe: that is the property that allows
// rerunning it calmly after an interruption. If the recorded fingerprint
// matches the supplied key, there is nothing to do and we say so.
// Otherwise, there is no READ key available and rekeying is impossible —
// refusing and saying so is a thousand times better than attempting
// anything.
func alreadySwitched(ctx context.Context, st *store.Store, newKey []byte) error {
	var stored, err = st.CredentialsKeyFingerprint(ctx)
	if err != nil {
		return err
	}
	if stored != nil && secretbox.SameFingerprint(stored, secretbox.Fingerprint(newKey)) {
		fmt.Println("Nothing to do: this database is ALREADY switched over to the supplied key.")
		fmt.Printf("  fingerprint in database = fingerprint of MESHCRAFT_SECRET_KEY = %s\n",
			secretbox.KeyShortID(newKey))
		fmt.Println("  no key remains in instance_secrets.")
		fmt.Println()
		var counts []store.RekeyCount
		if counts, err = st.CountSealed(ctx); err != nil {
			return err
		}
		fmt.Println("Inventory (sealed values, readable with this key):")
		printCounts(counts, false)
		return nil
	}
	var what = "  no fingerprint is recorded in this database."
	if stored != nil {
		what = fmt.Sprintf("  fingerprint recorded in database : %s\n"+
			"  fingerprint of MESHCRAFT_SECRET_KEY : %s",
			secretbox.ShortID(stored), secretbox.KeyShortID(newKey))
	}
	return fmt.Errorf(`no key to read with.

The instance_secrets table no longer contains a "%s" row, and:

%s

Rekeying needs TWO keys: one to read with and one to write with. With no
read key, there is nothing to rekey — and above all nothing to attempt:
writing anything here would turn an unreadable vault into a destroyed one.

WHAT TO DO: find the key with which these secrets were sealed and put it in
MESHCRAFT_SECRET_KEY — a normal controller startup will then be enough,
with no rekeying needed. If it is lost, restore a backup from before the
switchover.`, store.CredentialsKeyName, what)
}

// printCounts prints the inventory, one line per sealed column.
func printCounts(counts []store.RekeyCount, detail bool) {
	var c store.RekeyCount
	for _, c = range counts {
		if detail {
			fmt.Printf("  %-24s %-12s  %3d found  %3d resealed  %3d already current\n",
				c.Table, c.Column, c.Rows, c.Resealed, c.Already)
			continue
		}
		fmt.Printf("  %-24s %-12s  %3d\n", c.Table, c.Column, c.Rows)
	}
}

// compareInventories checks the before count against the report. Returns a
// description of the discrepancy, empty if everything matches.
func compareInventories(before, after []store.RekeyCount) string {
	if len(before) != len(after) {
		return "the before inventory and the report do not cover the same " +
			"number of columns"
	}
	var i int
	for i = range before {
		if before[i].Table != after[i].Table || before[i].Column != after[i].Column {
			return "the columns in the inventory and in the report are not the same"
		}
		if before[i].Rows != after[i].Rows {
			return fmt.Sprintf("%s.%s: %d value(s) before, %d processed",
				before[i].Table, before[i].Column, before[i].Rows, after[i].Rows)
		}
		if before[i].Rows != after[i].Resealed+after[i].Already {
			return fmt.Sprintf("%s.%s: %d value(s) but %d resealed + %d already current",
				before[i].Table, before[i].Column, before[i].Rows,
				after[i].Resealed, after[i].Already)
		}
	}
	return ""
}

// exitRekey wraps runRekey for main: prints the error to stderr as-is (the
// messages are multi-line and written to be read by a human, not indexed by
// a log aggregator) and exits.
func exitRekey(ctx context.Context, cfg config.Config, st *store.Store, force bool) {
	var err = runRekey(ctx, cfg, st, force)
	if err != nil {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "FAILURE: "+err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}
