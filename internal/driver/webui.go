package driver

// WEB INTERFACE ADDRESSING: where a platform's own administration UI lives, and
// how it authenticates.
//
// # Why this is an optional interface and not a field on Info
//
// Info describes what a connector IS. This describes where its platform's web
// interface answers, which is a different kind of fact: three of the five shipped
// ranges have one, at three different scheme/port combinations that nothing about
// the vendor name predicts (`https://` on 443 for Instant On, `http://` on 80 for
// SwOS which listens ONLY there, `http://` for RouterOS whose 443 is closed for
// lack of a factory certificate). Two of the five have no web interface worth
// opening at all.
//
// The rule from connector.go applies unchanged: a capability a platform can
// structurally NOT have belongs in a separate interface tested by assertion, so
// the caller refuses BEFORE it starts and can say why. A `WebUIBase` field on
// Info would have every connector answering the question, including the ones for
// which the honest answer is "there is nothing here to open".
//
// # This duplicates knowledge the connectors already had, and that is the point
//
// `instanton.OpenAdmin` builds "https://"+host, `swos.OpenAdmin` builds
// "http://"+host — buried in a constructor, reachable only by opening a session.
// A reverse proxy needs the address WITHOUT authenticating first, and the API
// layer must not be the place that decides which scheme a vendor speaks: that is
// precisely the fault the connector contract exists to make impossible.

// WebAuth names how a web interface expects credentials.
//
// The distinction that matters to a relay is not the vendor but whether
// credentials can be presented on EVERY REQUEST at the HTTP layer. When they can
// ([WebAuthBasic], [WebAuthDigest]), a proxy in front of the device can inject
// them and the operator never sees a login page or a password. When they cannot
// ([WebAuthForm]), no amount of proxying will log anyone in: the session lives in
// a cookie the device issues in exchange for a form post whose shape is
// vendor-specific and, on at least one range, RSA-encrypted.
//
// Saying which of the two applies is what lets a screen offer the right thing —
// a UI that is simply THERE, versus a UI plus the credential to paste into it —
// instead of promising a seamless login it cannot deliver.
type WebAuth string

const (
	// WebAuthNone: the interface serves its pages without asking.
	WebAuthNone WebAuth = ""
	// WebAuthBasic: `Authorization: Basic` on every request. RouterOS's REST API.
	WebAuthBasic WebAuth = "basic"
	// WebAuthDigest: `Authorization: Digest`, re-presented on every request. SwOS,
	// which has no session at all.
	WebAuthDigest WebAuth = "digest"
	// WebAuthForm: a login FORM that sets a cookie. Instant On.
	//
	// PRE-AUTHENTICATION IS IMPOSSIBLE for this mode and the caller must not
	// pretend otherwise. On the 1930 the form is an RSA-encrypted GET whose key
	// is fetched from a separate endpoint, and the session it opens lives in both
	// a cookie AND a `/csNNNNNNNN/` path prefix that every later URL carries.
	// Reproducing that inside a proxy would mean re-implementing the vendor's
	// login in a second place, where it would break silently the first time the
	// firmware changed — and the device only grants FIVE sessions, so getting it
	// wrong costs the operator their access.
	WebAuthForm WebAuth = "form"
)

// WebUIAddresser: the platform has a web administration interface, and the
// connector knows where it answers.
//
// On the Driver and not the Admin: this is a PURE function of the host, needs no
// session, and is most useful precisely when no session exists — a screen has to
// decide whether to draw the button before anything is opened.
type WebUIAddresser interface {
	Driver
	// WebUIBase returns the origin of the administration UI ("https://10.99.0.5"),
	// with no trailing slash and no path.
	WebUIBase(host string) string
	// WebUIAuth says how that interface authenticates, so a caller can tell a
	// seamless login from one where the operator still has to type a password.
	WebUIAuth() WebAuth
}

// AsWebUIAddresser returns the driver as a web-UI addresser when it is one.
func AsWebUIAddresser(d Driver) (WebUIAddresser, bool) {
	var a, ok = d.(WebUIAddresser)
	return a, ok
}
