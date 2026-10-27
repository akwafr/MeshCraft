package s3400

// The faceplate: what sockets this box physically has.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// ReadFaceplate describes the front panel.
//
// The 48T4SP is 48 copper GE + 4 SFP+ cages, and the two halves carry the
// SAME numbers in the device's own naming (g0/1 and tg0/1 are two different
// sockets). That is why the standard model uses two prefixes — GE and XG —
// exactly like the Catalyst connector, and why the faceplate has two groups.
//
// The silk field carries the platform's own short spelling, which is what an
// operator reads in "show vlan" and in a configuration prompt — on the cages,
// only when THIS session observed the numbering that spelling implies. See
// portGroups, and ADR-0018 § 7 for why a prefix cannot carry the other one.
func (c *Client) ReadFaceplate(ctx context.Context) ([]map[string]any, error) {
	return c.portGroups(ctx)
}

// portGroups builds the port groups. Model-driven, then CHECKED against the
// device: a 24-port unit of the same family must not be described as a 48.
func (c *Client) portGroups(ctx context.Context) ([]map[string]any, error) {
	var copper, sfp = portCountsFor(c.facts.Model)

	var out, err = c.cli.runPaged(ctx, "show interface | include protocol", 90*time.Second)
	if err == nil {
		c.noteCageNumbering(out)
		var links = ParseLinkStates(out)
		var maxGE, maxXG int
		var name string
		for name = range links {
			var n, kind, ok = splitStandard(name)
			if !ok {
				continue
			}
			if kind == "XG" && n > maxXG {
				maxXG = n
			}
			if kind == "GE" && n > maxGE {
				maxGE = n
			}
		}
		if maxGE > 0 {
			copper = maxGE
		}
		if maxXG > 0 {
			sfp = maxXG
		}
	}
	if copper <= 0 {
		return nil, fmt.Errorf("no port could be listed on %s", c.host)
	}

	var groups = []map[string]any{
		{"prefix": "GE", "from": 1, "count": copper, "silk": "g0/"},
	}
	if sfp > 0 {
		// The "SFP+" label + 10G speed carry the MEDIA, which the prefix alone
		// does not: portrules.mediaOf and the frontend's isSFPRef both classify
		// optical ports from the group's label (or an sfp-ish name), never from
		// "XG" — the Catalyst connector's identical XG group is recognized only
		// because it says "SFP+" too. Without the label these four cages drew
		// and filtered as copper, and the topology hid them (cf. TODO.md,
		// 2026-08-05).
		var cages = map[string]any{
			"prefix": "XG", "from": 1, "count": sfp,
			"label": "SFP+", "speed": "10G",
		}
		// THE SILKSCREEN IS A CLAIM, AND IT IS ONLY MADE WHEN IT WAS OBSERVED
		// (ADR-0018, § 7). The frontend composes it as `silk + k` with k the
		// STANDARD index (web/src/network.ts, expandPorts), so a bare prefix
		// cannot express a 48-socket offset: on a build that numbers its cages
		// 49..52 this field read "tg0/1", a name the switch refuses if the
		// operator copies it into its CLI. Omitted, the screen falls back to
		// the standard name — XG1 — which meshcraft uses everywhere else and
		// which claims nothing false. The read that fills `sfp` above is the
		// same one that establishes the numbering, so a device that listed its
		// cages at all has already answered this.
		if c.cages.known && c.cages.base == 0 {
			cages["silk"] = "tg0/"
		}
		groups = append(groups, cages)
	}
	return groups, nil
}

// reModelPorts reads the socket counts out of the model reference itself:
// "S3400-48T4SP" is 48 copper + 4 SFP+, "S3400-24T4FP" is 24 + 4. The
// range states both in its own naming, which is steadier than a table this
// connector would have to keep in step with the catalogue.
var reModelPorts = regexp.MustCompile(`(?i)S\d{4}-(\d+)T(\d+)[A-Z]`)

func portCountsFor(model string) (copper int, sfp int) {
	var m = reModelPorts.FindStringSubmatch(strings.ToUpper(model))
	if m == nil {
		return copperPorts, sfpPorts
	}
	var c, cerr = strconv.Atoi(m[1])
	var s, serr = strconv.Atoi(m[2])
	if cerr != nil || c <= 0 || c > 96 {
		return copperPorts, sfpPorts
	}
	if serr != nil || s < 0 || s > 8 {
		s = sfpPorts
	}
	return c, s
}

// faceplatePorts totals the sockets THE MODEL REFERENCE declares, or 0 when the
// model is not one a count can be read out of.
//
// Same doctrine, and deliberately the same shape, as `faceplatePortCount` on the
// API side: a yardstick is only worth having if it comes from somewhere OTHER
// than the read being judged, and it must say "I do not know" rather than
// guess. That is the one behaviour [portCountsFor] cannot provide — it answers
// with the range's most common faceplate when the model is unreadable, which is
// right for drawing a panel and wrong for a guardrail, because a wrong count
// would make a guardrail fire or hold on a device it knows nothing about.
//
// A caller must therefore test `> 0` before concluding anything from it.
func faceplatePorts(model string) int {
	if reModelPorts.FindStringSubmatch(strings.ToUpper(model)) == nil {
		return 0
	}
	var copper, sfp = portCountsFor(model)
	if copper <= 0 {
		return 0
	}
	return copper + sfp
}

// Inspect is the discovery-time deep read: identity plus faceplate, on one
// session that is closed when done.
//
// Called with the ONE credential the probe already got in with — replaying
// others here would spend the device's account-lockout budget for nothing.
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
		return id, fmt.Errorf("connected but nothing read — the CLI was %s",
			map[bool]string{true: "recognized", false: "NOT recognized"}[c.known])
	}
	return id, nil
}
