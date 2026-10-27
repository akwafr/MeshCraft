package eos

// Identity on the ALREADY-OPEN access (driver.IdentityReader), the re-read twin
// of [Driver.Identify]. Kept in its own file on purpose: contract.go carries the
// discovery-path identification and moves for other reasons.

import (
	"context"

	"meshcraft/internal/driver"
)

var _ driver.IdentityReader = (*Client)(nil)

// ReadIdentity returns what `show version` established AT LOGIN — model, serial,
// MAC, EOS release. ZERO wire round-trips: the facts are already in memory, which
// is what earns this connector the interface (cf. driver.IdentityReader). The
// faceplate deliberately stays out — [Client.ReadFaceplate] exists for it and
// costs a real command; identity refresh must stay free.
func (c *Client) ReadIdentity(_ context.Context) (driver.Identity, error) {
	return driver.Identity{
		Model:    c.facts.Model,
		Serial:   c.facts.Serial,
		MAC:      c.facts.MAC,
		Firmware: c.facts.Release,
		Source:   "ssh_cli",
	}, nil
}
