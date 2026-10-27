package eos

// SNMP identity — v2c AND, since 2026-08-10, v3.
//
// # WHY V3 WAS REFUSED, AND WHAT ANSWERED THE REFUSAL
//
// This file used to refuse SNMPv3 outright. The refusal had ONE reason and it was
// not prudence in general: a v3 user's keys were believed not to appear in
// "show running-config", so posing one would have been the single write this
// connector could not verify it had made.
//
// THAT PREMISE IS FALSE ON THIS PLATFORM, measured on core01.lab (EOS
// 4.28.13.1M) on 2026-08-10. The keys DO appear, as RFC 3414 LOCALIZED keys —
// a 40-hex SHA-1 digest and a 32-hex AES key — beside the engine ID they were
// localized against. So the permanent gap this file used to declare is narrower
// than it said: a connector that derived the expected localized key from
// passphrase + engine ID could verify the keys themselves. internal/snmpv3
// carries no derivation today, so that is an OPPORTUNITY, not a defect, and
// TODO.md records it.
//
// No key material is parsed, stored or transcribed: parseRunningConfig extracts
// only community/host/traps. It does pass through memory on every
// "show running-config", which is worth knowing before anything logs that
// output verbatim. The alternative — posing a v2c community where a v3 user was
// asked for — is worse than refusing: the caller gets working monitoring, false
// compliance, and a secret in the clear on the network that nobody chose.
//
// That objection was answered ON HARDWARE. core01.lab, EOS 4.28.13.1M,
// 2026-08-10: a temporary user was created at the CLI and "show snmp user"
// printed the block now pinned in testdata/show_snmp_user.txt and parsed by
// [parseSNMPUsers]. The KEYS are still not shown and never will be — that half of
// the objection stands for ever — but the NAME, the SECURITY MODEL, the
// AUTHENTICATION algorithm and the PRIVACY algorithm are, and that is exactly the
// standard every other write here is held to: a re-read that would catch a device
// which accepted the command and did something else.
//
// So what is verified is stated plainly, because the gap is real and permanent:
// this connector confirms that the user EXISTS, that it is v3, and that it carries
// the two algorithms asked for. It CANNOT confirm the keys, therefore it cannot
// confirm that the collector will authenticate. A poll that fails on a user this
// function confirmed means a passphrase mismatch, not a missing user, and that
// sentence is the whole diagnostic value of drawing the line here.
//
// # THE GROUP IS CREATED, AND THAT IS THE POINT OF THE WHOLE FILE
//
// The captured block ends "Group : meshprobe (not configured)". EOS ACCEPTS a
// user naming a group that does not exist, keeps the reference, and annotates it
// in the read. Such a user authenticates and is authorised for nothing: every walk
// comes back empty. A connector that wrote the user alone would report a v3
// identity posed and leave supervision silently dead.
//
// BOTH halves of the answer are taken, and neither would be enough on its own:
//
//   - the GROUP IS WRITTEN, before the user, so the reference resolves. Writing
//     only the user and refusing on the marker would be a refusal dressed as an
//     implementation — it would fail on every fresh device, for ever;
//   - the MARKER IS A HARD VERIFICATION GATE. Creating the group and trusting the
//     CLI's return code would be trusting a command form nobody has typed on an
//     Arista, which is the one thing this codebase never does. The marker is what
//     makes the group's creation OBSERVABLE through the very command the capture
//     established: if the group line was refused, or accepted and understood as
//     something else, the user's own "Group" field still says "(not configured)"
//     and this function fails instead of reporting success.
//
// That second point is worth stating once more, because it is the reason a deduced
// write form can ship at all here: the captured read verifies BOTH commands.
//
// # WHAT IS CAPTURED AND WHAT IS DEDUCED, per line
//
// CAPTURED: the output of "show snmp user" and every field name in it; that this
// platform prints "SHA" and "AES-128" for a user created with those
// algorithms; that a group can be referenced before it exists and is marked so.
//
// DEDUCED when they shipped. Each bullet says what, if anything, has confirmed it
// since:
//
//   - "snmp-server group <name> v3 priv" — the access-group line, with NO view
//     named (see below). NO LONGER A DEDUCTION as of 2026-08-10: the "show snmp
//     group" capture pinned below was taken on core01 on the group THIS LINE had
//     just created, so the form is read back accepted, at the level it asks for,
//     and understood as a group;
//   - "snmp-server user <name> <group> v3 auth sha <phrase> priv aes <phrase>" —
//     the user line, with "aes" as this platform's keyword for the AES-128 the
//     capture reads back;
//   - that re-issuing the user line REPLACES an existing user rather than being
//     refused. This is the deduction most likely to be wrong. Its failure mode is
//     a CLI refusal, caught by [reCLIError] and returned — loud, not silent. It is
//     deliberately not preceded by a "no snmp-server user" removal: a
//     delete-then-add whose second half fails would destroy working supervision to
//     protect against an error message.
//
// # WHAT THIS PLATFORM DOES NOT GET WRITTEN, said out loud
//
// A source restriction IS expressible on EOS — "snmp-server community S ro <acl>"
// binds the community to a standard access list — and it is NOT written here,
// because writing an ACL name without creating the ACL binds the community to an
// empty list and silences the agent completely. The bound the caller asked for
// therefore lands in Skipped, which is the honest answer: the identity answers
// anything that reaches the management VLAN.
//
// The group's VIEWS are refused for the identical reason, and it is the same
// mistake one level down: naming a read view that does not exist would create a
// group authorised for nothing, which is precisely the failure the "(not
// configured)" gate exists to catch. The group is therefore written with no view
// at all — it takes the platform's default read view, and never a write view.
//
// # THE SKIPPED LINE BECAME A CHECK, on 2026-08-10
//
// This header used to end "“show snmp group” has never been read on an Arista
// here; when it has, that Skipped line becomes a check". It has been read, on
// core01.lab (EOS 4.28.13.1M), on the group this connector's own deduced line
// created, and the block is pinned in testdata/show_snmp_group.txt and parsed by
// [parseSNMPGroups]. It answers the exact question the note was hedging:
//
//	Read view      : <no read view specified (default: all included)>
//
// The platform states in its own words that an unset read view falls back to ALL
// INCLUDED. The group written with no view therefore DOES authorise reads, and
// [verifySNMPv3Group] now asserts it instead of Skipped confessing it. A group the
// device reports as authorising NOTHING is a failed write on the same terms as the
// "(not configured)" marker — see that function for the argument, which is the
// same one: an identity that authenticates and can read nothing is supervision
// reported as posed and silently dead.
//
// What has NOT been captured is what a group with an EXPLICITLY NAMED read view
// prints. This connector never names one, so that shape can only come from a group
// somebody else configured; [verifySNMPv3Group] treats it as UNRESOLVED — a note,
// not a confirmation and not a refusal — and says why.

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
	"meshcraft/internal/snmpv3"
)

const (
	// showSNMPUserCmd is the command the capture of 2026-08-10 was taken with,
	// and the ONLY read that can confirm a v3 user on this platform.
	showSNMPUserCmd = "show snmp user"
	// showSNMPGroupCmd is the command the group capture of 2026-08-10 was taken
	// with, and the only read on this platform that says what a group may READ.
	showSNMPGroupCmd = "show snmp group"
	// defaultV3Group names the access group meshcraft creates for its own v3
	// users when the caller names none.
	//
	// The SAME NAME as the Instant On connector's (instanton/snmp.go), on
	// purpose: an operator looking at two switches from different vendors in the
	// same rack should see the same group, and a dedicated group is what keeps a
	// v3 user out of a factory group carrying a WRITE view.
	defaultV3Group = "meshcraft-ro"
	// secretMask replaces a passphrase before a command reaches the transcript.
	// Same mask as [redactSecrets], which does the same job for account secrets.
	secretMask = "•••"
	// snmpV3GroupSecurity is the security model AND level the group is written
	// with — and, since the capture of 2026-08-10, the exact string "show snmp
	// group" prints back in its "Security model" field for such a group.
	//
	// ONE constant for the write and the verification, for the reason
	// [snmpV3AuthAlgs] is one source for the declaration and the applier: a group
	// created at one level and checked against another would confirm an access
	// entry the user never goes through.
	snmpV3GroupSecurity = "v3 priv"
)

// snmpV3AuthAlgs / snmpV3PrivAlgs are the algorithms this connector DECLARES
// (cf. Capabilities in eos.go) and the algorithms it WILL WRITE — one source for
// both, so the contract and the applier cannot drift apart.
//
// The lists are short because they are the CAPTURED ones and nothing else. EOS
// certainly carries more; "certainly" is not a capture, and the day a stronger
// pair is created on hardware and read back through [parseSNMPUsers], both halves
// widen together by editing these two functions.
//
// The consequence is deliberate and visible: SHA/AES-128 sits below the default
// policy floor (SHA-256/AES-128), so snmpprov reports every Arista as monitored
// and NON-CONFORMANT, with the reason on screen. That is the truthful state — the
// alternative is claiming a floor nobody has seen this platform meet.
//
// They are functions rather than package slices so a caller of Capabilities
// cannot reach in and mutate the connector's declaration.
func snmpV3AuthAlgs() []string { return []string{"sha"} }
func snmpV3PrivAlgs() []string { return []string{"aes128"} }

// snmpV3AuthKeyword translates an algorithm name into the word the CLI takes.
//
// An unknown algorithm is REFUSED and never rounded to the nearest one: posing
// MD5 where SHA was asked for produces monitoring that works and a compliance
// claim that is false, which is the same trade the v2c-instead-of-v3 substitution
// would have made.
func snmpV3AuthKeyword(alg string) (string, error) {
	if snmpv3.Normalize(alg) == "sha" {
		return "sha", nil
	}
	var asked = "authentication " + orUnknown(alg)
	if snmpv3.Normalize(alg) == "" || snmpv3.Normalize(alg) == "none" {
		// "noAuthNoPriv" exists in the standard and protects nothing. Refusing it
		// is the same rule snmpv3.Negotiate follows by never offering it.
		asked = "no authentication at all"
	}
	return "", driver.NotSupported("arista-eos",
		"write an SNMPv3 user with "+asked,
		"this connector writes the algorithms it has SEEN this platform print back — SHA "+
			"and AES-128, from the \"show snmp user\" capture of 2026-08-10 on core01. EOS "+
			"very probably carries more, and probably is not a capture: a write whose "+
			"read-back word is unknown cannot be verified, and an unverifiable v3 write is "+
			"what this connector refused entirely until that capture existed. Create a user "+
			"with this algorithm at the CLI, read \"show snmp user\", and both the "+
			"capability and this refusal widen together")
}

// snmpV3PrivKeyword does the same for encryption, and refuses NO ENCRYPTION.
//
// "authNoPriv" exists in the standard, and it leaves every counter — and the
// walk that reads them — in the clear. It is refused for the reason
// snmpv3.Negotiate never offers "noAuthNoPriv": a v3 identity that protects
// nothing invites the belief that it does. The narrower reason applies too, and is
// the one this connector can act on: no capture shows what "show snmp user"
// prints in the Privacy field of a user without encryption, so such a user could
// not be verified.
func snmpV3PrivKeyword(alg string) (string, error) {
	if snmpv3.Normalize(alg) == "aes128" {
		return "aes", nil
	}
	var asked = orUnknown(alg)
	if snmpv3.Normalize(alg) == "" || snmpv3.Normalize(alg) == "none" {
		asked = "no encryption"
	}
	return "", driver.NotSupported("arista-eos",
		"write an SNMPv3 user with "+asked,
		"this connector writes AES-128 and nothing else on this platform: it is the one "+
			"encryption \"show snmp user\" has been seen to print back (core01, 2026-08-10), "+
			"and a v3 write it cannot read back is a write it cannot verify. An identity "+
			"WITHOUT encryption is refused outright and not merely unverified — it walks the "+
			"whole MIB in the clear while presenting itself as v3")
}

// snmpV3GroupCmd builds the access-group line. DEDUCED (cf. the file header).
//
// "v3 priv" is the security level required of the group's members —
// authenticated AND encrypted — and matches exactly what the user line writes. No
// read view and no write view are named: see the header for why naming one would
// reproduce the ACL trap one level down, and why a write view is never written on
// any platform here.
func snmpV3GroupCmd(group string) string {
	return "snmp-server group " + group + " " + snmpV3GroupSecurity
}

// snmpV3UserCmd builds the user line AND its redacted twin, from the same parts.
//
// TWO RETURN VALUES rather than a redacting pass over the finished string: the
// transcript is stored with the pipeline run and outlives the session, so the
// passphrases must never be in a string that reaches it. Building the masked form
// from the same pieces means no regexp stands between the secret and the record —
// the mask cannot miss what it was never given.
func snmpV3UserCmd(user, group, authKw, privKw, authSecret, privSecret string) (cmd, redacted string) {
	var head = "snmp-server user " + user + " " + group + " v3 auth " + authKw + " "
	var mid = " priv " + privKw + " "
	return head + authSecret + mid + privSecret, head + secretMask + mid + secretMask
}

// checkV3Secrets refuses the two passphrase mistakes that cost the most.
//
// The SECOND one is the subtle one and it is why [driver.SNMPIdentity] carries two
// distinct fields: reusing one phrase on both sides feeds exactly the same value
// into both RFC 3414 derivations, so the encryption key becomes a prefix of the
// authentication key and the encryption adds nothing at all. It is a v3 identity
// that reads as complete everywhere and protects half of what it claims.
func checkV3Secrets(want SNMPIdentity) error {
	if want.AuthSecret == "" {
		return fmt.Errorf("an SNMPv3 user without an authentication passphrase protects " +
			"nothing: meshcraft refuses to write it")
	}
	if want.PrivSecret == "" {
		return fmt.Errorf("no encryption passphrase for SNMPv3 user %q: this connector "+
			"writes authenticated AND encrypted users only", want.User)
	}
	if want.AuthSecret == want.PrivSecret {
		return fmt.Errorf("the authentication and encryption passphrases of SNMPv3 user %q "+
			"are the SAME: both RFC 3414 derivations then consume the same value, the "+
			"encryption key is only a prefix of the authentication key, and the encryption "+
			"adds nothing. Two distinct phrases, or no v3", want.User)
	}
	return nil
}

// ApplySNMP writes the SNMP identity — the v2c community, the v3 user, or both —
// verifies EACH by re-read, then PERSISTS. An identity that disappears on reboot
// silently kills supervision, which is exactly what this write exists to prevent.
//
// The two halves are INDEPENDENT, as the contract requires: an empty field means
// "nothing to do here" and never "remove". Nothing is ever taken as written on the
// strength of a return code — the community is confirmed against "show
// running-config" and the user against "show snmp user", and neither result
// flag is set before its own re-read has agreed.
func (c *Client) ApplySNMP(ctx context.Context, want SNMPIdentity) (SNMPResult, error) {
	var out = SNMPResult{Skipped: []string{}}

	// Everything that can be refused is refused BEFORE the device is touched: a
	// half-written identity is worse than one that was never started.
	var authKw, privKw string
	var err error
	if want.User != "" {
		if want.Group == "" {
			want.Group = defaultV3Group
		}
		authKw, err = snmpV3AuthKeyword(want.AuthAlg)
		if err != nil {
			return out, err
		}
		privKw, err = snmpV3PrivKeyword(want.PrivAlg)
		if err != nil {
			return out, err
		}
		err = checkV3Secrets(want)
		if err != nil {
			return out, err
		}
	}
	if want.Community == "" && want.User == "" {
		out.Skipped = append(out.Skipped,
			"no v2c community and no v3 user in the intent: nothing was written "+
				"(an apply is not a reset)")
		return out, nil
	}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	if len(want.Sources) > 0 {
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"source restriction (%s) not applied: EOS expresses it as an access-list name — "+
				"on the community line for v2c, on the group for v3 — and writing that name "+
				"without creating the list would bind the identity to an empty list and "+
				"silence the agent. The identity answers any address that reaches the "+
				"management VLAN",
			strings.Join(want.Sources, ", ")))
	}

	var wrote bool
	if want.Community != "" {
		var cwrote bool
		cwrote, err = c.applySNMPCommunity(ctx, want, &out)
		if err != nil {
			return out, err
		}
		wrote = wrote || cwrote
	}
	if want.User != "" {
		var uwrote bool
		uwrote, err = c.applySNMPv3(ctx, want, authKw, privKw, &out)
		if err != nil {
			return out, err
		}
		wrote = wrote || uwrote
	}
	if !wrote {
		// Nothing changed on the device, so there is nothing to persist and the
		// flash is not worn for a no-op.
		return out, nil
	}

	var perr = c.save(ctx)
	if perr != nil {
		out.Skipped = append(out.Skipped,
			"identity applied but NOT persisted: "+firstLine(perr.Error())+
				" — a reboot would silence monitoring")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", SaveCmd)
	return out, nil
}

// applySNMPCommunity writes and confirms the v2c half. Returns whether the device
// was actually written to.
func (c *Client) applySNMPCommunity(ctx context.Context, want SNMPIdentity,
	out *SNMPResult) (bool, error) {

	// Read first: idempotence avoids wearing the flash for nothing.
	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return false, fmt.Errorf("pre-write read failed: %v", err)
	}
	var rc = parseRunningConfig(runOut, c.host)
	// On EOS the agent is off until an identity exists, and on as soon as one
	// does — there is no separate enable switch.
	out.Enabled = len(rc.Communities) > 0

	if rc.Communities[want.Community] == "ro" {
		c.step(true, "SNMP already conforming",
			fmt.Sprintf("community %q present read-only", want.Community))
		out.Enabled = true
		return false, nil
	}

	// Lower case "ro" and not IOS's "RO": EOS prints the keyword back in
	// lower case, and the parser compares what it read. Sending the upper-case
	// form works — the CLI is case-insensitive here — and would make every
	// re-read look like a mismatch if the parser were ever tightened.
	err = c.runConfigBlock(ctx, []string{
		fmt.Sprintf("snmp-server community %s ro", want.Community),
	})
	if err != nil {
		c.step(false, "SNMP community not written", firstLine(err.Error()))
		return false, err
	}

	// Nothing is taken as written on the strength of a return code: re-read.
	runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return false, fmt.Errorf("post-write re-read failed: %v", err)
	}
	rc = parseRunningConfig(runOut, c.host)
	if rc.Communities[want.Community] != "ro" {
		c.step(false, "SNMP community absent after write",
			"the CLI accepted the command but the running configuration does not carry it")
		return false, fmt.Errorf("community %q not present after write", want.Community)
	}
	out.CommunitySet = true
	out.Enabled = true
	c.step(true, "SNMP community written and re-read",
		fmt.Sprintf("community %q read-only", want.Community))

	// A read-write community is not removed on the connector's own initiative:
	// cutting somebody else's monitoring to install our own would be the worst
	// possible service. Reported, not cleared.
	var name, right string
	for name, right = range rc.Communities {
		if name == want.Community || right != "rw" {
			continue
		}
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"community %q is READ-WRITE and is still present on the device — meshcraft does "+
				"not remove a community it did not set. Remove it by hand if it is not yours",
			name))
	}
	return true, nil
}

// readSNMPUsers runs "show snmp user" and parses it.
//
// An EMPTY answer is a legitimate state — no v3 user — and not an error: that is
// what the command printed on core01 once the probe user was removed. A transport
// failure with no output at all is an error, the same distinction every other read
// in this connector makes.
func (c *Client) readSNMPUsers(ctx context.Context) ([]snmpUser, error) {
	var out, err = c.cli.run(ctx, showSNMPUserCmd, cmdTimeout)
	if err != nil && out == "" {
		return nil, err
	}
	return parseSNMPUsers(out), nil
}

// readSNMPGroups runs "show snmp group" and parses it.
//
// THE ERROR RULE IS NOT [Client.readSNMPUsers]'s, and the difference is load-
// bearing. There it is "no output at all"; here it is "no group block came
// back", because the caller turns an error into a NOTE and a successful read that
// omits the group into a FAILED WRITE. A device that does not carry this command —
// an older release, a restricted role — answers "% Invalid input …", which
// [reCLIError] catches and which parses to zero groups. Judging that as "the
// device says the group authorises nothing" would fail an apply on a switch that
// never answered the question at all.
//
// A read that DID list groups is used even if [reCLIError] matched something in
// it: the blocks are the observation, and a false positive on the error regexp
// must not discard them.
func (c *Client) readSNMPGroups(ctx context.Context) ([]snmpGroup, error) {
	var out, err = c.cli.run(ctx, showSNMPGroupCmd, cmdTimeout)
	var groups = parseSNMPGroups(out)
	if err != nil && len(groups) == 0 {
		return nil, err
	}
	return groups, nil
}

// applySNMPv3 writes the group and the user, then CONFIRMS both — the user and the
// group's EXISTENCE against "show snmp user", the group's READ AUTHORITY against
// "show snmp group". Returns whether the device was written to.
//
// # Why the user is rewritten even when it looks conforming
//
// Everywhere else in this connector a state that already matches is left alone,
// and here it deliberately is not. The reason is the permanent gap named in the
// file header: THE KEYS ARE INVISIBLE. A user whose name, model and algorithms all
// match may hold keys derived from a passphrase that has since been rotated, and
// no read on this platform can tell the two apart. Skipping the write on an
// algorithm match would make every rotation a silent no-op — the collector would
// keep failing to authenticate against an identity every screen calls conformant,
// which is a silent-success bug wearing the costume of idempotence.
//
// The cost is small and bounded: the user line is a full specification, so
// re-issuing it with the same passphrase derives the same keys and changes nothing
// on the device. And ApplySNMP is not on a loop — it runs at adoption, on an
// explicit re-push, and on the heal pass, which skips devices already reported
// "ok" (api/snmpheal.go).
//
// The GROUP is not rewritten when the device's own read says it is configured:
// there is no invisible half to a group, so the ordinary rule applies to it.
func (c *Client) applySNMPv3(ctx context.Context, want SNMPIdentity,
	authKw, privKw string, out *SNMPResult) (bool, error) {

	var before, err = c.readSNMPUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("pre-write \"%s\" failed: %v", showSNMPUserCmd, err)
	}

	// The group's existence is read THROUGH a user that references it — that is
	// the only signal "show snmp user" carries about groups. With no such user,
	// the group is written: on a device that already has it, the line is a no-op,
	// and the alternative is asking a question this command cannot answer.
	var had, hadUser = findSNMPUser(before, want.User)
	var groupKnown = hadUser && had.Group == want.Group && had.GroupConfigured

	var cmds []string
	var shown []string
	if !groupKnown {
		cmds = append(cmds, snmpV3GroupCmd(want.Group))
		shown = append(shown, snmpV3GroupCmd(want.Group))
	}
	var userCmd, userShown = snmpV3UserCmd(
		want.User, want.Group, authKw, privKw, want.AuthSecret, want.PrivSecret)
	cmds = append(cmds, userCmd)
	shown = append(shown, userShown)

	err = c.runConfigBlock(ctx, cmds)
	if err != nil {
		c.step(false, "SNMPv3 identity not written", firstLine(err.Error()))
		return false, err
	}

	// Nothing is taken as written on the strength of a return code: re-read.
	var after []snmpUser
	after, err = c.readSNMPUsers(ctx)
	if err != nil {
		c.step(false, "SNMPv3 identity SENT and NOT confirmed", firstLine(err.Error()))
		return false, fmt.Errorf("the SNMPv3 group and user were accepted by the CLI and "+
			"NOT confirmed: \"%s\" did not answer (%v). Nothing is reported as written — "+
			"a return code is not an observation — but the commands did go out, so re-read "+
			"the device before deciding this was a failed write rather than a failed "+
			"verification", showSNMPUserCmd, err)
	}

	err = verifySNMPv3User(after, want)
	if err != nil {
		c.step(false, "SNMPv3 identity not confirmed", firstLine(err.Error()))
		return false, err
	}

	var got, _ = findSNMPUser(after, want.User)
	c.step(true, "SNMPv3 user written and re-read", fmt.Sprintf(
		"user %q in group %q, %s/%s, engine %s — sent: %s",
		got.Name, got.Group, got.Auth, got.Priv, orUnknown(got.EngineID),
		strings.Join(shown, " ; ")))

	// The group EXISTS — the "(not configured)" gate above proves that much. What
	// it may READ is the second question, and since 2026-08-10 this connector
	// answers it instead of confessing it. A failure here is a failed write and
	// lands BEFORE the result flags are set, exactly like the user's own gate.
	err = c.confirmSNMPv3GroupReads(ctx, want.Group, out)
	if err != nil {
		return false, err
	}

	out.UserSet = true
	out.GroupCreated = !groupKnown
	// DEDUCED, by symmetry with the community half: EOS has no separate enable
	// switch, and an agent that answers a v2c community as soon as one exists
	// answers a v3 user on the same terms. Reporting the agent OFF right after
	// confirming a user on it would put a false "supervision is dead" on screen.
	out.Enabled = true
	return true, nil
}

// confirmSNMPv3GroupReads re-reads "show snmp group" and decides whether the
// group the user was put in authorises reads.
//
// THE THREE OUTCOMES ARE THREE DIFFERENT THINGS and the whole value of this
// function is not collapsing them:
//
//   - the device SAID the group authorises reads → confirmed, a transcript step,
//     nothing in Skipped. This is the captured case and the one the connector
//     writes;
//   - the device SAID it authorises nothing → an ERROR. [verifySNMPv3Group] argues
//     it, and so does the shape no capture covers, which lands here too: an unset
//     view whose default this connector cannot read is not proof of authority;
//   - the device NAMED a read view → a NOTE. Unresolvable, not absent — again
//     [verifySNMPv3Group];
//   - the device COULD NOT BE ASKED → a NOTE. "I could not ask" is not "I was told
//     nothing is authorised", and only the second is evidence of a failed write. A
//     read that does not answer leaves exactly the gap this connector carried
//     before the capture existed, and it lands where that gap always lived:
//     Skipped. Failing the apply on a silent secondary read would throw away a user
//     write confirmed one line earlier, on the strength of no observation at all.
func (c *Client) confirmSNMPv3GroupReads(ctx context.Context, group string,
	out *SNMPResult) error {

	var groups, err = c.readSNMPGroups(ctx)
	if err != nil {
		c.step(false, "SNMPv3 group authority not read", firstLine(err.Error()))
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"the v3 group %q EXISTS — the user's own re-read proves it — but \"%s\" did not "+
				"answer (%s), so what the group may READ is unverified on this run. It was "+
				"written with no view named, which on this platform takes the default read "+
				"view. If a walk authenticates and returns nothing, that is where to look",
			group, showSNMPGroupCmd, firstLine(err.Error())))
		return nil
	}

	var note, verr = verifySNMPv3Group(groups, group)
	if verr != nil {
		c.step(false, "SNMPv3 group authorises no read", firstLine(verr.Error()))
		return verr
	}
	if note != "" {
		c.step(true, "SNMPv3 group re-read, read authority UNRESOLVED", note)
		out.Skipped = append(out.Skipped, note)
		return nil
	}

	var got, _ = findSNMPGroup(groups, group, snmpV3GroupSecurity)
	c.step(true, "SNMPv3 group re-read and its read authority confirmed", fmt.Sprintf(
		"group %q, %s — read view %s, write view %s",
		got.Name, got.SecurityModel, got.Read.Raw, got.Write.Raw))
	return nil
}

// verifySNMPv3Group decides what the device's own words about the group mean.
// An empty note and no error is a group confirmed to authorise reads.
//
// # A group that authorises NOTHING is a FAILED WRITE, not a note
//
// This is the judgement the function exists to make, and it is settled by an
// argument this file has already made once. A user whose group is marked "(not
// configured)" authenticates and is authorised for nothing — every walk comes
// back empty — and [verifySNMPv3User] calls that a failed write rather than a
// posed identity. A group that EXISTS and whose read view authorises nothing has
// the identical consequence: the collector connects, authenticates, walks, and
// gets nothing. Only the mechanism differs. Reporting it as a success with a
// footnote would put a green identity on screen over supervision that is dead,
// which is the exact silent-success class this connector re-reads to avoid — and
// the footnote would be read by nobody until an outage went unnoticed.
//
// The cost of being wrong runs the safe way, too: the failure is loud, it quotes
// the device's own line, and NOTHING was destroyed — the user and the group are
// both still on the device, so an operator who disagrees can look and act. The
// opposite mistake leaves nothing to look at.
//
// # An explicitly NAMED read view is UNRESOLVED — neither yes nor no
//
// This connector never names a view. A named one therefore means SOMEBODY ELSE
// configured this group, and neither available answer is honest:
//
//   - calling it authorising is a guess in the direction of success. The view
//     might be a two-subtree restriction that excludes everything the collector
//     polls, and the connector would confirm read authority it never observed;
//   - calling it a failure is a guess in the direction of destruction. A named read
//     view is most plausibly a DELIBERATE, legitimate operator restriction — it is
//     better practice than the wide-open default this connector settles for — and
//     failing the apply would report a working identity as broken, on a device
//     where the write already went out and cannot be taken back.
//
// So it is reported as what it is: the group exists at the right security level,
// its read view is NAMED, and this connector cannot resolve what that view
// contains — "show snmp view" has never been run on an Arista here. The view's
// name goes into the note so the operator reads one command away from the answer.
// The rule that keeps this from becoming an excuse: UNRESOLVED is only ever
// reachable through a shape no capture covers, never through the shape the
// connector itself writes.
func verifySNMPv3Group(groups []snmpGroup, name string) (note string, err error) {
	var got, ok = findSNMPGroup(groups, name, snmpV3GroupSecurity)
	if !ok {
		var seen = snmpGroupModels(groups, name)
		if len(seen) > 0 {
			return "", fmt.Errorf("the v3 user's group %q is listed by \"%s\" only at security "+
				"level(s) %s, and not at %q: the group line was accepted and understood as "+
				"something else, and the user's own level does not go through those entries",
				name, showSNMPGroupCmd, strings.Join(seen, ", "), snmpV3GroupSecurity)
		}
		// "show snmp user" said the group IS configured and "show snmp group"
		// does not list it. The two reads contradict each other, and the one that
		// speaks about read authority is the one that decides: nothing here can say
		// this group authorises anything.
		return "", fmt.Errorf("the v3 group %q is not listed by \"%s\" although the user's own "+
			"re-read reports it configured. The two reads disagree, and nothing observed says "+
			"this group authorises any read — the identity would authenticate and walk empty",
			name, showSNMPGroupCmd)
	}

	switch got.Read.Authority {
	case viewAuthorityAll:
		return "", nil
	case viewAuthorityNamed:
		return fmt.Sprintf(
			"the v3 group %q exists at %s and its READ VIEW IS NAMED (%q) — this connector "+
				"never names one, so the group was configured by somebody else. What that view "+
				"includes is not knowable from \"%s\" and \"show snmp view\" has never been run "+
				"on this platform, so whether the collector can read anything is UNRESOLVED: "+
				"neither confirmed nor refused. If a walk authenticates and returns nothing, "+
				"that view is where to look",
			name, got.SecurityModel, got.Read.Name, showSNMPGroupCmd), nil
	}
	return "", fmt.Errorf("the v3 group %q exists at %s and its read view authorises NOTHING — "+
		"the device printed \"%s\" where the captured platform default reads \"… %s …\". A user "+
		"in this group authenticates and every walk comes back empty, which is supervision "+
		"reported as posed and silently dead. This is a failed write, not a note: the user and "+
		"the group are both still on the device, so what the switch said can be looked at",
		name, got.SecurityModel, got.Read.Raw, defaultAllIncludedMark)
}

// verifySNMPv3User compares what the device now reports against what was asked.
//
// EVERY field the capture carries is checked, and each failure names what the
// device said rather than only that it disagreed: on a command set deduced from
// documentation, the exact wording the switch used is the next survey's evidence.
//
// The "(not configured)" check is the LAST one and the most important. It is the
// difference between "the user is there" and "the user can read something", and
// without it this whole function would confirm an identity that authorises
// nothing. It is also the FIRST confirmation that the group command itself was
// understood — cf. the file header. [verifySNMPv3Group] is the second and goes one
// level deeper: this one says the group exists, that one says what it may read.
func verifySNMPv3User(users []snmpUser, want SNMPIdentity) error {
	var got, ok = findSNMPUser(users, want.User)
	if !ok {
		return fmt.Errorf("the CLI accepted the SNMPv3 user %q and \"%s\" does not list it: "+
			"write without effect, nothing will be collected", want.User, showSNMPUserCmd)
	}
	if !strings.EqualFold(strings.TrimSpace(got.SecurityModel), "v3") {
		return fmt.Errorf("SNMPv3 user %q was created under security model %q, not v3: the "+
			"device accepted the command and made something else", want.User,
			orUnknown(got.SecurityModel))
	}
	if snmpv3.Normalize(got.Auth) != snmpv3.Normalize(want.AuthAlg) {
		return fmt.Errorf("SNMPv3 user %q authenticates with %q where %q was asked for: "+
			"the keys are derived from that algorithm, so the collector would not "+
			"authenticate", want.User, orUnknown(got.Auth), want.AuthAlg)
	}
	if snmpv3.Normalize(got.Priv) != snmpv3.Normalize(want.PrivAlg) {
		return fmt.Errorf("SNMPv3 user %q encrypts with %q where %q was asked for: "+
			"an identity that is weaker than the one requested must never be reported as "+
			"the one requested", want.User, orUnknown(got.Priv), want.PrivAlg)
	}
	if got.Group != want.Group {
		return fmt.Errorf("SNMPv3 user %q landed in group %q instead of %q", want.User,
			orUnknown(got.Group), want.Group)
	}
	if !got.GroupConfigured {
		return fmt.Errorf("SNMPv3 user %q exists and its group %q is marked \"%s\" by the "+
			"device: EOS accepts a user naming a group that does not exist, and such a user "+
			"authenticates while being authorised for NOTHING — every walk comes back empty. "+
			"The group command was refused or understood as something else. This is reported "+
			"as a failed write, not as a posed identity", want.User, want.Group,
			notConfiguredMark)
	}
	return nil
}
