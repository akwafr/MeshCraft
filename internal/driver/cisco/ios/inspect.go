package ios

// Deep identification for DISCOVERY: model, serial, and above all the
// FACEPLATE — the port count read ON THE DEVICE, never taken from a catalog of
// part numbers.
//
// This matters more on this vendor than on any other in the product. The
// 2960-X range alone ships 24- and 48-port variants, PoE and non-PoE, with
// four 1G SFP cages OR two 10G SFP+ cages depending on the suffix — and the
// suffix is the only thing that distinguishes WS-C2960X-48FPD-L from
// WS-C2960X-48TS-L. A catalog keyed on the part number is a catalog that will
// eventually be missing a part number, and the device will then be drawn with
// no ports at all. `show interfaces status` knows, so the device is asked.

import (
	"context"
	"fmt"

	"meshcraft/internal/driver"
)

// Inspect opens a session with THE CREDENTIAL THAT ALREADY OPENED the door
// (the probe's) and reads the identity. No other credential is tried here:
// discovery has a budget, this path does not.
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
		return id, fmt.Errorf("connected but nothing read — CLI dialect %q", c.dialect)
	}
	return id, nil
}

// ReadFaceplate (driver.FaceplateReader) re-reads the faceplate on the
// already-open access — this catches up a device adopted without one (before
// this connector existed, or entered by hand). Same read as identification,
// without a second authentication cycle.
func (c *Client) ReadFaceplate(ctx context.Context) ([]map[string]any, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("no open session on this access")
	}
	var statusOut, err = c.cli.run(ctx, "show interfaces status", cmdTimeout)
	if err != nil && statusOut == "" {
		return nil, fmt.Errorf("show interfaces status failed: %v", err)
	}
	c.learnPortStem(statusOut)
	var groups = portGroupsOf(parseInterfacesStatus(statusOut))
	if len(groups) == 0 {
		return nil, fmt.Errorf("no ports parsed from \"show interfaces status\"")
	}
	c.step(true, "faceplate read", fmt.Sprintf("%d port group(s)", len(groups)))
	return groups, nil
}

// portGroupsOf rebuilds the faceplate from "show interfaces status": one group
// per CONTIGUOUS run of ports sharing a standard prefix, a capability speed and
// a medium.
//
// The three split conditions are all necessary, and each has a real device
// behind it:
//
//   - prefix, because a 48FPD carries GigabitEthernet1/0/1 AND
//     TenGigabitEthernet1/0/1 — same number, different socket;
//   - medium, because Gi1/0/1..24 (RJ45) and Gi1/0/25..28 (SFP cages) are
//     numerically contiguous and physically are not the same thing. Drawing
//     them as one group would put optical cages in the copper block;
//   - contiguity, because announcing "24 ports from 1" with a hole in the
//     middle draws a socket that does not exist, and meshcraft would eventually
//     write to it.
func portGroupsOf(ports []statusPort) []map[string]any {
	if len(ports) == 0 {
		return nil
	}
	var out []map[string]any
	var start = -1
	var prevNum = -1
	var prefix = ""
	var stem = ""
	var speed = 0
	var cage = false

	var flush = func(endNum int) {
		if start < 0 {
			return
		}
		out = append(out, map[string]any{
			// STANDARD-MODEL prefix ("GE", "XG") — what the backend manipulates.
			"prefix": prefix,
			"count":  endNum - start + 1,
			"from":   start,
			"speed":  speedLabel(speed),
			// PoE is NOT deduced. "show interfaces status" does not carry it, and
			// a faceplate that claims PoE the device cannot deliver would let an
			// operator plan an access point on a port that will never power it.
			// `show power inline` answers this and is not read here.
			"poe":   false,
			"label": cageLabel(cage, speed),
			// The name printed on the faceplate and typed at the CLI ("Gi1/0/11"
			// on a stackable, "Gi0/11" on a 3560 — the stem is the device's own,
			// cf. silkOf).
			"silk": silkOf(prefix, stem),
		})
	}

	var p statusPort
	for _, p = range ports {
		if p.Num < 1 {
			continue
		}
		var isCage = isSFPCage(p.StdPrefix, p.Type)
		if start >= 0 && p.Num == prevNum+1 && p.StdPrefix == prefix &&
			p.SpeedMbps == speed && isCage == cage {
			prevNum = p.Num
			continue
		}
		flush(prevNum)
		start, prevNum = p.Num, p.Num
		prefix, stem, speed, cage = p.StdPrefix, p.Stem, p.SpeedMbps, isCage
	}
	flush(prevNum)
	return out
}

func speedLabel(mbps int) string {
	switch {
	case mbps >= 10000:
		return "10G"
	case mbps >= 2500:
		return "2.5G"
	case mbps >= 1000:
		return "1G"
	case mbps > 0:
		return "100M"
	default:
		return "1G"
	}
}

// cageLabel is what the group is CALLED on screen. It also feeds
// portrules.mediaOf, which classifies a port as copper / sfp / sfp_plus from
// the prefix and the label — so "SFP" here is not decoration, it is what makes
// a rule saying "the optical uplinks" select the right jacks.
func cageLabel(cage bool, mbps int) string {
	if !cage {
		return ""
	}
	if mbps >= 10000 {
		return "SFP+"
	}
	return "SFP"
}
