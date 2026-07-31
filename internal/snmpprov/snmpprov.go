// Package snmpprov decides a device's SNMP identity.
//
// It sets nothing and writes nothing: it ANSWERS THE QUESTION "what would it
// take to monitor this device?". Separating decision from action is what lets
// you validate it on a real fleet before a single write goes out
// (docs/03-Security/SNMP Identity §5).
package snmpprov

import (
	"meshcraft/internal/driver"
	"meshcraft/internal/snmpv3"
)

// Credential kinds, as `site_credentials.kind` carries them.
const (
	// KindV2c is SITE-scoped: a community string is plain text, nothing
	// prevents sharing it.
	KindV2c = "snmp_v2c"
	// KindV3 is ALWAYS tag-scoped: a v3 identity is a key derived from an
	// algorithm pair, and two devices with different algorithms cannot share
	// the same one.
	KindV3 = "snmp_v3"
)

// Policy is the floor wanted by operations.
type Policy struct {
	// MinAuth / MinPriv: the floor. A device below it is still deployed —
	// withholding monitoring hardens nothing — but marked non-conformant.
	MinAuth string
	MinPriv string
	// RequireV3: refuse v2c fallback. This is a FILTER, not hardening:
	// it does not make old devices better, it deprives them of SNMP.
	RequireV3 bool
	// AllowWeakFallback: accept v2c when v3 is required but impossible.
	AllowWeakFallback bool
}

// DefaultPolicy follows the spec (§2.1 Service Identity and Rotation): the
// best possible, floor SHA-256 / AES-128.
func DefaultPolicy() Policy {
	return Policy{MinAuth: "sha256", MinPriv: "aes128"}
}

// Plan states what the device needs.
type Plan struct {
	// Version: "v3", "v2c", or "" when SNMP is not manageable.
	Version string `json:"version"`
	// Tag: the technical tag to apply, empty for v2c.
	Tag string `json:"tag,omitempty"`
	// Kind: the credential kind to look up or generate.
	Kind string `json:"kind,omitempty"`
	// Auth / Priv: the chosen algorithms.
	Auth string `json:"auth,omitempty"`
	Priv string `json:"priv,omitempty"`
	// Conform: does the pair meet the policy floor?
	Conform bool `json:"conform"`
	// Reason explains the choice IN PLAIN LANGUAGE. This is what the UI shows
	// next to a non-conformant or unsupervised device: "no SNMP" with no reason
	// sends you looking for an outage that does not exist.
	Reason string `json:"reason"`
}

// Supervised reports whether a plan yields something collectable.
func (p Plan) Supervised() bool { return p.Version != "" }

// For decides from connector capabilities and policy.
func For(caps driver.Capabilities, pol Policy) Plan {
	if pol.MinAuth == "" {
		pol = DefaultPolicy()
	}
	var hasV2c, hasV3 bool
	var v string
	for _, v = range caps.SNMPVersions {
		switch v {
		case "v2c", "v1":
			hasV2c = true
		case "v3":
			hasV3 = true
		}
	}

	if hasV3 {
		var choice, conform, ok = snmpv3.Negotiate(
			caps.SNMPv3Auth, caps.SNMPv3Priv, pol.MinAuth, pol.MinPriv)
		if ok {
			var p = Plan{
				Version: "v3", Tag: choice.Tag(), Kind: KindV3,
				Auth: choice.Auth, Priv: choice.Priv, Conform: conform,
			}
			if conform {
				p.Reason = "SNMPv3 conformant to the policy floor."
			} else {
				// State it precisely: "non-conformant" without why allows
				// neither fixing nor deciding to accept the risk.
				p.Reason = "SNMPv3 deployed with what the device can do (" +
					choice.Auth + "/" + privLabel(choice.Priv) + "), below the floor " +
					pol.MinAuth + "/" + pol.MinPriv + ": monitored, but NON-conformant."
			}
			return p
		}
	}

	// v3 required and impossible: this is a filter, and it deprives SNMP. The
	// device must appear in an explicit list rather than vanish.
	if pol.RequireV3 && !pol.AllowWeakFallback {
		return Plan{Reason: "SNMP not managed: policy requires v3 and this device " +
			"cannot do it in a usable way."}
	}
	if hasV2c {
		var why = "SNMPv2c: this device cannot do usable v3."
		if hasV3 {
			why = "SNMPv2c: the v3 advertised by this device offers no " +
				"usable authentication algorithm."
		}
		if pol.RequireV3 {
			why += " Fallback allowed by policy."
		}
		return Plan{Version: "v2c", Kind: KindV2c, Reason: why}
	}
	return Plan{Reason: "SNMP not managed: this device advertises no SNMP version."}
}

func privLabel(p string) string {
	if p == "" {
		return "no encryption"
	}
	return p
}
