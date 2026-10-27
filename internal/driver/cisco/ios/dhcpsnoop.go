package ios

// DHCP snooping on IOS — reading the three levels the feature really has, and
// writing the one an operator clicks (driver/dhcpsnoop.go).
//
// # The three levels, and why all three are read
//
// IOS carries the feature at three heights, and only the first two decide
// whether anything is inspected at all:
//
//	ip dhcp snooping                     ← the switch-wide switch
//	ip dhcp snooping vlan 99,100         ← the VLANs it looks at
//	interface Gi1/0/28                   ← and, per port, the exception
//	 ip dhcp snooping trust
//
// A port carrying "trust" on a switch whose global statement is missing is an
// exact configuration protecting nothing, and "show ip dhcp snooping" prints
// exactly that state without commenting on it. So this reader reports the global
// flag, the VLAN list and the port flags together, and [driver.DHCPSnoopState]
// exists in that shape for the same reason.
//
// # The two VLAN lists are NOT the same list, and the difference is a real fault
//
// IOS prints both:
//
//	DHCP snooping is configured on following VLANs:
//	99,100
//	DHCP snooping is operational on following VLANs:
//	99
//
// A VLAN in the first list and not in the second is snooping asked for on a VLAN
// the switch does not have (or has not brought up) — configured, inert, and
// indistinguishable from working protection on any screen that shows one list.
// [DHCPSnoopState.VLANs] carries the CONFIGURED list, because that is what an
// apply must not remove and what the configuration says; the divergence goes to
// Notes, named VLAN by VLAN.
//
// # What "absent from the trust table" means here, and why read and write
// answer it differently
//
// "show ip dhcp snooping" lists ONLY the interfaces whose trust or rate limit
// departs from the default. Every other port is untrusted — that is the platform
// default and the protected state.
//
//   - the READ still reports those ports as absent, per the contract: a screen
//     drawing "untrusted" on a port whose flag was never read would be asserting
//     protection from a line the device never printed. The absence is explained
//     in Notes instead;
//   - the WRITE does not lean on the default either, but in the other direction:
//     a port absent from the table gets its command SENT even when the intent
//     matches the platform default. If this parser ever misses a "yes" row, the
//     skipped write would be the one that leaves a trusted port trusted — a
//     protection hole produced by an optimisation. One idempotent command is the
//     cheaper mistake.
//
// # Ordering: ports are trusted BEFORE the feature is turned on
//
// Enabling snooping on a VLAN drops every DHCP OFFER/ACK arriving on an
// untrusted port — including the uplink, until the uplink is trusted. Writing
// the global statement first would therefore open a window, however short,
// during which the site's DHCP replies are being dropped by a switch that is
// mid-apply. Trust first, enable second: the protection comes up with its
// exception already in place.
//
// # What is NOT done
//
//   - the feature is never turned OFF and no VLAN is ever removed from the list
//     (cf. the contract): both would strip protection from ports nobody clicked;
//   - the binding table is not read. It is telemetry, it moves on every lease,
//     and it belongs to a monitoring path, not to a configuration read that drift
//     compares;
//   - option-82 insertion, rate limits and ARP inspection are visible in this
//     command's output and are not modelled. They are reported in Notes when they
//     contradict what the operator sees, and nothing here writes them.
//
// # STATUS OF THE FORMS: THE READ IS CAPTURED, THE WRITE IS NOT PROVEN
//
// UPDATED 2026-08-08. "show ip dhcp snooping" HAS now been read from the lab
// 2960-X in both states, through meshcraft's own SOCKS route, and both outputs
// are in testdata/show_ip_dhcp_snooping_{off,on}.txt. The parser below was
// written from the Catalyst 2960-X command reference and needed no change
// against them — though they carried two lines no constructed fixture had (a
// "Switch DHCP gleaning is disabled" line directly under the one that matters,
// and an indented "Custom circuit-ids:" sub-line inside the trust table),
// which is the argument for capturing rather than reasoning.
//
// [Driver.WritesExtra] DECLARES "dhcp_snooping" SINCE 2026-08-09, and the three
// things that were owed have been paid.
//
// The earlier ON capture was not the live-proof standard this fleet declares on:
// the write forms had been driven by hand to produce it, no plan had gone through
// [Client.ApplyDHCPSnooping], the trusted port had been chosen for being empty
// rather than for being an uplink, and neither a removal nor a refusal had been
// exercised. live_snoop_test.go now does all of it on the lab 2960-X.
//
// WHY THAT EXERCISE NEEDED RECONNAISSANCE FIRST, unlike storm control's. A storm
// limiter on an empty port polices nothing, so the write is real and the
// consequence nil. "ip dhcp snooping vlan <n>" has no such version: it is
// VLAN-wide, and on a VLAN whose clients get their addresses through this switch
// it drops every offer arriving on an untrusted port. Picking the target from a
// guess would be picking whose DHCP to break. A read-only probe
// (live_snoopprobe_test.go) established that the switch snooped nothing, that
// exactly one port was connected — the 10G uplink — and that VLAN 42 carried no
// ports at all. Hence: VLAN 42, uplink trusted.
//
// Result: PortsWritten=[XG2], re-read enabled=true VLANs=[42] with XG2 trusted, a
// second identical apply wrote nothing, and the revert left the running AND
// startup configs as found.
//
// TWO PROPERTIES OF THIS APPLIER THE EXERCISE CONFIRMED, both worth keeping here
// because a future edit could quietly lose either:
//
//   - per-port trust is written BEFORE the feature is turned on (step 1 below).
//     The opposite order leaves a window in which the uplink is untrusted on a
//     snooped VLAN, and on a busy VLAN that window is a DHCP outage;
//   - the revert has to SAVE, because this applier persists. A revert left
//     unsaved leaves the running config clean and the startup config carrying
//     snooping — the trap this range already sprang during the storm-control
//     exercise on 2026-08-08.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin reads and writes DHCP snooping.
var (
	_ driver.DHCPSnoopReader  = (*Client)(nil)
	_ driver.DHCPSnoopApplier = (*Client)(nil)
)

// The command, kept as a constant because the read path and the verification
// re-read must never drift into two different questions.
const showSnoopCmd = "show ip dhcp snooping"

var (
	// "Switch DHCP snooping is enabled" — the global statement's echo. The
	// word "Switch" is IOS's, not a hostname.
	reSnoopGlobal = regexp.MustCompile(`(?im)^\s*Switch DHCP snooping is\s+(enabled|disabled)`)
	// The two VLAN headers, in one expression: group 1 says WHICH list, group 2
	// picks up a list printed on the same line (some trains do, most put it on
	// the next one).
	reSnoopVLANHdr = regexp.MustCompile(
		`(?i)^\s*DHCP snooping is (configured|operational) on (?:the )?following VLANs\s*:?\s*(.*)$`)
	// A continuation line of a VLAN list: digits, commas, ranges. "none" does
	// not match, which is exactly right — it ends the list and adds nothing.
	reSnoopVLANList = regexp.MustCompile(`^[\d,\-\s]+$`)
	// A trust row. Two column layouts exist across the family — "Trusted /
	// Allow option / Rate limit" on 15.x, "Trusted / Rate limit" on 12.2 —
	// and both start with the interface name followed by yes|no, which is all
	// this connector reads. The interface name is then required to parse as a
	// faceplate port, so header rows and prose lines cannot be mistaken for one.
	reSnoopTrustRow = regexp.MustCompile(`(?i)^\s*(\S+)\s+(yes|no)\b`)
)

// snoopRead is "show ip dhcp snooping", decomposed.
type snoopRead struct {
	// Enabled: the global statement is present.
	Enabled bool
	// Configured / Operational: the two lists IOS prints, kept APART (cf. the
	// header). Both sorted.
	Configured  []int
	Operational []int
	// Trust maps standard-model port name -> trusted, for the ports the device
	// PRINTED. A port absent was not printed; it is not "untrusted" here.
	Trust map[string]bool
	// Printed: the ports of Trust, so a caller can iterate deterministically.
	Printed []string
}

// parseShowDHCPSnooping reads the command's output.
//
// Written as a line scanner rather than a set of global regexps because the VLAN
// lists are CONTINUATION lines: their value is on the line after their header,
// and a global match would happily attach the operational list to the configured
// header on a train that prints them in the other order.
func parseShowDHCPSnooping(out string) snoopRead {
	var r = snoopRead{Trust: map[string]bool{}}
	// which list the following lines continue: "", "configured", "operational".
	var collecting = ""
	var add = func(which, spec string) {
		var ids, err = expandVlanList(spec)
		if err != nil {
			return
		}
		if which == "configured" {
			r.Configured = mergeSorted(r.Configured, ids)
			return
		}
		r.Operational = mergeSorted(r.Operational, ids)
	}

	var line string
	for _, line = range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		var m = reSnoopGlobal.FindStringSubmatch(line)
		if m != nil {
			r.Enabled = strings.EqualFold(m[1], "enabled")
			collecting = ""
			continue
		}
		m = reSnoopVLANHdr.FindStringSubmatch(line)
		if m != nil {
			collecting = strings.ToLower(m[1])
			var inline = strings.TrimSpace(m[2])
			if inline != "" {
				add(collecting, inline)
			}
			continue
		}
		var trimmed = strings.TrimSpace(line)
		if collecting != "" && trimmed != "" && reSnoopVLANList.MatchString(trimmed) {
			add(collecting, trimmed)
			continue
		}
		// Anything else closes the list: a blank line, "none", the next
		// section's heading. Better to under-collect than to swallow an unrelated
		// numeric line into a VLAN list.
		collecting = ""

		m = reSnoopTrustRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var stdPrefix, _, _, port, ok = splitIOSName(m[1])
		if !ok {
			continue
		}
		var name = fmt.Sprintf("%s%d", stdPrefix, port)
		var _, seen = r.Trust[name]
		if !seen {
			r.Printed = append(r.Printed, name)
		}
		r.Trust[name] = strings.EqualFold(m[2], "yes")
	}
	sort.Slice(r.Printed, func(i, j int) bool { return stdPortLess(r.Printed[i], r.Printed[j]) })
	return r
}

// stdPortLess orders standard-model names the way a faceplate does: by media
// prefix, then NUMERICALLY. Plain string order would put GE10 before GE2 and
// make two reads of the same device produce two different-looking lists.
func stdPortLess(a, b string) bool {
	var ma = reStdName.FindStringSubmatch(a)
	var mb = reStdName.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return a < b
	}
	if ma[1] != mb[1] {
		return ma[1] < mb[1]
	}
	var na, _ = strconv.Atoi(ma[2])
	var nb, _ = strconv.Atoi(mb[2])
	return na < nb
}

// readSnooping runs the command and decomposes it. Shared by the read and by
// the apply's before/after reads, so the three never ask different questions.
func (c *Client) readSnooping(ctx context.Context) (snoopRead, error) {
	if c.cli == nil {
		return snoopRead{}, fmt.Errorf("no open session on this access")
	}
	var out, err = c.cli.run(ctx, showSnoopCmd, cmdTimeout)
	if err != nil && out == "" {
		return snoopRead{}, err
	}
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid input") {
		// The refusal is the answer: this image does not carry the feature at
		// all. Said as such rather than reported as an empty (and therefore
		// reassuring) configuration.
		return snoopRead{}, fmt.Errorf("\"%s\" was refused by this image (%s): DHCP "+
			"snooping is not available on it, so neither the global state nor any port's "+
			"trust flag can be read — and a port trusted here would configure nothing",
			showSnoopCmd, firstLine(err.Error()))
	}
	return parseShowDHCPSnooping(out), nil
}

// snoopStateOf renders a read into the shared state, with the facts the type
// cannot carry put in Notes.
func snoopStateOf(r snoopRead) driver.DHCPSnoopState {
	var st = driver.DHCPSnoopState{Enabled: r.Enabled, VLANs: r.Configured}
	var name string
	for _, name = range r.Printed {
		st.Ports = append(st.Ports, driver.DHCPSnoopPortState{Port: name, Trusted: r.Trust[name]})
	}

	// THE THREE FACTS A SCREEN CANNOT DEDUCE FROM THE FIELDS ABOVE.
	if !r.Enabled {
		st.Notes = append(st.Notes, "DHCP snooping is OFF switch-wide (\"ip dhcp snooping\" "+
			"absent): every trust flag below is inert, and this device inspects no DHCP "+
			"traffic at all")
	} else if len(r.Configured) == 0 {
		st.Notes = append(st.Notes, "DHCP snooping is on switch-wide but covers NO VLAN "+
			"(\"ip dhcp snooping vlan …\" absent): it inspects nothing, which reads on a "+
			"port panel exactly like protection that works")
	}
	var missing = subtractInts(r.Configured, r.Operational)
	if len(missing) > 0 {
		st.Notes = append(st.Notes, "VLAN(s) "+joinInts(missing)+" are configured for snooping "+
			"but NOT operational: the switch is not inspecting them (usually a VLAN that "+
			"does not exist in its database, or one that is suspended)")
	}
	st.Notes = append(st.Notes, "this platform lists ONLY the ports whose trust or rate "+
		"limit departs from the default; a port absent from this list was not reported by "+
		"the device and is at IOS's default, which is UNTRUSTED — the protected state")
	return st
}

// ReadDHCPSnooping reports what the device says about DHCP snooping.
// READ ONLY, replayable, one command.
func (c *Client) ReadDHCPSnooping(ctx context.Context) (driver.DHCPSnoopState, error) {
	var r, err = c.readSnooping(ctx)
	if err != nil {
		return driver.DHCPSnoopState{}, err
	}
	c.step(true, "DHCP snooping read",
		fmt.Sprintf("global %s, VLAN(s) %s, %d port(s) with a non-default trust/rate line",
			enabledWord(r.Enabled), orNone(joinInts(r.Configured)), len(r.Printed)))
	return snoopStateOf(r), nil
}

// snoopTrustCmds is the per-port write. Both directions are explicit: the intent
// carries no "leave it alone" value (cf. the contract), so an intent that
// reached here has something to say.
func snoopTrustCmds(cliName string, trusted bool) []string {
	if trusted {
		return []string{"interface " + cliName, "ip dhcp snooping trust", "exit"}
	}
	return []string{"interface " + cliName, "no ip dhcp snooping trust", "exit"}
}

// snoopVLANsToAdd: the plan's VLANs the device does not already snoop, bounded
// and sorted. NEVER a removal — a VLAN found enabled is protection this
// connector did not install and must not take away (cf. the contract).
func snoopVLANsToAdd(want []int, have []int) []int {
	var known = map[int]bool{}
	var v int
	for _, v = range have {
		known[v] = true
	}
	var out []int
	var seen = map[int]bool{}
	for _, v = range want {
		if v < 1 || v > 4094 || reservedVLAN(v) || known[v] || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// snoopVLANsRefused names the plan's VLANs this connector will NOT write, with
// the reason. Dropping them inside [snoopVLANsToAdd] and saying nothing would
// leave an operator reading a successful apply while the VLAN they asked to
// protect carries no snooping at all — exactly what ApplyResult.Skipped is for
// (cf. the contract: an ordinary refusal is reported, never silently dropped).
func snoopVLANsRefused(want []int) []string {
	var out []string
	var seen = map[int]bool{}
	var v int
	for _, v = range want {
		if seen[v] {
			continue
		}
		seen[v] = true
		if v < 1 || v > 4094 {
			out = append(out, fmt.Sprintf("VLAN %d not enabled for DHCP snooping: outside the "+
				"802.1Q range (1-4094) — nothing was sent to the device", v))
			continue
		}
		if reservedVLAN(v) {
			out = append(out, fmt.Sprintf("VLAN %d not enabled for DHCP snooping: 1002-1005 are "+
				"IOS's reserved FDDI/Token-Ring VLANs, which carry no Ethernet traffic to "+
				"inspect — nothing was sent to the device", v))
		}
	}
	return out
}

// snoopVLANsNotRetained: of the VLANs this apply asked for, the ones the
// re-read does NOT show snooping on. A "ip dhcp snooping vlan …" the CLI
// accepted and the device dropped is the silent failure the re-read exists to
// catch, and it is silent in the direction that matters: the operator sees a
// trusted port and believes the VLAN behind it is inspected.
func snoopVLANsNotRetained(asked []int, after []int) []int {
	return subtractInts(asked, after)
}

// ApplyDHCPSnooping sets the per-port trust flags, turns the feature on
// globally and on the plan's VLANs, verifies by RE-READ, then persists.
//
// The write is ordered trust-then-enable (cf. the header) and is otherwise
// deliberately dull: no candidate forms are tried, because unlike trunking
// nothing in this command set is known to differ across the Catalyst family.
// A refusal is therefore reported as a refusal rather than retried in another
// spelling that would mean the same thing.
func (c *Client) ApplyDHCPSnooping(ctx context.Context,
	plan driver.DHCPSnoopPlan) (ApplyResult, error) {

	var out = ApplyResult{VLANsCreated: []int{}, PortsWritten: []string{}}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	// The device comes first, always: without the current state this connector
	// cannot tell a VLAN it must add from one it would be re-adding, and cannot
	// tell whether it is about to turn the feature on for the whole box.
	var before, err = c.readSnooping(ctx)
	if err != nil {
		return out, fmt.Errorf("pre-write read failed: %v — meshcraft cannot tell which "+
			"VLANs already carry snooping, nor whether the feature is on, so nothing is "+
			"written", err)
	}

	// --- 1. Per-port trust, BEFORE the feature is turned on ---

	var intents = make([]driver.DHCPSnoopPortIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(i, j int) bool { return stdPortLess(intents[i].Port, intents[j].Port) })

	var attempted []driver.DHCPSnoopPortIntent
	var it driver.DHCPSnoopPortIntent
	for _, it = range intents {
		// THE STEM THE DEVICE PRINTED, not the assumed one.
		//
		// This was the last caller in the connector still using bare
		// [cliPortName], which hardcodes the stacked "1/0/" form; apply.go,
		// portadmin.go and stormcontrol.go all moved to [Client.portName] and
		// parse.go's comment named this line as the remaining one. On a chassis
		// that is not stacked IOS prints `GigabitEthernet0/1`, so the trust write
		// addressed `GigabitEthernet1/0/1` — an interface that does not exist.
		//
		// It never showed on the lab 2960-X because that switch does use "1/0/",
		// which is exactly why a hardcoded stem survives: it is right on the
		// hardware you happen to have.
		var cliName = c.portName(it.Port)
		if cliName == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not trusted/untrusted: this name is outside the standard-model mapping "+
					"this connector translates (stack member 1, module slot 0) — nothing was "+
					"sent to the device", it.Port))
			continue
		}
		var now, printed = before.Trust[it.Port]
		if printed && now == it.Trusted {
			continue // already exactly this, and the device printed it: nothing to send
		}
		var cerr = c.runConfigBlock(ctx, snoopTrustCmds(cliName, it.Trusted))
		if cerr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not %s: the CLI refused \"ip dhcp snooping trust\" (%s) — command set "+
					"written against IOS %s from documentation, survey this device's forms",
				it.Port, trustWord(it.Trusted), firstLine(cerr.Error()), DocumentedRelease))
			continue
		}
		attempted = append(attempted, it)
		c.step(true, it.Port+" "+trustWord(it.Trusted)+" for DHCP snooping", cliName)
	}

	// --- 2. The global switch and the VLANs, and both are REPORTED ---

	var enableCmds []string
	if !before.Enabled {
		enableCmds = append(enableCmds, "ip dhcp snooping")
	}
	var toAdd = snoopVLANsToAdd(plan.VLANs, before.Configured)
	out.Skipped = append(out.Skipped, snoopVLANsRefused(plan.VLANs)...)
	if len(toAdd) > 0 {
		enableCmds = append(enableCmds, "ip dhcp snooping vlan "+joinInts(toAdd))
	}
	if len(enableCmds) > 0 {
		var gerr = c.runConfigBlock(ctx, enableCmds)
		if gerr != nil {
			// The per-port writes have already landed and are inert without this.
			// Said plainly rather than swallowed: a trust flag on a switch that
			// snoops nothing is the failure this contract exists to prevent.
			out.Notes = append(out.Notes, "the per-port trust flags were written but \""+
				strings.Join(enableCmds, " ; ")+"\" was REFUSED ("+firstLine(gerr.Error())+
				"): DHCP snooping is not inspecting anything on this device, so those flags "+
				"protect nothing")
			c.step(false, "DHCP snooping not enabled", firstLine(gerr.Error()))
		} else {
			if !before.Enabled {
				out.Notes = append(out.Notes, "DHCP snooping was OFF on this device and this "+
					"apply turned it ON switch-wide (\"ip dhcp snooping\"): that affects EVERY "+
					"port, not just the ones in this plan — every port not trusted below now "+
					"drops DHCP server replies on the snooped VLANs")
			}
			if len(toAdd) > 0 {
				out.Notes = append(out.Notes, "snooping enabled on VLAN(s) "+joinInts(toAdd))
			}
			c.step(true, "DHCP snooping enabled", strings.Join(enableCmds, " ; "))
		}
	}

	var kept = subtractInts(before.Configured, plan.VLANs)
	if len(kept) > 0 {
		out.Notes = append(out.Notes, "VLAN(s) "+joinInts(kept)+" already carried snooping and "+
			"are NOT in this plan: they were left enabled. This connector never removes a "+
			"VLAN it finds — that protection was installed by someone else")
	}
	if len(plan.VLANs) == 0 {
		out.Notes = append(out.Notes, "no VLAN could be derived from the ports this plan "+
			"names, and the site declares none either: the global feature is what this "+
			"apply touched, and it covers "+
			orNone(joinInts(before.Configured))+" — a port shown as trusted is trusted on a "+
			"feature that inspects that, and nothing more")
	}
	if !before.Enabled && !anyTrusted(plan.Ports) && len(plan.VLANs) > 0 {
		out.Notes = append(out.Notes, "this plan turns snooping on for VLAN(s) "+
			joinInts(plan.VLANs)+" and trusts NO port: on those VLANs every DHCP OFFER and "+
			"ACK will now be dropped, including the ones coming from the site's own server. "+
			"The uplink toward it is the port that must be trusted")
	}

	// --- 3. The re-read is authoritative, not the return codes ---

	var after snoopRead
	after, err = c.readSnooping(ctx)
	if err != nil {
		// NOT `PortsWritten = attempted`: a re-read that could not be taken
		// confirms nothing (cf. driver.ApplyResult.PortsWritten). The ports are
		// NAMED in the note instead, which is where the fact belongs and where
		// the frontend actually shows it.
		out.Unconfirmed("DHCP snooping trust", driver.SnoopIntentPorts(attempted),
			firstLine(err.Error()))
		return out, nil
	}
	for _, it = range attempted {
		var now, printed = after.Trust[it.Port]
		if printed && now == it.Trusted {
			out.PortsWritten = append(out.PortsWritten, it.Port)
			continue
		}
		if !printed && !it.Trusted {
			// An untrusted port drops off this table entirely once it carries no
			// rate limit either: absence IS the confirmation here, and the note
			// says so rather than leaving a caller to read a shorter list as a
			// failure.
			out.PortsWritten = append(out.PortsWritten, it.Port)
			continue
		}
		out.Notes = append(out.Notes, it.Port+": the CLI accepted the command but the "+
			"re-read does not show it "+trustWord(it.Trusted)+" — a return code that changes "+
			"nothing is exactly what this re-read exists to catch")
	}
	if !after.Enabled {
		out.Notes = append(out.Notes, "after the write, this device still reports DHCP "+
			"snooping OFF switch-wide: nothing is being inspected")
	}
	// The VLAN list is re-read for the same reason the trust flags are: "ip dhcp
	// snooping vlan 99" accepted on a VLAN the switch does not have leaves the
	// configured list unchanged and prints nothing.
	var lost = snoopVLANsNotRetained(toAdd, after.Configured)
	if len(lost) > 0 {
		out.Notes = append(out.Notes, "the CLI accepted \"ip dhcp snooping vlan "+joinInts(toAdd)+
			"\" but the re-read does not show snooping configured on VLAN(s) "+joinInts(lost)+
			": those VLANs are NOT being inspected, whatever the trust flags above say")
	}

	// --- 4. Persist: protection a reboot forgets is protection nobody has ---

	var perr = c.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes, "applied but NOT persisted: "+firstLine(perr.Error())+
			" — a reboot would take the snooping configuration with it")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", SaveCmd)
	return out, nil
}

func anyTrusted(ports []driver.DHCPSnoopPortIntent) bool {
	var p driver.DHCPSnoopPortIntent
	for _, p = range ports {
		if p.Trusted {
			return true
		}
	}
	return false
}

func trustWord(trusted bool) string {
	if trusted {
		return "trusted"
	}
	return "untrusted"
}

func enabledWord(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

func orNone(s string) string {
	if s == "" {
		return "no VLAN"
	}
	return s
}
