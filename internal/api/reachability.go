package api

import (
	"strings"
	"time"

	"meshcraft/internal/model"
)

// WHOSE VERDICT COUNTS, when several proxies watch one device and disagree.
//
// # The failure this answers
//
// On 2026-08-02 at 15:35:35 a newly enrolled instance proxy connected. At
// 15:35:39 — 3.7 seconds later, three missed pings at one per second — four
// devices of another site were marked unreachable. Every one of them was alive:
// their own collector was polling them successfully at that instant, and LLDP
// snapshots kept arriving throughout. The new proxy sat on 172.16.99.0/24, was
// handed the whole inventory because an instance proxy covers every site, and
// pinged addresses that simply do not exist on its network.
//
// Two things made that possible. `devices.status` was ONE column written by
// whichever proxy spoke last, so a proxy that could not reach a device overwrote
// the verdict of one that could. And nothing outranked it: the proxy actually
// talking to the device knew it by SNMP, but that knowledge lived nowhere the
// status decision consulted.
//
// The false state was also STICKY, which is what made it a fault rather than a
// blip: proxies emit on TRANSITION only (cf. cmd/proxy/monitor.go), so the
// collector — still holding "up" — had nothing to say, and no future event would
// correct the database until the device genuinely flapped.
//
// # The rule
//
// A device is online if a proxy that has PROVEN which device it is talking to
// sees it, whatever any other proxy says.
//
//	1. any CONFIRMED proxy reports up            → online
//	2. else, any CONFIRMED proxy reports down     → offline
//	3. else (nothing confirmed)                   → OR over the rest: up if any
//	                                                proxy sees it
//
// Rule 3 is not a leftover. Nine of the thirteen devices in the reference fleet
// have no usable SNMP identity — SwOS units with no community, devices meshcraft
// polls but cannot authenticate — and can therefore never be confirmed by anyone.
// Requiring proof before any verdict counted would have left them with no
// reachability monitoring at all. The OR alone already fixes the reported fault
// for them, because it replaces last-writer-wins with a rule where an unreachable
// proxy cannot contradict a reachable one.
//
// # Why proof, and not the address domain
//
// The obvious fix was to stop handing a proxy devices outside its declared
// address domain. That is TOPOLOGY: something an operator declares, and therefore
// something that can be absent, stale or wrong — which is precisely the state the
// fleet was in when this happened (the new proxy had a domain, the rule just
// never consulted it for membership). Identity is EVIDENCE, produced by the
// device itself and matched against the inventory. A proxy that produced that
// match is talking to the right equipment, and needs nobody's declaration to say
// so.
//
// Address domains keep their existing job — disambiguating two devices at one
// address (cf. internal/netscope) — they simply stop being the only thing between
// a misplaced proxy and the fleet's status column.
//
// # And the symmetric danger, which matters as much
//
// A rule that only stopped false REDS would be half a rule. An unconfirmed proxy
// answering at an address that belongs to a different machine on its network
// would report "up" for a device that is off — a green light on a dead switch,
// which the netscope header rightly calls worse than no light at all. Because
// rule 1 requires confirmation, and rule 2 lets a confirmed down stand against
// any number of unconfirmed ups, that case is closed by the same mechanism.

// reachVerdict is the computed device status and the sentence that explains it.
type reachVerdict struct {
	// Status is the value for devices.status: "ok", "off", or "unknown".
	Status string
	// Note goes to devices.status_note. It NAMES the proxy that established the
	// verdict — a false red from a misattached collector has to be diagnosable
	// from the device sheet, not from controller logs.
	Note string
	// Confirmed says the verdict rests on a proxy that proved the device's
	// identity. Drives nothing on its own; it is what the log line reports so
	// "who decided this" survives in the journal.
	Confirmed bool
	// Decider is the proxy whose observation carried the decision, for the log.
	Decider string
}

// judgeReach applies the rule above to every observation held about one device.
//
// PURE, and that is deliberate: this is the decision that was wrong in
// production, so it must be testable without a database, a proxy or a network.
// judgeReach applies the rule above, discarding observers that are no longer
// connected.
//
// # An observation is only worth what its observer is worth RIGHT NOW
//
// Verdicts persist in the database, and a proxy that loses its tunnel leaves its
// last one behind. Read as current, that produces the most misleading state the
// product can show: a device reported GREEN, "seen by proxy-zezombini", by a
// proxy that has been gone for twelve minutes. Nobody is looking, and the screen
// says everything is fine.
//
// So liveness is applied before anything else. What remains after it decides the
// verdict; if nothing remains, the answer is "indeterminate" — not "up", which
// would be the stale claim, and not "down", which would blame a device for its
// observer's outage. The two failures need opposite actions: one sends someone to
// a switch, the other to the proxy host.
func judgeReach(obs []model.ProxyReach, live func(proxyID string) bool) reachVerdict {
	if live != nil {
		// CREDIBILITY, not just liveness.
		//
		// Being connected does not make a proxy a witness. An instance proxy is
		// handed devices on networks it has never been able to route to, and it
		// dutifully reports them down forever — so "a live proxy says down" is not
		// evidence when that proxy has never once seen the device.
		//
		// A proxy is credible for a device if it has EVER proven its identity or
		// EVER reached it. Observed 2026-08-02: proxy-akwalan, live and unable to
		// route to 172.16.99.0/24, turned a device red the moment its real
		// observer went offline — which is the same lie as the stale green, told
		// the other way round.
		var credible = func(o model.ProxyReach) bool { return o.Confirmed() || o.Up() }
		var connected []model.ProxyReach
		var offlineWitnesses []model.ProxyReach
		var o model.ProxyReach
		for _, o = range obs {
			if live(o.ProxyID) {
				connected = append(connected, o)
				continue
			}
			if credible(o) {
				offlineWitnesses = append(offlineWitnesses, o)
			}
		}
		// Does anything still connected actually know this device?
		var liveWitness bool
		for _, o = range connected {
			if credible(o) {
				liveWitness = true
			}
		}
		if !liveWitness && len(offlineWitnesses) > 0 {
			// NOBODY CAN SEE IT. The device may be perfectly healthy; what is
			// certainly broken is the path meshcraft watches it through, and the
			// note has to say which so the search starts in the right place.
			return reachVerdict{
				Status: "unknown",
				Note: "no proxy that can actually see this device is connected (" +
					proxyNames(offlineWitnesses) + "): its state is unknown, and this " +
					"is a PROXY outage until proven otherwise — the device itself may " +
					"be fine",
				Decider: offlineWitnesses[0].ProxyName,
			}
		}
		obs = connected
	}
	if len(obs) == 0 {
		// No proxy watches this device. Not "down" — nobody is looking, and
		// reporting an outage nobody observed would invent one.
		return reachVerdict{Status: "unknown", Note: "no proxy watches this device"}
	}

	var confirmedUp, confirmedDown, plainUp []model.ProxyReach
	var anyOpinion bool
	var o model.ProxyReach
	for _, o = range obs {
		if o.Reachable != nil {
			anyOpinion = true
		}
		switch {
		case o.Confirmed() && o.Up():
			confirmedUp = append(confirmedUp, o)
		case o.Confirmed() && o.Down():
			confirmedDown = append(confirmedDown, o)
		case o.Up():
			plainUp = append(plainUp, o)
		}
	}

	// 1. A proxy that proved the device's identity can see it. Nothing outranks
	//    this, and nothing needs to: it is the strongest statement available.
	if len(confirmedUp) > 0 {
		return reachVerdict{
			Status:    "ok",
			Note:      "seen by " + proxyNames(confirmedUp) + ", which confirmed its identity",
			Confirmed: true,
			Decider:   confirmedUp[0].ProxyName,
		}
	}

	// 2. Every proxy that knows this device has lost it. Unconfirmed proxies
	//    reporting up do NOT rescue it — an address answering on some other
	//    network is not this device.
	if len(confirmedDown) > 0 {
		return reachVerdict{
			Status:    "off",
			Note:      downNote(confirmedDown, plainUp),
			Confirmed: true,
			Decider:   confirmedDown[0].ProxyName,
		}
	}

	// 3. Nothing confirmed. OR across whoever is watching.
	if len(plainUp) > 0 {
		return reachVerdict{
			Status:  "ok",
			Note:    "seen by " + proxyNames(plainUp),
			Decider: plainUp[0].ProxyName,
		}
	}
	if !anyOpinion {
		// Watched, but nobody has concluded yet — a proxy that just connected.
		// Announcing an outage here is exactly the 3.7-second fault.
		return reachVerdict{Status: "unknown", Note: "no proxy has concluded yet"}
	}
	return reachVerdict{
		Status:  "off",
		Note:    "no longer responds to ping (" + proxyNames(obs) + ")",
		Decider: obs[0].ProxyName,
	}
}

// downNote explains a confirmed outage, and says when identity was last proven.
//
// The date is what separates "this switch is off" from "this collector is
// pointed at the wrong thing": a proxy that confirmed the device minutes ago and
// has now lost it is reporting a real outage, while one whose proof is weeks old
// deserves a second look before anyone is sent to a rack.
//
// Unconfirmed proxies still seeing SOMETHING at that address are named too. That
// is not noise — it is the single most useful line when an address has been
// recycled onto another machine, and it is invisible everywhere else.
func downNote(confirmedDown, plainUp []model.ProxyReach) string {
	var note = "no longer responds to ping (" + proxyNames(confirmedDown) + ")"
	var last = confirmedDown[0].ConfirmedAt
	if last != nil {
		note += ", which last confirmed its identity " + humanSince(*last)
	}
	if len(plainUp) > 0 {
		note += ". " + proxyNames(plainUp) + " still gets an answer at that address " +
			"but has never confirmed it is this device"
	}
	return note
}

// proxyNames renders the deciding proxies, in a form a note can carry.
//
// Bounded at three: a fleet where a dozen proxies watch one device would
// otherwise produce a note longer than the screen showing it, and the fourth name
// adds nothing the first three did not.
func proxyNames(obs []model.ProxyReach) string {
	var names = make([]string, 0, len(obs))
	var seen = map[string]bool{}
	var o model.ProxyReach
	for _, o = range obs {
		var n = o.ProxyName
		if n == "" {
			n = o.ProxyID
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + " and " + itoa(len(names)-3) + " others"
	}
	return strings.Join(names, ", ")
}

// humanSince renders an age the way the note reads best: coarse on purpose,
// because the question it answers is "recently, or long ago?".
func humanSince(t time.Time) string {
	var d = time.Since(t)
	switch {
	case d < 2*time.Minute:
		return "moments ago"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + " min ago"
	case d < 48*time.Hour:
		return itoa(int(d.Hours())) + " h ago"
	default:
		return itoa(int(d.Hours()/24)) + " days ago"
	}
}

// --- Identity: what lets a proxy's verdict outrank another's ---

// matchSNMPIdentity decides whether what the agent says matches the device we
// think we are polling, and returns WHAT matched.
//
// # Why the model, and only corroborated by the vendor
//
// sysDescr carries the model on every agent surveyed in this fleet ("CSS326-24G-2S+
// SwOS v2.17", "S3900-24T4S", "WS-C2960X-24TD-L ... C2960X-UNIVERSALK9-M"), and a
// model string is DISTINCTIVE — two different machines answering at one address
// will not both claim to be a CSS326. sysObjectID is an IANA enterprise number:
// excellent for the vendor, useless for telling two switches of the same vendor
// apart, which is the case that actually happens when an address is recycled.
// So the model decides and the vendor corroborates, never the reverse.
//
// # Deliberately lenient about HOW the model is written
//
// An inventory model is what discovery or an operator recorded ("HPE Networking
// Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A"); sysDescr is what the vendor
// chose to print. They will not be equal, and demanding equality would mean no
// device is ever confirmed — which fails toward the bug this exists to fix, since
// an unconfirmed fleet is exactly the fleet where last-writer-wins returns.
//
// The rule is therefore: at least one DISTINCTIVE token of the model — four or
// more characters, containing a digit — appears in sysDescr. "1930", "CSS326",
// "S3900", "C2960X" and "JL685A" all qualify; "Switch", "Networking" and "4p" do
// not, which is the point.
func matchSNMPIdentity(vendor, model, sysDescr, sysObjectID string) (string, bool) {
	var descr = normalizeIdentity(sysDescr)
	if descr == "" {
		return "", false
	}
	var matched []string
	var tok string
	for _, tok = range identityTokens(model) {
		if strings.Contains(descr, tok) {
			matched = append(matched, "sysDescr carries "+tok)
			break
		}
	}
	if len(matched) == 0 {
		// No model evidence. The vendor alone is NOT enough: it would confirm any
		// MikroTik at that address as being this MikroTik, which is precisely the
		// recycled-address case a confirmation is supposed to catch.
		return "", false
	}
	var enterprise = enterpriseVendor(sysObjectID)
	if enterprise != "" && vendorMatches(vendor, enterprise) {
		matched = append(matched, "sysObjectID is "+enterprise)
	}
	return strings.Join(matched, ", "), true
}

// identityTokens keeps the parts of a model string worth matching on.
//
// Four characters and at least one digit: long enough not to collide by accident,
// and a digit is what separates a product number from a marketing word. Returned
// normalized, so the caller compares like with like.
func identityTokens(model string) []string {
	var out []string
	var field string
	for _, field = range strings.FieldsFunc(model, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9')
	}) {
		if len(field) < 4 {
			continue
		}
		var hasDigit bool
		var i int
		for i = 0; i < len(field); i++ {
			if field[i] >= '0' && field[i] <= '9' {
				hasDigit = true
				break
			}
		}
		if !hasDigit {
			continue
		}
		out = append(out, normalizeIdentity(field))
	}
	return out
}

// normalizeIdentity folds case and drops everything that is not alphanumeric, so
// "CSS326-24G-2S+" and "CSS326 24G 2S" compare equal. Vendors punctuate the same
// model differently in sysDescr and in a datasheet, and that difference carries no
// information.
func normalizeIdentity(s string) string {
	var b strings.Builder
	var i int
	for i = 0; i < len(s); i++ {
		var c = s[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c)
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + 32)
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		}
	}
	return b.String()
}

// enterpriseVendor maps an sysObjectID enterprise arc onto a vendor name.
//
// SHORT ON PURPOSE, and it must stay a corroboration rather than a decision. Only
// entries that can be justified are here:
//
//   - 14988 MikroTik: OBSERVED on hardware, CSS326-24G-2S+ under SwOS 2.17
//     answering `1.3.6.1.4.1.14988.2` (cf. internal/snmppoll/health.go, which
//     notes it is .2 and NOT .1);
//   - 9 Cisco and 11 Hewlett-Packard: the two oldest assignments in the IANA
//     registry, stable since the 1980s.
//
// Everything else returns empty, which costs nothing: the model match in sysDescr
// is what confirms, and an unknown enterprise simply adds no second line to the
// reason. Guessing numbers for the rest of the fleet would put invented facts in a
// file whose whole purpose is deciding what to believe.
func enterpriseVendor(sysObjectID string) string {
	var arc = strings.TrimPrefix(strings.TrimPrefix(sysObjectID, "."), "1.3.6.1.4.1.")
	if arc == sysObjectID {
		return ""
	}
	var dot = strings.IndexByte(arc, '.')
	if dot > 0 {
		arc = arc[:dot]
	}
	switch arc {
	case "14988":
		return "MikroTik"
	case "9":
		return "Cisco"
	case "11":
		return "Hewlett-Packard"
	}
	return ""
}

// vendorMatches compares an inventory vendor with the enterprise's name.
//
// Substring either way, because the two vocabularies genuinely differ: the
// inventory says "Aruba (HPE)" where the registry says "Hewlett-Packard", and
// "FS.com" where the agent is an Accton OEM. A mismatch is not a failure — it only
// means the reason string carries one line instead of two.
func vendorMatches(inventory, enterprise string) bool {
	var a = normalizeIdentity(inventory)
	var b = normalizeIdentity(enterprise)
	if a == "" || b == "" {
		return false
	}
	return strings.Contains(a, b) || strings.Contains(b, a) ||
		// "HPE" and "Hewlett-Packard" share no substring, and are the same company.
		(strings.Contains(a, "hp") && strings.Contains(b, "hewlettpackard")) ||
		(strings.Contains(a, "hpe") && strings.Contains(b, "hewlettpackard"))
}
