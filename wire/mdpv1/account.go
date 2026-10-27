package mdpv1

import (
	"fmt"
	"strings"
)

// What an administration account IS, across eight platforms that do not agree
// on the question.
//
// # Why this type exists
//
// [AccountManager.Accounts] used to return `[]string`. A fleet-wide audit on
// 2026-08-10 asked the one question the credential vault exists for — WHICH
// PRIVILEGED ACCOUNTS EXIST THAT MESHCRAFT DID NOT CREATE — and could not answer
// it through the contract at all: a privilege-15 administrator and a read-only
// account were the same string. Several connectors already had the answer in
// their hands and threw it away at the interface boundary (RouterOS parsed the
// group, Instant On parsed `accessLevel`, EOS parsed both the privilege AND the
// role). This type is that boundary widened to the width of what the connectors
// already read.
//
// # A privilege is not a bool, and not always a number
//
// The eight ranges say three different things, and flattening them would mean
// inventing facts:
//
//   - FASTPATH (EdgeSwitch), IOS, FS S3900, Instant On: a NUMBER, 0..15, where
//     15 is the full administrator;
//   - EOS: a number AND a named ROLE — "privilege 15 role network-admin" —
//     and the role is what authorisation actually consults;
//   - RouterOS: a named GROUP (`full`, `read`, `write`, or one an operator
//     defined) and NO number whatsoever;
//   - FS S3400: nothing at all on the account line. Privilege there is a
//     property of the enable levels, not of the account.
//
// So [Account] carries the number when there is one, the name when there is
// one, and — because a caller comparing vendor strings would have to know that
// `full` means administrator on RouterOS and `network-admin` on EOS — the
// connector's own verdict in [Account.Authority].
//
// # "NOT SAID" IS A FACT, AND IT IS NOT ZERO
//
// [PrivilegeUnknown] is -1, never 0: level 0 is a real privilege on several of
// these platforms, and folding "no privilege" into "not stated" is exactly how
// an audit stops telling a harmless account apart from a hidden administrator.
// [AuthorityUnknown] is the ZERO VALUE of [Authority] for the same reason in
// the other direction: a zero-valued Account must claim nothing.
//
// # CHEAPNESS IS THE ADMISSION TICKET
//
// No connector performs a new read to fill this in. Where the privilege is in
// bytes the connector was already fetching, it travels; where it is not, the
// account reads UNKNOWN and the connector says why in its doc comment. Turning
// an enumeration into a second wire round trip on a range that grants five
// sessions would buy the audit an answer at a price the rest of the tree
// refuses to pay.
//
// # REPORT, NEVER ACTION
//
// Nothing here decides what to DO about an account meshcraft did not create.
// That is a product decision recorded in TODO.md, and one taken in the driver
// layer would be taken silently, identically, for every site at once.

// PrivilegeUnknown is the privilege of an account whose source did not state
// one.
//
// DELIBERATELY NOT 0 — see this file's header. Every connector that cannot
// state a level uses this, and no connector substitutes a platform default for
// it: "the device did not say" and "the device said zero" are different
// facts and the audit needs both.
const PrivilegeUnknown = -1

// PrivilegeFull is the numeric level that means "full administrator" on every
// range in this tree that counts privileges at all: 15 on IOS, EOS, FASTPATH
// and Instant On. A platform that used another maximum would not use this
// constant — it would state its own verdict in [Account.Authority].
const PrivilegeFull = 15

// Authority is a connector's own verdict on how far an account's rights go.
//
// It exists because the numbers and the names are not comparable across ranges:
// RouterOS has no number to test against [PrivilegeFull], and a caller that
// pattern-matched `full` / `network-admin` / `15` itself would be re-deriving,
// badly, what each connector already knows. The connector fills this in; the
// caller reads it.
type Authority int

const (
	// AuthorityUnknown: the source did not say, or said something this
	// connector cannot rank without a read it does not perform. THE ZERO
	// VALUE, so an Account nobody filled in claims nothing.
	AuthorityUnknown Authority = iota
	// AuthorityLimited: the account exists and is NOT a full administrator.
	AuthorityLimited
	// AuthorityFull: the account can administer the device — the answer the
	// 2026-08-10 audit was actually asking for.
	AuthorityFull
)

// String renders an Authority for prose a human reads.
func (a Authority) String() string {
	switch a {
	case AuthorityFull:
		return "full administrator"
	case AuthorityLimited:
		return "limited"
	default:
		return "not stated by the device"
	}
}

// Account is one administration account as a read the connector ALREADY
// PERFORMS enumerates it.
type Account struct {
	// Name is the account name, exactly as the device spells it.
	Name string
	// Privilege is the numeric level when the platform states one, and
	// [PrivilegeUnknown] otherwise. RouterOS never states one; FS S3400 never
	// states one; IOS and FASTPATH state one only when the line carries it.
	Privilege int
	// Role is the named role or group when the platform has one: EOS's
	// `network-admin`, RouterOS's `full` / `read` / `write`. Empty when the
	// platform has no such notion — which is NOT the same as an account
	// without a role, a distinction no range in this tree currently makes.
	Role string
	// Authority is the connector's verdict. See [Authority].
	Authority Authority
	// Source is the read this row came from — the command or the section.
	// What the audit quotes when it says where a name was seen.
	Source string
}

// AuthorityFromLevel ranks a numeric privilege the way every counting platform
// in this tree does: [PrivilegeFull] and above administers, below it does not,
// and [PrivilegeUnknown] ranks nothing.
//
// A NEGATIVE level other than [PrivilegeUnknown] cannot come from a parse (the
// regexes capture digits) and is treated as unknown rather than as "limited":
// inventing a rank for a value no device produced would be the one lie this
// file exists to prevent.
func AuthorityFromLevel(level int) Authority {
	if level < 0 {
		return AuthorityUnknown
	}
	if level >= PrivilegeFull {
		return AuthorityFull
	}
	return AuthorityLimited
}

// AccountAtLevel builds an [Account] from a numeric level, keeping Privilege
// and Authority in step. The four counting connectors go through it rather than
// each setting the two fields by hand, which is how one of them would eventually
// report "privilege 15, limited".
func AccountAtLevel(name string, level int, source string) Account {
	return Account{
		Name:      name,
		Privilege: level,
		Authority: AuthorityFromLevel(level),
		Source:    source,
	}
}

// AccountNames reduces a list to the bare names, for the call sites that only
// need to know WHETHER a name is present — choosing between create and modify,
// or refusing to remove the last account. nil for nil: an absent list and an
// empty one mean the same thing here.
func AccountNames(accounts []Account) []string {
	if len(accounts) == 0 {
		return nil
	}
	var out = make([]string, 0, len(accounts))
	var i int
	for i = range accounts {
		out = append(out, accounts[i].Name)
	}
	return out
}

// HasAccount reports whether a name is in the list, case-insensitively —
// devices differ on the case they echo back, and none of them would let two
// accounts differ only by it.
func HasAccount(accounts []Account, name string) bool {
	var i int
	for i = range accounts {
		if strings.EqualFold(accounts[i].Name, name) {
			return true
		}
	}
	return false
}

// AccountsExcept is the list as it would read once `name` is gone —
// case-insensitively, because devices differ on the case they echo back and
// none of them would let two accounts differ only by it.
//
// # THE PREDICATE EVERY LAST-ACCOUNT GUARD IS BUILT ON
//
// It lives here rather than in a connector because two connectors already
// needed it (`ubiquiti/edgeswitch` 2026-08-12, `instanton` the same day) and a
// refusal that means "the device keeps an account" must mean the same thing
// on every range. It answers WHAT SURVIVES, not how many there are: that is
// `len(have) <= 1` made exact, and it also lets through the delete of a name
// the device does not enumerate at all — which removes nothing and cannot be
// the last of anything.
//
// # WHAT A GUARD BUILT ON THIS DOES NOT PROMISE
//
// It counts accounts the DEVICE enumerates. It cannot know which of their
// secrets meshcraft holds, which have ever been authenticated, or which is a
// factory password left by a previous owner — none of that is in an [Account]
// and none of it can be put there, because a connector has no vault. The honest
// guarantee is exactly one sentence: THE DEVICE WILL STILL HAVE AN ACCOUNT.
// Anything stronger — "a credential remains that works" — belongs to the
// layer that can see the vault, and must not be implied by a predicate that
// cannot check it.
func AccountsExcept(accounts []Account, name string) []Account {
	var out []Account
	var i int
	for i = range accounts {
		if strings.EqualFold(accounts[i].Name, name) {
			continue
		}
		out = append(out, accounts[i])
	}
	return out
}

// PrivilegeWord renders a privilege for prose a human reads. Used by the
// transcripts, where "level 0" and "the device did not say" must not read
// alike.
func PrivilegeWord(p int) string {
	if p == PrivilegeUnknown {
		return "privilege not stated by the device"
	}
	if p >= PrivilegeFull {
		return fmt.Sprintf("privilege %d — FULL ADMINISTRATOR", p)
	}
	return fmt.Sprintf("privilege %d", p)
}

// Describe renders one account for a transcript line: what was read, in the
// words of whichever of the three vocabularies the platform speaks.
func (a Account) Describe() string {
	var what = a.Authority.String()
	if a.Privilege != PrivilegeUnknown {
		what = PrivilegeWord(a.Privilege)
	}
	if a.Role != "" {
		what += " (" + a.Role + ")"
	}
	return a.Name + ": " + what
}
