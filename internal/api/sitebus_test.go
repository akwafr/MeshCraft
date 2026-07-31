package api

import (
	"strings"
	"testing"
)

// What these tests protect: coalescing, per-site scope, no echo to the writer,
// and subscriber cap. These four properties underpin real-time load — none is
// visible on a working UI.

// A burst must produce ONE wake and ONE batch of categories.
func TestSiteBusCoalesce(t *testing.T) {
	var bus = newSiteBus(8)
	var sub, stop, ok = bus.subscribe("site-1", "")
	if !ok {
		t.Fatal("subscription refused")
	}
	defer stop()

	// Fifty ports moving together: the case that motivated the feature.
	var i int
	for i = 0; i < 50; i++ {
		bus.notify("site-1", ChangePort, "")
	}
	bus.notify("site-1", ChangeDevice, "")

	// One wake token, regardless of notification count.
	select {
	case <-sub.wake:
	default:
		t.Fatal("no wake")
	}
	select {
	case <-sub.wake:
		t.Fatal("second wake: coalescing not working")
	default:
	}

	var kinds = strings.Join(sub.drain(), ",")
	if kinds != ChangeDevice+","+ChangePort {
		t.Fatalf("unexpected categories: %q", kinds)
	}
	// Drained: next pass must not re-announce.
	if len(sub.drain()) != 0 {
		t.Fatal("accumulator not drained")
	}
}

// A stream only sees its site: stream scope equals permission scope.
func TestSiteBusScopePerSite(t *testing.T) {
	var bus = newSiteBus(8)
	var sub, stop, _ = bus.subscribe("site-1", "")
	defer stop()

	bus.notify("site-2", ChangePort, "")
	select {
	case <-sub.wake:
		t.Fatal("change from another site woke the subscriber")
	default:
	}
	if len(sub.drain()) != 0 {
		t.Fatal("category accumulated for another site")
	}
}

// The writer of a change does not receive its echo; neighbors do.
func TestSiteBusNoEchoToWriter(t *testing.T) {
	var bus = newSiteBus(8)
	var writer, stopA, _ = bus.subscribe("site-1", "tab-A")
	defer stopA()
	var neighbor, stopB, _ = bus.subscribe("site-1", "tab-B")
	defer stopB()

	bus.notify("site-1", ChangeConfig, "tab-A")

	select {
	case <-writer.wake:
		t.Fatal("writer received echo of its own write")
	default:
	}
	select {
	case <-neighbor.wake:
	default:
		t.Fatal("neighbor was not woken")
	}

	// Empty origin = nobody excluded: monitoring and automatic apply have no human author.
	bus.notify("site-1", ChangeDevice, "")
	select {
	case <-writer.wake:
	default:
		t.Fatal("change without author must reach everyone")
	}
}

// Beyond the cap, refuse plainly: the client falls back to polling — degraded but honest.
func TestSiteBusCapsSubscribers(t *testing.T) {
	var bus = newSiteBus(2)
	var _, stop1, ok1 = bus.subscribe("site-1", "")
	var _, stop2, ok2 = bus.subscribe("site-1", "")
	var _, _, ok3 = bus.subscribe("site-1", "")
	if !ok1 || !ok2 {
		t.Fatal("first two subscriptions should succeed")
	}
	if ok3 {
		t.Fatal("third subscription should be refused")
	}
	if bus.count() != 2 {
		t.Fatalf("unexpected count: %d", bus.count())
	}
	// Unsubscribe frees a slot.
	stop1()
	var _, stop4, ok4 = bus.subscribe("site-1", "")
	if !ok4 {
		t.Fatal("freed slot was not returned")
	}
	stop2()
	stop4()
	if bus.count() != 0 {
		t.Fatalf("residual subscribers: %d", bus.count())
	}
}

// A nil bus does not panic: NotifySiteChanged must be callable from anywhere,
// including before the router is mounted.
func TestSiteBusNilTolerant(t *testing.T) {
	var s = &Server{}
	s.NotifySiteChanged("site-1", ChangePort)
	var _, _, ok = s.live.subscribe("site-1", "")
	if ok {
		t.Fatal("nil bus must accept no subscribers")
	}
}

// Site must appear in the URL without a database for routes that carry it,
// and stay empty for those that do not.
func TestSiteOfPathSansBase(t *testing.T) {
	var s = &Server{}
	var cas = map[string]string{
		"/api/sites/abc/devices":         "abc",
		"/api/sites/abc/devices/x/ports": "abc",
		"/api/users/42":                  "",
		"/api/proxies/7/revoke":          "",
		"/api/auth/logout":               "",
		"/healthz":                       "",
	}
	var path, want string
	for path, want = range cas {
		var got = s.siteOfPath(t.Context(), path)
		if got != want {
			t.Errorf("%s: site %q, want %q", path, got, want)
		}
	}
}

func TestKindOfPath(t *testing.T) {
	var cas = map[string]string{
		"/api/devices/x/ports/GE1":          ChangePort,
		"/api/devices/x/ports":              ChangePort,
		"/api/devices/x/read-state":         ChangeSync,
		"/api/devices/x/config/apply":       ChangeSync,
		"/api/devices/x/snmp-apply":         ChangeSync,
		"/api/links/x":                      ChangeLink,
		"/api/devices/x":                    ChangeDevice,
		"/api/discovery/candidates/x/adopt": ChangeDevice,
		"/api/sites/abc/vlans":              ChangeConfig,
		"/api/sites/abc/profiles/port":      ChangeConfig,
	}
	var path, want string
	for path, want = range cas {
		var got = kindOfPath(path)
		if got != want {
			t.Errorf("%s: category %q, want %q", path, got, want)
		}
	}
}
