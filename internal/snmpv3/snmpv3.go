// Package snmpv3 picks the best (authentication, encryption) pair a device can
// support, and names it.
//
// The name matters as much as the choice: it becomes a TAG (`snmpv3-sha-des`),
// and the tag carries the fleet's SNMP identity
// (cf. docs/en/01-Architecture/Tags and credential scope.md). Two devices that do
// not accept the same algorithms cannot share a v3 secret: keys derive from them.
//
// Overall rule: NO v3 tag = SNMPv2c. This is the default, and it is
// deliberately the weakest — a device we failed to qualify must not end up
// with a configuration it will silently refuse.
package snmpv3

import (
	"sort"
	"strings"

	"meshcraft/wire/mdpv1"
)

// Strength order, weakest to strongest. This is the only thing that decides:
// "the best" only makes sense against an explicit scale.
var authRank = map[string]int{
	"md5": 1, "sha": 2, "sha224": 3, "sha256": 4, "sha384": 5, "sha512": 6,
}

var privRank = map[string]int{
	"des": 1, "aes128": 2, "aes192": 3, "aes256": 4,
}

// Choice is a retained pair.
type Choice struct {
	Auth string `json:"auth"`
	Priv string `json:"priv"`
}

// Normalize puts an algorithm in canonical form: lowercase, no separator. The
// form is the wire's (mdpv1.NormalizeAlg): drivers compare what a device reads
// back against what the controller chose, and both sides must spell it the
// same way or every poll reports a drift.
func Normalize(a string) string {
	return mdpv1.NormalizeAlg(a)
}

// Supported reports whether meshcraft can name this algorithm.
func Supported(auth, priv string) bool {
	var _, okA = authRank[Normalize(auth)]
	if !okA {
		return false
	}
	if Normalize(priv) == "" || Normalize(priv) == "none" {
		return true
	}
	var _, okP = privRank[Normalize(priv)]
	return okP
}

func best(list []string, rank map[string]int) string {
	var out string
	var top int
	var a string
	for _, a = range list {
		var n = Normalize(a)
		var r, ok = rank[n]
		if ok && r > top {
			out, top = n, r
		}
	}
	return out
}

// Negotiate chooses what meshcraft will set on this device.
func Negotiate(devAuth, devPriv []string, floorAuth, floorPriv string) (c Choice, conform, ok bool) {
	c.Auth = best(devAuth, authRank)
	if c.Auth == "" {
		return Choice{}, false, false
	}
	c.Priv = best(devPriv, privRank)

	conform = authRank[c.Auth] >= authRank[Normalize(floorAuth)] &&
		privRank[c.Priv] >= privRank[Normalize(floorPriv)]
	return c, conform, true
}

const TagPrefix = "snmpv3-"

func (c Choice) Tag() string {
	var priv = c.Priv
	if priv == "" {
		priv = "nopriv"
	}
	return TagPrefix + c.Auth + "-" + priv
}

func ParseTag(tag string) (Choice, bool) {
	var t = strings.ToLower(strings.TrimSpace(tag))
	if !strings.HasPrefix(t, TagPrefix) {
		return Choice{}, false
	}
	var parts = strings.Split(strings.TrimPrefix(t, TagPrefix), "-")
	if len(parts) != 2 {
		return Choice{}, false
	}
	var c = Choice{Auth: parts[0], Priv: parts[1]}
	if c.Priv == "nopriv" {
		c.Priv = ""
	}
	var _, okA = authRank[c.Auth]
	if !okA {
		return Choice{}, false
	}
	if c.Priv != "" {
		var _, okP = privRank[c.Priv]
		if !okP {
			return Choice{}, false
		}
	}
	return c, true
}

func KnownTags() []string {
	var out []string
	var a, p string
	for a = range authRank {
		out = append(out, Choice{Auth: a}.Tag())
		for p = range privRank {
			out = append(out, Choice{Auth: a, Priv: p}.Tag())
		}
	}
	sort.Strings(out)
	return out
}
