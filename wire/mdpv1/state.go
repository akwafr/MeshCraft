package mdpv1

import "strings"

// The types the API layer exchanges with a connector: state read, the intent to
// set, and what a write produced.
//
// # They are SHARED because they already were, in three copies
//
// The three shipped connectors each carried their own `VLAN`, `PortState`,
// `State`, `ApplyResult`, and `Step`, and two of them said so explicitly in their
// comments — "same shape as the Instant On connector's, so the API has only one
// way to present a write," "this is what lets the reconciliation layer compare
// two platforms without telling them apart." Three identical definitions kept in
// sync by convention hold up only until the day one of them gains a field: that
// day, the API's silent conversion drops that field without any test noticing,
// and a configuration drift becomes invisible.
//
// The migration on the connector side costs nothing and does NOT require
// rewriting their code: an ALIAS is enough — `type PortState = driver.PortState`
// — and their fields, their pure functions, and their tests keep compiling as-is.
//
// # The JSON tags are the original ones, and this is not cosmetic
//
// Two of them cross a boundary Go does not watch over:
//
//   - `State` is serialized into `devices.observed` (via api.observedState) and
//     read back by everything that computes configuration drift. Renaming a tag
//     would make it read "zero VLANs, no ports" for the ENTIRE FLEET already
//     observed, which would look like a massive drift and trigger writes;
//   - `ApplyResult`, `SNMPResult`, `TrapResult`, and `Step` go into the HTTP
//     responses the frontend displays. A changed tag breaks nothing visibly
//     there: it simply displays emptiness, which is the worse of the two
//     failure modes.
//
// # What is NOT here, and why
//
// Nothing platform-specific: no `wcd` sections, no `*.b` pages, no RouterOS
// `.id`s. What is platform-specific but that the operator MUST know goes through
// the text fields planned for that purpose ([State.Notes], [ApplyResult.Skipped],
// [ApplyResult.Notes], [SNMPResult.Skipped], [TrapResult.Notes]) — sentences
// written by the connector, displayed as-is, without the API layer having to
// understand which platform they're talking about.

// VLAN is a VLAN declared on the device.
type VLAN struct {
	ID int `json:"id"`
	// Name is the label the device carries, when it carries one.
	//
	// DO NOT TREAT AS AN IDENTIFIER: RouterOS does not NAME its VLANs — its
	// bridge VLAN menu has no name field, and what stands in for one is a
	// free-text `comment` (observed: "added by pvid" on an entry the device
	// created entirely on its own). A connector reports back whatever it has; a
	// caller that compared names to decide on a write would be making a
	// mistake.
	Name string `json:"name"`
}

// PortState is the configuration of ONE port as the device carries it, or as
// meshcraft wants it.
//
// Deliberately limited to what is CONFIGURED. Rate, link state, PoE draw, and
// temperature are TELEMETRY: bringing them in here would make them get compared
// by the drift calculation, and the whole fleet would blink constantly.
//
// The same type serves both reads and intents, which is what makes diffing
// possible without translation — with one field's exception, flagged below.
type PortState struct {
	// Mode: access | trunk | empty. "empty" (and the zero value) both mean "not
	// managed": the connector leaves this port alone, which is the only way to
	// adopt a device without claiming all forty-eight of its jacks.
	Mode string `json:"mode"`
	// PVID is the native VLAN, the one whose traffic leaves WITHOUT a tag.
	PVID int `json:"pvid,omitempty"`
	// Tagged is the list of VLANs the port carries tagged, SORTED.
	//
	// Sorted because two runs must produce exactly the same request: otherwise
	// comparing two transcripts stops meaning anything, and a diff would report
	// a discrepancy on every re-read.
	Tagged []int `json:"tagged,omitempty"`
	// TaggedAtLeast: do not remove tagged VLANs on this port (unresolved "all").
	//
	// Set ONLY on intent, never on a read state. After ResolveTaggedAll, a normal
	// "all" carries the exact site VLAN list in Tagged and this flag is false —
	// apply adds and removes until the device matches. True only while the site
	// declares no VLANs yet: stripping tagged membership would be guessing.
	TaggedAtLeast bool `json:"tagged_at_least,omitempty"`
}

// State is the observed state of a device.
type State struct {
	VLANs []VLAN               `json:"vlans"`
	Ports map[string]PortState `json:"ports"`
	// MgmtVLAN is the VLAN through which the device says it is administrable,
	// as IT declares it — not as the site plan wants it to be.
	//
	// The distinction is vital, not cosmetic: a device not yet migrated to the
	// plan is reachable through ITS OWN VLAN. Protecting the plan's VLAN would
	// protect a VLAN the device isn't using, while letting the one carrying the
	// current session be overwritten. Observed on a 1930: plan says 99, device
	// says 1.
	//
	// ZERO DOES NOT MEAN THE SAME THING EVERYWHERE, and it's up to the connector
	// to translate it into a safeguard, not up to the caller to interpret it. On
	// Instant On, zero means ignorance and writes are refused. On SwOS, zero is
	// a LEGITIMATE state — "administration accepted from any VLAN," the state
	// of a device fresh from the factory — and refusing to write would prevent
	// adopting a new switch; protection then falls back to link state alone. On
	// RouterOS, zero means administration is not arriving through a tagged
	// VLAN.
	MgmtVLAN int `json:"mgmt_vlan"`
	// Links: the OPERATIONAL STATE of each port, richer than a link/no-link bool.
	//
	// The previous model was `LinkUp map[string]bool`, and it conflated three
	// facts that call for opposite reactions: a port with nothing plugged in, a
	// port an operator shut on purpose, and a port the SWITCH shut itself
	// (err-disabled — a refused optic, a bpduguard trip, a security violation).
	// All three read as "no link" and only one is a problem. This carries the
	// distinction the connectors already observe.
	//
	// [LinkUp] and [HasLink] preserve the old meaning for the apply guardrail:
	// only a port that is genuinely UP can carry the current administration
	// session, and that is what allows reconfiguring a brand-new fleet where
	// every port is untagged in the management VLAN. A missing entry means "not
	// read / unknown", the cautious default.
	Links map[string]LinkState `json:"links,omitempty"`
	// LAGs: the device's link aggregates, in the standard vocabulary
	// (cf. lag.go). Nil means the connector does not read them — which every
	// screen must keep distinct from "the device has none".
	LAGs []LAGState `json:"lags,omitempty"`
	// Notes carries what the connector understood that this type cannot
	// otherwise express.
	//
	// THE FIELD THAT AVOIDS LYING, and it was born from a fact observed on the
	// two newer platforms: a VLAN configuration can be PRESENT and INERT. On
	// SwOS, a port whose VLAN mode is "disabled" filters nothing — its PVID and
	// membership are configured but have no effect on traffic. On RouterOS, a
	// bridge whose `vlan-filtering` is off lets everything through everywhere,
	// and a port can tag VLAN 100 without being a member of VLAN 100 (observed
	// on the reference device, perfectly accepted and silent).
	//
	// Without this field, the UI would present these cases as segmentation
	// actually in place. These are SENTENCES for an operator, not codes: the
	// caller displays them, and draws no decision from them — a connector that
	// wants a decision to be made must refuse on its own.
	Notes []string `json:"notes,omitempty"`
}

// LinksFromUp converts an "up ports" boolean map — the shape several connectors
// build internally — into the shared [LinkState] vocabulary. Only ports present
// and true become [LinkUpState]; everything else is simply absent, meaning
// "unknown / no link", exactly as the old boolean map behaved. A platform that
// can distinguish disabled from unplugged builds the richer map directly instead.
func LinksFromUp(up map[string]bool) map[string]LinkState {
	if up == nil {
		return nil
	}
	var out = map[string]LinkState{}
	var name string
	var v bool
	for name, v = range up {
		if v {
			out[name] = LinkUpState
		}
	}
	return out
}

// LinkState is a port's operational state, as the fleet's switches actually
// report it. Deliberately a small, closed vocabulary shared by every connector:
// a platform that cannot tell two of these apart reports the coarser one rather
// than inventing a distinction it did not observe.
type LinkState string

const (
	// LinkUpState: a forwarding link is present. The only state that carries an
	// administration session, and therefore the only one the apply guardrail
	// treats as "do not cut".
	LinkUpState LinkState = "up"
	// LinkDown: the port is down for a reason the connector could not narrow to
	// one of the more specific states below. The honest fallback.
	LinkDown LinkState = "down"
	// LinkDisabled: administratively shut ("shutdown", "disable"). Down
	// because someone chose it, not because anything is wrong.
	LinkDisabled LinkState = "disabled"
	// LinkUnplugged: no cable / no signal ("notconnect"). Down because nothing
	// is connected — not a fault, and not a configuration to worry about.
	LinkUnplugged LinkState = "unplugged"
	// LinkErrDisabled: the SWITCH shut the port itself. A refused transceiver, a
	// bpduguard trip, a port-security violation — always something to look at,
	// and the one state that must be surfaced on the interface rather than
	// hidden inside "no link".
	LinkErrDisabled LinkState = "err-disabled"
)

// HasLink reports whether a port carries a usable link — the apply guardrail's
// question, and the exact meaning the old `LinkUp[name]` map carried. True only
// for [LinkUpState]: a disabled, unplugged or err-disabled port carries no
// session and may be reconfigured.
func (s State) HasLink(name string) bool {
	return s.Links[name] == LinkUpState
}

// LinkUp rebuilds the old boolean map, for the persisted DTO and for callers
// that only need "does this port have a link". One entry per UP port, matching
// the previous field's semantics exactly so `devices.observed` and conformity
// checks keep reading the same thing.
func (s State) LinkUp() map[string]bool {
	if s.Links == nil {
		return nil
	}
	var out = map[string]bool{}
	var name string
	var st LinkState
	for name, st = range s.Links {
		if st == LinkUpState {
			out[name] = true
		}
	}
	return out
}

// PortPlan is what a configuration write must achieve.
//
// An aggregate rather than five parameters, and not just for readability: the
// day the write needs one more constraint, the three connectors change their
// signature together or not at all — a parameter added in three places gets
// forgotten at the third, and that platform's write would set something other
// than what the UI advertises.
type PortPlan struct {
	// Desired is the wanted configuration, keyed by PORT NAME in the standard
	// model.
	//
	// A port ABSENT from this map is not "to be cleared": it is NOT MANAGED,
	// and the connector leaves it alone. Same rule as an empty `Mode`, and it's
	// what allows adopting a device on which only three jacks are configured.
	Desired map[string]PortState
	// MgmtVLAN is the management VLAN the SITE PLAN wants.
	//
	// The connector ADDS to it the one the device declares ([State.MgmtVLAN])
	// and protects both: they differ as long as a device hasn't been migrated
	// yet, and protecting only the first would let the one carrying the
	// session get overwritten.
	//
	// Zero means "the site has no actionable plan." This is not authorization
	// to write blindly: it's up to the connector to decide whether its
	// platform can still protect itself some other way (cf. [State.MgmtVLAN]).
	MgmtVLAN int
	// SiteVLANs are the VLANs the site declares, which must EXIST on the
	// device even if no port uses them yet.
	//
	// This isn't overzealousness: an adopted device must know its site's
	// VLANs, or assigning a port to one from the device's own interface — or
	// plugging a trunk into it — would offer nothing and carry nothing. This
	// is the difference between a device that is "adopted" and one that is
	// "merely reachable."
	SiteVLANs []VLAN
	// Force lifts the safeguards that protect ports carrying administration.
	//
	// NEVER enabled by default, nor by an automatic write: it's an explicit
	// gesture by the operator, who knows their topology better than meshcraft
	// and accepts the risk of having to travel on site. A connector that
	// ignored it would make the UI's "force" button silently useless; a
	// connector that forced without being asked would have no safeguard left
	// at all.
	Force bool
}

// ApplyResult says what a write did, and above all what it did NOT do.
type ApplyResult struct {
	// VLANsCreated: the VLANs that had to be created before ports could be
	// assigned to them.
	VLANsCreated []int `json:"vlans_created"`
	// PortsWritten: the ports actually reconfigured. A port already compliant
	// does not appear here — sending nothing is better than a round trip that
	// changes nothing.
	//
	// IT MEANS CONFIRMED, NEVER SENT. This is the one reading of the field, it
	// is normative for every connector, and [ApplyResult.Unconfirmed] is how a
	// connector says the other thing. Settled 2026-08-09, after a survey found
	// the nine connectors reading it three different ways on the same event —
	// a verifying re-read that FAILED (§ below).
	//
	// # What "confirmed" means, precisely
	//
	// The connector asked the device afterwards, got an answer, and that answer
	// agrees with what was asked. Three things are therefore NOT confirmations
	// and must not put a name in this list:
	//
	//   - a return code, an HTTP 2xx, or a CLI that did not complain. Every
	//     applier in this tree re-reads for exactly this reason, and the file
	//     headers say so in the same words: "a 2xx that changed nothing is what
	//     this re-read exists to catch";
	//   - a re-read that DISAGREED. That port goes to [ApplyResult.NotApplied]
	//     through [DeviceDeclined] — the device said no, and no meshcraft-side
	//     force will change its answer;
	//   - a re-read that could not be TAKEN at all. Nothing was observed, so
	//     nothing is confirmed: cf. [ApplyResult.Unconfirmed].
	//
	// # Why confirmed and not sent
	//
	// Because of what is downstream. `api.rememberApply` stores this list, the
	// apply event line reads "N port(s) written", and `pipelineForApply` labels a
	// run "Configuration applied on X (N port(s))" — all of them read months
	// later as statements of fact about a device. The frontend, checked the day
	// this was settled, reads `last_apply.skipped`, `.notes`, `.unsupported` and
	// `.error` and does NOT read `ports_written` at all, so nothing on screen
	// depended on the looser reading and nothing had to be traded away for the
	// stricter one.
	//
	// The precedent is `ubiquiti/edgeswitch.verifyPoEClasses`, which argued this
	// out first and named the cost of the other reading: an ApplyResult that
	// contradicts itself, listing the same port as written AND as skipped, and a
	// failed re-read reporting EVERY port as written while skipping every one of
	// them.
	//
	// # WHERE THE RULE IS NOT YET MET, named rather than left to be discovered
	//
	// The sweep of 2026-08-09 covered every applier that TAKES a verifying
	// re-read: DHCP snooping, storm control, port administrative state and PoE,
	// on all nine connectors. Two families are not there yet, and neither is an
	// exception to the rule — they are debt against it (TODO.md):
	//
	//   - ApplyPorts (VLAN membership) fills this list from what the device
	//     accepted. Its verification is the RECONCILIATION pass, which re-reads
	//     the whole state later and reports drift, so the fact is established —
	//     just not by the apply, and not in time for this field;
	//   - ApplySpanningTree on eos, fs/s3400 and hpe/procurve verifies the bridge
	//     level and never the per-port one, so its PortsWritten is CLI acceptance
	//     for every port in every case, not only after a failed re-read. Closing
	//     it means reading cost/edge/BPDU back per port on three platforms.
	PortsWritten []string `json:"ports_written"`
	// Skipped: what meshcraft REFUSED to do, with the reason — safety
	// decisions the operator can lift by forcing (an administration port,
	// etc.).
	//
	// THE MOST IMPORTANT FIELD OF THE STRUCT: a partial apply presented as
	// complete would suggest a configuration that isn't actually in place.
	//
	// TRANSITIONAL since the contract hardening pass: the structured form of
	// this information is [NotApplied], and this field is the remainder that
	// has not been migrated yet — free sentences, port unstated, treated as
	// guardrails by [ApplyResult.AllNotApplied] because a force button that
	// changes nothing is a lesser lie than a refusal presented as final when
	// it is not. Each driver's entries move to [NotApplied] as it migrates
	// out of the tree; new code must not append here.
	//
	// Not to be confused with Notes (facts with no decision behind them: lost
	// offload, etc.) nor with what the platform cannot WRITE at all
	// (cf. [Connector.WritesExtra]): mixing them up would offer a "force"
	// button that would change nothing.
	Skipped []string `json:"skipped,omitempty"`
	// NotApplied: every intent this apply did not land, as STRUCTURED entries
	// — which port, which kind of "no", and the connector's sentence.
	//
	// The kind is the load-bearing half: a guardrail is an invitation to
	// decide (lift it by forcing, or not), a platform refusal or a device
	// decline is a fact to plan around (another class, another unit, another
	// switch), and no force will change it. Over the wire the distinction is
	// the only way an external driver's caller can know whether Force is
	// worth offering; the port being a field rather than a prose prefix is
	// what lets a screen attach the entry to the socket it concerns.
	//
	// Built through [Guardrail], [PlatformRefusal] and [DeviceDeclined], so a
	// connector never invents a kind string. [Skipped] is the not-yet-migrated
	// remainder; [ApplyResult.AllNotApplied] merges both views for callers.
	NotApplied []NotApplied `json:"not_applied,omitempty"`
	// Notes: apply facts that need to be stated, with no decision attached.
	Notes []string `json:"notes,omitempty"`
	// Backup is the device's configuration BEFORE the write, exactly as the
	// platform returned it, or empty.
	//
	// Exists for one platform and one reason: SwOS applies and persists in a
	// single gesture, a power outage undoes nothing, and this copy is the ONLY
	// way back that exists. The connector takes it right before the first
	// write; it cannot be reconstructed afterward.
	//
	// Empty is not a gap: on a platform running `apply_then_save`, the safety
	// net is elsewhere — not persisting (cf. [Persister]). What the caller
	// does with it is the caller's responsibility, but letting it drop
	// silently would amount to advertising a safety net that was thrown away.
	Backup string `json:"backup,omitempty"`
}

// NotApplied is ONE intent an apply did not land: which port, which kind of
// "no", and the connector's sentence. See [ApplyResult.NotApplied].
type NotApplied struct {
	// Port is the socket the entry concerns, in the connector's own naming, or
	// empty when the fact is device-wide (a global feature, a plan-shape
	// refusal).
	Port string `json:"port,omitempty"`
	// Kind is one of [KindGuardrail], [KindPlatform], [KindDevice] — built
	// through the constructors below, never assembled by hand, so no connector
	// can invent a fourth kind the callers would silently drop.
	Kind string `json:"kind"`
	// Text is the connector's sentence, displayed as-is.
	Text string `json:"text"`
}

const (
	// KindGuardrail: meshcraft's own safety decision, liftable by forcing.
	KindGuardrail = "guardrail"
	// KindPlatform: the platform does not have the knob. No force changes it.
	KindPlatform = "platform"
	// KindDevice: the device itself declined the write on the verifying
	// re-read. No meshcraft-side force changes its answer.
	KindDevice = "device"
)

// Guardrail records a safety decision the operator can lift by forcing.
func Guardrail(port, text string) NotApplied {
	return NotApplied{Port: port, Kind: KindGuardrail, Text: text}
}

// PlatformRefusal records a knob this platform does not have.
func PlatformRefusal(port, text string) NotApplied {
	return NotApplied{Port: port, Kind: KindPlatform, Text: text}
}

// DeviceDeclined records a write the device itself rejected on re-read.
func DeviceDeclined(port, text string) NotApplied {
	return NotApplied{Port: port, Kind: KindDevice, Text: text}
}

// Forceable says whether a forced retry can land this entry.
func (n NotApplied) Forceable() bool { return n.Kind == KindGuardrail }

// AllNotApplied merges the structured entries with the not-yet-migrated
// [ApplyResult.Skipped] remainder, which reads as guardrails with no port —
// the lesser lie, as the Skipped doc says. Callers that count, display or
// serialize "what did not land" use THIS, so a driver's migration to the
// structured field changes nothing for them.
func (r ApplyResult) AllNotApplied() []NotApplied {
	var out = make([]NotApplied, 0, len(r.NotApplied)+len(r.Skipped))
	out = append(out, r.NotApplied...)
	var s string
	for _, s = range r.Skipped {
		out = append(out, Guardrail("", s))
	}
	return out
}

// Unconfirmed records that the verifying re-read could not be TAKEN, and is the
// only thing a connector may do with the ports it had sent when that happens.
//
// It appends one Note naming them and DOES NOT TOUCH [ApplyResult.PortsWritten]
// — which is the whole point, and the reason this is a method on the result
// rather than a sentence each connector writes for itself. Nine connectors
// phrased that sentence nine ways and six of them then filled PortsWritten from
// the sent list underneath it, so the prose said "none of this was confirmed"
// while the field said "all of it was written". A caller reading the field got
// the opposite of what a caller reading the note got.
//
// # Why a Note and not Skipped
//
// [ApplyResult.NotApplied] holds liftable guardrails and answers that will not
// change, and a failed re-read is neither: the commands went out and the
// device may well be carrying them. A guardrail entry would offer a "force"
// button for a network hiccup; a refusal entry would present a hiccup as a
// verdict. It is a fact with no decision attached, which is the definition of
// Notes.
//
// # Why the ports are NAMED
//
// Emptying PortsWritten without naming them would lose the only record of what
// went out, and on a platform that persists immediately (SwOS, RouterOS) that
// record is what an operator needs to know where a half-applied list stopped.
// The note is also the half the frontend actually shows (ConnectorNotesBanner),
// so naming them here puts the information on screen instead of in a field
// nothing reads.
//
// `what` names the setting in the connector's own words ("DHCP snooping trust",
// "storm control", "administrative state"), `sent` are the ports whose commands
// the device accepted, and `why` is the re-read's own failure. A call with no
// ports still writes the note: a gesture whose global half went out and could
// not be checked is worth the same sentence.
func (r *ApplyResult) Unconfirmed(what string, sent []string, why string) {
	var note = what + " was SENT"
	if len(sent) > 0 {
		note += " on " + strings.Join(sent, ", ")
	}
	note += " and NOT confirmed: the verifying re-read failed (" + why + "). Nothing " +
		"here is reported as written, because a return code is not an observation — the " +
		"commands were accepted, so re-read the device before sending again: this may be " +
		"a failed verification rather than a failed write."
	r.Notes = append(r.Notes, note)
}

// --- SNMP ---

// SNMPIdentity is the SNMP identity to set, as `snmpprov` decided it.
//
// The halves are INDEPENDENT: an empty field means "nothing to do here", never
// "remove". Setting is not a factory reset, and a connector that erased what it
// was not asked to set would cut off a third party's monitoring.
type SNMPIdentity struct {
	// Community: the v2c community, ALWAYS applied READ-ONLY. Empty = no v2c.
	//
	// meshcraft has no way to ask for a writable one, and that is deliberate: an RW
	// community on a fleet is a root password in clear on the network. It writes
	// NOTHING over SNMP anyway — all of its configuration goes through the
	// connector.
	Community string
	// User: the v3 user. Empty = no v3. Exclusive with Community in practice,
	// without the contract imposing it: the plan decides, and a platform may
	// legitimately carry both.
	User string
	// AuthAlg / PrivAlg: the algorithm names, in any form ("SHA-1", "sha", "DES").
	// The connector normalises them and REFUSES what its platform does not have —
	// never the "closest".
	AuthAlg string
	PrivAlg string
	// AuthSecret / PrivSecret: the passphrases the device will derive its keys from.
	//
	// TWO DISTINCT phrases, and that is not a stylistic precaution: reusing the same
	// on both sides feeds exactly the same value into both RFC 3414 derivations, so
	// that the encryption key is no more than a prefix of the authentication key —
	// encryption then adds nothing. Without a second phrase, v3 is NOT SET.
	//
	// They travel IN CLEAR in the request body, as these devices' own interfaces do.
	// Confidentiality relies on TLS when there is some, and on the proxy tunnel
	// always.
	AuthSecret string
	PrivSecret string
	// Group: the v3 access group. Empty = the connector's default.
	Group string
	// Sources bounds the addresses allowed to poll the agent, as the platform
	// expresses them (a list of prefixes).
	//
	// A FIELD BORN OF A ROUTEROS REFUSAL, and it must be read as such: on that
	// platform, a community carries an `addresses` field and the connector REFUSES
	// to leave it empty — "leaving SNMP open to every address must be requested
	// explicitly". The observed state shows `::/0` on the reference device, hence
	// EVERY source, including outside the management VLAN. It is a setting, not a
	// constraint, and leaving it open is a choice an operator should make
	// knowingly.
	//
	// The difference in nature with the Instant On is instructive: that product
	// line confines SNMP to the administration VLAN by construction, there is
	// nothing to bound. A connector whose platform lacks this setting IGNORES it.
	//
	// EMPTY MEANS "THE CALLER SAID NOTHING", and above all not "all". A connector
	// that cannot set without a bound must refuse, naming the missing gesture,
	// rather than invent a default nobody chose.
	Sources []string
}

// SNMPResult says what was done, and above all what was not.
type SNMPResult struct {
	// Enabled: the SNMP agent is active at the end of the operation.
	//
	// Setting an identity on an agent that is off would give a configured and
	// silent monitoring, the worst of both worlds: nothing answers, and nothing
	// says why. The connector therefore turns it on along the way.
	Enabled bool `json:"enabled"`
	// CommunitySet / UserSet / GroupCreated: a write did take place AND was
	// verified. FALSE when the device was already conformant — passing again over
	// an already qualified fleet must rewrite nothing.
	CommunitySet bool `json:"community_set"`
	UserSet      bool `json:"user_set"`
	GroupCreated bool `json:"group_created"`
	// Skipped: what meshcraft REFUSED to do, with the reason.
	Skipped []string `json:"skipped,omitempty"`
}

// --- Trap destination ---

// TrapDestination is the recipient to declare: where the collector listens,
// and under which identity the device must announce itself.
type TrapDestination struct {
	// Address is the collector's address, i.e. the proxy's.
	//
	// IT IS ASKED FOR, NOT DEDUCED, and that is the caller's responsibility: only
	// the proxy's kernel has the routing table that says through which address it
	// reaches this device. Looking, among the addresses the proxy declares, for the
	// one in the device's subnet is wrong as soon as it reaches it through routing
	// — observed on the lab fleet, where none of the proxy's addresses is on the
	// device's network.
	//
	// WORTH NOTING, because it is paid for dearly: the SOURCE address of traps is
	// generally not a choice. On the Instant On there is no SNMP `source-interface`
	// and only one usable IP interface — traps leave from the management address.
	// Moving the management VLAN therefore changes the sender as seen by the proxy
	// and silently invalidates its watch list.
	Address string
	// Port: 0 means the standard port 162.
	//
	// Configurable because the platforms allow it (observed on the Instant On: an
	// ordinary input validated over 1–65535), which spares the proxy from listening
	// on a privileged port. A connector whose platform lacks this field REFUSES a
	// value other than 0 or 162 rather than ignoring it: accepting a port it will
	// not write would make someone wait for traps on a silent port.
	Port int
	// Community: the community v2c traps will be marked with. Exclusive with
	// User.
	//
	// A CONSTRAINT THAT COMES FROM THE COLLECTOR, NOT FROM THE DEVICE: the proxy
	// accepts a trap only if the community it carries equals the one it POLLS the
	// device with. A distinct trap community, even a dedicated, "clean" one, would
	// get 100 % of the fleet's traps rejected — silently, with a DEBUG counter on
	// the proxy as the only witness. The devices do not require it: on the Instant
	// On the field is free text the firmware matches against no entry of its
	// community list.
	Community string
	// User: the SNMPv3 user to sign as. Exclusive with Community.
	//
	// It must EXIST on the device, and it is up to the connector to verify it
	// because the firmware does not: the field is free text it checks against
	// nothing. A misnamed v3 recipient registers without error and emits nothing
	// authenticable — a silence no UI can tell apart from a quiet network.
	User string
}

// TrapResult says what was done, and what could not be verified.
type TrapResult struct {
	// Set: a write took place AND was confirmed by a read-back. False when the
	// device already carried the right recipient.
	Set bool `json:"set"`
	// Version : "v2c" ou "v3".
	Version string `json:"version"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	// Replaced: a diverging recipient with the same address was removed before
	// setting. Concerns platforms whose identity is part of the entry KEY: a `set`
	// carrying another name would add a line there instead of replacing the one in
	// place, and the old one — inert — would keep occupying a slot in a table
	// whose ceiling is unknown.
	Replaced bool `json:"replaced"`
	// Known: how many recipients the device carried BEFORE the write.
	//
	// This is not statistics. The number of recipients these product lines accept
	// is documented nowhere, and the firmware only enforces it on ADDITION: this
	// counter is the only measure we will ever get of it — that of the last
	// accepted addition, and that of the first refused one.
	Known int `json:"known"`
	// Notes: what the connector could NOT verify.
	//
	// A recipient applied on a device whose trap emission may be switched off is
	// not monitoring: saying so is better than letting it be believed. This is
	// also where what is specific to a platform and what the operator must know
	// goes — two distinct tables on the Instant On, a trap generator limited to
	// temperature alerts on RouterOS.
	Notes []string `json:"notes,omitempty"`
}

// --- Identification ---

// Identity is what the connector could establish about the device at an address.
//
// PARTIAL BY CONSTRUCTION, and that is the point to remember: a connector
// returns what it COULD read even when it stopped on the way, and the
// accompanying error says why. "It is a CSS326-24G-2S+ whose password we do not
// have" is a useful result; "failure" is not.
//
// Platform-specific fields do not belong here — boot loader version, board
// revision, index of the first optical cage. Connectors keep their own,
// richer identity type, and fill this one for the API layer. That is the
// direction of flow: the contract carries what the CALLER consumes.
type Identity struct {
	// Model is the vendor reference, as the device names itself.
	Model string `json:"model"`
	// ModelCode is the catalogue reference when it differs from the label ("JL685A").
	ModelCode string `json:"model_code,omitempty"`
	// Family is the sub-line ("1930", "CSS"), useful for capability differences
	// that play out at that level.
	Family string `json:"family,omitempty"`
	// Serial is the serial number. IT IS THE ONLY FIELD THAT IS AUTHORITATIVE FOR
	// RECOGNISING A DEVICE AT ANOTHER ADDRESS: relocation keeps an address only if
	// the serial matches, otherwise meshcraft would point at another device.
	Serial   string `json:"serial,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Build    string `json:"build,omitempty"`
	// BootFirmware is the BOOT LOADER's own version, when the platform versions
	// it separately from the system (RouterBOOT on MikroTik). Kept apart from
	// Firmware on purpose: merging them once made a device look four major
	// versions behind, and the operator reading the note needs to know WHICH of
	// the two numbers is out of date.
	BootFirmware string `json:"boot_firmware,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	// Facade: what lets the device be drawn without a model catalogue.
	//
	// At adoption, a device starts blank in the model — but its ports EXIST.
	// Without taking over the observed front panel, adoption produced a device WITH
	// NO PORT AT ALL, which is not "blank" but wrong.
	CopperPorts int  `json:"copper_ports,omitempty"`
	SFPPorts    int  `json:"sfp_ports,omitempty"`
	SFPPlus     bool `json:"sfp_plus,omitempty"`
	PoE         bool `json:"poe,omitempty"`
	// PortGroups is the front panel in the shape the frontend already consumes
	// (`prefix`, `count`, `from`, `speed`, `poe`, `label`).
	//
	// Returned by the connector and copied without translation, deliberately:
	// numbering is platform knowledge. On the Instant On, the optical cages continue
	// the copper numbering (48 ports → cages 49 to 52); on RouterOS, meshcraft's
	// names ARE the device's names, so the groups carry several prefixes (`ether`,
	// `sfp-sfpplus`) and are checked to be CONTIGUOUS — announcing "24 ports from 1"
	// when one is missing in the middle would draw a jack that does not exist, and
	// meshcraft would end up writing to it.
	PortGroups []map[string]any `json:"port_groups,omitempty"`
	// Source says WHERE the information comes from: the UI must not present a
	// deduction as a reading. The values are the connector's (`login_page`,
	// `provisioning_api`, `digest_realm`, `rest_api`), and their heterogeneity is
	// intended — it is the only way a sceptic can find the observation again.
	Source string `json:"source,omitempty"`
}

// OnboardResult is what a switch to local administration produced.
//
// What matters is not the announced success but the BEFORE / AFTER comparison:
// a 2xx that changes nothing is a silent failure, and that is the failure mode
// we are trying to catch.
type OnboardResult struct {
	// Was / Stage: the management mode's state before and after, in the
	// connector's words.
	Was   string `json:"was"`
	Stage string `json:"stage"`
	// Changed: the two differ. That, and nothing else, proves the gesture took
	// effect.
	Changed bool `json:"changed"`
	// Identity is what we learned about the device along the way. The switch reads
	// its state anyway; returning it saves a second round trip at the precise
	// moment adoption needs the model and the serial.
	Identity Identity `json:"identity"`
	// Detail is the device's response, VERBATIM.
	//
	// On a platform whose write gesture was reconstructed and not documented, it is
	// the only thing that allows a refusal to be understood without going back on
	// site. Returned as is rather than interpreted: interpreting it would require
	// knowing the possible responses in advance, which is exactly what we do not
	// have.
	Detail string `json:"detail,omitempty"`
	// Steps is the transcript of the gesture. A switch-over is a durable WRITE — on
	// the known product line, only a factory reset undoes it — and it deserves the
	// same trace as a configuration push.
	Steps []Step `json:"steps,omitempty"`
}
