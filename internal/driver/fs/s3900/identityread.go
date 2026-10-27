package s3900

// Identity on the ALREADY-OPEN access (driver.IdentityReader), the re-read twin
// of [Driver.Identify]. In its own file so contract.go keeps only the
// discovery-path identification.
//
// # The system name rides READ-STATE, and it is never free
//
// This connector reported no Hostname at all until 2026-08-10, and that single
// empty field was the whole reason an S3900 could never be judged: its SNMP
// agent serves no LLDP-MIB and the CLI LLDP path writes no `sys_name`, so
// `extra.identity.hostname` was the only observation source left and nothing fed
// it. meshcraft therefore WROTE the name at every rename and could never confirm
// the switch had stored it — the verification, not the write, is what was
// missing.
//
// WHERE THE READ LIVES, and why not here: the device's own name is in `show run`
// and nowhere else — the login banner carries the model, `show version` carries
// serial and OpCode, and neither holds the name. `show run` costs
// showRunTimeout's worth of "Building running configuration. Please wait..."
// (30 s allowed, several seconds observed on 1.7.0 over serial), which is
// exactly the price this interface refuses to pay: cheapness is its admission
// ticket (cf. driver.IdentityReader).
//
// So the read is NOT added. It is HARVESTED from the one already paid for:
// [Client.ReadState] reads `show run` on every read-state gesture and hands the
// parsed name to [Client.noteSysName]. This method then reports it from memory.
//
//	cadence: once per read-state gesture (clicked or scheduled), on the
//	         running-config read that gesture already performs;
//	cost:    ZERO additional round trips — and zero on any access where
//	         read-state has NOT run, where this reports no name at all.
//
// # An empty name is a state, not a failure
//
// c.sysName is empty in three different situations and all three must read as
// "nothing observed", never as an observation of the empty string:
//
//   - no read-state has run on this access yet (the rename path, below);
//   - `show run` was read and carries NO `hostname` line — a switch still on its
//     factory name. Such a device announces nothing, and stays `not_observed`
//     until meshcraft renames it;
//   - the read failed. identityFacts drops empty values, and the API layer
//     overlays rather than replaces, so a previously known name survives.
//
// # THE RENAME PATH DELIBERATELY ANSWERS NOTHING
//
// [Client.SetHostname] re-reads `show run` to verify its own write, and that
// re-read is NOT allowed to feed this cache — [Client.forgetSysName] clears it
// instead. api.refreshIdentityAfterHostname calls ReadIdentity on the very
// session that just renamed the device, so feeding it would record meshcraft's
// own write as "the device announces this": every state would confirm itself
// and drift would become unobservable. The name is observed on the NEXT
// read-state, from a read that owes nothing to the write.

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

// ReadIdentity returns what connecting already established — serial and OpCode
// from the `show version` read at login, model from the login banner ("the
// S3900-24T4S is opened"), and the system name from the last plain `show run`
// on this access (cf. the file header: harvested, never fetched here). ZERO wire
// round-trips, which is what earns this connector the interface (cf.
// driver.IdentityReader). The faceplate stays out: [Client.ReadFaceplate] exists
// for it and costs a real command.
func (c *Client) ReadIdentity(_ context.Context) (driver.Identity, error) {
	var id = driver.Identity{
		Serial:   c.facts.Serial,
		Firmware: c.facts.OpCode,
		Hostname: c.sysName,
		Source:   "ssh_cli",
	}
	if c.cli != nil {
		id.Model = bannerModel(c.cli.banner)
	}
	return id, nil
}
