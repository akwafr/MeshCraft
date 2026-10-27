package swos

import (
	"context"
	"fmt"

	"meshcraft/internal/driver"
)

// Reading a SwOS's management addressing: how this device obtained the
// address meshcraft talks to it on.
//
// Writes live in mgmtwrite.go ([driver.MgmtAddrWriter], [driver.MgmtJumper]).
// This file only reads /sys.b.
//
// # What the survey establishes, and which changes how the product displays it
//
// The WRITABLE field set of /sys.b is enumerated by the device's
// configuration backup (testdata/backup.swb, CSS326-24G-2S+, SwOS 2.17), hence
// exhaustive — it is the same source as pages.go's, and it is what allows
// drawing an ABSENCE from it rather than a "not found":
//
//	sys.b:{id, wdt, dsc, pdsc, ivl, alla, allm, avln, allp, mgmt, prio, cost,
//	       frmc, poe, igmp, igmq, igfl, igve, ip, dtrp, ainf, iptp}
//
// There is `ip` and `iptp`. There is NEITHER A GATEWAY NOR A DNS RESOLVER, in
// any tab, and the platform's complete web UI fits in a single file that
// defines all its fields — enumeration exhausts the question, exactly as it
// exhausted it for the trap receiver.
//
// The consequence is an operational constraint, and it deserves to be stated
// where it is read: without a gateway, off-subnet egress (traps, DNS) has no
// route here; inbound administration can still answer over L2. That is what
// `Capabilities().Mgmt.Gateway = false` publishes, and it is what must stop a
// UI from offering a "gateway" field on this range: having someone type a
// value that would not be written makes the operator conclude meshcraft did
// not write what they asked for.

// What will make COMPILATION fail the day the contract moves — same reason as
// the assertions in contract.go: a type assertion at run time would fail
// silently, and in the field.
var _ driver.MgmtAddrReader = (*Admin)(nil)

// ReadMgmtAddressing says how this switch obtained its administration
// address.
//
// ONE PAGE is read, /sys.b, and it is the cheapest of the platform (780
// bytes) — the one authentication already reads to test the credentials. On a
// platform where every page costs two HTTP requests (Digest) and accesses are
// serialised, a UI that displays three fields must not cost four.
func (a *Admin) ReadMgmtAddressing(ctx context.Context) (driver.MgmtAddrState, error) {
	var sys, err = a.Get(ctx, PageSys)
	if err != nil {
		return driver.MgmtAddrState{}, fmt.Errorf("%s unreadable: %w", PageSys, err)
	}
	var out = MgmtAddrOf(sys)
	a.step(true, "management addressing read back",
		fmt.Sprintf("%s: mode \"%s\", address %s", PageSys,
			modeLabel(out.Mode), addrLabel(out.Address)))
	return out, nil
}

// MgmtAddrOf translates /sys.b into observed management addressing.
//
// PURE, like PlanPorts and for the same reason: it is verifiable on the
// device's real capture (testdata/sys.b) rather than on hardware, and it is
// what decides what the UI will assert about the configuration of a switch in
// service.
//
// THREE DECISIONS, each against a specific lie.
//
// 1. THE ADDRESS RETURNED IS `cip`, THE CURRENT ONE, AND NOT `ip`.
//
// The two fields coexist and do not mean the same thing: `ip` is the
// CONFIGURED address, `cip` the one actually carried. In static mode they are
// equal; on DHCP, `ip` keeps the fallback address and `cip` carries the lease.
// Returning `ip` would display, on a DHCP device, an address it does not
// answer on — and that is the kind of value an operator copies into a browser
// before concluding there is an outage. Falling back on `ip` only happens if
// `cip` is missing, and it is SAID in the notes.
//
// 2. AN UNKNOWN `iptp` DOES NOT TRANSLATE TO "STATIC".
//
// Three values are observed in the UI's "Address Acquisition" selector
// (0 "DHCP with fallback", 1 "static", 2 "DHCP only"). A fourth, on a more
// recent firmware, would return an EMPTY mode and a note — not the most likely
// mode. "We do not know" is an exact answer; guessing "static" on a device on
// a lease would keep as intentional an address that may change at any moment.
//
// 3. "DHCP WITH FALLBACK" IS RETURNED AS DHCP, and the fallback is stated in
// a note.
//
// It is indeed DHCP from the point of view of the question asked — the
// address is not decided by the operator — but the practical consequence is
// particular and worth displaying: this device HAS an address even when no
// server answers, which sends someone looking for a mute device where it does
// answer, at another address (cf. MgmtCapabilities.DHCPFallback, the same trap
// observed on Instant On).
func MgmtAddrOf(sys *Page) driver.MgmtAddrState {
	var out driver.MgmtAddrState

	var mode, hasMode = sys.Num(FieldAddrMode)
	if hasMode {
		switch mode {
		case AddrStatic:
			out.Mode = driver.MgmtAddrModeStatic
		case AddrDHCPOnly:
			out.Mode = driver.MgmtAddrModeDHCP
		case AddrDHCPFallback:
			out.Mode = driver.MgmtAddrModeDHCP
			out.Notes = append(out.Notes, "mode \"DHCP with fallback\": this device "+
				"carries a fallback address even when no DHCP server answers. A "+
				"CSS326 believed mute may therefore answer elsewhere, on its fallback "+
				"address, and not at the expected address.")
		default:
			out.Notes = append(out.Notes, fmt.Sprintf(
				"unrecognised acquisition mode (%s = 0x%02x): this firmware offers a "+
					"value the connector has not observed. meshcraft therefore concludes NOTHING "+
					"about the origin of the address rather than assuming the most likely one.",
				FieldAddrMode, mode))
		}
	} else {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"%s does not serve the \"%s\" field: the origin of the address is not readable on "+
				"this firmware.", PageSys, FieldAddrMode))
	}

	var cur, hasCur = sys.Num(FieldCurrentIP)
	var cfg, hasCfg = sys.Num(FieldIP)
	switch {
	case hasCur && cur != 0:
		out.Address = DecodeIPv4(cur)
		// A gap between configured and current is an anomaly only if the
		// device says it is static — otherwise it is normal DHCP operation.
		if hasCfg && cfg != cur && out.Mode == driver.MgmtAddrModeStatic {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"the device declares itself on static address %s but answers on %s. One of "+
					"the two fields was changed without the other following: to be verified before any "+
					"write on this page.", DecodeIPv4(cfg), DecodeIPv4(cur)))
		}
	case hasCfg && cfg != 0:
		out.Address = DecodeIPv4(cfg)
		out.Notes = append(out.Notes, fmt.Sprintf(
			"CURRENT address (\"%s\") missing from the page: the CONFIGURED address "+
				"(\"%s\") is the one displayed. On a DHCP device, the two may "+
				"differ.", FieldCurrentIP, FieldIP))
	}

	// Absence of gateway/DNS is a PLATFORM CAPABILITY (Gateway/DNS/Routed/IPARP
	// on MgmtCapabilities), not a per-read note: notes are localized in the UI
	// from caps, never stored as French/English prose in the observation.
	// No named interface: SwOS does not attach its address to an interface.
	// What stands in for a scope is "Allow From VLAN" (`avln`), which is the
	// management VLAN and is already read in State.MgmtVLAN — putting it back
	// here under the name "interface" would invite comparing it with a RouterOS
	// interface name, which is something else entirely.
	return out
}

// modeLabel and addrLabel exist only for the transcript, so that a transcript
// line never says `mode "", address ""` — a log that displays emptiness reads
// as a connector fault, where it is the device that said nothing.
func modeLabel(mode string) string {
	switch mode {
	case driver.MgmtAddrModeStatic:
		return "static"
	case driver.MgmtAddrModeDHCP:
		return "DHCP"
	}
	return "not observed"
}

func addrLabel(addr string) string {
	if addr == "" {
		return "unknown"
	}
	return addr
}
