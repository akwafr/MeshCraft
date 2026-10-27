package instanton

// LIVE SYSTEM-NAME WRITE — the one thing hostname.go says is missing.
//
// The READ was settled 2026-08-10 (`SystemGlobalSetting` / `systemName`, the lab
// unit answering with its own SERIAL). What had never been sent is the WRITE, and
// that is the whole of what keeps this range's entry in
// api.hostnameWritableConnectors at `false`.
//
// This harness drives [Admin.SetHostname] itself — probe, write, re-read,
// save — rather than reimplementing the sequence, so what it proves is the code
// the healer will run and not a lookalike. It writes the name it is given and
// then, unless told otherwise, PUTS THE OLD ONE BACK, because a harness that
// leaves a switch called something else is not a measurement, it is a change.
//
//	INSTANTON_LIVE_HOST=198.51.100.101 INSTANTON_LIVE_USER=… INSTANTON_LIVE_PASS=… \
//	INSTANTON_LIVE_SOCKS=127.0.0.1:1080 INSTANTON_LIVE_SOCKS_USER=… INSTANTON_LIVE_SOCKS_PASS=… \
//	INSTANTON_HOSTNAME=mctest-1930 \
//	go test ./internal/driver/instanton -run TestLiveSetSystemName -v
//
// Set INSTANTON_HOSTNAME_KEEP=1 to skip the revert — for the one case where the
// new name is the intended end state.

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveSetSystemName(t *testing.T) {
	if os.Getenv("INSTANTON_LIVE_HOST") == "" {
		t.Skip("INSTANTON_LIVE_HOST not set")
	}
	var want = os.Getenv("INSTANTON_HOSTNAME")
	if want == "" {
		t.Skip("INSTANTON_HOSTNAME not set")
	}

	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var a = liveSnoopAdmin(ctx, t, "system-name write (hardware proof)")

	var loc = a.locateSystemName(ctx)
	if !loc.found() {
		t.Fatalf("no system-name section answered (tried %v)", loc.tried)
	}
	t.Logf("BEFORE: section %q field %q value %q (tried %v)",
		loc.section, loc.field, loc.value, loc.tried)
	var before = loc.value

	var err = a.SetHostname(ctx, want)
	if err != nil {
		t.Fatalf("SetHostname(%q): %v", want, err)
	}

	// INDEPENDENT of the write's own re-read: a fresh section read.
	var body string
	body, err = a.RawSection(ctx, loc.section)
	if err != nil {
		t.Fatalf("independent re-read: %v", err)
	}
	t.Logf("AFTER: %s = %q", loc.field, xmlField(body, loc.field))

	var s Step
	for _, s = range a.Steps {
		t.Logf("step ok=%v %s — %s", s.OK, s.What, s.Detail)
	}

	if os.Getenv("INSTANTON_HOSTNAME_KEEP") != "" {
		t.Logf("KEEP set — %q left in place", want)
		return
	}
	err = a.SetHostname(ctx, before)
	if err != nil {
		t.Fatalf("REVERT to %q FAILED — the device is still called %q: %v",
			before, want, err)
	}
	body, err = a.RawSection(ctx, loc.section)
	if err != nil {
		t.Fatalf("revert re-read: %v", err)
	}
	t.Logf("REVERTED: %s = %q", loc.field, xmlField(body, loc.field))
}
