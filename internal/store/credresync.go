package store

// RE-SYNCHRONISING THE CREDENTIAL ROWS THAT ARE COPIES OF ONE ACCOUNT.
//
// # The defect, observed end to end on 2026-08-10
//
// A `gnmi` credential is its own `site_credentials` row holding its OWN COPY of a
// password for an account the vault already manages through generations. Nothing
// linked the two. In one morning: a `gnmi` row was created holding generation 4's
// secret for account `mcadmin`, telemetry flowed; the site ADMIN credential was then
// rotated to generation 5 and deployed to the fleet, which changed `mcadmin`'s password
// on every device; the `gnmi` row still held generation 4. TELEMETRY DIED AND NOTHING
// SAID SO — `gnmi_targets` still read 1, because a credential RESOLVED. It was merely
// wrong. The fleet credential runner, working exactly as designed, breaks gNMI
// collection on every rotation, at precisely the moment nobody is watching a telemetry
// graph.
//
// # Why re-syncing rather than the two alternatives
//
//   - GIVING THE `gnmi` KIND ITS OWN GENERATIONS duplicates the generation machinery
//     for what is not a separate account — it is a second PURPOSE for one login;
//   - DOCUMENTING THE HAZARD IN THE UI leaves it armed, and relies on an operator
//     remembering during the one gesture that touches every device in a site;
//   - RE-SYNCING is the only option that REMOVES the duplicate state instead of
//     managing it. After a rotation there is no second value left to be stale.
//
// # Why at ROTATION and not at deploy
//
// The sibling goes stale the moment the VAULT HEAD MOVES, and that happens in
// [Store.AddGeneration] — before any device is touched. Hanging the re-sync off the
// deploy runner instead (each device confirming) would leave the sibling wrong for the
// whole interval between rotating and running a deployment, which is exactly the window
// the observed defect lived in: the fleet was rotated in the morning and telemetry was
// dead from the minute the head moved, not from the minute a switch confirmed. It would
// also make the re-sync depend on the fleet being reachable — a site with every switch
// off would rotate, deploy nothing, and keep a `gnmi` row nobody would ever fix — and it
// would run the update N times, once per device, each one a separate transaction that
// can half-fail. There is exactly one moment at which the account's password changes in
// meshcraft's own records, and this is it.
//
// So the fan-out is part of the rotation's transaction. A rotation that moved the admin
// generation and failed to move the sibling would leave precisely the state this closes,
// so it must not be able to happen: same `tx`, one commit, all or nothing.
//
// # THE SCOPING RULE IS THE WHOLE DESIGN
//
// See [resyncTargets]. Getting it wrong is worse than not building this at all: a `gnmi`
// credential naming a DEDICATED telemetry account with its own password must never be
// touched, while a row naming the managed account is a copy and must follow.
//
// # What "moves to N" means here
//
// The sibling gets its OWN next generation number, not the rotated row's N. Numbering is
// per credential row and always has been (`addGenerationTx` reads `max(generation)+1` for
// one credential id), and pretending otherwise would either collide with the sibling's
// own history or leave holes in it. What is synchronised is the SECRET — the thing the
// account actually has — and the sibling's previous generation moves to `retiring` like
// any other, so a subscription that raced the rotation still has a candidate that works.

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Kind strings, as stored in `site_credentials.kind`.
//
// Declared here rather than imported from internal/snmpprov and internal/api because
// this package OWNS the column: these are values in a table, and the store has no
// business importing a protocol package to read its own rows. The literal strings are
// the contract — the same ones internal/api validates on the way in (checkCredScope) and
// the same ones already written in this package's own queries.
const (
	// kindAdmin is the administration password — the account meshcraft adopts devices
	// with, and the only kind the fleet deployment installs.
	kindAdmin = "admin"
	// kindSNMPv2c: a READ community. It has no account at all (the API forces its
	// username empty), so it never matches on username in the first place.
	kindSNMPv2c = "snmp_v2c"
	// kindSNMPv3: a USM identity — a PAIR of derived keys, not a login password.
	kindSNMPv3 = "snmp_v3"
)

// credRow is the minimum of a `site_credentials` row the re-sync rule judges on.
//
// Deliberately not [model.SiteCredential]: the rule must be readable as the few facts it
// actually uses, and a struct carrying `HasSecret` next to them invites a future version
// of this rule to start consulting things that are not identity or scope.
type credRow struct {
	ID       string
	Kind     string
	Username string
	// GroupName / Tag are the row's SCOPE. Read only about the row being rotated, and
	// only to establish that it is the site's default administration password rather
	// than a subset's — see [resyncTargets].
	GroupName string
	Tag       string
}

// SiblingResync is one credential row that FOLLOWED a rotation.
//
// Returned so the caller can say so out loud. A re-sync nobody can see is a second
// invisible state change, which is the same shape of problem as the one being fixed —
// an operator rotating a password must be told, in the site journal, that a second
// credential moved with it.
//
// IT CARRIES NO SECRET, and must never grow a field that could. What moved is identified
// by id, kind, account name and generation number; every one of those is already on the
// credential list screen. (Pinned by a reflection test in internal/api.)
type SiblingResync struct {
	CredentialID string `json:"credential_id"`
	Kind         string `json:"kind"`
	Username     string `json:"username"`
	Generation   int    `json:"generation"`
}

// resyncTargets picks the rows that must follow a rotation. THE WHOLE DESIGN IS HERE.
//
// PURE, and the one part of this mechanism that is fully provable without PostgreSQL,
// which is why it is a function and not a `where` clause. What it decides is which rows
// have their stored secret REWRITTEN; a wrong answer either leaves the observed defect
// armed or silently overwrites a password nobody asked it to touch.
//
// MATCHED — all four at once:
//
//  1. THE SAME SITE. Not expressed here: the caller's query is already restricted to one
//     site (`credSiblingRows`), because a credential id from another site is a different
//     fleet with a different password that happens to share a spelling;
//
//  2. THE ROTATED ROW IS THE SITE'S DEFAULT `admin` ONE — kind `admin`, no group, no tag.
//     Two guards in one, and both keep the blast radius small.
//
//     THE KIND, because the direction is not symmetric. The admin credential is the
//     account meshcraft installs on devices and the only one the fleet deployment moves,
//     so it is the one whose head changing makes a copy stale. Letting the fan-out run
//     the other way — rotating the `gnmi` row and having the admin row follow — would
//     mean a telemetry form could set the password meshcraft believes the whole fleet
//     carries, and a subsequent deploy run would push it to every switch. The telemetry
//     account is explicitly allowed its own cadence (cf. internal/api/gnmicred.go); it
//     does not get to speak for the fleet.
//
//     THE SCOPE, because a group's or a technical tag's admin row is A DIFFERENT PASSWORD
//     FOR A SUBSET, on purpose: pwpolicy_creds.go creates `pwpolicy-len15` precisely
//     because some platforms refuse the site secret. Those rows also name `mcadmin`, so
//     without this guard, rotating one of them would rewrite the site-wide telemetry row
//     to a secret only a handful of switches carry — breaking gNMI on every device that
//     was working. That is the defect being fixed, re-created by its own fix;
//
//  3. EXACTLY THE SAME USERNAME, byte for byte. This is the sentence that separates "a
//     copy of the managed account" from "a different account that also happens to be
//     stored here";
//
//  4. A DIFFERENT KIND. A row of the same kind is either the row being rotated itself or
//     a genuinely different credential in a different scope — two `admin` rows on one
//     site are the site secret and a group's or a technical tag's, which are different
//     passwords ON PURPOSE (pwpolicy_creds.go creates them precisely because one secret
//     does not fit every platform). Rewriting those would undo that.
//
// DELIBERATELY NOT MATCHED, each one earned:
//
//   - A DIFFERENT USERNAME, including one differing only in case. `MCADMIN` is not
//     matched. A case-insensitive compare is right when TRYING a login (cf.
//     credDeployAccount, which folds) and is a guess when it AUTHORISES A WRITE: if two
//     rows genuinely disagree about the spelling of an account, the honest outcome is
//     that one of them does not move and an operator sees it in the deployment view,
//     not that meshcraft picks one meaning and overwrites a password on it;
//   - AN EMPTY USERNAME on either side. "The same account" is not a statement that can
//     be made about no account. A v2c community has no account by construction, so it is
//     out by this rule alone, twice over;
//   - `snmp_v3`, EVEN WITH A MATCHING SECURITY NAME. A v3 secret is not this account's
//     login password: it is one half of a PAIR of keys derived from two phrases, changed
//     together or not at all (cf. checkCredSecrets, migration 0017). Writing the new
//     admin password into it would rotate the authentication phrase while the encryption
//     phrase stayed derived from the old one — the exact state that mechanism forbids —
//     and the device, which was never told, would simply drop the packets it cannot
//     decrypt. That is this defect's mirror image: silent, invisible, produced by a
//     rotation. A v3 user named `mcadmin` is a USM identity that shares a spelling with
//     a login, not a copy of it;
//   - `snmp_v2c`, for the same family of reason plus having no username to match;
//   - A PER-DEVICE ROTATION (`deviceID` non-empty). Under `per_device` there is no single
//     secret for the account at this site — each device has its own — so there is nothing
//     one sibling row could be re-synced TO. Pinning it to whichever device happened to
//     rotate last would be worse than leaving it stale, because it would look current.
//
// THE MISS THIS LEAVES, named rather than hidden: a site whose devices are all governed
// by a GROUP or technical-tag admin credential, with a `gnmi` row copying that password,
// is not re-synced — guard 2 refuses the rotation as a source. That is a false negative,
// and it is the direction to fail in: the row keeps the value an operator put there and
// stays visible in the credential list, whereas the alternative rewrites a password on a
// guess about which subset a telemetry row was meant to follow. Sites in that shape read
// the same journal entry they always did and nothing changes for them.
//
// Input order is preserved so a run is reproducible and the journal entry lists the same
// rows in the same order twice.
func resyncTargets(rotated credRow, deviceID string, rows []credRow) []credRow {
	var out = []credRow{}
	if deviceID != "" {
		return out
	}
	if rotated.Kind != kindAdmin || rotated.Username == "" {
		return out
	}
	if rotated.GroupName != "" || rotated.Tag != "" {
		return out
	}
	var r credRow
	for _, r = range rows {
		if r.ID == rotated.ID || r.Kind == rotated.Kind {
			continue
		}
		if r.Username == "" || r.Username != rotated.Username {
			continue
		}
		if r.Kind == kindSNMPv3 || r.Kind == kindSNMPv2c {
			continue
		}
		out = append(out, r)
	}
	return out
}

// credTx is the subset of [pgx.Tx] the generation writers use.
//
// It exists so that the ORDER and the EXTENT of what a rotation writes can be pinned
// without a database — the fan-out must stop at the first failure, and "it stops"
// is a claim about control flow, not about SQL. [pgx.Tx] satisfies it as-is, so nothing
// at the call sites changes and no production path goes through anything but a real
// transaction.
type credTx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// credSiblingRows reads, IN THE ROTATION'S OWN TRANSACTION, the credential being rotated
// and every other credential of its site.
//
// `for update` is not decoration. The rows are locked for the rest of the transaction so
// that a concurrent gesture cannot rename an account, or file a new `gnmi` row, between
// the moment this rule decides who follows and the moment their secrets are written. A
// site holds a handful of credentials, so locking all of them for the length of a
// rotation costs nothing measurable and removes a whole class of interleaving.
//
// The rotated row is returned separately rather than left in the list: everything the
// rule keys on comes from the DATABASE, never from what a caller believed it was
// rotating. A missing row yields a zero [credRow], and [resyncTargets] refuses it (no
// kind, no username), which is the right answer for a credential that vanished under us.
func credSiblingRows(ctx context.Context, tx pgx.Tx, credentialID string) (credRow,
	[]credRow, error) {
	var rotated credRow
	var others = []credRow{}
	var rows, err = tx.Query(ctx,
		`select c.id::text, c.kind, c.username, c.group_name, c.tag
		   from site_credentials c
		  where c.site_id = (select site_id from site_credentials where id = $1::uuid)
		  order by c.id
		    for update`, credentialID)
	if err != nil {
		return rotated, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r credRow
		if err = rows.Scan(&r.ID, &r.Kind, &r.Username, &r.GroupName, &r.Tag); err != nil {
			return rotated, nil, err
		}
		if r.ID == credentialID {
			rotated = r
			continue
		}
		others = append(others, r)
	}
	return rotated, others, rows.Err()
}

// resyncSiblingsTx writes the new secret onto each row the rule selected.
//
// Each sibling gets a REAL GENERATION through the same [addGenerationTx] a rotation uses
// — not a bare `update` of the head column. Two reasons, and the second is the one that
// bites: the sibling's previous secret must stay usable as a `retiring` candidate for
// anything that is mid-flight, and [Store.RevealSiteCredential] reads the ACTIVE
// GENERATION in preference to the head column, so a head-only write would be read back
// as the old secret by every consumer that matters — including `gnmiCredentialFor`,
// which is the whole point of this file.
//
// `secret2Enc` is deliberately nil for siblings: the second phrase belongs to `snmp_v3`
// alone, and v3 is not a kind this fan-out can select. Passing nil means both the
// generation insert and the head update leave whatever is stored alone rather than
// asserting an empty pair.
//
// THE FIRST ERROR STOPS EVERYTHING and is returned, so the caller's transaction is rolled
// back and NEITHER the rotation NOR any sibling took effect. A partially applied fan-out
// is the state this whole file exists to make impossible.
func resyncSiblingsTx(ctx context.Context, tx credTx, targets []credRow,
	secretEnc []byte, createdBy string) ([]SiblingResync, error) {
	var out = []SiblingResync{}
	var t credRow
	for _, t = range targets {
		var g, err = addGenerationTx(ctx, tx, t.ID, "", secretEnc, nil, createdBy)
		if err != nil {
			return nil, err
		}
		// The head follows for the same reason it follows on the rotated row: consumers
		// that predate generations, and `octet_length(secret_enc) > 0` — which is what
		// `HasSecret` is, and what gnmiCredentialFor checks before it will use a row.
		if _, err = tx.Exec(ctx,
			`update site_credentials set secret_enc = $2, rotated_at = now()
			  where id = $1::uuid`, t.ID, secretEnc); err != nil {
			return nil, err
		}
		out = append(out, SiblingResync{CredentialID: t.ID, Kind: t.Kind,
			Username: t.Username, Generation: g.Generation})
	}
	return out, nil
}
