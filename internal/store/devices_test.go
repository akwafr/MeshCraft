package store

import (
	"reflect"
	"testing"
	"time"
)

// Device attachment to its collector (migration 0022) does not live in Go: it lives in the
// `where` clause and projection of DevicesWatchedBy. These tests therefore replay its
// DECISION TABLE, as generations_test.go replays secret try order — without a database,
// because the package has none and a test you cannot run locks nothing.
//
// What they lock: semantics, i.e. the two invariants a future "small cleanup" of the query
// would break without anything failing elsewhere — eligibility stays by COVERAGE (so ping is
// not restricted), and the collector is UNIQUE once attachment exists.
//
// What they cannot verify: that the SQL actually says this. The predicates below are a
// word-for-word transcription of the query, and both must move together. The query itself
// was exercised on disposable PostgreSQL 17, case by case, before being written here.

// watchProxy: the LIST RECIPIENT proxy (query's `p`).
type watchProxy struct {
	id     string
	global bool
	// sites: coverage, i.e. `proxy_sites`. Empty = covers NOTHING, never "everywhere"
	// (cf. model.Proxy).
	sites map[string]bool
}

// watchRow: a `devices` row as the query sees it.
type watchRow struct {
	name    string
	siteID  string
	mgmtIP  string
	proxyID string // attachment; "" = unattached (NULL in database)
}

// watchEligible transcribes:
//
//	coalesce(d.mgmt_ip,'') <> '' and (p.global or exists (select 1 from proxy_sites …))
//
// No mention of attachment, and that is the whole point: eligibility — therefore ICMP
// monitoring — depends ONLY on coverage.
func watchEligible(p watchProxy, r watchRow) bool {
	if r.mgmtIP == "" {
		return false
	}
	return p.global || p.sites[r.siteID]
}

// watchCollects transcribes `(d.proxy_id is null or d.proxy_id = p.id)`.
func watchCollects(p watchProxy, r watchRow) bool {
	return r.proxyID == "" || r.proxyID == p.id
}

// collectors returns proxies that consider themselves collectors of this device among those
// that see it. That is the only thing that matters for traps: there must be exactly one
// once attachment is declared.
func collectors(proxies []watchProxy, r watchRow) []string {
	var out = []string{}
	var p watchProxy
	for _, p = range proxies {
		if watchEligible(p, r) && watchCollects(p, r) {
			out = append(out, p.id)
		}
	}
	return out
}

// watchers returns proxies that will ping this device.
func watchers(proxies []watchProxy, r watchRow) []string {
	var out = []string{}
	var p watchProxy
	for _, p = range proxies {
		if watchEligible(p, r) {
			out = append(out, p.id)
		}
	}
	return out
}

// The two instance proxies from the demo database: the situation that motivated this batch,
// and the only case where SNMP duplication is guaranteed.
var deuxGlobaux = []watchProxy{
	{id: "local", global: true},
	{id: "akwalan", global: true},
}

// The case that motivated the migration. Two instance proxies see the same device; without
// attachment, both poll it via SNMP and there is no answer to "which address to write as trap
// destination?" Attached, there is only one.
func TestRattachementRendLeCollecteurUnique(t *testing.T) {
	var dev = watchRow{name: "sw-a1", siteID: "A", mgmtIP: "192.168.1.10", proxyID: "akwalan"}
	var got = collectors(deuxGlobaux, dev)
	if len(got) != 1 || got[0] != "akwalan" {
		t.Fatalf("attached device must have ONE collector and only one (its own), got %v", got)
	}
}

// Counterpart of the previous test, and this batch's trap: making the collector unique must
// remove NOTHING from ping. Both proxies keep monitoring, otherwise the collector's fall would
// take device reachability with it — an indicator going dark because a proxy fell, not because
// the switch died.
func TestRattachementNeRestreintPasLePing(t *testing.T) {
	var dev = watchRow{name: "sw-a1", siteID: "A", mgmtIP: "192.168.1.10", proxyID: "akwalan"}
	var got = watchers(deuxGlobaux, dev)
	if len(got) != 2 {
		t.Fatalf("ICMP monitoring stays by coverage: expected both proxies, got %v", got)
	}
}

// Unattached: strictly pre-migration-0022 behavior. Every eligible proxy is a collector, nobody
// having declared otherwise. This test says the migration is semantics-preserving.
func TestNonRattacheConserveLeComportementParCouverture(t *testing.T) {
	var dev = watchRow{name: "sw-b1", siteID: "B", mgmtIP: "192.168.1.10"}
	var got = collectors(deuxGlobaux, dev)
	if len(got) != 2 {
		t.Fatalf("without attachment, every eligible proxy collects: expected 2, got %v", got)
	}
	if len(watchers(deuxGlobaux, dev)) != 2 {
		t.Fatalf("without attachment, every eligible proxy pings")
	}
}

// Detaching returns the device to coverage. Fallback for `on delete set null` as much as
// explicit detach (SetDeviceProxy with empty string), and it must leave no device without a
// collector.
func TestDetachementRendALaCouverture(t *testing.T) {
	var dev = watchRow{name: "sw-a1", siteID: "A", mgmtIP: "192.168.1.10", proxyID: "akwalan"}
	dev.proxyID = "" // what SetDeviceProxy(ctx, id, "") writes to database
	var got = collectors(deuxGlobaux, dev)
	if len(got) != 2 {
		t.Fatalf("after detach, back to coverage: expected 2, got %v", got)
	}
}

// Attachment to a proxy that does not cover the site does NOT make that proxy collect:
// coverage remains the only entry, because it is a confidentiality boundary, not convenience.
//
// The test records the assumed consequence: this device is collected by NO ONE — neither its
// proxy, which does not see it, nor the instance proxy, which sees it but is not the declared
// collector. It remains pinged. The layer proposing attachment must offer only covering
// proxies; if this line ever fails, someone widened eligibility and migration 0022 must be
// re-read before "fixing" the test.
func TestRattachementNeDonnePasLaCouverture(t *testing.T) {
	var proxies = []watchProxy{
		{id: "local", global: true},
		{id: "agence-b", sites: map[string]bool{"B": true}},
	}
	var dev = watchRow{name: "sw-a1", siteID: "A", mgmtIP: "192.168.1.10", proxyID: "agence-b"}
	if len(collectors(proxies, dev)) != 0 {
		t.Fatalf("non-covering proxy must not collect, got %v", collectors(proxies, dev))
	}
	var pinged = watchers(proxies, dev)
	if len(pinged) != 1 || pinged[0] != "local" {
		t.Fatalf("device remains pinged by covering proxy, got %v", pinged)
	}
}

// A device without address is neither pinged nor collected, attached or not: designating a
// collector does not manufacture an address to poll.
func TestSansAdressePasDeSurveillance(t *testing.T) {
	var dev = watchRow{name: "sw-neuf", siteID: "A", proxyID: "akwalan"}
	if len(watchers(deuxGlobaux, dev)) != 0 || len(collectors(deuxGlobaux, dev)) != 0 {
		t.Fatalf("device without mgmt_ip stays out of monitoring")
	}
}

// Empty string DETACHES, it does not fail. Verified on the translator SetDeviceProxy uses:
// that is the only path where "no attachment" can be lost, and an empty string sent as-is to
// a ::uuid parameter would fail the update instead of detaching.
//
// The test lives here with the attachment it serves, although nullableUUID is declared in
// discovery_store.go: SetDeviceProxy relies on this behavior to detach, and regression would
// show up here if someone decided an empty string is a caller error.
func TestNullableUUIDDetacheSurChaineVide(t *testing.T) {
	if nullableUUID("") != nil {
		t.Fatalf("empty string must become NULL, got %v", nullableUUID(""))
	}
	var id = "22222222-0000-0000-0000-000000000002"
	if nullableUUID(id) != id {
		t.Fatalf("id must pass through unchanged, got %v", nullableUUID(id))
	}
}

// --- Recorded trap destination (migration 0023) ---
//
// Three batch semantics live in SQL not Go: SetDeviceProxy and SetTrapDestination `case`
// clauses, and ConfirmTrapPath's `where`. Replayed here as a decision table, like attachment
// tests above and for the same reason — the package has no database, and a test you cannot run
// locks nothing.
//
// What they lock: the two invariants a "small cleanup" of these queries would break without
// anything failing elsewhere. Proof refers to a PATH, not a device — it must disappear as soon
// as the path changes — and it is journalized ONLY ONCE, the emitter rearming its counter on
// every tunnel reconnect.
//
// What they cannot verify: that the SQL actually says this. The predicates below transcribe
// the clauses, and both must move together.

// trapDestRow: migration 0023's four columns, plus attachment.
type trapDestRow struct {
	// proxyID: `devices.proxy_id`, declared collector.
	proxyID string
	// destAddr / destProxyID: what was WRITTEN into the device, and where the address came from.
	destAddr    string
	destProxyID string
	// confirmed: `trap_seen_at is not null` — a trap got through.
	confirmed bool
}

// setDeviceProxy transcribes SetDeviceProxy's `update`:
//
//	proxy_id = $2,
//	trap_dest_* = case when trap_dest_proxy_id is distinct from $2 then null else … end
//
// Predicate uses `trap_dest_proxy_id` not old `proxy_id`: recorded destination stays valid if
// and only if it comes from the proxy that is collector AFTER the update.
func setDeviceProxy(r trapDestRow, proxyID string) trapDestRow {
	var out = r
	out.proxyID = proxyID
	if r.destProxyID != proxyID {
		out.destAddr, out.destProxyID, out.confirmed = "", "", false
	}
	return out
}

// setTrapDestination transcribes SetTrapDestination's `update`:
//
//	trap_seen_at = case when trap_dest_addr = $2 and trap_dest_proxy_id = $3
//	                    then trap_seen_at else null end
func setTrapDestination(r trapDestRow, addr, proxyID string) trapDestRow {
	var out = r
	out.confirmed = r.confirmed && r.destAddr == addr && r.destProxyID == proxyID
	out.destAddr, out.destProxyID = addr, proxyID
	return out
}

// confirmTrapPath transcribes ConfirmTrapPath's `where`:
//
//	where id = $1 and trap_dest_proxy_id = $2 and trap_seen_at is null
//
// Returns row after and rows touched — i.e. "first time?", which journaling depends on.
func confirmTrapPath(r trapDestRow, proxyID string) (trapDestRow, bool) {
	if r.destProxyID != proxyID || r.confirmed {
		return r, false
	}
	var out = r
	out.confirmed = true
	return out, true
}

// Reference row: destination pushed and confirmed.
func destConfirmee() trapDestRow {
	return trapDestRow{
		proxyID: "akwalan", destAddr: "10.42.0.67", destProxyID: "akwalan",
		confirmed: true,
	}
}

// TestChangerDeCollecteurInvalideLaConfirmation: changing collector changes the address to
// write into the device. What is there becomes wrong, and acquired confirmation is worthless
// — it referred to the return path toward the OLD proxy. Keeping it would show "path verified"
// for a path nothing ever took.
func TestChangerDeCollecteurInvalideLaConfirmation(t *testing.T) {
	var got = setDeviceProxy(destConfirmee(), "local")
	if got.confirmed {
		t.Error("collector change must cancel confirmation")
	}
	if got.destAddr != "" || got.destProxyID != "" {
		t.Errorf("recorded destination must be forgotten, got %q/%q",
			got.destAddr, got.destProxyID)
	}
	// Detach too: no collector, therefore no unique address to write — destination designates
	// nothing reproducible.
	if setDeviceProxy(destConfirmee(), "").confirmed {
		t.Error("detach must cancel confirmation")
	}
}

// TestRattachementIdentiqueNePerdRien: counterpart of the previous. A screen resubmitting the
// current value, or a replayed gesture, must not destroy a still-accurate proof — otherwise the
// next trap would re-announce an already known path.
func TestRattachementIdentiqueNePerdRien(t *testing.T) {
	var got = setDeviceProxy(destConfirmee(), "akwalan")
	if !got.confirmed || got.destAddr != "10.42.0.67" {
		t.Errorf("identical re-attachment keeps destination and proof, got %+v", got)
	}
}

// TestReposeIdentiqueConserveLaPreuve: replaying exactly the same line — what every replayed
// provisioning does, and it replays on every model or address change — must neither lose the
// established fact nor generate a second "path established" announcement.
func TestReposeIdentiqueConserveLaPreuve(t *testing.T) {
	var got = setTrapDestination(destConfirmee(), "10.42.0.67", "akwalan")
	if !got.confirmed {
		t.Error("replaying same destination must not reset path doubt")
	}
}

// TestReposeAilleursRemetToutAProuver: a different address, or the same proxy via another
// route, is a DIFFERENT return path. Nothing says it works, and old proof does not carry over.
func TestReposeAilleursRemetToutAProuver(t *testing.T) {
	if setTrapDestination(destConfirmee(), "172.17.0.1", "akwalan").confirmed {
		t.Error("new address must reset proof")
	}
	if setTrapDestination(destConfirmee(), "10.42.0.67", "local").confirmed {
		t.Error("new proxy must reset proof")
	}
}

// TestPreuveNeSeJournaliseQuUneFois: proxy emits `EventTrapSeen` only once per device and
// proxy, but REARMS its counter as soon as the device leaves then re-enters its watch list —
// therefore on every tunnel reconnect and every fleet change. Without the `where` bound, a
// proxy restart would produce one "return path established" line for the whole fleet at once.
func TestPreuveNeSeJournaliseQuUneFois(t *testing.T) {
	var row = destConfirmee()
	row.confirmed = false
	var got, first = confirmTrapPath(row, "akwalan")
	if !first || !got.confirmed {
		t.Fatalf("first trap confirms and journals, got %v/%+v", first, got)
	}
	var again, second = confirmTrapPath(got, "akwalan")
	if second {
		t.Error("second trap must journal nothing more")
	}
	if !again.confirmed {
		t.Error("acquired confirmation must not be lost on next trap")
	}
}

// TestConfirmTrapPathNAccepteAucuneHorloge: clock-mixing lock, and it targets the SIGNATURE
// because that is all a test without a database can hold.
//
// What happened on first real-hardware trial: `trap_seen_at` was eleven seconds BEFORE the
// `trap_dest_at` of the push it confirmed. Proof before its claim — not causality violation,
// but because both columns came from two clocks. `trap_dest_at` is written by PostgreSQL
// `now()`, therefore the controller; proof date came from the proxy event (`Monitor.emit`),
// therefore a customer-deployed machine with no sync guarantee. Two adjacent columns in the
// same row invite comparison, and the defect was found exactly that way.
//
// The controller is the only clock that can order its own claims. The fix is therefore not a
// warning comment but REMOVING the parameter: as long as the function accepts no `time.Time`,
// nobody can inject a third-party date, and the `update`'s `now()` remains the only source.
// This test fails on the first attempt to add it back.
func TestConfirmTrapPathNAccepteAucuneHorloge(t *testing.T) {
	// METHOD EXPRESSION, not a bound method: receiver is in the signature, making parameter
	// count readable as-is.
	var fn = reflect.TypeOf((*Store).ConfirmTrapPath)
	var horloge = reflect.TypeOf(time.Time{})
	var i int
	for i = 0; i < fn.NumIn(); i++ {
		if fn.In(i) == horloge {
			t.Fatalf("ConfirmTrapPath accepts timestamp at position %d: proof would reuse "+
				"proxy date while trap_dest_at comes from now() — the two columns would no "+
				"longer be comparable", i)
		}
	}
	// Counterpart: date must come from somewhere. `now()` is written in the function SQL, same
	// as SetTrapDestination — both columns read on the SAME clock, which is all we require.
	if fn.NumIn() != 4 {
		t.Errorf("expected signature (Store, ctx, deviceID, proxyID), got %v", fn)
	}
}

// TestPreuveExigeLaDestinationEcrite: `where` also filters on `trap_dest_proxy_id`. A trap
// accepted by a proxy whose address is not the one we pushed does not confirm our claim — and
// a destination never pushed has nothing to confirm.
func TestPreuveExigeLaDestinationEcrite(t *testing.T) {
	var row = destConfirmee()
	row.confirmed = false
	var _, autre = confirmTrapPath(row, "local")
	if autre {
		t.Error("another proxy must not confirm written destination")
	}
	var jamaisPosee = trapDestRow{proxyID: "akwalan"}
	var _, rien = confirmTrapPath(jamaisPosee, "akwalan")
	if rien {
		t.Error("without written destination, nothing to confirm")
	}
}
