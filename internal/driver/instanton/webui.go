package instanton

import "meshcraft/internal/driver"

// The 1930's administration UI, for a caller that needs its ADDRESS without
// opening a session.
//
// `https://` on 443, and that is not a preference: this range redirects 80 to
// TLS, and its provisioning surface answers on 4443 (cf. api.go). The certificate
// is self-signed — the norm on network hardware, not an anomaly — so whoever
// dials this must not verify it; the confidentiality of the link is carried by
// the tunnel.
//
// [driver.WebAuthForm] and this is the honest answer, not a gap waiting to be
// filled: the login is an RSA-encrypted GET whose key comes from a separate
// endpoint, and the session it opens lives in a cookie AND in the `/csNNNNNNNN/`
// path prefix that every later URL carries. A proxy cannot pre-authenticate that
// without re-implementing the vendor's login a second time, in a place where it
// would break silently at the next firmware. The device grants FIVE sessions
// total, so guessing wrong costs the operator their access.
func (d *Driver1930) WebUIBase(host string) string { return "https://" + host }

func (d *Driver1930) WebUIAuth() driver.WebAuth { return driver.WebAuthForm }
