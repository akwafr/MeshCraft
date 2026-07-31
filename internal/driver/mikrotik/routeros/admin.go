package routeros

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Administration accounts on RouterOS.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ UNVERIFIED ON HARDWARE for everything that writes (EnsureAccount,           │
// │ DeleteAccount, SetPassword). READS (Accounts, Groups,                       │
// │ PasswordPolicy) were exercised on the reference CRS326 and their            │
// │ responses are in testdata/.                                                 │
// └─────────────────────────────────────────────────────────────────────────────┘
//
// Good news relative to the Instant On product line: RouterOS is a real
// multi-account system, and placing an account named `mcadmin` is the normal
// case. OBSERVED — `/user` is a list of records:
//
//	[{".id":"*1","name":"admin","group":"full","comment":"system default user",
//	  "disabled":"false","last-logged-in":"2026-07-28 17:03:48"}]
//
// THE TRAP on this platform, and it is specific: it is not the password that
// is the problem, it is the GROUP. A RouterOS account has no privileges of its
// own — everything comes from its group's policy. OBSERVED, `/user/group`:
//
//	read  : ...,read,test,winbox,password,web,sniff,sensitive,api,romon,rest-api,!ftp,!write,!policy
//	write : ...,read,write,test,winbox,password,web,sniff,sensitive,api,romon,rest-api,!ftp,!policy
//	full  : local,telnet,ssh,ftp,reboot,read,write,policy,test,winbox,password,web,sniff,sensitive,api,romon,rest-api
//
// Three consequences to keep in mind before placing anything:
//
//  1. WITHOUT `rest-api`, meshcraft cuts its own path. That is the nastiest
//     scenario: the set succeeds, the device confirms the account, and the next
//     read answers 403. All three factory groups carry it, but a group made by
//     the operator may not — hence EnsureAccount's explicit check.
//  2. WITHOUT `write`, no configuration. An account in group `read` will read
//     VLANs and fail to set them.
//  3. WITHOUT `policy`, no account management. Only `full` carries it. A
//     meshcraft account placed in `write` can neither create the next account
//     nor remove `admin` — and it will learn that via a 403 at the worst moment,
//     rotation time.

// adminGroup is the group in which meshcraft places its account.
//
// `full` and not `write`, deliberately. `write` would suffice to set VLANs, but
// not to rotate a secret or remove the factory account (`policy` policy,
// observed absent from `write`). A provisioning account that cannot provision
// the next account is a dead end: at the first secret change, one would have to
// bring back the `admin` account that had just been removed.
const adminGroup = "full"

// requiredPolicies are the policies without which the account meshcraft places
// would be useless to it.
//
// `rest-api` first because it is the only one whose absence is INVISIBLE at set
// time: the other two fail on the first write; that one closes the door behind us.
var requiredPolicies = []string{"rest-api", "read", "write"}

// ListAccounts lists administration accounts present on the device, with
// everything the record carries.
//
// This is a READ, and it is what makes account operations safe: knowing what
// exists before writing avoids creating an account that is already there and
// believing a still-present account was deleted.
//
// Named ListAccounts and not `Accounts`, because the contract reserves that name
// for a list of NAMES ([Client.Accounts]). What is kept here is what writes need
// and the contract does not carry: the record `.id` — without which no `set` or
// `remove` is possible —, the group, and the last-login date, which answers
// « has this account already been used? » before removing the one it replaces.
//
// VALIDATED LIVE against 10.99.0.54: returns one account, `admin`, in group `full`.
func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	var rows, err = c.List(ctx, "user")
	if err != nil {
		return nil, err
	}
	var out = []Account{}
	var r Row
	for _, r = range rows {
		out = append(out, Account{
			Name:      r.Get("name"),
			Group:     r.Get("group"),
			Comment:   r.Get("comment"),
			Disabled:  r.Bool("disabled"),
			LastLogin: r.Get("last-logged-in"),
			RowID:     r.ID(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Account is an administration account as the device carries it.
type Account struct {
	Name     string `json:"name"`
	Group    string `json:"group"`
	Comment  string `json:"comment,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// LastLogin answers « has this account already been used? » before removing
	// the one it replaces. Observed: « 2026-07-28 17:03:48 ».
	LastLogin string `json:"last_login,omitempty"`
	RowID     string `json:"-"`
}

// PasswordPolicy is the SITE password policy as the device carries it.
//
// OBSERVED — `/user/settings`: `{"minimum-password-length":"0","minimum-categories":"0"}`.
// RouterOS therefore imposes nothing from factory, but these two bounds are
// CONFIGURABLE: an operator who raised the minimum to 12 will reject a secret
// meshcraft considers fine. That is why this read exists — assuming 0 still
// holds exposes a provisioning failure whose cause would be invisible.
type PasswordPolicy struct {
	MinLength int `json:"min_length"`
	// MinCategories is the number of required character classes (lowercase,
	// uppercase, digits, punctuation). meshcraft only generates alphanumeric:
	// a requirement of 4 categories would therefore be a guaranteed refusal,
	// and it is better to say so before writing than after.
	MinCategories int `json:"min_categories"`
}

// PasswordPolicy reads the local policy.
func (c *Client) PasswordPolicy(ctx context.Context) (PasswordPolicy, error) {
	var out PasswordPolicy
	var row, err = c.Object(ctx, "user/settings")
	if err != nil {
		return out, err
	}
	out.MinLength, _ = row.Int("minimum-password-length")
	out.MinCategories, _ = row.Int("minimum-categories")
	return out, nil
}

// Accepts says whether a secret satisfies the device policy, and why not.
//
// Category counting matches RouterOS: lowercase, uppercase, digits, other. An
// alphanumeric secret generated by meshcraft therefore covers at most THREE —
// hence the explicit message when the device requires four, rather than a
// refusal one would blame on length.
func (p PasswordPolicy) Accepts(secret string) (bool, string) {
	if p.MinLength > 0 && len(secret) < p.MinLength {
		return false, fmt.Sprintf(
			"device requires at least %d characters, secret has %d",
			p.MinLength, len(secret))
	}
	if p.MinCategories <= 0 {
		return true, ""
	}
	var lower, upper, digit, other bool
	var r rune
	for _, r = range secret {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			other = true
		}
	}
	var count int
	var flag bool
	for _, flag = range []bool{lower, upper, digit, other} {
		if flag {
			count++
		}
	}
	if count < p.MinCategories {
		return false, fmt.Sprintf(
			"device requires %d character classes, secret has %d. "+
				"meshcraft generates alphanumeric secrets only (3 classes at most): "+
				"a requirement of 4 means lowering `minimum-categories` on the device, "+
				"or entering the password manually", p.MinCategories, count)
	}
	return true, ""
}

// Groups lists groups and their policies.
//
// VALIDATED LIVE: returns `read`, `write`, `full`.
func (c *Client) Groups(ctx context.Context) (map[string][]string, error) {
	var rows, err = c.List(ctx, "user/group")
	if err != nil {
		return nil, err
	}
	var out = map[string][]string{}
	var r Row
	for _, r = range rows {
		out[r.Get("name")] = SplitList(r.Get("policy"))
	}
	return out, nil
}

// GroupAllows says whether a policy is granted by a group.
//
// RouterOS policy is a list where an entry can be DENIED by a `!` prefix —
// observed: group `read` carries `!write` and `!policy`. A naive search for the
// substring « write » in `read`'s policy would find it inside `!write` and
// conclude exactly the opposite of the truth. That is the kind of error that
// places an account unable to write, discovered on the first apply.
func GroupAllows(policies []string, want string) bool {
	var granted bool
	var p string
	for _, p = range policies {
		if p == want {
			granted = true
		}
		if p == "!"+want {
			return false
		}
	}
	return granted
}

// EnsureAccount guarantees that an account named `user` exists, carries
// `secret`, and belongs to a group that lets meshcraft keep working.
//
// UNVERIFIED ON HARDWARE (write). The sequence is as follows, and order
// matters:
//
//  1. READ the password policy. A secret refused by local policy must be
//     detected BEFORE sending anything: on a platform that persists immediately,
//     a half-accepted `add` would leave an account with no usable password.
//  2. READ groups and verify the target carries `rest-api`, `read` and `write`.
//     Placing an account without `rest-api` is the only gesture in this file that
//     succeeds and turns against us on the next request.
//  3. READ accounts to choose between `add` and `set`. RouterOS distinguishes
//     the two and refuses one in place of the other; that is also what makes the
//     operation replayable — an interrupted set does not stumble on a half-created
//     account.
func (c *Client) EnsureAccount(ctx context.Context, user, secret string) error {
	if strings.TrimSpace(user) == "" {
		return fmt.Errorf("empty account name")
	}

	// 1. Local policy.
	var policy, err = c.PasswordPolicy(ctx)
	if err != nil {
		// Unreadable menu: continue, but say so. Refusing here would prevent
		// provisioning a firmware that does not serve `/user/settings`, while the
		// password might still pass.
		c.step(false, "password policy unreadable",
			err.Error()+" — apply continues, the device will reject the password itself if needed")
	} else {
		var ok, reason = policy.Accepts(secret)
		if !ok {
			c.step(false, "secret rejected by device password policy", reason)
			return fmt.Errorf("secret incompatible with device password policy: %s",
				reason)
		}
		c.step(true, "password policy verified",
			fmt.Sprintf("minimum length %d, character classes %d",
				policy.MinLength, policy.MinCategories))
	}

	// 2. The group. This is the check that avoids closing the door on ourselves.
	var groups map[string][]string
	groups, err = c.Groups(ctx)
	if err != nil {
		return err
	}
	var policies, known = groups[adminGroup]
	if !known {
		return fmt.Errorf("group %q does not exist on this device: "+
			"meshcraft will not place an account in a group it could not verify",
			adminGroup)
	}
	var needed string
	for _, needed = range requiredPolicies {
		if GroupAllows(policies, needed) {
			continue
		}
		return fmt.Errorf("group %q on this device does not carry policy `%s`: "+
			"placing the meshcraft account there would make it unusable "+
			"(observed policies: %s)", adminGroup, needed, JoinList(policies))
	}

	// 3. Create or modify.
	var accounts []Account
	accounts, err = c.ListAccounts(ctx)
	if err != nil {
		return err
	}
	var existing *Account
	var i int
	for i = range accounts {
		if strings.EqualFold(accounts[i].Name, user) {
			existing = &accounts[i]
			break
		}
	}

	var fields = map[string]string{
		"name":     user,
		"password": secret,
		"group":    adminGroup,
		// A trace of who placed this account, readable by the human admin who
		// will open `/user` one day and wonder where that name came from.
		"comment": "meshcraft",
	}
	if existing != nil {
		// `name` is not sent on a `set`: the account is designated by its `.id`,
		// and reaffirming the name only risks an accidental rename.
		delete(fields, "name")
		_, err = c.Set(ctx, "user", existing.RowID, fields)
		if err != nil {
			return err
		}
		c.step(true, fmt.Sprintf("account %q updated on device", user),
			"group "+adminGroup)
		return nil
	}
	_, err = c.Add(ctx, "user", fields)
	if err != nil {
		return err
	}
	c.step(true, fmt.Sprintf("account %q created on device", user), "group "+adminGroup)
	return nil
}

// VerifyLogin proves that an account REALLY opens a session, by using it.
//
// The only test worth running before removing the factory account: a write
// return code says the device ACCEPTED the account, not that it WILL ACCEPT it
// on the next login. The difference is not theoretical on this platform — a
// perfectly created account in a group without `rest-api` answers 403 here, and
// that is exactly what one wants to discover BEFORE cutting the other door.
//
// The chosen read is `/system/identity`, the cheapest that requires being
// authenticated (observed: 17 bytes of response).
func VerifyLogin(ctx context.Context, host string, d driver.Dialer,
	user, secret string) error {
	var probe = Open(host, d, user, secret)
	var _, err = probe.Object(ctx, "system/identity")
	if err != nil {
		return fmt.Errorf("account %q does not open a REST session: %w", user, err)
	}
	return nil
}

// DeleteAccount removes an administration account.
//
// UNVERIFIED ON HARDWARE (write).
//
// Less dangerous than its Instant On counterpart, and that must be said honestly:
// this platform has a serial console and MAC-winbox (both OBSERVED). Losing the
// last usable account does not require a factory reset — a cable or a host on
// the same L2 segment is enough to regain control.
//
// That excuses nothing: the caller MUST have proven, via VerifyLogin and not a
// return code, that another account works. An avoided site visit beats a
// recoverable one.
func (c *Client) DeleteAccount(ctx context.Context, user string) error {
	if strings.EqualFold(user, c.User) {
		// Deleting oneself would leave the current session without a holder, and
		// the device with no guarantee that another account answers.
		return fmt.Errorf("refusing to delete %q: it is the account of the current session", user)
	}
	var accounts, err = c.ListAccounts(ctx)
	if err != nil {
		return err
	}
	// How many accounts would remain able to write? RouterOS does not ask that
	// for us, and deleting the second-to-last administrator goes through without
	// warning.
	var target *Account
	var survivors int
	var i int
	for i = range accounts {
		if strings.EqualFold(accounts[i].Name, user) {
			target = &accounts[i]
			continue
		}
		if !accounts[i].Disabled {
			survivors++
		}
	}
	if target == nil {
		// Already absent: not an error, it is the desired state. Saying so avoids
		// failing a replayed provisioning.
		c.step(true, fmt.Sprintf("account %q already absent from device", user), "nothing to do")
		return nil
	}
	if survivors == 0 {
		return fmt.Errorf("refusing to delete %q: it would be the LAST active account "+
			"on the device. Access would remain possible only via serial console or "+
			"MAC-winbox, on site", user)
	}
	_, err = c.Remove(ctx, "user", target.RowID)
	if err != nil {
		return err
	}
	c.step(true, fmt.Sprintf("account %q deleted from device", user),
		strconv.Itoa(survivors)+" active account(s) remaining")
	return nil
}

// SetIdentity sets the device system name.
//
// UNVERIFIED ON HARDWARE (write). OBSERVED on read: `/system/identity` returns
// `{"name":"lan-r4"}`, a unique object not a list — so it is modified by `set`
// WITHOUT `.id`, the only place in this connector where a `set` without an
// identifier is legitimate.
func (c *Client) SetIdentity(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("empty system name")
	}
	var _, err = c.Command(ctx, "system/identity", "set",
		map[string]string{"name": name})
	if err != nil {
		return err
	}
	c.step(true, fmt.Sprintf("system name set to %q", name), "")
	return nil
}

// --- What the contract expects of an administration access ---

// Accounts lists the NAMES of administration accounts present.
//
// The shape the contract asks for, and it is poorer than what the device says:
// [Client.ListAccounts] returns full records, and that is what writes in this
// file use. The poverty is on the caller's side — the API layer only needs to
// know WHETHER a name exists, to choose between create and modify, and not to
// remove an account that is not there.
//
// Order is that of [Client.ListAccounts], so alphabetical: two runs must return
// the same list, otherwise comparing two observations means nothing.
//
// ONE READ, and it is what makes account writes safe: RouterOS distinguishes
// « create » from « modify » and refuses one in place of the other. That is also
// what makes the operation replayable — an interrupted then resumed set does not
// stumble on a half-created account.
func (c *Client) Accounts(ctx context.Context) ([]string, error) {
	var accounts, err = c.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var out = make([]string, 0, len(accounts))
	var a Account
	for _, a = range accounts {
		out = append(out, a.Name)
	}
	return out, nil
}

// SetSecret changes the password OF THE ACCOUNT THAT JUST LOGGED IN.
//
// The account is not a contract parameter, deliberately: it is
// [Client.Account], the one PROVEN to open the device. Letting the caller name
// another would open the door to « change the password of an account we have not
// proven we can access »; creating or modifying ANOTHER account is a distinct
// gesture, and it lives in [driver.AccountManager].
//
// `oldSecret` IS IGNORED, and why must be said rather than left unused: RouterOS
// does not require it. A `set` on `/user` carries the new password and nothing
// else, authorization coming from the request's own Basic auth. The parameter
// exists in the contract because SwOS uses it as the RC4 KEY of the digest it
// expects and answers HTTP 405 if wrong — and the caller need not distinguish
// the two cases. That is exactly what the contract exists to absorb.
//
// THE SET PERSISTS, with nothing further to do: RouterOS is
// [driver.CommitImmediate], the password is in non-volatile memory when the
// device answers. The caller therefore need not chain onto [driver.Persister],
// which this connector does not implement anyway (see contract.go).
//
// GOES THROUGH [Client.EnsureAccount], and that is not a shortcut: it is what
// makes changing a secret ALSO verify the site password policy and the group
// policy. A direct `set` would succeed at placing a secret that
// `minimum-password-length` refuses — on a platform that persists immediately,
// discovering the refusal after sending the request is the worst moment.
//
// What remains the caller's responsibility, and no connector can do in its
// place: only record the new secret in the database AFTER OBSERVING that it
// opens the device, i.e. after a second [Driver.Authenticate] carrying that
// single candidate. A database convinced of a success that never happened costs
// a site visit — mitigated here by serial console and MAC-winbox, but a visit
// nonetheless.
func (c *Client) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	if strings.TrimSpace(c.User) == "" {
		// Without a proven account, there is nobody whose secret can be changed.
		// This happens on the access [Driver.Authenticate] returns despite failure,
		// for its transcript: the refusal must be clear rather than issuing a `set`
		// on an empty name.
		return fmt.Errorf("no account has been proven on this device: there is " +
			"no password to change, and meshcraft does not guess the holder name")
	}
	return c.EnsureAccount(ctx, c.User, newSecret)
}
