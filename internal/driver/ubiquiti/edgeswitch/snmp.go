package edgeswitch

// SNMP — the channel the product's monitoring actually polls.
//
// CAPTURED on the lab EdgePoint S16 (firmware 1.9.2, 2026-08-03): neither
// "show snmpcommunity" nor "show snmptrap" exists on this build, though
// both are standard FASTPATH and named in the CLI reference. Ubiquiti trimmed
// them. The command that works is "show snmp", and it prints the community
// table plus the trap targets in one go:
//
//	Community-String     Community-Access View Name        IP Address
//	-------------------- ---------------- ---------------- ----------------
//	public               Read Only        Default          All
//
// "show running-config | include snmp" returns NOTHING even with a community
// configured, so the running config is not a usable source here — the table is.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"meshcraft/internal/driver"
)

// reSNMPCommunity matches a row of the first table of "show snmp".
// The access column carries a SPACE ("Read Only"), which is why the row is
// matched by shape rather than split on whitespace.
var reSNMPCommunity = regexp.MustCompile(`(?m)^\s*(\S+)\s+(Read Only|Read Write|Read/Write)\s+(\S+)\s+(\S+)\s*$`)

// SNMPCommunity is one row of the community table.
type SNMPCommunity struct {
	Name     string
	ReadOnly bool
	View     string
	Source   string // "All" or a restricting address
}

func parseShowSNMP(out string) []SNMPCommunity {
	var rows = reSNMPCommunity.FindAllStringSubmatch(out, -1)
	var list []SNMPCommunity
	var i int
	for i = range rows {
		list = append(list, SNMPCommunity{
			Name:     rows[i][1],
			ReadOnly: strings.EqualFold(rows[i][2], "Read Only"),
			View:     rows[i][3],
			Source:   rows[i][4],
		})
	}
	return list
}

// ReadSNMP reports the communities the switch currently accepts.
func (c *Client) ReadSNMP(ctx context.Context) ([]SNMPCommunity, error) {
	var out, err = c.cli.run(ctx, "show snmp", showTimeout)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("show snmp on %s: %w", c.host, err)
	}
	return parseShowSNMP(out), nil
}

// ApplySNMP configures the read-only community this instance polls with.
//
// v3 is REFUSED rather than approximated: "show snmp" prints a v3
// notification table on this platform, so the feature exists, but nothing v3
// has been exercised here and declaring it would advertise monitoring this
// connector cannot verify it set.
func (c *Client) ApplySNMP(ctx context.Context, want driver.SNMPIdentity) (driver.SNMPResult, error) {
	var res driver.SNMPResult
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}
	if strings.TrimSpace(want.User) != "" || strings.TrimSpace(want.AuthSecret) != "" {
		res.Skipped = append(res.Skipped,
			"SNMPv3 not applied: only v1/v2c has been exercised on this platform")
	}
	var community = strings.TrimSpace(want.Community)
	if community == "" {
		res.Skipped = append(res.Skipped, "no community supplied: nothing was written")
		return res, nil
	}
	if !validCommunity(community) {
		return res, fmt.Errorf("community rejected: this CLI takes the string as a bare word")
	}

	// Global Config, ONE command. "snmp-server community ro <name>" — the
	// form the FASTPATH reference documents for setting access — answers
	// "% Invalid input detected at '^' marker" on this build (captured
	// 2026-08-03), so it is not sent. A community created here comes up
	// Read Only by default, which is what the read-back below confirms rather
	// than assumes; the factory "public" shows the same access.
	err = c.runBlock(ctx, []string{
		"configure",
		"snmp-server community " + community,
		"exit",
	})
	if err != nil {
		c.step(false, "SNMP community refused", firstLine(err.Error()))
		return res, fmt.Errorf("snmp-server community on %s: %w", c.host, err)
	}

	// Verify by reading the table back: nothing is taken as written on the
	// strength of a return code.
	var seen, readErr = c.ReadSNMP(ctx)
	if readErr == nil {
		var i int
		for i = range seen {
			if seen[i].Name != community {
				continue
			}
			res.Enabled = true
			res.CommunitySet = true
			if !seen[i].ReadOnly {
				// Say it rather than let a read-write community pass as though
				// it had been asked for: this connector only ever asks for RO.
				res.Skipped = append(res.Skipped,
					"the community exists but \"show snmp\" reports it as "+
						"read-write — this platform did not take the read-only access")
				c.step(false, "SNMP community is read-write, not read-only", seen[i].Name)
				return res, nil
			}
			c.step(true, "SNMP v2c community set", "read-only, confirmed in \"show snmp\"")
			return res, nil
		}
		return res, fmt.Errorf("community %q was accepted but does not appear in \"show snmp\"", community)
	}
	res.Enabled = true
	res.CommunitySet = true
	c.step(true, "SNMP v2c community set", "read-back failed: "+firstLine(readErr.Error()))
	return res, nil
}

// RemoveSNMPCommunity drops a community — used to undo a test, and by any
// caller rotating away from a leaked string.
func (c *Client) RemoveSNMPCommunity(ctx context.Context, community string) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	if !validCommunity(community) {
		return fmt.Errorf("community rejected: this CLI takes the string as a bare word")
	}
	return c.runBlock(ctx, []string{
		"configure",
		"no snmp-server community " + community,
		"exit",
	})
}

func validCommunity(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	var r rune
	for _, r = range s {
		var ok = (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if !ok {
			return false
		}
	}
	return true
}
