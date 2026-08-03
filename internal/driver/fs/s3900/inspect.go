package s3900

// Deep identification for DISCOVERY: serial number, firmware, and above all
// the FACEPLATE — the port count read on the device itself, not guessed from
// a catalog. This is what gives adoption a drawable device, LLDP resolution
// its port names, and SNMP polling its MaxIfIndex bound.

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Inspect opens a session with THE CREDENTIAL THAT ALREADY OPENED the door
// (the probe's) and reads the identity. No other credential is tried here:
// discovery has a budget, this path doesn't.
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
// already-open access — this catches up a device adopted without a faceplate
// (before this connector existed, or entered by hand). Same read as
// identification, without a second authentication cycle.
func (c *Client) ReadFaceplate(ctx context.Context) ([]map[string]any, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("no open session on this access")
	}
	var briefOut, err = c.cli.run(ctx, "show interfaces brief", cmdTimeout)
	if err != nil && briefOut == "" {
		return nil, fmt.Errorf("show interfaces brief failed: %v", err)
	}
	var groups = portGroupsOf(parseInterfacesBrief(briefOut))
	if len(groups) == 0 {
		return nil, fmt.Errorf("no ports parsed from \"show interfaces brief\"")
	}
	c.step(true, "faceplate read", fmt.Sprintf("%d port group(s)", len(groups)))
	return groups, nil
}

// portGroupsOf rebuilds the faceplate from "show interfaces brief": one group
// per CONTIGUOUS range of the same speed. Contiguity is verified — announcing
// "24 ports from 1" with a gap would draw a socket that doesn't exist, and
// meshcraft would eventually write to it.
//
// Captured in the lab (S3900-24T4S): Eth 1/1..1/24 at 1000BASE-T then Eth
// 1/25..1/28 at 10GBASE SFP+ — two groups, common prefix "ethernet 1/".
func portGroupsOf(ports []briefPort) []map[string]any {
	if len(ports) == 0 {
		return nil
	}
	var out []map[string]any
	var start = -1
	var prevNum = -1
	var speed = 0
	var label = ""

	var flush = func(endNum int) {
		if start < 0 {
			return
		}
		out = append(out, map[string]any{
			// STANDARD-MODEL name ("GE11") — what the backend manipulates.
			"prefix": "GE",
			"count":  endNum - start + 1,
			"from":   start,
			"speed":  speedLabel(speed),
			"poe":    false, // nothing observed: an unknown capability is absent
			"label":  label,
			// Hardware silkscreen ("1/11" printed on the faceplate) — display only.
			"silk": silkPrefix,
		})
	}

	var p briefPort
	for _, p = range ports {
		if p.Num < 1 {
			continue
		}
		if start >= 0 && p.Num == prevNum+1 && p.SpeedMbps == speed {
			prevNum = p.Num
			continue
		}
		flush(prevNum)
		start, prevNum, speed = p.Num, p.Num, p.SpeedMbps
		label = labelOfType(p.Type)
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

func labelOfType(t string) string {
	var lower = strings.ToLower(t)
	if strings.Contains(lower, "sfp+") {
		return "SFP+"
	}
	if strings.Contains(lower, "sfp") {
		return "SFP"
	}
	return ""
}
