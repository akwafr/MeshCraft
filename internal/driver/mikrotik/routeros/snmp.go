package routeros

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"meshcraft/internal/driver"
)

// SNMP on RouterOS.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ READS exercised on the reference CRS326 (captures in testdata/).            │
// │ WRITES NOT VERIFIED.                                                        │
// │                                                                            │
// │ And one more limit that must be said: no OID has been read on this         │
// │ device. SNMP is UDP; the only channel available from the development host  │
// │ is HTTP read. This file configures SNMP; it does not prove that collection │
// │ works.                                                                     │
// └─────────────────────────────────────────────────────────────────────────────┘
//
// WHAT IS OBSERVED, and that is what founds the SNMPVersions declaration of the
// contract:
//
//	/snmp            → {"enabled":"true","engine-id":"80003a8c04",
//	                    "trap-community":"qgK3hB8sePXOMI5","trap-generators":"temp-exception",
//	                    "trap-target":"","trap-version":"1","src-address":"::"}
//	/snmp/community  → [{".id":"*0","name":"qgK3hB8sePXOMI5","default":"true",
//	                     "addresses":"::/0","read-access":"true","write-access":"false",
//	                     "security":"none","authentication-protocol":"MD5",
//	                     "encryption-protocol":"DES",
//	                     "authentication-password":"","encryption-password":""}]
//
// THREE things to take from that, two of them counter-intuitive:
//
//  1. v3 USERS LIVE IN `/snmp/community`. There is no `/snmp/user` — that path
//     returns 500 (observed). The same record carries the v1/v2c community name AND,
//     when `security` is something other than `none`, the v3 credentials. Looking for
//     a v3 user menu is a waste of time.
//  2. The `engine-id` IS ALREADY THERE. An engine ID only makes sense for v3: the
//     machinery is initialized on this firmware. That is the strongest argument in
//     favour of v3 — and it is not enough; see the SNMPVersions comment in
//     routeros.go for why declaring it is refused.
//  3. TRAPS DO NOT CARRY LINKS. `trap-generators` is `temp-exception`: only
//     temperature alerts leave. Getting linkUp/linkDown requires writing
//     `trap-generators=interfaces`, which no campaign has done yet. The `if_state`
//     cadence therefore cannot rest on traps today.

// Community is an SNMP community as the device carries it.
type Community struct {
	Name string `json:"name"`
	// Addresses bounds the allowed sources. Observed: `::/0`, therefore ALL —
	// including from outside the management VLAN. That is an important difference
	// from the Instant On line, which confines SNMP to the administration VLAN:
	// here it is a setting, not a constraint, and leaving it open is a choice an
	// operator should make knowingly.
	Addresses string `json:"addresses,omitempty"`
	Read      bool   `json:"read"`
	Write     bool   `json:"write"`
	// Security: `none` for v1/v2c, `authorized` or `private` for v3.
	Security string `json:"security,omitempty"`
	// AuthProtocol / PrivProtocol: the v3 algorithms. Observed at MD5 and DES, but
	// those are the field DEFAULTS — not the list of what it accepts. See the
	// SNMPVersions comment in routeros.go: it is precisely because that list could
	// not be established that v3 is not declared.
	AuthProtocol string `json:"auth_protocol,omitempty"`
	PrivProtocol string `json:"priv_protocol,omitempty"`
	// Default: the entry created by the device itself. Observed `"default":"true"`
	// on the sole community. RouterOS REFUSES to delete its default entry: modifying
	// it is the only path, and that is what EnsureCommunity does.
	Default bool   `json:"default,omitempty"`
	RowID   string `json:"-"`
}

// SNMPState is the device's SNMP configuration.
type SNMPState struct {
	Enabled     bool   `json:"enabled"`
	Contact     string `json:"contact,omitempty"`
	Location    string `json:"location,omitempty"`
	EngineID    string `json:"engine_id,omitempty"`
	TrapTarget  string `json:"trap_target,omitempty"`
	TrapVersion string `json:"trap_version,omitempty"`
	// TrapGenerators says WHAT generates a trap. Observed: `temp-exception`
	// only — so not link state changes. The field is reported whole rather than
	// reduced to a boolean, because it is its VALUE that decides whether
	// supervision can count on traps.
	TrapGenerators []string    `json:"trap_generators,omitempty"`
	TrapCommunity  string      `json:"trap_community,omitempty"`
	Communities    []Community `json:"communities,omitempty"`
}

// PushesLinkTraps says whether the device generates traps on link state change.
//
// False on the reference device, and that is the point that matters for planning:
// without that, `if_state` must be polled, not awaited.
func (s SNMPState) PushesLinkTraps() bool {
	if s.TrapTarget == "" {
		return false // no collector: traps go nowhere
	}
	var g string
	for _, g = range s.TrapGenerators {
		if g == "interfaces" {
			return true
		}
	}
	return false
}

// ReadSNMP reads the SNMP configuration. READ ONLY.
//
// VALIDATED LIVE against 192.0.2.54: returns SNMP enabled, an engine ID, a
// default read-only community open to `::/0`, and traps limited to temperature
// alerts with no destination.
func (c *Client) ReadSNMP(ctx context.Context) (SNMPState, error) {
	var out SNMPState
	var row, err = c.Object(ctx, "snmp")
	if err != nil {
		return out, err
	}
	out.Enabled = row.Bool("enabled")
	out.Contact = row.Get("contact")
	out.Location = row.Get("location")
	out.EngineID = row.Get("engine-id")
	out.TrapTarget = row.Get("trap-target")
	out.TrapVersion = row.Get("trap-version")
	out.TrapCommunity = row.Get("trap-community")
	out.TrapGenerators = SplitList(row.Get("trap-generators"))

	var rows []Row
	rows, err = c.List(ctx, "snmp/community")
	if err != nil {
		return out, err
	}
	var r Row
	for _, r = range rows {
		out.Communities = append(out.Communities, Community{
			Name:         r.Get("name"),
			Addresses:    r.Get("addresses"),
			Read:         r.Bool("read-access"),
			Write:        r.Bool("write-access"),
			Security:     r.Get("security"),
			AuthProtocol: r.Get("authentication-protocol"),
			PrivProtocol: r.Get("encryption-protocol"),
			Default:      r.Bool("default"),
			RowID:        r.ID(),
		})
	}
	sort.Slice(out.Communities, func(i, j int) bool {
		return out.Communities[i].Name < out.Communities[j].Name
	})
	c.step(true, "SNMP configuration read",
		fmt.Sprintf("enabled=%t, %d community(ies), traps to '%s' (generators: %s)",
			out.Enabled, len(out.Communities), out.TrapTarget,
			JoinList(out.TrapGenerators)))
	return out, nil
}

// EnsureCommunity sets an SNMP v2c community READ ONLY.
//
// NOT VERIFIED ON HARDWARE (write).
//
// v2c and not v3, and read-only and not write: both are decisions, not shortcuts.
//
//   - v2c because that is what the contract declares for this platform
//     (SNMPVersions in routeros.go), and setting a v3 the contract does not announce
//     would produce on the device an identity meshcraft would know nothing about.
//   - read-only because meshcraft writes NOTHING via SNMP — all its configuration
//     goes through REST. A write community would be an attack surface offered with no
//     use, on a protocol where the secret travels in the clear.
//
// The DEFAULT entry case is handled separately: RouterOS creates one (observed
// `"default":"true"`) and refuses that it be deleted. When the target community
// does not yet exist, we MODIFY the default entry rather than stacking a second
// one — otherwise the device would end up with a forgotten community, born from
// the factory, still active and known to nobody.
func (c *Client) EnsureCommunity(ctx context.Context, name, addresses string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("empty community name")
	}
	if addresses == "" {
		// No default invented here: 'all sources' is an operational choice, not a
		// technical default. The caller must say it.
		return fmt.Errorf("no source bound for community '%s': "+
			"leaving SNMP open to all addresses must be requested explicitly", name)
	}

	var state, err = c.ReadSNMP(ctx)
	if err != nil {
		return err
	}

	var fields = map[string]string{
		"name":         name,
		"addresses":    addresses,
		"read-access":  "yes",
		"write-access": "no",
		// `security=none` is what makes this entry a v1/v2c community rather than a
		// v3 user. Making it explicit avoids inheriting a value left by a previous
		// configuration.
		"security": "none",
	}

	var existing *Community
	var fallback *Community
	var i int
	for i = range state.Communities {
		if state.Communities[i].Name == name {
			existing = &state.Communities[i]
			break
		}
		if state.Communities[i].Default {
			fallback = &state.Communities[i]
		}
	}
	switch {
	case existing != nil:
		_, err = c.Set(ctx, "snmp/community", existing.RowID, fields)
	case fallback != nil:
		c.step(true, "reusing the device's default community",
			"'"+fallback.Name+"' is renamed rather than left active alongside: "+
				"RouterOS refuses to delete its default entry")
		_, err = c.Set(ctx, "snmp/community", fallback.RowID, fields)
	default:
		_, err = c.Add(ctx, "snmp/community", fields)
	}
	if err != nil {
		return err
	}

	// SNMP must be ENABLED, otherwise the community is useless. Observed already at
	// `true` on the reference device, but a factory-fresh device does not
	// necessarily have it.
	if !state.Enabled {
		_, err = c.Command(ctx, "snmp", "set", map[string]string{"enabled": "yes"})
		if err != nil {
			return fmt.Errorf("community set but SNMP remains disabled: %w", err)
		}
		c.step(true, "SNMP enabled", "")
	}
	c.step(true, "SNMP community '"+name+"' set",
		"read-only, sources "+addresses)
	return nil
}

// --- What the contract expects of an SNMP apply ---

// trapPort is the only destination port this platform can aim at.
//
// 162, and that is not a default: it is a CONSTRAINT. The `trap-target` field of
// `/snmp` carries only ADDRESSES (observed: `"trap-target":""`, and RouterOS
// documentation describes no other form). There is nowhere to write a port number,
// so nothing that can divert traps elsewhere than the standard port.
const trapPort = 162

// ApplySNMP sets the SNMP identity on the device.
//
// NOT VERIFIED ON HARDWARE for the write part (cf. the header of this file).
//
// THREE REFUSALS, and all three arrive BEFORE the device is contacted for what
// they can. That is what the contract asks: an identity the platform cannot carry
// is REFUSED, never approximated to the 'nearest'.
//
//  1. v3 IS REFUSED. Not because the platform does not have it — it does, and the
//     observation is clear: `/snmp` carries an `engine-id` (`80003a8c04`), which only
//     makes sense FOR v3, and `/snmp/community` carries `security`,
//     `authentication-protocol` and `encryption-protocol`. The machinery is there,
//     initialized. It is refused because this connector does not DECLARE v3 in its
//     capabilities (cf. SNMPVersions in routeros.go), and for two cumulative reasons:
//     no write has been exercised on hardware, and the ALGORITHMS actually accepted
//     could not be established — the observed MD5/DES values are the field DEFAULTS,
//     not the list of what it admits.
//     Accepting v3 in silence would therefore produce, in the best case, an identity
//     that exists in meshcraft's database and NOWHERE ELSE. That is worse than an
//     absent identity: the fleet would believe its supervision authenticated.
//  2. A COMMUNITY WITHOUT A SOURCE BOUND IS REFUSED, naming the missing gesture.
//     RouterOS carries an `addresses` field on every community, and the observation
//     shows `::/0` on the reference device — therefore ALL sources, including outside
//     the management VLAN. That is not a platform constraint, it is a SETTING, and
//     'leave SNMP open to all addresses' is a choice an operator must make
//     knowingly. Inventing `::/0` as a default would be deciding for them, and doing
//     it in silence. The difference in nature with the Instant On line is
//     instructive: that one confines SNMP to the administration VLAN by construction,
//     there is nothing to bound.
//  3. AN EMPTY IDENTITY SETS NOTHING, and says so. An empty field means 'nothing to
//     do here', never 'remove': an apply is not a reset, and erasing what one is
//     not asked to set would cut a third party's supervision.
//
// THE AGENT IS TURNED ON ALONG THE WAY when it is not: setting an identity on a
// turned-off agent would give a configured and mute supervision, which is the worst
// of both worlds — nothing answers, and nothing says why. It is
// [Client.EnsureCommunity] that handles it, and [SNMPResult.Enabled] that reports it.
//
// `GroupCreated` STAYS FALSE, always: RouterOS has no SNMP access group. Its v3
// users live in `/snmp/community` (there is no `/snmp/user` — that path returns 500,
// observed), and there is therefore no object that field could describe. Filling it
// on a platform that has no such notion would be a capability that lies.
func (c *Client) ApplySNMP(ctx context.Context, want SNMPIdentity) (SNMPResult, error) {
	var out SNMPResult

	if strings.TrimSpace(want.User) != "" {
		return out, driver.NotSupported((&Driver{}).Info().Name,
			"set an SNMPv3 user",
			"the v3 machinery EXISTS on this firmware (observed: `/snmp` carries an "+
				"`engine-id`, and `/snmp/community` carries `security`, "+
				"`authentication-protocol` and `encryption-protocol`), but this connector "+
				"does not declare it: no write has been exercised on hardware, and the "+
				"algorithms actually accepted could not be established — the observed "+
				"MD5/DES are the field DEFAULTS. Setting a v3 under these conditions "+
				"would create an identity that exists in meshcraft's database and nowhere "+
				"else. To reopen after the write campaign: it is the only declaration of "+
				"this connector that campaign must change")
	}

	if strings.TrimSpace(want.Community) != "" && len(want.Sources) == 0 {
		return out, fmt.Errorf("SNMP community '%s' not set: no source bound "+
			"was supplied, and RouterOS requires filling the community `addresses` field. "+
			"MISSING GESTURE: indicate the prefixes allowed to query the agent — typically "+
			"that of the supervising proxy. Leaving SNMP open to ALL addresses is possible "+
			"on this platform (observed `::/0` on the reference device) but must be "+
			"requested explicitly, by passing `0.0.0.0/0,::/0`: meshcraft does not invent "+
			"a default nobody chose", want.Community)
	}

	// Read first: it serves to report the agent state honestly, and to NOT rewrite
	// a community already conforming.
	var state, err = c.ReadSNMP(ctx)
	if err != nil {
		return out, err
	}
	out.Enabled = state.Enabled

	if strings.TrimSpace(want.Community) == "" {
		out.Skipped = append(out.Skipped, "No SNMP identity to set: the plan asks for "+
			"neither a v2c community nor a v3 user. Nothing was written — and nothing was "+
			"REMOVED either: an apply is not a reset, and erasing a community one is not "+
			"asked to set would cut a third party's supervision.")
		return out, nil
	}

	var addresses = JoinList(want.Sources)
	if snmpConformant(state, want.Community, addresses) {
		// Going back over an already-qualified fleet must rewrite NOTHING: that is one
		// fewer round-trip, and on a platform that persists immediately, a write one
		// does not need to do is a chance to err that one does not take.
		c.step(true, "SNMP community '"+want.Community+"' already conforming",
			"sources "+addresses+", read-only, agent active — nothing to write")
		return out, nil
	}

	err = c.EnsureCommunity(ctx, want.Community, addresses)
	if err != nil {
		return out, err
	}
	out.CommunitySet = true
	// The agent is active at the end of the operation: EnsureCommunity turns it on if
	// it was not, and fails explicitly if the turn-on is refused.
	out.Enabled = true
	return out, nil
}

// snmpConformant says whether the device ALREADY carries the requested identity,
// agent included.
//
// The four conditions are those [Client.EnsureCommunity] writes, and all four are
// needed: a community of the right name but open to all addresses, or authorized
// for write, or switched to v3 by a previous configuration, is not the requested
// identity — and leaving it in place while reporting 'already conforming' would
// leave a door open that nobody would see.
func snmpConformant(state SNMPState, name, addresses string) bool {
	if !state.Enabled {
		return false
	}
	var comm Community
	for _, comm = range state.Communities {
		if comm.Name != name {
			continue
		}
		return comm.Addresses == addresses && comm.Read && !comm.Write &&
			comm.Security == "none"
	}
	return false
}

// ApplyTrapReceiver declares the collector as a trap destination.
//
// NOT VERIFIED ON HARDWARE (write).
//
// WHAT THIS WRITE REALLY CHANGES, observed on the reference device:
//
//	trap-target     ""               → there is NO destination, so no trap leaves
//	                                   this device today
//	trap-generators "temp-exception" → only TEMPERATURE alerts generate a trap.
//	                                   Link state changes are not part of it
//	trap-version    "1"              → and meshcraft only sets v2c
//
// Hence three gestures in a single write, not one.
//
// EXISTING GENERATORS ARE PRESERVED. Writing `trap-generators=interfaces` alone
// would remove `temp-exception`, therefore temperature alerts, which nobody asked
// to cut. Same principle as mergeMembers in apply.go: on a field that is written as
// a block, 'leave alone' demands an explicit effort. And THIRD-PARTY DESTINATIONS
// TOO are preserved — cutting someone else's supervision to install one's own would
// be the worst of services.
//
// `trap-version` IS FORCED TO 2. v1 traps do not carry the same fields as a v2c: a
// collector configured for v2c would ignore them IN SILENCE, which would give an
// exact configuration and zero alerts. Leaving the observed value (`1`) would
// therefore be setting a mute supervision.
//
// A PORT OTHER THAN 162 IS REFUSED, never ignored. `trap-target` carries only
// ADDRESSES: there is nowhere to write a port number. Accepting a value one would
// not write would make one wait for traps on a mute port — i.e. a supervision that
// stops without a word, whose only witness would be a DEBUG counter on the proxy.
//
// THE COMMUNITY IS MANDATORY, and the constraint comes from THE COLLECTOR and not
// the device: the proxy only accepts a trap if the community it carries equals the
// one with which it QUERIES the device. A distinct trap community, even dedicated,
// even 'clean', would make 100 % of the fleet's traps be rejected. The firmware,
// for its part, does not require it — `trap-community` is free text that it does
// not match against any `/snmp/community` entry.
//
// `Replaced` STAYS FALSE, always, and that is a platform fact: on RouterOS a
// destination is a simple address in a list, with no attached identity. There is
// therefore no 'divergent entry carrying the same address' that would need to be
// removed before adding one's own — the case that field describes does not exist
// here.
//
// AND THE RESULT IS ONLY A CLAIM: the line is in the device, which does not prove
// that its traps arrive. The proof is the first trap received, and it belongs to
// the caller — a firewall, an asymmetric route or an ACL on the management VLAN
// suffice to emit into the void.
func (c *Client) ApplyTrapReceiver(ctx context.Context,
	want TrapDestination) (TrapResult, error) {
	var out = TrapResult{Version: "v2c", Address: want.Address, Port: trapPort}
	var name = (&Driver{}).Info().Name

	// --- Refusals, BEFORE contacting the device ---

	if strings.TrimSpace(want.User) != "" {
		return out, driver.NotSupported(name, "declare an SNMPv3 trap destination",
			"this connector declares only v1/v2c (cf. SNMPVersions in routeros.go): "+
				"no v3 user is set on this platform, so none can sign a trap. RouterOS does "+
				"NOT verify that the name exists — the field is free text — so a misnamed "+
				"v3 destination would register without error and emit nothing authenticable, "+
				"a silence no screen distinguishes from a quiet network")
	}
	if want.Port != 0 && want.Port != trapPort {
		return out, fmt.Errorf("trap destination on port %d refused: RouterOS's "+
			"`trap-target` field carries only ADDRESSES, there is no port field to write. "+
			"Accepting this value would make one wait for traps on a mute port. GESTURE TO "+
			"MAKE: have the collector listen on %d/udp, the only port this device can aim "+
			"at", want.Port, trapPort)
	}
	if strings.TrimSpace(want.Address) == "" {
		return out, fmt.Errorf("no collector address supplied: it is ASKED for and " +
			"not deduced — only the proxy kernel has the routing table that says by which " +
			"address it reaches this device")
	}
	if strings.TrimSpace(want.Community) == "" {
		return out, fmt.Errorf("no trap community supplied: it must be the one with " +
			"which the proxy QUERIES the device, otherwise the collector will reject 100 %% " +
			"of traps — silently, with as sole witness a DEBUG counter on the proxy. " +
			"RouterOS, for its part, does not require it: `trap-community` is free text " +
			"that it does not match against any `/snmp/community` entry. The constraint " +
			"comes from the collector, not the device")
	}

	// --- Current state ---

	var state, err = c.ReadSNMP(ctx)
	if err != nil {
		return out, err
	}
	var targets = SplitList(state.TrapTarget)
	// How many destinations the device carried BEFORE the write. The ceiling this
	// platform accepts is documented nowhere: this counter is the only measure one
	// will ever have of it — that of the last accepted add, and that of the first
	// refused.
	out.Known = len(targets)

	if !state.Enabled {
		// A destination set on a turned-off agent gives an exact configuration and
		// total silence. It is [Client.ApplySNMP] that turns the agent on, and the
		// order between the two is not negotiable.
		return out, fmt.Errorf("SNMP agent DISABLED on this device: a trap " +
			"destination would be set exactly there and would emit nothing — a correct " +
			"configuration and total silence. GESTURE TO MAKE FIRST: set the SNMP " +
			"identity, which turns the agent on along the way. The order between the two " +
			"is not negotiable. Observed on the reference device, the agent is ACTIVE: a " +
			"device where it is not probably comes from the factory or a reset")
	}

	// --- What we want, computed without losing anything ---

	var newTargets = insertOnce(append([]string{}, targets...), want.Address)
	sort.Strings(newTargets)
	var generators = append([]string{}, state.TrapGenerators...)
	generators = insertOnce(generators, "interfaces")
	sort.Strings(generators)

	out.Notes = append(out.Notes, "Only port 162/udp is reachable: RouterOS's "+
		"`trap-target` field carries only addresses, with no port number.")
	if !containsString(state.TrapGenerators, "interfaces") {
		out.Notes = append(out.Notes, fmt.Sprintf("Trap generators BEFORE write: "+
			"%s — link state changes were NOT part of them, so no linkUp/linkDown left "+
			"this device. `interfaces` was added, and existing generators are kept: "+
			"cutting temperature alerts that nobody asked to cut would be a silent "+
			"regression.",
			JoinList(state.TrapGenerators)))
	}
	if state.TrapVersion != "2" {
		out.Notes = append(out.Notes, fmt.Sprintf("`trap-version` was '%s' and is "+
			"forced to 2: meshcraft only sets v2c, and a collector configured for v2c "+
			"ignores v1 traps IN SILENCE, which do not carry the same fields.",
			state.TrapVersion))
	}
	if out.Known > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d destination(s) already present "+
			"(%s): none was removed. Cutting a third party's supervision to install "+
			"one's own would be the worst of services.", out.Known, JoinList(targets)))
	}

	// --- Already conforming? ---
	if sameMembers(targets, newTargets) && state.TrapVersion == "2" &&
		state.TrapCommunity == want.Community &&
		containsString(state.TrapGenerators, "interfaces") {
		c.step(true, "trap destination already declared", "'"+want.Address+
			"', community '"+want.Community+"', v2c — nothing to write")
		out.Notes = append(out.Notes, "The device already carried this destination: "+
			"nothing was rewritten. That does not prove that its traps ARRIVE — the only "+
			"proof is the first trap received by the collector.")
		return out, nil
	}

	_, err = c.Command(ctx, "snmp", "set", map[string]string{
		"trap-target":     JoinList(newTargets),
		"trap-community":  want.Community,
		"trap-generators": JoinList(generators),
		"trap-version":    "2",
	})
	if err != nil {
		return out, err
	}

	// THE REREAD IS AUTHORITATIVE, not the return code. On a platform whose writes
	// have not been exercised on hardware, a 2xx that changes nothing is the failure
	// mode to catch — and the only way to catch it is to reread.
	var after SNMPState
	after, err = c.ReadSNMP(ctx)
	if err != nil {
		out.Notes = append(out.Notes, "Write accepted but NOT REREAD ("+err.Error()+
			"): meshcraft cannot confirm that the device retained it.")
		return out, nil
	}
	out.Set = containsString(SplitList(after.TrapTarget), want.Address) &&
		after.TrapVersion == "2" && after.TrapCommunity == want.Community &&
		containsString(after.TrapGenerators, "interfaces")
	if !out.Set {
		out.Notes = append(out.Notes, fmt.Sprintf("WRITE ACCEPTED BUT NOT CONFIRMED "+
			"by the reread: the device carries trap-target '%s', trap-version '%s', "+
			"generators '%s'. A 2xx that changes nothing is a silent failure, and that "+
			"is what this reread exists to catch.",
			after.TrapTarget, after.TrapVersion, JoinList(after.TrapGenerators)))
		return out, nil
	}
	out.Notes = append(out.Notes, "The line is in the device, which does NOT prove "+
		"that its traps arrive: a firewall, an asymmetric route or an ACL on the "+
		"management VLAN suffice to emit into the void. The proof is the first trap "+
		"received.")
	c.step(true, "trap destination declared",
		"'"+want.Address+"' on "+JoinList(generators)+", v2c")
	return out, nil
}

// containsString says whether a slice contains a string. The counterpart of
// containsInt for RouterOS lists, which are comma-separated strings.
func containsString(list []string, want string) bool {
	var s string
	for _, s = range list {
		if s == want {
			return true
		}
	}
	return false
}
