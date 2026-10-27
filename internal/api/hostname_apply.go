package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// Writing the DEVICE'S OWN name — hostname, system name, identity depending on
// the vendor — and normalizing `devices.name` into something a hostname can be.
//
// # ONE name, not two, and no button
//
// `devices.name` is the name the fleet is administered by; the device's own
// hostname is where that name becomes visible OUTSIDE meshcraft — console banner,
// LLDP system-name TLV, every syslog line the device emits at three in the
// morning. There is no separate hostname field to keep in step, and as of
// 2026-08-06 there is no operator gesture either: the name is RECONCILED, and
// hostnameheal.go is the loop that does it. internal/driver/hostname.go argues
// both points at length, including the reasoning it now retracts.
//
// WHAT WAS DELETED HERE, so nobody looks for it: POST
// /api/devices/{id}/hostname/apply and its handler. It let the operator send an
// arbitrary hostname, which was the whole reason the two names were allowed to
// diverge; with the name reconciled, a hostname typed by hand would be overwritten
// by the next pass, and a button whose effect expires within half an hour is worse
// than no button. What remains is [Server.pushHostname] — the same write, as an
// INTERNAL entry point, called by the rename hook and by the healer.
//
// # What is NOT done
//
// Nothing here decides WHETHER to push (that is hostnameheal.go's verdict), and
// nothing renames the device inside meshcraft: the name flows one way only, from
// the inventory to the equipment. The normalization below REFUSES rather than
// truncating, and that refusal reaches the operator instead of being rounded off.

// hostnamePush is what one push attempt produced.
//
// The transcript travels with it because it is the only way to understand a
// failure on a device with neither SSH nor a serial port, and because this push no
// longer has an HTTP response to ride home in: it lands in a pipeline instead (cf.
// pipelineForHostname).
type hostnamePush struct {
	// Name is what was actually sent — already normalized by the caller.
	Name  string
	Steps []driver.Step
	Err   error
}

// pushHostname sets the device's own system name. THE internal write path.
//
// VALIDATION HAPPENS BEFORE THE DEVICE IS REACHED, and that is the point of
// driver.ValidHostname being a pure function: an invalid name costs no session on
// a range that only grants five, and — the stronger reason — on the six CLI
// connectors this string is concatenated into a command line, where a newline
// would run whatever follows it as a second configuration command with the
// privileges of the session. Rejecting outside the RFC 1123 alphabet closes that
// here, once, rather than in eight connectors' quoting rules.
//
// Never panics on a nil access and never leaks a session: `adminFor` may return a
// non-nil Admin alongside an error (two connectors do, so the transcript survives)
// and may return nil (SwOS does, so unproven access does not circulate) — cf.
// transcriptOf, which carries that rule.
func (s *Server) pushHostname(ctx context.Context, d model.Device,
	name string) hostnamePush {

	var out = hostnamePush{Name: name}
	if !driver.ValidHostname(name) {
		out.Err = errors.New(hostnameRefusal(name))
		return out
	}
	var _, adm, _, err = s.adminFor(ctx, d)
	if adm != nil {
		defer adm.Close()
	}
	if err != nil {
		out.Steps, out.Err = transcriptOf(adm), err
		return out
	}
	var setter, canSet = driver.AsHostnameSetter(adm)
	if !canSet {
		// THE TABLE IS LYING, and this is the runtime half of the guard described
		// on hostnameWritableConnectors: a session says this connector cannot write
		// the system name while the table promised it could, which means every
		// screen has been showing `drift` — "a repair is coming" — for a device
		// that can never converge. Loud, because the fix is one line in that map
		// and nothing else would surface the disagreement.
		slog.Error("hostname: the connector refuses the write the capability table "+
			"promised — hostnameWritableConnectors is stale",
			"connector", adm.Connector().Name, "device", d.Name)
		out.Steps = transcriptOf(adm)
		out.Err = driver.NotSupported(adm.Connector().Name, "set the system name",
			"the field exists on every range surveyed, the WRITE has not been proven "+
				"on this one")
		return out
	}
	out.Err = setter.SetHostname(ctx, name)
	if out.Err == nil {
		s.refreshIdentityAfterHostname(ctx, d, adm)
	}
	// Transcript LAST: it accumulates as the access works, and the identity re-read
	// above is part of what this gesture did.
	out.Steps = transcriptOf(adm)
	return out
}

// refreshIdentityAfterHostname re-reads identity on the open access and merges
// what it learned into `extra.identity`.
//
// MEANT TO BE LOAD-BEARING, and TODAY IT LARGELY IS NOT — read this before
// relying on it. `extra.identity.hostname` is the SECOND observation source of the
// reconciliation (cf. hostnameObserved), so this re-read is what should let a
// device with no LLDP answer converge at all. Without it the push succeeds, the
// healer still reads the pre-write name, judges it drifted, and pushes again on
// every cooldown — forever.
//
// The gap, verified in the code on 2026-08-08: `identityFacts` persists
// `id.Hostname`, but of the four connectors implementing [driver.IdentityReader]
// only mikrotik-routeros ever SET that field. cisco-ios, arista-eos and fs-s3900
// each built a driver.Identity literal with Model / Serial / MAC / Firmware and no
// Hostname, so this re-read moved everything about the device EXCEPT its name.
//
// # THE OTHER HALF OF THE PICTURE, and it is NOT "one connector" (2026-08-09)
//
// Three MORE connectors do fill [driver.Identity.Hostname], on the other path —
// hpe-procurve (inspect.go, from the captured "System Name" row of `show
// system-information`), aruba-instant-on-1930 (inspect.go, from the login page's
// captured `sysName` hidden input) and mikrotik-swos (inspect.go, from `/sys.b`'s
// `id`, captured as `id:'6c616e2d6d36'`). None of them is an IdentityReader, so
// they are reached only through observeIdentity's [driver.Identifier] FALLBACK —
// which runs exactly when `devices.model` is EMPTY, i.e. at most once per device,
// on the first read-state after adoption, and never again once the model is filled.
//
// # THE TWO FS RANGES LEFT THAT LIST ON 2026-08-10, AND THIS FUNCTION STILL
// # REPORTS NO NAME FOR THEM — ON PURPOSE
//
// fs-s3400 and fs-s3900 were the worst case: no LLDP observation either — the
// S3400's agent serves NOTHING under `1.0.8802` (walked live 2026-08-05, cf.
// deviceKnownMACs) and the CLI LLDP path deliberately never writes `sys_name`
// (cf. lldpCLISnapshot) — so NO source could report, they sat at `not_observed`
// for ever, and since only `drift` is repairable the healer never pushed to them.
// A rename did, once, through hostnameAfterRename: meshcraft wrote a name to an
// FS switch and could never confirm the switch had stored it.
//
// Both now carry the name in their [driver.IdentityReader], and fs-s3400 gained
// that interface to do it. But NOT by reading here: on those ranges the system
// name lives in the running configuration alone, which is a ninety-second paged
// read on the S3400, and driver.IdentityReader's whole contract is that it costs
// no round trip. Their ReadState harvests the name from the configuration read it
// already performs and their ReadIdentity replays it from memory (each package's
// identityread.go). So they answer on the READ-STATE gesture and are silent on
// every access where read-state has not run.
//
// THIS IS SUCH AN ACCESS. pushHostname opened this session to write, not to read;
// no ReadState has run on it, and each connector's SetHostname additionally
// CLEARS what an earlier read had established. That is deliberate and it is the
// contract hostnameheal.go rests on: a successful SetHostname must never be
// recorded as "the device now announces this", or the verified re-read the
// write already performs would confirm the write with itself and drift would stop
// being observable. The name is observed on the next read-state, from a read that
// owes nothing to this write.
//
// So the honest count is: `extra.identity.hostname` is written continuously on
// THREE ranges (mikrotik-routeros, fs-s3400, fs-s3900 — of which the two FS ones
// only through read-state), once-and-only-once on THREE (hpe-procurve,
// aruba-instant-on-1930, mikrotik-swos), and NEVER on cisco-ios, arista-eos and
// ubiquiti-edgeswitch. This function is on the cheap path only, and of the six it
// can move the name on exactly one: mikrotik-routeros.
//
// Only the CHEAP path (driver.IdentityReader). A connector that would need a
// fresh session to answer is left alone: identity_selfheal.go establishes that
// a second session is only worth paying for to fill a HOLE, and this is not
// one — we just wrote the name, and the next read-state will catch up for free.
// Such a device converges through LLDP, or stays `drift` until someone reads it
// back, which the doubling cooldown makes cheap rather than free.
//
// The stored model is passed as "" so store.SetDeviceIdentity's `nullif` guard
// keeps whatever the column holds: this gesture has no business moving the
// model, and an identity read that came back partial must not blank it.
func (s *Server) refreshIdentityAfterHostname(ctx context.Context, d model.Device,
	adm driver.Admin) {
	var reader, cheap = driver.AsIdentityReader(adm)
	if !cheap {
		return
	}
	// Partial by construction: the contract says a connector returns what it
	// COULD read even alongside an error, so the error is not a reason to
	// discard the facts.
	var id, _ = reader.ReadIdentity(ctx)
	var merged = mergeIdentityFacts(storedIdentityFacts(d.Extra), identityFacts(id))
	if merged == nil {
		return
	}
	var raw, mErr = json.Marshal(merged)
	if mErr != nil {
		return
	}
	_, _ = s.Store.SetDeviceIdentity(ctx, d.ID, "", raw)
}

// mergeIdentityFacts overlays an observation on what is stored, and returns nil
// when nothing moved.
//
// OVERLAY AND NOT REPLACE: identityFacts drops empty fields, so a replace would
// turn "this connector did not read the serial this time" into "this device
// has no serial". Returning nil for an unchanged result is what keeps a
// re-read that learned nothing from costing a row write.
func mergeIdentityFacts(stored, observed map[string]string) map[string]string {
	if len(observed) == 0 {
		return nil
	}
	var out = make(map[string]string, len(stored)+len(observed))
	var key, val string
	for key, val = range stored {
		out[key] = val
	}
	var changed = false
	for key, val = range observed {
		if out[key] != val {
			out[key] = val
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return out
}

// --- the normalizer NO LONGER LIVES HERE ---------------------------------------
//
// It moved to internal/driver/hostname.go on 2026-08-09, beside
// [driver.ValidHostname], whose doc comment was already the argument for it. The
// three functions below are one-line delegations kept for the callers in this
// package, and they are NOT a second implementation — that is the whole point of
// the move.
//
// WHY IT MOVED, since a normalizer used by an HTTP handler looks like it belongs
// next to the handler: the five serial-provisioning connectors cannot import
// `internal/api`, so while the semantics lived here each of them grew a private
// `sanitizeHostname` instead. Those five, plus the four browser scripts that
// mirror them, disagreed with this function and with each other on every axis
// that matters — see the header over [driver.HostnameFromDeviceName] for the
// catalogue. A shared rule that half the tree cannot reach is not a shared rule.
//
// The reasoning that used to be written out here is written out there, in full,
// including the paragraph about refusing rather than truncating at 63 octets.

// hostnameFromDeviceName derives a device's system name from its meshcraft
// name, or says why it cannot. See [driver.HostnameFromDeviceName] — this is
// that function, reached under the name this package's callers already use.
func hostnameFromDeviceName(deviceName string) (string, string) {
	return driver.HostnameFromDeviceName(deviceName)
}

// collapseHyphens reduces runs of hyphens to one and trims them at both ends,
// which is where RFC 1123 forbids them. See [driver.CollapseHostnameHyphens].
func collapseHyphens(in string) string {
	return driver.CollapseHostnameHyphens(in)
}

// hostnameRefusal says WHAT is wrong with a name, naming the offending
// characters. See [driver.HostnameRefusal].
func hostnameRefusal(name string) string {
	return driver.HostnameRefusal(name)
}
