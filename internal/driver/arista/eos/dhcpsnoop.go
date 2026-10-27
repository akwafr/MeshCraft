package eos

// DHCP snooping — read and write (driver/dhcpsnoop.go).
//
// # TWO LEVELS EXIST HERE, AND THE THIRD ONE DOES NOT — captured 2026-08-10
//
// The contract's feature is global, then per-VLAN, then per-port trust. On this
// range the first two exist and are written by [Client.ApplyDHCPSnooping]. THE
// THIRD IS ABSENT FROM THE PLATFORM, and that is a capture rather than a
// suspicion — core01.lab, DCS-7050SX-64-R, EOS 4.28.13.1M, with snooping
// armed on VLAN 777 (dhcpsnoop_live_test.go holds the two outputs verbatim):
//
//	ip dhcp snooping trust          →  % Invalid input
//	show ip dhcp snooping trust     →  % Invalid input
//	show ip dhcp snooping ?         →  COUNTERS and HARDWARE, nothing else
//	(interface) ip dhcp snooping ?  →  "interface", whose sole leaf is
//	                                   "information option" — Option-82
//
// and the enabled output carries NO per-port section in either state. The level
// is not misspelled here, it is missing, so no spelling fixes it and no capture
// will: this file emits no per-port command at all, and every trust intent in a
// plan is an ORDINARY REFUSAL in [driver.ApplyResult.Skipped] naming the
// platform fact (cf. [snoopTrustRefusal], and instanton/stormcontrol.go for the
// same shape of "this hardware does not have this level" as opposed to "we have
// not proven it").
//
// THE READ SIDE STILL LOOKS FOR THE TABLE, deliberately. [reSnoopPortRow] and
// [snoopRead.Trust] cost nothing on a device that prints no such grid — they
// come back empty — and EOS's snooping support depends on the forwarding ASIC as
// much as on the release (§ below), so a model that DOES print one would be
// reported rather than hidden. If that ever happens the refusal below is what
// has to be revisited, and [TestTheEnabledCaptureHasNoPerPortTrustTable] is
// where the revisit starts.
//
// # THE FEATURE IS NOT ON EVERY PLATFORM IN THIS RANGE, and that is not a bug
//
// Unlike VLANs or interfaces, DHCP snooping on EOS depends on the FORWARDING
// ASIC as much as on the software train: the command exists on some platforms
// in this range and is absent on others running the same EOS release. A
// connector that treated its absence as an error would report a broken switch;
// one that treated it as "off" would report protection as merely disabled when
// it is in fact unavailable. Both are wrong in ways an operator would act on, so
// a refused `show ip dhcp snooping` becomes [driver.NotSupported] — a capability
// answer, with the reason — and never a failure.
//
// # THE TWO LEVELS THAT EXIST ARE RE-READ, and PortsWritten stays empty
//
// The apply's re-read checks the global enable and the VLAN list — the two
// things it can write. [driver.ApplyResult.PortsWritten] is now permanently
// EMPTY on this connector, and that is the honest value rather than an
// omission: the field means CONFIRMED (driver/state.go), no per-port command is
// sent here, so no port can be confirmed. Between 2026-08-08 and 2026-08-10 a
// verifySnoopTrust compared each sent flag against a per-port table; it went
// with the write, because the table it read and the command it verified were
// both invented.
//
// # ARMING THE FEATURE FOR A PLAN THAT WANTED A TRUSTED PORT IS REFUSED
//
// The refusal above is per port, but one case needs more than a Skipped line.
// A plan that asks for a TRUSTED port and would also have to turn inspection ON
// — the global enable, or a VLAN this device does not yet snoop — is asking for
// something whose two halves are not separable: the caller derives the plan's
// VLANs from the membership of the ports it names (api/dhcpsnoop_apply.go,
// dhcpSnoopVLANsFor), so the VLANs about to be armed are the ones crossing the
// very uplink it wanted exempted. Arming them while no port can be exempted
// drops DHCP OFFER/ACK on the path the server answers over: not the protection
// asked for, an outage in its place. So that gesture writes NOTHING and says so
// (cf. [snoopArmRefusal]).
//
// A plan asking only for UNTRUSTED ports does not trip it — untrusted is the
// protected default and the two levels above it are exactly what the operator
// is asking for. Neither does a plan on a switch already snooping those VLANs:
// there is nothing to arm, and the state it would have armed is one this apply
// did not create.
//
// # What is NOT here
//
// The binding table (the leases snooping learned) is TELEMETRY, and a rich,
// fast-moving kind: it belongs to a monitoring path, not to a configuration
// contract that drift compares. Option-82 insertion and ARP inspection ride on
// the same tables and are REPORTED in Notes when observed, never written.
//
// # STATUS: THE READ IS CAPTURED, THE WRITE IS NOT, AND NEITHER IS DECLARED
//
// [Driver.WritesExtra] returns false for "dhcp_snooping". That is the rule
// [driver.DHCPSnoopApplier] states verbatim, and this connector is still the
// case it describes for the two levels it does write: "ip dhcp snooping" and
// "ip dhcp snooping vlan …" were typed at core01's CLI on 2026-08-10 and the
// feature came up on VLAN 777, but [Client.ApplyDHCPSnooping] itself has never
// run against a device, so the button stays off and TODO.md keeps the entry.
//
// The per-port half of that entry is CLOSED rather than pending: there is no
// capture to take, because there is no command.
//
// # "ENABLED" AND "NOT OPERATIONAL" IN THE SAME BREATH — acted on 2026-08-11
//
// The same session found one level up what the per-port hunt did not: the device
// answered "DHCP Snooping is enabled", "configured on … 777" and "DHCP
// Snooping is NOT OPERATIONAL" together, with "show ip dhcp snooping hardware"
// answering "disabled". The ASIC never programmed it. [snoopStateOf] used to
// report that switch as inspecting VLAN 777 while nothing on it did; it now
// carries the operational line into [driver.DHCPSnoopState.Notes], which is the
// only surface this shared type has for a fact it cannot express as a field.
// Enabled STAYS true — the configuration IS on the device, and a reader that
// flipped it to false would report "somebody turned snooping off" about a
// switch nobody touched. What changed is that the note is now next to the VLAN
// list an operator reads (TestTheCapturedEnabledStateSaysNothingInspectsIt).
//
// The other defect that capture exposed is in [snoopConfiguredVLANs]: the VLAN
// ids sit on the line BELOW the header, and one regexp was reaching them by
// accident. Read that function before touching the pattern.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin reads and writes DHCP snooping.
var (
	_ driver.DHCPSnoopReader  = (*Client)(nil)
	_ driver.DHCPSnoopApplier = (*Client)(nil)
)

const showSnoopCmd = "show ip dhcp snooping"

var (
	// "DHCP Snooping is enabled" / "... is disabled"
	reSnoopEnabled = regexp.MustCompile(`(?i)DHCP\s+Snooping\s+is\s+(enabled|disabled)`)
	// "DHCP Snooping is not operational" — configured, stored, and inspecting
	// NOTHING, because the forwarding hardware never programmed it (§ below).
	reSnoopNotOperational = regexp.MustCompile(`(?i)DHCP\s+Snooping\s+is\s+not\s+operational`)
	// The HEADER of the configured-VLAN section, and only the header. The ids
	// are collected by [snoopConfiguredVLANs], because on the captured firmware
	// they are NOT on this line — see that function for why the split matters.
	//
	// The word "configured" is LOAD-BEARING: the same output carries a second
	// "… on following VLANs:" section, "operational", which on core01 said
	// "None" while the configured one said 777. A pattern that matched either
	// would report a configured-but-unprogrammed switch as covering nothing.
	reSnoopVLANs = regexp.MustCompile(`(?i)configured on (?:the )?following\s+vlans?\s*:`)
	// The per-port trust table: "Ethernet49          Yes"
	reSnoopPortRow = regexp.MustCompile(`(?im)^\s*((?:ethernet|et)\s*\d+(?:/\d+)?)\s+(yes|no|trusted|untrusted)\b`)
	// Adjacent features, reported and never written.
	reSnoopOpt82 = regexp.MustCompile(`(?i)Option-?82[^\n]*\b(enabled)\b`)
	// The shapes a platform without the feature answers with.
	reSnoopAbsent = regexp.MustCompile(`(?i)invalid input|unavailable command|not supported|` +
		`incomplete command|unknown command`)
)

// snoopConfiguredVLANs returns the id list of the "configured on following
// VLANs" section, as a string [expandVlanList] can read.
//
// # WHY THIS IS A LINE SCANNER AND NOT ONE REGEXP
//
// Until 2026-08-11 the whole section was read by a single pattern whose capture
// class was `[\d,\-\s]+`. It got the right answer on the core01 capture BY LUCK,
// and both halves of the luck are worth naming because each fails differently:
//
//   - `\s` matches a newline, so the pattern walked off the end of the header
//     line and onto the indented ids below it. That is the shape this firmware
//     actually prints, so the ACCIDENT was load-bearing — anchoring the pattern
//     to a line, the obvious tidy-up, would have made every Arista report
//     snooping as covering NO VLAN, which [snoopStateOf] turns into "every port
//     trust flag below is inert";
//   - it stopped in the right place only because the NEXT line began with the
//     "D" of "DHCP Snooping is operational …". A firmware whose next line
//     begins with a digit — a counter, a second indented block, a timestamp —
//     would have been swallowed into the VLAN list.
//
// So the multi-line shape is kept and made DELIBERATE. A continuation line must
// be INDENTED and START WITH A DIGIT, the same rule [reMSTInstCont] applies to
// wrapped MST VLAN lists and for the same reason: EOS wraps long lists across
// lines, and a parser that only reads the first one drops VLANs silently.
//
// Wrapping is assumed to happen BETWEEN items, never inside one, which is what
// EOS does. A line already ending in `,` or `-` is joined directly so a range
// split across the break survives; otherwise a comma is inserted, so two items
// on two lines can never fuse into one number.
func snoopConfiguredVLANs(out string) string {
	var lines = strings.Split(out, "\n")
	var i int
	for i = range lines {
		var head = strings.TrimRight(lines[i], "\r")
		var at = reSnoopVLANs.FindStringIndex(head)
		if at == nil {
			continue
		}
		var spec = strings.TrimSpace(head[at[1]:])
		var j int
		for j = i + 1; j < len(lines); j++ {
			var cont = strings.TrimRight(lines[j], "\r")
			if !snoopVLANContinuation(cont) {
				break
			}
			var t = strings.TrimSpace(cont)
			if spec == "" || strings.HasSuffix(spec, ",") || strings.HasSuffix(spec, "-") {
				spec += t
				continue
			}
			spec += "," + t
		}
		return spec
	}
	return ""
}

// snoopVLANContinuation reports whether a line continues the VLAN id list:
// indented AND beginning with a digit. The digit clause is what keeps the next
// SECTION HEADER — "DHCP Snooping is operational on following VLANs:", and the
// "None" this device prints under it — out of the list.
func snoopVLANContinuation(line string) bool {
	if line == "" || (line[0] != ' ' && line[0] != '\t') {
		return false
	}
	var t = strings.TrimSpace(line)
	return t != "" && t[0] >= '0' && t[0] <= '9'
}

// snoopRead is what `show ip dhcp snooping` established.
type snoopRead struct {
	Enabled bool
	// NotOperational: the device said "DHCP Snooping is not operational" in the
	// same breath as "enabled". The configuration is stored and the forwarding
	// ASIC never programmed it, so nothing is inspected on any VLAN below.
	NotOperational bool
	VLANs          []int
	// Trust maps standard-model port name -> trusted. A port ABSENT from this map
	// was not read, which is not the same as untrusted (cf.
	// driver.DHCPSnoopPortState).
	Trust map[string]bool
	Notes []string
}

func parseShowDHCPSnooping(out string) snoopRead {
	var r = snoopRead{Trust: map[string]bool{}}
	var m = reSnoopEnabled.FindStringSubmatch(out)
	if m != nil {
		r.Enabled = strings.EqualFold(m[1], "enabled")
	}
	r.NotOperational = reSnoopNotOperational.MatchString(out)
	var spec = snoopConfiguredVLANs(out)
	if spec != "" {
		var ids, err = expandVlanList(spec)
		if err == nil {
			r.VLANs = ids
		}
	}
	var rows = reSnoopPortRow.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var std, ok = (&Driver{}).StandardPortName(rows[i][1])
		if !ok {
			continue
		}
		var w = strings.ToLower(rows[i][2])
		r.Trust[std] = w == "yes" || w == "trusted"
	}
	if reSnoopOpt82.MatchString(out) {
		r.Notes = append(r.Notes,
			"Option-82 insertion is ENABLED on this switch. meshcraft neither wrote it nor "+
				"removes it — it is reported because a relay agent downstream that does not "+
				"expect the option drops the offer, and the symptom is clients that get no "+
				"lease on a VLAN whose snooping looks perfectly configured")
	}
	return r
}

// readSnooping runs the read and turns "the platform does not have this" into a
// capability answer rather than a failure (cf. the file header).
func (c *Client) readSnooping(ctx context.Context) (snoopRead, error) {
	if c.cli == nil {
		return snoopRead{}, fmt.Errorf("no open session on this access")
	}
	var out, err = c.cli.run(ctx, showSnoopCmd, cmdTimeout)
	if err != nil && out == "" {
		return snoopRead{}, fmt.Errorf("%s failed: %v", showSnoopCmd, err)
	}
	if reSnoopAbsent.MatchString(out) || (err != nil && reSnoopAbsent.MatchString(err.Error())) {
		return snoopRead{}, driver.NotSupported("arista-eos", "read DHCP snooping",
			"this switch does not carry the DHCP snooping command set. On EOS the feature "+
				"depends on the forwarding hardware as much as on the software train, so its "+
				"absence here is a property of THIS model rather than of the release or of "+
				"meshcraft — there is nothing to enable and nothing to wait for")
	}
	return parseShowDHCPSnooping(out), nil
}

func snoopStateOf(r snoopRead) driver.DHCPSnoopState {
	var st = driver.DHCPSnoopState{
		Enabled: r.Enabled,
		VLANs:   r.VLANs,
		Notes:   r.Notes,
	}
	var names []string
	var name string
	for name = range r.Trust {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return stdPortLess(names[i], names[j]) })
	for _, name = range names {
		st.Ports = append(st.Ports, driver.DHCPSnoopPortState{Port: name, Trusted: r.Trust[name]})
	}
	// THE OPERATIONAL LINE OUTRANKS EVERYTHING ELSE IN THIS OUTPUT, so it is said
	// first. A switch that answers "enabled" and "not operational" together is
	// carrying a stored configuration the forwarding hardware never programmed:
	// the VLAN list above is what was ASKED FOR, not what is inspected.
	if r.NotOperational {
		st.Notes = append(st.Notes,
			"the switch reports DHCP snooping \"enabled\" and \"NOT OPERATIONAL\" in the same "+
				"output: the configuration is stored and the forwarding hardware never "+
				"programmed it, so NOTHING is inspected — on the VLANs listed here or on any "+
				"other. Captured on core01.lab (DCS-7050SX-64-R, EOS "+CapturedRelease+
				") on 2026-08-10, where \"show ip dhcp snooping hardware\" answered "+
				"\"disabled\" in the same session. On EOS this feature depends on the "+
				"forwarding ASIC as much as on the software train; this is that dependency in "+
				"its least visible form — not \"the command is absent\" but \"the command is "+
				"accepted, the configuration is stored, and nothing inspects anything\"")
	}
	if r.Enabled && len(r.VLANs) == 0 {
		st.Notes = append(st.Notes,
			"snooping is ON but covers NO VLAN: every port trust flag below is inert until "+
				"\"ip dhcp snooping vlan <ids>\" names something for it to inspect")
	}
	if !r.Enabled && len(r.Trust) > 0 {
		st.Notes = append(st.Notes,
			"snooping is OFF switch-wide: the port trust flags below are stored and do "+
				"nothing. They are shown because they are what the device carries, not "+
				"because they protect anything today")
	}
	return st
}

// ReadDHCPSnooping reports the three levels the device carries.
func (c *Client) ReadDHCPSnooping(ctx context.Context) (driver.DHCPSnoopState, error) {
	var r, err = c.readSnooping(ctx)
	if err != nil {
		return driver.DHCPSnoopState{}, err
	}
	return snoopStateOf(r), nil
}

// snoopTrustRefusal is the platform fact, written once so the per-port refusal,
// the arming refusal and the capability declaration cannot become three
// different accounts of one piece of hardware.
//
// It names what was OBSERVED — the command, the completion, the missing section
// — rather than reporting a failure, because a port trust that cannot exist is
// not a device that misbehaved (cf. instanton/stormcontrol.go, stormRefusal).
const snoopTrustRefusal = "this platform has NO PER-PORT DHCP SNOOPING TRUST LEVEL AT ALL. " +
	"Captured on core01.lab (DCS-7050SX-64-R, EOS " + CapturedRelease + ") on 2026-08-10 " +
	"with snooping armed on VLAN 777: \"ip dhcp snooping trust\" in an interface block is " +
	"answered \"% Invalid input\", so is \"show ip dhcp snooping trust\", the CLI's own " +
	"completion offers only COUNTERS and HARDWARE under \"show ip dhcp snooping\" and only " +
	"\"information option\" (Option-82) under the interface command, and the enabled output " +
	"carries no per-port section in either state. The level is ABSENT from this hardware, not " +
	"misspelled — there is no command to send, nothing was sent, and no \"force\" would " +
	"change it. What protects a port here is the pair of levels that DO exist: the switch-wide " +
	"enable and the VLAN coverage. A port that must keep receiving DHCP server traffic has to " +
	"be left OUT of the inspected VLANs, since it cannot be exempted from them"

// snoopArmRefusal is the second half of the same fact, and it is the one that
// stops an apply doing harm rather than merely doing nothing (cf. the header).
const snoopArmRefusal = "DHCP snooping was NOT armed on this switch and nothing was written: " +
	"the plan asks for at least one TRUSTED port, this platform has no per-port trust level, " +
	"and the VLANs this plan would have started inspecting are the ones that cross the very " +
	"port it wanted exempted. Turning inspection on there while no port can be exempted drops " +
	"DHCP OFFER/ACK on the path the server answers over — an outage in place of the protection " +
	"asked for. The device is left exactly as it was found. Arm snooping on the VLANs that " +
	"carry no DHCP server, or accept that this switch cannot exempt an uplink"

// snoopTrustSkipped turns a plan's per-port intents into the refusals they are
// on this platform. PURE, and sorted by faceplate order so two identical plans
// produce two identical results.
//
// The second return says whether any intent asked for TRUST — the one case the
// arming guard cares about. An intent asking for UNTRUSTED is refused just the
// same (it is still a level this hardware does not carry, and the contract
// forbids accepting an intent and reporting success), but its refusal is
// harmless to grant in effect: untrusted is what every port here permanently is.
func snoopTrustSkipped(ports []driver.DHCPSnoopPortIntent) (skipped []string, wantsTrust bool) {
	var intents = make([]driver.DHCPSnoopPortIntent, len(ports))
	copy(intents, ports)
	sort.Slice(intents, func(i, j int) bool { return stdPortLess(intents[i].Port, intents[j].Port) })
	var it driver.DHCPSnoopPortIntent
	for _, it = range intents {
		if it.Trusted {
			wantsTrust = true
		}
		skipped = append(skipped, fmt.Sprintf("%s: NOT set %s — %s",
			it.Port, trustWord(it.Trusted), snoopTrustRefusal))
	}
	return skipped, wantsTrust
}

// snoopVLANsToAdd is the plan's VLANs minus what the device already inspects.
//
// NEVER a subtraction in the other direction: a VLAN somebody else enabled
// snooping on is protection we did not install and must not take away (cf.
// driver.DHCPSnoopPlan.VLANs).
func snoopVLANsToAdd(want, have []int) []int {
	var has = map[int]bool{}
	var v int
	for _, v = range have {
		has[v] = true
	}
	var out []int
	for _, v = range want {
		if v >= 1 && v <= 4094 && !has[v] {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

// ApplyDHCPSnooping turns the feature on and extends it to the plan's VLANs —
// the two levels this platform has — and REFUSES the per-port trust level,
// which it does not (cf. the header, and [snoopTrustRefusal]).
//
// The plan's VLANs are the ones the NAMED PORTS carry, derived by the API layer
// (api/dhcpsnoop_apply.go, dhcpSnoopVLANsFor) rather than lifted from the site —
// a port trusted outside every inspected VLAN is a write some platforms accept
// and then ignore. Nothing here widens that list.
func (c *Client) ApplyDHCPSnooping(ctx context.Context,
	plan driver.DHCPSnoopPlan) (ApplyResult, error) {

	// PortsWritten is initialised empty and STAYS empty: it means confirmed, and
	// no per-port command exists on this platform to confirm.
	var out = ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}

	// THE PER-PORT REFUSAL IS FIRST, before the session is even checked, so that
	// a plan carrying an impossible intent says so even when the gesture goes on
	// to fail for some ordinary reason. It costs no round trip: the fact is about
	// this hardware, and reaching a device to ask for something already known to
	// be impossible is what driver.NotSupported's own comment warns against.
	var refusals, wantsTrust = snoopTrustSkipped(plan.Ports)
	out.Skipped = append(out.Skipped, refusals...)
	if len(refusals) > 0 {
		c.step(false, fmt.Sprintf("per-port DHCP snooping trust refused on %d port(s)",
			len(refusals)), snoopTrustRefusal)
	}

	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	// The read decides whether the platform has the feature at all, and it is a
	// capability refusal rather than an error (cf. the file header).
	var before, rerr = c.readSnooping(ctx)
	if rerr != nil {
		return out, rerr
	}

	// ARMING FOR A PLAN THAT WANTED A TRUSTED PORT: refused, and nothing written.
	// "Arming" is the narrow thing it says — the global enable this apply would
	// have done, or a VLAN this device does not yet inspect. A switch already
	// snooping those VLANs is not armed by this gesture and is left to run.
	var toAdd = snoopVLANsToAdd(plan.VLANs, before.VLANs)
	if wantsTrust && (!before.Enabled || len(toAdd) > 0) {
		out.Skipped = append(out.Skipped, snoopArmRefusal)
		c.step(false, "DHCP snooping NOT armed", snoopArmRefusal)
		return out, nil
	}

	// sent records whether this gesture wrote ANYTHING. It decides the re-read and
	// the save below: a plan whose ports were all refused and whose two other
	// levels were already in place has nothing to verify and nothing to persist,
	// and copying running-config to startup for it would be a flash write nobody
	// asked for.
	var sent bool

	// 1. Global, first. The VLAN coverage below is inert without it, and so is
	//    every protection an operator thinks they installed.
	if !before.Enabled {
		var err = c.runConfigBlock(ctx, []string{"ip dhcp snooping"})
		if err != nil {
			return out, fmt.Errorf("enabling DHCP snooping switch-wide: %v — nothing further "+
				"was written, because VLAN coverage under a disabled feature is inert", err)
		}
		sent = true
		c.step(true, "DHCP snooping enabled switch-wide", "ip dhcp snooping")
		out.Notes = append(out.Notes, "DHCP snooping was OFF on this switch and this apply "+
			"turned it ON: from now on the switch drops DHCP server traffic arriving on any "+
			"port of an inspected VLAN. THIS PLATFORM HAS NO PER-PORT EXEMPTION (cf. the "+
			"refusals in Skipped, if the plan asked for one), so a DHCP server or relay reached "+
			"through any port of those VLANs stops answering clients")
	}

	// 2. The VLANs. Added to whatever the device already had, never pruned.
	if len(toAdd) > 0 {
		sent = true
		var err = c.runConfigBlock(ctx, []string{"ip dhcp snooping vlan " + joinInts(toAdd)})
		if err != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"VLAN coverage %s not applied (%s): the feature is on but inspects only %s",
				joinInts(toAdd), firstLine(err.Error()), orNone(joinInts(before.VLANs))))
		} else {
			c.step(true, "DHCP snooping extended to VLAN(s) "+joinInts(toAdd))
		}
	}
	if len(plan.VLANs) == 0 && len(before.VLANs) == 0 {
		out.Notes = append(out.Notes, "no VLAN could be derived from the ports this plan "+
			"names, the site declares none either, and the device inspects none: the feature "+
			"is on and covers NOTHING until a VLAN is named")
	}

	// 3. THERE IS NO THIRD LEVEL ON THIS PLATFORM. The intents were refused at the
	//    top of this function and nothing per-port is sent from here — deliberately
	//    not even a "no … trust", which is the same absent command in the other
	//    direction.

	if !sent {
		// Nothing went out, so there is nothing to verify and nothing to persist.
		return out, nil
	}

	// The re-read is what says the two levels landed — never the return codes.
	var after, aerr = c.readSnooping(ctx)
	if aerr != nil {
		out.Notes = append(out.Notes, "write accepted but NOT re-read ("+
			firstLine(aerr.Error())+"): meshcraft cannot confirm the device retained it")
		return out, nil
	}
	if !after.Enabled {
		out.Notes = append(out.Notes, "the CLI accepted \"ip dhcp snooping\" but the switch "+
			"still reports the feature DISABLED — a return code that changes nothing is what "+
			"this re-read exists to catch. Nothing below it is protecting anything")
	}
	var missing = snoopVLANsToAdd(plan.VLANs, after.VLANs)
	if len(missing) > 0 {
		out.Notes = append(out.Notes, "VLAN(s) "+joinInts(missing)+" were asked for and are "+
			"NOT inspected after the write: traffic on them is not snooped")
	}

	var perr = c.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes, "applied but NOT persisted: "+firstLine(perr.Error())+
			" — a reboot would take the protection with it")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", SaveCmd)
	return out, nil
}

func trustWord(trusted bool) string {
	if trusted {
		return "trusted"
	}
	return "untrusted"
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}
