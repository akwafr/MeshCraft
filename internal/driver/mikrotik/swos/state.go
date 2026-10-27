package swos

import (
	"context"
	"fmt"
	"meshcraft/internal/driver"
	"sort"
	"strconv"
)

// Reading the state ACTUALLY applied on a SwOS switch.
//
// This is the "Read" step of the reconciliation pipeline: without it,
// meshcraft cannot know whether what it believes is what is, and
// "configuration drift" remains a word.
//
// TWO PAGES are enough, and they say complementary things:
//
//	GET /vlan.b → the VLAN TABLE: an identifier, a name, and a mask of MEMBER
//	              ports.
//	GET /fwd.b  → per PORT: its VLAN mode (`vlan`), what it accepts on ingress
//	              (`vlni`), and its "Default VLAN ID" (`dvid`).
//
// THE SwOS VLAN MODEL, AND THE TRANSLATION IT IMPOSES
//
// SwOS nowhere says "this port is tagged in this VLAN". There is NO field for
// that — neither in the VLAN table, nor per port, nor in the complete
// configuration backup. What the platform carries is leaner:
//
//   - the VLAN table says WHO is a member of what;
//   - each port has ONE "Default VLAN ID", which is its native VLAN.
//
// The tagging rule follows from that, and it is the only one that makes
// these two pieces of data sufficient: a frame leaves a port WITHOUT a tag if
// its VLAN is that port's "Default VLAN ID", and WITH a tag otherwise. That is
// what MikroTik's documentation says for this field, and it is what the
// absence of any other setting confirms — the device's UI, which exposes
// absolutely all its fields, offers nothing else.
//
// So, for a port of index i:
//
//	PVID(i)    = dvid[i]
//	Tagged(i)  = { vid of the table | i is a member of vid } \ { dvid[i] }
//	Mode(i)    = "trunk" if Tagged(i) is not empty, "access" otherwise
//
// WHAT CORROBORATES THIS TRANSLATION ON THE REFERENCE DEVICE, and it is no
// small thing: applied as is to the reference CSS326-24G-2S+, it says that
// cage SFP2 — the only port carrying a link — is a trunk with native VLAN 1
// carrying VLANs 99 and 100 tagged. Now this device is administered in VLAN
// 99 (`avln:0x0063`) and it ANSWERS. Its learned address table shows hosts in
// VLAN 99 on that same port. The translation therefore predicts exactly the
// only switching fact one can observe from the outside: that VLAN 99 arrives
// tagged through that port. An inverted tagging rule would predict the
// opposite, and the device would be mute.
//
// That does not replace a traffic injection — it is written here so that it
// is known what is established and what is not.

// `VLAN`, `PortState` and `State` are no longer defined here: they are ALIASES
// of the contract types (cf. contract.go). This package used to carry copies,
// and its comment already said why — "same shape as the Instant On
// connector's, so the API has only one way to present a write", "that is
// what lets the reconciliation layer compare two platforms without telling
// them apart". The alias keeps that promise instead of repeating it: what
// this file builds IS what the API layer reads back, with no conversion,
// hence no field lost the day the contract gains one.
//
// What the comments of those copies carried that was specific to SwOS was
// MOVED into the contract, where it was needed — see [driver.State.MgmtVLAN],
// whose documentation says explicitly that zero is not "unknown" here but
// "administration accepted from any VLAN", and that protection then falls
// back on link state alone (cf. ApplyPorts).
//
// ONE field could not follow, and it now lives below: the per-port VLAN mode.
// The contract does not carry it — there is nothing universal about it — and
// [driver.State.Notes] is what tells the operator what they need to know.

// VLANFiltering returns, per port, the VLAN mode the device APPLIES
// ("disabled", "optional", "enabled", "strict", cf. VLANModeLabel).
//
// Specific to this platform, and that is why it is not a standard state
// field: "disabled" means the port filters NOTHING, and the PVID and
// membership read elsewhere then do not describe what the port actually does
// with traffic. Keeping quiet about that distinction would present a VLAN
// configuration as active when it is INERT — and that is the kind of silence
// that sends someone hunting for a fault elsewhere for an hour.
//
// What the state read does with it: a sentence in [driver.State.Notes],
// because the caller must DISPLAY it and has no decision to draw from it (cf.
// stateOf).
type VLANFiltering map[string]string

// Disabled returns, sorted, the ports whose VLAN filtering is off.
//
// Sorted because this list goes into a note read by a human and compared from
// one read to the next: Go's map iteration order varies on every run, and
// would make a note that does not change flicker.
func (f VLANFiltering) Disabled() []string {
	var out []string
	var name, mode string
	for name, mode = range f {
		if mode == "disabled" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// snapshot is the set of pages the state needs, read in ONE pass.
//
// It exists for a reason of COST, and that reason is specific to this
// platform. SwOS has no session: every page costs TWO HTTP requests, the
// Digest challenge then the authenticated request. And this connector
// declares `MaxSessions: 1`, so its accesses are serialised. A first version
// re-read /link.b twice per state read, and /vlan.b and /fwd.b a second time
// before writing: eight requests too many on an access switch's CPU, on every
// reconciliation pass, across the whole fleet. Passing the pages around rather
// than asking for them again is not a style optimisation here — it is
// consistency with the cost we documented ourselves.
type snapshot struct {
	ports    int
	sfp      int
	sfpFirst int
	// facade is the geometry AND the naming, inseparable: the position of the
	// cages decides whether index 24 is called "SFP1" or "Port25" (cf. Facade).
	facade   Facade
	link     *Page
	fwd      *Page
	sys      *Page
	vlanRows []*Page
}

// readAll fetches the four state pages: /link.b, /vlan.b, /fwd.b, /sys.b.
func (a *Admin) readAll(ctx context.Context) (*snapshot, error) {
	var snap = &snapshot{}
	var err error
	snap.link, err = a.Get(ctx, PageLink)
	if err != nil {
		return nil, err
	}
	snap.ports, snap.sfp, snap.sfpFirst, err = geometryOf(snap.link)
	if err != nil {
		a.step(false, "model geometry unreadable", err.Error())
		return nil, err
	}
	// The geometry is cached: writes need it to compose the /fwd.b field set,
	// and they must not re-read /link.b to get it.
	a.ports, a.sfp, a.sfpFirst = snap.ports, snap.sfp, snap.sfpFirst
	snap.facade = a.facade()
	a.step(true, "model geometry read",
		fmt.Sprintf("%d ports including %d SFP cage(s), the first at index %d — "+
			"named \"%s\" … \"%s\"", snap.ports, snap.sfp, snap.sfpFirst,
			snap.facade.Name(0), snap.facade.Name(snap.ports-1)))

	snap.vlanRows, err = a.GetTable(ctx, PageVLAN)
	if err != nil {
		return nil, err
	}
	snap.fwd, err = a.Get(ctx, PageFwd)
	if err != nil {
		return nil, err
	}
	snap.sys, err = a.Get(ctx, PageSys)
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// ReadState reads the device back. READ ONLY: nothing is modified, which
// allows replaying it as often as wanted, including on a fleet in production.
//
// This is the "Read" step of the reconciliation pipeline, and the signature is
// the contract's: no parameter. PORT NAMES ARE THE DEVICE'S — "Port1"…
// "Port24", "SFP1", "SFP2" — and so they do not come from the call: they come
// from the geometry /link.b serves, through [Facade]. Two calls on the same
// access must name the same sockets, otherwise the write would land on a
// different port from the one the UI showed.
//
// meshcraft's numbered family ("GE25") remains ACCEPTED on input, and the
// prefix carrying it still comes from the TARGET (`driver.Target.PortPrefix`,
// kept by [Driver.Authenticate]). It is never written any more — see the box in
// naming.go.
//
// FOUR PAGES, TWO HTTP REQUESTS EACH: /link.b, /vlan.b, /fwd.b, /sys.b, i.e.
// eight round trips. SwOS has no session, Digest is renegotiated every time
// (a 401 carrying the challenge, then the authenticated request) and none of
// it can be pooled. That is the real cost of a state read on this platform,
// and it is what justifies readAll reading them in ONE pass to share them with
// the write (cf. snapshot).
func (a *Admin) ReadState(ctx context.Context) (State, error) {
	var snap, err = a.readAll(ctx)
	if err != nil {
		return State{VLANs: []VLAN{}, Ports: map[string]PortState{}}, err
	}
	var out State
	out, err = a.stateOf(snap)
	if err != nil {
		return out, err
	}
	// Aggregates, from /lacp.b (a fifth page — two more HTTP round trips, the
	// platform's known cost). Tolerated failure: nil reads as "not read".
	var lags, lagErr = a.readLAGs(ctx)
	if lagErr == nil {
		out.LAGs = lags
	} else {
		out.Notes = append(out.Notes, "aggregates not read: "+lagErr.Error())
	}
	return out, nil
}

// stateOf rebuilds the state from a snapshot and records what was understood.
func (a *Admin) stateOf(snap *snapshot) (State, error) {
	var out = State{VLANs: []VLAN{}, Ports: map[string]PortState{}}

	var err error
	out.VLANs, err = vlansFrom(snap.vlanRows)
	if err != nil {
		a.step(false, "VLAN table unreadable", err.Error())
		return out, err
	}
	a.step(true, "VLANs read on the device",
		fmt.Sprintf("%d VLAN(s): %s", len(out.VLANs), vlanSummary(out.VLANs)))

	var filtering VLANFiltering
	out.Ports, filtering, err = portsFrom(snap.vlanRows, snap.fwd, snap.facade)
	if err != nil {
		a.step(false, "port membership unreadable", err.Error())
		return out, err
	}
	a.step(true, "port VLAN membership rebuilt",
		fmt.Sprintf("%d port(s)", len(out.Ports)))

	// THIS PLATFORM'S MANDATORY NOTE: VLAN filtering turned off makes the read
	// configuration INERT.
	//
	// It goes into State.Notes and not only into the transcript, and the
	// difference matters: the transcript tells what an operation did, the notes
	// accompany the STATE itself — hence the UI that displays these ports'
	// native VLAN and membership. Without this sentence, that UI would present
	// a segmentation in place where the device filters nothing: the PVID and the
	// member list are indeed configured, and the port ignores them. It is a fact
	// no field of the standard model can express, and keeping quiet about it
	// would send someone hunting for the fault elsewhere for an hour.
	//
	// A SENTENCE and not a code: the caller displays it as is and draws no
	// decision from it (the contract is explicit about that). If a decision had
	// to be made, it would be this connector's to make — and it would refuse.
	var inert = filtering.Disabled()
	if len(inert) > 0 {
		var note = "VLAN filtering DISABLED on " + joinComma(inert) + " (/fwd.b " +
			"\"" + FieldVLANMode + "\" = disabled): on these ports, the native VLAN and " +
			"the membership above are indeed configured but WITHOUT EFFECT on traffic — " +
			"the port filters nothing and lets everything through. What is displayed for them is " +
			"an intent, not a behaviour. A meshcraft write WAKES this filtering up " +
			"when it can do so without risking access (cf. planModeRaise)."
		out.Notes = append(out.Notes, note)
		a.step(true, "VLAN filtering disabled on some ports", note)
	}

	// THE THREE OTHER PER-PORT FIELDS, and what they contradict.
	//
	// The VLAN mode is not the only setting able to make the displayed
	// membership lie: "Force VLAN ID" puts all incoming traffic into the native
	// VLAN, "VLAN Receive" can drop tagged or untagged traffic on ingress, and a
	// PVID without membership leads nowhere. These states are accepted by the
	// device without any warning, and each one makes a UI showing the native
	// VLAN and the list of tagged VLANs DESCRIBE SOMETHING OTHER than what the
	// port does with traffic.
	//
	// Same nature as the note above, same destination: [driver.State.Notes],
	// sentences the caller DISPLAYS without drawing a decision from them.
	var anomalies = vlanAnomalies(snap.vlanRows, snap.fwd, snap.facade)
	if len(anomalies) > 0 {
		out.Notes = append(out.Notes, anomalies...)
		var note string
		for _, note = range anomalies {
			a.step(true, "contradictory VLAN configuration observed", note)
		}
	}

	// The management VLAN, FROM the device. The only source that says where
	// the current session goes through.
	var avln, hasAvln = snap.sys.Num(FieldAllowVLAN)
	if !hasAvln {
		// Field missing: no assumption. Without a known management VLAN, the
		// write will fall back on protection by link.
		var note = "The device's management VLAN could not be read: this " +
			"firmware does not serve the \"" + FieldAllowVLAN + "\" field of /sys.b. A write " +
			"will remain possible, but it will protect itself on link state alone — so " +
			"it will spare EVERY active port."
		out.Notes = append(out.Notes, note)
		a.step(false, "management VLAN unknown", note)
	} else {
		out.MgmtVLAN = int(avln)
		if avln == 0 {
			// Zero is a LEGITIMATE state here, not ignorance — and it is the one
			// of a factory-fresh device. The note exists because the contract
			// forbids the caller from interpreting this zero itself: on Instant
			// On it means "unknown" and makes the write refuse, here it means
			// "from any VLAN" and the write remains possible.
			var note = "The device accepts administration from ANY VLAN " +
				"(/sys.b \"" + FieldAllowVLAN + "\" = 0), which is its factory state: there " +
				"is no management VLAN to spare, and a write will therefore protect itself on " +
				"link state alone — every active port will be spared."
			out.Notes = append(out.Notes, note)
			a.step(true, "administration accepted from any VLAN", note)
		} else {
			a.step(true, "device management VLAN",
				"VLAN "+strconv.Itoa(out.MgmtVLAN))
		}
	}

	var upMap map[string]bool
	upMap, err = linksFrom(snap.link, snap.facade)
	out.Links = driver.LinksFromUp(upMap)
	if err != nil {
		a.step(false, "link state unreadable", err.Error())
		return out, err
	}
	a.step(true, "link state read",
		fmt.Sprintf("%d port(s) with link", len(out.LinkUp())))

	// A PORT'S LOCAL LABEL, when it differs from the faceplate name. Same nature
	// as the notes above — a sentence to display, no decision to draw from it —
	// and it exists for the reason that made us adopt the device's names: so
	// that the socket meshcraft talks about and the one the operator sees in
	// SwOS are the same. A port renamed on the device is the only case where the
	// two screens still diverge, and this sentence is what explains it (cf.
	// naming.go).
	var renamed = renamedNote(snap.link, snap.facade)
	if renamed != "" {
		out.Notes = append(out.Notes, renamed)
		a.step(true, "ports renamed on the device", renamed)
	}
	return out, nil
}

// vlansFrom reads the VLAN table. A pure function, hence verifiable on a
// capture.
//
// A row without an identifier is not silently ignored: it is a table we cannot
// read, and the write that followed would rebuild the ENTIRE table from what
// we understood of it — hence losing that row. On this platform, where writing
// a table means replacing it, refusing to read is better than losing a VLAN.
func vlansFrom(rows []*Page) ([]VLAN, error) {
	var out = []VLAN{}
	var i int
	var row *Page
	for i, row = range rows {
		var vid, ok = row.Num(FieldVLANID)
		if !ok {
			return nil, fmt.Errorf("/vlan.b: row %d carries no \"%s\" field — "+
				"table refused rather than half read, the rewrite would lose this row",
				i+1, FieldVLANID)
		}
		// The name is optional and often empty: the reference device names
		// none.
		var name, _ = row.Text(FieldVLANName)
		out = append(out, VLAN{ID: int(vid), Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func vlanSummary(vlans []VLAN) string {
	var parts = make([]string, 0, len(vlans))
	var v VLAN
	for _, v = range vlans {
		var s = strconv.Itoa(v.ID)
		if v.Name != "" {
			s += " \"" + v.Name + "\""
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "none"
	}
	return joinComma(parts)
}

// portsFrom rebuilds the PER-PORT configuration from the VLAN table and the
// forwarding page.
//
// This is where the switch of viewpoint happens: the device reasons per VLAN
// ("who is a member?") and per port only for the native VLAN, meshcraft
// reasons per port ("what does this port carry?"). The tagging rule applied is
// the one documented at the top of the file.
func portsFrom(vlanRows []*Page, fwd *Page, f Facade) (
	map[string]PortState, VLANFiltering, error) {
	var ports = f.Ports()
	var dvid, hasDvid = fwd.Nums(FieldDefaultVLAN)
	if !hasDvid {
		return nil, nil, fmt.Errorf("/fwd.b does not carry the \"%s\" array: "+
			"without the native VLANs, port membership cannot be rebuilt",
			FieldDefaultVLAN)
	}
	if len(dvid) < ports {
		return nil, nil, fmt.Errorf("/fwd.b: \"%s\" carries only %d value(s) for "+
			"%d port(s) — truncated page or inconsistent geometry",
			FieldDefaultVLAN, len(dvid), ports)
	}
	// The VLAN mode is optional on read: its absence does not prevent reading
	// membership, it only prevents saying whether filtering is active. The two
	// are kept apart rather than refusing the whole read.
	var modes, hasModes = fwd.Nums(FieldVLANMode)

	var out = map[string]PortState{}
	var filtering = VLANFiltering{}

	var i int
	for i = 0; i < ports; i++ {
		var name = f.Name(i)
		var st = PortState{Mode: "access", PVID: int(dvid[i])}

		var row *Page
		for _, row = range vlanRows {
			var vid, ok = row.Num(FieldVLANID)
			if !ok {
				// vlansFrom has already refused this case; nothing is made up here.
				continue
			}
			var member bool
			member, ok = row.Bit(FieldVLANMembers, i)
			if !ok {
				return nil, nil, fmt.Errorf("/vlan.b: VLAN %d carries no readable member "+
					"mask (\"%s\") — the membership of port %s would be guessed",
					vid, FieldVLANMembers, name)
			}
			if !member {
				continue
			}
			// The port's native VLAN is not tagged on this port: that is the whole rule.
			if int(vid) == st.PVID {
				continue
			}
			st.Tagged = append(st.Tagged, int(vid))
		}
		if len(st.Tagged) > 0 {
			// A port that carries tagged VLANs is a trunk, whatever its native
			// VLAN.
			sort.Ints(st.Tagged)
			st.Mode = "trunk"
		}
		out[name] = st

		if hasModes && i < len(modes) {
			var label, ok = VLANModeLabel(modes[i])
			if ok {
				filtering[name] = label
			}
		}
	}
	return out, filtering, nil
}

// linksFrom reads the ports that carry a link.
//
// Only ports WITH a link are returned: absence from the map means "no link",
// which is also the cautious default when the read said nothing. A missing
// mask, on the other hand, is an ERROR and not "no link" — believing no port
// has a link would lift the write guard on all of them, which is exactly the
// opposite of caution.
func linksFrom(link *Page, f Facade) (map[string]bool, error) {
	var ports = f.Ports()
	if !link.Has(FieldLink) {
		return nil, fmt.Errorf("/link.b does not carry the \"%s\" mask: link state "+
			"is unknown, and assuming it empty would suggest no port "+
			"carries the current session", FieldLink)
	}
	var out = map[string]bool{}
	var i int
	for i = 0; i < ports; i++ {
		var up, ok = link.Bit(FieldLink, i)
		if !ok {
			return nil, fmt.Errorf("/link.b: mask \"%s\" too short for %d port(s)",
				FieldLink, ports)
		}
		if up {
			out[f.Name(i)] = true
		}
	}
	return out, nil
}

func joinComma(parts []string) string {
	var out string
	var i int
	for i = 0; i < len(parts); i++ {
		if i > 0 {
			out += ", "
		}
		out += parts[i]
	}
	return out
}
