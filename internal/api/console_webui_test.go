package api

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The rewriting web-UI proxy these tests used to cover is gone (cf.
// internal/socks for why). What survives is the part that still decides what the
// console panel draws: whether a platform speaks a transport, and whether a
// connector claiming a web interface can say WHERE it answers — because the
// browser now goes to that address directly, and a connector that cannot name it
// leaves the operator with a button that leads nowhere.

func TestTransportHas(t *testing.T) {
	if !transportHas([]string{"web", "snmp"}, "web") {
		t.Error("web not found")
	}
	if !transportHas([]string{"API", " SSH "}, "ssh") {
		t.Error("transport matching must tolerate case and spacing")
	}
	if transportHas([]string{"web"}, "ssh") {
		t.Error("ssh reported on a web-only platform")
	}
	if transportHas(nil, "ssh") {
		t.Error("an undeclared transport list must not report a capability")
	}
}

// The rule from the connector contract: a capability a platform structurally
// does not have is DECLARED absent, not discovered by trying. Every connector
// that claims a web transport must therefore be able to say where that interface
// answers, or the console panel will offer a link it cannot build.
func TestWebTransportConnectorsDeclareTheirWebUI(t *testing.T) {
	var name string
	for _, name = range driver.Names() {
		var d, ok = driver.Get(name)
		if !ok {
			continue
		}
		if !transportHas(d.Info().Transport, "web") {
			continue
		}
		var addr, isAddresser = driver.AsWebUIAddresser(d)
		if !isAddresser {
			t.Errorf("connector %q declares a web transport but no WebUIAddresser: "+
				"the console screen cannot know where to point", name)
			continue
		}
		var base = addr.WebUIBase("10.0.0.1")
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			t.Errorf("connector %q WebUIBase = %q, want an absolute origin", name, base)
		}
		if strings.HasSuffix(base, "/") {
			t.Errorf("connector %q WebUIBase = %q, want no trailing slash", name, base)
		}
		// The auth mode is what tells the panel whether to promise a seamless
		// login or offer the credential for pasting. An unknown value would make
		// that choice silently.
		switch addr.WebUIAuth() {
		case driver.WebAuthNone, driver.WebAuthBasic, driver.WebAuthDigest, driver.WebAuthForm:
		default:
			t.Errorf("connector %q declares an unknown web auth %q", name, addr.WebUIAuth())
		}
	}
}
