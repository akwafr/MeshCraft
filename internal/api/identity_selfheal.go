package api

// Inventory self-heal: refresh WHAT the device IS on every re-read.
//
// Until this file, NO live path updated `devices.model` after adoption: a device
// adopted by hand, or before its connector's identification improved, kept an
// empty (or wrong) model forever — the S3900 whose model was fixed once BY HAND
// is the incident this closes. Identity is a fact of the hardware, exactly like
// the faceplate (cf. fillFaceplate), so a read is the right place to catch up.
//
// Self-contained on purpose: one function called from one line of
// handleReadDeviceState, everything else lives here.
//
// TWO PRICES, TWO POLICIES:
//
//   - connector implements [driver.IdentityReader] (IOS, EOS, S3400, S3900,
//     RouterOS): the open access already carries — or reads cheaply — model,
//     serial, firmware.
//     Refreshed on EVERY read: fills holes AND detects a hardware swap at the
//     same address (a real change updates the model and leaves a device event,
//     so the swap is visible in history).
//   - connector only implements [driver.Identifier] (SwOS, Instant On, and any
//     newer connector that has not adopted IdentityReader yet): identification
//     opens its own session, so it is only worth it to FILL A HOLE — it runs
//     when the stored model is EMPTY, with the credential that just proved
//     itself, and never again once filled. No swap detection on these
//     platforms; the comment on driver.IdentityReader says how to earn it.
//
// FIRMWARE AND SERIAL are persisted under `extra.identity` (see identityFacts):
// they are facts the UI and support flows keep re-asking for, and every
// connector here yields them for free once identity is read at all. `seen_at`
// is DELIBERATELY not stored: identical facts must not cost a row write per
// read — observed_at already timestamps the gesture.

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// selfHealIdentity observes the device's identity on the open access and
// persists what differs from the inventory. Best-effort, like every catch-up in
// this pipeline: any failure lands in `out` as a note, never interrupts the
// read — ports and VLANs are already saved when this runs.
//
// TAKES A ctx AND AN EMITTER, not an *http.Request, since the read-state gesture
// became schedulable (cf. deviceEventFn): a hardware swap discovered by the
// periodic read-back is exactly as worth a history line as one discovered by a
// click, and it must be attributed to "meshcraft" rather than to whoever's
// session happened to be open.
func (s *Server) selfHealIdentity(ctx context.Context, d model.Device,
	adm driver.Admin, out map[string]any, emit deviceEventFn) {

	var id, obsErr = s.observeIdentity(ctx, d, adm)
	if obsErr != nil && strings.TrimSpace(id.Model) == "" && len(identityFacts(id)) == 0 {
		out["identity_note"] = "identity not read: " + obsErr.Error()
		return
	}
	if obsErr != nil {
		// Partial identity is a useful result — the contract says so — but the
		// operator must see it was partial.
		out["identity_note"] = "identity read partially: " + obsErr.Error()
	}

	var heal = identityHealDecision(d.Model, id.Model)
	var facts = identityFacts(id)
	var factsChanged = len(facts) > 0 && !maps.Equal(facts, storedIdentityFacts(d.Extra))
	if !heal.Update && !factsChanged {
		return // same knowledge, zero writes: the common case must stay free
	}

	// Model passed to the store is "" when only the facts moved — the SQL guard
	// (`nullif`) then keeps the stored model, cf. store.SetDeviceIdentity.
	var newModel string
	if heal.Update {
		newModel = heal.Model
	}
	var raw, mErr = json.Marshal(facts)
	if mErr != nil {
		raw = []byte(`{}`)
	}
	var _, sErr = s.Store.SetDeviceIdentity(ctx, d.ID, newModel, raw)
	if sErr != nil {
		out["identity_note"] = "identity observed but not saved: " + sErr.Error()
		return
	}
	if heal.Update {
		out["model_refreshed"] = heal.Model
	}

	// History: filling a blank is worth a line; a CHANGED model on the same
	// address is a hardware swap and MUST be visible after the fact.
	switch heal.Event {
	case healEventFilled:
		emit(d, "device.identity", model.SevInfo,
			"Model observed on \""+d.Name+"\": "+heal.Model+
				" (was empty — filled on re-read).")
	case healEventChanged:
		emit(d, "device.identity", model.SevInfo,
			"Model changed on \""+d.Name+"\": \""+strings.TrimSpace(d.Model)+
				"\" -> \""+heal.Model+"\" — hardware swapped at the same address?")
	}
}

// observeIdentity returns what the connector can say about WHAT this device is,
// choosing the cheap path when it exists (cf. file header for the two policies).
//
// ORDER MATTERS FOR ONE FIELD, and it is not obvious from here: on fs-s3400 and
// fs-s3900 the cheap path can only report the device's own NAME if ReadState has
// already run on this access, because that is the gesture whose running-config
// read the name is harvested from (cf. each package's identityread.go, and
// hostnameObserved for what the name is then used for). readDeviceState does read
// state first — TestTheSecondSourceIsFedBeforeIdentityIsObserved pins it — and an
// access that never reads state simply observes no name, which is a state, not a
// failure.
func (s *Server) observeIdentity(ctx context.Context, d model.Device,
	adm driver.Admin) (driver.Identity, error) {

	var reader, cheap = driver.AsIdentityReader(adm)
	if cheap {
		return reader.ReadIdentity(ctx)
	}
	// No cheap path: a second session is only worth a HOLE. A device that
	// already has a model keeps it until its connector learns IdentityReader.
	if strings.TrimSpace(d.Model) != "" {
		return driver.Identity{}, nil
	}
	var conn, tgt, _, err = s.accessFor(ctx, d)
	if err != nil {
		return driver.Identity{}, err
	}
	var ident, can = conn.(driver.Identifier)
	if !can {
		return driver.Identity{}, nil
	}
	// The credential that just proved itself, and ONLY that one: Identify's
	// candidate budget exists to protect against lockout, and the read that
	// brought us here already knows which secret the device accepts. Connectors
	// that need no credential (Instant On) ignore the list.
	var cands []driver.Candidate
	var acc = adm.Accepted()
	if acc != (driver.Candidate{}) {
		cands = []driver.Candidate{acc}
	}
	return ident.Identify(ctx, tgt, cands)
}

// --- Pure decision, testable without a device or a database ---

const (
	healEventFilled  = "filled"
	healEventChanged = "changed"
)

// identityHeal is what the observation decided: whether `devices.model` moves,
// to what, and whether history gets a line.
type identityHeal struct {
	Model  string
	Update bool
	// Event: "" (silent no-op), healEventFilled or healEventChanged.
	Event string
}

// identityHealDecision applies the model-refresh rules. They fit in four lines
// and each one is an invariant the tests lock:
//
//   - observation EMPTY        → keep whatever is stored (a connector that could
//     not read the model this time must not erase what discovery once knew);
//   - same model               → no-op, no write, no event;
//   - stored EMPTY, obs known  → fill, event (the self-heal this file exists for);
//   - both known, different    → update + event (hardware swap at the same IP).
func identityHealDecision(storedModel, observedModel string) identityHeal {
	var stored = strings.TrimSpace(storedModel)
	var observed = strings.TrimSpace(observedModel)
	if observed == "" || observed == stored {
		return identityHeal{}
	}
	if stored == "" {
		return identityHeal{Model: observed, Update: true, Event: healEventFilled}
	}
	return identityHeal{Model: observed, Update: true, Event: healEventChanged}
}

// identityFacts reduces an observation to what is persisted under
// `extra.identity`: flat strings, empties dropped, so the stored object can be
// compared with maps.Equal and identical knowledge costs no write. The model is
// repeated here on purpose — `extra.identity` is then self-carrying (one object
// says model+serial+firmware+source together, as the observation made them).
func identityFacts(id driver.Identity) map[string]string {
	var out = map[string]string{}
	var put = func(key, val string) {
		val = strings.TrimSpace(val)
		if val != "" {
			out[key] = val
		}
	}
	put("model", id.Model)
	put("model_code", id.ModelCode)
	put("serial", id.Serial)
	put("mac", id.MAC)
	put("firmware", id.Firmware)
	put("build", id.Build)
	// hostname is the device's OWN name, and it is here because the hostname
	// reconciliation reads it as its second observation source (cf.
	// hostnameObserved): LLDP answers for free on every poll cycle, and this is
	// what answers for a device whose LLDP or SNMP is silent. It was absent from
	// this map until 2026-08-06 — the field existed on driver.Identity and nothing
	// persisted it, so the only observation of a device's own name was LLDP's, and
	// a device off the LLDP-MIB could never be judged at all.
	//
	// The one cost: renaming a device at its console now makes the next read-state
	// write a row (the facts moved). That is the correct amount — a name that
	// changed IS new knowledge.
	put("hostname", id.Hostname)
	put("source", id.Source)
	return out
}

// storedIdentityFacts reads `extra.identity` back in the shape identityFacts
// writes. Unreadable or absent yields an empty map — which simply means "we
// knew nothing", and any observation then counts as new knowledge.
func storedIdentityFacts(extra json.RawMessage) map[string]string {
	var wrap struct {
		Identity map[string]string `json:"identity"`
	}
	if len(extra) == 0 || json.Unmarshal(extra, &wrap) != nil || wrap.Identity == nil {
		return map[string]string{}
	}
	return wrap.Identity
}

// --- WHEN a stored fact was taken, for the consumers that must not read one in
// --- the present tense ---

// identityRefreshingConnectors says, per connector, whether `extra.identity` is
// RE-OBSERVED on every read-state or was written once and then frozen. It is this
// file's two policies restated in the one form a VERDICT can consult: without a
// session, on a list of two hundred devices.
//
// # The defect this exists for
//
// `extra.identity.hostname` is the second observation source of the hostname
// reconciliation (cf. hostnameObserved). On the connectors below marked false the
// [driver.Identifier] fallback is the ONLY way that key is ever written, and
// observeIdentity runs the fallback solely while `devices.model` is EMPTY — once,
// on the first read-state after adoption, and never again. The value is then
// frozen for the life of the row. Handed back plainly it is indistinguishable
// from a reading taken this minute, so a device renamed at its console reported
// AGREEMENT with a name it no longer has; and when it disagreed instead, the
// verdict was `drift`, the healer pushed, and hostnameConverged re-read the same
// frozen fact and never saw convergence — a push a day, for ever, on the
// platforms with the smallest session budgets. Both halves are the same error:
// a source that cannot be refreshed was allowed to speak in the present tense.
//
// # Why a table, when the answer is an interface
//
// The split IS [driver.IdentityReader], but it is resolved by a type assertion on
// a live [driver.Admin] — i.e. after a session is open, which the verdict path
// must never do. So the answer is declared here and keyed on the connector name
// `devices.extra.driver` already carries: the same trade as
// hostnameWritableConnectors next door, for the same reason, and guarded the same
// way. TestTheTwoPathsToTheSecondSourceStayApart asserts every entry against the
// connector's real access type and fails on a registered connector missing from
// this map, so a connector that gains or loses ReadIdentity cannot ship with the
// declaration and the implementation out of step.
//
// An unknown connector is treated as NOT refreshing, the project rule that an
// undeclared capability is absent — here it costs coverage, and erring the other
// way would be the lie above.
//
// # WHY THIS IS NOT A TIMESTAMP ON THE STORED FACT
//
// Dating the fact is the obvious shape and it does not work, which is worth
// recording since it will be proposed again:
//
//   - an honest `observed_at` would have to be rewritten on every read-state, and
//     this file's header refuses exactly that cost — identical facts must not cost
//     a row write per read. Written only when the facts CHANGE, it dates the last
//     change and not the last look, so a RouterOS that has answered correctly every
//     hour for six months would carry a six-month-old date;
//   - and AGE IS NOT THE PROPERTY. A cheap-path fact is current however old it is
//     in wall-clock terms, because the next read-state re-takes it; a swos fact is
//     already frozen five minutes after adoption, because nothing will ever re-take
//     it. No threshold separates those two. The structural question — can this
//     device's identity be re-observed AT ALL — needs no clock and nothing to tune;
//   - and a marker stored per row would mark every EXISTING row untrusted,
//     including the three ranges whose facts are genuinely refreshed, i.e. it would
//     degrade what works to describe what does not.
//
// The one inaccuracy left is bounded and self-correcting, and it is in the right
// direction: this table describes the connectors as they are TODAY, while a row
// may hold a fact written before its connector gained ReadIdentity (fs-s3400
// crossed on 2026-08-10). Such a device is trusted one read-state too early — and
// that read-state overwrites the fact with a fresh one.
var identityRefreshingConnectors = map[string]bool{
	// CHEAP PATH: [driver.IdentityReader] on the already-open access, so
	// selfHealIdentity re-observes identity on every read-state and the stored
	// facts are as recent as the last one. Of these five only mikrotik-routeros,
	// fs-s3400 and fs-s3900 actually carry a Hostname — a reader that reports no
	// name simply leaves the key empty, which hostnameObserved already handles as
	// "not observed". This list says WHEN the facts travel, not WHICH.
	"mikrotik-routeros": true,
	"cisco-ios":         true,
	"arista-eos":        true,
	"fs-s3400":          true,
	"fs-s3900":          true,
	// FROZEN AT ADOPTION: no IdentityReader, so identification opens its own
	// session and is only worth a HOLE (see the file header). hpe-procurve,
	// aruba-instant-on-1930 and mikrotik-swos all DO fill Identity.Hostname on that
	// path — from `show system-information`, the login page's `sysName` and
	// `/sys.b`'s `id` respectively — which is precisely why they are the ranges
	// that carry a stale name to hand back. ubiquiti-edgeswitch fills it on
	// neither path and is here for completeness.
	//
	// These entries turn false into MISSING COVERAGE, deliberately: nothing is
	// concluded about their names from the identity source, and LLDP judges them
	// where it answers. The alternative was a periodic re-identification — one
	// session per device per pass on the two smallest session budgets in the fleet,
	// to detect a console rename nobody has made. Refused on 2026-08-10; the honest
	// statement is "cannot tell", and it is what the verdict now says.
	"hpe-procurve":          false,
	"aruba-instant-on-1930": false,
	"mikrotik-swos":         false,
	"ubiquiti-edgeswitch":   false,
}

// identityCanBeReObserved reports whether this device's stored `extra.identity`
// is re-taken on every read-state, i.e. whether it may be read as a statement
// about the device NOW rather than about the device at adoption.
//
// PURE and database-free, like hostnameWriteSupport and for its reason: called
// once per device on a fleet list.
func identityCanBeReObserved(d model.Device) bool {
	return identityRefreshingConnectors[driver.DriverNameFromExtra(d.Extra)]
}
