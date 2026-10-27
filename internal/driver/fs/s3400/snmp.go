package s3400

// SNMP and LLDP — the two channels the product's monitoring actually reads.
//
// Both were RUN on a S3400-48T4SP (Version 2.0.2J) on 2026-08-03 and read
// back, then reverted:
//
//	config
//	 snmp-server community 0 <string> ro     → stored as "snmp-server community 0 <string> RO"
//	 snmp-server location <text>
//	 lldp run
//	 exit
//
// "show lldp neighbor" went from "LLDP is not enabled" to a populated
// table within one advertisement interval.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ApplySNMP configures the read-only community this instance polls with, and —
// since 2026-08-12 — the SNMPv3 identity, at authNoPriv and only there.
//
// PRIVACY IS REFUSED BEFORE THE DEVICE IS TOUCHED, never rounded down to
// authNoPriv. The argument, the ladder behind it and the exact refusal live in
// snmpv3.go; the short of it is that this firmware has no slot for a second
// passphrase, so accepting one would mean discarding it silently, and the
// re-read that catches every other "accepted and did something else" could
// never catch this one.
//
// The two halves are INDEPENDENT, as the contract requires: an empty field means
// "nothing to do here" and never "remove". Nothing is taken as written on the
// strength of a return code.
func (c *Client) ApplySNMP(ctx context.Context, want SNMPIdentity) (SNMPResult, error) {
	var res SNMPResult
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}

	// Everything refusable is refused BEFORE anything is written: a half-applied
	// identity is worse than one that was never started.
	var wantsV3 = strings.TrimSpace(want.User) != ""
	err = refuseSNMPv3Priv(want)
	if err != nil {
		return res, err
	}
	var authKw string
	if wantsV3 {
		if strings.TrimSpace(want.Group) == "" {
			want.Group = defaultV3Group
		}
		authKw, err = snmpV3AuthKeyword(want.AuthAlg)
		if err != nil {
			return res, err
		}
		if want.AuthSecret == "" {
			return res, fmt.Errorf("SNMPv3 user %q was requested with no authentication "+
				"passphrase: this platform has no unauthenticated user form, so there is "+
				"nothing to write", want.User)
		}
	} else if strings.TrimSpace(want.AuthSecret) != "" {
		// A passphrase with no user names nobody. Refused rather than dropped:
		// the caller believes it set an identity.
		return res, fmt.Errorf("an SNMPv3 authentication passphrase was supplied with no " +
			"user name: there is nothing to write it on")
	}

	if wantsV3 {
		var _, v3err = c.applySNMPv3(ctx, want, authKw, &res)
		if v3err != nil {
			return res, v3err
		}
	}

	var community = strings.TrimSpace(want.Community)
	if community == "" {
		if !wantsV3 {
			res.Skipped = append(res.Skipped, "no community supplied: nothing was written")
		}
		return res, nil
	}
	if !validCommunity(community) {
		return res, fmt.Errorf("community rejected: this CLI takes the string as a bare " +
			"word, so spaces and quotes cannot be sent safely")
	}

	// IDEMPOTENT BY OBSERVATION, the same discipline as the ProCurve
	// connector's ApplySNMP: the configuration is re-read and the community
	// looked for. A device that already carries it is left alone and reports
	// CommunitySet=false — re-writing a fleet that is already conformant
	// would churn the running config on every pass for nothing. The FULL
	// "show running-config" is read rather than a "| include" filter,
	// because that filter is erratic on this firmware (cf. readLinks:
	// "| include snmp" returns nothing on a config that contains
	// "snmp-server community" — captured 2026-08-03).
	var cfgOut, cfgErr = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
	if cfgErr == nil && strings.TrimSpace(cfgOut) != "" {
		var have string
		for _, have = range ParseRunningConfig(cfgOut).Communities {
			if have == community {
				res.Enabled = true
				c.step(true, "SNMP community already in place", "nothing rewritten")
				return res, nil
			}
		}
	}

	// "0" is the plain-text encryption type, the form the captured
	// configuration stores ("snmp-server community 0 <string> RO").
	var cmds = []string{fmt.Sprintf("snmp-server community 0 %s ro", community)}
	err = c.configBlock(ctx, cmds)
	if err != nil {
		c.step(false, "SNMP community refused", firstLine(err.Error()))
		return res, fmt.Errorf("snmp-server community on %s: %w", c.host, err)
	}
	res.Enabled = true
	res.CommunitySet = true
	c.step(true, "SNMP v2c community set", "read-only")

	// The agent answers as soon as a community exists — "show snmp" was
	// already counting input packets before one was configured (captured), so
	// nothing else has to be turned on.
	return res, nil
}

// validCommunity: the CLI takes the community as a bare word.
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

// EnsureLLDP turns LLDP on, which this platform does NOT do by default.
//
// Captured 2026-08-03: a factory-reset S3400 answers "LLDP is not enabled"
// to "show lldp neighbor", and the running-config carries no "lldp run".
// Until it is set the switch is invisible to every neighbour in the fleet and
// sees none itself — the topology view stays empty on a perfectly healthy
// device, which is exactly the failure mode worth writing a method for.
// IDEMPOTENT BY OBSERVATION, like ApplySNMP above: when the session has not
// read the configuration yet, one cheap "show lldp neighbor" decides —
// this firmware answers "LLDP is not enabled" when it is off (captured),
// and anything else proves it is on. A device already running LLDP gets
// nothing written; only an undecidable read (command failed outright) falls
// through to the write, which the hand survey proved harmless to repeat.
func (c *Client) EnsureLLDP(ctx context.Context) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	if !c.lldpKnown {
		var _, on, rerr = c.ReadLLDPNeighbors(ctx)
		if rerr == nil {
			c.lldpOn = on
			c.lldpKnown = true
		}
	}
	if c.lldpKnown && c.lldpOn {
		c.step(true, "LLDP already enabled", "\"lldp run\" already active — nothing written")
		return nil
	}
	err = c.configBlock(ctx, []string{"lldp run"})
	if err != nil {
		c.step(false, "\"lldp run\" refused", firstLine(err.Error()))
		return fmt.Errorf("lldp run on %s: %w", c.host, err)
	}
	c.lldpOn = true
	c.lldpKnown = true
	c.step(true, "LLDP enabled", "\"lldp run\" — neighbours appear within one advertisement interval")
	return nil
}

// ReadLLDPNeighbors returns the neighbour table.
//
// Returns an empty list AND a note when LLDP is off, rather than an error: an
// operator asking "who is on the other end" deserves "LLDP is not enabled
// here", not a failed read.
func (c *Client) ReadLLDPNeighbors(ctx context.Context) ([]LLDPNeighbor, bool, error) {
	var out, err = c.cli.runPaged(ctx, "show lldp neighbor", cmdTimeout)
	if strings.Contains(out, "LLDP is not enabled") {
		return nil, false, nil
	}
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, false, fmt.Errorf("show lldp neighbor on %s: %w", c.host, err)
	}
	return parseLLDPNeighbors(out), true, nil
}
