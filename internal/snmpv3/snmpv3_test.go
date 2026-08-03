package snmpv3

import "testing"

const (
	floorAuth = "sha256"
	floorPriv = "aes128"
)

func TestNormalize(t *testing.T) {
	var cas = map[string]string{
		"SHA-256": "sha256", "sha_256": "sha256", " AES 128 ": "aes128", "MD5": "md5",
	}
	var in, want string
	for in, want = range cas {
		var got = Normalize(in)
		if got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNegotiateStrongest(t *testing.T) {
	var c, conform, ok = Negotiate(
		[]string{"MD5", "SHA", "SHA-256"}, []string{"DES", "AES-128"},
		floorAuth, floorPriv)
	if !ok {
		t.Fatal("a v3 device must be negotiable")
	}
	if c.Auth != "sha256" || c.Priv != "aes128" {
		t.Errorf("chosen pair %+v", c)
	}
	if !conform {
		t.Error("this pair meets the floor, it must be conformant")
	}
}

func TestWeakV3Platform(t *testing.T) {
	var c, conform, ok = Negotiate(
		[]string{"MD5", "SHA"}, []string{"DES"}, floorAuth, floorPriv)
	if !ok {
		t.Fatal("the device can do v3, even weak v3")
	}
	if c.Auth != "sha" || c.Priv != "des" {
		t.Errorf("strongest available expected, got %+v", c)
	}
	if conform {
		t.Error("SHA-1/DES must NEVER be presented as conformant")
	}
	if c.Tag() != "snmpv3-sha-des" {
		t.Errorf("tag %q", c.Tag())
	}
}

func TestNoAuthenticationNoV3(t *testing.T) {
	var _, _, ok = Negotiate(nil, []string{"AES-128"}, floorAuth, floorPriv)
	if ok {
		t.Error("without authentication, there is no usable v3")
	}
	_, _, ok = Negotiate([]string{"kangaroo"}, nil, floorAuth, floorPriv)
	if ok {
		t.Error("an unknown algorithm must be ignored, not accepted")
	}
}

func TestAuthWithoutEncryption(t *testing.T) {
	var c, conform, ok = Negotiate([]string{"SHA-256"}, nil, floorAuth, floorPriv)
	if !ok || c.Priv != "" {
		t.Fatalf("unexpected pair %+v", c)
	}
	if conform {
		t.Error("without encryption, the floor is not met")
	}
	if c.Tag() != "snmpv3-sha256-nopriv" {
		t.Errorf("tag %q: missing encryption must be EXPLICIT", c.Tag())
	}
}

func TestParseTag(t *testing.T) {
	var c, ok = ParseTag("snmpv3-sha256-aes128")
	if !ok || c.Auth != "sha256" || c.Priv != "aes128" {
		t.Errorf("parse failed: %+v", c)
	}
	if c, ok = ParseTag("snmpv3-sha-nopriv"); !ok || c.Priv != "" {
		t.Errorf("nopriv must parse as no encryption: %+v", c)
	}
	var reject = []string{"widget", "snmpv3", "snmpv3-thingy-doodad", "snmpv3-sha", ""}
	var s string
	for _, s = range reject {
		if _, ok = ParseTag(s); ok {
			t.Errorf("%q must not pass as a v3 tag", s)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	var tag string
	for _, tag = range KnownTags() {
		var c, ok = ParseTag(tag)
		if !ok {
			t.Fatalf("generated tag not readable: %q", tag)
		}
		if c.Tag() != tag {
			t.Errorf("unstable round trip: %q → %q", tag, c.Tag())
		}
	}
}

func TestSupported(t *testing.T) {
	if !Supported("SHA-256", "AES-128") || !Supported("md5", "none") {
		t.Error("known algorithms must be accepted")
	}
	if Supported("kangaroo", "aes128") || Supported("sha256", "rot13") {
		t.Error("an unknown algorithm must not be accepted")
	}
}
