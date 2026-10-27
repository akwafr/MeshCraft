package eos

// The self-signed TLS builders, and the coupling between a certificate and the
// address it names.
//
// Written from a CAPTURE rather than from the vendor reference: the command
// forms below were run on a DCS-7050SX-64-R (EOS 4.28.13.1M) on 2026-08-07, the
// profile came up "valid", and internal/gnmipoll subscribed against the
// resulting certificate with verification ON. That is why these assertions are
// exact strings — they are transcripts, not guesses.

import (
	"strings"
	"testing"
)

// THE CERTIFICATE MUST CARRY THE MANAGEMENT ADDRESS, and the builder refuses
// without one.
//
// The refusal is the point. A certificate with no IP SAN generates cleanly,
// installs cleanly, and reports "valid" — and then fails every handshake with
// "certificate is valid for X, not Y", three layers away from its cause. Captured
// exactly that way: the collector's error named the mismatch, not the
// certificate.
func TestSelfSignedCertRefusesWithoutAManagementAddress(t *testing.T) {
	var bad = []string{"", "   ", "core01-lab", "192.0.2.999", "not-an-ip"}
	var s string
	for _, s = range bad {
		var _, err = SelfSignedCertCmds(s, "core01-lab")
		if err == nil {
			t.Errorf("SelfSignedCertCmds(%q) produced a certificate with no usable IP SAN", s)
		}
	}
}

// The captured command forms, in the captured ORDER.
//
// The key comes first because the certificate refuses otherwise — "Key not
// found under sslkey:" is the observed error, not a deduction.
func TestSelfSignedCertCmdsMatchTheCapturedForms(t *testing.T) {
	var cmds, err = SelfSignedCertCmds("192.0.2.5", "core01-lab")
	if err != nil {
		t.Fatalf("SelfSignedCertCmds: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("%d commands, want the key then the certificate: %v", len(cmds), cmds)
	}
	if !strings.HasPrefix(cmds[0], "security pki key generate rsa 2048 ") {
		t.Errorf("first command is not the key generation: %q", cmds[0])
	}
	var cert = cmds[1]
	// The IP SAN is the load-bearing part; assert it explicitly rather than by
	// matching the whole line, so a reordering of the optional clauses does not
	// fail the test that guards the essential one.
	if !strings.Contains(cert, "subject-alternative-name ip 192.0.2.5") {
		t.Errorf("the certificate carries no IP subject alternative name: %q", cert)
	}
	if !strings.Contains(cert, "dns core01-lab") {
		t.Errorf("the hostname SAN is missing: %q", cert)
	}
	// EOS takes ONE subject-alternative-name clause carrying both; a second
	// clause does not parse.
	if strings.Count(cert, "subject-alternative-name") != 1 {
		t.Errorf("the SAN clause is repeated, which EOS does not parse: %q", cert)
	}
	if !strings.Contains(cert, "key "+TLSKeyName) {
		t.Errorf("the certificate does not reference the generated key: %q", cert)
	}
}

// With no hostname, the certificate still gets its IP SAN — the only part
// verification actually uses — and the address stands in as the common name.
func TestSelfSignedCertWorksWithNoHostname(t *testing.T) {
	var cmds, err = SelfSignedCertCmds("192.0.2.5", "")
	if err != nil {
		t.Fatalf("SelfSignedCertCmds: %v", err)
	}
	var cert = cmds[1]
	if !strings.Contains(cert, "subject-alternative-name ip 192.0.2.5") {
		t.Errorf("no IP SAN without a hostname: %q", cert)
	}
	if strings.Contains(cert, "dns ") {
		t.Errorf("an empty hostname produced a dns SAN: %q", cert)
	}
	if !strings.Contains(cert, "common-name 192.0.2.5") {
		t.Errorf("the address did not stand in as common name: %q", cert)
	}
}

// TWO SUBMODES, TWO EXITS. `management security` opens one and `ssl profile`
// opens a second inside it; exiting once would leave the session in
// config-mgmt-security and have the next line of the enclosing push read as a
// security keyword.
func TestSSLProfileCmdsCloseBothSubmodes(t *testing.T) {
	var cmds = SSLProfileCmds()
	var exits int
	var c string
	for _, c = range cmds {
		if c == "exit" {
			exits++
		}
	}
	if exits != 2 {
		t.Errorf("%d exits, want one per submode: %v", exits, cmds)
	}
	if cmds[0] != "management security" {
		t.Errorf("the block does not open management security: %v", cmds)
	}
	if !strings.Contains(strings.Join(cmds, "\n"), "certificate "+TLSCertName+" key "+TLSKeyName) {
		t.Errorf("the profile binds no certificate and key: %v", cmds)
	}
}

// The ssl profile is written BEFORE the transport that references it.
//
// EOS accepts `ssl profile X` naming a profile that does not exist and then
// refuses to bring the transport up — a block that looks written and serves
// nothing, which is this connector's recurring failure shape.
func TestProvisioningWritesTheProfileBeforeTheTransport(t *testing.T) {
	var cmds, _ = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.5", PrefixLen: 24, Gateway: "192.0.2.1",
		Username: "mcadmin", Password: "secret1234567890",
		GNMI:        true,
		GNMIOptions: GNMIOpts{SSLProfile: TLSProfileName},
	})
	var joined = strings.Join(cmds, "\n")
	var profileAt = strings.Index(joined, "ssl profile "+TLSProfileName+"\n")
	var transportAt = strings.Index(joined, "transport grpc default")
	if profileAt < 0 {
		t.Fatalf("no ssl profile block in the push: %v", cmds)
	}
	if transportAt < 0 {
		t.Fatalf("no gNMI transport in the push: %v", cmds)
	}
	if profileAt > transportAt {
		t.Error("the ssl profile is written AFTER the transport that references it: " +
			"EOS would accept the reference and refuse to bring the transport up")
	}
}

// A push that asks for gNMI without naming a profile stays PLAINTEXT, and emits
// no profile block. Turning encryption on must be a choice somebody made, not a
// default that appears in a diff.
func TestGNMIWithoutAProfileEmitsNoSSLBlock(t *testing.T) {
	var cmds, _ = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.5", PrefixLen: 24,
		Username: "mcadmin", Password: "secret1234567890",
		GNMI: true,
	})
	var joined = strings.Join(cmds, "\n")
	if strings.Contains(joined, "management security") {
		t.Error("a push that named no ssl profile wrote one anyway")
	}
	if !strings.Contains(joined, "transport grpc default") {
		t.Error("the gNMI transport is missing")
	}
}

// HasGNMICertificate reads the profile off a running configuration. It is what
// the re-address path consults BEFORE renumbering a switch, which is the only
// moment it can still be asked.
func TestHasGNMICertificateReadsTheRunningConfig(t *testing.T) {
	var with = "management security\n   ssl profile " + TLSProfileName +
		"\n      certificate " + TLSCertName + " key " + TLSKeyName + "\n"
	if !HasGNMICertificate(with) {
		t.Error("a configuration carrying the profile was not recognised")
	}
	if HasGNMICertificate("management security\n   ssl profile ARISTA_DEFAULT_PROFILE\n") {
		t.Error("Arista's own default profile was mistaken for ours: renumbering would " +
			"warn about a certificate meshcraft never created")
	}
	if HasGNMICertificate("") {
		t.Error("an empty configuration reported a certificate")
	}
}

// A PROVISIONING RUN PERSISTS ITSELF. Regression guard for a whole run lost on
// reload.
//
// The builder used to produce a complete, correct configuration that lived
// entirely in the running config: optics, VLANs, SVI, address, route, trunks,
// gNMI and the account all vanished at the next reload, and the switch came back
// to the empty startup-config ZTP had left. Nothing reported it — the push
// succeeded and the device was reachable until somebody power-cycled it.
func TestProvisioningPushPersistsItself(t *testing.T) {
	var cmds, _ = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.5", PrefixLen: 24, Gateway: "192.0.2.1",
		Username: "mcadmin", Password: "secret1234567890",
	})
	if len(cmds) < 2 {
		t.Fatalf("push is too short to be a provisioning run: %v", cmds)
	}
	var last = cmds[len(cmds)-1]
	if last != SaveCmd {
		t.Errorf("the push ends with %q, not %q: everything it wrote is lost on reload",
			last, SaveCmd)
	}
	// `write memory` is an EXEC command and is REFUSED in configuration mode, so
	// the block must leave it first. Without the exit the save is the one line
	// whose failure is silent — the run looks complete and persists nothing,
	// which is the bug this test exists for, arriving by another door.
	if cmds[len(cmds)-2] != "end" {
		t.Errorf("the save is issued from configuration mode (%q before it): "+
			"\"write memory\" is not a config command and would be refused",
			cmds[len(cmds)-2])
	}
	// One save, not two. The fallback form belongs to the interactive path, which
	// can read the answer and choose; a blind command list sending both would put
	// an unexplained error in the middle of a console transcript.
	var saves int
	var c string
	for _, c = range cmds {
		if c == SaveCmd || c == SaveCmdFallback {
			saves++
		}
	}
	if saves != 1 {
		t.Errorf("%d save commands in one push, want exactly one", saves)
	}
}

// FQDN's empty cases are the whole difficulty: blind concatenation produces
// ".example.com" or "switch." — both install cleanly and match nothing, so the
// certificate is only discovered to be useless at the first handshake.
func TestFQDNHandlesTheEmptyCases(t *testing.T) {
	var cases = []struct{ host, domain, want string }{
		{"core01", "lab.example", "core01.lab.example"},
		{"core01", "", "core01"},
		{"", "lab.example", ""},
		{"", "", ""},
		{"  core01  ", "  lab.example  ", "core01.lab.example"},
		// A trailing dot on the domain is how a DNS root gets typed in by hand.
		{"core01", "lab.example.", "core01.lab.example"},
		// Already qualified: appending a second domain would turn a name somebody
		// deliberately wrote in full into "sw.a.example.com".
		{"core01.other.lan", "lab.example", "core01.other.lan"},
	}
	var c struct{ host, domain, want string }
	for _, c = range cases {
		var got = FQDN(c.host, c.domain)
		if got != c.want {
			t.Errorf("FQDN(%q, %q) = %q, want %q", c.host, c.domain, got, c.want)
		}
	}
}

// The renewal note must CARRY the commands, because the gesture it names does
// not exist anywhere else in the product.
//
// This is the assertion that keeps the warning honest: `CertificateOutlivesAddress`
// ends with "re-run the certificate gesture", and until this function there was
// nothing to re-run — no code path issues a certificate, and the two builders'
// only caller (MgmtPushCmds) is itself called by nothing.
func TestCertificateRenewalNoteCarriesTheCommands(t *testing.T) {
	var note = CertificateRenewalNote("192.0.2.36", "core01-lab")
	if !strings.HasPrefix(note, CertificateOutlivesAddress) {
		t.Fatalf("the note dropped the warning it exists to answer: %q", note)
	}
	// The NEW address, in the SAN clause. A note quoting the old one would be
	// worse than no note: it would have the operator regenerate exactly the
	// certificate that just stopped working.
	if !strings.Contains(note, "subject-alternative-name ip 192.0.2.36") {
		t.Errorf("the note does not name the new address in the SAN clause: %q", note)
	}
	if !strings.Contains(note, "security pki key generate rsa") {
		t.Errorf("the note omits the key generation, without which the certificate "+
			"refuses with \"Key not found under sslkey:\": %q", note)
	}
	// Both halves, and NAMED as such. EXEC commands and configuration-mode
	// commands pasted into the wrong prompt are both rejected, so a note that ran
	// them together would be a footgun rather than an instruction.
	if !strings.Contains(note, "enable prompt") ||
		!strings.Contains(note, "configuration mode") {
		t.Errorf("the note does not say which prompt each half belongs at: %q", note)
	}
	if !strings.Contains(note, "ssl profile "+TLSProfileName) {
		t.Errorf("the note omits the profile that re-binds the certificate: %q", note)
	}
	// It must not read as something meshcraft did.
	if !strings.Contains(note, "manual") {
		t.Errorf("the note does not say the gesture is manual, which is the one "+
			"thing an operator must not assume otherwise: %q", note)
	}
}

// A DHCP re-address does not know its new address, and a certificate built on a
// guessed one is precisely what SelfSignedCertCmds refuses to emit. The note then
// degrades to the warning alone.
func TestCertificateRenewalNoteWithNoAddressIsTheWarningAlone(t *testing.T) {
	var bad = []string{"", "   ", "192.0.2.36/24", "not-an-ip"}
	var s string
	for _, s = range bad {
		var note = CertificateRenewalNote(s, "core01-lab")
		if note != CertificateOutlivesAddress {
			t.Errorf("CertificateRenewalNote(%q) invented a command sequence: %q", s, note)
		}
	}
}

// With no hostname the note still stands: the IP SAN is the only part
// verification uses, and the collector subscribes by address.
func TestCertificateRenewalNoteWithNoHostname(t *testing.T) {
	var note = CertificateRenewalNote("192.0.2.36", "")
	if !strings.Contains(note, "subject-alternative-name ip 192.0.2.36") {
		t.Errorf("no IP SAN without a hostname: %q", note)
	}
	if strings.Contains(note, " dns ") {
		t.Errorf("an empty hostname produced a dns SAN: %q", note)
	}
}

// The certificate SAN takes the device's OWN domain when the configuration
// declares one, and stays bare when it does not.
//
// The second half is the one that matters: `ip domain-name` is a DEDUCED line
// form (no fixture in this package was captured with one), so the parse must not
// be load-bearing. A device that declares no domain has to produce exactly the
// SAN it produced before the field existed, or an unconfirmed regex would have
// silently changed every certificate note on the fleet.
func TestRunningConfigDomainQualifiesTheSAN(t *testing.T) {
	var withDomain = "hostname core01-lab\nip domain-name lab.example\n!\n"
	var rc = parseRunningConfig(withDomain, "")
	if rc.DomainName != "lab.example" {
		t.Fatalf("domain not read: %q", rc.DomainName)
	}
	var got = FQDN(rc.Hostname, rc.DomainName)
	if got != "core01-lab.lab.example" {
		t.Errorf("SAN = %q, want core01-lab.lab.example", got)
	}

	var noDomain = "hostname core01-lab\n!\n"
	var bare = parseRunningConfig(noDomain, "")
	if bare.DomainName != "" {
		t.Errorf("invented a domain: %q", bare.DomainName)
	}
	got = FQDN(bare.Hostname, bare.DomainName)
	if got != "core01-lab" {
		t.Errorf("SAN without a domain = %q, want the bare hostname", got)
	}
}

// `ip domain-list` is a SEARCH list, not the device's own domain. Taking its
// first entry would qualify the SAN with a name the device does not answer to.
func TestDomainListIsNotTheDomainName(t *testing.T) {
	var cfg = "hostname core01-lab\nip domain-list example.net\nip domain-list two.example.net\n!\n"
	var rc = parseRunningConfig(cfg, "")
	if rc.DomainName != "" {
		t.Errorf("domain-list taken as domain-name: %q", rc.DomainName)
	}
}
