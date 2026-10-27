package s3400

// SNMPv3 on the FS S3400 — authNoPriv, and NOTHING ELSE, by decision of
// 2026-08-12 taken on measurements rather than on a reference guide.
//
// # WHY THIS RANGE GETS authNoPriv AND WHY THAT IS NOT A FALLBACK
//
// A form ladder run on poefs3400lab (Version 2.0.2J build 95262) on 2026-08-11
// asked the only question that decides whether authPriv can exist here at all —
// is there a user form taking a SECOND passphrase? Every candidate was sent as a
// WHOLE command, never probed with "?", and the running-config came back
// byte-identical afterwards:
//
//	snmp-server user u g v3 auth sha <P1> priv des <P2>            → Unknown command
//	snmp-server user u g v3 auth sha <P1> priv aes <P2>            → Unknown command
//	snmp-server user u g v3 encrypted auth sha <P1> priv des <P2>  → Unknown command
//	snmp-server user u g v3 auth sha <P1> priv <P2>                → Unknown command
//	snmp-server user u g v3 auth md5 <P1>                          → ACCEPTED
//
// There is NO SLOT for a privacy passphrase on this firmware. [SNMPIdentity]
// requires two DISTINCT phrases precisely because reusing one feeds the same
// value into both RFC 3414 derivations — the privacy key becomes a prefix of the
// authentication key and the encryption adds nothing — so an applier that took
// [SNMPIdentity.PrivSecret] here would have to DISCARD it. That is the
// "accepted and did something else" class the re-read discipline exists to
// catch, and it is the one case no re-read could ever catch: the configuration
// would show exactly what the connector typed.
//
// So privacy is REFUSED, by name and with the firmware's own reason
// ([Client.refuseSNMPv3Priv]), and [Capabilities.SNMPv3Priv] is declared EMPTY so
// that no screen offers what the platform does not have. authNoPriv is what this
// range CAN express honestly, not a rounding of what was asked for.
//
// # WHAT THE 2026-08-12 LADDER MEASURED, and every line of this file rests on it
//
// [TestLiveS3400AuthNoPrivLadder], one session on poefs3400lab, final
// running-config BYTE-IDENTICAL to the baseline:
//
//  1. THE GROUP CAN BE CREATED AT "auth". "snmp-server group <g> v3 auth" is
//     ACCEPTED and "show snmp group" reads it back as "security model: v3 auth"
//     with "read: default". This was the decision's one blocking unknown: the
//     2026-08-10 survey recorded that an authNoPriv user against a "v3 priv"
//     group answers authorizationError — it authenticates, walks, and gets
//     nothing — so an authNoPriv applier had to be able to write the group at
//     "auth" or not ship at all. It can. "v3 noauth" is also accepted
//     ("security model: v3 noauth") and is NOT used: an unauthenticated group is
//     not an identity;
//  2. THE AUTHENTICATION ALGORITHMS ARE MD5 AND SHA-1, AND NOTHING ABOVE. Measured
//     one form per user name so no acceptance masked the next verdict:
//
//     snmp-server user u g v3 auth sha <P>            → ACCEPTED, "Authentication Protocol: SHA"
//     snmp-server user u g v3 auth md5 <P>            → ACCEPTED, "Authentication Protocol: MD5"
//     snmp-server user u g v3 encrypted auth sha <P>  → ACCEPTED, "Authentication Protocol: SHA"
//     snmp-server user u g v3 auth sha256 <P>         → Unknown command
//     snmp-server user u g v3 auth sha-256 <P>        → Unknown command
//     snmp-server user u g v3 noauth                  → Unknown command
//
//     The PLAIN form is the one written here. "encrypted" is accepted and is not
//     used: it renders the passphrase in clear all the same (see 4), so it buys
//     nothing and adds a token to every comparison;
//  3. A USER MAY NAME A GROUP THAT DOES NOT EXIST, and this firmware does not say
//     so. "snmp-server user mcorph mcnogroup v3 auth sha <P>" was ACCEPTED with
//     mcnogroup never created, and "show snmp user" printed "Group-name:
//     mcnogroup" with NO annotation — where EOS prints "(not configured)".
//     THIS REFUTES the hope recorded in TODO.md on 2026-08-11 that naming the
//     group in the user read made the EOS failure class catchable from that read
//     alone. It is catchable only through "show snmp group", which is why
//     [Client.confirmSNMPv3Group] is a hard gate and not a note;
//  4. THE PASSPHRASE COMES BACK IN CLEAR in "show running-config", with an
//     encryption-type "0" INSERTED between the algorithm and it — the one token
//     this firmware adds that was not typed:
//
//     typed:  snmp-server user mcau1 mcagA v3 auth sha <P>
//     stored: snmp-server user mcau1 mcagA v3 auth sha 0 <P>
//
// # THIS CONNECTOR VERIFIES THE PASSPHRASE, WHICH THE EOS ONE CANNOT
//
// Point 4 is worth stating as a capability and not as a curiosity. On EOS the
// keys are RFC 3414 LOCALIZED digests, so that connector confirms the user
// EXISTS with the right algorithms and says plainly it cannot confirm the keys —
// it therefore REWRITES the user on every apply, because a rotation that was a
// silent no-op would leave the collector failing against an identity every screen
// calls conformant.
//
// Here the phrase itself is comparable, so the opposite discipline is the correct
// one: an identity whose name, group, algorithm AND passphrase all match is left
// ALONE, and a rotation is detected as a difference rather than papered over by
// an unconditional rewrite. The verification is strictly stronger and the fleet
// churn strictly lower.
//
// The price is stated once, out loud: EVERY "show running-config" on this range
// carries every v3 passphrase in clear through memory, which was already true
// before this file existed (ApplySNMP reads the whole configuration for the
// community). Nothing here logs it, transcribes it or puts it in a transcript
// step — [snmpV3UserCmd] returns a redacted twin for exactly that reason.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

const (
	showSNMPUserCmd  = "show snmp user"
	showSNMPGroupCmd = "show snmp group"
	// defaultV3Group is used when the caller names none. One group per fleet is
	// enough: the group carries the security level, not the identity.
	defaultV3Group = "meshcraft"
	// v3GroupSecurity is the security model this connector writes and demands on
	// re-read. "v3 priv" would authenticate the user and authorise it for
	// nothing (authorizationError, 2026-08-10).
	v3GroupSecurity = "v3 auth"
)

// snmpV3AuthAlgs are the algorithms MEASURED on this firmware, in meshcraft's
// canonical spelling. Both were accepted by the 2026-08-12 ladder and read back
// by "show snmp user" as "MD5" and "SHA".
//
// Nothing above SHA-1 exists here: "sha256" and "sha-256" were both answered
// "Unknown command". That is the whole reason this range can never be
// conformant to the SHA-256/AES-128 floor, and saying it in the capability is
// what puts it on the operator's screen instead of in a surprise.
func snmpV3AuthAlgs() []string { return []string{"md5", "sha"} }

// snmpV3AuthKeyword maps a requested algorithm onto this CLI's keyword, and
// REFUSES anything else BY NAME rather than picking the closest.
//
// Rounding SHA-256 down to SHA-1 would give working monitoring and false
// compliance, which is worse than a refusal the operator can read.
func snmpV3AuthKeyword(alg string) (string, error) {
	switch normalizeAlg(alg) {
	case "md5":
		return "md5", nil
	case "sha", "sha1":
		return "sha", nil
	case "":
		return "", fmt.Errorf("no SNMPv3 authentication algorithm requested: this " +
			"platform has no unauthenticated user form (\"snmp-server user … v3 noauth\" " +
			"is answered \"Unknown command\", measured 2026-08-12), so there is nothing " +
			"to write")
	}
	return "", fmt.Errorf("SNMPv3 authentication algorithm %q is not available on the "+
		"FS S3400: this firmware accepts \"md5\" and \"sha\" (SHA-1) and answers "+
		"\"Unknown command\" to everything above — \"sha256\" and \"sha-256\" were both "+
		"measured refused on Version 2.0.2J, 2026-08-12. Setting SHA-1 where %q was asked "+
		"for would give working monitoring and false compliance", alg, alg)
}

// normalizeAlg lowercases and drops separators, so "SHA-1" and "sha1" are one
// thing. Deliberately local: internal/snmpv3 is the decision layer and connectors
// do not import it.
func normalizeAlg(a string) string {
	var r = strings.NewReplacer("-", "", "_", "", " ", "")
	return r.Replace(strings.ToLower(strings.TrimSpace(a)))
}

// refuseSNMPv3Priv is THE refusal this range exists to make.
//
// It fires on either half — an algorithm named, or a passphrase supplied — and
// both matter. A caller that names "des" is asking for something the firmware
// has no word for; a caller that supplies [SNMPIdentity.PrivSecret] without
// naming an algorithm is handing over a secret this connector would have to drop
// on the floor. Accepting either and writing the authNoPriv line anyway is the
// silent downgrade the whole decision was taken to prevent.
//
// The message quotes the DEVICE, not the connector's opinion: "Unknown
// command" is what the CLI answered to all four privacy forms, and an operator
// reading this needs to know the refusal is the firmware's and no configuration
// change will lift it.
func refuseSNMPv3Priv(want SNMPIdentity) error {
	if strings.TrimSpace(want.PrivAlg) == "" && want.PrivSecret == "" {
		return nil
	}
	var asked = strings.TrimSpace(want.PrivAlg)
	if asked == "" {
		asked = "an encryption passphrase with no algorithm named"
	}
	return driver.NotSupported("fs-s3400", "configure SNMPv3 privacy ("+asked+") on this device",
		"the FS S3400 CLI has NO privacy form for an SNMPv3 user. Every candidate was "+
			"sent whole to poefs3400lab on Version 2.0.2J (2026-08-11) and every one was "+
			"answered \"Unknown command\": \"… v3 auth sha <P> priv des <P2>\", "+
			"\"… priv aes <P2>\", \"… v3 encrypted auth sha <P> priv des <P2>\" and "+
			"\"… v3 auth sha <P> priv <P2>\". There is no slot for a second passphrase, "+
			"so meshcraft would have to accept the encryption passphrase and silently "+
			"DISCARD it — and no re-read could catch that, since the configuration would "+
			"show exactly what was typed. This range is declared authNoPriv "+
			"(SNMPv3Priv empty) and the identity is REFUSED rather than quietly "+
			"downgraded to it")
}

// snmpV3GroupCmd is the group line. The tail is EMPTY on purpose: measured
// 2026-08-11, "snmp-server group <n> v3 priv" and nothing else is accepted and
// the device fills "read: default" itself, and 2026-08-12 confirmed the same
// shape at "auth". Naming a view here would bind the group to a view this
// connector never creates.
func snmpV3GroupCmd(group string) string {
	return "snmp-server group " + group + " " + v3GroupSecurity
}

// snmpV3UserCmd returns the user line AND a redacted twin for the transcript.
//
// Two return values rather than one, for the same reason the EOS connector does
// it: the line carries a passphrase in clear, and a transcript is read, copied
// into tickets and pasted into chat.
func snmpV3UserCmd(user, group, authKw, phrase string) (string, string) {
	var head = "snmp-server user " + user + " " + group + " v3 auth " + authKw + " "
	return head + phrase, head + "<passphrase>"
}

// --- the two reads ---

// snmpUser is one block of "show snmp user".
type snmpUser struct {
	Name     string
	EngineID string
	Auth     string
	Group    string
}

// snmpGroup is one block of "show snmp group".
type snmpGroup struct {
	Name          string
	SecurityModel string
	Read          string
}

// parseSNMPUsers reads the blocks "show snmp user" prints.
//
// CAPTURED SHAPE (testdata/show_snmp_user_authnopriv.txt, 2026-08-12) — blank-line
// separated blocks of "Key: value", with a trailing TAB after the group name
// and a trailing space after the protocol, both of which are trimmed here:
//
//	User name: mcau1
//	Engine ID: 8000cda203649d99227b95
//	storage-type: nonvolatile
//	Rowstatus: active
//	Authentication Protocol: SHA
//	Group-name: mcagA
//
// With NOTHING configured the command prints no block at all — not a header, not
// an empty table (measured 2026-08-11) — so zero users is a legitimate reading and
// never an error.
func parseSNMPUsers(out string) []snmpUser {
	var users []snmpUser
	var cur snmpUser
	var open bool
	var line string
	for _, line = range strings.Split(out, "\n") {
		var key, val, ok = splitSNMPField(line)
		if !ok {
			continue
		}
		switch key {
		case "user name":
			if open {
				users = append(users, cur)
			}
			cur, open = snmpUser{Name: val}, true
		case "engine id":
			cur.EngineID = val
		case "authentication protocol":
			cur.Auth = val
		case "group-name":
			cur.Group = val
		}
	}
	if open {
		users = append(users, cur)
	}
	return users
}

// parseSNMPGroups reads the blocks "show snmp group" prints.
//
// CAPTURED SHAPE (testdata/show_snmp_group_auth.txt, 2026-08-12):
//
//	groupname: mcagA
//	security model: v3 auth
//	read: default
//	write: <no writeview specified>
//	notify: <no notifyview specified>
func parseSNMPGroups(out string) []snmpGroup {
	var groups []snmpGroup
	var cur snmpGroup
	var open bool
	var line string
	for _, line = range strings.Split(out, "\n") {
		var key, val, ok = splitSNMPField(line)
		if !ok {
			continue
		}
		switch key {
		case "groupname":
			if open {
				groups = append(groups, cur)
			}
			cur, open = snmpGroup{Name: val}, true
		case "security model":
			cur.SecurityModel = val
		case "read":
			cur.Read = val
		}
	}
	if open {
		groups = append(groups, cur)
	}
	return groups
}

// splitSNMPField splits "Key: value", lowercasing the key and trimming the
// tabs and trailing spaces this firmware pads its values with.
//
// The echoed command and the prompt come through [cliConn.runPaged] on this range
// and must not be mistaken for fields; neither carries a colon, so both fall out
// here. A line whose value is empty is refused too: "notify:" with nothing after
// it would otherwise open a block.
func splitSNMPField(line string) (string, string, bool) {
	var i = strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	var key = strings.ToLower(strings.TrimSpace(line[:i]))
	var val = strings.Trim(strings.TrimSpace(line[i+1:]), " \t")
	if key == "" || val == "" {
		return "", "", false
	}
	return key, val, true
}

func findSNMPUser(users []snmpUser, name string) (snmpUser, bool) {
	var u snmpUser
	for _, u = range users {
		if u.Name == name {
			return u, true
		}
	}
	return snmpUser{}, false
}

func findSNMPGroup(groups []snmpGroup, name string) (snmpGroup, bool) {
	var g snmpGroup
	for _, g = range groups {
		if g.Name == name {
			return g, true
		}
	}
	return snmpGroup{}, false
}

// parseSNMPv3UserLine reads a "snmp-server user …" line out of the running
// configuration and returns the group, the algorithm and THE PASSPHRASE.
//
// CAPTURED FORMS, 2026-08-12 — note the encryption-type "0" this firmware
// INSERTS between the algorithm and the phrase, and the optional "encrypted"
// keyword which it keeps if it was typed:
//
//	snmp-server user mcau1 mcagA v3 auth sha 0 <phrase>
//	snmp-server user mcau3 mcagA v3 encrypted auth sha 0 <phrase>
//
// Both are read, because a device may carry a user somebody else wrote in the
// "encrypted" form, and reporting that user as absent would make this connector
// rewrite an identity it had actually found.
func parseSNMPv3UserLine(line string) (name, group, alg, phrase string, ok bool) {
	var f = strings.Fields(strings.TrimSpace(line))
	if len(f) < 8 || f[0] != "snmp-server" || f[1] != "user" {
		return "", "", "", "", false
	}
	name, group = f[2], f[3]
	if f[4] != "v3" {
		return "", "", "", "", false
	}
	var rest = f[5:]
	if rest[0] == "encrypted" {
		rest = rest[1:]
	}
	// "auth <alg> <enc-type> <phrase>" — four tokens, and the encryption type is
	// not assumed to be "0": it is skipped as a positional field, which is what
	// it is.
	if len(rest) < 4 || rest[0] != "auth" {
		return "", "", "", "", false
	}
	return name, group, rest[1], rest[3], true
}

// findSNMPv3UserInConfig returns what the running configuration stores for a user.
func findSNMPv3UserInConfig(cfg, user string) (group, alg, phrase string, ok bool) {
	var line string
	for _, line = range strings.Split(cfg, "\n") {
		var n, g, a, p, parsed = parseSNMPv3UserLine(line)
		if parsed && n == user {
			return g, a, p, true
		}
	}
	return "", "", "", false
}

// --- the applier ---

// applySNMPv3 writes the group and the user, then CONFIRMS BOTH.
//
// # What is checked, and why each check is a gate rather than a note
//
//   - THE GROUP EXISTS AND IS AT "v3 auth" — "show snmp group". Hard gate,
//     and the 2026-08-12 measurement is the argument: this firmware ACCEPTS a user
//     naming a group that does not exist and annotates NOTHING, so the user read
//     cannot answer this question. A group that is missing, or that exists at
//     "v3 priv", leaves an identity that authenticates and is authorised for
//     nothing — the collector connects, walks, and gets an empty result that looks
//     exactly like a dead device;
//   - THE USER EXISTS, IN THAT GROUP, WITH THAT ALGORITHM — "show snmp user".
//     Hard gate, the ordinary standard of every write in this connector;
//   - THE PASSPHRASE IS THE ONE ASKED FOR — "show running-config". Hard gate WHEN
//     THE LINE IS READABLE, a note when it is not. That asymmetry is the same one
//     the EOS connector makes about its group read: "the device told me something
//     else" is evidence of a failed write, "I could not ask" is not, and failing an
//     apply on a silent secondary read would throw away a user write confirmed one
//     line earlier.
//
// # Why an already-conformant identity is LEFT ALONE here
//
// The EOS connector rewrites its v3 user unconditionally, and is right to: its
// keys are invisible, so a match on name and algorithms cannot distinguish a
// current passphrase from a rotated one, and skipping would make every rotation a
// silent no-op. That argument does not transfer, because here the passphrase
// ITSELF is compared. A device whose stored phrase equals the wanted one is
// genuinely conformant, on complete evidence, and rewriting it would churn the
// running configuration of every S3400 in the fleet on every pass — on a platform
// where each write is a chance to err.
func (c *Client) applySNMPv3(ctx context.Context, want SNMPIdentity,
	authKw string, out *SNMPResult) (bool, error) {

	// --- what the device carries now. The full configuration is read rather
	// than a "| include" filter, which is erratic on this firmware (cf.
	// ApplySNMP).
	var cfg, cfgErr = c.cli.runPaged(ctx, "show running-config", showRunTimeout)
	var haveGroup, haveAlg, havePhrase string
	var haveUser bool
	if cfgErr == nil && strings.TrimSpace(cfg) != "" {
		haveGroup, haveAlg, havePhrase, haveUser = findSNMPv3UserInConfig(cfg, want.User)
	}

	var groups, gerr = c.readSNMPGroups(ctx)
	var group, groupOK = findSNMPGroup(groups, want.Group)
	var groupRight = gerr == nil && groupOK &&
		strings.EqualFold(strings.TrimSpace(group.SecurityModel), v3GroupSecurity)

	// --- already conformant? Only on COMPLETE evidence: the group read had to
	// answer, the user line had to be found, and all four fields had to match.
	if groupRight && haveUser && haveGroup == want.Group &&
		normalizeAlg(haveAlg) == normalizeAlg(authKw) && havePhrase == want.AuthSecret {
		out.Enabled = true
		c.step(true, "SNMPv3 identity already in place",
			fmt.Sprintf("user %q in group %q, %s — passphrase compared against the "+
				"running configuration, nothing rewritten", want.User, want.Group, authKw))
		return false, nil
	}

	var cmds []string
	var shown []string
	if !groupRight {
		cmds = append(cmds, snmpV3GroupCmd(want.Group))
		shown = append(shown, snmpV3GroupCmd(want.Group))
	}
	var userCmd, userShown = snmpV3UserCmd(want.User, want.Group, authKw, want.AuthSecret)
	cmds = append(cmds, userCmd)
	shown = append(shown, userShown)

	var err = c.configBlock(ctx, cmds)
	if err != nil {
		c.step(false, "SNMPv3 identity refused", firstLine(err.Error()))
		return false, fmt.Errorf("SNMPv3 identity on %s: %w", c.host, err)
	}

	// --- nothing is taken as written on the strength of a return code.
	err = c.confirmSNMPv3Group(ctx, want.Group, out)
	if err != nil {
		return false, err
	}

	var users []snmpUser
	users, err = c.readSNMPUsers(ctx)
	if err != nil {
		c.step(false, "SNMPv3 identity SENT and NOT confirmed", firstLine(err.Error()))
		return false, fmt.Errorf("the SNMPv3 group and user were accepted by the CLI and "+
			"NOT confirmed: \"%s\" did not answer (%v). Nothing is reported as written — a "+
			"return code is not an observation — but the commands did go out, so re-read the "+
			"device before deciding this was a failed write rather than a failed "+
			"verification", showSNMPUserCmd, err)
	}
	var got, found = findSNMPUser(users, want.User)
	if !found {
		c.step(false, "SNMPv3 user absent after write",
			"the CLI accepted the command and \""+showSNMPUserCmd+"\" does not list it")
		return false, fmt.Errorf("SNMPv3 user %q is not listed by \"%s\" after a write the "+
			"CLI accepted", want.User, showSNMPUserCmd)
	}
	if got.Group != want.Group {
		c.step(false, "SNMPv3 user in the wrong group", got.Group)
		return false, fmt.Errorf("SNMPv3 user %q was written into group %q and not %q: it "+
			"would authenticate and be authorised by a group meshcraft did not configure",
			want.User, got.Group, want.Group)
	}
	if normalizeAlg(got.Auth) != normalizeAlg(authKw) {
		c.step(false, "SNMPv3 user carries another algorithm", got.Auth)
		return false, fmt.Errorf("SNMPv3 user %q reads back with authentication %q where "+
			"%q was written: the collector would fail to authenticate against an identity "+
			"this apply would otherwise call posed", want.User, got.Auth, authKw)
	}

	// --- the passphrase. Readable → gate. Unreadable → note.
	var after, aerr = c.cli.runPaged(ctx, "show running-config", showRunTimeout)
	var _, _, storedPhrase, storedOK = findSNMPv3UserInConfig(after, want.User)
	switch {
	case aerr != nil && strings.TrimSpace(after) == "":
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"the SNMPv3 user %q EXISTS with the right group and algorithm — \"%s\" proves "+
				"that much — but \"show running-config\" did not answer (%s), so the "+
				"PASSPHRASE is unverified on this run. If a walk authenticates against "+
				"nothing, that is where to look", want.User, showSNMPUserCmd,
			firstLine(aerr.Error())))
	case !storedOK:
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"the SNMPv3 user %q EXISTS with the right group and algorithm, and its "+
				"\"snmp-server user\" line could not be read back out of the running "+
				"configuration, so the PASSPHRASE is unverified on this run", want.User))
	case storedPhrase != want.AuthSecret:
		c.step(false, "SNMPv3 passphrase not the one written",
			"the running configuration stores a different phrase for this user")
		return false, fmt.Errorf("SNMPv3 user %q was accepted and the running configuration "+
			"stores a DIFFERENT passphrase than the one written (lengths %d vs %d): the "+
			"device kept something other than what it was given, and the collector would "+
			"fail to authenticate", want.User, len(storedPhrase), len(want.AuthSecret))
	default:
		c.step(true, "SNMPv3 passphrase confirmed",
			"the running configuration stores exactly the phrase that was written")
	}

	out.UserSet = true
	out.GroupCreated = !groupRight
	// DEDUCED, by symmetry with the community half and on the same observation:
	// this platform has no separate enable switch and "show snmp" was already
	// counting input packets before any identity existed (captured 2026-08-03).
	out.Enabled = true
	c.step(true, "SNMPv3 user written and re-read", fmt.Sprintf(
		"user %q in group %q, %s, engine %s — sent: %s",
		got.Name, got.Group, got.Auth, orUnknownEngine(got.EngineID), strings.Join(shown, " ; ")))
	return true, nil
}

func orUnknownEngine(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(not printed)"
	}
	return s
}

// readSNMPUsers runs "show snmp user".
//
// An EMPTY answer is a legitimate state — no v3 user — and not an error: with
// nothing configured this command prints nothing at all (measured 2026-08-11). A
// transport failure with no output is an error, the same distinction every other
// read here makes.
func (c *Client) readSNMPUsers(ctx context.Context) ([]snmpUser, error) {
	var out, err = c.cli.runPaged(ctx, showSNMPUserCmd, cmdTimeout)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, err
	}
	return parseSNMPUsers(out), nil
}

// readSNMPGroups runs "show snmp group".
//
// THE ERROR RULE IS NOT [Client.readSNMPUsers]'s, and the difference carries
// weight: an error here becomes a FAILED APPLY, so a firmware that does not carry
// this command at all must not be read as "the device says the group authorises
// nothing". "Unknown command" is caught by [reCLIError] and returned as an
// error, which the caller turns into a refusal naming the read — not into a
// silent success.
func (c *Client) readSNMPGroups(ctx context.Context) ([]snmpGroup, error) {
	var out, err = c.cli.runPaged(ctx, showSNMPGroupCmd, cmdTimeout)
	if err != nil {
		return nil, err
	}
	return parseSNMPGroups(out), nil
}

// confirmSNMPv3Group is the HARD GATE the 2026-08-12 measurement made necessary.
//
// Three outcomes, three different things:
//
//   - the group EXISTS at "v3 auth" with a read view → confirmed;
//   - the group is MISSING, or exists at another security model, or authorises no
//     read → a FAILED WRITE. All three leave a user that authenticates and walks
//     back nothing, and reporting that as a posed identity would put a green
//     supervision status over a dead one;
//   - the read COULD NOT BE MADE → a failed apply too, and this is where this
//     connector is deliberately STRICTER than the EOS one. There the group read is
//     a secondary confirmation of a user whose own read already proved the group
//     resolved; here the user read proves nothing about the group, so a silent
//     "show snmp group" leaves the load-bearing fact unknown. An apply that
//     cannot see the group has not verified the identity, and says so.
func (c *Client) confirmSNMPv3Group(ctx context.Context, group string, out *SNMPResult) error {
	var groups, err = c.readSNMPGroups(ctx)
	if err != nil {
		c.step(false, "SNMPv3 group not confirmed", firstLine(err.Error()))
		return fmt.Errorf("\"%s\" did not answer (%v), so the v3 group %q is unverified. "+
			"On this firmware a user naming a group that does not exist is ACCEPTED and "+
			"flagged nowhere (measured 2026-08-12), so this read is the only thing that "+
			"tells an authorised identity from one that authenticates and walks back "+
			"nothing", showSNMPGroupCmd, err, group)
	}
	var got, found = findSNMPGroup(groups, group)
	if !found {
		c.step(false, "SNMPv3 group absent after write", group)
		return fmt.Errorf("the v3 group %q is not listed by \"%s\" after a group line the "+
			"CLI accepted: the user references a group that does not exist, which this "+
			"firmware accepts silently — it would authenticate and be authorised for "+
			"nothing", group, showSNMPGroupCmd)
	}
	var model = strings.TrimSpace(got.SecurityModel)
	if !strings.EqualFold(model, v3GroupSecurity) {
		c.step(false, "SNMPv3 group at the wrong security model", model)
		return fmt.Errorf("the v3 group %q reads back as \"%s\" where \"%s\" was written. "+
			"An authNoPriv user against a \"v3 priv\" group answers authorizationError "+
			"(measured 2026-08-10): it authenticates, walks, and returns nothing",
			group, model, v3GroupSecurity)
	}
	if strings.Contains(strings.ToLower(got.Read), "no readview") {
		c.step(false, "SNMPv3 group authorises no read", got.Read)
		return fmt.Errorf("the v3 group %q exists at \"%s\" and its read view is \"%s\": "+
			"the collector would authenticate and every walk would come back empty",
			group, model, got.Read)
	}
	if got.Read != "default" {
		// A NAMED view means somebody else configured this group, and neither
		// available answer is honest — this connector never names one and cannot
		// resolve what it contains.
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"the v3 group %q authorises the read view %q, which meshcraft did not create "+
				"and cannot resolve: whether the collector sees the OIDs it polls depends "+
				"on that view's contents", group, got.Read))
		c.step(true, "SNMPv3 group re-read, read authority UNRESOLVED", got.Read)
		return nil
	}
	c.step(true, "SNMPv3 group written and re-read", fmt.Sprintf(
		"group %q, %s — read view %s", got.Name, model, got.Read))
	return nil
}

const showRunTimeout = 120 * time.Second
