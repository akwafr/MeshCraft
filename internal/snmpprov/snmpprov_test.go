package snmpprov

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/driver/instanton"
)

func caps(versions []string, auth, priv []string) driver.Capabilities {
	return driver.Capabilities{
		SNMPVersions: versions, SNMPv3Auth: auth, SNMPv3Priv: priv,
	}
}

// Test1930IsV2c: this line is an EXCEPTION, and the test locks it down.
func Test1930IsV2c(t *testing.T) {
	var d = &instanton.Driver1930{}
	var p = For(d.Capabilities("… 1930 JL685A"), DefaultPolicy())

	if p.Version != "v2c" || p.Kind != KindV2c {
		t.Fatalf("unexpected plan: %+v", p)
	}
	if p.Tag != "" {
		t.Errorf("a v2c plan carries no tag: %q", p.Tag)
	}
	if !p.Supervised() {
		t.Error("v2c is still monitoring")
	}
	if !strings.Contains(p.Reason, "v3") {
		t.Errorf("fallback must be explained: %q", p.Reason)
	}
}

func TestModernFleetConformant(t *testing.T) {
	var p = For(caps([]string{"v2c", "v3"},
		[]string{"md5", "sha", "sha256"}, []string{"des", "aes128"}), DefaultPolicy())
	if p.Version != "v3" || !p.Conform || p.Tag != "snmpv3-sha256-aes128" {
		t.Errorf("unexpected plan: %+v", p)
	}
}

func TestWithoutV3FallsBackToV2c(t *testing.T) {
	var p = For(caps([]string{"v1", "v2c"}, nil, nil), DefaultPolicy())
	if p.Version != "v2c" || p.Kind != KindV2c || p.Tag != "" {
		t.Errorf("unexpected plan: %+v", p)
	}
	if !p.Supervised() {
		t.Error("v2c is still monitoring")
	}
}

func TestV3AdvertisedButUnusable(t *testing.T) {
	var p = For(caps([]string{"v2c", "v3"}, []string{"kangourou"}, nil), DefaultPolicy())
	if p.Version != "v2c" {
		t.Fatalf("fallback expected: %+v", p)
	}
	if !strings.Contains(p.Reason, "no usable") {
		t.Errorf("fallback reason must be stated: %q", p.Reason)
	}
}

func TestRequireV3IsAFilter(t *testing.T) {
	var pol = DefaultPolicy()
	pol.RequireV3 = true

	var p = For(caps([]string{"v2c"}, nil, nil), pol)
	if p.Supervised() {
		t.Errorf("v3 required and impossible: device is not monitored: %+v", p)
	}
	if !strings.Contains(p.Reason, "not managed") {
		t.Errorf("reason must be explicit: %q", p.Reason)
	}

	pol.AllowWeakFallback = true
	if p = For(caps([]string{"v2c"}, nil, nil), pol); p.Version != "v2c" ||
		!strings.Contains(p.Reason, "Fallback allowed") {
		t.Errorf("fallback expected and reported: %+v", p)
	}
}

func TestNoSNMP(t *testing.T) {
	var p = For(caps(nil, nil, nil), DefaultPolicy())
	if p.Supervised() || p.Reason == "" {
		t.Errorf("a device without SNMP must be reported: %+v", p)
	}
}

func TestIdempotence(t *testing.T) {
	var c = caps([]string{"v3"}, []string{"sha", "md5"}, []string{"des"})
	var a = For(c, DefaultPolicy())
	var b = For(c, DefaultPolicy())
	if a != b {
		t.Errorf("unstable decision: %+v then %+v", a, b)
	}
}
