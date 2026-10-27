package store

// THE FALLBACK ADMINISTRATION ACCOUNT GETS ITS OWN TRACKED SECRET.
//
// # The defect, measured on two ranges rather than argued
//
// `keep_previous_account` (internal/api/devicecreds_handlers.go) leaves the
// account a device was adopted through in place, on the operator's explicit
// request, because on a range with no out-of-band console the old door is often
// the only one ever proven. Nothing then tracked that door. What it became was
// measured on 2026-08-11 and 2026-08-12, read-only:
//
//   - `unifimoule1` (FASTPATH, unsalted digest, so a changed digest is a changed
//     secret and nothing else). `admin` and `mcadmin` carried BYTE-IDENTICAL
//     hashes on 2026-08-10 — the fallback was not an independent way in, it was
//     the same secret under a second name. The 5 → 6 deployment of 15:59 on
//     08-11 moved `mcadmin` and left `admin` untouched, and the two have
//     diverged. `admin` was then proven BY LOGIN to be on GENERATION 4;
//   - `CiscoLabFirst1` (IOS type-5, md5-crypt, salt carried in the string, so
//     each hash is verifiable against a vault candidate). `admin` verifies
//     against GENERATION 4, `mcadmin` against GENERATION 6 — two rotations
//     stale, frozen at 2026-08-07 while the head moved twice.
//
// So the fallback is not a dead entry and not a duplicate: it is a WORKING,
// privilege-15, two-rotations-stale door, on a secret nothing rotates and nothing
// reports. That is worse than either of the two things it was mistaken for.
//
// # The decision, and why it lands HERE
//
// The operator's decision of 2026-08-12 is that `admin` gets ITS OWN TRACKED
// SECRET — a real second credential, rotated on its own schedule and visible on
// the device sheet — rather than a copy of the first or a piece of folklore.
//
// The whole of that decision is expressible as A SECOND `site_credentials` ROW,
// and this file is the scoping rule that makes the row safe. Everything else
// already exists and is reused unchanged: generations ([Store.AddGeneration]),
// try order ([Store.SecretsForCredential]), revelation
// ([Store.RevealSiteCredential]), per-device installed state
// (`device_credential_state`). Giving the fallback its own generation machinery
// would have been the mistake credresync.go names in its own header.
//
// # THE SCOPE KEY IS A RESERVED TAG, AND THE TAG IS NEVER SET ON A DEVICE
//
// `site_credentials` is unique on (site_id, group_name, tag, kind), so a second
// `admin` row must differ on one of the two scope columns.
//
//   - NOT `group_name`: that column is the operator's own filing ("ops"), and
//     a fallback filed there would answer [Store.CredentialFor] for any group
//     that happened to be spelled the same way;
//   - A RESERVED TAG, on the precedent of `pwpolicy-` (internal/pwpolicy): the
//     namespace is refused to manual entry, so a spelling variant cannot
//     fabricate a second fallback that nothing rotates — which is the very defect
//     being closed.
//
// AND THEN THE ONE RULE THAT MAKES IT SAFE: this tag is a scope key in
// `site_credentials` and MUST NEVER become a `device_tags` row. [Store.CredentialsForDevice]
// ranks a tag credential at specificity 3 against the site row's 1, so tagging a
// device would make the FALLBACK the credential every session opens with and
// every fleet deployment writes — the emergency door would silently become the
// main one, and the main one would go untracked. That is the single most
// dangerous mistake available in this design, it is one `SetDeviceTag` call away,
// and [IsFallbackAdminTag] exists so the guards can say no.
//
// # What this file deliberately does NOT do
//
// IT WRITES NOTHING TO ANY DEVICE. Placing a second account on a fleet is a
// separate, riskier gesture that the operator has not authorised, and the halt
// recorded in TODO.md — a deletion ordered and stopped before any write — is the
// reason to keep the two apart. What is built here is the vault side: the row,
// its generations, and the reading that lets a device sheet say which generation
// each account carries.

import (
	"context"
	"strings"
	"time"

	"meshcraft/internal/model"
)

// FallbackAdminTag is the reserved scope key of the fallback administration
// credential. One per site, and its whole namespace is refused to manual entry
// for the reason [IsFallbackAdminTag] states.
const FallbackAdminTag = "fallback-admin"

// IsFallbackAdminTag recognises the reserved namespace.
//
// DELIBERATELY BROADER than an equality test, exactly like
// [pwpolicy.IsTechnicalTag] and for the same reason: what input validation needs
// is to refuse the ENTIRE namespace by hand. A guard that refused only the
// canonical spelling would let `fallback-admin-2` through, which looks official,
// would be tracked by nothing, and would reproduce the untracked second door this
// whole mechanism exists to remove.
//
// It is also what the device-tag guards key on: this tag must never be set on a
// device (see the file header), and a variant spelling must not slip past that
// check either.
func IsFallbackAdminTag(tag string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), FallbackAdminTag)
}

// ErrFallbackTagIsNotADeviceTag is [Store.SetDeviceTag]'s refusal of the reserved
// namespace. A sentence rather than a bare error because the caller that trips it
// is almost certainly doing what looks like the obvious thing.
const ErrFallbackTagIsNotADeviceTag = storeError("\"" + FallbackAdminTag +
	"\" is a credential scope, not a device tag: it must never be set on a device. " +
	"Doing so would make the FALLBACK administration credential more specific than " +
	"the site one, so every session and every fleet deployment would use the " +
	"emergency account instead of the managed one")

// storeError is a constant error, so a refusal can be declared next to the rule
// it enforces.
type storeError string

func (e storeError) Error() string { return string(e) }

// FallbackAdminCredential returns a site's fallback administration credential.
//
// [ErrNotFound] when the site has none, and that is an ORDINARY answer, not an
// error condition: most sites do not carry a second account, and `core01.lab`
// proves the third shape exists too — it carries `no username admin`, the
// fallback was REMOVED there rather than duplicated. A caller must be able to
// tell "this site has no fallback" from "this site has one and it is stale",
// because only the second is a finding.
func (s *Store) FallbackAdminCredential(ctx context.Context,
	siteID string) (model.SiteCredential, error) {
	return scanSiteCred(s.pool.QueryRow(ctx,
		`select `+siteCredCols+` from site_credentials
		  where site_id = $1::uuid and kind = $2 and tag = $3`,
		siteID, kindAdmin, FallbackAdminTag))
}

// EnsureFallbackAdminCredential files the fallback account in the vault, with its
// own first generation.
//
// # It records an account that ALREADY EXISTS on devices
//
// This is not a provisioning call and it must not read like one. The account is
// already out there — `keep_previous_account` put it there, or a hand-configured
// device came with it — and what has been missing is a row saying so. Which is
// why the caller supplies the SECRET rather than having one generated: the
// honest first generation of `unifimoule1`'s fallback is the vault's generation
// 4, the value it was PROVEN to carry by login, not a fresh secret no device
// holds.
//
// A caller that genuinely wants a new secret generates one and passes it — and
// then owes the fleet a deployment, which is a separate gesture this package does
// not perform.
//
// # Idempotent, and it never silently rewrites
//
// A `nil` secret leaves the stored one alone: [Store.UpsertSiteCredential]'s
// secretless path updates the label and nothing else. That matters more here than
// elsewhere — the fallback is the door of last resort, and a screen edit that
// quietly replaced its secret with an empty one would remove the only proven way
// back into a device whose main credential has drifted.
//
// A non-nil secret ALWAYS opens a generation, including the first, so the
// fallback's history starts the way every other credential's does and
// [Store.SecretsForCredential] has something to offer from the moment the row
// exists.
func (s *Store) EnsureFallbackAdminCredential(ctx context.Context, siteID, username string,
	secretEnc []byte, updatedBy string) (model.SiteCredential, error) {
	return s.UpsertSiteCredential(ctx, model.SiteCredential{
		SiteID: siteID, Tag: FallbackAdminTag, Kind: kindAdmin, Username: username,
		Note: "Fallback administration account, tracked on its own schedule. It is the " +
			"account devices were adopted through and that `keep_previous_account` left " +
			"in place; it is NOT a copy of the site password and does not follow its " +
			"rotations. Rotating it is a deliberate gesture and needs its own deployment " +
			"— until one runs, the devices still carry whatever generation they were " +
			"left on. This credential is managed by meshcraft and is not deletable by " +
			"hand: an untracked second door is what this row exists to replace.",
	}, secretEnc, nil, updatedBy)
}

// AccountGeneration is what a device sheet needs to say WHICH GENERATION EACH
// ACCOUNT CARRIES.
//
// # Why this is not carried on [driver.Account]
//
// `Accounts()` already returns `Name`, `Privilege`, `Role`, `Authority` and
// `Source` — the natural place to look, and the wrong one. Every field there is
// something the connector READ OFF THE DEVICE, and `Source` is the command it
// read it from. A generation number is not on the device and never will be: it is
// vault knowledge, and a connector that filled it in would be fabricating exactly
// the kind of state this codebase re-reads devices to avoid. The one range where
// the device could in principle answer — IOS, whose type-5 hash is verifiable
// against a candidate — proves the point rather than weakening it: getting the
// answer took every generation out of the vault and an md5-crypt implementation,
// which is not a connector's job.
//
// So the device sheet JOINS two truths that stay separate:
//
//   - [driver.Account] — the accounts that EXIST on the device, from the device;
//   - this type — what meshcraft has RECORDED about each account, from the vault.
//
// The join key is the account NAME, which both sides carry. An account present on
// the device with no row here is exactly the finding worth showing: an untracked
// door, which is what `ubnt` is on `unifimoule1` and why the deletion of `admin`
// there was halted.
type AccountGeneration struct {
	CredentialID string `json:"credential_id"`
	// Username as the VAULT spells it, and InstalledUsername as the DEVICE was
	// last seen to spell it. They differ on every range that only accepts
	// `admin`, and collapsing them would make the sheet's join key a guess.
	Username          string `json:"username"`
	InstalledUsername string `json:"installed_username,omitempty"`
	// Fallback distinguishes the two rows without the caller having to know the
	// tag spelling.
	Fallback bool `json:"fallback"`
	// Installed is the generation THIS DEVICE was last confirmed to carry, and 0
	// means meshcraft has never confirmed one. Zero is not "generation zero" and
	// must not be shown as a number: it is the honest state of every device whose
	// fallback nobody has ever recorded — which was all of them until this row
	// existed.
	Installed int `json:"installed_generation"`
	// Active is the vault's current head for this credential. `Installed < Active`
	// is the STALE FALLBACK, stated as a comparison rather than left for a reader
	// to make: `admin` on generation 4 against a head of 6 is the measured state
	// of both lab units.
	Active        int        `json:"active_generation"`
	InstalledAt   *time.Time `json:"installed_at,omitempty"`
	LastVerified  *time.Time `json:"last_verified_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	RotationsBack int        `json:"rotations_back"`
}

// Stale reports a credential whose device is behind the vault head.
//
// Requires a CONFIRMED installed generation: an unrecorded account (Installed
// zero) is UNKNOWN, not stale, and the two must not be shown the same way. Every
// fallback in the fleet is unrecorded until somebody records it, and painting
// them all red on the day this ships would say nothing about any of them.
func (a AccountGeneration) Stale() bool {
	return a.Installed > 0 && a.Active > 0 && a.Installed < a.Active
}

// Unrecorded reports an account the vault tracks but has never confirmed on this
// device.
func (a AccountGeneration) Unrecorded() bool { return a.Installed == 0 }

// AdminAccountGenerations answers, for one device, what the vault knows about each
// administration account of its site — the primary first, the fallback second.
//
// It reads `device_credential_state`, which ALREADY records the installed
// generation per (device, credential) and has since migration 0011. That is the
// point of the whole design: the fallback getting its own credential row is what
// makes this table answer the question, with no new column and no new writer.
//
// The head generation is the SITE-LEVEL active one (`device_id is null`), the same
// row [Store.RevealSiteCredential] reveals, so "active" here and "what a reveal
// hands you" cannot drift apart. Under `per_device` there is no single head and
// the comparison is meaningless; such a credential reports Active 0, which
// [AccountGeneration.Stale] then refuses to judge rather than judging wrongly.
func (s *Store) AdminAccountGenerations(ctx context.Context,
	deviceID string) ([]AccountGeneration, error) {
	var rows, err = s.pool.Query(ctx,
		`select c.id::text, c.username, c.tag,
		        coalesce(st.installed_generation, 0), coalesce(st.installed_username, ''),
		        st.installed_at, st.last_verified_at, coalesce(st.last_error, ''),
		        coalesce(g.generation, 0)
		   from site_credentials c
		   left join device_credential_state st
		     on st.credential_id = c.id and st.device_id = $1::uuid
		   left join lateral (
		         select generation from credential_generations
		          where credential_id = c.id and device_id is null and state = 'active'
		          order by generation desc limit 1
		   ) g on true
		  where c.site_id = (select site_id from devices where id = $1::uuid)
		    and c.kind = $2
		    and not c.per_device
		  order by (c.tag <> ''), c.tag, c.id`, deviceID, kindAdmin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out = []AccountGeneration{}
	for rows.Next() {
		var a AccountGeneration
		var tag string
		if err = rows.Scan(&a.CredentialID, &a.Username, &tag, &a.Installed,
			&a.InstalledUsername, &a.InstalledAt, &a.LastVerified, &a.LastError,
			&a.Active); err != nil {
			return nil, err
		}
		a.Fallback = IsFallbackAdminTag(tag)
		if a.Stale() {
			a.RotationsBack = a.Active - a.Installed
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
