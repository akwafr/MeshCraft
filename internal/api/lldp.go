package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// LLDP neighborhood DECISIONS, and nothing else.
//
// See docs/en/01-Architecture/LLDP neighbours.md for the hardware survey each rule in this
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

// macIdentityToken keeps a normalized token ONLY when it has the shape of a MAC
// address — twelve hex digits once separators are removed — and returns ""
// otherwise.
//
// Shared by the two indexings whose values are trustworthy identity ONLY when
// they are hardware addresses: a device's `lldpLocPortId` values, and the MAC
// facts meshcraft reads outside LLDP (deviceKnownMACs). A port identifier of
// "45" or a bridge identifier a platform serves as text would otherwise
// recognize as this device any neighbor whose chassis happens to normalize the
// same way.
func macIdentityToken(raw string) string {
	var token = normalizeChassisID(raw)
	if len(token) != 12 || strings.ContainsAny(token, "ghijklmnopqrstuvwxyz") {
		return ""
	}
	return token
}

// deviceKnownMACs lists the MAC identities meshcraft holds for a device OUTSIDE
// of LLDP collection.
//
// # The measured fact that makes this second source necessary
//
// The FS S3400 runs LLDP — `lldp run` is configured, and two fleet devices
// record its frames (chassis `0x649d99227b95`, ports "TGi0/1"/"TGi0/2") — but
// its SNMP agent serves NOTHING under `1.0.8802`: the standard LLDP-MIB is
// simply not implemented in this BDCOM-derived firmware, and its private trees
// (enterprises 3320 and 52642) answer empty too (walked live, 2026-08-05). Its
// `lldp_self` row therefore carries an EMPTY chassis, and every link pointing AT
// it stayed an unresolved ghost even though the device names itself on the wire
// and nothing on the switch is misconfigured.
//
// Yet meshcraft already KNOWS that MAC by another path: the CLI spanning-tree
// read stores the local bridge identifier — the base MAC on every platform
// surveyed — under `devices.extra.stp_observed.bridge_id`, refreshed at every
// read-state. And a device adopted from discovery carries the MAC its connector
// read at inspection under `devices.extra.mac` (cf. handleAdoptDiscovered). An
// LLDP chassis id of subtype macAddress IS that address: indexing these carries
// the same strength of proof as a declared `lldpLocChassisId`.
//
// Values are returned RAW; the caller keeps only MAC-shaped tokens
// (macIdentityToken), so a platform serving a textual bridge identifier never
// enters the chassis index.
func deviceKnownMACs(d model.Device) []string {
	if len(d.Extra) == 0 {
		return nil
	}
	var bag map[string]json.RawMessage
	if json.Unmarshal(d.Extra, &bag) != nil {
		return nil
	}
	var out []string
	var mac string
	if json.Unmarshal(bag["mac"], &mac) == nil && mac != "" {
		out = append(out, mac)
	}
	var stp struct {
		BridgeID string `json:"bridge_id"`
	}
	if json.Unmarshal(bag["stp_observed"], &stp) == nil && stp.BridgeID != "" {
		out = append(out, stp.BridgeID)
	}
	return out
}

// --- Recognize a neighbor in inventory ---

// The three recognition paths, from most to least reliable. The value goes into
// `LLDPNeighbor.PeerVia` and up to the screen: a link drawn on faith in a system name
// deserves to be read as less reliable than a link drawn on a MAC address, and presenting
// the two as equivalent would be the kind of silence that wastes an hour.
const (
	// lldpViaChassis: the chassis announced by the neighbor is one a fleet device
	// declares (`lldpLocChassisId`) — or a base MAC meshcraft itself observed on
	// that device by CLI (deviceKnownMACs), which is the same address by another
	// witness. A MAC is globally unique: strong proof.
	lldpViaChassis = "chassis"
	// lldpViaPortID: the chassis announced by the neighbor is one of a fleet device's
	// `lldpLocPortId` values.
	//
	// This path looks absurd and it is the one that saves the reference fleet's only
	// bilateral link. Survey: the 1930 declares `lldpLocChassisId = 0x02005e1061a7`, but
	// frames it emits on port 50 carry a chassis-id of `0x02005e1061d9` — that PORT's
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
	// `lab-crs326` announces "lan-r4" and `sw-labo-1930` announces by serial "CN0EXAMPL1".
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

// buildLLDPIdentity builds the index from what devices ANNOUNCE — and from the
// MAC identities meshcraft observed on them by other channels.
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
		// MAC facts read OUTSIDE LLDP — the CLI spanning-tree bridge identifier,
		// the adoption-time MAC — enter the CHASSIS index, alongside `lldp_self`
		// and never instead of it. This is what recognizes a device whose SNMP
		// agent serves no LLDP-MIB at all while the protocol runs on its wire —
		// the FS S3400 case documented on deviceKnownMACs. `add` keeps the
		// ambiguity guard: a MAC two devices would somehow both carry identifies
		// neither.
		var raw string
		for _, raw = range deviceKnownMACs(devices[i]) {
			idx.add(idx.chassis, macIdentityToken(raw), devices[i].ID)
		}
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
			idx.add(idx.portIDs, macIdentityToken(s.PortIDs[p]), s.DeviceID)
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
// Reuses `facade.resolveNeighborPort`, which shares its ladder with the metrics path's
// `facade.resolve`, and that sharing is essential: fleet labels take every form — "sfp-
// sfpplus2" (exact match), "SFP2" (normalized match), "Te1/0/2" and "0/18" and
// "Eth 1/26" (the connector's own translation). Writing a second version of this matching
// here would have produced two ways to name the same port, and a link whose two halves do
// not recognize each other.
//
// THE ONE STEP THIS PATH DOES NOT GET is prefix arithmetic: a bare "50" is a port
// number when OUR agent says it about OUR interface, and an unknown TLV value when a
// STRANGER says it about its own. The reasoning, the measurement and the accepted cost
// are written down on [facade.resolveNeighborPort]; the paragraph below is why the cost
// is the right way round.
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
//
// # A WRONG name now costs more than it used to, and how much more is measured here
//
// Returning "" is fully covered downstream: an unresolved far port never refutes, in both
// refutation rules. Returning the WRONG port of the right device is not covered, and since
// refutation is port-exact it can now delete a link where it used to only misdraw one.
// The two rules react to a mis-resolution in OPPOSITE directions, which is why the
// exposure is smaller than it first reads (both cases are locked by test):
//
//   - [markRefutedObservations] is where the teeth are. A wrongly named far port that
//     happens to be held by exactly one fresher stranger refutes the claim, and if the far
//     end does not report the claimant back, that cable leaves the map entirely. Narrow —
//     it needs a mis-resolution AND a single occupant AND a [staleWitnessMargin] of
//     freshness AND no reciprocity — but it is the expensive mistake, so it is written
//     down rather than left to be met in the field;
//   - [refuteByFreshSilence] cannot be saved by its condition 3 at all, because a port
//     this device is not really on is empty by construction. Yet the effect there is
//     benign and usually an improvement: the row it retires is the one drawn on the wrong
//     port, and the far end — which by that rule's own condition 1 IS being collected —
//     keeps drawing the same cable from its side, on the right port.
//
// THE WALK THIS PARAGRAPH USED TO ASK FOR HAS BEEN DONE — `lldpRemPortId` /
// `lldpRemPortDesc` read off every platform on 2026-08-10 and compared against what the
// facade produces (api/lldp_realforms_test.go). It found no wrong resolution among the
// cables the fleet really has, and one form that WOULD produce one: a bare ifIndex, which
// prefix arithmetic turned into a port name with only the inventory's size as a guard.
// That is what `resolveNeighborPort` now refuses, and the tests in that file hold both
// halves — every in-fleet form still resolves, the bare number no longer does.
//
// WHAT THAT COST, AND WHERE IT WAS PAID BACK. Two measured cables —
// `lab-crs326:sfp-sfpplus2 ↔ sw-labo-1930:GE50` and `s3900lab:GE28 ↔ sw-labo-1930:GE51` —
// were reciprocal only because the near side concatenated the 1930's bare "50" / the
// 1930's neighbour concatenated a bare "51". Losing that left each cable drawn twice,
// one-sided, one half per side; an operator reported the second one on 2026-08-11 as a
// double link, which is exactly what two one-sided halves of one cable look like on a map.
//
// The reciprocity is back and this function is unchanged, which is the point: it is
// [completeReciprocalPeerPorts] that fills the far port, from the FAR DEVICE'S OWN
// observation rather than from arithmetic. Returning "" here is still the right answer —
// a bare TLV value is not a port name — and it is now an answer somebody else can complete
// when, and only when, the far device has stated the endpoint itself.
func resolvePeerPort(idx lldpIdentity, peerID string, n model.LLDPNeighbor) string {
	var d, known = idx.devices[peerID]
	if !known {
		return ""
	}
	var f = facadeOf(d)
	var name = f.resolveNeighborPort(n.PortID)
	if name != "" {
		return name
	}
	return f.resolveNeighborPort(n.PortDesc)
}

// IdentifyLLDPNeighbors fills `PeerDevice` / `PeerPort` / `PeerVia` on observations read
// from storage.
//
// Matching is done AT READ TIME and never stored, because it depends on inventory: the day
// the operator adopts the device on the other side, all observations that mentioned it
// must recognize it WITHOUT rewriting a table. Identification frozen in storage would lie
// until the next snapshot — sixty seconds for a neighbor (cf. `wantedLLDPInterval`), never
// for a device that does not speak LLDP.
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

// markRefutedObservations discards the observations a FRESHER one contradicts, so a
// re-cabling redraws instead of leaving the old cable on the map.
//
// # What forgets an observation today: one thing, and only one
//
// Nothing in this product ages an LLDP observation out. There is no TTL, no prune job,
// no expiry column: `store.ReplaceLLDP` deletes the rows of the snapshotting device that
// the new snapshot no longer contains, and that is the whole of it. So an observation is
// forgotten by exactly one event — a fresh snapshot from the SAME device that made it.
//
// Two ordinary situations therefore freeze rows in place for as long as they last:
//
//   - a device the poller can no longer reach. `cmd/proxy/lldp.go` pushes NOTHING on a
//     failed walk, deliberately and with the reason written down: an empty snapshot would
//     erase a neighborhood we simply failed to read. The rows stay exactly as they were;
//   - a `source='cli'` device — the FS S3400, whose agent serves no LLDP-MIB at all — whose
//     rows are refreshed only when an operator runs read-state (cf. lldp_cli.go).
//
// Re-cable one of those and the other side, still polled every sixty seconds, reports the
// NEW neighbor while the frozen side keeps claiming the old one. `deriveLLDPLinks` would
// draw both, and the older of the two is a cable that no longer exists.
//
// # The rule
//
// A claim dies only when the endpoint it claims is demonstrably occupied by SOMEBODY ELSE,
// according to that endpoint's OWN device, in a STRICTLY fresher observation. A physical
// port carries one cable: if A/p1 now says it faces C/p2, then B's frozen "I face A/p1" is
// a statement about a port that is answering to someone else, and B is the one who is late.
//
// # The four refusals, and what each one prevents
//
// Every one of them exists because dropping a TRUE link is the expensive mistake here —
// this file's header says a false link is credible and unquestioned; a link that vanishes
// is worse still, it sends someone to the wrong rack looking for a cable that is there.
//
//   - SILENCE never refutes. A far endpoint with ZERO observations proves nothing: an
//     entire fleet platform never sees anybody (SwOS serves nothing under `1.0.8802`,
//     measured) and the S3400 has no rows whatsoever. Treating silence as refutation would
//     erase every one-sided link — which `deriveLLDPLinks` calls, correctly, the NORMAL case;
//   - an UNRECOGNIZED far neighbor never refutes. If the row occupying that far port carries
//     no `PeerDevice`, it may well BE the claimant under a chassis spelling we failed to
//     match — the 1930 announcing its port MAC instead of its chassis proves that shape
//     exists. One unidentified occupant disables refutation for that endpoint entirely;
//   - an UNRESOLVED far port never refutes. `resolvePeerPort` returns "" often and by design
//     (no `lldpRemPortDesc` on the CRS326, a port label no facade attaches), and comparing
//     at port granularity is meaningless when one of the two ports has no name;
//   - an EQUAL-OR-OLDER witness never refutes. Two sides polled in the same cycle that
//     disagree are a genuine flood domain or a real ambiguity, and picking a winner would be
//     picking at random. This is also the honest answer to "which side is authoritative":
//     FRESHNESS is, and on a tie nobody is — both claims stay drawn, one-sided;
//   - a witness only MARGINALLY fresher never refutes, and this one was added last, after a
//     probe. "Equal-or-older never refutes" prevents TIES; it does not prevent ALTERNATION.
//     Two devices collected on their own sixty-second cycles take turns being the fresher
//     of the pair, so when a far port has exactly ONE occupant that does not name us back,
//     the verdict was decided by nothing but which of the two had been polled most recently
//     — and it flipped on every read. Measured on three cycles differing only in poll order:
//     links=2, links=1, links=2, a topology map and an event log churning against a fleet
//     where no cable had moved. The witness must therefore be ahead by [staleWitnessMargin],
//     not merely ahead. See that constant for why a generous margin costs nothing: a claim
//     can only go stale if its claimant froze, and a frozen claimant is hours behind, never
//     seconds.
//
// # Which of the two sides is even ELIGIBLE
//
// Neither rule reads `lldp_self.source`, so nothing here prefers SNMP to CLI by name. The
// asymmetry an operator will nonetheless observe — a `source='cli'` device is refuted and
// never refutes — falls out of freshness alone and is deliberate; it is written up in full
// at [refuteByFreshSilence], which is where its sharp edge is.
//
// # Comparing dates while staying CLOCKLESS
//
// `deriveLLDPLinks` states that nothing in the derivation compares a date to `time.Now()`,
// and that property survives here: this function compares two ROWS to each other, never a
// row to now. Both were stamped by `now()` on the PostgreSQL server — every write goes
// through `store.ReplaceLLDP`, for the SNMP path and the CLI path alike — so there is one
// clock and proxy clock drift (measured at 11 s on the lab) never enters the arbitration.
//
// That is now ENFORCED and no longer only asserted here, which is worth the sentence
// because a future collection path carrying a client-side timestamp would break the
// arbitration SILENTLY — no error, no failing test, just links retired or held up on
// whichever device's clock ran ahead. Three things hold it:
//
//   - `ReplaceLLDP`'s SQL hardcodes `now()` for `lldp_self.seen_at`,
//     `lldp_neighbors.first_seen` and `last_seen`. A caller that fills `LLDPSelf.SeenAt`
//     or `LLDPNeighbor.LastSeen` on the model has the value DISCARDED: there is no
//     parameter to bind a client clock to. Both writers (`lldpRowsFor` on the SNMP push,
//     `lldpCLISnapshot` on read-state) leave those fields zero anyway;
//   - migration 0028 defaults all three columns to `now()` and records the reason with
//     the number, the 11 s drift above;
//   - `store.TestLLDPTimestampsAreStampedByTheServer` parses the store package and fails
//     if any write to those two tables ever stamps one of the three from anything but
//     `now()`. It is the guard that makes the first point survive a future edit.
//
// # A neighbor that DISAPPEARS needs none of this
//
// It is already handled: the next snapshot from its observer no longer contains the row, and
// `ReplaceLLDP`'s bounded delete removes it. Only a neighbor that CHANGES creates a
// contradiction outliving a snapshot boundary, because the contradiction does not live on
// the rows being replaced — it lives on the OTHER device's rows, which that snapshot does
// not touch.
//
// # Hand-entered links are not ours to discard
//
// `device_links` is a different table, written only by `handleCreateLink` and config import,
// and nothing in this file reads or writes it. An operator's intent survives the cable being
// unplugged; that is the point of typing it in.
//
// # Evidence
//
// DOCUMENTED, NOT CAPTURED: no re-cabling has been performed on lab hardware to watch this
// happen. The stale-link mechanism is deduced from the two code paths named above plus the
// operator's report, and the convergence time also depends on each platform's LLDP holdtime
// — how long a removed neighbor keeps appearing in `lldpRemTable` — which is unmeasured on
// this fleet. During that window the port legitimately lists two neighbors and the existing
// flood rule already suppresses the edge, so the map shows nothing rather than the old link.
//
// Returns a NEW slice, same defensive rule as `IdentifyLLDPNeighbors`. Every verdict is
// computed from the ORIGINAL observations in ONE pass, so a refutation can never cascade:
// a row dropped here cannot go on to refute a third one, which would let one late device
// unravel a whole chain of true links.
// staleWitnessMargin is how far a claimant must have fallen behind a contradicting
// witness before the contradiction counts against it.
//
// BOTH refutation rules use it, and on purpose — one number, one meaning. Whether
// the contradiction is "somebody else now holds that port"
// ([markRefutedObservations]) or "the far end reported and found the port empty"
// ([refuteByFreshSilence]), the question being answered is the same: has the
// claimant genuinely stopped moving, or is this just the gap between two
// collectors? A single constant also keeps the two rules from converging at
// different speeds on the same cable, which would show as an edge blinking in and
// out while the two arbitrations disagreed.
//
// It is the ONE number standing between "this cable was moved" and "this device's
// collector paused for a moment", so it is deliberately generous. It has to
// exceed, added together:
//
//   - several LLDP collection cycles. SIXTY seconds each, not thirty, and this
//     line said thirty until the constants were read back: every connector
//     declares a 30 s floor on its `lldp` channel, but the cadence the controller
//     actually pushes is [wantedLLDPInterval] = 60 s, and `effectiveInterval` only
//     ever RAISES a wanted cadence to a floor — it never lowers one below it. The
//     floor is not the cadence, and it is the cadence that sets the jitter this
//     margin arbitrates;
//   - the platform's LLDP HOLDTIME — how long a removed neighbour keeps appearing
//     in the table after the cable is pulled — which is UNMEASURED on every fleet
//     platform. Nobody knows this number, and 120 s is the protocol
//     default that most implementations keep;
//   - a proxy restart, a tunnel outage, a controller redeploy.
//
// Fifteen minutes is fifteen collection cycles, and clears all three by a wide
// margin. The cost of it being too large is a stale link that lingers a quarter of
// an hour longer than it had to. The cost of it being too small is a TRUE link
// deleted from the map, which sends somebody to the wrong rack looking for a cable
// that is there — this file's header calls that the expensive mistake, and the
// asymmetry is why this is fifteen minutes and not ninety seconds.
//
// # It cannot go to ZERO, whatever the holdtimes turn out to be
//
// Worth stating, because the obvious reading of the list above is that measuring
// the holdtimes would let this constant be deleted and stale links converge in one
// cycle. TWO independent things need it, and only one of them is about holdtime:
//
//   - ASYMMETRIC LLDP, the phenomenon [refuteByFreshSilence]'s condition 2 exists
//     to protect, which nobody on this fleet has yet observed. If it is
//     ever measured never to happen, this reason does go away;
//   - ALTERNATION between two LIVE collectors, which was measured — three reads
//     differing in nothing but poll order gave links 2, 1, 2. Two devices on
//     independent 60 s cycles take turns being the fresher of the pair, and no
//     holdtime measurement changes that. This reason does NOT go away, and it puts
//     a hard floor under the constant at several collection cycles.
//
// TestStaleWitnessMarginClearsTheCollectionCadence holds that floor, so lowering
// this number past what the cadence alone justifies fails a test instead of
// quietly reintroducing the flicker.
//
// Being generous costs LESS than it looks, and the reason is worth stating because
// it is not obvious: a claim can only ever go stale if its claimant FROZE. A device
// still being collected has its rows replaced wholesale by `store.ReplaceLLDP` on
// every snapshot, so it never carries a contradiction into the next cycle — there
// is nothing for either rule to refute. Every claim these two rules actually delete
// therefore comes from a witness that is hours or days behind, not fifteen minutes.
// The margin is spent entirely on jitter it should not be arbitrating.
const staleWitnessMargin = 15 * time.Minute

// refuteByFreshSilence marks observations whose far end has since reported and
// did NOT name them.
//
// # THE STALE LINK THIS EXISTS FOR
//
// A cable is unplugged between A and B. Both stop seeing each other, both
// re-report, `ReplaceLLDP` drops both sets of rows, the edge disappears. That is
// the healthy path and it needs none of this.
//
// The broken path is when one witness FREEZES. A is no longer collected — its
// proxy went away, it was taken out of the watch, its rows came from a one-off
// CLI read that will never be repeated — so `ReplaceLLDP` never revisits them.
// A's claim "I face B:Q" sits in the table for ever, and `deriveLLDPLinks`
// faithfully keeps drawing a cable that was pulled months ago. Nothing in
// [markRefutedObservations] catches it: that function needs the far port to be
// OCCUPIED BY SOMEONE ELSE, and a port with nothing plugged into it is occupied
// by nobody.
//
// # WHY SILENCE ALONE STILL DOES NOT REFUTE
//
// This file's header commits to "silence never refutes", and that commitment is
// kept. Silence is a device with no rows, which proves nothing — a whole fleet
// platform serves no LLDP at all (SwOS, measured), and asymmetric LLDP, where one
// side transmits and the other never lists it, is the phenomenon that rule was
// founded on.
//
// What refutes here is not silence, it is a FRESHER WITNESS THAT WENT LOOKING.
// Three conditions, and each removes one way of being wrong:
//
//  1. the far device has an `lldp_self` record — it is collected, and it serves
//     LLDP. A device that never reports is still silent and still proves nothing;
//  2. that record is NEWER than the claim by [staleWitnessMargin] — the far side
//     has produced at least one full snapshot since this claim was made, while
//     the claimant has not moved. This is the condition that saves asymmetric
//     LLDP: if A is still being collected, A keeps re-reporting, A's LastSeen
//     keeps up with B's, and the margin is never cleared. Only a FROZEN witness
//     falls behind;
//  3. the far port lists NOBODY at all. If it lists somebody, that is
//     [markRefutedObservations]'s case and it is handled there with its own four
//     refusals — running both would arbitrate the same contradiction twice.
//
// # What this deliberately cannot do
//
// It cannot empty the map. If collection stops fleet-wide, every `lldp_self`
// freezes with the observations, no witness ever pulls ahead, and every link
// stays drawn. That is the correct behaviour and it falls out of the design
// rather than being guarded: the rule is RELATIVE — one witness moving on while
// another stands still — and a stopped fleet has no witness moving on.
//
// # A `source='cli'` DEVICE IS ALWAYS THE FROZEN ONE, and that is the choice
//
// Neither refutation rule reads `lldp_self.source`. The asymmetry is real all the
// same, it is entirely a consequence of freshness, and it is stated here rather
// than left to be rediscovered:
//
//   - a CLI-sourced device can never REFUTE. Its rows are stamped at read-state
//     and nothing moves them until an operator runs another one, so against any
//     SNMP-polled device it is always the older witness. Its contribution to the
//     topology is structurally subordinate. This half is a pure under-assertion —
//     the worst it does is leave a stale link drawn a while longer — and it needs
//     no guard;
//   - a CLI-sourced device is ALWAYS REFUTABLE by condition 2, and this half has
//     teeth. Condition 2's premise is "only a FROZEN witness falls behind", where
//     frozen means broken — a proxy that went away, a device dropped from the
//     watch. For a CLI device frozen is not the broken state, it is the ONLY
//     state: fifteen minutes after every read-state its claims clear the margin,
//     for ever, until the next gesture. So a one-sided S3400 link whose far end is
//     polled and lists nobody is drawn for fifteen minutes after each read-state
//     and then retired.
//
// KEPT ON PURPOSE, not overlooked. Exempting `source='cli'` from condition 2 was
// considered and rejected: this file's own account of the fault names that exact
// device as the canonical frozen witness ("its rows came from a one-off CLI read
// that will never be repeated"), so the exemption would restore the stale link for
// the one platform the rule was built for. The retirement only fires when the far
// end is collected AND reports an EMPTY port — a real S3400 link survives it
// indefinitely, because the S3400 transmits LLDP and its neighbour therefore
// occupies the port (condition 3). What gets retired is the case where the far end
// went looking and found nothing, which is what we want retired. The exposure is
// narrowed to one shape: asymmetric LLDP on a CLI-sourced device, which is the
// phenomenon never yet observed on this fleet. If it is ever
// measured, THIS is the paragraph to come back to.
//
// DOCUMENTED, NOT CAPTURED, like everything else in this section: no cable has
// been unplugged on lab hardware and watched. The margin in particular is chosen
// against an LLDP holdtime nobody has measured.
func refuteByFreshSilence(neighbors []model.LLDPNeighbor, selves []model.LLDPSelf,
	out []model.LLDPNeighbor) {

	// When was each device last heard from, as a WITNESS rather than as a subject.
	var seenAt = map[string]time.Time{}
	var i int
	for i = range selves {
		seenAt[selves[i].DeviceID] = selves[i].SeenAt
	}
	// Which local ports have at least one occupant.
	var occupied = map[lldpEndpoint]bool{}
	for i = range neighbors {
		occupied[lldpEndpoint{
			device: neighbors[i].DeviceID, port: neighbors[i].LocalPort,
		}] = true
	}

	for i = range neighbors {
		var n = neighbors[i]
		if n.PeerDevice == "" || n.PeerPort == "" {
			// Claims no endpoint precise enough to be checked.
			continue
		}
		var witness, collected = seenAt[n.PeerDevice]
		if !collected {
			// Condition 1. The far device does not report LLDP at all: silence,
			// which proves nothing and never refutes.
			continue
		}
		if occupied[lldpEndpoint{device: n.PeerDevice, port: n.PeerPort}] {
			// Condition 3. Somebody IS on that port — that contradiction belongs to
			// markRefutedObservations, with its own four refusals. Deciding it here
			// as well would arbitrate one disagreement twice, by two rules, and the
			// two could disagree.
			continue
		}
		if !witness.After(n.LastSeen.Add(staleWitnessMargin)) {
			// Condition 2. The far side has not pulled meaningfully ahead, so this
			// claimant is still keeping up and its claim is current. THE GUARD THAT
			// SAVES ASYMMETRIC LLDP.
			continue
		}
		out[i].Refuted = true
	}
}

// completeReciprocalPeerPorts fills an unresolved far port from the FAR DEVICE'S OWN
// observation, and only from there.
//
// # The defect, measured on the fleet
//
// `s3900lab:GE28` announces its neighbour's port as a bare "51", and `resolvePeerPort`
// refuses bare digits — a number in `lldpRemPortId` is whatever the far platform put in
// the TLV, and the same fleet walk found a Juniper announcing "521" for a port really
// called `ge-0/0/9.0`. So the S3900's half names one endpoint out of two. The 1930's half
// names both, exactly: `sw-labo-1930:GE51 → s3900lab:GE28`.
//
// Reciprocity in `deriveLLDPLinks` is EXACT — both halves must name the same pair of
// endpoints — so the two halves did not recognize each other and ONE cable was drawn as
// TWO one-sided links. That is what an operator sees as a double link, and it is the
// report this function answers. The same shape held `lab-crs326:sfp-sfpplus2 ↔
// sw-labo-1930:GE50`, where the flood branch turned it into a single one-sided edge
// instead.
//
// # Why this is OBSERVATION and not the arithmetic that was removed
//
// The refusal in `facade.resolveNeighborPort` exists because concatenating a bare number
// onto a facade prefix INVENTS a port name: nothing said "51" was a port index, and on
// the Juniper it was not. Nothing here computes anything. The value taken is
// `lldp_neighbors.local_port` of a row the FAR DEVICE wrote about ITSELF — the same
// column, produced by the same metrics-side rule, that the far half already uses as its
// own endpoint when it is drawn alone. The far device names the port; we copy the name.
//
// It follows that this adds NO new claim. Both rows already existed, both already agreed
// on the pair — A says "I am on pa facing B", B says "I am on GE51 facing A:pa". The
// only thing that changes is that the pair becomes RECOGNIZABLE as one cable.
//
// # The one refusal
//
// If SEVERAL of the far device's own ports name our endpoint, nothing is completed. Two
// ports of one switch claiming the same far port is an ambiguity — a loop, a flood domain
// — and picking one would be the guess this whole file refuses. Counted on distinct far
// LOCAL PORTS, not on rows: one port may legitimately carry several rows for us (the
// primary key includes the remote chassis and port), and that is one port, not two.
//
// # Why it runs AFTER refutation, and must keep running after it
//
// A completed port is second-hand: it is the far device's word about which of ITS ports
// faces us. That is exactly enough to draw a cable both sides already agree on, and NOT
// enough to delete somebody else's — and `markRefutedObservations` treats an occupant
// with no far port as a reason to disable refutation for that endpoint entirely ("this
// occupant could be the claimant itself under a spelling we failed to match"). Completing
// before refutation would silently hand that veto away and let a derived value retire an
// observation. Running after leaves both refutation rules judging exactly the rows the
// devices reported, which is what they were measured against.
//
// The value IS published on `Neighbors` — the Ports panel should show the far port it now
// knows — and that is safe for the same reason: nothing downstream of derivation
// arbitrates on it.
//
// Mutates in place, and the index is built from the rows AS THEY ARRIVE, before anything
// is completed. So a completion can never feed another one: every filled port traces back
// to a port a device stated about itself, never to one this function derived.
func completeReciprocalPeerPorts(rows []model.LLDPNeighbor) {
	// Which local ports name each endpoint completely, by the device that owns them.
	// Keyed on the endpoint CLAIMED, so an incomplete row can look itself up.
	var namers = map[lldpEndpoint]map[string]map[string]bool{}
	var i int
	for i = range rows {
		var n = rows[i]
		if n.Refuted || n.PeerDevice == "" || n.PeerPort == "" {
			continue
		}
		var claimed = lldpEndpoint{device: n.PeerDevice, port: n.PeerPort}
		if namers[claimed] == nil {
			namers[claimed] = map[string]map[string]bool{}
		}
		if namers[claimed][n.DeviceID] == nil {
			namers[claimed][n.DeviceID] = map[string]bool{}
		}
		namers[claimed][n.DeviceID][n.LocalPort] = true
	}
	for i = range rows {
		var n = rows[i]
		if n.Refuted || n.PeerDevice == "" || n.PeerPort != "" {
			// Recognized nobody, or already names both ends: nothing to complete.
			continue
		}
		var here = lldpEndpoint{device: n.DeviceID, port: n.LocalPort}
		// Only the device we say we face may answer for its own ports.
		var ports = namers[here][n.PeerDevice]
		if len(ports) != 1 {
			// Nobody said it (the far side is silent, or never resolved us), or SEVERAL
			// of its ports did. Neither is an observation of one endpoint.
			continue
		}
		var p string
		for p = range ports {
			rows[i].PeerPort = p
		}
	}
}

func markRefutedObservations(neighbors []model.LLDPNeighbor) []model.LLDPNeighbor {
	// Who occupies each local port, by index into the original slice.
	var occupants = map[lldpEndpoint][]int{}
	var i int
	for i = range neighbors {
		var here = lldpEndpoint{device: neighbors[i].DeviceID, port: neighbors[i].LocalPort}
		occupants[here] = append(occupants[here], i)
	}
	var out = make([]model.LLDPNeighbor, len(neighbors))
	copy(out, neighbors)
	for i = range neighbors {
		var n = neighbors[i]
		if n.PeerDevice == "" || n.PeerPort == "" {
			// Nothing precise enough to be contradicted: this observation claims no
			// endpoint we could check against.
			continue
		}
		var far = lldpEndpoint{device: n.PeerDevice, port: n.PeerPort}
		var rows = occupants[far]
		// Silence never refutes.
		if len(rows) == 0 {
			continue
		}
		// AN AMBIGUOUS FAR PORT NEVER REFUTES, and this guard was missing.
		//
		// deriveLLDPLinks already refuses to derive any claim from a port that
		// hears SEVERAL recognized neighbours — "saying "this port faces that
		// one" when it sees three would be choosing at random", the measured
		// case being a CRS326 reaching a 1930 through a transparent switch. This
		// function let exactly such a port ARBITRATE: it picked whichever
		// occupant had the newest LastSeen and refuted with it. So a port whose
		// own claims are too ambiguous to draw a link FROM was trusted to delete
		// a link somebody else had drawn — and a true observation was erased by
		// whichever unrelated neighbour happened to be polled last.
		//
		// The rule is the same one, applied in the same direction: a port that
		// cannot say who it faces cannot say who somebody else does not face.
		var distinct = map[string]bool{}
		var k int
		for k = range rows {
			var o = neighbors[rows[k]]
			if o.PeerDevice != "" {
				distinct[o.PeerDevice+"\x00"+o.PeerPort] = true
			}
		}
		if len(distinct) > 1 {
			continue
		}

		var newest time.Time
		var by string
		var bail bool
		var j int
		for j = range rows {
			var o = neighbors[rows[j]]
			if o.PeerDevice == "" || o.PeerPort == "" {
				// Unrecognized, or unresolved at port granularity: this occupant could be
				// the claimant itself under a spelling we failed to match.
				bail = true
				break
			}
			if o.PeerDevice == n.DeviceID && o.PeerPort == n.LocalPort {
				// The far end names US back. That is confirmation, not contradiction —
				// and it holds whatever the other occupants of that port say, which is
				// what keeps a legitimate multi-neighbor port (a hub, a phone with a
				// pass-through port) from thrashing between its speakers.
				bail = true
				break
			}
			if o.LastSeen.After(newest) {
				newest = o.LastSeen
				by = o.PeerDevice
			}
		}
		if bail || by == "" {
			continue
		}
		// Equal or older never refutes: a tie is an ambiguity, not an arbitration.
		// Nor does MARGINALLY fresher: two live collectors take turns leading by a
		// cycle, and arbitrating on that made the verdict flip on every read. The
		// witness has to have genuinely moved on, not merely gone last.
		if !newest.After(n.LastSeen.Add(staleWitnessMargin)) {
			continue
		}
		out[i].Refuted = true
		out[i].RefutedBy = by
	}
	return out
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
// # An observation may be absent from this derivation entirely
//
// A row flagged `Refuted` is dropped BEFORE anything below is counted — see
// `markRefutedObservations` for the rule. Dropping it first, rather than at the emit step,
// is deliberate: a contradicted observation must not make its port look flooded, nor supply
// a claim, nor answer a reciprocity check. It only stopped being true; it never was a
// second cable.
//
// PURE, and clockless: nothing here compares a date to `time.Now()`. Observation freshness
// is judged on the SCREEN, with the viewer's clock, because that is the only one that
// makes "seen 3 minutes ago" true for them. (Refutation compares two rows to EACH OTHER,
// both stamped by the same server clock, which leaves that property intact.)
func deriveLLDPLinks(neighbors []model.LLDPNeighbor) []model.LLDPLink {
	var kept = make([]model.LLDPNeighbor, 0, len(neighbors))
	var k int
	for k = range neighbors {
		if !neighbors[k].Refuted {
			kept = append(kept, neighbors[k])
		}
	}
	neighbors = kept

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

	// floodClaimants: for a FLOODED endpoint, the distinct OUTSIDE ends that name it
	// completely — i.e. the observations that would draw a cable INTO the flood domain
	// from somewhere else. Counted because one is a fact and several are the ambiguity.
	//
	// A flooded port hears many speakers, and the ones it can name back are settled by
	// `floodKeep` above. This map answers the mirror question, which nothing asked
	// before: when the flooded side keeps NOBODY and therefore draws nothing at all, is
	// there still exactly one complete half pointing at it? If so that cable is not
	// ambiguous from any angle and dropping it loses a real link; if there are two or
	// more, drawing them would assert several cables into one port, which is the flood
	// ambiguity `TestMultiNeighborPortDoesNotThrash` refuses to arbitrate.
	var floodClaimants = map[lldpEndpoint][]lldpEndpoint{}
	for i = range neighbors {
		var n = neighbors[i]
		if n.PeerDevice == "" || n.PeerPort == "" {
			continue
		}
		var far = lldpEndpoint{device: n.PeerDevice, port: n.PeerPort}
		if !flooded[far] {
			continue
		}
		var here = lldpEndpoint{device: n.DeviceID, port: n.LocalPort}
		var already bool
		var ci int
		for ci = range floodClaimants[far] {
			if floodClaimants[far][ci] == here {
				already = true
				break
			}
		}
		if !already {
			floodClaimants[far] = append(floodClaimants[far], here)
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
			// Far end is a flood domain. Until 2026-08-11 that alone skipped the reverse
			// half, on the reasoning that "only the floodKeep peer is drawn, and from
			// that side". THAT REASONING HOLDS ONLY WHEN THE FAR SIDE REALLY DRAWS
			// SOMETHING, and when it does not the skip erased a cable off the map —
			// the outcome this file's header calls the expensive mistake, since a link
			// that vanishes sends someone to the wrong rack looking for a cable that is
			// there. So the two situations are now separated.
			//
			// KEPT — the far side holds a unique reciprocal peer, so IT draws the pair
			// and drawing it here too would return the same cable twice. Skipped
			// whether or not that peer is us: if it is, our half is the duplicate; if
			// it is somebody else, the flooded port has already settled what it faces.
			var _, kept = floodKeep[far]
			// AMBIGUOUS — several complete halves point into the same flooded port.
			// Drawing them would assert several cables into one port, and choosing
			// between them by freshness is the map that flips at every poll
			// (TestMultiNeighborPortDoesNotThrash). An ambiguous flood domain draws
			// nothing, and that doctrine is unchanged.
			if kept || len(floodClaimants[far]) != 1 {
				continue
			}
			// UNIQUE AND UNKEPT — this is the case that used to be lost. The flooded
			// side keeps nobody, so it draws nothing whatsoever; exactly one complete
			// half names this endpoint, so there is no ambiguity to arbitrate from any
			// angle. It is drawn ONE-SIDED, from the only side able to name both ends.
			//
			// MEASURED on the fleet 2026-08-10, which is why this is a defect and not a
			// preference: `lab-crs326:sfp-sfpplus2` faces a transparent switch and hears
			// FOUR speakers, so it floods. Once `resolveNeighborPort` stopped reading
			// `sw-labo-1930`'s bare "50" as a port number, the CRS326's own half no
			// longer named the far port and so no longer qualified as the floodKeep
			// candidate — the port kept nobody. `sw-labo-1930:GE50 → lab-crs326:
			// sfp-sfpplus2`, a half naming both endpoints exactly, was then dropped
			// here, and lab-crs326 left the topology with ZERO links while its cable was
			// plugged in. The accepted cost of that change was "drawn twice,
			// one-sided" — never "gone".
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
// their MAC as port identifier, and "0x02005e1061d9" on a port cell teaches nobody where
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
	// Notes: sentences about THIS derivation, written only when the numbers beside them
	// would otherwise read as a defect. Empty on the overwhelming majority of reads, and
	// deliberately so — a field that always has something in it stops being read.
	//
	// One thing writes here today, and it is the one arithmetic in this file that
	// surprises its reader: refutation can RAISE the link count (cf. refutationNote).
	//
	// Always an array, never null — same rule as `Links` and `Silent`. A field that is
	// sometimes absent and sometimes a list is a field every caller has to special-case.
	Notes []string `json:"notes"`
}

// lldpSilence: a device whose neighborhood is UNKNOWN, and why.
type lldpSilence struct {
	DeviceID string `json:"device_id"`
	// Reason: four values, and four were needed.
	//
	// A boolean would have flattened situations repaired by OPPOSITE actions, and that is
	// the kind of shortcut that sends someone looking for a meshcraft defect where the
	// firmware lacks the feature:
	//
	//   - "platform_unsupported": the CONNECTOR declares, with its survey, that this
	//     platform does not speak LLDP. Nothing is therefore ever interrogated, and no
	//     snapshot will ever arrive. That is MikroTik on SwOS. Not repairable;
	//   - "lldp_disabled": THE DEVICE ITSELF SAID SO. A connector that reads the
	//     neighbour table over the admin session (driver.LLDPReader) came back with
	//     Enabled=false, which on the platforms that can report it is a literal
	//     refusal sentence — the FS S3400 answers "LLDP is not enabled" to "show
	//     lldp neighbor". Repairable, on the device, in one command, BY A HUMAN:
	//     meshcraft does not write it from this screen;
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
	//
	// # Why "lldp_disabled" had to be split off "not_served"
	//
	// Measured on sw01-arena (FS S3400-48T4SP, 2026-10-25), a switch that carries a
	// live 20 Gb/s uplink to the site core and appeared on the plan with NO LINK AT
	// ALL. Its `lldp_self` row is `source='cli', served=false` — the CLI read ran and
	// the device answered "LLDP is not enabled". The screen said `not_served`, whose
	// published help sentence begins "it was interrogated and returned nothing under
	// 1.0.8802": every word of that is wrong here. The agent was never asked, the MIB
	// is irrelevant on this firmware (it serves nothing under 1.0.8802 by design, cf.
	// deviceKnownMACs), and the one fact meshcraft actually held — the device's own
	// refusal — was the one the operator could have acted on.
	//
	// `lldp_self.source` is what separates them, and it is exact rather than
	// heuristic: `served` comes from `obs.Enabled` on the CLI path
	// (lldpCLISnapshot), and only a connector that can WITNESS the protocol being
	// off ever sets it false — hpe/procurve and ubiquiti/edgeswitch both declare
	// Enabled true by construction, saying so in full, because their neighbour table
	// prints the same empty rows whether LLDP is off or nothing is plugged in. So
	// `source='cli' && !served` cannot arise from a connector that merely failed to
	// look.
	Reason string `json:"reason"`
	// SeenAt: when the snapshot that said so was taken. Null when no snapshot exists —
	// therefore for "never_polled" and "platform_unsupported", where we never interrogated.
	// Set for "lldp_disabled" as for "not_served": both rest on a snapshot, and when the
	// device's refusal was recorded is the first thing an operator who has just turned
	// LLDP on will want to compare against.
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
	lldpSilencePlatform = "platform_unsupported"
	// lldpSilenceDisabled: the DEVICE answered that LLDP is off. Only a CLI
	// snapshot can carry this (cf. lldpSilence.Reason).
	lldpSilenceDisabled  = "lldp_disabled"
	lldpSilenceNotServed = "not_served"
	lldpSilenceNeverPoll = "never_polled"
	// lldpSilenceReasonHelp goes WITH the data (cf. handleListSiteLLDP): the screen must
	// be able to explain "zero neighbors" without a developer recopying the nuance into a
	// component, where it would diverge from the backend at the first rewording.
	lldpSilenceReasonHelp = "platform_unsupported: this platform's connector declares, " +
		"from hands-on survey, that it does not speak LLDP — it is therefore never " +
		"interrogated and no meshcraft version will change that; lldp_disabled: the device " +
		"itself answered that LLDP is not enabled when its neighbour table was read over " +
		"the admin session — one command on the device repairs it, and meshcraft does not " +
		"write it from here; not_served: its SNMP agent was interrogated and returned " +
		"nothing under 1.0.8802, which contradicts its connector's declaration or signals " +
		"LLDP disabled on the device; never_polled: no snapshot has arrived yet " +
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
	var drv, known = driver.Get(driver.DriverNameFromExtra(d.Extra))
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

// refutationNote says out loud the one case where this file's arithmetic runs backwards:
// discarding observations DREW MORE CABLES than keeping them.
//
// # The mechanism, because nobody guesses it
//
// `deriveLLDPLinks` drops refuted rows FIRST, before it counts how many remotes each local
// port heard — deliberately, and that function says why: a contradicted observation must not
// make its port look flooded. But the flood threshold is a COUNT. A port with two occupants
// is a flood domain and draws nothing; take one occupant away as refuted and the port has
// one occupant, is no longer flooded, and the edge the flood rule was suppressing appears.
// Retiring one stale claim can therefore ADD an edge, and can add more than the refutation
// removed. Verified by probe: one link before, two after.
//
// # Why a sentence and not a fix
//
// The behaviour is right. The flood was an artifact of a row that had stopped being true, and
// once that row goes the port really does have a single neighbour — suppressing the edge
// would mean letting a stale observation go on hiding a live cable. Nothing to repair.
//
// What is wrong is only that it happens in silence. `counts.refuted` is published so a
// support read can reconcile "N observations listed, M edges drawn"; hand somebody a count of
// DISCARDED observations and they will expect the edges to have gone down. When they have
// gone UP instead, the response looks like a derivation bug, and the next hour is spent
// looking for one.
//
// Returns "" when nothing surprising happened, which is almost always — including the common
// unchanged case, where refuting one half of a bilateral link leaves the other half drawn
// one-sided and the total does not move. Only a STRICT increase earns a sentence; a note that
// fires on every read is a note nobody reads.
//
// The counterfactual derivation is run ONLY when something was actually refuted. It doubles
// the cost of the derivation on those reads and that is accepted: `deriveLLDPLinks` is
// already the quadratic part of this path, and a site with zero refuted rows — the ordinary
// site — pays nothing at all.
func refutationNote(identified []model.LLDPNeighbor, drawn int) string {
	var refuted int
	var i int
	for i = range identified {
		if identified[i].Refuted {
			refuted++
		}
	}
	if refuted == 0 {
		return ""
	}
	// The same rows, judged as if nothing had been contradicted. A copy: the caller's
	// slice is what the response lists, and its flags must survive.
	var raw = make([]model.LLDPNeighbor, len(identified))
	copy(raw, identified)
	for i = range raw {
		raw[i].Refuted = false
	}
	var without = len(deriveLLDPLinks(raw))
	if drawn <= without {
		return ""
	}
	return fmt.Sprintf("refutation discarded %d observation(s) and the link count ROSE, "+
		"from %d to %d. That is not a derivation error: a discarded row no longer counts "+
		"toward its port's multi-neighbour flood threshold, so a port that was suppressed "+
		"as a flood domain can fall back to a single neighbour and draw the edge the flood "+
		"rule was hiding. The stale observation was what made the port look flooded.",
		refuted, without, drawn)
}

// buildLLDPView assembles the view. PURE: this is the function tests exercise, and the
// route above is only a database read and a `respond`.
func buildLLDPView(neighbors []model.LLDPNeighbor, selves []model.LLDPSelf,
	devices []model.Device) lldpView {
	// Order is a constraint, not a preference: refutation reasons about `PeerDevice` /
	// `PeerPort`, so it can only run once identification has filled them, and it must run
	// before derivation, which is what it removes edges from.
	//
	// Refuted rows STAY in `Neighbors`. The device really did report that neighbor, the
	// Ports panel must keep showing it, and hiding it would make a link disappear from the
	// map with nothing on screen to explain why.
	var identified = markRefutedObservations(IdentifyLLDPNeighbors(neighbors, selves, devices))
	// The SECOND refutation rule, and it catches a case the first structurally
	// cannot: a witness that FROZE. markRefutedObservations needs the far port to
	// be occupied by somebody else, so an unplugged port — occupied by nobody —
	// never contradicts anything, and a device that stopped being collected keeps
	// asserting its last cable for ever.
	//
	// Applied AFTER, over the same identified rows, and it only ever sets Refuted
	// — never clears it. The two rules are therefore additive and order-free
	// between themselves, which matters because they arbitrate different
	// contradictions and must not be able to overturn each other.
	refuteByFreshSilence(identified, selves, identified)
	// THEN, and only then, complete the far ports the far devices themselves named. This
	// runs after both refutation rules on purpose — a completed port is the far device's
	// word about its own port, enough to draw a cable both sides agree on and not enough
	// to retire somebody else's. `completeReciprocalPeerPorts` says why at length.
	completeReciprocalPeerPorts(identified)
	var out = lldpView{
		Neighbors: identified,
		Links:     deriveLLDPLinks(identified),
		Silent:    []lldpSilence{},
		Notes:     []string{},
	}
	if out.Links == nil {
		out.Links = []model.LLDPLink{}
	}
	// Refutation removing edges needs no explanation; refutation ADDING them does, and
	// this is the only place that can tell, because it is the only place holding both the
	// judged rows and the count they produced.
	var note = refutationNote(identified, len(out.Links))
	if note != "" {
		out.Notes = append(out.Notes, note)
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
		var reason, declared = lldpPlatformSilent(devices[i])
		if declared {
			out.Silent = append(out.Silent, lldpSilence{
				DeviceID: devices[i].ID, Reason: lldpSilencePlatform, Detail: reason})
			continue
		}
		if !polled {
			out.Silent = append(out.Silent, lldpSilence{
				DeviceID: devices[i].ID, Reason: lldpSilenceNeverPoll})
			continue
		}
		if !s.Served {
			// WHO said nothing is the whole distinction, and `source` is the only
			// column that holds it. A CLI snapshot with Served=false is the DEVICE's
			// own answer — "LLDP is not enabled" — and names an action an operator
			// can take tonight. An SNMP one is an empty 1.0.8802 subtree, which is
			// either a connector declaration to fix or the same device-side fact
			// seen through a channel that cannot tell the two apart. Reporting the
			// first as the second publishes a sentence about an OID that was never
			// queried (cf. lldpSilence.Reason).
			var at = s.SeenAt
			var why = lldpSilenceNotServed
			if s.Source == lldpSourceCLI {
				why = lldpSilenceDisabled
			}
			out.Silent = append(out.Silent, lldpSilence{
				DeviceID: devices[i].ID, Reason: why, SeenAt: &at})
		}
	}
	sort.Slice(out.Silent, func(a, b int) bool {
		return out.Silent[a].DeviceID < out.Silent[b].DeviceID
	})
	return out
}
