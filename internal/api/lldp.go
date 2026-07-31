package api

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// LLDP neighborhood DECISIONS, and nothing else.
//
// See docs/01-Architecture/Voisinage LLDP.md for the hardware survey each rule in this
// file derives from. Everything here is PURE: this package has no database in tests, and
// this is where the two judgments are made that decide what an operator will see drawn on
// their weathermap — recognize a neighbor, and conclude that a link is seen from both
// sides. The convention is that of `judgeProxyScope`, `judgeCollector`, `judgeTrapProof`
// and `effectiveInterval`.
//
// # The two mistakes not to make, and why they do not cost the same price
//
// A neighbor we fail to recognize produces a link to an "unknown": visible, named, and
// the operator immediately understands a device is missing from inventory. A neighbor
// recognized WRONGLY produces a link between two devices that are not cabled together:
// credible, false, and nobody will question it. The second mistake is therefore far more
// expensive, and that explains every refusal in this file — an ambiguous identity token is
// discarded, a remote port we cannot name leaves the link seen from one side only.

// --- Normalize a chassis identity ---

// normalizeChassisID makes two writings of the SAME chassis comparable.
//
// # The measured fact that makes this function indispensable
//
// Chassis `B8:69:F4:5B:A0:D2` — the lab CRS326's — is served in TWO ways in the reference
// fleet, depending on who reports it:
//
//	the CRS326 itself  lldpLocChassisId  "B8:69:F4:5B:A0:D2"  (17 bytes of TEXT)
//	the 1930 seeing it lldpRemChassisId  six raw bytes → "0xb869f45ba0d2"
//
// Comparing strings as-is would therefore NEVER match these two observations, and the
// fleet's only bilateral link would appear seen from one side only. This is not a firmware
// defect: the MIB says "OctetString", and a vendor may put readable text there.
//
// # Why normalization is CONDITIONAL
//
// An `lldpChassisId` is not necessarily a MAC address. The MIB enumerates seven subtypes,
// including `local(7)` — a free string the administrator chooses — and `networkAddress(5)`.
// Blindly reducing the value to hex digits would destroy a chassis named "room-b-rack-12"
// into "bab12", and two devices whose names share only a-f letters would be declared
// identical.
//
// MAC normalization therefore applies only if the result has the right shape: twelve hex
// digits, and nothing but separators around them. Everything else is simply folded to
// lowercase and trimmed, which is enough to absorb the only variation we see on text.
//
// Never returns a value shorter than an usable token: empty input returns empty, and an
// empty token identifies nobody (cf. lldpIdentity.add).
func normalizeChassisID(raw string) string {
	var trimmed = strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var lower = strings.ToLower(trimmed)
	// The "0x" prefix is what `snmppoll.OctetText` puts on non-printable bytes: it is
	// part of our writing, not of the value.
	var body = strings.TrimPrefix(lower, "0x")
	var hex []rune
	var other bool
	var r rune
	for _, r = range body {
		switch {
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'):
			hex = append(hex, r)
		case r == ':' || r == '-' || r == '.' || r == ' ':
			// MAC writing separators. Ignored, but they are not enough to prove we are
			// reading one.
		default:
			other = true
		}
	}
	if !other && len(hex) == 12 {
		return string(hex)
	}
	return lower
}

// --- Recognize a neighbor in inventory ---

// The three recognition paths, from most to least reliable. The value goes into
// `LLDPNeighbor.PeerVia` and up to the screen: a link drawn on faith in a system name
// deserves to be read as less reliable than a link drawn on a MAC address, and presenting
// the two as equivalent would be the kind of silence that wastes an hour.
const (
	// lldpViaChassis: the chassis announced by the neighbor is one a fleet device
	// declares (`lldpLocChassisId`). A MAC is globally unique: strong proof.
	lldpViaChassis = "chassis"
	// lldpViaPortID: the chassis announced by the neighbor is one of a fleet device's
	// `lldpLocPortId` values.
	//
	// This path looks absurd and it is the one that saves the reference fleet's only
	// bilateral link. Survey: the 1930 declares `lldpLocChassisId = 0x8825101761a7`, but
	// frames it emits on port 50 carry a chassis-id of `0x8825101761d9` — that PORT's
	// MAC, which is precisely its `lldpLocPortId`. The CRS326 therefore records a neighbor
	// whose identifier appears nowhere in the 1930's DECLARED identity. Without this path,
	// this link would be seen from one side only.
	//
	// Its strength is that of chassis — a port MAC is as unique as a chassis MAC — but it
	// is named separately because an operator reading "recognized by port identifier"
	// understands they are looking at firmware that announces itself oddly, which
	// "recognized by its chassis" would hide.
	lldpViaPortID = "port_id"
	// lldpViaSysName: the announced system name matches one a fleet device declares
	// (`lldpLocSysName`).
	//
	// LAST resort, and it must be said why it exists despite weakness: it is the only path
	// that works when a device announces a chassis in a form we cannot match. And it is the
	// `lldpLocSysName` from collection that is compared, NEVER the meshcraft name — survey:
	// `lab-crs326` announces "lan-r4" and `sw-labo-1930` announces by serial "CN2AKPH86J".
	// Comparing to the meshcraft name would have matched none of the fleet devices.
	lldpViaSysName = "sys_name"
)

// lldpIdentity indexes fleet identity tokens to the device that carries them.
//
// Three maps and not one, to hold priority ORDER and name the path taken. A single map
// would have made the verdict depend on insertion order, therefore on Go map traversal
// order, which varies each run: two refreshes of the same page could have attributed the
// same neighbor to two different devices.
type lldpIdentity struct {
	chassis map[string]string
	portIDs map[string]string
	sysName map[string]string
	// devices: device by identifier, to resolve the remote port in ITS facade.
	devices map[string]model.Device
}

// add registers a token, and REMOVES one shared by two devices.
//
// Removing an ambiguous token follows the same doctrine as `facade.norm`: matching at
// random is worse than not matching. Two devices announcing the same system name, which
// happens (a factory-default model, two switches never renamed), must not have each
// other's neighbors attributed to them.
//
// The sentinel value is the empty string rather than deletion, so the token stays
// NEUTRALIZED even if a third device declares it afterward — deletion would let the third
// take it for itself alone.
func (idx lldpIdentity) add(m map[string]string, token, deviceID string) {
	if token == "" || deviceID == "" {
		return
	}
	var prev, seen = m[token]
	if seen && prev != deviceID {
		m[token] = ""
		return
	}
	if seen && prev == "" {
		return
	}
	m[token] = deviceID
}

// buildLLDPIdentity builds the index from what devices ANNOUNCE.
//
// PURE, and it is the entry to the testable part: it takes the two base readings
// (announced identity, devices) and returns a matching decision, without querying anything.
func buildLLDPIdentity(selves []model.LLDPSelf, devices []model.Device) lldpIdentity {
	var idx = lldpIdentity{
		chassis: map[string]string{},
		portIDs: map[string]string{},
		sysName: map[string]string{},
		devices: map[string]model.Device{},
	}
	var i int
	for i = range devices {
		idx.devices[devices[i].ID] = devices[i]
	}
	for i = range selves {
		var s = selves[i]
		idx.add(idx.chassis, normalizeChassisID(s.ChassisID), s.DeviceID)
		idx.add(idx.sysName, strings.ToLower(strings.TrimSpace(s.SysName)), s.DeviceID)
		var p int
		for p = range s.PortIDs {
			// `lldpLocPortId` values are indexed only when they look like a MAC. A port
			// identifier of "45" or "ether3" identifies NOTHING beyond its device, and
			// indexing it would recognize as the 1930 any neighbor whose chassis is called
			// "45" — which `normalizeChassisID` would render identical.
			var token = normalizeChassisID(s.PortIDs[p])
			if len(token) != 12 || strings.ContainsAny(token, "ghijklmnopqrstuvwxyz") {
				continue
			}
			idx.add(idx.portIDs, token, s.DeviceID)
		}
	}
	return idx
}

// identifyPeer recognizes — or not — the device behind a neighbor, and says what it relied
// on.
//
// Consultation ORDER IS the decision, as in `judgeProxyScope`: chassis, then port
// identifier, then system name. The first that answers wins, and we do not try the rest —
// a chassis pointing to A and a system name pointing to B is the case of a renamed or
// cloned device, and the hardware address is right.
//
// Returns "" when nothing matches, and that is a RESULT not a failure to work around: an
// unrecognized neighbor is still drawn, with the name it gave itself. That is even the
// most frequent case in operations — a vendor router, an access point outside meshcraft,
// the S3900 seen on the fleet.
func identifyPeer(idx lldpIdentity, n model.LLDPNeighbor) (string, string) {
	var token = normalizeChassisID(n.ChassisID)
	var id = idx.chassis[token]
	if id != "" {
		return id, lldpViaChassis
	}
	if id = idx.portIDs[token]; id != "" {
		return id, lldpViaPortID
	}
	if id = idx.sysName[strings.ToLower(strings.TrimSpace(n.SysName))]; id != "" {
		return id, lldpViaSysName
	}
	return "", ""
}

// resolvePeerPort names the REMOTE port in the recognized device's facade.
//
// Reuses `facade.resolve`, which is already the product's only way to match an interface
// label to a facade port, and that is essential: fleet labels take every form — "sfp-
// sfpplus2" (exact match), "SFP2" (normalized match), "50" on a 1930 whose facade calls
// that port "GE50" (prefix + label). Writing a second version of this matching here would
// have produced two ways to name the same port, and a link whose two halves do not
// recognize each other.
//
// TWO columns tried, `PortID` then `PortDesc`, because neither is served everywhere: the
// CRS326 serves NO `lldpRemPortDesc` column (empty walk, measured), the 1930 serves both —
// and its `PortDesc` ("Ethernet Port on unit 1, port 28" for a third-party neighbor) is
// sometimes only a sentence, which is exactly why `PortID` goes first.
//
// Returns "" when neither attaches. That is an UNDER-ASSERTION choice, assumed and counted
// below: the link will stay "seen from one side" although the other side may see it too.
// Inventing a port name would have drawn a cable on the wrong port of the right facade —
// the credible, false fault.
func resolvePeerPort(idx lldpIdentity, peerID string, n model.LLDPNeighbor) string {
	var d, known = idx.devices[peerID]
	if !known {
		return ""
	}
	var f = facadeOf(d)
	var name = f.resolve(n.PortID)
	if name != "" {
		return name
	}
	return f.resolve(n.PortDesc)
}

// IdentifyLLDPNeighbors fills `PeerDevice` / `PeerPort` / `PeerVia` on observations read
// from storage.
//
// Matching is done AT READ TIME and never stored, because it depends on inventory: the day
// the operator adopts the device on the other side, all observations that mentioned it
// must recognize it WITHOUT rewriting a table. Identification frozen in storage would lie
// until the next snapshot — thirty seconds for a neighbor, never for a device that does
// not speak LLDP.
//
// Returns a NEW slice: input observations come from the store and must not be damaged (same
// rule as `scopedSamples`, whose filter does not mutate its batch).
func IdentifyLLDPNeighbors(neighbors []model.LLDPNeighbor, selves []model.LLDPSelf,
	devices []model.Device) []model.LLDPNeighbor {
	var idx = buildLLDPIdentity(selves, devices)
	var out = make([]model.LLDPNeighbor, len(neighbors))
	var i int
	for i = range neighbors {
		out[i] = neighbors[i]
		var peer, via = identifyPeer(idx, neighbors[i])
		if peer == "" {
			continue
		}
		out[i].PeerDevice = peer
		out[i].PeerVia = via
		out[i].PeerPort = resolvePeerPort(idx, peer, neighbors[i])
	}
	return out
}

// --- Derive a link from one or two observations ---

// lldpEndpoint: one link end, as compared.
type lldpEndpoint struct {
	device string
	port   string
}

// deriveLLDPLinks derives topology links from observations.
//
// # A link seen from ONE side is the NORMAL case
//
// It must be said that way, because intuition says the opposite. An entire reference fleet
// platform does NOT speak 802.1AB: SwOS serves nothing under `1.0.8802` (empty walk,
// measured; its connector declares it with proof). Every link touching a CSS326 will
// therefore never be seen except from the other side, and the connector already wrote that
// — "at best a neighbor will see it, it will see nobody". A unilateral link is DRAWN,
// and said as such.
//
// The rest of the fleet adds: a neighbor outside inventory (vendor router, access point)
// will never report anything from its side, by construction.
//
// # Required reciprocity is EXACT, and that is an assumed under-assertion
//
// Two observations match when the end one CLAIMS is the end the other OCCUPIES, both
// ways: A/pa says it sees B/pb, and B/pb says it sees A/pa. Nothing less.
//
// The broader rule — "A and B see each other, so it is the same link" — would have caught
// a few more cases (those where the neighbor's facade cannot name the remote port) and
// would have been wrong on the case that matters: two devices linked by TWO cables, which
// every redundant uplink in the real world presents. It would then have mutually confirmed
// the two wrong halves. What we lose is a link displayed "seen from one side" although it
// is from both — visible, cautious, and fixable by naming the port. What we would avoid
// losing would never be seen.
//
// # Multi-neighbor ports: flood domain, with one exception
//
// A local port that reports MORE THAN ONE remote chassis sits in a flood domain:
// an intermediate switch that does not consume LLDP (measured: SwOS) floods frames,
// so the observer hears speakers behind that hop. Those rows stay in `lldp_neighbors`
// for the Ports panel ("this port sees N names").
//
// Default: emit NO weathermap edge for them — drawing CRS326→RB5009 and CRS326→CRS305
// when both arrive on the same uplink would invent cables that do not exist.
//
// Exception: if EXACTLY ONE of those remotes also claims this local port back
// (exact device+port reciprocity), that peer is kept as a bilateral edge. Measured:
// CRS326 `sfp-sfpplus2` hearing RB5009 + CRS305 + Instant On 1930, while only the
// 1930 answers on GE50 — the CRS↔Aruba link must stay on the plan; the other two
// must not. Zero or several reciprocal remotes → still nothing (ambiguous).
//
// # What dates the link carries
//
// `FirstSeen` is the OLDEST of the two observations and `LastSeen` the most RECENT: the
// link exists since the first side saw it, and was confirmed at the last snapshot that
// saw it again. Taking one side's dates would have made displayed age depend on sort order.
//
// # Output
//
// One link per RECIPROCAL observation pair, one link per solitary observation on a
// single-neighbor port, plus the unique-reciprocal flood exception, SORTED. Sort is what
// keeps the weathermap stable from refresh to refresh: without it, two identical
// responses would differ and the browser would redraw a plan that did not move (same
// reason as `facade.names`).
//
// PURE, and clockless: nothing here compares a date to `time.Now()`. Observation freshness
// is judged on the SCREEN, with the viewer's clock, because that is the only one that
// makes "seen 3 minutes ago" true for them.
func deriveLLDPLinks(neighbors []model.LLDPNeighbor) []model.LLDPLink {
	// How many remotes each local port heard this round.
	var portNeighbors = map[lldpEndpoint]int{}
	var i int
	for i = range neighbors {
		var here = lldpEndpoint{device: neighbors[i].DeviceID, port: neighbors[i].LocalPort}
		portNeighbors[here]++
	}
	var flooded = map[lldpEndpoint]bool{}
	var ep lldpEndpoint
	for ep = range portNeighbors {
		if portNeighbors[ep] > 1 {
			flooded[ep] = true
		}
	}

	// For each flooded port, the unique remote that also claims it back — if any.
	var floodKeep = map[lldpEndpoint]lldpEndpoint{}
	for ep = range flooded {
		var backs []lldpEndpoint
		for i = range neighbors {
			var n = neighbors[i]
			if n.DeviceID != ep.device || n.LocalPort != ep.port {
				continue
			}
			if n.PeerDevice == "" || n.PeerPort == "" {
				continue
			}
			var far = lldpEndpoint{device: n.PeerDevice, port: n.PeerPort}
			if !lldpObserves(neighbors, far, ep) {
				continue
			}
			var already bool
			var bi int
			for bi = range backs {
				if backs[bi] == far {
					already = true
					break
				}
			}
			if !already {
				backs = append(backs, far)
			}
		}
		if len(backs) == 1 {
			floodKeep[ep] = backs[0]
		}
	}

	// occupied: which end each observation occupies, and what it claims opposite.
	var claims = map[lldpEndpoint]lldpEndpoint{}
	for i = range neighbors {
		var n = neighbors[i]
		if n.PeerDevice == "" || n.PeerPort == "" {
			continue
		}
		var here = lldpEndpoint{device: n.DeviceID, port: n.LocalPort}
		// A port seeing SEVERAL recognized neighbors — the measured CRS326 case, one port
		// seeing three through a transparent switch — cannot carry a unique claim. We then
		// retain NONE for reciprocity: saying "this port faces that one" when it sees three
		// would be choosing at random. (The floodKeep exception above is separate.)
		var prev, seen = claims[here]
		var far = lldpEndpoint{device: n.PeerDevice, port: n.PeerPort}
		if seen && prev != far {
			claims[here] = lldpEndpoint{}
			continue
		}
		if seen && prev == (lldpEndpoint{}) {
			continue
		}
		claims[here] = far
	}

	var out []model.LLDPLink
	// paired: pairs already rendered, so the same bilateral link is not returned twice
	// (once per observation).
	var paired = map[lldpEndpoint]bool{}
	for i = range neighbors {
		var n = neighbors[i]
		var here = lldpEndpoint{device: n.DeviceID, port: n.LocalPort}
		var far = lldpEndpoint{device: n.PeerDevice, port: n.PeerPort}
		if flooded[here] {
			var keep, ok = floodKeep[here]
			if !ok || n.PeerDevice == "" || n.PeerPort == "" || keep != far {
				continue
			}
		} else if n.PeerDevice != "" && n.PeerPort != "" && flooded[far] {
			// Far end is a flood domain: only the floodKeep peer is drawn, and from
			// that side — skip the reverse half here to avoid a duplicate edge.
			continue
		}
		var link = model.LLDPLink{
			ADevice: n.DeviceID, APort: n.LocalPort,
			BDevice: n.PeerDevice, BPort: n.PeerPort,
			BLabel: n.SysName, BPortLabel: peerPortLabel(n),
			BChassisID: n.ChassisID, BSysDesc: n.SysDesc,
			FirstSeen: n.FirstSeen, LastSeen: n.LastSeen,
		}
		var bilateral = n.PeerDevice != "" && n.PeerPort != "" &&
			(claims[far] == here && claims[here] == far ||
				floodKeep[here] == far)
		if bilateral {
			if paired[far] {
				// The other half already produced the link: do not return it twice. Dates
				// were merged at that moment.
				continue
			}
			link.BothSides = true
			// Dates from BOTH halves: the link exists since the oldest observation.
			var j int
			for j = range neighbors {
				var o = neighbors[j]
				if o.DeviceID != far.device || o.LocalPort != far.port {
					continue
				}
				if o.PeerDevice != n.DeviceID || o.PeerPort != n.LocalPort {
					continue
				}
				if o.FirstSeen.Before(link.FirstSeen) {
					link.FirstSeen = o.FirstSeen
				}
				if o.LastSeen.After(link.LastSeen) {
					link.LastSeen = o.LastSeen
				}
			}
			paired[here] = true
		}
		out = append(out, link)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].ADevice != out[b].ADevice {
			return out[a].ADevice < out[b].ADevice
		}
		if out[a].APort != out[b].APort {
			return out[a].APort < out[b].APort
		}
		if out[a].BDevice != out[b].BDevice {
			return out[a].BDevice < out[b].BDevice
		}
		return out[a].BChassisID < out[b].BChassisID
	})
	return out
}

// lldpObserves reports whether `from` has an observation that names `toward` exactly.
func lldpObserves(neighbors []model.LLDPNeighbor, from, toward lldpEndpoint) bool {
	var i int
	for i = range neighbors {
		var n = neighbors[i]
		if n.DeviceID != from.device || n.LocalPort != from.port {
			continue
		}
		if n.PeerDevice == toward.device && n.PeerPort == toward.port {
			return true
		}
	}
	return false
}

// peerPortLabel chooses the remote port label to DISPLAY.
//
// `PortID` first because it is a port name when the subtype says so; `PortDesc` as
// fallback. Both may be missing: a neighbor with neither served exists, and the screen
// will then show only the system name — which is still information, unlike an invented
// dash.
//
// A `PortID` that is not a readable name — six MAC bytes, which `snmppoll.OctetText`
// renders "0x…" — yields to `PortDesc`. Measured: the 1930's ports 50 and 52 announce
// their MAC as port identifier, and "0x8825101761d9" on a port cell teaches nobody where
// "50" places the cable.
func peerPortLabel(n model.LLDPNeighbor) string {
	if n.PortID != "" && !strings.HasPrefix(n.PortID, "0x") {
		return n.PortID
	}
	if n.PortDesc != "" {
		return n.PortDesc
	}
	return n.PortID
}

// --- The view rendered on screen ---

// lldpView is what the read route returns: observations and links derived from them, plus
// what we do NOT know.
type lldpView struct {
	// Neighbors: observations, matching included. Serves port hover — the screen needs a
	// port's neighbor, not a link.
	Neighbors []model.LLDPNeighbor `json:"neighbors"`
	// Links: derived links. Serves the weathermap.
	Links []model.LLDPLink `json:"links"`
	// Silent: site devices known NOT to serve the LLDP-MIB, and those we never read
	// anything from.
	//
	// This field exists to prevent the most tempting and falsest reading: "this switch
	// has no neighbors, so nothing is plugged in". A CSS326 on SwOS will never see anyone
	// (empty walk of `1.0.8802`, measured) and a device no proxy interrogates yet had
	// nothing to say. Both show zero neighbors, and neither means "nothing is plugged in".
	Silent []lldpSilence `json:"silent"`
}

// lldpSilence: a device whose neighborhood is UNKNOWN, and why.
type lldpSilence struct {
	DeviceID string `json:"device_id"`
	// Reason: three values, and three were needed.
	//
	// A boolean would have flattened situations repaired by OPPOSITE actions, and that is
	// the kind of shortcut that sends someone looking for a meshcraft defect where the
	// firmware lacks the feature:
	//
	//   - "platform_unsupported": the CONNECTOR declares, with its survey, that this
	//     platform does not speak LLDP. Nothing is therefore ever interrogated, and no
	//     snapshot will ever arrive. That is MikroTik on SwOS. Not repairable;
	//   - "not_served": we interrogated, and the agent has nothing under `1.0.8802` although
	//     its connector did not announce that. The fact is the same as above for the
	//     operator, but not for us: connector declaration to fix, or LLDP disabled in device
	//     configuration;
	//   - "never_polled": no snapshot has arrived yet for a device whose platform can
	//     render it. It lacks a collector, or the first cycle has not run. Repairable, and
	//     quickly.
	//
	// The distinction between the first two was added rereading the real path:
	// `optionalIntervalFor` turns collection on only if the connector declares an SNMP
	// `lldp` channel. A CSS326 is therefore NEVER interrogated, and would never have an
	// `lldp_self` row — it would have presented as "a collector is missing", exactly the
	// misconception this field exists to prevent.
	Reason string `json:"reason"`
	// SeenAt: when the snapshot that said so was taken. Null when no snapshot exists —
	// therefore for "never_polled" and "platform_unsupported", where we never interrogated.
	SeenAt *time.Time `json:"seen_at,omitempty"`
	// Detail: technical survey Source from the connector when Reason is
	// platform_unsupported. Operator-facing copy is localized in the UI from Reason.
	//
	// Rendered as-is rather than reworded on screen: it is the sentence a developer wrote
	// with the device in hand, and it carries proof ("the entire web interface contains no
	// occurrence of LLDP outside PoE…"). Rewriting it shorter would remove precisely what
	// makes it believable.
	Detail string `json:"detail,omitempty"`
}

const (
	// lldpSilencePlatform: the connector DECLARES the platform does not speak LLDP.
	lldpSilencePlatform  = "platform_unsupported"
	lldpSilenceNotServed = "not_served"
	lldpSilenceNeverPoll = "never_polled"
	// lldpSilenceReasonHelp goes WITH the data (cf. handleListSiteLLDP): the screen must
	// be able to explain "zero neighbors" without a developer recopying the nuance into a
	// component, where it would diverge from the backend at the first rewording.
	lldpSilenceReasonHelp = "platform_unsupported: this platform's connector declares, " +
		"from hands-on survey, that it does not speak LLDP — it is therefore never " +
		"interrogated and no meshcraft version will change that; not_served: it was " +
		"interrogated and returned nothing under 1.0.8802, which contradicts its declaration " +
		"or signals LLDP disabled on the device; never_polled: no snapshot has arrived yet " +
		"although the platform can render it — a collector is missing, or the first cycle " +
		"has not run yet"
)

// lldpPlatformSilent says whether a device's CONNECTOR declares LLDP unavailable, and
// returns its reason.
//
// Goes through the driver registry like `unavailableMetricsOf`, for the same reasons —
// including the one that matters: absent `extra` or unknown connector yields "we do not
// know", never "it is available". A manually entered device without connector therefore
// falls in `never_polled`, which is exact: we have no reason to believe it does not speak
// LLDP.
func lldpPlatformSilent(d model.Device) (string, bool) {
	var extra map[string]string
	// Error ignored deliberately, as in `unavailableMetricsOf`: a record without connector
	// is not a read anomaly.
	_ = json.Unmarshal(d.Extra, &extra)
	var drv, known = driver.Get(extra["driver"])
	if !known {
		return "", false
	}
	var why, declared = drv.Capabilities(d.Model).Monitoring.WhyUnavailable(snmppoll.MetricLLDP)
	if !declared {
		return "", false
	}
	// Technical survey evidence for the silence detail expander; operator copy is
	// localized from Reason=platform_unsupported in the UI.
	return why.Source, true
}

// buildLLDPView assembles the view. PURE: this is the function tests exercise, and the
// route above is only a database read and a `respond`.
func buildLLDPView(neighbors []model.LLDPNeighbor, selves []model.LLDPSelf,
	devices []model.Device) lldpView {
	var identified = IdentifyLLDPNeighbors(neighbors, selves, devices)
	var out = lldpView{
		Neighbors: identified,
		Links:     deriveLLDPLinks(identified),
		Silent:    []lldpSilence{},
	}
	if out.Links == nil {
		out.Links = []model.LLDPLink{}
	}
	var byDevice = map[string]model.LLDPSelf{}
	var i int
	for i = range selves {
		byDevice[selves[i].DeviceID] = selves[i]
	}
	for i = range devices {
		var s, polled = byDevice[devices[i].ID]
		// Connector DECLARATION goes first, and order is the message: a device whose
		// platform does not speak LLDP is never interrogated, so it will never have a
		// snapshot. Judging first on absent snapshot would have ranked it as "a collector
		// is missing" — an action to take where there is nothing to do.
		var motif, declared = lldpPlatformSilent(devices[i])
		if declared {
			out.Silent = append(out.Silent, lldpSilence{
				DeviceID: devices[i].ID, Reason: lldpSilencePlatform, Detail: motif})
			continue
		}
		if !polled {
			out.Silent = append(out.Silent, lldpSilence{
				DeviceID: devices[i].ID, Reason: lldpSilenceNeverPoll})
			continue
		}
		if !s.Served {
			// Interrogated, and nothing under `1.0.8802` although the connector did not
			// announce that. For the operator the fact matches above; for us it is a
			// declaration to fix, hence a distinct word.
			var at = s.SeenAt
			out.Silent = append(out.Silent, lldpSilence{
				DeviceID: devices[i].ID, Reason: lldpSilenceNotServed, SeenAt: &at})
		}
	}
	sort.Slice(out.Silent, func(a, b int) bool {
		return out.Silent[a].DeviceID < out.Silent[b].DeviceID
	})
	return out
}
