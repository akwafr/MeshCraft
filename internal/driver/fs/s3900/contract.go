package s3900

// Adoption of the access contract (driver/connector.go) by the FS S3900
// connector — the product's FIRST SSH/CLI connector. The structure follows
// RouterOS (internal/driver/mikrotik/routeros/contract.go); the platform does
// not: here there is a real shell SESSION, opened once by Authenticate and
// reused for every gesture of the access.
//
// # Two CLI dialects, and why the connector carries this structurally
//
// The S3900 fleet exists with TWO incompatible CLIs depending on firmware:
//
//   - "fsos17" (Accton lineage): the one from the lab capture on OpCode
//     1.7.0 — "vlan database", "interface ethernet 1/1", "show interfaces
//     brief", saved via "copy running-config startup-config" + a filename
//     prompt. EVERY write this connector performs is in this dialect;
//   - "s3900r" (BDCOM lineage, S3900-R CLI guide): "Switch_config_g0/1#",
//     "switchport pvid", "lldp run". Documented, NEVER captured on real
//     hardware here. An upgraded firmware may switch into this world.
//
// The project's rule applies: an unknown capability is assumed absent. On an
// unrecognized dialect, reads stay best-effort and WRITES are REFUSED with the
// way forward (capture the post-upgrade CLI, complete the command set) —
// never attempted "just to see" on a CLI that has not been surveyed.

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
	"regexp"
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
	dialectFSOS17  = "fsos17"
	dialectUnknown = ""
)

// VerifiedOpCode: the version on which EVERY command form was captured. A
// different OpCode within this family does not forbid acting — the dialect is
// recognized from "show version" — but it deserves a transcript note: after a
// firmware upgrade, re-verify the forms.
const VerifiedOpCode = "1.7"

// Client is the proven administration access — an open SSH shell session.
type Client struct {
	cli  *cliConn
	host string
	// User is the account that actually opened the access.
	User string
	// accepted: the candidate that worked, exactly as supplied. Unexported: a
	// public field could be mutated after the fact.
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

		c.postLogin(ctx)
		return c, nil
	}

	return c, fmt.Errorf("no credential accepted by %s after %d attempt(s) — %s",
		tgt.Host, len(tried), strings.Join(refusals, " ; "))
}

// postLogin: pagination cut (best effort) and identity read. This is where
// the dialect gets decided — every write gesture consults it.
func (c *Client) postLogin(ctx context.Context) {
	// "terminal length 0": not captured on 1.7.0 — a refusal only costs an
	// error echo, whereas active pagination would cost every "show run".
	var _, tlErr = c.cli.run(ctx, "terminal length 0", cmdTimeout)
	if tlErr != nil {
		c.step(true, "terminal length 0 rejected — relying on the tall pty", firstLine(tlErr.Error()))
	}

	var out, err = c.cli.run(ctx, "show version", cmdTimeout)
	if err != nil && out == "" {
		c.step(false, "show version failed", firstLine(err.Error()))
		return
	}
	c.facts = parseShowVersion(out)
	if c.facts.OpCode != "" {
		c.dialect = dialectFSOS17
		if !strings.HasPrefix(c.facts.OpCode, VerifiedOpCode) {
			c.step(true, "firmware differs from the originally verified one",
				fmt.Sprintf("command set first captured on OpCode %s.x — device runs %s. "+
					"The forms known to move between revisions are gated by version and "+
					"carry a fallback (cf. vlanCreateCandidates); anything else is "+
					"unverified on this revision", VerifiedOpCode, c.facts.OpCode))
		} else {
			c.step(true, "FSOS "+c.facts.OpCode+" recognized",
				fmt.Sprintf("serial %s, %d ports", orUnknown(c.facts.Serial), c.facts.Ports))
		}
		return
	}
	c.dialect = dialectUnknown
	c.step(false, "CLI dialect not recognized",
		"« show version » does not carry « Operation Code Version » — this is not the "+
			"CLI the command set was verified on (possibly the post-upgrade S3900-R CLI). "+
			"Reads stay best-effort; writes are refused until the new CLI is surveyed.")
}

// requireWritableDialect: the refusal common to every write.
func (c *Client) requireWritableDialect() error {
	if c.dialect == dialectFSOS17 {
		return nil
	}
	return driver.NotSupported("fs-s3900", "write to this device",
		"the CLI dialect was not recognized at login (« show version » without "+
			"« Operation Code Version »). The verified command set targets FSOS 1.7; "+
			"a device upgraded to the S3900-R CLI needs its command set surveyed and "+
			"added before meshcraft writes anything to it")
}

// WritesExtra: nothing is written beyond VLAN membership.
func (d *Driver) WritesExtra(name string) bool { return false }

// Identify: WHICH device answers at this address. Like RouterOS, NOTHING is
// read without a credential: the CLI sits behind SSH authentication.
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
		Serial:   c.facts.Serial,
		Firmware: c.facts.OpCode,
		Model:    bannerModel(c.cli.banner),
		Source:   "ssh_cli",
	}
	var briefOut, err = c.cli.run(ctx, "show interfaces brief", cmdTimeout)
	if err != nil && briefOut == "" {
		return id
	}
	var ports = parseInterfacesBrief(briefOut)
	id.PortGroups = portGroupsOf(ports)
	var p briefPort
	for _, p = range ports {
		if p.SpeedMbps >= 10000 {
			id.SFPPorts++
			id.SFPPlus = true
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

// Close closes the shell session. Not part of the contract (two platforms out
// of three have nothing to close) — exposed for internal paths that know they
// are done (Identify, Inspect).
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

// bannerModel pulls the product reference out of the login banner. On this
// platform « show version » carries NO model line — the name appears only in the
// banner (« CLI session with the S3900-24T4S is opened ») and in sysDescr — which
// is why discovery, keyed on a « Model: » pattern, left the field empty. Matches
// an S3900 / S3900-R product code; empty when the banner does not carry one, so a
// firmware that changes the wording simply yields no model rather than a wrong one.
func bannerModel(banner string) string {
	var m = reBannerModel.FindStringSubmatch(banner)
	if m == nil {
		return ""
	}
	return m[1]
}

var reBannerModel = regexp.MustCompile(`(?i)(S3900-[A-Z0-9]+)`)
