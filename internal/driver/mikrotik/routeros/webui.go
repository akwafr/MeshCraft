package routeros

import "meshcraft/internal/driver"

// Webfig, RouterOS's web interface.
//
// `http://` because 443 is closed on a factory unit for lack of a certificate —
// the same reason the REST API is reached over plain HTTP through the tunnel.
//
// [driver.WebAuthForm] even though the REST API next to it is Basic, and the
// distinction is deliberate: this value describes what a BROWSER meets, and a
// browser pointed at a RouterOS lands on Webfig, which logs in with its own form
// and its own JavaScript. Answering `basic` here would promise a seamless login
// that the operator would then not get.
//
// In practice this matters little for this range: RouterOS declares `ssh` in its
// transports, so a console opens a real shell and the web interface is the
// secondary path rather than the only one.
func (d *Driver) WebUIBase(host string) string { return "http://" + host }

func (d *Driver) WebUIAuth() driver.WebAuth { return driver.WebAuthForm }
