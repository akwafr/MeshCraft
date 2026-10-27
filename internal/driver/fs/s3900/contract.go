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
	// Read proven on the lab S3900-24T4S, FSOS 1.7.7 (2026-08-05) — cf. stp.go.
	_ driver.SpanningTreeReader = (*Client)(nil)
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
	// sysName: the device's OWN name, as the last PLAIN `show run` on this
	// access read it. Filled by ReadState, cleared by SetHostname, reported by
	// ReadIdentity — the whole policy, including why a write must never feed it,
	// is on identityread.go.
	sysName string
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
		"\"show version\" does not carry \"Operation Code Version\" — this is not the "+
			"CLI the command set was verified on (possibly the post-upgrade S3900-R CLI). "+
			"Reads stay best-effort; writes are refused until the new CLI is surveyed.")
}

// requireWritableDialect: the refusal common to every write.
func (c *Client) requireWritableDialect() error {
	if c.dialect == dialectFSOS17 {
		return nil
	}
	return driver.NotSupported("fs-s3900", "write to this device",
		"the CLI dialect was not recognized at login (\"show version\" without "+
			"\"Operation Code Version\"). The verified command set targets FSOS 1.7; "+
			"a device upgraded to the S3900-R CLI needs its command set surveyed and "+
			"added before meshcraft writes anything to it")
}

// WritesExtra: the shut / un-shut gesture, and nothing else beyond VLAN
// membership.
//
// # `port_admin` — "shutdown" / "no shutdown", TYPED ONCE on 2026-08-10, on a
// # firmware that is about to move
//
// Declared 2026-08-06 on an operator decision, when this was the THINNEST
// evidence of the eight connectors. A hand exercise on 2026-08-10 moved two of
// the three bullets below, and the note keeps them rather than borrowing the
// confidence of the others:
//
//   - "shutdown" / "no shutdown" inside "interface ethernet 1/N" is the
//     Accton-family form, and it HAD never been typed on this hardware — the lab
//     survey (OpCode 1.7.0) and the post-upgrade capture (1.7.7) proved "vlan
//     database", "vlan N name X [media ethernet]", "switchport mode trunk",
//     "switchport trunk allowed vlan add/remove" and the save sequence, nothing
//     about administrative state. On 2026-08-10 it was typed, on the lab
//     S3900-24T4S, on a dark port, and accepted ("conf t" is the config-mode
//     word; plain "configure" is refused);
//   - the admin-down ROW came with it — "Eth 1/ 1 … Down Down" where the port
//     beside it read "Up Down" — where every line of the earlier lab capture
//     read "Up" in the Admin column. Both are replayed in
//     portadmin_live_test.go. What was NOT exercised is [Client.ApplyPortAdmin]
//     itself: the commands were hand-typed, so the guardrail, the idempotence
//     skip and the post-write verification are still constructed tests only;
//   - and this package has already been bitten on this exact firmware boundary —
//     "media ethernet" is REQUIRED on 1.7.0 and REFUSED on 1.7.7 (apply.go's
//     vlanCreateCmds carries the capture, caret included). The pending fleet
//     upgrade may swap the CLI for the BDCOM-lineage S3900-R dialect entirely, on
//     which [Client.requireWritableDialect] refuses every write.
//
// WHAT MAKES THAT DECLARABLE is that the two dialects fail in DIFFERENT and both
// loud ways. On an unrecognised CLI nothing is sent at all — the dialect check
// refuses before the first command. On the verified CLI a rejected verb comes back
// as a caret line that [Client.runConfigBlock] reads, and the port is reported in
// [driver.ApplyResult.Skipped] naming the firmware the command set was surveyed
// on. There is only ONE spelling of "shutdown" in either lineage, so unlike
// apply.go's ordered candidates there is nothing to fall back to — and nothing to
// try blindly either.
//
// AND THE GUARDRAIL is the real safety net: portadmin.go will not shut a port
// carrying a LIVE LINK in the management VLAN without [driver.PortAdminPlan.Force],
// spares every linked port when the running configuration names no SVI with an
// address, and deliberately does not "copy running-config startup-config" — so a
// reboot brings the port back. There is a console port too.
//
// What is left to close is an exercise of the APPLIER on hardware, and re-verifying
// the forms after the pending upgrade — the 2026-08-10 capture was hand-typed, and
// says nothing about the post-upgrade CLI.
//
// # `dhcp_snooping` — EARNED ON HARDWARE 2026-08-11, applier and all
//
// This one is declared on the bar this feature sets for every range and not on
// the strength of the fields existing: [Client.ApplyDHCPSnooping] was driven end
// to end against the lab S3900-24T4S on FSOS 1.7.7, through
// live_snoop_test.go, over the controller's SOCKS route. What the run measured,
// in the applier's own order:
//
//   - the global enable took and was confirmed by a re-read — "dhcp snooping",
//     the spelling captured on 2026-08-10, with no "ip" prefix;
//   - `GE26` — the port `show lldp neighbor` shows facing core01-lab on
//     Ethernet48, i.e. the real uplink — came back in
//     [driver.ApplyResult.PortsWritten], which on this contract means CONFIRMED
//     by re-read and not merely sent;
//   - an INDEPENDENT [Client.ReadDHCPSnooping] then reported the feature on,
//     VLAN 777 armed and GE26 trusted — the applier's own verification was not
//     asked to vouch for itself;
//   - a SECOND identical apply wrote NOTHING, which is what makes this callable
//     from a reconciler;
//   - the teardown restored the opening state and "show dhcp snooping config"
//     came back byte-for-byte identical, re-proved afterwards from a separate
//     SSH session.
//
// TWO LIMITS, named rather than left to be discovered. Inspection was armed on
// VLAN 777, which is undefined on that switch, so the run proves the applier and
// does NOT observe a DHCP frame being dropped — watching that needs a VLAN
// carrying clients, which is an outage on a live path. And the per-port trust
// flag was exercised on the UPLINK only; a plan that untrusts a port has never
// been run here.
func (d *Driver) WritesExtra(name string) bool {
	return name == "port_admin" || name == "dhcp_snooping"
}

// Supports: the optional gestures of this platform, computed from the method
// sets and never listed by hand — see driver.GesturesOf.
func (d *Driver) Supports() []driver.Gesture {
	return driver.GesturesOf(d, (*Client)(nil))
}

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
// platform "show version" carries NO model line — the name appears only in the
// banner ("CLI session with the S3900-24T4S is opened") and in sysDescr — which
// is why discovery, keyed on a "Model:" pattern, left the field empty. Matches
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
