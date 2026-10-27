package routeros

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Setting the device's OWN name — the "identity" on this platform.
//
// # One object, no `.id`, and that is the whole peculiarity
//
// `/system/identity` is a UNIQUE object, not a list: the read returns
// `{"name":"lan-r4"}` (captured — testdata/system_identity.json, and it is also
// the cheapest authenticated probe this connector has, cf. authProbe). It is
// therefore modified by `set` WITHOUT an `.id`, the only place in this connector
// where that is legitimate. The wire form is `POST /rest/system/identity/set`
// with `{"name":"…"}`, which is RouterOS's own console-command mapping — every
// menu command is a POST on `<menu>/<command>` (cf. rest.go, § on why POST and
// not PATCH). The vendor-documented PATCH-on-the-object variant is NOT used: this
// connector has exactly one way of writing, and adding a second HTTP verb for one
// field would leave two paths where one is exercised.
//
// # CAPTURED / DEDUCED — and the two halves of this repository DISAGREE
//
// READ: captured (testdata/system_identity.json).
//
// WRITE: the API layer records it as PROVEN. api.hostnameWritableConnectors lists
// `mikrotik-routeros` among the two entries "PROVEN ON HARDWARE, both by a live
// rename observed converging", dated 2026-08-05 23:24 on `lab-crs326` — "which
// was answering to "lan-r4"; both observation sources agree afterwards", i.e. a
// re-read, which is the only thing that may turn a write into a claim.
//
// CORROBORATED HERE AT LAST — 2026-08-10, and the disagreement is over.
//
// Until then no transcript, no fixture and no dated note in this package recorded
// the POST going out, so this file refused to call the write proven while
// api.hostnameWritableConnectors did. The run that settles it, done exactly as
// the note below prescribed: `lab-crs326` was renamed through meshcraft to
// "lab-crs326-rn", `device.hostname_set` fired — "System name set on
// "lab-crs326-rn": "lab-crs326-rn" — pushed because the device was renamed in
// meshcraft" — and BOTH observation sources then reported the new name,
// `extra.identity.hostname` AND `lldp_self.sys_name`. Renamed back, both
// converged again to `lab-crs326`.
//
// So the write goes out, the device takes it, and the re-read reports it. The
// old objection is recorded because it was RIGHT to make: the sentence this file
// used to doubt on — "the only channel to this device from the development host
// is restricted to GET/HEAD" — is true and is not an argument about the write.
// It constrains FIXTURE CAPTURE from the development host, not the controller's
// own path through the proxy, which has since written to this exact unit
// (storm control on ether2, 192.0.2.54, RouterOS 7.21beta3, 2026-08-07).
//
// # No Persist, and that is information rather than a gap
//
// This platform is [driver.CommitImmediate] (cf. routeros.go): there is nothing
// to save and therefore no safety net either. The write is in effect the instant
// it is accepted, which also means the identity cannot be staged and rolled back
// the way an Instant On change can.
//
// # What is NOT done
//
//   - Nothing touches `/system/note`, the DNS name, or any DHCP-client hostname:
//     they are separate facts, and one button writing three of them is how a
//     fleet acquires configuration nobody asked for.
//   - No ceiling of our own beyond driver.ValidHostname's 63 bytes. RouterOS
//     accepts a long identity and nothing in this project captures where it
//     stops, so an invented number would refuse names the device takes. A name
//     the device shortens on its own is caught by the re-read, which compares
//     byte for byte. Nothing is truncated on our side.
var _ driver.HostnameSetter = (*Client)(nil)

// hostnameMismatch explains a re-read that does not carry the requested name.
func hostnameMismatch(want, got string) string {
	if got == "" {
		return "the REST API accepted the write but /system/identity re-reads with " +
			"no name at all: a 2xx that changes nothing is the failure mode this " +
			"re-read exists to catch"
	}
	if len(got) < len(want) && strings.HasPrefix(want, got) {
		return fmt.Sprintf("the device stored %q — the requested name cut at %d "+
			"bytes. That is this platform's own ceiling, learned here rather than "+
			"guessed; the router now answers to a name meshcraft does not carry, so "+
			"send a name of at most %d bytes", got, len(got), len(got))
	}
	return fmt.Sprintf("/system/identity re-reads as %q, not %q: the write did not "+
		"take the way it was sent", got, want)
}

// SetHostname sets the device's own system name — driver.HostnameSetter.
//
// Write then re-read; no save, because there is nothing to save on this platform.
// The re-read is not a formality: on a write form that has never been exercised
// on hardware it is the only thing standing between "the API answered 200" and
// "the device is renamed".
func (c *Client) SetHostname(ctx context.Context, name string) error {
	// The shared floor first. This connector puts the name in a JSON body rather
	// than a command line, so injection is not the risk here — but a caller that
	// got a name past this would produce an identity no CLI platform in the fleet
	// could carry, and the whole point of the floor is that one name works
	// everywhere.
	if !driver.ValidHostname(name) {
		return fmt.Errorf("system name %q refused: it must carry only letters, "+
			"digits and hyphens, must not start or end with a hyphen, and must fit "+
			"in 63 bytes — this connector does not normalize it, because a device "+
			"answering to a name meshcraft did not choose breaks the LLDP "+
			"correlation this gesture exists to enable", name)
	}

	// SetIdentity is the existing wire call, and it is deliberately reused rather
	// than reimplemented: two places building the same POST is how the two
	// eventually disagree.
	var err = c.SetIdentity(ctx, name)
	if err != nil {
		c.step(false, "system name NOT set", err.Error())
		return fmt.Errorf("system identity on %s: %w", c.host, err)
	}

	var row Row
	row, err = c.Object(ctx, "system/identity")
	if err != nil {
		c.step(false, "system name not re-read", err.Error())
		return fmt.Errorf("identity %q sent to %s but NOT re-read (%v): meshcraft "+
			"cannot confirm the device carries it", name, c.host, err)
	}
	var got = row.Get("name")
	if got != name {
		var why = hostnameMismatch(name, got)
		c.step(false, "system name not confirmed", why)
		return fmt.Errorf("system identity on %s: %s", c.host, why)
	}
	c.step(true, "system name confirmed by re-read", "/system/identity name = "+got)
	return nil
}
