package eos

// Deep identification for DISCOVERY: model, serial, and above all the FACEPLATE
// — the port count read ON THE DEVICE, never taken from a catalog of part
// numbers.
//
// The reason is the same one cisco/ios gives and it is if anything stronger
// here: this vendor's part numbers encode the faceplate in a suffix
// (DCS-7050SX3-48YC8 is 48 SFP28 cages and 8 QSFP28, DCS-7050SX3-48C8 is not the
// same switch), and the naming scheme has changed twice across generations. A
// catalog keyed on the part number is a catalog that will eventually be missing a
// part number, and the device will then be drawn with no ports at all. `show
// interfaces status` knows, so the device is asked.

import (
	"context"
	"fmt"

	"meshcraft/internal/driver"
)

// Inspect opens a session with THE CREDENTIAL THAT ALREADY OPENED the door (the
// probe's) and reads the identity. No other credential is tried here: discovery
// has a budget, this path does not.
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
// already-open access — this catches up a device adopted without one (before this
// connector existed, or entered by hand). Same read as identification, without a
// second authentication cycle.
func (c *Client) ReadFaceplate(ctx context.Context) ([]map[string]any, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("no open session on this access")
	}
	var statusOut, err = c.cli.run(ctx, "show interfaces status", cmdTimeout)
	if err != nil && statusOut == "" {
		return nil, fmt.Errorf("show interfaces status failed: %v", err)
	}
	var groups = portGroupsOf(parseInterfacesStatus(statusOut))
	if len(groups) == 0 {
		return nil, fmt.Errorf("no ports parsed from \"show interfaces status\"")
	}
	c.step(true, "faceplate read", fmt.Sprintf("%d port group(s)", len(groups)))
	// A split cage produces no group; the transcript is where that absence is
	// dated, so a faceplate refresh that "lost" four sockets reads as the
	// device-side split it is (ReadState carries the operator-facing note).
	var split = breakoutNote(parseBreakoutCages(statusOut))
	if split != "" {
		c.step(true, "split cages not drawn", split)
	}
	return groups, nil
}

// portGroupsOf rebuilds the faceplate from "show interfaces status": one group
// per CONTIGUOUS run of ports sharing a speed and a medium.
//
// Only TWO split conditions here where cisco/ios needs three, and the missing one
// is the interesting part: that connector splits on the standard PREFIX, because
// a Catalyst faceplate carries two media words over the same numbers. EOS names
// every socket `EthernetN` with no media word at all (cf. the numbering header in
// parse.go), so there is one prefix and nothing to split on. What is left:
//
//   - medium, because Et1..48 (SFP28 cages) and Et49..56 (QSFP28 cages) are
//     numerically contiguous and are not the same jack. Drawing them as one group
//     would put a 100G cage in the 25G block;
//   - contiguity, because announcing "48 ports from 1" with a hole in the middle
//     draws a socket that does not exist, and meshcraft would eventually write to
//     it. On this platform the holes are real: a cage configured for breakout
//     disappears from the numbering (its lanes are Et49/1..4, which this
//     connector refuses to name), so a switch with one QSFP split has a genuine
//     gap where that cage used to be.
func portGroupsOf(ports []statusPort) []map[string]any {
	if len(ports) == 0 {
		return nil
	}
	var out []map[string]any
	var start = -1
	var prevNum = -1
	var speed = 0 // the MAX seen in the run — see below
	var cage = false
	var class = 0

	var flush = func(endNum int) {
		if start < 0 {
			return
		}
		out = append(out, map[string]any{
			// STANDARD-MODEL prefix — one value on this platform.
			"prefix": StdPrefix,
			"count":  endNum - start + 1,
			"from":   start,
			"speed":  speedLabel(speed),
			// PoE is NOT deduced. "show interfaces status" does not carry it, and a
			// faceplate that claimed PoE the device cannot deliver would let an
			// operator plan an access point on a port that will never power it.
			// Only the 720XP line has PSE hardware at all, and "show power inline"
			// — which answers this — is not read here.
			"poe": false,
			// The label is what carries the MEDIUM to the screen and to
			// portrules.mediaOf, which classifies a port as copper / sfp / sfp_plus
			// from the prefix and the label. On a platform whose prefix says nothing
			// (every socket is "ET"), the label is the only signal there is — so it
			// is data here, not decoration.
			"label": cageLabel(cage, speed),
			// The name printed on the faceplate and typed at the CLI ("Et49").
			"silk": Silk,
		})
	}

	var p statusPort
	for _, p = range ports {
		if p.Num < 1 {
			continue
		}
		var thisCage = isCage(p.Type, p.SpeedMbps)
		var thisClass = cageClass(thisCage, p.SpeedMbps)
		// What may extend a run, and what must break one:
		//
		//   - CAGES group by CLASS (SFP-family vs QSFP-family), never by the
		//     speed the current module negotiated. The Speed column reports the
		//     MODULE — a 1000BASE-T at a-1G, a 1G optic — and grouping on it
		//     drew core01.lab's 48 identical SFP+ cages as four blocks with
		//     three different labels. The faceplate is made of sockets, and a
		//     socket does not change when a slower module is seated;
		//   - COPPER still groups by exact speed: on a multigig faceplate the
		//     1G and 2.5G blocks are different hardware over the same jacks,
		//     and the speed column is the only thing that separates them.
		var joins = start >= 0 && p.Num == prevNum+1 && thisClass == class &&
			(cage || p.SpeedMbps == speed)
		if joins {
			prevNum = p.Num
			// The run's speed is its MAX: an empty or full-speed cage reports
			// the cage's own speed, a slower module under-reports it, so the
			// max across the run is the closest thing to the socket's rating
			// this table can give.
			if p.SpeedMbps > speed {
				speed = p.SpeedMbps
			}
			continue
		}
		flush(prevNum)
		start, prevNum = p.Num, p.Num
		speed, cage, class = p.SpeedMbps, thisCage, thisClass
	}
	flush(prevNum)
	return out
}

// cageClass buckets a port for grouping: 0 = copper jack, 1 = SFP-family cage
// (SFP/SFP+/SFP28), 2 = QSFP-family cage (QSFP+/QSFP28).
//
// The 40G threshold is the physical one — nothing below 40G ships in a QSFP
// form factor — and it is what keeps an UNSPLIT QSFP+ block (Et49..52 at 40G)
// its own group instead of being absorbed into the SFP+ run beside it.
func cageClass(cage bool, mbps int) int {
	if !cage {
		return 0
	}
	if mbps >= 40000 {
		return 2
	}
	return 1
}

func speedLabel(mbps int) string {
	switch {
	case mbps >= 100000:
		return "100G"
	case mbps >= 40000:
		return "40G"
	case mbps >= 25000:
		return "25G"
	case mbps >= 10000:
		return "10G"
	case mbps >= 2500:
		return "2.5G"
	case mbps >= 1000:
		return "1G"
	case mbps > 0:
		return "100M"
	default:
		// Zero means the device reported neither a speed nor a readable type — an
		// empty cage on a train whose Type column says nothing. "1G" is the safe
		// floor for a drawing; the label below is what still says it is a cage.
		return "1G"
	}
}

// cageLabel is what the group is CALLED on screen, and — because this platform
// has only one prefix — the ONLY thing that tells portrules.mediaOf a group is
// optical. Getting it wrong classifies a whole faceplate as copper, which makes
// every rule saying "the optical uplinks" select nothing.
//
// The form factor is named as the operator would name it, and the names are the
// vendor's own: a 25G cage is SFP28, a 40G one QSFP+, a 100G one QSFP28. All of
// them carry "SFP", which is what the classifier keys on.
func cageLabel(cage bool, mbps int) string {
	if !cage {
		return ""
	}
	switch {
	case mbps >= 100000:
		return "QSFP28"
	case mbps >= 40000:
		return "QSFP+"
	case mbps >= 25000:
		return "SFP28"
	case mbps >= 10000:
		return "SFP+"
	default:
		return "SFP"
	}
}
