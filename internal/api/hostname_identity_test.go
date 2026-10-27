package api

// The SECOND observation source of the hostname reconciliation, pinned END TO END.
//
// # What was unpinned, and why that mattered
//
// `extra.identity.hostname` is the fallback source hostnameObserved reads when a
// device's LLDP says nothing (cf. hostnameheal.go). It is produced two files away:
// identityFacts turns a driver.Identity into the flat map store.SetDeviceIdentity
// writes under `extra.identity`, and hostnameObserved reads one key out of it by
// literal name. NOTHING checked that the two agree. hostnameheal_test.go's
// observation test hand-writes `{"identity":{"hostname":"old-name"}}`, so renaming
// identityFacts' key — or dropping the `put("hostname", …)` line, which was only
// added on 2026-08-06 — would break the fallback in silence: every test in this
// package would still pass, and the fleet's LLDP-silent devices would simply go
// back to reading `not_observed` for ever.
//
// This file walks the whole chain on pure functions: driver.Identity →
// identityFacts → the JSON shape the store persists → storedIdentityFacts →
// hostnameObserved → hostnameConformityOf.
//
// # It pins the GAP as much as the path
//
// The path works. What does not is that not much FEEDS it — as of 2026-08-10, of
// the nine connectors, `mikrotik-routeros`, `fs-s3400` and `fs-s3900` set
// driver.Identity.Hostname on every read-state; hpe-procurve,
// aruba-instant-on-1930 and mikrotik-swos set it only on the [driver.Identifier]
// fallback, which observeIdentity reaches at most once per device (when
// `devices.model` is empty); and cisco-ios, arista-eos and ubiquiti-edgeswitch
// never set it at all. TestIdentityWithoutAHostnameLeavesTheDeviceUnobserved is
// that last shape, pinned so the consequence — `not_observed`, and therefore NO
// repair, since hostnameRepairable requires `drift` — is stated in a test and not
// only in prose.
//
// THE TWO FS RANGES WERE IN THAT LAST GROUP UNTIL 2026-08-10 and they were its
// worst case, because neither has an LLDP system name either: no source could
// report, so they were permanently `not_observed` and a name meshcraft had
// written could never be verified, only rewritten at the next rename. What they
// gained is a READ, not a write — and it is not free, so it does not live on the
// identity path: their name is in the running configuration alone (a
// ninety-second paged read on the S3400), harvested by ReadState and replayed
// from memory by ReadIdentity. TestFSRangesNowFeedTheSecondSourceFromReadState
// and TestTheSecondSourceIsFedBeforeIdentityIsObserved hold the two halves of
// that claim on this side of the boundary; each connector's identityread_test.go
// holds the other.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// deviceCarryingIdentity builds the device row as it exists AFTER
// store.SetDeviceIdentity has run: the facts map, marshalled, under
// `extra.identity`. Going through identityFacts rather than writing the JSON by
// hand is the entire point of this file — that is the coupling being pinned.
func deviceCarryingIdentity(t *testing.T, name, connector string,
	id driver.Identity) model.Device {

	t.Helper()
	var facts, err = json.Marshal(identityFacts(id))
	if err != nil {
		t.Fatalf("identity facts: %v", err)
	}
	var extra, mErr = json.Marshal(map[string]json.RawMessage{
		"driver":   json.RawMessage(`"` + connector + `"`),
		"identity": facts,
	})
	if mErr != nil {
		t.Fatalf("extra: %v", mErr)
	}
	return model.Device{
		ID: "d-" + name, SiteID: "s1", Name: name, MgmtIP: "192.0.2.2", Extra: extra,
	}
}

// A device whose connector reads its own name — the RouterOS shape — converges
// with NO LLDP at all. This is the whole promise of the second source.
func TestIdentityHostnameIsTheSecondObservationSource(t *testing.T) {
	var d = deviceCarryingIdentity(t, "sw-lab-01", "mikrotik-routeros",
		driver.Identity{
			Model: "CRS326-24G-2S+", Serial: "9458", Hostname: "sw-lab-01",
			Source: "rest_api",
		})

	// No LLDP row: `announced` false, which is the case this source exists for.
	var observed, source = hostnameObserved(d, model.LLDPSelf{}, false)
	if observed != "sw-lab-01" {
		t.Fatalf("observed = %q, want the name identityFacts persisted — the key "+
			"identityFacts writes and the key hostnameObserved reads have drifted "+
			"apart, and the fallback source is dead", observed)
	}
	if source != hostnameSourceReadState {
		t.Errorf("source = %q, want %q: the UI shows WHERE a verdict came from, and "+
			"this one is as old as the last read-state, not fresh like LLDP",
			source, hostnameSourceReadState)
	}
	var v = hostnameConformityOf(d, observed, source)
	if v.State != hostnameOK {
		t.Fatalf("state = %q (%s), want ok: a device answering to the inventory's "+
			"name through the only source it has must not be repaired", v.State, v.Because)
	}
}

// An identity WITHOUT a hostname leaves the device unobserved — and unobserved is
// not drift, so the healer pushes nothing. That is the state three of the nine
// ranges are in today (cisco-ios, arista-eos, ubiquiti-edgeswitch), where LLDP is
// the only thing judging them.
//
// It is ALSO the state an fs-s3400 or fs-s3900 is in on an access where read-state
// has not run — the rename path — so this shape has not become historical, it has
// become conditional. That is exactly why it stays pinned.
func TestIdentityWithoutAHostnameLeavesTheDeviceUnobserved(t *testing.T) {
	// The literal cisco-ios / arista-eos ReadIdentity shape: everything about the
	// device except its name.
	var d = deviceCarryingIdentity(t, "sw-lab-01", "cisco-ios", driver.Identity{
		Model: "WS-C2960X-24TS-L", Serial: "FOC1841X0PQ", MAC: "64:9d:99:22:7b:95",
		Firmware: "15.2(4)E10", Source: "ssh_cli",
	})
	var observed, source = hostnameObserved(d, model.LLDPSelf{}, false)
	if observed != "" || source != "" {
		t.Fatalf("observed %q from %q: an identity with no Hostname is not an "+
			"observation of the device's name", observed, source)
	}
	var v = hostnameConformityOf(d, observed, source)
	if v.State != hostnameNotObserved {
		t.Fatalf("state = %q, want %q", v.State, hostnameNotObserved)
	}
	// The consequence, stated where it can be checked: nothing is repaired on an
	// absent reading, so the healer never opens a session on this device.
	if hostnameRepairableFor(d, v) {
		t.Error("an unobserved device must not be repairable: a push on an absent " +
			"reading is the write-forever loop hostnameheal.go exists to refuse")
	}
}

// A BLANK LLDP system name falls through to the identity rather than counting as
// an observation. That is the FS shape (its agent serves no LLDP-MIB at all, and
// the CLI path deliberately writes no `sys_name`), and it must not read as "this
// device announces the empty string".
//
// THE CONNECTOR HERE USED TO BE hpe-procurve and had to change on 2026-08-10: that
// range's identity is frozen at adoption, so hostnameObserved now declines to
// report it at all (TestAFrozenIdentityHostnameIsNotAnObservation) and this test
// would have been pinning the fall-through with the one shape where the second
// source is refused. fs-s3400 is the honest carrier for the rule — refreshable, and
// the range the blank-`sys_name` case was described from in the first place.
func TestBlankLLDPSysNameFallsThroughToTheIdentity(t *testing.T) {
	var d = deviceCarryingIdentity(t, "sw-lab-01", "fs-s3400",
		driver.Identity{Model: "S3400-48T4SP", Hostname: "SW-PLAYER-79", Source: "ssh_cli"})

	// The row EXISTS — the poller wrote it — and carries no name.
	var blank = model.LLDPSelf{DeviceID: d.ID, SysName: "", Served: true}
	var observed, source = hostnameObserved(d, blank, true)
	if observed != "SW-PLAYER-79" || source != hostnameSourceReadState {
		t.Fatalf("observed %q from %q: a served LLDP row with no system name has "+
			"told us nothing about the name, and must not shadow the second source",
			observed, source)
	}
	// And it reads as DRIFT against the inventory, which is the point — the device
	// answers to something else, and meshcraft owns the name.
	var v = hostnameConformityOf(d, observed, source)
	if v.State != hostnameDrift {
		t.Fatalf("state = %q (%s), want drift", v.State, v.Because)
	}
}

// THE DEFECT, PINNED: a stored name that can never be re-read must not be handed
// back as an observation of the device now.
//
// hpe-procurve and mikrotik-swos both fill driver.Identity.Hostname, and both do it
// only through observeIdentity's [driver.Identifier] fallback — which runs while
// `devices.model` is empty, i.e. once, at adoption, and never again. The stored
// value is therefore frozen for the life of the row, and before 2026-08-10
// hostnameObserved returned it tagged `read_state`, indistinguishable from a
// reading taken this minute. Two consequences, and this test locks both away:
//
//   - a device renamed at its console reported `ok` — AGREEMENT with a name it no
//     longer answers to. That is the false present tense, and it is the reason the
//     conformity axis must be able to say "cannot tell";
//   - a device whose frozen name disagreed reported `drift`, which promises a
//     repair. The healer pushed, and hostnameConverged re-read the same unmovable
//     fact and never saw convergence — so the push came back on every cooldown,
//     for ever, on two of the smallest session budgets in the fleet.
//
// `not_observed` is the right verdict for both, and it is deliberately the SAME
// word a device no source has ever reported gets: from the axis's point of view
// they are one state — nothing is known about the present, so nothing is repaired.
// What separates them is the sentence, checked below.
func TestAFrozenIdentityHostnameIsNotAnObservation(t *testing.T) {
	var frozen = []struct{ connector, model, stored string }{
		// The "System Name" row of `show system-information`, read once.
		{"hpe-procurve", "J9085A", "SW-PLAYER-79"},
		// `/sys.b`'s `id`, read once — and SwOS grants the fewest sessions of any
		// range here, which is why re-reading it on a cadence was refused.
		{"mikrotik-swos", "CSS326-24G-2S+", "lan-m6"},
	}
	var c struct{ connector, model, stored string }
	for _, c = range frozen {
		var d = deviceCarryingIdentity(t, "sw-lab-01", c.connector, driver.Identity{
			Model: c.model, Hostname: c.stored, Source: "ssh_cli",
		})
		// The fact IS stored — this is not a test about an empty key.
		if storedIdentityFacts(d.Extra)["hostname"] != c.stored {
			t.Fatalf("%s: the fixture stored no name, so this test proves nothing",
				c.connector)
		}
		var observed, source = hostnameObserved(d, model.LLDPSelf{}, false)
		if observed != "" || source != "" {
			t.Fatalf("%s: observed %q from %q — a name read once at adoption was "+
				"reported as what the device announces now", c.connector, observed, source)
		}
		var v = hostnameConformityOf(d, observed, source)
		if v.State != hostnameNotObserved {
			t.Fatalf("%s: state = %q (%s), want %q", c.connector, v.State, v.Because,
				hostnameNotObserved)
		}
		if hostnameRepairableFor(d, v) {
			t.Errorf("%s: repairing on a frozen reading is the push-for-ever loop — "+
				"hostnameConverged re-reads the same unmovable fact", c.connector)
		}
		// The sentence must say WHY it cannot tell, and name the stale reading so an
		// operator can compare it against the console themselves. "No source has
		// ever reported" would send them hunting a fault on the equipment.
		if !strings.Contains(v.Because, c.stored) ||
			!strings.Contains(v.Because, "adoption") {
			t.Errorf("%s: the sentence must name the stale reading and say it was "+
				"taken at adoption, got %q", c.connector, v.Because)
		}
	}

	// THE CASE THAT MUST NOT MOVE, side by side with the one that did: the SAME
	// stored fact on a refreshable range is still an observation. mikrotik-routeros
	// carries its name inside driver.IdentityReader, so every read-state re-takes it
	// — nothing about that fact is frozen, and treating it as stale would throw away
	// the only source a device with no LLDP has.
	var live = deviceCarryingIdentity(t, "sw-lab-01", "mikrotik-routeros",
		driver.Identity{Model: "CRS326-24G-2S+", Hostname: "lan-m6", Source: "rest_api"})
	var observed, source = hostnameObserved(live, model.LLDPSelf{}, false)
	if observed != "lan-m6" || source != hostnameSourceReadState {
		t.Fatalf("observed %q from %q on a refreshable range: the fix must cost the "+
			"cheap-path connectors nothing — this fact is re-taken on every read-state",
			observed, source)
	}
	if hostnameConformityOf(live, observed, source).State != hostnameDrift {
		t.Error("a RouterOS answering to another name must still be drift and still " +
			"be repaired: it is observed, and the repair converges")
	}
}

// TestTheTwoPathsToTheSecondSourceStayApart pins WHICH PATH each connector is on,
// because the difference is the whole content of hostnameObserved's "thinly fed"
// paragraph and nothing checked it.
//
// A connector that fills driver.Identity.Hostname feeds `extra.identity.hostname`
// CONTINUOUSLY if it is a [driver.IdentityReader] — refreshIdentityAfterHostname
// and identity_selfheal both take that cheap path on every read-state — and ONCE
// PER DEVICE if it is not, because the only other way in is observeIdentity's
// [driver.Identifier] fallback, which runs solely while `devices.model` is empty
// (identity_selfheal.go:119). Same field, same key, two completely different
// freshness guarantees: on the once-only three a console rename is invisible here
// for ever.
//
// So the reader set is the load-bearing half, and it is checked the way the
// capability table is — type assertions on the typed nils of hostnameAdminSamples,
// no method ever called. Adding ReadIdentity to hpe-procurve, mikrotik-swos or
// aruba-instant-on-1930 would promote those three from once-only to continuous and
// make that paragraph wrong; dropping it from mikrotik-routeros, fs-s3400 or
// fs-s3900 would take the second source away from a range that has no other.
// Either way this fails first.
//
// SINCE 2026-08-10 THE SET IS NO LONGER LOCAL TO THIS TEST. It moved into
// identityRefreshingConnectors, where hostnameObserved consults it to decide
// whether a stored name may be reported in the present tense, and this test became
// its build-time detector rather than a second copy of it. So a wrong entry now
// costs more than a stale paragraph: declared true where the connector cannot
// re-read hands a frozen name back as a current observation, which is the defect
// the table was extracted to fix; declared false where it can throws away a live
// source and returns that range to `not_observed`.
//
// # fs-s3400 CROSSED ON 2026-08-10, deliberately
//
// It is the only connector that has ever moved between these two paths, and the
// move is the whole point of the change: an S3400 has no LLDP system name, so
// `extra.identity.hostname` was its only possible observation source, and the
// [driver.Identifier] fallback fed that source at most ONCE per device — while
// `devices.model` was empty — at the price of a second SSH session. meshcraft
// wrote a name to those switches at every rename and could never verify it.
// Gaining ReadIdentity moves it to the continuous path AND removes that second
// session, since `show version` at login already holds model, serial, MAC and
// firmware.
//
// What it does NOT do is put an expensive read on the cheap path: on both FS
// ranges the system name lives in the running configuration alone (ninety seconds
// of paged output on the S3400), so ReadState harvests it from the read it
// already performs and ReadIdentity replays it from memory. This list says which
// PATH a connector is on; it cannot say whether the connector actually fills the
// field, which is why the FS half is pinned by
// TestFSRangesNowFeedTheSecondSourceFromReadState here and by each package's
// identityread_test.go there.
func TestTheTwoPathsToTheSecondSourceStayApart(t *testing.T) {
	var name string
	var sample driver.Admin
	for name, sample = range hostnameAdminSamples {
		var _, cheap = driver.AsIdentityReader(sample)
		var declared, listed = identityRefreshingConnectors[name]
		if !listed {
			t.Errorf("connector %q is registered and absent from "+
				"identityRefreshingConnectors: classify it (true if its access "+
				"implements driver.IdentityReader, false otherwise) or its devices' "+
				"stored identity will be treated as un-refreshable, i.e. its name "+
				"will never be judged", name)
			continue
		}
		if cheap == declared {
			continue
		}
		if cheap {
			t.Errorf("connector %q gained driver.IdentityReader: if it also fills "+
				"Identity.Hostname it now feeds `extra.identity.hostname` on EVERY "+
				"read-state instead of once at adoption. Set it true in "+
				"identityRefreshingConnectors — until then hostnameObserved throws its "+
				"name away as un-refreshable — and rewrite hostnameObserved's account "+
				"of the second source, not just the table", name)
			continue
		}
		t.Errorf("connector %q lost driver.IdentityReader: it now reaches "+
			"`extra.identity.hostname` only through observeIdentity's "+
			"driver.Identifier fallback, i.e. at most once per device while "+
			"`devices.model` is empty. Set it false in identityRefreshingConnectors "+
			"— left true, its devices' names are frozen at adoption and reported as "+
			"if read this minute. On mikrotik-routeros, fs-s3400 or fs-s3900 it also "+
			"takes the second source away from a range whose LLDP says nothing, "+
			"i.e. back to `not_observed` for ever", name)
	}
}

// The FS shape, END TO END on this side of the boundary: what those connectors
// now return from a read-state is what makes the device judgeable.
//
// It is a separate test from the path list above because the two facts are
// independent and both are needed — being on the cheap path says WHEN the field
// travels, filling it says WHETHER there is anything to travel. The connectors'
// own identityread_test.go files pin the other half: that the name comes from a
// READ of the running configuration, that ReadIdentity spends no round trip
// getting it, and that a rename's verifying re-read is never allowed to become
// the observation.
func TestFSRangesNowFeedTheSecondSourceFromReadState(t *testing.T) {
	// The literal fs-s3400 ReadIdentity shape after a read-state: `show version`
	// facts from login, plus the name harvested from that gesture's
	// "show running-config".
	var d = deviceCarryingIdentity(t, "sw-lab-01", "fs-s3400", driver.Identity{
		Model: "S3400-48T4SP", Serial: "CG2003161906N0002", MAC: "64:9d:99:22:7b:95",
		Firmware: "2.0.2J", Hostname: "sw-lab-01", Source: "ssh_cli",
	})

	// No LLDP row, and there never will be one: this range's agent serves nothing
	// under 1.0.8802 and its CLI LLDP path writes no sys_name.
	var observed, source = hostnameObserved(d, model.LLDPSelf{}, false)
	if observed != "sw-lab-01" || source != hostnameSourceReadState {
		t.Fatalf("observed %q from %q: an FS switch has no other source, so this is "+
			"the difference between a verified name and `not_observed` for ever",
			observed, source)
	}
	if hostnameConformityOf(d, observed, source).State != hostnameOK {
		t.Errorf("state = %q, want ok: the device answers to the inventory's name",
			hostnameConformityOf(d, observed, source).State)
	}

	// And the case this exists for: the same device carrying a name meshcraft did
	// NOT write reads as drift and becomes repairable — which it could not be
	// while nothing observed it at all.
	var renamed = deviceCarryingIdentity(t, "sw-lab-01", "fs-s3900", driver.Identity{
		Model: "S3900-24T4S", Firmware: "1.7.0", Hostname: "test-fs-s3900",
		Source: "ssh_cli",
	})
	observed, source = hostnameObserved(renamed, model.LLDPSelf{}, false)
	var v = hostnameConformityOf(renamed, observed, source)
	if v.State != hostnameDrift {
		t.Fatalf("state = %q (%s), want drift", v.State, v.Because)
	}
	if !hostnameRepairableFor(renamed, v) {
		t.Error("an observed FS switch answering to another name must be repairable: " +
			"before this source was fed, the healer could not act on either range")
	}
}

// The ordering this rests on, pinned in the only place it exists: readDeviceState
// must call ReadState BEFORE selfHealIdentity.
//
// On fs-s3400 and fs-s3900 the identity path reports the device's own name only
// if the running-config read has already happened on that access — the name is
// harvested from ReadState, never fetched by ReadIdentity, because fetching it
// would cost ninety seconds of paged output on the S3400 (each package's
// identityread.go). Swap the two calls and nothing fails to compile, no test
// about identity fails, and both FS ranges quietly go back to `not_observed`
// while every screen keeps showing a hostname section that can never converge.
//
// SOURCE-ORDER CHECK, and yes that is unusual: the alternative is a Server test,
// which needs PostgreSQL, and this file's whole premise is that the reconciliation
// stays testable without one. The two call sites are unique strings in the file.
func TestTheSecondSourceIsFedBeforeIdentityIsObserved(t *testing.T) {
	var src, err = os.ReadFile("recon_handlers.go")
	if err != nil {
		t.Fatalf("read recon_handlers.go: %v", err)
	}
	var text = string(src)
	var readState = strings.Index(text, "state, err = adm.ReadState(ctx)")
	var selfHeal = strings.Index(text, "s.selfHealIdentity(ctx, d, adm, out, emit)")
	if readState < 0 || selfHeal < 0 {
		t.Fatalf("call sites moved or were renamed (ReadState at %d, selfHealIdentity "+
			"at %d): re-anchor this test rather than deleting it — what it protects is "+
			"that the FS ranges' only observation source is filled before it is read",
			readState, selfHeal)
	}
	if readState > selfHeal {
		t.Error("selfHealIdentity now runs BEFORE ReadState: on fs-s3400 and fs-s3900 " +
			"the device's own name is harvested by ReadState and replayed from memory " +
			"by ReadIdentity, so in this order it is never observed and both ranges " +
			"return to `not_observed` — silently, since nothing else notices")
	}
}

// The overlay rule, on the field this section cares about: a partial re-read that
// learned only the name must ADD it without erasing the serial the previous read
// established. mergeIdentityFacts had no test at all.
func TestMergeKeepsTheFactsAPartialReadDidNotSee(t *testing.T) {
	var stored = identityFacts(driver.Identity{
		Model: "CRS326-24G-2S+", Serial: "9458", Source: "rest_api",
	})
	var merged = mergeIdentityFacts(stored, identityFacts(driver.Identity{
		Hostname: "sw-lab-01",
	}))
	if merged == nil {
		t.Fatal("a re-read that learned the name changed something: nil means \"no write\"")
	}
	if merged["hostname"] != "sw-lab-01" {
		t.Errorf("hostname = %q, want the observed one", merged["hostname"])
	}
	if merged["serial"] != "9458" || merged["model"] != "CRS326-24G-2S+" {
		t.Errorf("a partial observation erased what was known: %v", merged)
	}
	// Re-reading the SAME facts must cost no row write.
	if mergeIdentityFacts(merged, identityFacts(driver.Identity{
		Hostname: "sw-lab-01",
	})) != nil {
		t.Error("an unchanged re-read must return nil, or every read-state writes a row")
	}
}
