package procurve

import "meshcraft/internal/driver"

// The ProCurve web interface, for a caller that needs its ADDRESS without
// opening a session.
//
// `http://` on 80, OBSERVED on the reference unit (2026-08-02): port 80 answers
// with `Server: eHTTP v2.0` and serves the management page. Port 443 was not
// open on that unit. This range can be given a certificate and made to listen on
// TLS, but a connector must describe what the hardware does rather than what it
// could be configured to do — and pointing a browser at https:// on a switch that
// does not listen there produces a refused connection, not a redirect.
//
// [driver.WebAuthForm], and this is the honest answer rather than a gap. The
// probe recorded `web_auth = no-http-auth`: this interface issues NO HTTP
// authentication challenge, it posts a login FORM. Nothing can pre-authenticate
// it by injecting an Authorization header, so the console panel offers the
// credential for pasting instead of promising a seamless login it cannot deliver.
func (d *Driver) WebUIBase(host string) string { return "http://" + host }

func (d *Driver) WebUIAuth() driver.WebAuth { return driver.WebAuthForm }
