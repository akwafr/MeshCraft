// Package eos is the Arista EOS connector knowledge base.
//
// # The status of what is written here, said before anything else
//
// Almost every CLI form below is DOCUMENTED, not captured — the same standing as
// the Cisco IOS connector next door, and the same rule applies: what has not been
// seen is announced as such, in the transcript, at every login (cf.
// [Client.postLogin]), and in [Driver.ResetProcedure] via `Confirmed: false`.
// The forms come from Arista's own command reference and from the ZTP chapter of
// the EOS User Manual. TODO.md names, one by one, the captures that would flip
// each declaration.
//
// WHAT HAS BEEN CAPTURED, against a DCS-7050SX-64-R running EOS 4.28.13.1M
// ([CapturedRelease]) — `core01.lab`, the lab switch:
//
//   - 2026-08-07, the READ and TELEMETRY half: the gNMI enable block (gnmi.go),
//     the `show version` identity read, `show zerotouch`, `show lldp neighbors`
//     and the four streaming channels, all exercised end to end;
//   - 2026-08-08, the FIRST TWO WRITE GESTURES: [Client.EnsureAccount] created
//     `mcadmin` with `privilege 15 role network-admin`, removed the enable
//     password, deleted the previous account and persisted with `write memory`
//     (its first attempt failed for a real reason — see [NoEnableCmds]); and
//     [Client.ApplyStormControl] wrote all three classes to an empty Ethernet1
//     cage, read them back, converged on a second apply and removed one class
//     cleanly. `storm_control` is the one intent [Driver.WritesExtra] declares;
//   - 2026-08-08, two STP reads: `show spanning-tree` and `show spanning-tree
//     mst configuration`, pinned as fixtures in stp_live_test.go. One of them
//     found a read defect no invented fixture would have (cf. `reMSTName`).
//
// Everything else is still documented only. The spanning-tree WRITES, DHCP
// snooping, port admin, the trap channel and the management push have never
// reached a switch, and [Driver.WritesExtra] stays false for each of them.
//
// Two small reads are worth carrying forward because they contradict what a
// reader would assume:
//
//   - `show zerotouch` prints "ZeroTouch Mode: Disabled" on a configured
//     switch. [reZTPActive] correctly does not match it — verified rather than
//     hoped — and would match the "Enabled" spelling;
//   - the fleet's `admin` account lands on user EXEC ("core01-lab>",
//     privilege 1) and `enable` PROMPTS FOR A PASSWORD, which the account's own
//     secret satisfies. That is exactly the path [Driver.Authenticate] takes when
//     `cli.priv` is false, and it is now known to work rather than assumed.
//
// # Why this is NOT a copy of cisco/ios, despite the family resemblance
//
// EOS is deliberately IOS-SHAPED — `configure`, `interface`, `switchport mode
// trunk`, `write memory` all behave as an IOS hand expects — and that surface
// similarity is exactly what makes a shared command set dangerous. The five
// places the two CLIs genuinely disagree are all places where the IOS form is
// silently WRONG on EOS:
//
//  1. THE ADDRESS IS A PREFIX. `ip address 192.0.2.5/24` — EOS takes CIDR and
//     refuses the dotted mask IOS demands. The connector therefore carries no
//     mask conversion at all on the write side, and [PrefixFromMask] exists only
//     to read a foreign configuration back;
//  2. THERE IS NO `ip default-gateway`. EOS is a routing platform end to end;
//     the default route is `ip route 0.0.0.0/0 <next-hop>`, and the IOS line is
//     rejected outright rather than quietly ignored;
//  3. THERE IS NO TRUNK ENCAPSULATION. EOS speaks 802.1Q and nothing else, so
//     the ordered-candidate dance cisco/ios needs for 3560-vs-2960 has no
//     counterpart here — one form, no fallback, no ambiguity to record;
//  4. SSH NEEDS NO KEY CEREMONY. EOS generates its host key on first boot and
//     runs SSH by default, so there is no `ip domain-name`, no `crypto key
//     generate rsa`, and no non-crypto image that can strand a switch on the
//     console. This removes the single longest step of the IOS provisioning
//     push, and the single most common way it fails;
//  5. A FACTORY SWITCH IS NOT AT A PROMPT — IT IS IN ZTP. See below. This is
//     the difference that reshapes the whole serial provisioning flow, and it
//     has no equivalent anywhere else in the fleet.
//
// # ZTP: the state a factory Arista is actually in
//
// An EOS switch with NO startup-config does not boot to a login prompt with an
// empty configuration, the way every other range in this product does. It boots
// into ZERO TOUCH PROVISIONING: it brings every port up, sends DHCP on all of
// them looking for a boot file, fails, waits, and retries — for ever, logging
// each round to the console. `admin` logs in with no password, but the CLI it
// lands on is a holding pen, and any configuration written there is lost at the
// next ZTP cycle.
//
// The way out is `zerotouch cancel`, which writes an EMPTY startup-config (that
// file's mere existence is what disables ZTP) and REBOOTS. So:
//
//   - a switch that still holds a configuration is reset with
//     `delete flash:startup-config` + `reload now` — and that lands it in ZTP,
//     which is a SECOND state to get out of, not the end of the reset;
//   - a switch already in ZTP needs only `zerotouch cancel`.
//
// Two reboots for a full reset, and a provisioning script that assumed one would
// sit typing configuration into a console that is about to throw it away. This
// is the reason [FactoryResetCmds] and [ZeroTouchCancelCmds] are separate, and
// the reason web/src/pages/add/aristaEosSoftReset.ts detects which of the two
// states it is looking at before it sends anything.
//
// # Third-party optics, and why the key is not a secret
//
// EOS refuses an optic it does not read as Arista-coded, err-disables the port
// with cause `xcvr-unsupported`, and that port then reads exactly like an empty
// one — the same failure mode, symptom for symptom, that cisco/ios documents at
// [UnsupportedTransceiverNote]. EOS unlocks it with a vendor-scoped command
// carrying a checksum of the vendor string:
//
//	service unsupported-transceiver <vendor-string> <key>
//
// The pair shipped here is the fleet's ([UnsupportedTransceiverVendor] /
// [UnsupportedTransceiverKey]). It is NOT a credential: the key is a checksum of
// the vendor string, it is specific to no switch, and it grants nothing except
// the acceptance of optics the operator already owns. Applied at every
// provisioning run, deliberately, because it lives in the running configuration
// and the factory reset that precedes provisioning is precisely what removes it.
package eos

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

func init() { driver.Register(&Driver{}) }

// Driver identifies Arista EOS and reaches it over SSH (contract.go).
// Serial provisioning pushes the same command set through the console using
// [MgmtPushCmds]; both paths speak one EOS vocabulary, and a device whose
// `show version` does not announce Arista gets reads only.
type Driver struct{}

func (d *Driver) Info() driver.Info {
	return driver.Info{
		Name:      "arista-eos",
		Vendor:    "Arista",
		Family:    "EOS (7000 / 720 / 750 series)",
		Transport: []string{"ssh", "serial"},
		// The CLI sits behind SSH authentication: nothing is identifiable
		// without a credential.
		IdentifyNeedsCredentials: true,
		Doc: "Arista EOS User Manual and CLI command reference " +
			"(https://www.arista.com/en/support/product-documentation)",
	}
}

// DocumentedRelease is the train the command set was WRITTEN AGAINST, from the
// vendor's reference. It is not a verified release, and the constant is named so
// that nobody reading the transcript mistakes one for the other.
const DocumentedRelease = "4.2x"

// CapturedRelease is the train the telemetry half was actually EXERCISED on.
//
// Distinct from [DocumentedRelease] and deliberately so: they answer different
// questions, and collapsing them into one "version" field is how a connector
// starts claiming that everything it does has been seen. The captures behind this
// constant are listed in the package doc, and they now include two WRITE gestures
// (the account and storm control) as well as the telemetry half — but not the
// spanning-tree, snooping, port-admin, trap or management-push forms.
const CapturedRelease = "4.28.13.1M"

var (
	// reArista is the strong signal: the vendor names itself in `show version`,
	// in its SSH banner and in sysDescr, and no other platform in this fleet
	// carries the word.
	reArista = regexp.MustCompile(`(?i)\bArista\b`)
	// reEOSSoftware is stronger still — the exact line `show version` prints.
	reEOSSoftware = regexp.MustCompile(`(?i)Software image version:\s*\d+\.\d+|Arista (?:DCS|CCS)-`)
	// reEOSModel matches the orderable part numbers this range ships under.
	reEOSModel = regexp.MustCompile(`(?i)\b(?:DCS|CCS)-7\d{3}|\bDCS-720|\bCCS-720|\bDCS-750`)
)

// monitoring: through which channels this platform is observed.
//
// # THIS PLATFORM IS OBSERVED BY STREAMING TELEMETRY, NOT BY SNMP
//
// It used to declare four SNMP channels. All four are gone, and the whole set is
// now `gnmi` — which is a promise this build can finally keep: internal/gnmipoll
// has a real transport (gnmic over gRPC), `TransportReady` is true, cmd/proxy
// subscribes, and internal/api's registry guard enforces that those halves stay
// together.
//
// The migration is NOT a like-for-like relocation, and two of the four channels
// are strictly better off here:
//
//   - `health` over SNMP was ONE NUMBER on this platform. The old note said so
//     itself: CPU and memory were explicitly not claimed, because EOS serves them
//     under HOST-RESOURCES-MIB and its own enterprise tree (30065), and
//     internal/snmppoll queries neither. OpenConfig models uptime, CPU, memory,
//     temperature, fans and PSUs, and internal/gnmipoll reads all of them;
//   - `lldp` over SNMP was a full table walk every thirty seconds to notice that
//     nothing had changed. Over gNMI it is ON CHANGE — a neighbour table moves
//     when a cable moves, which is an event — and it is the one place in this lot
//     where streaming genuinely beats polling rather than merely replacing it.
//
// # WHY `if_state` MOVED TOO, WHICH THE ORIGINAL DESIGN REFUSED
//
// internal/driver/arista/eos/gnmi.go's header proposed declaring `if_counters`
// ALONE, and gave a real reason: port.up / port.down events are derived on the
// proxy from the `if_state` poll, that derivation read the SNMP `ifindex` label,
// and gNMI samples deliberately carry no ifindex. Moving `if_state` without
// touching the detector would have stopped every port transition for every
// Arista — richer telemetry, quieter fleet log, nothing saying why.
//
// That constraint was not worked around, it was removed: cmd/proxy/gnmi.go's
// `notePorts` is a transition detector keyed on the PORT NAME, which is what a
// gNMI sample actually carries. So the objection no longer holds and `if_state`
// is declared — which matters, because on-change port state is the single most
// useful thing streaming offers an access network.
//
// # WHAT WAS CAPTURED, AND WHAT IS STILL NOT CLAIMED
//
// CAPTURED 2026-08-07 on a DCS-7050SX-64-R running EOS 4.28.13.1M, by
// subscribing with internal/gnmipoll itself rather than with an external tool —
// so what was verified is not "the device serves these paths" but the stronger
// claim that this product's own table names them correctly. All four channels
// produced samples. Three findings are folded into the notes below; two cost a
// code fix (gNMI decimal64 was not decoded at all, and the power-supply subtree
// is a SIBLING of `state` and was therefore never subscribed).
//
// STILL NOT OBSERVED, and named rather than left to be discovered: PSU
// telemetry, which this chassis publishes as six empty leaves; fan speed, which
// it publishes as a percentage under a leaf OpenConfig calls a speed (so it is
// deliberately not mapped — see internal/gnmipoll/translate.go); and free
// memory, which `/system/memory/state` does not carry at all here — only
// `physical` and `reserved`.
func monitoring() driver.MonitoringCapabilities {
	return driver.MonitoringCapabilities{
		// Streaming: the SUMMARY of the channel list below, and it must agree with
		// it. A connector declaring Streaming with no gnmi channel would put
		// "streaming" in front of an operator while BestOfKind(KindGNMI, …) found
		// nothing (cf. driver.MonitoringCapabilities).
		Streaming: true,
		Channels: []driver.MonitorChannel{
			{
				Kind:    driver.KindGNMI,
				Metrics: []string{"if_state"},
				// ON CHANGE, so this floor bounds nothing on the wire — it is
				// carried because the controller resolves all four cadences the same
				// way, and because it is what a heartbeat interval would be bounded
				// by the day one is added.
				MinInterval: 5 * time.Second,
				Note: "/interfaces/interface[name=*]/state/oper-status and " +
					"ethernet/state/port-speed, subscribed ON CHANGE. Port up/down events are " +
					"derived from this on the proxy BY PORT NAME (cmd/proxy/gnmi.go, notePorts) " +
					"— gNMI carries no ifindex, and the name is what identifies a port here",
			},
			{
				Kind:    driver.KindGNMI,
				Metrics: []string{"if_counters"},
				// 5 s is the floor the design fixed for streaming telemetry
				// (docs/en/01-Architecture/Unified monitoring and pipelines.md §1). It
				// means something different from a poll floor: it bounds how often the
				// DEVICE is asked to sample, not how often we ask.
				MinInterval: 5 * time.Second,
				Note: "the /state/counters container subscribed as ONE path in SAMPLE mode. " +
					"CAPTURED: octets, packets, errors and discards all arrive, in and out — " +
					"eight series per port. carrier-transitions does NOT: this train does not " +
					"serve it, so meshcraft_if_link_downs_total stays empty on Arista and " +
					"flap detection falls back to counting transitions",
			},
			{
				Kind:        driver.KindGNMI,
				Metrics:     []string{"lldp"},
				MinInterval: 30 * time.Second,
				Note: "/lldp/interfaces/interface[name=*]/neighbors, ON CHANGE. EOS runs LLDP " +
					"ON BY DEFAULT — unlike IOS — so this is populated on a factory switch " +
					"without meshcraft configuring anything. CAPTURED: four neighbours read " +
					"back identical to \"show lldp neighbors\", with chassis ids in colon-hex " +
					"and local ports as full names (Ethernet34). The device's own chassis id " +
					"and system name come from /lldp/state. LLDP holdtime observed at 120 s, " +
					"which is the real convergence floor for a re-cable. THE PATH STOPS AT THE " +
					"CONTAINER on purpose: asking for …/neighbor[id=*]/state returns only TWO " +
					"of those four on this train (4.28.13.1M), the same two every time and in " +
					"both sub-modes — see internal/gnmipoll.pathLLDPNeighbor for the bisection",
			},
			{
				Kind:    driver.KindGNMI,
				Metrics: []string{"optical_power"},
				// 30 s: a fibre's level moves on the minute scale, and the point of
				// the series is history and drift, not a trigger. Well above the 5 s
				// design floor for streaming, well below the 5 min health cadence —
				// a level dropping NOW is something an operator watches live while
				// re-seating a module.
				MinInterval: 30 * time.Second,
				Note: "/components/component[name=*]/transceiver/physical-channels/" +
					"channel[index=*]/state, SAMPLE mode. CAPTURED on the lab 7050SX " +
					"(2026-08-12): input-power, output-power and laser-bias-current " +
					"arrive per channel, already in dBm and mA; the component is named " +
					"\"EthernetN transceiver\" and gnmipoll strips the suffix for the " +
					"port label. Empty cages publish nothing; a cage receiving no " +
					"light publishes input-power at exactly -30, the platform's floor, " +
					"kept verbatim",
			},
			{
				Kind:        driver.KindGNMI,
				Metrics:     []string{"health"},
				MinInterval: 5 * time.Minute,
				Note: "/system/state (boot-time, from which uptime is derived), /system/memory " +
					"and the /components tree. CAPTURED: uptime, total memory, CPU load and " +
					"transceiver temperature arrive. PSU leaves are served but EMPTY on this " +
					"chassis, fan speed is a percentage and deliberately unmapped, and free " +
					"memory is not served at all. Still strictly more than the SNMP channel " +
					"this replaces, which carried sysUpTime alone",
			},
		},
		// TRAPS ARE NO LONGER DECLARED, and this is a consequence of the migration
		// rather than a capability that went away.
		//
		// "snmp-server host" is still written by this connector (trap.go), and
		// the platform still emits. What changed is that a trap has nothing left to
		// do here. Its ONLY job in this product is to ADVANCE a poll that would
		// have happened anyway — see tunnel.EventPortUp: trap content is never a
		// state source, because a v2c trap is unauthenticated UDP that anyone on
		// the path can forge. There is no `if_state` poll on this platform any
		// more; the subscription is ON CHANGE, so the switch already tells us the
		// instant a port falls, over an authenticated connection.
		//
		// Declaring Traps here would therefore make the proxy open 162/udp on the
		// management network to receive datagrams whose only possible effect is to
		// trigger a poll that does not exist. That is an attack surface offered for
		// nothing, which is exactly the trade-off [driver.TrapPusher] tells
		// connectors to refuse.
		Traps:  false,
		Syslog: false,
		Unavailable: []driver.MetricUnavailable{
			// `optical_power` no longer sits here: internal/gnmipoll carries the
			// transceiver paths since 2026-08-12 and the channel above declares
			// them, captured. The old entry's reasoning ("declaring the channel
			// before the collector carries the paths would promise samples nothing
			// produces") is what gated the move, and it is the note to restore if
			// the subscription ever has to be pulled back.
			{
				Metric:   "poe",
				Observed: false,
				Source: "the ranges this connector targets are data-centre and campus uplink " +
					"switches; PoE exists on the 720XP line only, and no PoE read has been " +
					"exercised on any of them here",
			},
		},
	}
}

// Fingerprint. "Arista" alone is already strong — unlike "cisco", the word is
// not borrowed by the lookalike CLIs — but the model and the `show version`
// software line are stronger still and are what separate a confident match from
// a banner that merely mentions the vendor.
func (d *Driver) Fingerprint(ev driver.Evidence) int {
	var hay = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if reEOSModel.MatchString(hay) {
		return 93
	}
	if reEOSSoftware.MatchString(hay) {
		return 88
	}
	if reArista.MatchString(hay) {
		return 70
	}
	return 0
}

func (d *Driver) Capabilities(model string) driver.Capabilities {
	return driver.Capabilities{
		VlanRange: [2]int{1, 4094},
		// EOS carries the full 4094 SIMULTANEOUSLY on the ranges this connector
		// targets — this is the difference from a campus 2960-X, which stops at
		// 255 active VLANs. The two limits are distinct and it is the second one
		// a write hits, so it is stated rather than left equal to the range by
		// default.
		MaxVlans: 4094,
		// The 7050SX datasheet's aggregation bounds; the write was proven with
		// Po999, so the numbering space is at least that wide.
		MaxLAGs:     64,
		PortsPerLAG: 32,
		Features: map[string]bool{
			"ssh": true, "serial": true,
			// EOS refuses `telnet` by default (`management telnet` is shut), and
			// this connector never turns it on.
			"telnet": false,
			// Per-port spanning tree, as stp.go actually writes it.
			driver.FeatSTPEdge: true,
			// "spanning-tree portfast auto" exists here, so the three-valued edge
			// policy the contract carries maps one-to-one.
			driver.FeatSTPEdgeAuto: true,
			driver.FeatBPDUGuard:   true,
			// SUPER ROOT: this connector is the only one that expresses it
			// (priority 0 on the MST instance, plus "spanning-tree root super").
			// Declaring the FEATURE is not declaring the WRITE — WritesExtra("stp")
			// stays false, and the UI requires both, so the control stays off until
			// a capture flips that. Saying so here is what keeps the checkbox off
			// every OTHER platform, including the one that does declare stp.
			driver.FeatSTPSuperRoot: true,
			// MST REGION: this connector writes the whole triplet — "spanning-tree
			// mst configuration" / "name" / "revision" / "instance N vlan …"
			// (stp.go, the region FIRST and before the mode). Exercised through
			// Client.ApplySpanningTree on core01 on 2026-08-12, read back by
			// parseMSTRegion from "show spanning-tree mst configuration".
			//
			// Declared because nothing else in the contract said it, and the silence
			// cost a switch: fs-s3400 also declares WritesExtra("stp") and writes NO
			// region, so a caller gating on writability alone could not tell the two
			// apart (ADR-0022).
			driver.FeatMSTRegion: true,
			// TRAFFIC DECOMPOSED BY CLASS (ADR-0028) — the two classes this
			// platform COUNTS, measured rather than assumed.
			//
			// Read read-only on core01-arena on 2026-10-25 by dumping
			// every leaf of `/interfaces/interface[name=*]/state/counters`:
			// fifteen arrive, and `in-multicast-pkts`, `out-multicast-pkts`,
			// `in-broadcast-pkts`, `out-broadcast-pkts` are four of them. The
			// subscription already covered the whole container, so they were
			// being streamed and discarded before they were mapped.
			driver.FeatTrafficClassMulticast: true,
			driver.FeatTrafficClassBroadcast: true,
			// FALSE, and it is a platform fact rather than a gap — the same
			// shape of statement as FeatSTPPortEnable below.
			//
			// Those fifteen leaves contain NO unknown-unicast count. openconfig
			// serves `in-unicast-pkts`, which is unicast total with the flooded
			// share folded in, and there is no second counter for the known
			// share to subtract. This platform DOES count the class in one
			// place — the `Drops` column of "show storm-control" (ADR-0020) —
			// but those are frames the limiter THREW AWAY, not frames the port
			// carried, so they are not what this capability promises and
			// stacking them under forwarded rates would total nothing.
			//
			// Saying so explicitly is what lets the overlay print "this
			// platform does not report unknown unicast" instead of dropping
			// the band and leaving a two-class stack looking complete.
			driver.FeatTrafficClassUnknownUnicast: false,
			// LACP: read AND written live on core01 (2026-08-13) — Po999 built
			// through "interface Port-Channel999" + "channel-group 999 mode
			// active", read back in "show port-channel dense", torn down clean.
			"LACP": true,
			// FALSE, and this is a platform fact rather than a gap: EOS has no
			// per-port switch that takes a port out of the tree. The nearest thing
			// ("spanning-tree bpdufilter") hides the port from the topology
			// instead of excluding it, which is a different guarantee — stp.go
			// refuses the intent rather than substituting one for the other.
			driver.FeatSTPPortEnable: false,
			// DHCP snooping exists here, GLOBALLY and PER VLAN — both were typed at
			// core01's CLI on 2026-08-10 and the feature came up on VLAN 777.
			"DHCPSnooping": true,
			// The PER-PORT TRUST level does not, and the pair is declared the way
			// instanton declares "StormControlPort: false": an operator's screen
			// should say "this hardware cannot" rather than offer a control that is
			// always skipped. "ip dhcp snooping trust" is answered "% Invalid
			// input" on EOS 4.28.13.1M and the CLI's completion offers nothing under
			// the interface command but Option-82 (dhcpsnoop.go, snoopTrustRefusal).
			// No capture will change this one — it is not "unproven", it is absent.
			"DHCPSnoopingPortTrust": false,
		},
		// EOS defaults to MSTP and carries plain single-instance RSTP under its own
		// name — which is why the fleet's vocabulary maps one-to-one here and has
		// to be approximated on the Cisco connector (whose "rapid-pvst" is RSTP
		// per VLAN, a different protocol).
		//
		// LEGACY 802.1D IS DELIBERATELY ABSENT. There is no "spanning-tree mode
		// stp" on this platform: an RSTP bridge falls back to 802.1D per port when
		// it hears a legacy BPDU, which is the interoperability a site asking for
		// "stp" actually wants. Listing a mode the CLI does not have would make
		// driver.ResolveSTPMode pick it and stp.go refuse it, instead of resolving
		// down to rstp — which is the correct answer and the one this omission
		// produces.
		STPModes: []string{"rstp", "mstp"},
		// v3 JOINED THIS LIST ON 2026-08-10, and the two lines under it are what
		// made that possible rather than merely desirable.
		//
		// It was absent for one reason — snmp.go was believed unable to VERIFY a
		// v3 write, because a user's keys were thought never to appear in
		// "show running-config". That premise turned out to be FALSE on EOS
		// 4.28.13.1M (they appear as RFC 3414 localized keys; cf. snmp.go's
		// header), but the answer does not depend on it: the
		// "show snmp user" capture of that day settled it anyway — the name, the
		// security model and both algorithms come back, so a write can be
		// confirmed to the same standard as every other write on this connector.
		//
		// WIDENING THIS LIST IS THE WHOLE DECISION, and it is a fleet-wide one:
		// snmpprov.For reads it, and the moment v3 is here every Arista is planned
		// for v3 rather than v2c. The half-measure — lifting the refusal in
		// snmp.go and leaving this list narrow — would have been no lift at all,
		// since nothing but that plan ever fills SNMPIdentity.User.
		//
		// WHAT THAT COSTS WAS CHECKED BEFORE IT WAS DONE, and on THIS connector it
		// is bounded, which is the reason the decision was takeable at all:
		//
		//   - meshcraft's own collection does not move. This range declares no SNMP
		//     channel — every metric above is gNMI — so `carriedOnGNMI` excludes
		//     every Arista from `snmpWatchesFor` whatever its plan says, and the
		//     proxy's poller (which is v2c-only, cf. tunnel.SNMPWatch) was never
		//     going to be asked to poll one;
		//   - traps are already `Traps: false` here, and `judgeTrapDest` already
		//     carries a named `trapDestV3Only` verdict with its own sentence;
		//   - the identity itself is what changes, and a v3 write that fails does
		//     so LOUDLY — `provisionSNMP` raises `snmp.apply_failed` and records the
		//     pipeline — never by silently posing a v2c community instead, which is
		//     the substitution snmp.go refused from the beginning.
		//
		// On a connector that WAS SNMP-collected, that check would have come out
		// differently and this list would have needed the proxy's v3 support first.
		SNMPVersions: []string{"v1", "v2c", "v3"},
		// The algorithms are the CAPTURED ones and nothing more: SHA and AES-128
		// are the two words "show snmp user" was seen to print back on core01.
		// The single source is snmp.go, which writes exactly what is declared here
		// and refuses anything else by name — cf. snmpV3AuthAlgs for why the pair
		// is deliberately below the default policy floor rather than optimistic.
		SNMPv3Auth: snmpV3AuthAlgs(),
		SNMPv3Priv: snmpV3PrivAlgs(),
		// A budget, not a target: EOS caps concurrent CLI sessions well above
		// this, and every connector in the product stays at 5 so a fleet-wide
		// pass cannot exhaust a device's session table.
		MaxSessions: 5,
		// EOS distinguishes running from startup: apply, verify, THEN save.
		Commit:     driver.CommitApplyThenSave,
		Monitoring: monitoring(),
		Mgmt: driver.MgmtCapabilities{
			VlanIDs:   driver.VlanAny,
			VlanRange: [2]int{1, 4094},
			TaggedOK:  true,
			Static:    true,
			DHCP:      true,
			Gateway:   true,
			DNS:       true,
			// EOS is a routing platform: management leaves its subnet through a
			// real route, not through the L2-only "ip default-gateway" a 2960-X
			// has to make do with.
			Routed: true,
			IPARP:  true,
			// RJ45 console on every model in this range, 9600 8N1 — plus a
			// dedicated Management1 out-of-band port, which is a second physical
			// path off the production network.
			OutOfBand: true,
			// NOT PROVEN ON HARDWARE. False by default is the project rule applied
			// to its most expensive case, and this connector has never re-addressed
			// a live EOS switch. mgmtaddr.go is written and refuses nothing it can
			// express; this flag flips when the same-VLAN re-address has been run
			// on the lab switch and the device answered on the new address.
			ApplySameVLAN: false,
			Notes: []string{
				"\"ip address 192.0.2.5/24\" — EOS takes the PREFIX form and refuses the " +
					"dotted mask IOS requires. This is the opposite convention from the " +
					"Cisco connector and the one place a shared command builder would " +
					"silently produce a rejected line",
				"an SVI created by \"interface Vlan N\" comes up SHUTDOWN: \"no shutdown\" " +
					"is required, and the VLAN must exist in the VLAN database first",
				"there is NO \"ip default-gateway\" on this platform: the default route is " +
					"\"ip route 0.0.0.0/0 <next-hop>\"",
				"a dedicated Management1 port exists and sits OUTSIDE the switching fabric. " +
					"This connector administers through an SVI, and never writes Management1 — " +
					"moving management onto an out-of-band port is an operator's decision about " +
					"cabling, not a connector's about configuration",
				"changing the address of the SVI the session came in through cuts the " +
					"session: verify afterward through the NEW address",
			},
		},
		Admin: driver.AdminAccount{
			Custom: true,
			Notes: []string{
				"username U privilege 15 role network-admin secret 0 SECRET",
				"NO ENABLE PASSWORD is set, by fleet rule, and \"no enable password\" is " +
					"written to remove one a device already carried: an account with " +
					"\"role network-admin\" lands on \"#\" and never traverses an enable " +
					"prompt, so a second secret would guard a door the administrator is " +
					"already through",
				"SSH is ON by default and the host key is generated at first boot — there is " +
					"no \"crypto key generate\" step and no non-crypto image, which is the " +
					"single biggest difference from the IOS provisioning path",
			},
		},
	}
}

func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Software: "enable; delete flash:startup-config; reload now — the switch then comes " +
			"back in ZERO TOUCH PROVISIONING, which is not the end of the reset: log in as " +
			"admin (no password) and run \"zerotouch cancel\", which writes an empty " +
			"startup-config and reboots a SECOND time",
		Hardware: "There is no reset button on this range. The console port IS the recovery " +
			"path: interrupt boot with Ctrl-C at \"Press Ctrl-C now to enter Aboot shell\", " +
			"then \"Aboot# cd /mnt/flash ; rm startup-config ; exit\". Aboot itself can be " +
			"password-protected, in which case the password lives in /mnt/flash/aboot.conf " +
			"on the USB flash and the recovery becomes a card reader.",
		Erases: []string{
			"startup configuration (/mnt/flash/startup-config)",
			"the VLAN database — UNLIKE IOS, EOS keeps no separate vlan.dat: the VLANs live " +
				"in the startup-config and go with it. There is no \"delete flash:vlan.dat\" " +
				"equivalent to forget",
		},
		AfterReset: []string{
			"9600 8N1 console (the same speed as the Cisco range, NOT the 115200 the FS and " +
				"Ubiquiti ranges use)",
			"the switch boots into ZTP and stays there, retrying DHCP on every port and " +
				"logging each round to the console. It is NOT sitting at an idle prompt",
			"\"admin\" logs in with NO password; \"enable\" asks for none either",
			"\"zerotouch cancel\" is what ends ZTP — it writes an empty startup-config and " +
				"reboots. Configuration typed before that is thrown away at the next ZTP cycle",
			"push management via MgmtPushCmds / the Web Serial provisioning script",
		},
		Pitfalls: []string{
			"THE RESET LANDS THE SWITCH IN ZTP, AND ZTP LOOKS LIKE A WORKING SWITCH. Every " +
				"port comes up, DHCP goes out on all of them, and the console shows a login " +
				"prompt. A script that starts configuring here loses everything it writes at " +
				"the next ZTP cycle — the state must be recognised before anything is sent",
			"ZTP BRINGS EVERY PORT UP AT ONCE, unconfigured and in VLAN 1. On a switch already " +
				"cabled into a production fabric that is a broadcast domain nobody planned; " +
				"reset a cabled Arista with the uplinks OUT, or at a maintenance window",
			"\"reload\" asks \"Save? [yes/no]\" when the running configuration differs from " +
				"the startup one — answering yes RE-SAVES the configuration that was just " +
				"deleted, and the reset then silently does nothing. \"reload now\" skips the " +
				"question entirely and is the form this connector uses",
			"THIRD-PARTY SFP: EOS refuses any optic it does not read as Arista-coded, " +
				"err-disables the port (cause \"xcvr-unsupported\") and the cage then reads " +
				"exactly like an EMPTY one — so an uplink looks absent while the fibre is " +
				"plugged in. Provisioning applies \"service unsupported-transceiver " +
				UnsupportedTransceiverVendor + " " + UnsupportedTransceiverKey + "\" and " +
				"\"errdisable recovery cause xcvr-unsupported\"; a factory reset removes both, " +
				"which is why they are re-applied every run",
			"there is no \"ip default-gateway\" on this platform. The IOS habit produces a " +
				"rejected line and a switch with no way out of its subnet",
			"the address form is the PREFIX one (\"ip address 192.0.2.5/24\"). The dotted " +
				"mask IOS wants is refused",
			"Management1 is an out-of-band port on its own: an address written there is NOT " +
				"reachable from the switched fabric, and a management plan that lands on it " +
				"without the cable to match produces a switch that answers nobody",
		},
		// NOTHING here has been run on an Arista by this connector. The gestures
		// are the vendor's documented ones; that is not the same as verified, and
		// the UI must be able to say which.
		Confirmed: false,
		Source: "Arista EOS User Manual (ZTP and \"zerotouch cancel\" chapters) + EOS CLI " +
			"command reference — DOCUMENTED, not captured on lab hardware",
	}
}

// --- CLI builders (the serial provisioning path and the SSH connector share these) ---

// UnsupportedTransceiverVendor / UnsupportedTransceiverKey are the fleet's
// third-party optic pair, as `service unsupported-transceiver` takes them.
//
// NOT A CREDENTIAL, and worth saying plainly because the shape invites the
// assumption: the second token is a checksum of the first, it is identical on
// every switch, it authenticates nobody, and it unlocks nothing but the
// acceptance of optics the fleet already owns. Keeping the pair in the source is
// what lets the SSH path and the serial path apply the SAME line — a fleet where
// half the switches accept their own optics and half do not is worse than one
// where none do, because only the second is diagnosable.
const (
	UnsupportedTransceiverVendor = "wiprolabs"
	UnsupportedTransceiverKey    = "f5047577"
)

// ErrDisableXcvr is the cause EOS reports when it refuses a transceiver.
//
// Worth a constant for the same reason `gbic-invalid` is one on the IOS
// connector: this is the single most common way a provisioning run dies, and it
// dies looking like a cabling fault. The port is shut, so it reads exactly like
// an empty cage — every symptom points at the patch panel and none at the optic.
const ErrDisableXcvr = "xcvr-unsupported"

// UnsupportedTransceiverNote explains the condition and the way out, in the words
// an operator needs at the moment a port is unexpectedly dark.
const UnsupportedTransceiverNote = "the switch REFUSED the transceiver in this cage " +
	"(errdisabled, cause \"" + ErrDisableXcvr + "\"): EOS only accepts Arista-coded optics " +
	"unless told otherwise. The port reads exactly like an empty one, which is why an " +
	"uplink can seem to be missing when the fibre is in fact plugged in. Fix: " +
	"\"service unsupported-transceiver " + UnsupportedTransceiverVendor + " " +
	UnsupportedTransceiverKey + "\" then \"errdisable recovery cause " + ErrDisableXcvr +
	"\", then bounce the port (shutdown / no shutdown) — a port already errdisabled stays " +
	"down until the recovery timer fires or it is bounced, and enabling recovery does not " +
	"clear the ports already sitting in that state"

// UnsupportedTransceiverCmds returns the global commands that make EOS accept the
// fleet's third-party optics, in order.
//
// Two commands and they do DIFFERENT things — applying one without the other is
// half a fix:
//
//   - `service unsupported-transceiver` stops EOS refusing the optic in the
//     first place. It is what matters for a port brought up from now on;
//   - `errdisable recovery cause xcvr-unsupported` arms the automatic recovery
//     for ports ALREADY sitting err-disabled. Without it, a cage that was
//     refused before the first command landed stays dark until somebody bounces
//     it by hand — which, on a switch being provisioned remotely, means the
//     uplink never comes back and the probe blames the network.
//
// Both live in the RUNNING CONFIGURATION, so a factory reset takes them with it.
// That is exactly why they belong in the provisioning push rather than in a
// one-off gesture: the reset that precedes the push is what removes them.
//
// Shared by the SSH connector and the serial script so the two paths never drift
// into two different remediations.
func UnsupportedTransceiverCmds() []string {
	return []string{
		fmt.Sprintf("service unsupported-transceiver %s %s",
			UnsupportedTransceiverVendor, UnsupportedTransceiverKey),
		"errdisable recovery cause " + ErrDisableXcvr,
	}
}

// FanSpeedOverrideCmd caps the fans, by fleet decision.
//
// A data-centre Arista runs its fans for a data-centre inlet temperature, which
// in a lab or a small comms room is simply loud. 30 % is the fleet's setting.
//
// # This is NOT the same kind of decision as the transceiver pair
//
// [UnsupportedTransceiverCmds] changes what the switch will ACCEPT; this changes
// what it does to keep itself alive. EOS answers the command with a four-line
// banner saying so — CAPTURED 2026-08-06 on the lab switch:
//
//	WARNING: Overriding the system fan speed is unsupported and should only
//	be done under the direction of an Arista Networks engineer.
//	You can risk damaging hardware by setting the fan speed too low
//	and doing so without direction from Arista Networks can be grounds
//	for voiding your warranty.
//
// The banner is NOT a refusal — the command succeeded and the prompt came
// straight back — and [reCLIError] does not match it, because every refusal form
// there is anchored on a leading "%" or "!" and this banner has neither. That
// is a property worth keeping: a future widening of the error pattern to catch
// bare words would turn every provisioning run on this platform into a failure.
//
// The override lives in the running configuration and is therefore SAVED, so it
// survives a reload — a switch moved from a cool lab into a warm rack keeps the
// cap it was provisioned with. [FanSpeedAutoCmd] is the undo.
//
// # MEASURED WITH THE CAP IN FORCE — core01, 2026-08-11, IDLE
//
// System status Ok. Ambient 33 °C. Hottest sensor 58.5 °C against a 100 °C alert
// limit — 41.5 °C of margin. All four system fans stable at 30 %; the two PSU
// fans run at 34 % and report "FW Override", i.e. the power supplies keep their
// own control loop and the cap does not reach them.
//
// So 30 % is comfortably sufficient on THIS model at THIS inlet temperature with
// the box doing nothing. What is still unknown is the same question on a LOADED
// box, which needs traffic the lab cannot generate.
//
// THE VERB HAS MOVED, and the old one is what a reader will reach for:
// "show environment temperature" is DEPRECATED on this train. The current
// spellings are "show system environment temperature" and
// "show system environment cooling".
const (
	FanSpeedOverrideCmd = "environment fan-speed override 30"
	FanSpeedAutoCmd     = "environment fan-speed auto"
)

// FactoryResetCmds is the soft factory-reset sequence (privileged EXEC), for a
// switch that STILL HOLDS A CONFIGURATION.
//
// Three facts about this sequence, each of which has bitten somebody:
//
//  1. `delete flash:startup-config` asks NOTHING on EOS. There is no
//     "[confirm]", no "Delete filename?" — the IOS reflex of answering a prompt
//     that never comes leaves the console with a stray Enter, which EOS reads as
//     an empty command and ignores. Harmless here, and worth knowing when
//     reading a transcript;
//  2. `reload now` is used rather than `reload`. Plain `reload` asks
//     "Save? [yes/no]" whenever running and startup differ — which, one command
//     after deleting the startup file, they always do — and the default answer
//     writes the configuration straight back over the file just removed. `now`
//     skips the question;
//  3. THIS DOES NOT FINISH THE RESET. The switch comes back in ZTP, which is a
//     second state with its own exit ([ZeroTouchCancelCmds]).
func FactoryResetCmds() []string {
	return []string{
		"delete flash:startup-config",
		"reload now",
	}
}

// ZeroTouchCancelCmds ends Zero Touch Provisioning.
//
// ONE command, and it reboots the switch. `zerotouch cancel` writes an EMPTY
// startup-config — the file's existence, not its content, is what tells EOS not
// to run ZTP again — and reloads. A switch that reaches a prompt after this is a
// switch with a blank configuration and no ZTP, which is the state every other
// range in this product is in the moment it finishes a factory reset.
//
// Runs from the restricted CLI a ZTP switch offers, where almost nothing else
// does. That is the reason it has its own builder rather than being appended to
// [FactoryResetCmds]: the two are sent in different states, minutes and a reboot
// apart, and a caller that concatenated them would send this one into a CLI that
// is not there yet.
func ZeroTouchCancelCmds() []string {
	return []string{"zerotouch cancel"}
}

// --- THE FLEET TEMPLATE FOR THIS PLATFORM ---
//
// What every Arista provisioned by meshcraft comes out looking like. Both paths
// — this connector over SSH and the Web Serial script — build from these, so a
// switch adopted at the console and one adopted over the network are the same
// switch. When they disagree, THIS is the reference.
//
//	no enable password        every account is already privileged; see NoEnableCmds
//	admin account             username U privilege 15 role network-admin secret 0 S
//	management                SVI on the site's VLAN, "ip address A.B.C.D/NN"
//	management VLAN           TAGGED on every trunk — never native
//	native VLAN               left at the platform default (1)
//	trunks                    every faceplate port, allowed = site VLANs + 1 + mgmt
//	default route             ip route 0.0.0.0/0 <gw>   (there is no ip default-gateway)
//	third-party optics        accepted, with errdisable recovery armed
//	fan speed                 capped at 30 %            (fleet decision, see FanSpeedOverrideCmd)
//	LLDP                      on (it already is by default; written anyway)
//	SSH                       on and not shut (it already is; written anyway)
//
// Each line is justified where it is built. The two that are FLEET POLICY rather
// than platform fact — the fan cap and the absent enable password — carry their
// reasoning at their own constants, because those are the two a future operator
// is most likely to want to revisit.

// NoEnableCmds enforces the fleet's universal rule: NO ENABLE PASSWORD, on any
// switch, on any platform.
//
// # Why this is a write and not simply an omission
//
// Not setting one would leave the rule true on a factory switch and false on
// every switch that already had one — which is precisely the fleet where it
// matters. So the rule is APPLIED: "no enable password" is idempotent on a device
// that carries none, and removes it on a device that does.
//
// # Why removing it is not a downgrade
//
// This paragraph used to argue that the accounts this connector creates carry
// `privilege 15` and `role network-admin` and therefore "land on "#" at login,
// never traversing an enable prompt at all". MEASURED on 2026-08-07, that is
// false. After a clean provisioning run:
//
//	username mcadmin privilege 15 role network-admin secret sha512 $6$…
//
//	core01-lab> show privilege
//	Current privilege level is 1
//
// The account lands on USER EXEC regardless, because this switch carries no
// `aaa authorization exec default local`. So the enable prompt is on the normal
// path, not an edge case — which is precisely why removing the password matters
// rather than being cosmetic.
//
// What makes the removal safe is the OTHER half: with no enable password set,
// `enable` grants privilege 15 without asking. Verified on the same box in the
// same session. An enable password would instead add a second secret to rotate,
// let it drift out of step with the first, and strand automation at ">" — the
// failure this codebase has already met on other ranges.
//
// The login password is untouched and is what actually protects the device.
//
// # The connector still ANSWERS an enable prompt
//
// [cliConn.enable] and the serial script's `enterEnable` both still handle a
// device that asks — a switch meshcraft did not provision, or one provisioned
// before this rule. Enforcing the rule on write and refusing to cope with it on
// read would lock this connector out of exactly the devices it is meant to take
// over.
// # The keyword is "password", not "secret"
//
// EOS has no `enable secret`. Captured on a DCS-7050SX-64-R running 4.28.13.1M,
// in configuration mode:
//
//	core01(config)#no enable ?
//	  password  Assign the enable password
//
// and the device stores it as `enable password 5 $1$…`. The form written here
// was `no enable secret` — borrowed from IOS, where both keywords exist — and it
// came back `% Invalid input` on the connector's first live write. Nothing else
// in the sequence was wrong.
func NoEnableCmds() []string {
	return []string{"no enable password"}
}

// MgmtOpts is what provisioning / onboarding needs on the wire.
type MgmtOpts struct {
	VlanID   int
	VlanName string // default LAN<id>_Management
	IP       string
	// PrefixLen is used AS IS: EOS takes "ip address A.B.C.D/NN" and refuses
	// the dotted mask. No conversion happens on the write side, which is the
	// single most consequential difference from the IOS builder.
	PrefixLen int
	Gateway   string
	Username  string
	Password  string
	// NOTE: there is deliberately NO EnablePassword field. The fleet rule is that
	// no switch carries an enable password (cf. NoEnableCmds), and the field used
	// to exist only to be defaulted to Password and written — so removing it is
	// what makes the rule impossible to bypass by filling in a struct.
	//
	// Hostname is the device's MESHCRAFT NAME, not an already-normalized system
	// name: [MgmtPushCmds] runs it through [driver.HostnameFromDeviceName] — the
	// same function api.hostnameheal uses — so a switch provisioned from a
	// console and the same switch reconciled half an hour later cannot end up
	// carrying two different names. A name that cannot be normalized is REFUSED,
	// never mangled: MgmtPushCmds returns the reason and omits the line.
	Hostname string
	// SiteVLANs are every VLAN the SITE declares. They are created on the device
	// during provisioning AND they are the allowed list every faceplate port's
	// trunk is given.
	//
	// A switch coming out of a factory reset has VLAN 1 and nothing else, so any
	// port profile applied later — access on VLAN 30, trunk carrying 40 — lands
	// on a VLAN the device has never heard of. Creating them here means the
	// switch leaves provisioning already able to carry the site.
	SiteVLANs []driver.VLAN
	// TrunkPorts are the FULL EOS interface names ("Ethernet49") to configure as
	// trunks. Empty means EVERY faceplate port the caller knows about is left
	// alone — this builder never invents a port list, because a name it guessed
	// wrong is a line the device rejects in the middle of a provisioning run.
	TrunkPorts []string
	// GNMI writes the `management api gnmi` block (cf. GNMIEnableCmds).
	//
	// DEFAULT FALSE, and provisioning never sets it. The reason is in gnmi.go's
	// header and is worth repeating where the field is declared: the transport
	// this would enable is PLAINTEXT gRPC authenticated by the very account the
	// lines below create, so turning it on fleet-wide would put a privileged
	// device login in clear on the management network — in exchange for
	// telemetry that no collector in this build subscribes to.
	//
	// The field exists now, rather than being added later, so the command forms
	// and their ordering are written down and tested while they are fresh; it is
	// the caller's decision, taken once per site, that flips it.
	GNMI bool
	// DomainName is the SITE's local DNS domain (model.Site.LocalDomain).
	//
	// It reaches the wire in exactly ONE place: the DNS subject alternative name
	// of the gNMI certificate, which becomes `<hostname>.<domain>`. That is not a
	// cosmetic use — a collector verifying TLS by name checks the SAN, so a
	// certificate whose DNS name is a bare hostname is only usable by address.
	//
	// `ip domain-name` is deliberately NOT written from it. EOS carries the
	// command, but this connector has never run a configuration write on an
	// Arista, and adding a line to a provisioning push on the strength of "the
	// platform has it" is what the honesty rule exists to stop. The certificate
	// SAN is different: it is a string this connector already composes, not a new
	// command.
	//
	// Empty is normal and means the site declares no domain: the SAN then carries
	// the bare hostname, which is what it carried before this field existed.
	DomainName string
	// GNMIOptions carries the port, the VRF and the ssl profile through to
	// [GNMIEnableCmds]. Zero value means the defaults that builder documents.
	//
	// It exists because this struct USED TO DROP ALL THREE. `MgmtPushCmds` called
	// `GNMIEnableCmds(GNMIOpts{})` unconditionally, so a site that had moved
	// management into a named VRF got a transport bound in the default one —
	// which binds successfully and is reachable from nowhere — with no way to
	// correct it short of editing this file. The builder handled both fields and
	// was tested for both; only the caller threw them away.
	//
	// The ssl profile matters more than the other two: without it the block is
	// plaintext, and this is the only route by which a caller can ask for a
	// protected transport at provisioning time.
	GNMIOptions GNMIOpts
}

// DefaultVlanName is what an unnamed management VLAN is called on the device.
const DefaultVlanName = "Management"

// MgmtPushCmds returns configuration-mode lines that bring a blank EOS switch to
// a reachable, site-ready management state.
//
// # IT HAS NO GO CALLER, AND THAT IS THE DESIGN — recorded 2026-08-09
//
// This was read twice as dead code before somebody counted: NOT ONE of the six
// `MgmtPushCmds` in this tree has a Go caller. cisco/ios, ubiquiti/edgeswitch,
// fs/s3400, fs/s3900 and this one are all reached only from their own tests. So
// the absence is the convention and not an oversight here, and the convention is
// worth stating once rather than being rediscovered per connector:
//
//   - the BUILDER IS THE SPECIFICATION. The gesture runs in the browser over Web
//     Serial (`web/src/pages/add/aristaEosSoftReset.ts`, which says "This script
//     mirrors MgmtPushCmds line for line"), and `internal/discovery/reset.go`
//     points an operator at this function by name as the reference for what that
//     script must send. Deleting it would delete the written form of the gesture
//     and leave the TypeScript as its own only authority;
//   - the Go tests are what keep the two honest. `pushFixture` (eos_test.go) and
//     the pki/gnmi push tests pin the order, the fan cap, the ssl-profile-before-
//     transport rule and the credentials-last rule against this builder, and the
//     script's own suite pins the same forms on its side;
//   - WIRING IT WOULD BE A CONTRADICTION, not a feature. This block exists to give
//     a switch a management address. A switch that has one is already provisioned;
//     a switch that has not cannot be reached by SSH, and if it is still in ZTP
//     [Client.requireWritableDialect] refuses every write anyway — correctly, since
//     ZTP discards them. There is no device state in which an SSH caller both can
//     reach the switch and needs this.
//
// What IS reachable from Go is what other callers took out of it: the certificate
// builders through [CertificateRenewalNote] (mgmtaddr.go, on a re-address), the
// optics unlock through provisioning's own read/repair path, and the gNMI block
// through [GNMIEnableCmds]. The one line whose only Go home is this function is
// [FanSpeedOverrideCmd]; it is exported, asserted here and independently mirrored
// by the script's own constant, so it is documented rather than orphaned.
//
// The ORDER is load-bearing and is not a style choice:
//
//  1. THE OPTICS FIRST, before anything that depends on a link existing. EOS
//     shuts a cage holding an optic it does not accept, and such a port is
//     indistinguishable from an empty one — so uplink discovery finds nothing,
//     the management VLAN is carried nowhere, and the probe blames the network;
//  2. every VLAN exists before any port is told to carry it, and before the SVI
//     that serves the management one — an SVI on a VLAN that does not exist
//     never comes up;
//  3. the trunks come before the SVI's address, so the moment management has an
//     address there is already a path for it to answer on;
//  4. CREDENTIALS LAST — same reason as every other connector in this product:
//     the console session can drop right at the password change of the account
//     currently logged in, and everything else is then already in place.
//
// # THE ERROR IS THE SYSTEM NAME'S REFUSAL, AND NOTHING ELSE (2026-08-09)
//
// This returned a bare []string until the four connector `sanitizeHostname`
// spellings were collapsed into [driver.HostnameFromDeviceName]. That function
// REFUSES a name it cannot normalize rather than mangling it, and a builder with
// no error channel can honour a refusal only by dropping the line in silence —
// which is the mangle-doctrine in another costume: the operator asked for a name,
// the switch keeps `localhost`, and nothing anywhere says why.
//
// THE REST OF THE PUSH COMES BACK ALONGSIDE THE ERROR, deliberately. That is the
// convention `adminFor` and the identity readers already follow in this tree —
// return what you COULD build next to the reason the rest failed — and here it is
// the difference between a switch left factory-reset and unreachable because its
// meshcraft name carries an accent, and a switch that is reachable, adopted and
// merely still called `localhost`, with the reason on the operator's screen.
// This connector's local sanitizer was the LEAST wrong of the four (it mapped `_`
// to `-` and trimmed) and it still dropped silently and still fell back to
// `switch`, which is why "nearly right" was not a reason to keep it.
//
// The mirror is `buildPushCmds` in web/src/pages/add/aristaEosSoftReset.ts, which
// reports the refusal into the run log and pushes everything else.
func MgmtPushCmds(opts MgmtOpts) ([]string, error) {
	var vlanID = opts.VlanID
	if vlanID < 1 {
		vlanID = 1
	}
	var prefix = opts.PrefixLen
	if prefix < 1 || prefix > 32 {
		prefix = 24
	}

	var out []string

	// 1. Optics, first. Both lines live in the running configuration, so the
	//    factory reset that precedes this push is what removed them.
	out = append(out, UnsupportedTransceiverCmds()...)

	// Fan speed, with the other "make this hardware usable in OUR racks" line.
	// It answers with a warranty banner rather than silence (cf.
	// FanSpeedOverrideCmd) — getting it out of the way here keeps that banner
	// out of the middle of the VLAN block an operator will actually read.
	out = append(out, FanSpeedOverrideCmd)

	// The system name, through the ONE normalizer — see the note where this
	// package's sanitizeHostname used to be.
	var hostErr error
	if strings.TrimSpace(opts.Hostname) != "" {
		var host, refusal = driver.HostnameFromDeviceName(opts.Hostname)
		if refusal != "" {
			hostErr = errors.New(refusal)
		} else {
			out = append(out, "hostname "+host)
		}
	}

	// LLDP is ON by default on EOS — unlike IOS, where its absence leaves a hole
	// in the topology exactly where the switch is. The line is written anyway and
	// costs one command: a switch someone had turned it off on comes back with it,
	// and provisioning is the only pass that visits every device.
	out = append(out, "lldp run")

	// 2. VLANs before anything references them. The management VLAN is folded
	//    into the same list so it cannot be forgotten when the site declares it
	//    and cannot be created twice when it does not.
	out = append(out, vlanCreateCmds(mergeSiteVLANs(opts.SiteVLANs, vlanID, opts.VlanName))...)

	// 3. Trunks: every faceplate port the caller named, carrying every VLAN the
	//    site declares, with the management VLAN as native.
	out = append(out, trunkAllCmds(opts.TrunkPorts, allowedVlanList(opts.SiteVLANs, vlanID))...)

	// 4. The management SVI. PREFIX form — the dotted mask IOS wants is refused
	//    here, and this is the line where that difference actually bites.
	out = append(out,
		fmt.Sprintf("interface Vlan%d", vlanID),
		fmt.Sprintf("ip address %s/%d", strings.TrimSpace(opts.IP), prefix),
		// An SVI created in configuration mode comes up administratively down.
		"no shutdown",
		"exit",
	)

	var gw = strings.TrimSpace(opts.Gateway)
	if gw != "" {
		// NOT "ip default-gateway": that command does not exist on EOS. The
		// default route is a real route, in the default VRF.
		out = append(out, "ip route 0.0.0.0/0 "+gw)
	}

	// SSH: already running, already keyed. All that is left is to be sure the
	// service is not administratively down — which it is not on a factory switch,
	// and which one operator in the past will have shut. Two idempotent lines
	// beat a provisioning run that ends with an unreachable switch.
	out = append(out,
		"management ssh",
		"no shutdown",
		"exit",
	)

	// gNMI, with the other management-plane service and for that reason: `management
	// api gnmi` is the same family of statement as `management ssh`, and a reader
	// looking for "which doors are open on this switch" should find them together.
	//
	// It is BEFORE the credentials for the same reason everything else is: the
	// console session can drop at the password change, and a block written after
	// it would be the one thing missing from a switch that otherwise came up
	// complete. Nothing is emitted unless the caller asked — see MgmtOpts.GNMI.
	if opts.GNMI {
		// The ssl profile comes BEFORE the transport that references it. EOS
		// accepts `ssl profile X` naming a profile that does not exist yet and
		// then refuses to bring the transport up, which is the quiet half of this
		// ordering: the block would look written and serve nothing.
		//
		// The profile is emitted only when the caller asked for a named one. With
		// no profile the transport is PLAINTEXT — the device login travels in
		// clear in gRPC metadata — and that stays an explicit choice rather than
		// something provisioning does by default.
		if strings.TrimSpace(opts.GNMIOptions.SSLProfile) == TLSProfileName {
			out = append(out, SSLProfileCmds()...)
		}
		out = append(out, GNMIEnableCmds(opts.GNMIOptions)...)
	}

	// 5. Credentials last.
	var user = strings.TrimSpace(opts.Username)
	var pass = opts.Password
	if user != "" && pass != "" {
		// "secret" and not "password": the second stores the value reversibly
		// in the configuration. `role network-admin` is what EOS actually consults
		// — `privilege 15` alone leaves the account able to log in and unable to
		// configure anything.
		out = append(out, fmt.Sprintf("username %s privilege 15 role network-admin secret 0 %s",
			user, pass))
	}

	// NO ENABLE PASSWORD — the fleet rule, applied rather than merely omitted
	// (cf. NoEnableCmds). It comes AFTER the account on purpose: the account it
	// leaves in charge is already privileged, so at no point in this block is the
	// device without a way in.
	out = append(out, NoEnableCmds()...)

	// 5. PERSIST. Last, unconditionally, and it used to be missing entirely.
	//
	// # What was wrong
	//
	// This builder produced a complete, correct provisioning run that lived
	// ENTIRELY in the running configuration. Everything above — the optics
	// unlock, the VLANs, the SVI, the address, the default route, the trunks, the
	// gNMI transport, the account — was gone at the next reload, and the switch
	// came back with the empty startup-config that ZTP had left behind. Nothing
	// said so: the push succeeded, every command was accepted, the device was
	// reachable, and it stayed that way until somebody power-cycled it, possibly
	// months later and probably in a rack nobody was standing next to.
	//
	// `ApplyMgmtAddressing` gets away without this — its note says "write memory
	//" is what makes it survive — because that write usually severs the session
	// that would have issued the save, so it can only tell the caller. A serial
	// provisioning run has no such excuse: the console does not go anywhere.
	//
	// # Why it is EXEC and needs the exit
	//
	// Everything before this is configuration mode; `write memory` is not a
	// configuration command and is refused there. `end` closes whatever submode
	// the block finished in and returns to the enable prompt. Emitting the save
	// without it would append the one line whose failure is silent — the run
	// would look complete and persist nothing, which is exactly the bug being
	// fixed.
	//
	// [SaveCmdFallback] is NOT emitted alongside: two saves on a train that
	// accepts the first would put an unexplained error in the middle of a
	// console transcript. The interactive path picks between them by reading the
	// answer ([Client.save]); a blind command list cannot, so it sends the form
	// every EOS train carries.
	out = append(out, "end", SaveCmd)

	return out, hostErr
}

// mergeSiteVLANs folds the management VLAN into the site's list, without
// duplicating it and without letting a site entry lose its own name.
//
// VLAN 1 is dropped: it always exists on EOS, `vlan 1` is accepted but renaming
// it is a change nobody asked for, and creating it would be the one line in a
// provisioning push that modifies a VLAN the operator did not declare.
func mergeSiteVLANs(site []driver.VLAN, mgmtVLAN int, mgmtName string) []driver.VLAN {
	var out []driver.VLAN
	var seen = map[int]bool{}
	var v driver.VLAN
	for _, v = range site {
		if v.ID < 1 || v.ID > 4094 || v.ID == 1 || seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		out = append(out, v)
	}
	if mgmtVLAN > 1 && !seen[mgmtVLAN] {
		var name = strings.TrimSpace(mgmtName)
		if name == "" {
			name = fmt.Sprintf("LAN%d_%s", mgmtVLAN, DefaultVlanName)
		}
		out = append(out, driver.VLAN{ID: mgmtVLAN, Name: name})
	}
	sortVLANs(out)
	return out
}

// vlanCreateCmds: the EOS global-configuration form. A bare "vlan N" is enough;
// the name is set on the following line when one is carried, and OMITTED when it
// is not — EOS then keeps its own default (VLAN0100), which is exactly what
// [parseVlanTable] reads back.
func vlanCreateCmds(vlans []driver.VLAN) []string {
	var cmds []string
	var v driver.VLAN
	for _, v = range vlans {
		cmds = append(cmds, fmt.Sprintf("vlan %d", v.ID))
		if strings.TrimSpace(v.Name) != "" {
			cmds = append(cmds, "name "+sanitizeVlanName(v.Name))
		}
		cmds = append(cmds, "exit")
	}
	return cmds
}

// allowedVlanList is what every provisioned trunk is allowed to carry: every
// VLAN the SITE declares, plus the management VLAN, plus VLAN 1.
//
// VLAN 1 IS IN THE LIST DELIBERATELY, and leaving it out is the mistake this
// function exists to avoid. EOS drops native-VLAN traffic on a trunk whose
// allowed list does not contain the native VLAN, and the native VLAN on these
// trunks IS 1 — the platform default, which [trunkAllCmds] deliberately does not
// touch. An allowed list built from the site VLANs alone would produce a trunk
// that looks perfectly configured and drops everything untagged.
//
// The management VLAN appears here as an ORDINARY TAGGED MEMBER, exactly like
// every site VLAN beside it. It is not native and must not be made native.
func allowedVlanList(site []driver.VLAN, mgmtVLAN int) []int {
	var seen = map[int]bool{1: true}
	var out = []int{1}
	var add = func(id int) {
		if id < 1 || id > 4094 || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	var v driver.VLAN
	for _, v = range site {
		add(v.ID)
	}
	add(mgmtVLAN)
	sortInts(out)
	return out
}

// trunkAllCmds turns the named ports into trunks carrying the whole site.
//
// # Why every faceplate port and not just the uplinks
//
// This is the fleet's chosen provisioning shape and it differs from the IOS
// connector's, which trunks only the ports it found a live link on. The reason
// is what happens NEXT: a provisioned switch gets its per-port profiles from
// meshcraft afterwards, and a port that starts as a trunk carrying everything can
// be narrowed to an access port by one apply. A port that starts as access on
// VLAN 1 cannot carry anything until somebody notices, which on a switch nobody
// is watching is however long it takes for a complaint to arrive.
//
// The cost is that an unpatched port is a trunk. On a switch not yet in service
// that is nothing; once it is in service the profiles have been applied and the
// question no longer arises.
//
// # THE MANAGEMENT VLAN IS TAGGED LIKE ANY OTHER — no native VLAN is written
//
// An earlier version of this builder set the management VLAN as each trunk's
// NATIVE one, reasoning that it made a freshly provisioned switch reachable from
// an upstream port that had not been told about tagging yet. That is a
// convenience for the ten minutes of provisioning, paid for permanently by the
// design, and it is wrong in two ways that matter:
//
//   - native means UNTAGGED. An upstream expecting the management VLAN tagged —
//     which is what a correctly configured neighbour does — sees untagged frames
//     and drops them. The setting breaks precisely the sites that got their own
//     configuration right;
//   - it puts the administration VLAN on the one part of a trunk that carries no
//     tag, which is the surface every VLAN-hopping technique aims at. The
//     management VLAN is the last one that should volunteer for it.
//
// So the native VLAN is left at the platform default (1), which is what a switch
// coming out of a factory reset already carries. VLAN 1 stays in the allowed list
// for that reason and only that reason — EOS drops native traffic on a trunk
// whose allowed list omits the native VLAN (cf. [allowedVlanList]).
func trunkAllCmds(ports []string, allowed []int) []string {
	if len(ports) == 0 {
		return nil
	}
	var cmds []string
	var port string
	for _, port = range ports {
		port = strings.TrimSpace(port)
		if port == "" {
			continue
		}
		cmds = append(cmds,
			"interface "+port,
			"switchport mode trunk",
			// The SET form, not "add": it is the only one that RESTRICTS, and on
			// EOS a trunk with no allowed line carries every VLAN there is. "add"
			// on such a trunk changes nothing and reports success.
			"switchport trunk allowed vlan "+joinInts(allowed),
			"exit",
		)
	}
	return cmds
}

// TrunkAllCmds is [trunkAllCmds] for callers outside this package (the serial
// provisioning path builds the same block from the ports it discovered).
//
// mgmtVLAN is still taken, and it still only feeds the ALLOWED LIST: the
// management VLAN must be carried, tagged, on every trunk. It is not made native
// (cf. [trunkAllCmds]).
func TrunkAllCmds(ports []string, siteVLANs []driver.VLAN, mgmtVLAN int) []string {
	return trunkAllCmds(ports, allowedVlanList(siteVLANs, mgmtVLAN))
}

// SaveCmd is the save this connector emits first; [SaveCmdFallback] is the one
// tried if EOS refuses it. "write memory" prompts for nothing, which is why it
// comes first — and unlike IOS, neither form asks for a destination file name.
const (
	SaveCmd         = "write memory"
	SaveCmdFallback = "copy running-config startup-config"
)

// MaskFromPrefix turns a prefix length into a dotted mask.
//
// NOT USED ON THE WRITE PATH — EOS takes the prefix form directly, which is the
// whole point of this connector's addressing notes. It exists for the reverse
// direction only: reporting an address to a caller whose vocabulary is dotted,
// and for symmetry with [PrefixFromMask] so neither can be edited without the
// other being obviously stale.
func MaskFromPrefix(prefix int) string {
	if prefix < 0 || prefix > 32 {
		prefix = 24
	}
	var octets [4]int
	var i int
	for i = 0; i < 4; i++ {
		var bits = prefix - i*8
		if bits >= 8 {
			octets[i] = 255
			continue
		}
		if bits <= 0 {
			octets[i] = 0
			continue
		}
		octets[i] = 256 - (1 << uint(8-bits))
	}
	return fmt.Sprintf("%d.%d.%d.%d", octets[0], octets[1], octets[2], octets[3])
}

// PrefixFromMask is the reverse, for reading a configuration written elsewhere.
// Returns 0 when the mask is unreadable — the caller then says so instead of
// assuming /24.
func PrefixFromMask(mask string) int {
	var parts = strings.Split(strings.TrimSpace(mask), ".")
	if len(parts) != 4 {
		return 0
	}
	var bits int
	var p string
	for _, p = range parts {
		var b, err = strconv.Atoi(p)
		if err != nil || b < 0 || b > 255 {
			return 0
		}
		for b > 0 {
			bits += b & 1
			b >>= 1
		}
	}
	return bits
}

// sanitizeVlanName keeps only what EOS accepts in a VLAN name. EOS is stricter
// than IOS here: spaces are refused outright rather than truncating the name at
// the first one, so they become underscores.
func sanitizeVlanName(s string) string {
	var b strings.Builder
	var r rune
	for _, r = range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' {
			b.WriteByte('_')
		}
	}
	var out = b.String()
	if out == "" {
		return DefaultVlanName
	}
	// EOS caps a VLAN name at 32 characters and refuses the command beyond it.
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

// There is no sanitizeHostname here any more, and its removal is not tidying.
// It was the LEAST wrong of the four connector spellings — it mapped `_` to `-`
// and it trimmed — and it was still wrong in the two ways that matter: it
// SILENTLY DROPPED every character outside its alphabet, turning "Switch étage
// 1" into a name nobody can trace back to the deleted letter, and it fell back
// to the literal `switch` on an empty result, which is a fleet of
// identically-named devices produced by the gesture meant to give each switch an
// identity. [MgmtPushCmds] now calls [driver.HostnameFromDeviceName], the
// reconciliation loop's own normalizer, which refuses rather than mangling. Do
// not re-introduce a local one: this drift began as exactly one such function per
// connector, each reasonable on its own.
