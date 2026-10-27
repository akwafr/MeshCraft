package s3400

// Adoption of the access contract (driver/connector.go) by the FS S3400
// connector. The structure follows the S3900 connector next door; the
// PLATFORM does not — see ssh.go for the pager, and parse.go for the five
// spellings this CLI has for one socket.
//
// # Two things this CLI does that the S3900's does not
//
//  1. SSH lands in USER mode. The prompt after login is "Switch>", where
//     "show version" itself answers "Unknown command" (captured
//     2026-08-03). Every session therefore runs "enable" first — which on
//     this platform asks for NO password, because the factory configuration
//     carries "aaa authentication enable default none". postLogin does it
//     once and records it; a device whose enable HAS been given a password
//     answers a "Password:" prompt, and that is handled rather than hung on.
//
//  2. There is no writable/unwritable dialect split like the S3900's, because
//     there is only one CLI in this family. What replaces it is the same
//     discipline: the connector recognizes the CLI from "show version", and
//     refuses to WRITE to a device whose version line it cannot read at all.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// Aliases (ALIASES, not definitions — cf. s3900/contract.go).
type (
	Step         = driver.Step
	Candidate    = driver.Candidate
	VLAN         = driver.VLAN
	PortState    = driver.PortState
	State        = driver.State
	PortPlan     = driver.PortPlan
	ApplyResult  = driver.ApplyResult
	SNMPIdentity = driver.SNMPIdentity
	SNMPResult   = driver.SNMPResult
	Identity     = driver.Identity
)

// Compile-time assertions: a signature drift shows up at build time, not in
// the field.
var (
	_ driver.Connector  = (*Driver)(nil)
	_ driver.Identifier = (*Driver)(nil)
	_ driver.PortNamer  = (*Driver)(nil)

	_ driver.Admin               = (*Client)(nil)
	_ driver.AccountManager      = (*Client)(nil)
	_ driver.SNMPConfigurator    = (*Client)(nil)
	_ driver.TrapPusher          = (*Client)(nil)
	_ driver.Persister           = (*Client)(nil)
	_ driver.MgmtAddrReader      = (*Client)(nil)
	_ driver.MgmtAddrWriter      = (*Client)(nil)
	_ driver.FaceplateReader     = (*Client)(nil)
	_ driver.SpanningTreeReader  = (*Client)(nil)
	_ driver.SpanningTreeApplier = (*Client)(nil)
	_ driver.LLDPReader          = (*Client)(nil)
	_ driver.OpticsReader        = (*Client)(nil)
	_ driver.IdentityReader      = (*Client)(nil)
)

// VerifiedVersion is the software version every command form here was
// captured on. A different one does not forbid acting — the CLI is recognized
// from "show version" — but it earns a transcript note: re-verify the forms
// after a firmware upgrade.
const VerifiedVersion = "2.0.2J"

// Client is the proven administration access — an open SSH shell session.
type Client struct {
	cli  *cliConn
	host string
	// User is the account that actually opened the access.
	User string
	// accepted: the candidate that worked, exactly as supplied.
	accepted Candidate
	// Steps is the transcript, in order.
	Steps []Step
	// facts: what "show version" established at login.
	facts versionFacts
	// known: the CLI was recognized. False = reads best-effort, writes refused.
	known bool
	// unknownWhy: what was ACTUALLY evaluated when `known` came back false,
	// as s3400CLIVerdict put it. Carried so every write refusal quotes the
	// finding instead of restating a generic pair of guesses.
	unknownWhy string
	// privileged: "enable" succeeded and the session sits at "#".
	privileged bool
	// cages: which of the two SFP+ numbering conventions THIS device uses, as
	// an output of THIS session showed it (ADR-0018). Filled by
	// noteCageNumbering from reads that were already paid for; read by
	// cageNumbering.cliPortName, which refuses to name a cage while it is
	// unknown rather than guess tg0/1 on a chassis numbered 49..52.
	//
	// On the Client and not on Driver DELIBERATELY: Driver is one object for
	// the whole fleet, so a convention remembered there would be the one of
	// whichever S3400 was read last, and a write to one switch would depend on
	// another. A Client is one session on one host.
	cages cageNumbering
	// lldpOn / lldpKnown: what the last configuration read said about "lldp
	// run". Apply turns LLDP on when it is off, and needs to know without
	// paying for a second "show running-config".
	lldpOn    bool
	lldpKnown bool
	// sysName: the device's OWN name, as the last PLAIN "show running-config"
	// on this access read it — same doctrine as lldpOn above, one field filled by
	// a read that was already paid for. Filled by ReadState, cleared by
	// SetHostname, reported by ReadIdentity; the policy, including why a write
	// must never feed it, is on identityread.go.
	sysName string
}

// noteCageNumbering remembers what an output just read says about this
// device's own cage numbering. Costs nothing: every caller is a read the
// connector was already making (ADR-0018, § 2).
//
// THE FIRST KNOWN SIGHTING WINS and is never replaced. A switch does not change
// firmware build in the middle of a session, so a second look can only confirm
// it — and a value that cannot move is a value a transcript can be read against
// afterwards. An output that says nothing, or that contradicts itself, leaves
// the field alone rather than clearing a fact already established.
func (c *Client) noteCageNumbering(out string) {
	if c.cages.known {
		return
	}
	var seen = observeCageNumbering(out)
	if !seen.known {
		return
	}
	c.cages = seen
	var how = "the cages restart their own series (\"tg0/1\"..\"tg0/4\", Build 95262)"
	if seen.base != 0 {
		how = fmt.Sprintf("the cages CONTINUE the copper series (\"tg0/%d\"..\"tg0/%d\", "+
			"Build 130907)", seen.base+1, seen.base+sfpPorts)
	}
	c.step(true, "SFP+ cage numbering observed", how+
		" — this is the numbering every write on this session will address")
}

// ensureCageNumbering establishes the numbering when nothing on this session
// has revealed it yet, at the cost of ONE read.
//
// For a caller that legitimately has no prior read — the optics sampler opens a
// session and calls [Client.ReadOptics] alone (internal/api/opticssample.go) —
// and for that caller only. The four WRITE paths each re-read the device before
// touching a port (ADR-0018, § 2), so calling this there would be a round trip
// that can never fire; and a write is not where an unplanned ninety-second
// pager read belongs.
//
// "show interface | include protocol" is the command that NAMES every socket,
// which is why it is this one and not a cheaper per-cage probe: "show interface
// tg0/1" carries the echo of the command, so a device that answers it with a
// refusal still hands back the string "tg0/1" — a session reading its own
// question as the answer. See [observeCageNumbering].
func (c *Client) ensureCageNumbering(ctx context.Context) {
	if c.cages.known {
		return
	}
	var out, err = c.cli.runPaged(ctx, "show interface | include protocol", 90*time.Second)
	if err != nil && strings.TrimSpace(out) == "" {
		return
	}
	c.noteCageNumbering(out)
}

func (c *Client) step(ok bool, what string, detail ...string) {
	c.Steps = append(c.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// Authenticate opens a shell session with the first accepted candidate.
//
// The order comes from the caller and is never reordered; no candidate is
// replayed (the try budget protects against account lockout). Access is
// returned EVEN on failure, so the transcript survives.
func (d *Driver) Authenticate(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Admin, error) {

	var c = &Client{host: tgt.Host}
	if len(cands) == 0 {
		c.step(false, "no credential to try",
			"the caller provided an empty candidate list — nothing was sent to the device")
		return c, fmt.Errorf("no credential to try on %s", tgt.Host)
	}

	var tried = map[string]bool{}
	var refusals []string
	var cand Candidate
	for _, cand = range cands {
		if ctx.Err() != nil {
			return c, ctx.Err()
		}
		var key = cand.Username + "\x00" + cand.Secret
		if tried[key] {
			c.step(true, "duplicate candidate skipped: \""+cand.Why+"\"",
				"same username and secret as an earlier attempt — the try budget is not spent twice")
			continue
		}
		tried[key] = true

		var client, err = dialSSH(ctx, tgt, cand.Username, cand.Secret)
		if err != nil {
			if isSSHAuthFailure(err) {
				refusals = append(refusals, fmt.Sprintf("\"%s\" refused", cand.Why))
				c.step(false, "credential refused: \""+cand.Why+"\"", firstLine(err.Error()))
				continue
			}
			c.step(false, "SSH unreachable", firstLine(err.Error()))
			return c, fmt.Errorf("SSH to %s: %v", tgt.Host, err)
		}

		var cli *cliConn
		cli, err = openCLI(ctx, client)
		if err != nil {
			_ = client.Close()
			c.step(false, "shell did not present a prompt", firstLine(err.Error()))
			return c, fmt.Errorf("SSH shell on %s: %v", tgt.Host, err)
		}

		c.cli = cli
		c.User = cand.Username
		c.accepted = cand
		c.step(true, "connected as \""+cand.Username+"\" ("+cand.Why+")",
			"interactive shell opened, prompt seen")

		c.postLogin(ctx, cand.Secret)
		return c, nil
	}

	return c, fmt.Errorf("no credential accepted by %s after %d attempt(s) — %s",
		tgt.Host, len(tried), strings.Join(refusals, " ; "))
}

// rePasswordPrompt: what "enable" asks when an enable secret IS configured.
var rePasswordPrompt = regexp.MustCompile(`(?i)password\s*:\s*$`)

// postLogin raises privilege and reads the identity. This is where the CLI is
// recognized — every write gesture consults it.
func (c *Client) postLogin(ctx context.Context, secret string) {
	c.enable(ctx, secret)

	// "terminal length 0" is sent for the record and NOT relied upon: it is
	// accepted silently on this firmware and changes nothing, from EXEC or
	// from config mode (captured 2026-08-03, tall pty requested). The pager is
	// fed instead, by every read (cf. cliConn.runPaged).
	var _, _ = c.cli.run(ctx, "terminal length 0", cmdTimeout)

	var out, err = c.cli.run(ctx, "show version", cmdTimeout)
	if err != nil && out == "" {
		c.step(false, "show version failed", firstLine(err.Error()))
		return
	}
	c.facts = parseShowVersion(out)
	c.known, c.unknownWhy = s3400CLIVerdict(out, c.facts)
	if !c.known {
		c.step(false, "CLI not recognized",
			c.unknownWhy+". Reads stay best-effort; writes are refused until this "+
				"CLI is surveyed.")
		return
	}
	if !strings.EqualFold(c.facts.Version, VerifiedVersion) {
		c.step(true, "firmware differs from the verified one",
			fmt.Sprintf("command set captured on Version %s — device runs %s. The forms "+
				"are those of the S3400 CLI reference; re-verify after an upgrade",
				VerifiedVersion, c.facts.Version))
		return
	}
	c.step(true, "FS "+c.facts.Model+" Version "+c.facts.Version+" recognized",
		fmt.Sprintf("serial %s, base MAC %s", orUnknown(c.facts.Serial), orUnknown(c.facts.MAC)))
}

// enable raises the session from "Switch>" to "Switch#".
//
// The factory configuration carries "aaa authentication enable default none"
// (captured in the running-config 2026-08-03), so no password is asked and the
// prompt changes immediately. A device that HAS been given an enable secret
// answers "Password:" — it is given the session's own secret, which is what
// meshcraft's own provisioning sets it to.
func (c *Client) enable(ctx context.Context, secret string) {
	var serr = c.cli.sendLine("enable")
	if serr != nil {
		c.step(false, "enable could not be sent", firstLine(serr.Error()))
		return
	}
	var out, _, err = c.cli.waitPromptOrPager(ctx, cmdTimeout)
	if err != nil {
		c.step(false, "no answer to \"enable\"", firstLine(err.Error()))
		return
	}
	if rePasswordPrompt.MatchString(strings.TrimRight(out, " \t\r\n")) {
		if err = c.cli.sendLine(secret); err != nil {
			c.step(false, "enable password could not be sent", firstLine(err.Error()))
			return
		}
		out, _, err = c.cli.waitPromptOrPager(ctx, cmdTimeout)
		if err != nil {
			c.step(false, "enable password refused or unanswered", firstLine(err.Error()))
			return
		}
	}
	c.privileged = strings.Contains(tail(out, 200), "#")
	if c.privileged {
		c.step(true, "privileged mode (#)",
			"\"enable\" — this platform asks for no password by default "+
				"(aaa authentication enable default none)")
		return
	}
	c.step(false, "still in user mode after \"enable\"",
		"reads that need privilege will fail; \"show version\" itself is refused at \">\"")
}

// requireWritable is the refusal common to every write.
func (c *Client) requireWritable() error {
	if !c.known {
		var why = c.unknownWhy
		if why == "" {
			why = "\"show version\" was never read on this session"
		}
		return driver.NotSupported("fs-s3400", "write to this device",
			"the CLI was not recognized at login: "+why+". The verified command set "+
				"targets the S3400 PoE+ CLI; anything else needs surveying before "+
				"meshcraft writes to it")
	}
	if !c.privileged {
		return driver.NotSupported("fs-s3400", "write to this device",
			"the session never reached privileged mode: \"enable\" did not produce a "+
				"\"#\" prompt, and configuration mode is unreachable from \">\"")
	}
	return nil
}

// WritesExtra: this connector also writes spanning-tree (stp.go), per-port
// storm control (stormcontrol.go) and the shut / un-shut gesture
// (portadmin.go) — what the UI must know about beyond VLAN membership.
//
// `storm_control` is declared since 2026-08-08 and it is the BEST-evidenced of
// the three: the command forms were captured on the lab S3400 (192.0.2.3,
// OpCode 2.0.2J) and the applier was then driven end to end on that hardware.
// The capture also disproved two things this connector believed — the bare
// spelling does not exist on this firmware, and the removal command was wrong
// — which is why stormcontrol.go's header now leads with the evidence.
//
// # `port_admin` — "shutdown" / "no shutdown", CAPTURED since 2026-08-10
//
// Declared 2026-08-06 on an operator decision, at a time when the evidence was
// weaker than this package's other writes; it no longer is, and the note is kept
// spelled out because the boundary of what was proven still matters. The
// 2026-08-03 hand survey and the 2026-08-05 connector-driven exercise
// (live_write_test.go) between them proved every form in apply.go on the lab
// S3400-48T4SP (Version 2.0.2J) — "config", "interface g0/N", "switchport
// pvid", "switchport mode", "switchport trunk vlan-allowed", "no vlan N",
// "write" — and none of them was "shutdown". On 2026-08-10 a shut → capture →
// revert exercise on the dark port g0/1 of that same lab unit added both missing
// halves: "shutdown" under "interface g0/1" was accepted, and the read-back
// word came with it — "GigaEthernet0/1 is administratively down, line protocol
// is down", reverting to "is down, line protocol is down". So
// [ParseLinkStates] mapping that phrase to [driver.LinkDisabled] is now replayed
// against real bytes (testdata/show_interface_g01_admin_down.txt,
// portadmin_live_test.go).
//
// WHAT IS STILL UNPROVEN is the applier itself: those commands were typed at the
// CLI by hand, NOT driven through [Client.ApplyPortAdmin], so its guardrail, its
// idempotence skip and its post-write verification remain covered by constructed
// tests only. This declaration did not move today, and the paragraphs below —
// written when the verb was deduced — describe failure modes that still hold on
// any unit whose firmware differs from the lab's.
//
// WHAT THE FAILURE LOOKS LIKE IF THE VERB IS WRONG: this CLI answers an
// unrecognised command with an error the connector reads, [Client.configBlock]
// attributes it to the line that earned it, and the port lands in
// [driver.ApplyResult.Skipped] naming the capture the form comes from, so the
// refusal reads as "this device's firmware differs from the lab's" rather than as
// a guess that failed. If instead the verb were accepted but the read-back phrase
// differed, the
// post-write re-read reports the port as NOT moved rather than as written. Both
// paths are loud; neither leaves a port in an unreported state.
//
// AND IF THE VERB IS RIGHT AND HITS THE WRONG PORT, the guardrail is what stands
// in the way: portadmin.go refuses to shut a port carrying a LIVE LINK in the
// management VLAN without [driver.PortAdminPlan.Force], spares every linked port
// when no SVI reports an address, and the write is deliberately not followed by
// "write" — so a reboot brings the port back. This platform has a console port
// as well.
//
// What is left to close is an exercise of the APPLIER on hardware — the capture
// above was hand-typed at the CLI.
func (d *Driver) WritesExtra(name string) bool {
	return name == "stp" || name == "port_admin" || name == "storm_control"
}

// Supports: the optional gestures of this platform, computed from the method
// sets and never listed by hand — see driver.GesturesOf.
func (d *Driver) Supports() []driver.Gesture {
	return driver.GesturesOf(d, (*Client)(nil))
}

// Identify: WHICH device answers at this address. Nothing is read without a
// credential — the CLI sits behind SSH authentication.
func (d *Driver) Identify(ctx context.Context, tgt driver.Target,
	cands []Candidate) (Identity, error) {

	if len(cands) == 0 {
		return Identity{}, fmt.Errorf("this platform reveals nothing without a credential: " +
			"the CLI sits behind SSH authentication")
	}
	var adm, err = d.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		return Identity{}, err
	}
	defer c.Close()
	return c.identity(ctx), nil
}

// identity assembles the identity from the facts read at login.
//
// Unlike the S3900, "show version" here carries the model AND the serial
// AND the base MAC, so nothing has to be scraped out of the banner — the
// banner is used only as a fallback.
func (c *Client) identity(ctx context.Context) Identity {
	var model = c.facts.Model
	if model == "" {
		model = bannerModel(c.cli.banner)
	}
	var id = Identity{
		Serial:   c.facts.Serial,
		Firmware: c.facts.Version,
		Model:    model,
		// The base MAC is the CHASSIS identity, not decoration: LLDP
		// correlation matches a neighbour's chassis-id against the MACs a
		// device is known by (cf. deviceKnownMACs, internal/api/lldp_cli.go).
		// "show version" hands it over at login and reVerMAC has always
		// parsed it — it was simply dropped on the floor here, so a device
		// adopted through Identify came in with no chassis MAC at all while
		// ReadIdentity, on the very same facts, reported one.
		MAC:    c.facts.MAC,
		Source: "ssh_cli",
	}
	var groups, err = c.portGroups(ctx)
	if err == nil {
		id.PortGroups = groups
		id.CopperPorts = copperPorts
		id.SFPPorts = sfpPorts
		id.SFPPlus = true
	}
	return id
}

// Connector returns the identity of the connector that produced this access.
func (c *Client) Connector() driver.Info { return (&Driver{}).Info() }

// Account is the account that actually opened the access.
func (c *Client) Account() string { return c.User }

// Accepted is the candidate that worked, exactly as it was supplied.
func (c *Client) Accepted() Candidate { return c.accepted }

// Transcript is the trace of everything this access has done, in order.
func (c *Client) Transcript() []Step { return c.Steps }

// Close closes the shell session.
func (c *Client) Close() {
	if c.cli != nil {
		c.cli.close()
		c.cli = nil
	}
}

func isSSHAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	var s = strings.ToLower(err.Error())
	return strings.Contains(s, "unable to authenticate") ||
		strings.Contains(s, "permission denied") ||
		strings.Contains(s, "auth fail")
}

func firstLine(s string) string {
	var i = strings.IndexAny(s, "\r\n")
	if i >= 0 {
		return s[:i]
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
