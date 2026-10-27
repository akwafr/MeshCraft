package ios

// Adoption of the access contract (driver/connector.go) by the Cisco Catalyst
// connector. The structure follows the FS S3900 connector
// (internal/driver/fs/s3900/contract.go): a real shell SESSION, opened once by
// Authenticate and reused for every gesture of the access.
//
// # The dialect gate, and what it protects against HERE
//
// On the FS connector the gate exists because the same product line ships two
// incompatible CLIs. On a Catalyst the risk is different and just as real: this
// vendor's CLI is the one the whole industry imitates. A Huawei, an H3C, a
// Dell, a FortiSwitch and a dozen white-label switches all answer `show
// version` and all accept `conf t` — and none of them means the same thing by
// `switchport trunk allowed vlan`. A connector that assumed "it answered, so it
// is a Catalyst" would eventually write a Cisco command set into a lookalike.
//
// So: `show version` must announce IOS ([reIsIOS]). If it does not, reads stay
// best-effort and WRITES ARE REFUSED with the way forward — never attempted
// "just to see" on a CLI that has not been surveyed.
//
// # And the second gate, which the FS connector does not need
//
// Nothing in this package has been run on hardware (cf. the package doc). Every
// session therefore carries a transcript step saying so. It is not decoration:
// the transcript is what an operator reads when a write did something
// unexpected, and "this command set is documented, not captured" is the first
// thing that should be known at that moment.

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Aliases (ALIASES, not definitions — cf. routeros/contract.go).
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

	_ driver.Admin            = (*Client)(nil)
	_ driver.AccountManager   = (*Client)(nil)
	_ driver.SNMPConfigurator = (*Client)(nil)
	_ driver.Persister        = (*Client)(nil)
	_ driver.MgmtAddrReader   = (*Client)(nil)
	_ driver.MgmtAddrWriter   = (*Client)(nil)
	_ driver.FaceplateReader  = (*Client)(nil)
)

// CLI dialects.
const (
	dialectIOS     = "ios"
	dialectUnknown = ""
)

// DocumentedRelease is the train the command set was written against. It is
// NOT a "verified" version — nothing here has been captured — and the constant
// is named accordingly so nobody reading the transcript mistakes one for the
// other.
const DocumentedRelease = "15.2(7)E"

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
	// dialect: the recognized CLI. Empty = unknown, writes refused.
	dialect string
	// portStem: the interface-name stem THIS device printed — "1/0/" on a
	// stackable, "0/" on a 2960 classic or a 3560. Empty until a read has been
	// parsed; the renderers then fall back to the stacked form (cf. parse.go).
	portStem string
}

func (c *Client) step(ok bool, what string, detail ...string) {
	c.Steps = append(c.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// learnPortStem records how THIS chassis spells its interface names, from any
// output that names one: a "show interfaces status" table or a running
// configuration.
//
// Why it is learned and not derived from the model string: the model is a
// marketing part number, the stem is a chassis property, and the mapping
// between them is a table nobody here has verified end to end. The device
// prints the answer in every read the write paths already perform — asking it
// costs nothing and cannot be out of date.
//
// FIRST ANSWER WINS and it is never revised: one session talks to one chassis,
// and a stem that changed mid-session would mean the reads were interleaved
// from two devices, which is a worse problem than the naming. A read that names
// no faceplate port at all leaves the default in place.
func (c *Client) learnPortStem(text string) {
	if c.portStem != "" || text == "" {
		return
	}
	var line string
	for _, line = range strings.Split(text, "\n") {
		var f = strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		// A status row opens with the name; a config block opens with the
		// keyword and then the name.
		var tok = f[0]
		if strings.EqualFold(tok, "interface") {
			if len(f) < 2 {
				continue
			}
			tok = f[1]
		}
		var _, stem, _, ok = decomposeIOSName(tok)
		if !ok {
			continue
		}
		c.portStem = stem
		if stem != stackedStem {
			// Worth a transcript line: it changes every interface name this
			// session will write, and it is the fact an operator needs when the
			// commands in the transcript do not look like the documentation.
			c.step(true, "interface naming: no stack member on this chassis",
				"ports are addressed \""+stem+"N\" (e.g. GigabitEthernet"+stem+"1), "+
					"not the \"1/0/N\" of a stackable")
		}
		return
	}
}

// portName is [cliPortNameStem] bound to what this device printed. Every write
// that names an interface goes through it.
func (c *Client) portName(std string) string {
	return cliPortNameStem(std, c.portStem)
}

// Authenticate opens a shell session with the first accepted candidate.
//
// The order comes from the caller and is never reordered; no candidate is
// replayed (the try budget protects against account lockout — and IOS with
// `login block-for` configured will lock an address out, not just an account).
// Access is returned EVEN on failure, so the transcript survives.
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
			// Dead transport or impossible negotiation: further attempts would
			// prove nothing and would burn the budget.
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

		if !cli.priv {
			// The account does not carry privilege 15. Try to raise the session
			// with the account's own secret — which is what a device provisioned
			// before the fleet's no-enable-password rule still carries.
			var enErr = cli.enable(ctx, cand.Secret)
			if enErr != nil {
				c.step(false, "user EXEC only: \"enable\" failed", firstLine(enErr.Error()))
				c.step(false, "reads and writes will be refused",
					"this account landed on \">\" and could not reach privileged EXEC. "+
						"meshcraft sets NO enable password (fleet rule) and IOS refuses "+
						"\"enable\" from a vty when none is set, so the fix is \"privilege 15\" "+
						"on this account — which EnsureAccount writes, and which this device "+
						"has evidently lost or never had. It needs console access")
				return c, fmt.Errorf("account %q on %s reaches user EXEC only: %v",
					cand.Username, tgt.Host, enErr)
			}
			c.step(true, "privileged EXEC reached via \"enable\"",
				"the account does not carry privilege 15 — EnsureAccount would set it")
		}

		c.postLogin(ctx)
		return c, nil
	}

	return c, fmt.Errorf("no credential accepted by %s after %d attempt(s) — %s",
		tgt.Host, len(tried), strings.Join(refusals, " ; "))
}

// postLogin: pagination and vty logging cut, then the identity read. This is
// where the dialect gets decided — every write gesture consults it.
func (c *Client) postLogin(ctx context.Context) {
	// "terminal length 0" is universal on IOS; a refusal only costs an error
	// echo, whereas active pagination would swallow every "show run".
	var _, tlErr = c.cli.run(ctx, "terminal length 0", cmdTimeout)
	if tlErr != nil {
		c.step(true, "terminal length 0 rejected — relying on the tall pty", firstLine(tlErr.Error()))
	}
	// "terminal no monitor" stops syslog from being interleaved into command
	// output on this vty. It is best effort: [reCLIError] is written not to
	// mistake a syslog line for a refusal, so this is the second lock and not
	// the only one.
	var _, tmErr = c.cli.run(ctx, "terminal no monitor", cmdTimeout)
	if tmErr != nil {
		c.step(true, "terminal no monitor rejected",
			"syslog may interleave with command output — the refusal parser is written for it")
	}

	var out, err = c.cli.run(ctx, "show version", cmdTimeout)
	if err != nil && out == "" {
		c.step(false, "show version failed", firstLine(err.Error()))
		return
	}
	if !reIsIOS.MatchString(out) {
		c.dialect = dialectUnknown
		c.step(false, "CLI dialect not recognized",
			"\"show version\" does not announce Cisco IOS. Many platforms imitate this "+
				"CLI and mean different things by the same words — reads stay best-effort, "+
				"writes are refused until this device's own command set is surveyed.")
		return
	}
	c.dialect = dialectIOS
	c.facts = parseShowVersion(out)
	c.step(true, "Cisco IOS recognized",
		fmt.Sprintf("release %s, model %s, serial %s",
			orUnknown(c.facts.Release), orUnknown(c.facts.Model), orUnknown(c.facts.Serial)))
	if !c.facts.hasCrypto() && c.facts.Image != "" {
		c.step(true, "non-crypto image: this device cannot serve SSH",
			fmt.Sprintf("feature set %q carries no \"crypto\" command, so no host key can be "+
				"generated. The device stays reachable by console (and by telnet if it is "+
				"enabled) — a k9 image is required to adopt it over the network", c.facts.Image))
	}
	// The honesty step. Present on EVERY session, deliberately: the transcript
	// is read at the moment something behaved unexpectedly, and this is the
	// first fact that matters then.
	//
	// IT USED TO SAY "no form in this connector has been run on lab hardware"
	// and it kept saying it for six days after that stopped being true. An
	// honesty note that is itself out of date is worse than none: an operator
	// reading it discounts a transcript that is in fact evidence. It now names
	// the split, and whoever exercises the port write path is expected to move
	// that line rather than add a second one somewhere else.
	c.step(true, "command set PARTLY captured, partly documented",
		fmt.Sprintf("written against IOS %s from the vendor's command reference. EXERCISED "+
			"on the lab 2960-X: management addressing (2026-08-03), hostname (2026-08-05), "+
			"the trap recipient and per-port storm control (both apply → idempotent re-apply "+
			"→ revert, by re-read), the spanning-tree and DHCP-snooping reads. NOT yet run "+
			"on hardware: the port VLAN write path (\"switchport mode access / trunk\" and "+
			"its encapsulation fallback) and \"shutdown\" on a physical port. Forms that "+
			"differ across the Catalyst family are tried as ordered candidates and the "+
			"accepted one is recorded below", DocumentedRelease))
}

// requireWritableDialect: the refusal common to every write.
func (c *Client) requireWritableDialect() error {
	if c.dialect == dialectIOS {
		return nil
	}
	return driver.NotSupported("cisco-ios", "write to this device",
		"\"show version\" did not announce Cisco IOS at login. This connector's command "+
			"set is Catalyst-specific and several platforms imitate the same CLI without "+
			"sharing its semantics; writing here could configure something other than what "+
			"was asked. Survey this device's actual command set and add it before meshcraft "+
			"writes anything to it")
}

// WritesExtra: spanning tree, storm control, and the shut / un-shut gesture.
//
// PoE and port security remain writable on this platform and unimplemented —
// declaring either would announce configuration that never arrives. `stp` is
// declared because [Client.ApplySpanningTree] genuinely writes it: mode, MST
// region and bridge priority, with the region ordered before the mode so the
// device converges once rather than twice (cf. stp.go).
//
// `storm_control` is declared since 2026-08-08 and it is the one entry here
// with a full live proof behind it: [Client.ApplyStormControl] was applied,
// re-applied for idempotence, partly removed and reverted on the lab
// CiscoLabFirst1 (live_stormcontrol_test.go), each step confirmed by re-read.
// `dhcp_snooping` is deliberately still ABSENT — cf. dhcpsnoop.go, which
// carries its own reasoning.
//
// # `port_admin` — "shutdown" / "no shutdown", CAPTURED as a form, unexercised
// # on a physical port
//
// [Client.ApplyPortAdmin] writes "shutdown" / "no shutdown" inside an
// "interface" block. It is declared on an OPERATOR DECISION taken 2026-08-06,
// and the honest evidence line is this:
//
//   - "no shutdown" inside an interface block HAS been sent by this connector
//     on hardware — on an SVI, in [Client.ApplyMgmtAddressing], 2026-08-03 on the
//     lab 2960-X. The verb pair is the oldest and least ambiguous form in the IOS
//     command set, and the operator named it explicitly when asking for this;
//   - "shutdown" on a PHYSICAL port has never been sent by this connector, and
//     the "disabled" status word portadmin.go reads back to confirm the write
//     has never appeared in a capture (every "show interfaces status" in
//     testdata/ reads "connected" or "notconnect").
//
// SO THE RISK IF THE FORM WERE WRONG IS BOUNDED AND LOUD, which is what makes
// this declarable rather than reckless. A refused command arrives as a "%" line
// that [Client.runConfigBlock] surfaces, the port is reported in
// [driver.ApplyResult.Skipped] naming the release the command set was written
// against, and the post-write re-read refuses to claim a port moved on the
// strength of a return code. The realistic bad outcome is "nothing happened and
// the screen said so", not a silent wrong write.
//
// The reason a declaration is survivable at all is the GUARDRAIL, not the form:
// portadmin.go refuses to shut a port carrying a LIVE LINK in the management VLAN
// (access, native, tagged, or a trunk with no allowed-vlan line — which on IOS
// means ALL VLANs) unless [driver.PortAdminPlan.Force] is set, and spares every
// linked port when the management VLAN cannot be read. A Catalyst also has a
// console port, so the worst case here is a person in the rack rather than the
// paperclip the Instant On and SwOS ranges need.
//
// TODO.md still names the capture that would upgrade this from "form documented,
// bounded failure" to "exercised": "shutdown" on a spare port, with the
// "disabled" word read back from this hardware.
func (d *Driver) WritesExtra(name string) bool {
	return name == "stp" || name == "port_admin" || name == "storm_control" ||
		name == "dhcp_snooping"
}

// Supports: the optional gestures of this platform, computed from the method
// sets and never listed by hand — see driver.GesturesOf.
func (d *Driver) Supports() []driver.Gesture {
	return driver.GesturesOf(d, (*Client)(nil))
}

// Identify: WHICH device answers at this address. Like RouterOS and the FS
// connector, NOTHING is read without a credential: the CLI sits behind SSH
// authentication.
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

// identity assembles the identity from the already-read facts + the faceplate.
func (c *Client) identity(ctx context.Context) Identity {
	var id = Identity{
		Model:    c.facts.Model,
		Serial:   c.facts.Serial,
		MAC:      c.facts.MAC,
		Firmware: c.facts.Release,
		Source:   "ssh_cli",
	}
	var statusOut, err = c.cli.run(ctx, "show interfaces status", cmdTimeout)
	if err != nil && statusOut == "" {
		return id
	}
	c.learnPortStem(statusOut)
	var ports = parseInterfacesStatus(statusOut)
	id.PortGroups = portGroupsOf(ports)
	var p statusPort
	for _, p = range ports {
		if isSFPCage(p.StdPrefix, p.Type) {
			id.SFPPorts++
			if p.SpeedMbps >= 10000 {
				id.SFPPlus = true
			}
		} else {
			id.CopperPorts++
		}
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

// Close closes the shell session. Not part of the contract — exposed for the
// internal paths that know they are done (Identify, Inspect).
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
