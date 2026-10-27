package procurve

// Identity and faceplate: who this switch is, and what sockets it has.

import (
	"context"
	"strings"

	"meshcraft/internal/driver"
)

// ReadFaceplate describes the front panel FROM THE DEVICE, not from a catalogue.
//
// The groups follow the Type column of "show interfaces brief", because that
// is where this range states its own hardware. CAPTURED on a 2610-48:
//
//	1     10/100TX   …
//	49    100/1000T  …
//	51               …
//
// Three kinds of socket on one faceplate: 48 fast-ethernet, 2 dual-personality
// gigabit, and 2 mini-GBIC cages whose Type is BLANK because nothing is plugged
// into them. That blank is not a read failure — it is how this platform says
// "cage present, empty" — and splitting the groups on it keeps a 24-port unit
// of the same family from being drawn as a 48.
//
// ONE PREFIX for all of them: this CLI names ports with a bare number and
// nothing else, so there is no second numbering space to distinguish (contrast
// the Catalyst and the S3400, where Gi1/0/1 and Te1/0/1 are different sockets
// sharing a number).
func (c *Client) ReadFaceplate(ctx context.Context) ([]map[string]any, error) {
	if c.cli == nil {
		return nil, errNoSession
	}
	var text, err = c.cli.run(ctx, "show interfaces brief", showRunTimeout)
	if err != nil && strings.TrimSpace(text) == "" {
		return nil, err
	}
	var groups = faceplateGroups(parseInterfacesBrief(text))
	if len(groups) == 0 {
		return nil, driver.NotSupported("hpe-procurve", "faceplate",
			"\"show interfaces brief\" listed no port on this device")
	}
	return groups, nil
}

// faceplateGroups collapses the port list into contiguous runs of one socket
// type.
func faceplateGroups(ports map[int]portStatus) []map[string]any {
	if len(ports) == 0 {
		return nil
	}
	var max int
	var n int
	for n = range ports {
		if n > max {
			max = n
		}
	}
	var out []map[string]any
	var start, count int
	var kind string
	var flush = func() {
		if count == 0 {
			return
		}
		var g = map[string]any{
			"prefix": defaultPortPrefix, "from": start, "count": count, "silk": "",
		}
		if kind != "" {
			// The platform's own word for the socket, so an operator reading the
			// faceplate sees what "show interfaces brief" would tell them.
			g["label"] = kind
		}
		out = append(out, g)
		count = 0
	}
	for n = 1; n <= max; n++ {
		var st, ok = ports[n]
		if !ok {
			flush()
			continue
		}
		if count == 0 {
			start, kind, count = n, st.Kind, 1
			continue
		}
		if st.Kind != kind {
			flush()
			start, kind, count = n, st.Kind, 1
			continue
		}
		count++
	}
	flush()
	return out
}

// identity reads what an inventory needs to name this device.
//
// THE MODEL COMES FROM LLDP, and that is not a detour. "show version" on this
// range prints a build stamp and nothing else — no product, no part number:
//
//	Image stamp:    /sw/code/build/nemo
//	                Sep 20 2017 11:04:46
//	                R.11.121
//
// The switch does state both, once, in the system description it advertises to
// its neighbours: "ProCurve J9088A Switch 2610-48, revision R.11.121". Reading
// it there is how an adopted device gets a model instead of an empty field.
//
// # WHY THIS CONNECTOR HAS NO driver.IdentityReader, checked 2026-08-10
//
// The obvious move — expose this method on the open session, as cisco/ios and
// fs/s3400 do — is refused, and the reason is in the three `run` calls below.
// [driver.IdentityReader]'s admission ticket is CHEAPNESS: the CLI platforms
// that carry it answer from memory because they parsed `show version` (or the
// login banner) at connect, for zero wire round-trips. This connector parses
// NOTHING at connect (cf. openCLI, which clears the banner gate and sends
// "no page"), and none of its other read paths harvests the model on the way
// past either — [Client.ReadState] reads the running configuration and the
// interface summary, and the model appears in neither. On this range it lives in
// the LLDP system description and nowhere else, so a ReadIdentity here would have
// to fetch it, on a platform that allows FOUR management sessions and whose long
// reads are the expensive part of every gesture.
//
// So the OTHER adapter is the one this connector implements, and it is not a
// consolation prize: api.observeIdentity falls back to [driver.Identifier]
// precisely for this case, spending one extra session to FILL A HOLE while
// `devices.model` is empty and never again once filled. What that costs, stated
// plainly, is swap detection — a 2610 replaced by another 2610 at the same
// address keeps the old model until someone clears the field.
//
// WHAT WOULD EARN THE OTHER ONE: not a new command, but an existing read
// harvested. If a login-time parse or [Client.ReadState] ever comes to hold the
// model — the running configuration's own header carries the part number
// ("; J9088A Configuration Editor; Created on release #R.11.121"), which is
// the model CODE but not the model name — then noting it on the Client and
// answering from memory is the fs/s3400 pattern, and this paragraph is the one
// to delete.
func (c *Client) identity(ctx context.Context) driver.Identity {
	var id driver.Identity
	if c.cli == nil {
		return id
	}
	var sys, err = c.cli.run(ctx, "show system-information", cmdTimeout)
	if err == nil || strings.TrimSpace(sys) != "" {
		var info = parseSystemInfo(sys)
		id.Hostname = info.Name
		id.Serial = info.Serial
		id.MAC = info.MAC
		id.Firmware = info.Revision
	}
	var local string
	local, err = c.cli.run(ctx, "show lldp info local-device", showRunTimeout)
	if err == nil || strings.TrimSpace(local) != "" {
		id.Model, id.ModelCode = parseLocalDevice(local)
	}
	if id.Model != "" {
		id.Family = "ProCurve 2600"
	}

	var brief string
	brief, err = c.cli.run(ctx, "show interfaces brief", showRunTimeout)
	if err == nil || strings.TrimSpace(brief) != "" {
		var ports = parseInterfacesBrief(brief)
		id.PortGroups = faceplateGroups(ports)
		var st portStatus
		for _, st = range ports {
			// A blank Type is an EMPTY optical cage (cf. ReadFaceplate); an
			// electrical port always names its speeds.
			if st.Kind == "" {
				id.SFPPorts++
			} else {
				id.CopperPorts++
			}
		}
	}
	return id
}

// Identify opens a session, reads the identity, and closes it.
//
// Called with the ONE credential the probe already got in with: replaying others
// here would spend this range's account-lockout budget for nothing, and a
// ProCurve whose manager account is locked is recovered by walking to it.
func (d *Driver) Identify(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Identity, error) {
	var adm, err = d.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		return driver.Identity{}, err
	}
	defer c.Close()
	return c.identity(ctx), nil
}
