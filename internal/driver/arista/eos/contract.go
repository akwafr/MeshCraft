package eos

// Adoption of the device-access contract (driver/connector.go) by the Arista EOS
// connector. The structure follows cisco/ios: a real shell SESSION, opened once
// by Authenticate and reused for every gesture of the access.
//
// # The dialect gate, and what it protects against HERE
//
// Every CLI connector in this product carries a gate; they do not all guard the
// same risk. On the FS connector it is two incompatible CLIs shipped under one
// product line. On a Catalyst it is the dozen white-label switches that imitate
// the IOS command set without sharing its semantics.
//
// On EOS the risk is the MIRROR of the Catalyst one, and it is worth naming
// because it points the other way. EOS is itself an IOS look-alike — deliberately
// so — which means the danger is not that something else gets mistaken for an
// Arista, but that an ARISTA gets driven with the IOS command set that "obviously"
// works. Four of the five commands a provisioning run leans on are subtly
// different here (cf. the package doc), and every one of them fails in the quiet
// direction: a rejected line in the middle of a block, a gateway that never gets
// written, an address the device refuses while the transcript scrolls past.
//
// So: `show version` must announce Arista ([reIsEOS]). If it does not, reads stay
// best-effort and WRITES ARE REFUSED with the way forward.
//
// # And the second gate, which is about a STATE rather than a dialect
//
// A switch in ZERO TOUCH PROVISIONING answers, has a prompt, and takes commands
// — and throws away everything written to it at its next ZTP cycle. That is not a
// dialect problem and the dialect gate would not catch it, so it has a check of
// its own ([Client.postLogin]) and its own refusal. A connector that wrote a full
// management push into a ZTP console would report a flawless run against a switch
// that comes back factory-fresh minutes later, and the operator would have no way
// to tell that from a device that never received anything.
//
// # And the third: MOST of this has still not been run on hardware
//
// Every session carries a transcript step saying which half is which. It is not
// decoration: the transcript is what an operator reads when a write did something
// unexpected, and "this command set is documented, not captured" is the first
// thing that should be known at that moment.
//
// The step no longer says "nothing", because that stopped being true on
// 2026-08-08 — [Client.EnsureAccount] and [Client.ApplyStormControl] have both
// written to core01.lab and been read back. It names what HAS run instead,
// which is the only version of that sentence an operator can act on: a blanket
// disclaimer over a connector that has landed two write gestures teaches the
// reader to skip it.

import (
	"context"
	"fmt"
	"regexp"
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

// Compile-time assertions: a signature drift shows up at build time, not in the
// field.
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
	dialectEOS     = "eos"
	dialectUnknown = ""
)

// reZTPActive matches `show zerotouch` reporting that ZTP is still armed.
//
// Tolerant on purpose: the wording of this one-line report has changed across
// trains ("ZeroTouch: on", "ZeroTouch provisioning mode is enabled"), and a
// pattern pinned to one of them would silently stop protecting against the state
// it exists to catch. The cost of a false positive here is a refused write and a
// message naming the command that clears it; the cost of a false negative is a
// provisioning run thrown away.
var reZTPActive = regexp.MustCompile(`(?i)zerotouch[^\n]*\b(?:on|enabled|active|armed)\b`)

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
	// ztp: the device answered that Zero Touch Provisioning is still armed.
	// Writes are refused (cf. requireWritableDialect) — everything written to a
	// ZTP switch is discarded at its next cycle.
	ztp bool
}

func (c *Client) step(ok bool, what string, detail ...string) {
	c.Steps = append(c.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// Authenticate opens a shell session with the first accepted candidate.
//
// The order comes from the caller and is never reordered; no candidate is
// replayed (the try budget protects against account lockout). Access is returned
// EVEN on failure, so the transcript survives.
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
			// The account does not carry a privileged role: raise the session with
			// the SAME secret, which is what EnsureAccount aligns `enable password` on.
			var enErr = cli.enable(ctx, cand.Secret)
			if enErr != nil {
				c.step(false, "user EXEC only: \"enable\" failed", firstLine(enErr.Error()))
				c.step(false, "reads and writes will be refused",
					"this account landed on \">\" and its secret does not open privileged "+
						"EXEC. Give it \"role network-admin\", or align \"enable password\" on it")
				return c, fmt.Errorf("account %q on %s reaches user EXEC only: %v",
					cand.Username, tgt.Host, enErr)
			}
			c.step(true, "privileged EXEC reached via \"enable\"",
				"the account does not carry a privileged role — EnsureAccount would set it")
		}

		c.postLogin(ctx)
		return c, nil
	}

	return c, fmt.Errorf("no credential accepted by %s after %d attempt(s) — %s",
		tgt.Host, len(tried), strings.Join(refusals, " ; "))
}

// postLogin: pagination and vty logging cut, then the identity read and the ZTP
// check. This is where the dialect gets decided — every write gesture consults it.
func (c *Client) postLogin(ctx context.Context) {
	// "terminal length 0" is universal here; a refusal only costs an error echo,
	// whereas active pagination would swallow every "show running-config".
	var _, tlErr = c.cli.run(ctx, "terminal length 0", cmdTimeout)
	if tlErr != nil {
		c.step(true, "terminal length 0 rejected — relying on the tall pty", firstLine(tlErr.Error()))
	}
	// Syslog on the vty interleaves with command output. EOS spells this the
	// Unix way round; the IOS spelling is tried second rather than instead,
	// because a connector that knew one form would leave the other's syslog
	// running through every apply. Best effort either way: [reCLIError] is
	// written not to mistake a syslog line for a refusal, so this is the second
	// lock and not the only one.
	var _, tmErr = c.cli.run(ctx, "no terminal monitor", cmdTimeout)
	if tmErr != nil {
		_, tmErr = c.cli.run(ctx, "terminal no monitor", cmdTimeout)
	}
	if tmErr != nil {
		c.step(true, "terminal monitor could not be turned off",
			"syslog may interleave with command output — the refusal parser is written for it")
	}

	var out, err = c.cli.run(ctx, "show version", cmdTimeout)
	if err != nil && out == "" {
		c.step(false, "show version failed", firstLine(err.Error()))
		return
	}
	if !reIsEOS.MatchString(out) {
		c.dialect = dialectUnknown
		c.step(false, "CLI dialect not recognized",
			"\"show version\" does not announce Arista EOS. This connector's command set "+
				"differs from the IOS one it resembles in exactly the places that matter — "+
				"the address form, the default route, the trunk encapsulation — so reads stay "+
				"best-effort and writes are refused until this device's own command set is "+
				"surveyed.")
		return
	}
	c.dialect = dialectEOS
	c.facts = parseShowVersion(out)
	c.step(true, "Arista EOS recognized",
		fmt.Sprintf("release %s, model %s, serial %s",
			orUnknown(c.facts.Release), orUnknown(c.facts.Model), orUnknown(c.facts.Serial)))

	c.checkZeroTouch(ctx)

	// The honesty step. Present on EVERY session, deliberately: the transcript is
	// read at the moment something behaved unexpectedly, and this is the first
	// fact that matters then.
	//
	// It names the exercised half rather than disclaiming everything (cf. the file
	// header). A step that said "nothing has been captured" on a connector that
	// has created an account and rate-limited a port on a live 7050SX would be
	// read once and skipped for ever after.
	c.step(true, "MOST WRITE forms documented, not captured",
		fmt.Sprintf("written against EOS %s from the vendor's command reference. EXERCISED on "+
			"EOS %s: the identity read, the ZTP check, the gNMI block and the four streaming "+
			"channels, the account gesture (EnsureAccount, including \"write memory\"), "+
			"storm control, PORT ADMIN — driven through ApplyPortAdmin on 2026-08-10, "+
			"down and back up on a dark cage, confirmed by re-read — and SPANNING TREE, "+
			"driven through ApplySpanningTree on 2026-08-12: the region, the mode and the "+
			"root half, read back as \"spanning-tree mst 0-1 priority 0\" with \"Root this "+
			"switch\" on both instances. NOT exercised, and therefore not declared "+
			"writable: the trap channel and the "+
			"management push. DHCP snooping is a separate case: its global and per-VLAN "+
			"levels are captured, but this hardware HAS NO PER-PORT TRUST LEVEL at all and "+
			"the connector refuses that intent rather than sending a command EOS does not "+
			"have. What each remaining capture would unlock is listed in TODO.md",
			DocumentedRelease, CapturedRelease))
}

// checkZeroTouch asks whether the device is still in Zero Touch Provisioning.
//
// ONE command, and it is worth it: a ZTP switch takes every command and keeps
// none of them. Being told that before a write, in the transcript, is the
// difference between "the provisioning run was refused, run zerotouch cancel" and
// a switch that comes back factory-fresh an hour later with a green transcript
// behind it.
//
// A device that does not answer the command is NOT assumed to be in ZTP: older
// trains do not carry `show zerotouch` at all, and refusing every write on a
// switch that is perfectly configured would be the worse error. The absence is
// recorded and nothing is claimed either way.
func (c *Client) checkZeroTouch(ctx context.Context) {
	var out, err = c.cli.run(ctx, "show zerotouch", cmdTimeout)
	if err != nil && out == "" {
		c.step(true, "ZTP state not read",
			"\"show zerotouch\" did not answer ("+firstLine(err.Error())+"). This train may "+
				"not carry the command; nothing is assumed either way")
		return
	}
	if !reZTPActive.MatchString(out) {
		return
	}
	c.ztp = true
	c.step(false, "THIS SWITCH IS STILL IN ZERO TOUCH PROVISIONING",
		"it answers, it has a prompt, and it will DISCARD everything written to it at its "+
			"next ZTP cycle — configuration typed here does not survive. Writes are refused. "+
			"\"zerotouch cancel\" on the console ends it: that command writes an empty "+
			"startup-config and reboots, after which the switch keeps what it is given")
}

// requireWritableDialect: the refusal common to every write.
//
// TWO conditions and not one. The dialect gate is the ordinary one every CLI
// connector has; the ZTP gate is this platform's own, and it refuses a device
// that would ACCEPT every command — which is precisely why it cannot be left to
// the caller to notice.
func (c *Client) requireWritableDialect() error {
	if c.ztp {
		return driver.NotSupported("arista-eos", "write to this device",
			"this switch is still in Zero Touch Provisioning: it accepts commands and "+
				"discards them at its next ZTP cycle, so a write here would report success "+
				"and leave nothing behind. Run \"zerotouch cancel\" on its console — it "+
				"writes an empty startup-config and reboots — and the device becomes "+
				"writable")
	}
	if c.dialect == dialectEOS {
		return nil
	}
	return driver.NotSupported("arista-eos", "write to this device",
		"\"show version\" did not announce Arista EOS at login. This connector's command "+
			"set is EOS-specific, and it differs from the IOS set it resembles in exactly "+
			"the lines a provisioning run depends on — the address is a prefix, the gateway "+
			"is a route, and there is no trunk encapsulation. Writing here could configure "+
			"something other than what was asked. Survey this device's actual command set "+
			"and add it before meshcraft writes anything to it")
}

// WritesExtra: two intents, each on its own capture, and the rest still staged.
//
// [Client.ApplySpanningTree], [Client.ApplyPortAdmin] and
// [Client.ApplyDHCPSnooping] are all IMPLEMENTED in this package.
// [driver.DHCPSnoopApplier] states the rule this follows verbatim: "a connector
// whose forms are deduced from vendor documentation ships the code, leaves
// WritesExtra false, and names the unverified form in TODO.md". A declaration is
// therefore a statement about HARDWARE, per gesture, and never about how well the
// code reads or how green the suite is.
//
// "STORM_CONTROL" IS DECLARED, and it was the first on this range (2026-08-08).
// It earned it the way the rule asks: [Client.ApplyStormControl] itself wrote all
// three classes to Ethernet1 of the lab core01.lab — a cage with no
// transceiver fitted, so a limiter there polices nothing whatever the rate — the
// device reported them back at the rates asked for, a SECOND identical apply
// wrote nothing (the convergence the rate spelling threatened, cf.
// stormcontrol.go), a removal through the same applier took one class away
// without disturbing the two the plan did not name, and the port was left clean.
//
// # "PORT_ADMIN" IS DECLARED, AND SINCE 2026-08-10 THE APPLIER ITSELF IS PROVEN
//
// core01.lab, DCS-7050SX-64-R, EOS 4.28.13.1M, 192.0.2.5, over the
// controller's SOCKS route as `mcadmin`. Ethernet1 — the same dark cage, no
// transceiver fitted, "notconnect" / "Not Present" — was shut with
// `configure` / `interface Ethernet1` / `shutdown` / `end`, and "show interfaces
// status" answered "disabled". "no shutdown" put it back to "notconnect".
// Running configuration only; nothing was copied to startup.
//
// That settles what was missing and it was the important half: the WORD. This
// connector's whole verify path turns on "disabled", a word no fixture in this
// repository could establish, and one that had to be told apart from the
// "notconnect" the same port printed a minute earlier. Both lines are pinned
// verbatim in live_portadmin_test.go.
//
// THAT CAPTURE WAS TYPED AT A CLI, and on its own it left the applier unproven.
// TestLiveWritePortAdmin (live_portadmin_test.go) was RUN the same day with
// EOS_LIVE_WRITE=1, on the same Ethernet1, and closed it. What it added is exactly
// what the gap was: the commands [Client.ApplyPortAdmin] emits are the ones the
// device accepts, in the applier's own order and through its own
// configuration-block handling; its verification re-read reached the same verdict
// an INDEPENDENT read did (PortsWritten=[ET1], and "show interfaces status"
// agreeing on "disabled"); a second identical plan wrote NOTHING, so the gesture
// converges instead of flapping the port on every automatic pass; and the way back
// up works through the applier too, leaving the port at "notconnect" where it
// was found.
//
// ONE BRANCH REMAINS FAKE-CLI ONLY, and it is named rather than glossed: the
// management-VLAN refusal. It fires on a port that has a LIVE LINK *and* carries
// the management VLAN, and on core01 the only linked ports are the four uplinks
// the session arrives over. Proving it on hardware means shutting the jack the
// prover is sitting on, so it will stay proven by portadmin_test.go.
//
// A SECOND FINDING FROM THAT RUN, because it changes what the guardrail means in
// practice: on core01 EVERY faceplate port carries "switchport mode trunk" with
// "switchport trunk allowed vlan 1,42,98-100", and 99 is the management VLAN. So
// carriesMgmtVLAN is TRUE for all 48 ports, and that is the ordinary shape of an
// Arista rather than a peculiarity of this lab. The applier is unaffected — its
// guardrail is the CONJUNCTION with a live link, so a dark trunk is still shut on
// request. It was live_portadmin_test.go's own scratch-port picker that applied the
// VLAN half alone and consequently refused the entire faceplate; it now admits a
// port whose cage is EMPTY ("Not Present" in the Type column), which is a
// physical fact rather than a re-derivation of the code under test.
//
// The remaining intents stay false, and "no command has ever reached an Arista"
// is not the reason — that sentence was true when this comment was first written
// and has not been since 2026-08-08. The reason is narrower and per intent:
// nothing has exercised THEM. TODO.md names the captures.
//
// "STP" IS THE CASE WHERE THAT DISTINCTION IS EASIEST TO GET WRONG, so it is
// spelled out. Its READ side is finished: the last two shapes TODO.md owed were
// captured on 2026-08-10 and are pinned as files in testdata/ (a three-instance
// `show spanning-tree`, and a `mst configuration` whose VLAN list wraps over six
// lines), they confirmed the 2026-08-07 instance-leak fix on real output, and
// they found and closed a real defect in the wrapped-list parse. None of that
// moves this flag. Both were produced by typing at the CLI;
// [Client.ApplySpanningTree] was not involved, and the entry's bar is the mode
// + region WRITE being seen through the applier. Cf. the STATUS section at the
// top of stp.go.
// "STP" IS DECLARED SINCE 2026-08-12, AND THE REASON IS A ROOT BRIDGE THAT WAS
// DESIGNATED AND NEVER WRITTEN.
//
// meshcraft has carried a site-level root-bridge designation the whole time
// (internal/driver/rootbridge.go, `stp_root_bridges` in the site plan) and
// NOTHING consumed it at apply time: `rootPlanOf` had no caller outside its own
// handlers, so naming core01.lab the root of its region stored a row, drew a
// screen, and left the switch exactly as it was. On this platform "be the root"
// is `spanning-tree mst <inst> priority 0` plus `spanning-tree root super`, both
// of which this package already builds — behind a flag that said do not send.
//
// So the flag moves for one intent's sake, and the cost is stated rather than
// discovered: this key also carries the MODE, the MST REGION and the per-port
// writes onto the automatic apply path. Two of those remain documentation-derived
// (`spanning-tree mst 0 priority N` under rstp, `spanning-tree portfast auto`),
// and the region write is the one live_stp_test.go declines on core01 because it
// restarts the MST state machine under four live trunks. That harness's refusal
// stands and is now the ONLY thing standing between an automatic pass and a
// region write, which is thinner than it was — TODO.md carries what to capture
// next.
//
// WHAT IS PROVEN, on hardware, through this package's own applier rather than by
// typing: see the STP section of TODO.md for the run.
func (d *Driver) WritesExtra(name string) bool {
	return name == "storm_control" || name == "port_admin" || name == "stp"
}

// Supports: the optional gestures of this platform, computed from the method
// sets and never listed by hand — see driver.GesturesOf.
func (d *Driver) Supports() []driver.Gesture {
	return driver.GesturesOf(d, (*Client)(nil))
}

// Identify: WHICH device answers at this address. Like every other CLI connector
// here, NOTHING is read without a credential: the CLI sits behind SSH
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
		if isCage(p.Type, p.SpeedMbps) {
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

// Close closes the shell session. Idempotent, per the contract: calling it twice,
// or on an access that never fully opened, must never panic.
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
