package procurve

// The Connector contract for this range: it authenticates, it READS, and it
// refuses to write.
//
// # Why a connector that refuses is the right shape here
//
// driver/connector.go draws the line, and it is not a formality: a capability
// every switching platform HAS but that a connector has not coded yet is a
// MANDATORY method returning [driver.ErrNotImplemented] — never an empty
// success, because "the caller has nothing else to decide besides displaying the
// failure", and a write reported as applied that never arrived is the one outcome
// the contract exists to prevent.
//
// Every command this range needs is documented and none has been run by this
// connector: the reference unit did not complete an SSH login (cf. ssh.go). So
// reads are attempted — a wrong read is visible and costs nothing — and writes
// refuse with the reason, until one session on the hardware confirms the prompts,
// the paging behaviour and the refusal shapes.
//
// The moment that survey happens, ApplyPorts and SetSecret are a small amount of
// work on top of what is already here. Shipping them BEFORE it would be shipping
// a guess with a switch's uplink riding on it.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"meshcraft/internal/driver"
)

// Compile-time: everything this connector claims it can do.
//
// These assertions are the reason a capability cannot quietly disappear. Every
// one of them is reached through an `As…` helper at the call site, so dropping a
// method does not break a build — it makes the feature vanish from the UI with
// nothing to say why.
//
// [driver.IdentityReader] IS DELIBERATELY ABSENT, and this is the place a reader
// checks before filing that as a gap again: identity on this range costs three
// commands the open session has not already paid for, so the interface's
// admission ticket — cheapness — is not met. [Driver.Identify] is the adapter
// this connector carries instead, and api.observeIdentity does reach it. The
// full argument, with what would have to change to earn the other one, is on
// [Client.identity] in inspect.go.
var (
	_ driver.Connector = (*Driver)(nil)
	_ driver.Admin     = (*Client)(nil)
	// Naming and drawing: the two halves of one translation, checked against
	// each other by TestConnectorFaceplatesAgreeWithTheirOwnPortNamer.
	_ driver.PortNamer       = (*Driver)(nil)
	_ driver.Identifier      = (*Driver)(nil)
	_ driver.FaceplateReader = (*Client)(nil)
	// LLDPReader: the CLI neighbour read wired into the read-state gesture
	// (2026-08-10, cf. lldp.go). Pure wiring — no new command, no new session,
	// and the range stays parked for writes.
	_ driver.LLDPReader = (*Client)(nil)
	// Reads and writes proven on SW-PLAYER-79, 2026-08-03.
	_ driver.SNMPConfigurator    = (*Client)(nil)
	_ driver.Persister           = (*Client)(nil)
	_ driver.MgmtAddrReader      = (*Client)(nil)
	_ driver.MgmtAddrWriter      = (*Client)(nil)
	_ driver.SpanningTreeReader  = (*Client)(nil)
	_ driver.SpanningTreeApplier = (*Client)(nil)
	_ driver.WebUIAddresser      = (*Driver)(nil)
)

type (
	// Step, Candidate and friends are ALIASES, not new types: the contract's
	// shapes travel unchanged into the API response the frontend reads.
	Step      = driver.Step
	Candidate = driver.Candidate
	State     = driver.State
	PortPlan  = driver.PortPlan
)

// Client is a proven administration access to one ProCurve.
type Client struct {
	client *ssh.Client
	// cli is the ONE interactive shell this connection gets: the device refuses a
	// second pty (cf. cli.go). Every command goes through it.
	cli      *cliConn
	tgt      driver.Target
	User     string
	accepted Candidate
	Steps    []Step
}

func (c *Client) step(ok bool, what string, detail ...string) {
	var d string
	if len(detail) > 0 {
		d = detail[0]
	}
	c.Steps = append(c.Steps, Step{What: what, Detail: d, OK: ok})
}

// Authenticate tries the candidates IN THE CALLER'S ORDER and stops at the first
// that opens a shell.
//
// THE ORDER IS NEVER REORDERED and no candidate is replayed or invented — the
// contract's rule, and it matters more on this range than on most: a ProCurve
// locks an account after repeated failures, and a 2610 whose manager password is
// locked out is recovered by walking to it and holding two buttons.
//
// The loop advances ONLY on an authentication refusal. A TCP failure or a dead
// tunnel aborts immediately: walking the list against a switch that never
// answered would burn the budget on a fault that has nothing to do with
// passwords, and report "no credential accepted" about a device that was simply
// unreachable.
func (d *Driver) Authenticate(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Admin, error) {
	if strings.TrimSpace(tgt.Host) == "" {
		return nil, errors.New("no management address for this device")
	}
	if len(cands) == 0 {
		return nil, errors.New("no credential to present: meshcraft holds no secret for this device")
	}
	var c = &Client{tgt: tgt}
	var lastAuth error
	var cand Candidate
	for _, cand = range cands {
		var user = cand.Username
		if user == "" {
			// This range has no anonymous administration: an empty name is the
			// caller saying "whatever the platform calls it", and here that is
			// `manager` — the read-write level.
			user = "manager"
		}
		var client, err = dialSSH(ctx, tgt, user, cand.Secret)
		if err != nil {
			if isSSHAuthFailure(err) {
				c.step(false, "credential refused ("+cand.Why+")", firstLine(err.Error()))
				lastAuth = err
				continue
			}
			c.step(false, "SSH transport failed", firstLine(err.Error()))
			return c, fmt.Errorf("SSH to %s: %v", tgt.Host, firstLine(err.Error()))
		}
		var cli *cliConn
		cli, err = openCLI(ctx, client)
		if err != nil {
			_ = client.Close()
			c.step(false, "shell did not open", firstLine(err.Error()))
			return c, fmt.Errorf("SSH shell on %s: %v", tgt.Host, firstLine(err.Error()))
		}
		c.client = client
		c.cli = cli
		c.User = user
		c.accepted = cand
		c.step(true, "connected as \""+user+"\"", cand.Why)
		return c, nil
	}
	if lastAuth != nil {
		return c, fmt.Errorf("no credential accepted (%d tried, last refusal: %v)",
			len(cands), firstLine(lastAuth.Error()))
	}
	return c, errors.New("no credential accepted")
}

// WritesExtra names what an apply can carry BESIDES VLAN membership.
//
// "stp" is declared because [Client.ApplySpanningTree] exists, writes the mode,
// the bridge priority, the MST region and the per-port cost/edge/BPDU settings,
// and — this is the part that earns the declaration — VERIFIES every one of them
// by re-reading the device afterwards.
//
// PoE and port security are writable on this platform and neither is
// implemented. They are absent here rather than listed, because a name in this
// list is a promise that an apply will carry that configuration, and a promise
// that resolves to nothing is worse than a missing feature: it is a
// configuration reported as applied that never arrived.
//
// # `port_admin` — "disable" CAPTURED, "enable" DEDUCED, and NOTHING SENT LIVE
//
// Declared 2026-08-06 on an operator decision. The two halves of this pair do not
// have the same standing and the difference is structural to this range, not a gap
// somebody forgot to close:
//
//   - "disable" is CAPTURED, from the strongest source this platform has. The
//     reference unit's own running configuration (SW-PLAYER-79, a 2610-48 / J9088A
//     on R.11.121, read 2026-08-03) carries "interface 1 / disable / exit"
//     verbatim on a port a human had shut, and on this range the configuration file
//     IS the command language. Its read-back is captured too — the same unit prints
//     "Enabled No" for port 1 and "Yes" for the other fifty-one, so BOTH values
//     of the column portadmin.go reads exist in testdata/;
//   - "enable" is DEDUCED AND CAN NEVER BE OTHERWISE from a capture. A
//     configuration on this range records only a DEPARTURE from the defaults (cf.
//     reNoLLDP's comment), and an enabled port IS the default — so the word cannot
//     appear in a running-config however many switches are read. It is the
//     documented inverse, and nothing in this connector has ever sent it.
//
// SO THIS CONNECTOR IS DECLARED ON A DEDUCED VERB in the un-shut direction, and
// the only thing that would change that is a live exercise rather than another
// read. THERE HAS BEEN NO SUCH EXERCISE: this package has no live_*_test.go at
// all, unlike fs/s3400 or ubiquiti/edgeswitch, and THE LAB 2610 IS CURRENTLY
// OFFLINE, so it cannot be arranged today.
//
// THE ASYMMETRY IS THE RISK, AND IT POINTS THE WRONG WAY. If "enable" were the
// wrong word, the direction that fails is the RECOVERY one: a port shut by a
// captured verb that will not come back up by a deduced one. What keeps that
// bounded is that a refusal on this platform arrives as a "% Invalid input"
// line in the output, [cliRefusal] reads it, the port is reported in
// [driver.ApplyResult.Skipped] naming "enable" as the deduced half, and
// "write memory" is deliberately NOT sent — so a reboot restores whatever the
// startup configuration says. There is a serial console as well.
//
// MIND THE SESSION BUDGET when the unit comes back: this range allows FOUR
// concurrent sessions and, saturated, authenticates and then refuses the channel,
// which reads exactly like a bug (cf. testdata/show_ip_ssh.txt and Close). A cut
// session goes on holding one of the four until the switch times it out.
//
// TODO.md names the exercise that would retire the deduced half.
func (d *Driver) WritesExtra(name string) bool {
	return name == "stp" || name == "port_admin"
}

// Supports: the optional gestures of this platform, computed from the method
// sets and never listed by hand — see driver.GesturesOf.
func (d *Driver) Supports() []driver.Gesture {
	return driver.GesturesOf(d, (*Client)(nil))
}

func (c *Client) Connector() driver.Info { return (&Driver{}).Info() }
func (c *Client) Account() string        { return c.User }
func (c *Client) Accepted() Candidate    { return c.accepted }
func (c *Client) Transcript() []Step     { return c.Steps }

// Close releases the SSH session. This range allows five, and an abandoned one
// is a session the next operator does not get.
func (c *Client) Close() {
	if c.cli != nil {
		c.cli.close()
	}
	if c.client != nil {
		_ = c.client.Close()
	}
}
