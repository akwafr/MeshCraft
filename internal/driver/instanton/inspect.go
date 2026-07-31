package instanton

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Precise identification of an Instant On, WITHOUT authentication.
//
// Two sources, depending on device state:
//
//   - fresh from factory, the provisioning UI (port 4443) exposes
//     /api/systemInfo: model, serial, MAC, firmware, port count;
//   - once in local administration, that port closes — but the classic UI
//     LOGIN PAGE carries the same information in hidden fields
//     (`sysDescr`, `sysName`, `rlPhdUnitGenParamSerialNum`). No credentials
//     are needed to read them.
//
// Observed on real hardware:
//
//	sysDescr = "HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A,
//	            InstantOn_1930_3.4.0.0 (6)"

// RawSection returns the RAW XML of a `wcd` section as the device serves it.
// READ ONLY.
//
// Reconnaissance tool, and one was needed. This interface has no
// documentation: we know how to read a section because we observed it, and we
// only know how to WRITE it after seeing its full field set. Guessing fields is
// the best way to get this product line's characteristic failure mode —
// `statusCode 0`, no change, no message.
//
// The section name is sanitized of characters that cannot appear in a legitimate
// name: it goes in the query string, and injection there would redirect the
// request to another device endpoint.
func (a *Admin) RawSection(ctx context.Context, section string) (string, error) {
	var clean = sectionName(section)
	if clean == "" {
		return "", fmt.Errorf("invalid section name")
	}
	var resp, err = a.web.Get(ctx, a.wcdURL()+"?{"+clean+"}")
	if err != nil {
		a.step(false, "raw read of "+clean, err.Error())
		return "", err
	}
	a.step(true, "section "+clean+" read", strconv.Itoa(len(resp.Body))+" bytes")
	return resp.Body, nil
}

// sectionName keeps only characters valid in a section name.
func sectionName(s string) string {
	var out []rune
	var r rune
	for _, r = range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' {
			out = append(out, r)
		}
	}
	return string(out)
}

// `Identity` is now the contract type (`driver.Identity`), by ALIAS:
// cf. contract.go. Every field this package carried is there — vendor label,
// JLxxxA reference, sub-family (1930 | 1830), serial, MAC, firmware,
// build, hostname, faceplate, and `Source` saying where the information came
// from (`provisioning_api` | `login_page`) so a screen never presents a deduction
// as a reading.
//
// ONE DIFFERENCE, and it needs attention: the contract type carries
// `PortGroups` as a FIELD, whereas this package returned it via a method. The
// faceplate is therefore computed by [portGroups] and FILLED IN by everything
// that produces an identity — [ParseLoginPage] and [identityFromSystemInfo].
// Without it, adoption produced a device WITH NO PORTS, which is not « blank »
// but wrong.

var (
	reSysDescr  = regexp.MustCompile(`name="sysDescr"[^>]*value="([^"]*)"`)
	reSysName   = regexp.MustCompile(`name="sysName"[^>]*value="([^"]*)"`)
	reSerialFld = regexp.MustCompile(`name="rlPhdUnitGenParamSerialNum\$repeat\?1"[^>]*value="([^"]*)"`)

	reModelCode = regexp.MustCompile(`\b(JL\d{3}[A-Z])\b`)
	reFamily    = regexp.MustCompile(`\b(18|19)30\b`)
	reFirmware  = regexp.MustCompile(`InstantOn_\d+_([0-9.]+)\s*\((\d+)\)`)
	reCopper    = regexp.MustCompile(`(\d+)p\s+(?:Gigabit|Gig|G\b)`)
	reSFPPlus   = regexp.MustCompile(`(\d+)p\s+SFP\+`)
	reSFP       = regexp.MustCompile(`(\d+)p\s+SFP\b`)
)

// unescape turns UI non-breaking spaces and entities into readable text.
func unescape(s string) string {
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, " ", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	return strings.Join(strings.Fields(s), " ")
}

// ParseLoginPage extracts identity from login page hidden fields.
func ParseLoginPage(body string) (Identity, error) {
	var id = Identity{Source: "login_page"}
	var m = reSysDescr.FindStringSubmatch(body)
	if m == nil {
		return id, fmt.Errorf("login page without sysDescr: not an Instant On UI")
	}
	var descr = unescape(m[1])
	id.Model = strings.TrimSuffix(strings.Split(descr, ",")[0], " ")

	var c = reModelCode.FindStringSubmatch(descr)
	if c != nil {
		id.ModelCode = c[1]
	}
	var f = reFamily.FindStringSubmatch(descr)
	if f != nil {
		id.Family = f[1] + "30"
	}
	var v = reFirmware.FindStringSubmatch(descr)
	if v != nil {
		id.Firmware, id.Build = v[1], v[2]
	}
	var pCopper = reCopper.FindStringSubmatch(descr)
	if pCopper != nil {
		id.CopperPorts, _ = strconv.Atoi(pCopper[1])
	}
	var pSFP = reSFPPlus.FindStringSubmatch(descr)
	if pSFP != nil {
		id.SFPPorts, _ = strconv.Atoi(pSFP[1])
		id.SFPPlus = true
	} else if pSFP = reSFP.FindStringSubmatch(descr); pSFP != nil {
		id.SFPPorts, _ = strconv.Atoi(pSFP[1])
	}
	id.PoE = strings.Contains(strings.ToUpper(descr), "POE")

	var n = reSysName.FindStringSubmatch(body)
	if n != nil {
		id.Hostname = unescape(n[1])
	}
	var sn = reSerialFld.FindStringSubmatch(body)
	if sn != nil {
		id.Serial = unescape(sn[1])
	} else if id.Hostname != "" {
		// By default the device names itself from its serial number.
		id.Serial = id.Hostname
	}
	// Faceplate travels with identity, instead of being recomputed by the caller:
	// it is the field the contract expects, and the only way to draw the device at
	// adoption without a model catalog.
	id.PortGroups = portGroups(id)
	return id, nil
}

// identityFromSystemInfo translates the provisioning UI response (port 4443)
// into standard model identity.
//
// Extracted from [Inspect] to be shared with [Driver1930.Onboard]: switching to
// local administration reads this same page anyway, and adoption that follows
// needs model and serial at the same moment. Two copies of this translation would
// eventually deduce different faceplates from the same label.
func identityFromSystemInfo(info SystemInfo) Identity {
	var id = Identity{
		Model: info.Model, Serial: info.SerialNumber, MAC: info.MACAddress,
		Firmware: info.FirmwareVersion, Build: info.BuildNumber,
		Source: "provisioning_api",
	}
	var c = reModelCode.FindStringSubmatch(info.Model)
	if c != nil {
		id.ModelCode = c[1]
	}
	var f = reFamily.FindStringSubmatch(info.Model)
	if f != nil {
		id.Family = f[1] + "30"
	}
	var pCopper = reCopper.FindStringSubmatch(info.Model)
	if pCopper != nil {
		id.CopperPorts, _ = strconv.Atoi(pCopper[1])
	}
	var pSFP = reSFPPlus.FindStringSubmatch(info.Model)
	if pSFP != nil {
		id.SFPPorts, _ = strconv.Atoi(pSFP[1])
		id.SFPPlus = true
	}
	id.PoE = strings.Contains(strings.ToUpper(info.Model), "POE")
	// numberOfPorts is authoritative when the label is silent.
	if id.CopperPorts == 0 && info.NumberOfPorts > 0 {
		id.CopperPorts = info.NumberOfPorts - id.SFPPorts
	}
	id.PortGroups = portGroups(id)
	return id
}

// Inspect identifies the device at the given address, without authentication.
// Tries the provisioning UI first (richer), then the login page.
func Inspect(ctx context.Context, host string, d driver.Dialer) (Identity, error) {
	// 1. Device still factory-fresh: provisioning API responds.
	var client = NewClient(host, d)
	var info, err = client.SystemInfo(ctx)
	if err == nil && info.Model != "" {
		return identityFromSystemInfo(info), nil
	}

	// 2. Device in local administration: login page is enough.
	var web = driver.NewWebSession("https://"+host, d, 0)
	var resp, getErr = web.Get(ctx, "/")
	if getErr != nil {
		return Identity{}, getErr
	}
	return ParseLoginPage(resp.Body)
}

// portGroups translates the faceplate into standard model port groups. Names
// follow the device: copper ports are numbered from 1, SFPs continue the
// numbering (48 ports → SFPs 49 to 52), confirmed by `numberOfPorts` = 52 on
// a 48-port model.
//
// A FUNCTION, no longer an `Identity` method: this type is now the contract
// type, on which this package cannot define methods — and it carries `PortGroups`
// as a field anyway. The result is the same, computed in the same place, and
// filled into that field by identity producers.
func portGroups(id Identity) []map[string]any {
	var out = []map[string]any{}
	if id.CopperPorts > 0 {
		var label = fmt.Sprintf("1–%d", id.CopperPorts)
		if id.PoE {
			label += " · PoE"
		}
		out = append(out, map[string]any{
			"prefix": "GE", "count": id.CopperPorts, "from": 1,
			"speed": "1G", "poe": id.PoE, "label": label,
		})
	}
	if id.SFPPorts > 0 {
		var speed = "1G"
		var name = "SFP"
		if id.SFPPlus {
			speed, name = "10G", "SFP+"
		}
		out = append(out, map[string]any{
			"prefix": "GE", "count": id.SFPPorts, "from": id.CopperPorts + 1,
			"speed": speed, "poe": false, "label": name,
		})
	}
	return out
}
