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
}

func (c *Client) step(ok bool, what string, detail ...string) {
	c.Steps = append(c.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
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
			c.step(true, "duplicate candidate skipped: « "+cand.Why+" »",
				"same username and secret as an earlier attempt — the try budget is not spent twice")
			continue
		}
		tried[key] = true

		var client, err = dialSSH(ctx, tgt, cand.Username, cand.Secret)
		if err != nil {
			if isSSHAuthFailure(err) {
				refusals = append(refusals, fmt.Sprintf("« %s » refused", cand.Why))
				c.step(false, "credential refused: « "+cand.Why+" »", firstLine(err.Error()))
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
		c.step(true, "connected as « "+cand.Username+" » ("+cand.Why+")",
			"interactive shell opened, prompt seen")

		if !cli.priv {
			// The account does not carry privilege 15: raise the session with
			// the SAME secret, which is what EnsureAccount aligns `enable
			// secret` on.
			var enErr = cli.enable(ctx, cand.Secret)
			if enErr != nil {
				c.step(false, "user EXEC only: « enable » failed", firstLine(enErr.Error()))
				c.step(false, "reads and writes will be refused",
					"this account landed on « > » and its secret does not open privileged "+
						"EXEC. Give it « privilege 15 », or align « enable secret » on it")
				return c, fmt.Errorf("account %q on %s reaches user EXEC only: %v",
					cand.Username, tgt.Host, enErr)
			}
			c.step(true, "privileged EXEC reached via « enable »",
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
			"« show version » does not announce Cisco IOS. Many platforms imitate this "+
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
			fmt.Sprintf("feature set %q carries no « crypto » command, so no host key can be "+
				"generated. The device stays reachable by console (and by telnet if it is "+
				"enabled) — a k9 image is required to adopt it over the network", c.facts.Image))
	}
	// The honesty step. Present on EVERY session, deliberately: the transcript
	// is read at the moment something behaved unexpectedly, and this is the
	// first fact that matters then.
	c.step(true, "command set DOCUMENTED, not captured",
		fmt.Sprintf("written against IOS %s from the vendor's command reference; no form in "+
			"this connector has been run on lab hardware. Forms that differ across the "+
			"Catalyst family are tried as ordered candidates and the accepted one is "+
			"recorded below", DocumentedRelease))
}

// requireWritableDialect: the refusal common to every write.
func (c *Client) requireWritableDialect() error {
	if c.dialect == dialectIOS {
		return nil
	}
	return driver.NotSupported("cisco-ios", "write to this device",
		"« show version » did not announce Cisco IOS at login. This connector's command "+
			"set is Catalyst-specific and several platforms imitate the same CLI without "+
			"sharing its semantics; writing here could configure something other than what "+
			"was asked. Survey this device's actual command set and add it before meshcraft "+
			"writes anything to it")
}

// WritesExtra: spanning tree, and nothing else yet.
//
// PoE and port security remain writable on this platform and unimplemented —
// declaring either would announce configuration that never arrives. `stp` is
// declared because [Client.ApplySpanningTree] genuinely writes it: mode, MST
// region and bridge priority, with the region ordered before the mode so the
// device converges once rather than twice (cf. stp.go).
func (d *Driver) WritesExtra(name string) bool { return name == "stp" }

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
