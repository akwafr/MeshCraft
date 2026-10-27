package s3400

// Identity on the ALREADY-OPEN access (driver.IdentityReader), the re-read twin
// of [Driver.Identify]. In its own file so contract.go keeps only the
// discovery-path identification.
//
// # Why this connector gained the interface on 2026-08-10
//
// Not for the model — `show version` gave that away at login already. For the
// SYSTEM NAME. This range had NO IdentityReader, so the only way into
// `extra.identity` was api.observeIdentity's [driver.Identifier] fallback, which
// runs solely while `devices.model` is EMPTY — at most once per device, on the
// first read-state after adoption, at the price of a whole second SSH session.
// Combined with an S3400's silence on LLDP (its agent serves nothing under
// `1.0.8802`, and the CLI LLDP path writes no `sys_name`), that left every
// S3400 permanently `not_observed`: meshcraft pushed a name at each rename and
// had no way to confirm the switch had stored it, stored it whole, or stored it
// at all. The verification was the gap, not the write.
//
// Implementing this interface fixes both halves at once: identity now comes off
// the session the caller already holds (no second authentication cycle), and it
// carries the name.
//
// # WHERE THE EXPENSIVE READ LIVES — read this before adding anything here
//
// The device's own name is in `show running-config` and NOWHERE cheaper: the
// login banner carries the product name, `show version` the model, serial, base
// MAC and firmware, and neither holds the hostname. On this range that command
// is a NINETY-SECOND PAGED READ — "terminal length 0" is accepted silently and
// changes nothing here, so every page costs a round trip through
// [cliConn.runPaged] (cf. ssh.go, § "The pager is FED, not disabled"), and
// "| include" is documented unreliable on this firmware, so it cannot be
// narrowed.
//
// That read is therefore NOT performed here — cheapness is this interface's
// admission ticket (cf. driver.IdentityReader), and a ReadIdentity that quietly
// took ninety seconds would break every caller that trusts the contract. It is
// HARVESTED from the one already paid for: [Client.ReadState] runs
// `show running-config` on every read-state gesture and hands the parsed name to
// [Client.noteSysName]; this method reports it from memory.
//
//	cadence: once per read-state gesture (clicked or scheduled), on the
//	         running-config read that gesture already performs;
//	cost:    ZERO additional round trips — and no name at all on an access
//	         where read-state has not run.
//
// # UNVERIFIED AGAINST HARDWARE, precisely
//
// The parse is [ParseRunningConfig]'s `reCfgHostname`, and no capture in this
// package has ever contained a `hostname` line: labRunningConfig (parse_test.go)
// IS a verbatim 2026-08-03 capture of a real 48T4SP, and the switch was on its
// factory name, so the configuration carries no such line — testdata/ holds
// interface, LLDP and spanning-tree captures only. The line's shape (bare,
// unquoted, first token) comes from the vendor documentation and from the
// sibling S3900 capture, `hostname test-fs-s3900` on OpCode 1.7.0. Same CLI
// lineage, different firmware: evidence, not proof. identityread_test.go pins it
// with a CONSTRUCTED fixture, labelled as such, and pins the captured
// factory-named configuration as reading NO name — which is the honest verdict
// for a switch meshcraft has never renamed.
//
// # An empty name is a state, not a failure
//
// c.sysName is empty when no read-state has run on this access, when the
// configuration carries no `hostname` line (factory-named switch: it announces
// nothing, and stays `not_observed` until meshcraft renames it), and when the
// read failed. All three must read as "nothing observed", never as an
// observation of the empty string — identityFacts drops empty values and the API
// layer overlays rather than replaces, so a previously known name survives.
//
// # THE RENAME PATH DELIBERATELY ANSWERS NOTHING
//
// [Client.SetHostname] pays for that same ninety-second read to verify its own
// write, and it is NOT allowed to feed this cache — [Client.forgetSysName]
// clears it instead. api.refreshIdentityAfterHostname calls ReadIdentity on the
// very session that just renamed the device, so feeding it would record
// meshcraft's own write as "the device announces this": every state would
// confirm itself and drift would become unobservable. The name is observed on
// the next read-state, from a read that owes nothing to the write.

import (
	"context"

	"meshcraft/internal/driver"
)

var _ driver.IdentityReader = (*Client)(nil)

// noteSysName records the system name a PLAIN configuration read established.
//
// Only plain reads may call it (cf. the file header): a verifying re-read that
// follows our own write is not an observation of the device.
func (c *Client) noteSysName(hostname string) { c.sysName = hostname }

// forgetSysName drops the recorded name because the device has just been
// written to and what it announces is no longer what the last read saw.
func (c *Client) forgetSysName() { c.sysName = "" }

// ReadIdentity returns what connecting already established — model, serial, base
// MAC and firmware from the `show version` read at login — plus the system name
// from the last plain `show running-config` on this access (cf. the file header:
// harvested, never fetched here). ZERO wire round-trips.
//
// The faceplate stays out, unlike [Client.identity] on the discovery path:
// portGroups costs real commands and [Client.ReadFaceplate] exists for it.
func (c *Client) ReadIdentity(_ context.Context) (driver.Identity, error) {
	var name = c.facts.Model
	if name == "" && c.cli != nil {
		name = bannerModel(c.cli.banner)
	}
	return driver.Identity{
		Model:    name,
		Serial:   c.facts.Serial,
		MAC:      c.facts.MAC,
		Firmware: c.facts.Version,
		Hostname: c.sysName,
		Source:   "ssh_cli",
	}, nil
}
