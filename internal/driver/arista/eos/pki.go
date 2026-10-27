package eos

// Self-signed TLS for the gNMI transport: key, certificate, ssl profile.
//
// # WHY SELF-SIGNED IS NOT A COMPROMISE HERE
//
// The obvious reading of "self-signed" is "TLS with the verification turned
// off", and that is what it usually means in practice — an operator generates a
// certificate nothing can check, and every client is configured to skip
// verification, which defeats an active attacker completely. It is barely better
// than plaintext and it is how most switch TLS ends up deployed.
//
// That is NOT what this does. A self-signed certificate is its own root, so
// handing the collector a COPY OF IT as its trust anchor gives genuine
// verification: the connection is refused unless the switch presents exactly
// that certificate. It is certificate PINNING, and against the threat that
// matters here — somebody on the management VLAN who wants the device login the
// subscriber sends — it is as strong as a private CA and needs no CA to exist.
//
// VERIFIED, not reasoned: captured 2026-08-07 on a DCS-7050SX-64-R running EOS
// 4.28.13.1M. The commands below were run in this order, the profile came up
// `valid`, and internal/gnmipoll subscribed with the certificate as its
// [gnmipoll.Target.CAPEM] and streamed samples with SkipVerify FALSE.
//
// One detail of that capture is worth recording because it looks like it should
// have failed: EOS emits the certificate with NO `basicConstraints` extension, so
// it is not marked CA:TRUE. Go's verifier accepts it as its own root regardless —
// its CA check is `BasicConstraintsValid && !IsCA`, and an absent extension
// leaves the first term false. A future Go release tightening that would break
// pinning fleet-wide, which is exactly the kind of dependency worth having
// written down rather than rediscovered.
//
// # THE SAN IS THE WHOLE THING, AND IT CONTAINS THE MANAGEMENT ADDRESS
//
// The proxy subscribes to a device BY ADDRESS — it has no name for it — so TLS
// verification checks the address against the certificate's subject alternative
// names. A certificate carrying only a common name verifies against nothing the
// collector can present, and the handshake fails with "certificate is valid for
// X, not Y" — which reads like a broken collector and is in fact a certificate
// that was never usable.
//
// So [SelfSignedCertCmds] REQUIRES the management address and writes it as an IP
// SAN. And that couples the certificate to the address, which is the sharp edge
// this file exists to make visible: see [CertificateOutlivesAddress].

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Names of the key, certificate and profile this connector creates.
//
// FIXED rather than generated, and that is what makes the gesture idempotent:
// re-running provisioning overwrites the same three objects instead of littering
// the certificate store with one per run. They carry the product's name so an
// operator reading `dir certificate:` knows what made them and can tell them from
// Arista's own ARISTA_ROOT_CA / ARISTA_SIGNING_CA / ARISTA_DEFAULT_PROFILE.
const (
	TLSKeyName     = "meshcraft.key"
	TLSCertName    = "meshcraft.crt"
	TLSProfileName = "meshcraft"
)

// TLSKeyBits is the RSA modulus size. EOS offers 2048, 3072 and 4096.
//
// 2048, and it is a deliberate floor rather than the strongest option: this key
// protects a subscription on a management VLAN, it is regenerated whenever the
// address changes, and the switch's control-plane CPU does the handshake. 4096
// buys margin against an adversary who does not exist in this threat model and
// costs it on every reconnect of every device in a fleet.
const TLSKeyBits = 2048

// FreshCertificateIsNotYetValid is the failure a certificate has the moment it
// is created, and it is the one nobody predicts.
//
// # OBSERVED, not theorised
//
// Generating a certificate on the lab switch and subscribing to it immediately
// gave:
//
//	x509: certificate has expired or is not yet valid:
//	current time 2026-08-07T20:30:13+02:00 is before 2026-08-07T18:32:08Z
//
// The switch stamps `notBefore` with ITS OWN clock, and that clock was about a
// hundred seconds ahead of the collector's. So the certificate is genuinely not
// valid yet — for exactly as long as the skew lasts — and every handshake is
// refused until the collector's clock catches up.
//
// Three things make this worth a named constant rather than a footnote:
//
//  1. THE MESSAGE SAYS "EXPIRED". Go's x509 error covers both bounds in one
//     sentence, and the first word a reader sees on a certificate created ninety
//     seconds ago is "expired". The obvious conclusions — the validity is wrong,
//     the certificate is broken, regenerate it — are all wrong, and regenerating
//     restarts the wait;
//  2. IT SELF-HEALS, which is worse. The failure appears at provisioning, when
//     somebody is watching, and is gone by the time they investigate. That is
//     the shape of bug that gets recorded as "flaky" and never fixed;
//  3. IT CANNOT BE FIXED IN THE COMMAND. EOS's `validity` sets a DURATION; there
//     is no way to backdate `notBefore` from the CLI. The only real remedies are
//     NTP on the switch and a collector that keeps retrying — which the proxy's
//     supervisor does anyway (cmd/proxy/gnmi.go), with backoff, so a fleet
//     recovers on its own.
//
// The practical consequence for a caller: do NOT verify a freshly generated
// certificate by subscribing to it immediately and treating a refusal as
// failure. The subscription is expected to fail for the duration of the clock
// skew.
const FreshCertificateIsNotYetValid = "a certificate generated seconds ago can be refused as " +
	"\"not yet valid\": the switch stamps it with its own clock, and telemetry only " +
	"starts once the collector's clock reaches that instant. Go reports it as \"expired " +
	"or is not yet valid\", which reads like the wrong thing — check the switch's time " +
	"before regenerating anything"

// TLSValidityDays is how long the certificate lasts.
//
// Ten years, which for a public CA would be indefensible and here is the honest
// choice: nothing in this product renews certificates yet. A one-year validity
// would mean every switch's telemetry stops on an anniversary nobody is watching
// for, with the same silent symptom as a wrong SAN. A long life is not security
// theatre when the alternative is a scheduled outage — and the certificate is
// pinned, so its expiry is the ONLY thing that revokes it. Shorten this the day
// something renews it.
const TLSValidityDays = 3650

// FQDN joins a hostname and a site domain the way the certificate wants it.
//
// A function rather than a `+"."+` at the call site because the empty cases are
// the whole difficulty and there are three of them: no hostname (nothing to
// qualify), no domain (the bare name, which is what the SAN carried before
// domains existed), and neither. Concatenating blindly produces ".example.com"
// or "switch." — both of which install cleanly and match nothing.
func FQDN(hostname, domain string) string {
	var h = strings.TrimSpace(hostname)
	var d = strings.Trim(strings.TrimSpace(domain), ".")
	if h == "" {
		return ""
	}
	if d == "" {
		return h
	}
	if strings.Contains(h, ".") {
		// Already qualified. Appending a second domain would produce
		// "sw.a.example.com" from a hostname somebody deliberately wrote in full.
		return h
	}
	return h + "." + d
}

// SelfSignedCertCmds returns the EXEC commands that create the key and the
// certificate. NOT configuration-mode commands — these run at the enable prompt,
// which is why they are separate from [SSLProfileCmds] below.
//
// `mgmtIP` is REQUIRED and the function refuses without it, rather than emitting
// a certificate with no IP SAN. That refusal is the whole point: such a
// certificate generates cleanly, installs cleanly, shows `valid` in
// `show management security ssl profile`, and then fails every single handshake
// — a failure that looks like a broken collector and is three layers away from
// its cause.
//
// `dnsName` is the device's hostname and is optional: it costs one SAN entry and
// makes the certificate usable if a site ever resolves its switches by name.
// Pass it through [FQDN] with the site's domain to get a name a collector can
// actually verify against.
func SelfSignedCertCmds(mgmtIP, dnsName string) ([]string, error) {
	var ip = strings.TrimSpace(mgmtIP)
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("a gNMI certificate needs the management ADDRESS in its "+
			"subject alternative name — the collector subscribes by address and TLS "+
			"verification checks it — and %q is not one. A certificate without it "+
			"installs cleanly and then fails every handshake", mgmtIP)
	}
	var params = []string{
		"security pki certificate generate self-signed " + TLSCertName,
		"key " + TLSKeyName,
		"validity " + strconv.Itoa(TLSValidityDays),
		"parameters common-name " + certCommonName(dnsName, ip),
		"subject-alternative-name ip " + ip,
	}
	var dns = strings.TrimSpace(dnsName)
	if dns != "" {
		// Appended to the SAME subject-alternative-name clause: EOS takes
		// `subject-alternative-name ip A dns B`, and a second clause would not
		// parse. Captured in that order.
		params = append(params, "dns "+dns)
	}
	return []string{
		// The key first — the certificate refuses with "Key not found under
		// sslkey:" otherwise, which is the observed error and not a guess.
		fmt.Sprintf("security pki key generate rsa %d %s", TLSKeyBits, TLSKeyName),
		strings.Join(params, " "),
	}, nil
}

// certCommonName picks the subject common name.
//
// The hostname when there is one, the address otherwise. The common name is
// decorative for verification — modern TLS reads the SAN and ignores it — but it
// is what `show` commands and certificate viewers display, so an operator looking
// at the switch should see something that names the switch.
func certCommonName(dnsName, ip string) string {
	var dns = strings.TrimSpace(dnsName)
	if dns != "" {
		return dns
	}
	return ip
}

// SSLProfileCmds returns the configuration-mode lines binding the certificate
// and key into a named ssl profile.
//
// The nesting is real, as it is for the gNMI block, and the exits are not
// decoration: `management security` opens a submode and `ssl profile <name>`
// opens a second inside it. A caller that exited once would leave the session in
// `config-mgmt-security` and have the next line of the enclosing push
// interpreted as a security keyword.
func SSLProfileCmds() []string {
	return []string{
		"management security",
		"ssl profile " + TLSProfileName,
		"certificate " + TLSCertName + " key " + TLSKeyName,
		"exit",
		"exit",
	}
}

// CertificateOutlivesAddress is the warning that must travel with any gNMI
// certificate this connector creates.
//
// # THE FAILURE IT NAMES
//
// The certificate's SAN carries the management ADDRESS, because that is what the
// collector verifies against. meshcraft RE-ADDRESSES devices — that is what
// [Client.ApplyMgmtAddr] and the whole management migration flow exist to do — and
// nothing about moving an address touches the certificate. So the moment a switch
// is renumbered:
//
//   - the switch keeps serving a certificate naming its OLD address;
//   - the collector dials the NEW one and refuses the handshake;
//   - `show management api gnmi` still says "Enabled: yes", the ssl profile
//     still says "valid", and the switch is still perfectly reachable by SSH.
//
// Every screen says healthy and the graphs stop. That is the same shape as every
// other failure this package documents, and it is worse than most because the
// gesture that caused it — a successful re-address — reports success.
//
// # WHY THIS IS A CONSTANT AND NOT JUST A COMMENT
//
// Because it has to reach an OPERATOR, not a reader of this file. It is returned
// in the re-address result's Notes so that it appears on the screen of whoever
// just moved the address, at the moment they moved it, rather than living here
// where only the next maintainer would find it.
const CertificateOutlivesAddress = "the gNMI telemetry certificate on this switch names its " +
	"PREVIOUS management address and no longer matches: streaming telemetry will fail " +
	"every handshake until it is regenerated, while the switch stays reachable and every " +
	"status stays green. Re-run the certificate gesture for this device"

// CertificateRenewalNote is [CertificateOutlivesAddress] with the commands that
// answer it, rendered for the new address.
//
// # WHY THE WARNING ALONE WAS NOT ENOUGH
//
// It ends with "re-run the certificate gesture for this device", and THERE IS
// NO SUCH GESTURE. Nothing in this product issues a certificate: the builders
// exist, [SelfSignedCertCmds] and [SSLProfileCmds], and their only caller was
// [MgmtPushCmds], which has no Go caller of its own — deliberately, and for the
// reason recorded on that function: it is the written specification the Web Serial
// script mirrors, not an entry point. So the two builders were reachable only from
// a gesture that runs in a browser, on a switch being provisioned, which is
// precisely NOT the situation an operator renumbering a live device is in.
//
// This function is what put them back on a Go path: it is called from
// [Client.ApplyMgmtAddressing], so the builders are no longer test-only. So an operator who just
// renumbered a switch was told they owed it something, and given no way to pay.
//
// Composing the lines here does not make the gesture automatic and deliberately
// does not: nothing below is sent, this is a STRING. What it removes is the
// three-file reconstruction an operator would otherwise have to do — the key
// bits, the validity, the exact spelling of the SAN clause, and the fact that
// the address must appear as an IP SAN or the certificate installs cleanly and
// fails every handshake.
//
// # THE TWO HALVES ARE LABELLED, AND THAT IS NOT COSMETIC
//
// [SelfSignedCertCmds] returns EXEC commands and [SSLProfileCmds] returns
// configuration-mode ones. Concatenating them into one runnable-looking list is
// exactly the mistake this function refuses to hand somebody: pasted at the
// enable prompt the profile lines are rejected, pasted in configuration mode the
// generation lines are. So they are emitted as two named sequences.
//
// The profile half is included even though the caller only reaches this on a
// device that ALREADY carries the profile (that is what
// [HasGNMICertificate] established). It is idempotent — the three object names
// are fixed constants, which is what the header calls the gesture's idempotency
// — and it is the half that re-binds a regenerated certificate. Whether EOS
// picks the new certificate up under a profile that is never re-entered has NOT
// been observed here, and re-entering it costs nothing.
//
// # THE EMPTY ADDRESS IS A REAL CASE
//
// A DHCP re-address does not know the new address — that is the point of DHCP —
// so there is nothing to put in a SAN. The warning is then returned alone rather
// than with a command carrying a guessed address, which would produce precisely
// the certificate [SelfSignedCertCmds] refuses to build.
func CertificateRenewalNote(mgmtIP, dnsName string) string {
	var exec, err = SelfSignedCertCmds(mgmtIP, dnsName)
	if err != nil {
		return CertificateOutlivesAddress
	}
	return CertificateOutlivesAddress + ". From the enable prompt: \"" +
		strings.Join(exec, "\" then \"") + "\" — then from configuration mode: \"" +
		strings.Join(SSLProfileCmds(), "\" then \"") +
		"\". Nothing here is sent by meshcraft: the certificate gesture is manual on " +
		"this connector"
}

// HasGNMICertificate reports whether a running configuration carries the ssl
// profile this connector creates.
//
// Read off the CONFIGURATION rather than from `show management security ssl
// profile`, because the caller that needs it — the re-address path — already has
// the running config in hand and must not spend a second round trip on a device
// it is about to renumber.
func HasGNMICertificate(runningConfig string) bool {
	return strings.Contains(runningConfig, "ssl profile "+TLSProfileName)
}
