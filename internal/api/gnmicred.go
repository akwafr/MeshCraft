package api

// Resolving the credential a streaming-telemetry subscription authenticates with.
//
// # WHY TELEMETRY GETS A CREDENTIAL KIND OF ITS OWN
//
// This was the open question of the gNMI work — whether telemetry rides
// the provisioned service account or gets its own `SiteCredential` kind — and it
// is settled here in favour of its own kind. The argument is about what a leak
// costs, and the two are not comparable:
//
//   - `tunnel.SNMPWatch.Community` is a read-only v2c community. Every proxy
//     holds one per device it polls. Leaked, it READS A MIB;
//   - a gNMI subscriber authenticates against the switch's ordinary AAA stack
//     with a username and password. Leaked, it CONFIGURES THE SWITCH.
//
// Putting the fleet admin account into a watch pushed to every proxy would hand
// every proxy a privileged login for every Arista it watches — a materially
// bigger secret than the struct has ever carried, arriving as a side effect of a
// monitoring change. A separate kind means a proxy compromise costs a subscriber
// and not a configurer, and it means the telemetry account can be rotated on its
// own cadence without touching the account meshcraft adopts devices with.
//
// The cost is real: somebody has to create the credential, and until they do, the
// device is simply not subscribed (silently, per device — see gnmiWatchesFor).
// That is the correct failure: no telemetry is a visible gap, whereas an admin
// password on the wire is not.
//
// # UNVERIFIED, AND NAMED AS SUCH
//
// Whether a `network-operator` account suffices for a Subscribe on EOS, or
// whether it needs `network-admin`, has not been established on hardware. It
// matters because it decides whether this credential can be genuinely
// least-privilege or is an admin account after all. That question is still open.

import (
	"context"
	"strings"

	"meshcraft/internal/model"
)

// CredKindGNMI is the site-credential kind a streaming subscription uses.
//
// Deliberately NOT in internal/snmpprov, which owns `snmp_v2c` and `snmp_v3`:
// that package is about SNMP identity provisioning — deciding which version a
// device gets and pushing it — and a gNMI login is neither SNMP nor provisioned
// by it. Putting it there would have made every reader of that package's kind
// list believe meshcraft writes this account onto devices, which it does not.
const CredKindGNMI = "gnmi"

// Transport-security modes, as stored in `site_credentials.transport_tls`
// (migration 0039). Three values, so that the meaningless combination
// "plaintext and skip-verify" cannot be expressed.
const (
	// TLSVerify: TLS with the certificate checked. The default, and the only mode
	// that resists someone on the management path.
	TLSVerify = "verify"
	// TLSSkipVerify: TLS with the certificate NOT checked. Exists because these
	// switches ship self-signed certificates and a site with no CA has nothing to
	// check against. Weaker than TLSVerify and much stronger than TLSPlaintext: an
	// unverified session still defeats a passive listener.
	TLSSkipVerify = "skip-verify"
	// TLSPlaintext: no TLS. The device login travels in clear in gRPC metadata.
	TLSPlaintext = "plaintext"
)

// gnmiCredential is what a subscription needs, resolved and decrypted.
type gnmiCredential struct {
	Username string
	Password string
	// Plaintext / SkipVerify / CAPEM: the transport posture, already translated
	// out of the stored mode string so that no caller has to know the spelling.
	Plaintext  bool
	SkipVerify bool
	CAPEM      string
}

// gnmiCredentialFor resolves the `gnmi` credential that applies to a device.
//
// Goes through CredentialsForDevice, which is the TAG-AWARE resolution: it
// returns the site's credentials of this kind ordered most-specific first (tag,
// then group, then site default), which is what lets one site run a shared
// telemetry account while a family of switches carries its own.
//
// The FIRST usable one wins, and "usable" means it has both a username and a
// readable secret. That check is not a formality: a credential row can exist with
// no secret — the vault is a separate store and a creation can half-fail — and a
// caller handed a username with an empty password would subscribe anonymously,
// which EOS rejects per attempt while the proxy's supervisor retries. Skipping to
// the next candidate is what stops a half-created credential from shadowing a
// working one.
func (s *Server) gnmiCredentialFor(ctx context.Context, d model.Device,
	groupName string) (gnmiCredential, error) {

	var out gnmiCredential
	if s.Secrets == nil {
		// No vault: there is nothing to decrypt a password with. Returning an
		// empty credential rather than an error keeps this on the same footing as
		// a site that has not created one — the device is not subscribed, and
		// nothing pretends otherwise.
		return out, nil
	}
	var creds, err = s.Store.CredentialsForDevice(ctx, d.ID, d.SiteID,
		groupName, CredKindGNMI)
	if err != nil {
		return out, err
	}
	var c model.SiteCredential
	for _, c = range creds {
		if c.Username == "" || !c.HasSecret {
			continue
		}
		var _, enc, _, revErr = s.Store.RevealSiteCredential(ctx, c.ID)
		if revErr != nil {
			continue
		}
		var secret string
		if secret, err = s.Secrets.Open(enc); err != nil || secret == "" {
			continue
		}
		out.Username = c.Username
		out.Password = secret
		out.Plaintext, out.SkipVerify = tlsPosture(c.TransportTLS)
		out.CAPEM = c.CAPEM
		return out, nil
	}
	return out, nil
}

// tlsPosture translates the stored mode into the two booleans the wire carries.
//
// AN UNKNOWN MODE FALLS TO VERIFY, and that direction is the whole reason this is
// a function rather than two comparisons at the call site. The failure modes are
// not symmetric: falling to `verify` on a value nobody recognises produces a
// subscription that refuses to connect — loud, immediate, and fixed by correcting
// the field. Falling to `plaintext` would produce a subscription that WORKS, with
// an admin password on the wire and nothing on any screen to say so.
//
// The database has a CHECK constraint on this column, so an unknown value should
// be unreachable. It is handled anyway, because "should be unreachable" is not a
// property this file gets to rely on when the cost of being wrong is a leaked
// device login.
func tlsPosture(mode string) (plaintext, skipVerify bool) {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case TLSPlaintext:
		return true, false
	case TLSSkipVerify:
		return false, true
	}
	return false, false
}
