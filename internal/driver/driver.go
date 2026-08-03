// Package driver defines the contract for platform connectors and the registry
// that tracks them.
//
// A connector knows how to talk to ONE platform: it translates the standard data
// model into what the device understands, and back again on reads. See
// docs/01-Architecture/Connecteurs.
//
// This package knows neither SSH nor HTTP: a connector receives a Session whose
// transport is already established — directly, or through a [[Proxy]] tunnel — and
// has no notion of a tunnel of its own. This is the "single code path" rule.
package driver

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrNotImplemented: the capability exists in the contract but not yet in this
// connector. Saying so explicitly is better than an empty behavior that would
// suggest success.
var ErrNotImplemented = errors.New("not implemented by this connector")

// Info identifies the connector.
type Info struct {
	// Name is the stable identifier used in the database and in the API.
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
	// Family describes the range covered, as an operator would name it.
	Family string `json:"family"`
	// Transport: what the device is actually administered through.
	Transport []string `json:"transport"` // web | ssh | telnet | snmp | api
	// Doc: the vendor reference the capabilities come from.
	Doc string `json:"doc,omitempty"`
}

// VlanSupport says which VLANs the administration interface accepts.
type VlanSupport string

const (
	// VlanAny: the management VLAN is freely configurable.
	VlanAny VlanSupport = "any"
	// VlanDefaultOnly: management on the default VLAN (1) only.
	VlanDefaultOnly VlanSupport = "default_only"
	// VlanUnknown: it isn't known — so the worst case is assumed (cf. the rule
	// "an unknown capability is assumed absent").
	VlanUnknown VlanSupport = ""
)

// MgmtCapabilities describes what the administration interface accepts. This is
// the most dangerous capability to get wrong: a mistake here makes the device
// unreachable (cf. docs/01-Architecture/Adressage de management).
type MgmtCapabilities struct {
	VlanIDs VlanSupport `json:"vlan_ids"`
	// VlanRange bounds the IDs accepted for management.
	VlanRange [2]int `json:"vlan_range"`
	// TaggedOK: management works on a tagged VLAN.
	TaggedOK bool `json:"tagged_ok"`
	// Static / DHCP: addressing modes accepted.
	Static bool `json:"static"`
	DHCP   bool `json:"dhcp"`
	// Gateway / DNS: does the platform carry a GATEWAY and RESOLVERS for its
	// administration?
	//
	// These two fields are not decorative, and above all they cannot be deduced
	// from `Static`. The observation that gave rise to them: the writable field
	// set of /sys.b on a CSS326-24G-2S+ — enumerated from its own configuration
	// backup, and therefore exhaustive — does contain `ip` and `iptp` but NO
	// gateway and NO resolver at all. Without a gateway, outbound traffic leaving
	// the subnet (traps, DNS) has no route here; inbound administration can still
	// answer at L2. This is an operational fact, not a shortcoming.
	//
	// FALSE BY DEFAULT, like everything touching access. A UI that offers three
	// input fields on a platform that only carries one lets an operator enter a
	// gateway that will land nowhere, and the operator concludes that meshcraft
	// failed to write what was requested. Saying "this platform has no
	// management gateway" is information; showing an empty field is not.
	Gateway bool `json:"gateway"`
	DNS     bool `json:"dns"`
	// Routed: can management leave its own subnet (default gateway / L3)?
	//
	// Distinct from [Gateway] only in wording for the UI: Gateway is "does the
	// platform expose a gateway field?", Routed is the exploitation fact
	// "off-subnet outbound has a route". Today they match on every shipped
	// connector; both stay so the screen can say MgmtRouted: no without prose.
	Routed bool `json:"routed"`
	// IPARP: the device still answers ARP / L2 for its management IP when it has
	// no gateway — inbound admin from another subnet can work if an L2 return
	// path exists. SwOS is the reference: no gateway, but L2 reply still works.
	IPARP bool `json:"ip_arp"`
	// DHCPFallback: fallback address if DHCP doesn't answer.
	DHCPFallback string `json:"dhcp_fallback,omitempty"`
	// OutOfBand: an administration path exists OUTSIDE THE NETWORK — serial
	// console, MAC-winbox, a dedicated management port — through which a device
	// left mute by an addressing mistake can be recovered.
	//
	// THE FIELD THAT DECIDES SEVERITY, and it was born from a FALSE message. The
	// API layer used to refuse a management-VLAN switch by claiming "this range
	// has neither SSH nor a serial port: the only recourse would be a factory
	// reset, on site." That was true of the two web-only ranges shipped, and false
	// of RouterOS — which has a serial console AND MAC-winbox, both OBSERVED. A
	// whole refusal message was therefore calibrated on an unrecoverable platform,
	// and recited verbatim to a platform that can, in fact, be recovered.
	//
	// DO NOT DEDUCE FROM [Info.Transport]: that list says what meshcraft
	// administers through, not what's left when the network is lost. A connector
	// can speak web to a device that also has a serial port (RouterOS), and
	// another can advertise `ssh` on a platform whose SSH runs over the same VLAN
	// as everything else — and therefore goes down with it. Both mistakes point in
	// the dangerous direction.
	//
	// FALSE BY DEFAULT, and this is the project rule applied to its most costly
	// case: an unknown capability is assumed ABSENT. Getting it wrong in this
	// direction produces a warning that is too harsh, which costs some caution;
	// getting it wrong the other way risks attempting an irreversible maneuver on
	// a device believed recoverable, which costs a site visit with a paperclip.
	OutOfBand bool `json:"out_of_band"`
	// ApplySameVLAN: the connector has proven [MgmtAddrWriter] on hardware for
	// same-VLAN DHCP/static/gateway/DNS apply. FALSE by default until proven.
	ApplySameVLAN bool `json:"apply_same_vlan"`
	// MigrateVLAN: the connector implements [MgmtMover] (dual-path prepare /
	// addressed / commit). Instant On is the reference implementation.
	MigrateVLAN bool `json:"migrate_vlan"`
	// JumpVLAN: the connector implements [MgmtJumper] — honest single-address leap
	// with backup. SwOS-shaped; never set together with MigrateVLAN as a substitute
	// for dual-path on a platform that can do both.
	JumpVLAN bool `json:"jump_vlan"`
	// Notes carries the ORDERING constraints and pitfalls documented by the
	// vendor — the difference between a write that works and a device that's
	// lost.
	Notes []string `json:"notes,omitempty"`
}

// AdminAccount describes what the platform accepts as an administration ACCOUNT.
// meshcraft wants to set the same name everywhere (`mcadmin`) so that direct
// access is predictable; many entry-range switches only accept `admin`. This
// capability is what prevents pushing a name the device will refuse — and then
// believing it was accepted.
type AdminAccount struct {
	// Custom: the platform can create an account under a chosen name.
	Custom bool `json:"custom"`
	// FixedName: the only possible name when Custom is false.
	FixedName string `json:"fixed_name,omitempty"`
	// PasswordMin / PasswordMax: bounds imposed by the device. 0 = unknown.
	PasswordMin int `json:"password_min,omitempty"`
	PasswordMax int `json:"password_max,omitempty"`
	// AlphanumericOnly: the device rejects punctuation. This is one of the
	// reasons meshcraft only generates alphanumeric passwords
	// (cf. internal/pwgen).
	AlphanumericOnly bool `json:"alphanumeric_only,omitempty"`
	// Notes carries what doesn't fit in code: operation ordering, pitfalls.
	Notes []string `json:"notes,omitempty"`
}

// MonitorChannel: a collection channel, with its polling-rate floor.
//
// MinInterval is a FLOOR, not a target: "never more often than." A fleet of two
// hundred devices polled at the floor is dozens of requests per second at all
// times, most of which never change anything — and an access switch's monitoring
// CPU budget is modest. A device that is monitored too aggressively is a device
// that gets damaged.
type MonitorChannel struct {
	// Kind: web | snmp | streaming.
	Kind string `json:"kind"`
	// Metrics: what this channel can return, named as in the standard model
	// (if_state, if_counters, health, lldp…).
	Metrics []string `json:"metrics"`
	// MinInterval: the SHORTEST interval acceptable on this channel.
	MinInterval time.Duration `json:"min_interval"`
	// Note explains a limitation the struct doesn't otherwise state — for
	// example an access restricted to the management VLAN.
	Note string `json:"note,omitempty"`
}

// MetricUnavailable declares a metric that the PLATFORM does not expose, along
// with the reason — the absence stated out loud rather than left to silence.
//
// Not to be confused with a metric meshcraft does not collect YET. The two read
// the same way in the UI — no graph — and lead to OPPOSITE decisions:
//
//   - "meshcraft doesn't collect it yet" is a gap on our side: it gets closed by
//     a release, and the operator is right to expect it eventually;
//   - "the platform doesn't expose it" is a fact about the device: no amount of
//     development changes it, and the operator must either change hardware or
//     give up.
//
// Confusing the two costs time and credibility. A gap on OUR side therefore has
// no business here: it shows up as the absence of collection ALONGSIDE a channel
// that can, in fact, return the metric (health, lldp, and poe are in that
// category). This list only carries what the device does not serve.
type MetricUnavailable struct {
	// Metric: named as in the standard model — the same key as on channels — so
	// "no channel renders this metric" and "here is why" answer under the same
	// name. Operator-facing copy is NOT here: the UI localizes from this key
	// (`monitoring.unavailable.<metric>`). The connector only declares absence.
	Metric string `json:"metric"`
	// Observed: is the absence ESTABLISHED on hardware, or only assumed from
	// product-line documentation?
	//
	// Same distinction as ResetProcedure.Confirmed, and for the same reason: verified
	// knowledge and assumed knowledge must not be shown the same way. An observed
	// absence is a fact shown without reservation; an assumed absence must stay
	// revisable, because later firmware or another model in the line may disprove it.
	Observed bool `json:"observed"`
	// Source: technical evidence — the reading that establishes absence when
	// Observed is true, the guide page that suggests it otherwise. For a skeptical
	// operator or engineer, not marketing copy (that lives in the UI catalog).
	Source string `json:"source,omitempty"`
}

// MonitoringCapabilities describes a platform's observability.
//
// It is the connector that decides, not a setting: nobody should have to know,
// device by device, what their fleet supports. What the platform cannot do is
// not worked around — the device shows up with whatever channel it has, and the
// UI says which one. A fleet that is half-monitored without anyone noticing
// would be the worst possible outcome.
//
// See docs/01-Architecture/Monitoring unifie et pipelines §1.
type MonitoringCapabilities struct {
	Channels []MonitorChannel `json:"channels"`
	// Traps / Syslog: can the device PUSH to a collector? This is what gives
	// reactivity, where polling gives the baseline reference.
	Traps  bool `json:"traps"`
	Syslog bool `json:"syslog"`
	// Streaming: gNMI or equivalent.
	Streaming bool `json:"streaming"`
	// Unavailable: what the platform does NOT expose, and why.
	//
	// Everything else in this struct describes what EXISTS; without this field,
	// an absence is just silence, and silence cannot be told apart from an
	// oversight. This is the read-side counterpart of instanton.WritableExtras,
	// whose emptiness "is read from the UI instead of being discovered in the
	// field."
	//
	// Empty is NOT an admission of ignorance: the project rule remains "an
	// unknown capability is assumed absent," and a metric with no channel is
	// simply not monitored. This field only serves to add the REASON where it is
	// known.
	Unavailable []MetricUnavailable `json:"unavailable,omitempty"`
}

// Best returns the fastest channel available for a given metric, and says
// whether one exists. Used for planning: no point asking for a rate the device
// won't sustain.
func (m MonitoringCapabilities) Best(metric string) (MonitorChannel, bool) {
	var out MonitorChannel
	var found bool
	var c MonitorChannel
	for _, c = range m.Channels {
		var s string
		for _, s = range c.Metrics {
			if s != metric {
				continue
			}
			if !found || c.MinInterval < out.MinInterval {
				out, found = c, true
			}
		}
	}
	return out, found
}

// BestOfKind returns the fastest channel of a given KIND (`snmp`, `web`, `api`,
// `streaming`), and says whether one exists.
//
// # Why Best wasn't enough, and what it cost
//
// Best returns the fastest channel ACROSS ALL KINDS, which is the right answer
// to "how fast can this platform be observed." It is NOT the right answer to
// "how fast am I allowed to poll THIS channel," and that second question is the
// one planning actually asks — because the product's periodic collector runs on
// the proxy and speaks ONLY SNMP (`internal/snmppoll`).
//
// The bug this produced is reproducible: the RouterOS connector declares a `web`
// channel at 5 s and an `snmp` channel at 60 s, the latter deliberately high
// because no OID had ever been read from it. Best therefore returned 5 s, and the
// controller pushed a 5 s rate to the proxy — applied to an SNMP POLL, i.e. to
// the very channel the connector had just declared "never more often than 60 s."
// One channel's floor was pitted against a different channel, which is exactly
// what a floor is not supposed to be.
//
// Falling back to Best is left to the CALLER and is not done here: a platform
// that declares no channel of the requested kind must be handled explicitly —
// either by falling back to the floor for the unknown, or by not collecting at
// all — and hiding that choice behind a silent fallback would poll a platform
// over SNMP that never advertised the capability.
func (m MonitoringCapabilities) BestOfKind(kind, metric string) (MonitorChannel, bool) {
	var out MonitorChannel
	var found bool
	var c MonitorChannel
	for _, c = range m.Channels {
		if c.Kind != kind {
			continue
		}
		var s string
		for _, s = range c.Metrics {
			if s != metric {
				continue
			}
			if !found || c.MinInterval < out.MinInterval {
				out, found = c, true
			}
		}
	}
	return out, found
}

// WhyUnavailable returns the declared reason for a metric the platform does not
// expose. Queried with the same key as Best: where Best answers "no channel,"
// this one answers "and here is why there never will be one."
//
// ok=false does NOT mean the metric is available — it means nobody has
// documented its absence. It's Best that states availability.
func (m MonitoringCapabilities) WhyUnavailable(metric string) (MetricUnavailable, bool) {
	var u MetricUnavailable
	for _, u = range m.Unavailable {
		if u.Metric == metric {
			return u, true
		}
	}
	return MetricUnavailable{}, false
}

// Contradictions returns the metrics declared BOTH as returned by a channel AND
// as unavailable on the platform.
//
// A self-contradicting declaration is worse than no declaration at all: either
// the UI advertises a graph that no collection will ever fill, or it shows a
// reason for absence next to a graph that actually exists. Either way, the
// operator loses the one thing these capabilities are supposed to provide —
// certainty that what's shown matches the device.
//
// This is not a runtime check: it is an invariant locked in by every connector's
// tests, including future ones. It makes this kind of inconsistency impossible to
// introduce silently, in either direction.
func (m MonitoringCapabilities) Contradictions() []string {
	var out []string
	var u MetricUnavailable
	for _, u = range m.Unavailable {
		var _, servi = m.Best(u.Metric)
		if servi {
			out = append(out, u.Metric)
		}
	}
	sort.Strings(out)
	return out
}

// EffectiveUsername resolves the name that can actually be set. The desired name
// is only a wish: what this function returns is what lands on the device, and
// therefore what must be recorded.
func (a AdminAccount) EffectiveUsername(wanted string) string {
	if a.Custom && wanted != "" {
		return wanted
	}
	if a.FixedName != "" {
		return a.FixedName
	}
	// Unknown capability: assume the worst case, i.e. the account name
	// historically used across the whole industry.
	return "admin"
}

// CommitStyle: how the platform makes a change durable. This is what determines
// the write's safety strategy (cf. [[Reconciliation]]).
type CommitStyle string

const (
	// CommitImmediate: the change applies and persists in one shot. The worst
	// case — no safety net provided by the platform.
	CommitImmediate CommitStyle = "immediate"
	// CommitApplyThenSave: the change applies right away but does not survive a
	// reboot until it is explicitly saved. This is a free rollback: apply,
	// verify, THEN save.
	CommitApplyThenSave CommitStyle = "apply_then_save"
	// CommitConfirmed: the platform rolls itself back if not confirmed.
	CommitConfirmed CommitStyle = "commit_confirmed"
	// CommitArchiveRollback: snapshot + restore (IOS archive).
	CommitArchiveRollback CommitStyle = "archive_rollback"
)

// Capabilities: what the platform knows how to do. An intent asking for
// something else is refused at compile time, with a readable message — never a
// silent failure at deployment.
type Capabilities struct {
	// VlanRange and MaxVlans: 802.1Q.
	VlanRange [2]int `json:"vlan_range"`
	MaxVlans  int    `json:"max_vlans"`
	// Features: what's present or absent, named as in the standard model.
	Features map[string]bool `json:"features"`
	// STPModes : none | stp | rstp | mstp. "none" means the platform can turn
	// spanning tree off (RouterOS protocol-mode=none, Instant On admin mode 0).
	// Platforms that only speak one protocol (SwOS RSTP) omit "none" and list
	// that protocol alone — ResolveSTPMode will not honor a "none" preference
	// against them.
	STPModes []string `json:"stp_modes"`
	// MSTInstances, MaxACLs, MaxACEs, MaxStaticRoutes: the numeric limits, used
	// to refuse BEFORE writing.
	MSTInstances    int `json:"mst_instances"`
	MaxACLs         int `json:"max_acls"`
	MaxACEs         int `json:"max_aces"`
	MaxStaticRoutes int `json:"max_static_routes"`
	// MaxLAGs / PortsPerLAG: aggregation. Depends on the model's port count.
	MaxLAGs     int `json:"max_lags"`
	PortsPerLAG int `json:"ports_per_lag"`
	// SNMPVersions: v1 | v2c | v3. Decides the service identity pushed
	// (cf. docs/03-Securite/Identite de service et rotation).
	SNMPVersions []string `json:"snmp_versions"`
	// SNMPv3Auth / SNMPv3Priv: the algorithms ACTUALLY available.
	//
	// "Supports v3" is not a boolean. A platform can advertise v3 while only
	// offering MD5 and DES, which is no better than v2c — and presenting it as
	// secure would be the worst kind of lie. The connector therefore declares
	// the algorithms, policy carries a floor, and the gap between the two is
	// DISPLAYED (spec §2.1 of Identite de service et rotation).
	//
	// These lists also decide the LABEL the device will receive
	// (`snmpv3-sha-des`), and therefore the secret it will share: two devices
	// with different algorithms cannot carry the same v3 key.
	SNMPv3Auth []string `json:"snmpv3_auth,omitempty"`
	SNMPv3Priv []string `json:"snmpv3_priv,omitempty"`
	// Monitoring: which channels this device can be monitored through, and at
	// what rate that's reasonable.
	Monitoring MonitoringCapabilities `json:"monitoring"`
	// MaxSessions: simultaneous administration sessions. A connector that
	// ignores this takes down the device's own UI under its own requests.
	MaxSessions int `json:"max_sessions"`
	// Commit: the write's safety strategy.
	Commit CommitStyle      `json:"commit"`
	Mgmt   MgmtCapabilities `json:"mgmt"`
	// Admin: what the platform accepts as an administration account.
	Admin AdminAccount `json:"admin"`
}

// Has answers without panicking on a nil map. An unknown feature is ABSENT: for
// anything that can cut off access, the unknown is treated as a refusal.
func (c Capabilities) Has(feature string) bool {
	if c.Features == nil {
		return false
	}
	return c.Features[feature]
}

// ResetProcedure is KNOWLEDGE, not an action: the connector describes the factory
// reset, meshcraft does not perform it in the operator's place
// (cf. docs/01-Architecture/Discovery).
type ResetProcedure struct {
	// Hardware: the physical gesture, described unambiguously.
	Hardware string `json:"hardware,omitempty"`
	// Software: the software path when admin access is retained.
	Software string `json:"software,omitempty"`
	// Erases: what is actually wiped.
	Erases []string `json:"erases,omitempty"`
	// AfterReset: the state the device is found in afterward — this is what
	// allows chaining straight into adoption.
	AfterReset []string `json:"after_reset,omitempty"`
	// Pitfalls: known traps.
	Pitfalls []string `json:"pitfalls,omitempty"`
	// Confirmed: has the procedure been verified for this exact model, or is it
	// deduced from a product range? The UI must say which — a wrong reset
	// procedure costs time at best, access to the device at worst.
	Confirmed bool   `json:"confirmed"`
	Source    string `json:"source,omitempty"`
}

// Driver is a connector's contract. Methods not yet shipped return
// ErrNotImplemented.
type Driver interface {
	Info() Info
	// Capabilities depends on the exact model: an 8-port and a 48-port unit of
	// the same range do not have the same limits.
	Capabilities(model string) Capabilities
	// Fingerprint: "is this device mine?", from 0 to 100.
	Fingerprint(ev Evidence) int
	ResetProcedure(model string) ResetProcedure
}

// Evidence gathers what discovery learned about an address, without prejudging
// the transport.
type Evidence struct {
	HTTPServer  string
	HTTPTitle   string
	HTTPBody    string
	SSHBanner   string
	ShellOut    string
	SysDescr    string
	SysObjectID string
	OpenPorts   []int
}

// --- Registry ---

var (
	registryMu sync.RWMutex
	registry   = map[string]Driver{}
)

// Register registers a connector. Called from the init of connector packages.
func Register(d Driver) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[d.Info().Name] = d
}

// Get returns a connector by name.
func Get(name string) (Driver, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var d, ok = registry[name]
	return d, ok
}

// Names lists every REGISTERED connector, sorted.
//
// Distinct from discovery.Drivers(), which enumerates FINGERPRINTS — what
// meshcraft knows how to recognize on a network. This enumerates what it knows
// how to talk to, which is the question a capability check has to ask: "does
// every connector that claims a web transport also say where that interface
// answers?" is only meaningful over the registry.
//
// Sorted so a test that iterates reports its failures in a stable order rather
// than a different one per run.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out = make([]string, 0, len(registry))
	var name string
	for name = range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// DriverNameFromExtra reads devices.extra.driver without failing when Extra also
// carries nested objects (stp_observed, …). Unmarshalling Extra into
// map[string]string rejects those shapes and used to make the connector look
// unknown (addressing apply shown as ANNOTATION only).
func DriverNameFromExtra(raw json.RawMessage) string {
	return ExtraString(raw, "driver")
}

// ExtraString reads a string field from devices.extra. Nested objects under other
// keys (stp_observed, …) are ignored — they must not make the whole decode fail.
func ExtraString(raw json.RawMessage, key string) string {
	if len(raw) == 0 || key == "" {
		return ""
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(raw, &bag) != nil {
		return ""
	}
	var v, ok = bag[key]
	if !ok || len(v) == 0 || string(v) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return s
}

// List returns all connectors, by name.
func List() []Driver {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out = make([]Driver, 0, len(registry))
	var d Driver
	for _, d = range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Info().Name < out[j].Info().Name })
	return out
}

// Best picks the connector with the highest score above the threshold. Returns
// ok=false if nothing matches — the device then shows up as unidentified rather
// than forced into a range it isn't part of.
func Best(ev Evidence, threshold int) (Driver, int, bool) {
	var best Driver
	var bestScore int
	var d Driver
	for _, d = range List() {
		var score = d.Fingerprint(ev)
		if score > bestScore {
			best, bestScore = d, score
		}
	}
	if best == nil || bestScore < threshold {
		return nil, bestScore, false
	}
	return best, bestScore, true
}

// Unused keeps context in the signature of the upcoming contract (ReadState,
// Plan, Apply) without imposing a dependency starting now.
var _ = context.Background
