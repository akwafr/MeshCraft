package edgeswitch

// Adoption of the access contract (driver/connector.go).

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

type (
	Step        = driver.Step
	Candidate   = driver.Candidate
	VLAN        = driver.VLAN
	PortState   = driver.PortState
	State       = driver.State
	PortPlan    = driver.PortPlan
	ApplyResult = driver.ApplyResult
	Identity    = driver.Identity
)

var (
	_ driver.Connector  = (*Driver)(nil)
	_ driver.Identifier = (*Driver)(nil)
	_ driver.PortNamer  = (*Driver)(nil)

	_ driver.Admin              = (*Client)(nil)
	_ driver.MgmtAddrReader     = (*Client)(nil)
	_ driver.FaceplateReader    = (*Client)(nil)
	_ driver.SpanningTreeReader = (*Client)(nil)
	_ driver.SNMPConfigurator   = (*Client)(nil)
	_ driver.Persister          = (*Client)(nil)
	_ driver.AccountManager     = (*Client)(nil)
	_ driver.PoEReader          = (*Client)(nil)
	_ driver.PoEApplier         = (*Client)(nil)
)

// VerifiedFirmware is the version the reads were captured on.
const VerifiedFirmware = "1.9.2"

// Client is the proven administration access — an open SSH shell session.
type Client struct {
	cli        *cliConn
	host       string
	User       string
	accepted   Candidate
	Steps      []Step
	facts      versionFacts
	known      bool
	privileged bool
}

func (c *Client) step(ok bool, what string, detail ...string) {
	c.Steps = append(c.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// Authenticate opens a shell session with the first accepted candidate.
func (d *Driver) Authenticate(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Admin, error) {

	var c = &Client{host: tgt.Host}
	if len(cands) == 0 {
		c.step(false, "no credential to try", "the caller provided an empty candidate list")
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
				"the try budget is not spent twice")
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

// postLogin raises privilege, kills the pager and reads the identity.
func (c *Client) postLogin(ctx context.Context, secret string) {
	c.enable(ctx, secret)

	// CAPTURED: "terminal length 0" is accepted here; "no pagination", which
	// the CLI reference names, is refused. Reads feed the pager anyway.
	var _, _ = c.cli.run(ctx, "terminal length 0", cmdTimeout)

	var out, err = c.cli.run(ctx, "show version", cmdTimeout)
	if err != nil && strings.TrimSpace(out) == "" {
		c.step(false, "show version failed", firstLine(err.Error()))
		return
	}
	c.facts = parseShowVersion(out)
	c.known = c.facts.Model != "" || c.facts.Firmware != ""
	if !c.known {
		c.step(false, "CLI not recognized",
			"\"show version\" carries neither a Machine Model nor a Software Version line. "+
				"Reads stay best-effort; writes are refused until this CLI is surveyed.")
		return
	}
	if c.facts.Firmware != VerifiedFirmware {
		c.step(true, "firmware differs from the verified one",
			fmt.Sprintf("reads captured on %s — device runs %s; re-verify the forms",
				VerifiedFirmware, c.facts.Firmware))
		return
	}
	c.step(true, "Ubiquiti "+orUnknown(c.facts.Model)+" firmware "+c.facts.Firmware+" recognized",
		fmt.Sprintf("serial %s, MAC %s", orUnknown(c.facts.Serial), orUnknown(c.facts.MAC)))
}

// enable raises the session from ">" to "#".
//
// CAPTURED 2026-08-03: enable ASKS FOR A PASSWORD on this platform and the
// login password answers it. An empty answer is refused — the assumption this
// package shipped with, and the reason a whole serial run stalled.
func (c *Client) enable(ctx context.Context, secret string) {
	var serr = c.cli.sendLine("enable")
	if serr != nil {
		c.step(false, "enable could not be sent", firstLine(serr.Error()))
		return
	}
	var out, kind, err = c.cli.waitAny(ctx, cmdTimeout)
	if err != nil {
		c.step(false, "no answer to \"enable\"", firstLine(err.Error()))
		return
	}
	if kind == "password" {
		if err = c.cli.sendLine(secret); err != nil {
			c.step(false, "enable password could not be sent", firstLine(err.Error()))
			return
		}
		out, _, err = c.cli.waitAny(ctx, cmdTimeout)
		if err != nil {
			c.step(false, "enable password refused or unanswered", firstLine(err.Error()))
			return
		}
	}
	c.privileged = rePriv.MatchString(strings.TrimRight(tail(out, 200), " \t\r\n"))
	if c.privileged {
		c.step(true, "privileged mode (#)", "\"enable\" answered with the login password")
		return
	}
	c.step(false, "still in user mode after \"enable\"",
		"reads that need privilege will fail")
}

// requireWritable is the refusal common to every write.
func (c *Client) requireWritable() error {
	if !c.known {
		return driver.NotSupported("ubiquiti-edgeswitch", "write to this device",
			"the CLI was not recognized at login")
	}
	if !c.privileged {
		return driver.NotSupported("ubiquiti-edgeswitch", "write to this device",
			"the session never reached privileged mode: \"enable\" did not produce a \"#\" prompt")
	}
	return nil
}

// WritesExtra: PoE (poe.go) and the PoE power cycle (portadmin.go).
//
// "poe" is declared writable: ApplyPoE's command forms — "poe opmode
// auto/passive24v/shutdown", the confirmation a passive class asks, and the
// two rejection forms a PoE-incapable or 4-pair-incapable port answers with —
// were exercised live on the lab EdgePoint S16, 2026-08-05, on both a
// PoE-capable port (0/5) and a non-capable one (0/1), and reverted to the
// state found. Nothing claims success without a re-read.
//
// "port_power_cycle" is declared because this is the ONLY connector in the
// fleet that can honestly promise the restore half of a cycle: it rides entirely
// on the two forms above — read the class, send "shutdown", send the class
// back — so the write vocabulary is the one already proven live, and
// [Client.CyclePortPower] verifies the restore with the same retried re-read
// ApplyPoE uses.
//
// Declared under its own name rather than inherited from "poe" on purpose: two
// other connectors declare "poe" and implement no cycler, so a screen keyed on
// "poe" would offer the button on platforms that can only refuse it (cf.
// driver/portadmin.go, § on the name).
//
// WHAT IS STILL UNOBSERVED, and it is the branch that matters: every capture was
// taken with nothing plugged in, so cutting a jack that is ACTUALLY DELIVERING
// and putting the same class back has never been seen end to end. TODO.md carries
// it. The gesture refuses a non-capable port and a port already at "none", and
// a failed restore names the port and the class it owes — so the failure is loud,
// not silent.
//
// "port_admin" is declared since 2026-08-06, on an operator decision, and the
// evidence behind it is DEDUCED: "shutdown" / "no shutdown" inside
// "interface 0/N" comes from this CLI family's grammar (FASTPATH-derived, cf.
// the package doc comment) and from the "Admin Mode" column portadmin.go reads
// back — but every port of every capture in testdata/ reads Admin Mode
// "Enable", the word "Disable" has never been seen on this hardware, and no
// session has ever sent "shutdown" to a physical jack here.
//
// What makes it declarable is that the failure is loud on both sides. A rejected
// command is reported per port in [driver.ApplyResult.Skipped], naming the form as
// deduced; a command accepted with nothing changed is caught by the post-write
// re-read, which this CLI has earned (ssh.go's rePrompt records it executing a
// command perfectly well while the session read it as refused). And the write is
// deliberately not saved with "write memory confirm", so a reboot brings the
// port back.
//
// The guardrail is the safety net that makes this survivable on a range reached
// over SSH and nothing else: portadmin.go refuses to shut a port carrying a LIVE
// LINK in the management VLAN — untagged or tagged — without
// [driver.PortAdminPlan.Force], and spares every linked port when "show network"
// yields no readable "Management VLAN ID". On this platform a zero there is a
// FAILED READ, never permission.
//
// "port_admin" is a separate name from "port_power_cycle" and neither implies
// the other: this is the only connector that declares both, and they are different
// gestures on different hardware paths (cf. driver/portadmin.go's § on the name).
//
// TODO.md names the capture that would retire the deduced status — and the
// "AdminUp" repair in parseShowPortAll that portadmin.go works around.
//
// # "dhcp_snooping", declared 2026-08-08
//
// The last capability to join, and the only one here that was proven before it
// was declared rather than the other way round. On the lab EP-S16 (1.9.3), all
// four gestures went through [Client.ApplyDHCPSnooping] itself — global enable,
// per-VLAN enable, trust, and trust removal — each verified by the contract's own
// re-read, and "write memory confirm" ran inside the apply so the protection
// survives a reboot. The switch was returned to its exact prior state, saved.
//
// One finding from that exercise is worth carrying here, because it decides
// whether the feature works at all: A TRUSTED PORT MUST PARTICIPATE IN A SNOOPED
// VLAN. Outside one, the write lands in the running configuration and the switch
// does not even list it — a success that protects nothing. The API layer is what
// makes that impossible, by deriving the VLANs to inspect from the ports the
// gesture names (cf. api/dhcpsnoop_apply.go, dhcpSnoopVLANsFor); this connector
// enables exactly what the plan asks for and nothing wider.
//
// Still unexercised, and named in TODO.md rather than hidden here: a REFUSAL — a
// port this platform declines to trust — has never been observed, so
// [driver.ApplyResult.Skipped] carries a path no device has walked.
func (d *Driver) WritesExtra(name string) bool {
	return name == "poe" || name == "port_power_cycle" || name == "port_admin" ||
		name == "dhcp_snooping" || name == "storm_control"
}

// Supports: the optional gestures of this platform, computed from the method
// sets and never listed by hand — see driver.GesturesOf.
func (d *Driver) Supports() []driver.Gesture {
	return driver.GesturesOf(d, (*Client)(nil))
}

// Identify: WHICH device answers here. Nothing is read without a credential.
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

func (c *Client) identity(ctx context.Context) Identity {
	var id = Identity{
		Serial:   c.facts.Serial,
		Firmware: c.facts.Firmware,
		Model:    c.facts.Model,
		Source:   "ssh_cli",
	}
	var groups, err = c.portGroups(ctx)
	if err == nil {
		id.PortGroups = groups
	}
	// PoE: READ, not guessed from the model string. The lab unit is the
	// demonstration why: "EdgePoint S16" carries neither a wattage suffix nor
	// the word "PoE", and twelve of its sixteen sockets have real PSE
	// hardware behind them (cf. poe.go). Best-effort — a failure here must
	// not fail identification of a device that has SSH but, say, a session
	// too busy to answer one more "show" this cycle.
	var poe, poeErr = c.ReadPoE(ctx)
	if poeErr == nil {
		var p driver.PoEPortState
		for _, p = range poe.Ports {
			if p.Capable {
				id.PoE = true
				break
			}
		}
	}
	return id
}

// Inspect is the discovery-time deep read, with the one credential that
// already opened the door.
func Inspect(ctx context.Context, host string, d driver.Dialer, user, secret string) (Identity, error) {
	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, driver.Target{Host: host, Dialer: d},
		[]Candidate{{Username: user, Secret: secret, Why: "credential accepted by the probe"}})
	var c, _ = adm.(*Client)
	if err != nil {
		return Identity{}, err
	}
	defer c.Close()
	var id = c.identity(ctx)
	if id.Serial == "" && id.PortGroups == nil {
		return id, fmt.Errorf("connected but nothing read")
	}
	return id, nil
}

func (c *Client) Connector() driver.Info { return (&Driver{}).Info() }
func (c *Client) Account() string        { return c.User }
func (c *Client) Accepted() Candidate    { return c.accepted }
func (c *Client) Transcript() []Step     { return c.Steps }

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
		strings.Contains(s, "auth fail") ||
		// FASTPATH answers a wrong password on the keyboard-interactive path
		// with USERAUTH_FAILURE (51) where the client expects INFO_REQUEST
		// (60); Go words that "unexpected message type 51 (expected 60)".
		// Observed on unifimoule1 2026-08-14 — without this line the refusal
		// read as "unreachable" and the candidate loop stopped at the FIRST
		// secret instead of walking the vault generations and the fallback
		// account.
		strings.Contains(s, "unexpected message type 51")
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
