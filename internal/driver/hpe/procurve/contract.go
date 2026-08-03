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

// Compile-time: this Driver is a Connector, and its Client is an Admin.
var (
	_ driver.Connector = (*Driver)(nil)
	_ driver.Admin     = (*Client)(nil)
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

// WritesExtra: nothing beyond VLAN membership, and VLAN membership itself is not
// written yet either (cf. ApplyPorts). Spanning tree, PoE and port security are
// all writable on this platform and none is implemented — declaring one would
// announce configuration that never arrives.
func (d *Driver) WritesExtra(name string) bool { return false }

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
