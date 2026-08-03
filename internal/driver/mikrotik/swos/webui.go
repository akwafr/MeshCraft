package swos

import "meshcraft/internal/driver"

// SwOS's administration UI.
//
// `http://` on 80, and SwOS listens ONLY there: pointing a browser at https://
// on one of these gets a refused connection, not a redirect.
//
// [driver.WebAuthDigest], which is the interesting case for a relay. SwOS has NO
// SESSION AT ALL — the first fact stated at the top of admin.go — and
// re-presents `Authorization: Digest` on every single request. That is exactly
// what makes a pre-authenticated reverse proxy possible here: there is no login
// to reproduce, only a header to compute, and the operator never sees a password
// prompt because there is no prompt to see.
func (d *Driver) WebUIBase(host string) string { return "http://" + host }

func (d *Driver) WebUIAuth() driver.WebAuth { return driver.WebAuthDigest }
