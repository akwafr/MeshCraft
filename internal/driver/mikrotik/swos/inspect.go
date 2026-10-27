package swos

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Identifying a SwOS switch — and this platform's good surprise.
//
// SwOS hands out the EXACT MODEL REFERENCE without any credentials. Its HTTP
// authentication challenge carries the model in its `realm`:
//
//	$ GET /sys.b (without credentials)
//	HTTP/1.1 401 Unauthorized
//	WWW-Authenticate: Digest realm="CSS326-24G-2S+", qop="auth",
//	                  nonce="84aaad9d", stale=FALSE
//
// Verified on 192.0.2.36. It is gold for discovery: the precise model — hence
// the capabilities, hence the faceplate geometry — is obtained BEFORE having
// the slightest secret, which no other platform in the project offers. Where
// the Instant On connector has to dig through the hidden fields of its login
// page in the hope of a `sysDescr`, here knocking on the door is enough.
//
// What the realm does NOT give, and which therefore requires authenticating:
// the serial number, the firmware version, the system name, the MAC address
// and the real geometry. All of that lives in /sys.b and /link.b, behind
// Digest.
//
// The distinction is structural in Identity.Source: the UI must not present
// on the same footing what was obtained at the door and what was read inside.

// Identity is what is known about a SwOS switch.
type Identity struct {
	// Model is the exact vendor reference ("CSS326-24G-2S+").
	Model string `json:"model"`
	// Family is the sub-range ("CSS", "CRS"), useful for the capability
	// differences that play out at that level.
	Family   string `json:"family"`
	Serial   string `json:"serial,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	// Build is the firmware's build timestamp, as the device's UI displays it
	// next to the version.
	Build string `json:"build,omitempty"`
	// Hostname is the device's "Identity" field.
	Hostname string `json:"hostname,omitempty"`
	// Revision and BoardName: often empty on CSS, filled in on other boards.
	// Returned as is rather than guessed.
	Revision  string `json:"revision,omitempty"`
	BoardName string `json:"board_name,omitempty"`
	// Faceplate: what lets the device be drawn without a catalogue.
	CopperPorts int `json:"copper_ports"`
	SFPPorts    int `json:"sfp_ports"`
	// SFPFirst is the (0-based) index of the first SFP cage. On every model
	// met so far the cages are at the end of the faceplate, but SwOS has a
	// field to say so and it is better to read it than to assume it.
	SFPFirst int  `json:"sfp_first"`
	SFPPlus  bool `json:"sfp_plus"`
	PoE      bool `json:"poe"`
	// MgmtIP / MgmtVLAN: the administration addressing AS THE DEVICE CARRIES
	// IT. Used for a cutover's pre-flight: it is the only source that says
	// where the current session goes through.
	MgmtIP   string `json:"mgmt_ip,omitempty"`
	MgmtVLAN int    `json:"mgmt_vlan,omitempty"`
	// Source says where the information comes from. `digest_realm` = obtained
	// WITHOUT authentication; `sys.b` = read under authentication.
	Source string `json:"source"`
}

// reRealm extracts the realm of a `WWW-Authenticate` challenge.
var reRealm = regexp.MustCompile(`(?i)realm="([^"]+)"`)

// header finds a header in a response of the shared transport.
//
// `driver.Response.Headers` is a BARE map (`map[string][]string`), not an
// `http.Header`: it has no `Get`, and above all it carries the keys in the
// case the SERVER sent. Direct access by `"WWW-Authenticate"` would work as
// long as the firmware capitalises that way, and would return an empty string
// the day one capitalises otherwise — i.e. would conclude "this is not a
// SwOS" on a device that is one. The comparison is therefore
// case-insensitive, as HTTP requires.
func header(headers map[string][]string, name string) string {
	var k string
	var v []string
	for k, v = range headers {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// hdrAuthenticate is the header that carries the challenge — and, on SwOS,
// the model.
const hdrAuthenticate = "WWW-Authenticate"

// reModel recognises a MikroTik switching board reference: the sub-range,
// then everything that follows to the end of the realm.
//
// Deliberately anchored on the TWO sub-ranges SwOS runs on, CSS and CRS, and
// not on "anything that looks like a model": matching too broadly would pass
// a device that is not a SwOS off as one, and the capabilities that follow
// decide what gets written.
var reModel = regexp.MustCompile(`^(CSS|CRS)[0-9A-Za-z+\-]*$`)

// ModelFromRealm extracts the model from the realm of a Digest challenge.
//
// Returns `ok=false` rather than an empty string when the realm does not look
// like a MikroTik reference: an unexpected realm means we are not talking to a
// SwOS, and it is better to say so than to force the device into this range.
func ModelFromRealm(challenge string) (string, bool) {
	var m = reRealm.FindStringSubmatch(challenge)
	if m == nil {
		return "", false
	}
	var realm = strings.TrimSpace(m[1])
	if !reModel.MatchString(realm) {
		return "", false
	}
	return realm, true
}

// FamilyOf returns the sub-range of a model ("CSS326-24G-2S+" → "CSS").
func FamilyOf(model string) string {
	var up = strings.ToUpper(model)
	switch {
	case strings.HasPrefix(up, "CSS"):
		return "CSS"
	case strings.HasPrefix(up, "CRS"):
		return "CRS"
	}
	return ""
}

// Inspect identifies the device at the given address.
//
// Two steps, and the order matters: first the realm, WITHOUT credentials,
// which returns the model even if the secret is wrong or unknown; then
// /sys.b and /link.b behind Digest, for everything else. A partial
// identification is returned with its error rather than lost — knowing "it is
// a CSS326-24G-2S+ whose password we do not have" is infinitely better than
// "failure".
func Inspect(ctx context.Context, host, user, secret string, d driver.Dialer) (Identity, error) {
	var out Identity

	// 1. The realm, at the door.
	var model, err = realmModel(ctx, host, d)
	if err != nil {
		return out, err
	}
	out.Model, out.Family, out.Source = model, FamilyOf(model), "digest_realm"
	out.PoE = ModelHasPoE(model)

	// 2. The rest, inside. The partial identity is returned WITH the error:
	//    the model is acquired, and losing it because the rest failed would be
	//    losing the only thing this platform gives for free.
	var admin = OpenAdmin(host, user, secret, d)
	err = admin.fillIdentity(ctx, &out)
	if err != nil {
		return out, fmt.Errorf("model identified (%s) but %w", model, err)
	}
	return out, nil
}

// realmModel knocks on the door and returns the model the Digest challenge
// announces.
//
// WITHOUT ANY CREDENTIALS, and that is the whole point: the session has no
// authentication armed, so the 401 is not replayed and its header is exactly
// what we came for. This read costs ONE HTTP request, not two — there is no
// authenticated request behind it.
//
// Verified again live on the reference device through the tunnel:
// `WWW-Authenticate: Digest realm="CSS326-24G-2S+", qop="auth", nonce="4753ad9d",
// stale=FALSE`, served with the 401 itself, and returned identically when the
// secret presented is wrong.
func realmModel(ctx context.Context, host string, d driver.Dialer) (string, error) {
	var anon = driver.NewWebSession("http://"+host, d, 0)
	var resp, err = anon.Get(ctx, PageSys)
	if err != nil {
		return "", fmt.Errorf("no response from %s on port 80: %w", host, err)
	}
	var challenge = header(resp.Headers, hdrAuthenticate)
	var model, ok = ModelFromRealm(challenge)
	if !ok {
		return "", fmt.Errorf("the device at %s does not present a SwOS challenge "+
			"(HTTP %d, %s: %q) — it is not a SwOS switch, or its firmware does not "+
			"announce itself that way", host, resp.Status, hdrAuthenticate, challenge)
	}
	return model, nil
}

// fillIdentity completes an identity with what requires authenticating.
//
// On an Admin rather than on host parameters, for two reasons: errors go into
// this access's TRANSCRIPT, and a credentials refusal comes back to the caller
// as errCredentialsRefused — which lets Driver.Identify know whether to try
// the next candidate or stop.
//
// The identity is completed IN PLACE and progressively: when /link.b fails
// after a successful /sys.b, the serial and the firmware are already there,
// and the caller returns them with its error. A partial identification is
// infinitely better than a failure.
func (a *Admin) fillIdentity(ctx context.Context, id *Identity) error {
	var sys, sysErr = a.Get(ctx, PageSys)
	if sysErr != nil {
		return fmt.Errorf("%s unreadable: %w", PageSys, sysErr)
	}
	fillFromSys(id, sys)

	var link, linkErr = a.Get(ctx, PageLink)
	if linkErr != nil {
		return fmt.Errorf("%s unreadable, the faceplate remains unknown: %w", PageLink, linkErr)
	}
	return fillFromLink(id, link)
}

// Contract translates what was observed into the identity the contract
// carries.
//
// A TRANSLATION AND NOT AN ALIAS, and the contract asks for it explicitly:
// "Platform-specific fields do not belong here […] Connectors keep their own,
// richer identity type, and fill this one in for the API layer." The
// direction of flow is that one: the contract carries what the CALLER
// consumes.
//
// WHAT THIS CONVERSION LEAVES BEHIND, said here so nobody goes looking for
// it: `Revision` and `BoardName` (empty on CSS, filled in on other boards),
// `SFPFirst` (the index of the first cage — the information survives in
// PortGroups, whose second group starts there), `MgmtIP` and `MgmtVLAN` (the
// administration addressing as the device carries it, which the contract
// exposes otherwise: [driver.State.MgmtVLAN], returned by ReadState). All of
// it remains readable through [Inspect], which discovery calls.
//
// `ModelCode` stays EMPTY, and that is correct: MikroTik has no catalogue
// reference distinct from the product label — "CSS326-24G-2S+" IS the
// reference. Copying the model there would give two identical fields, one of
// which would suggest a second source.
func (id Identity) Contract() driver.Identity {
	return driver.Identity{
		Model:       id.Model,
		Family:      id.Family,
		Serial:      id.Serial,
		MAC:         id.MAC,
		Firmware:    id.Firmware,
		Build:       id.Build,
		Hostname:    id.Hostname,
		CopperPorts: id.CopperPorts,
		SFPPorts:    id.SFPPorts,
		SFPPlus:     id.SFPPlus,
		PoE:         id.PoE,
		PortGroups:  id.PortGroups(),
		Source:      id.Source,
	}
}

// fillFromSys completes the identity from /sys.b. A pure function, hence
// verifiable on a real capture.
//
// Every field is optional by construction: an older firmware may not serve
// one, and what is missing stays EMPTY. No field is inferred from another — a
// serial number invented from the system name, as the Instant On connector
// does for lack of anything better, would have no basis here.
func fillFromSys(id *Identity, sys *Page) {
	// The /sys.b model confirms the realm's. It replaces it only if present,
	// the realm being the safest source — the firmware composes it, it is not
	// an editable field.
	var brd, ok = sys.Text(FieldBoard)
	if ok && brd != "" {
		id.Model = brd
		id.Family = FamilyOf(brd)
		id.PoE = ModelHasPoE(brd)
		id.Source = "sys.b"
	}
	// The text fields, in the order the device serves them. A table rather
	// than a series of blocks: it shows at a glance what is read and what is
	// not, and a missing field leaves its destination EMPTY without anyone
	// having to think about it.
	var texts = []struct {
		field string
		dest  *string
	}{
		{FieldSerial, &id.Serial},
		{FieldVersion, &id.Firmware},
		{FieldRevision, &id.Revision},
		{FieldMarket, &id.BoardName},
		{FieldIdentity, &id.Hostname},
	}
	var tf struct {
		field string
		dest  *string
	}
	for _, tf = range texts {
		var v, has = sys.Text(tf.field)
		if has {
			*tf.dest = v
		}
	}

	var build, hasBuild = sys.Num(FieldBuild)
	if hasBuild && build != 0 {
		id.Build = strconv.FormatUint(uint64(build), 10)
	}

	// The MAC is served without a separator: `'b869f45f14a8'`.
	var raw, hasMAC = sys.Value(FieldMAC)
	if hasMAC && raw.Kind == KindStr {
		var mac, macOK = DecodeMAC(raw.Raw)
		if macOK {
			id.MAC = mac
		}
	}

	// The CURRENT address (`cip`) rather than the configured one (`ip`): on
	// DHCP the two differ, and the session goes through the former.
	var cip, hasCIP = sys.Num(FieldCurrentIP)
	if hasCIP && cip != 0 {
		id.MgmtIP = DecodeIPv4(cip)
	}
	var avln, hasVLAN = sys.Num(FieldAllowVLAN)
	if hasVLAN {
		id.MgmtVLAN = int(avln)
	}
}

// fillFromLink completes the faceplate from /link.b.
func fillFromLink(id *Identity, link *Page) error {
	var ports, sfp, first, err = geometryOf(link)
	if err != nil {
		return err
	}
	id.CopperPorts, id.SFPPorts, id.SFPFirst = ports-sfp, sfp, first
	// SFP or SFP+: the reference says so ("2S+" = two 10-gigabit cages), and
	// the negotiated rate confirms it when a transceiver is inserted. We rely
	// on the reference, the only one that answers even with empty cages.
	id.SFPPlus = sfp > 0 && strings.Contains(id.Model, "S+")
	return nil
}

// reModelPoE recognises a reference with power over the cable.
//
// MikroTik's naming convention marks PoE with a `P` where models without PoE
// carry a `G`: "CRS328-24P-4S+" versus "CSS326-24G-2S+". The pattern is
// therefore a port count followed by `P`, and nothing else — matching an
// isolated `P` would pass "CSS326-24G-2S+" off as PoE at the first `+`.
//
// It is worth what a naming convention is worth, i.e. less than an
// observation: that is why the PoE in Features is not declared by this
// pattern alone when the device's geometry is readable (cf. the comment on
// Features).
var reModelPoE = regexp.MustCompile(`(?i)[0-9]+P(-|$)|PoE`)

// ModelHasPoE says whether the reference announces PoE.
func ModelHasPoE(model string) bool { return reModelPoE.MatchString(model) }

// PortGroups translates the faceplate into port groups of the standard model.
//
// TWO FAMILIES, AND THEY ARE THE DEVICE'S: "Port1"… "Port24" for copper,
// "SFP1" and "SFP2" for the cages, each starting again at 1. That is what
// /link.b serves in `nm`, it is what the switch's UI displays, and it is
// therefore what the operator reads on the two screens they compare.
//
// CONTINUOUS numbering including cages — "GE25" for the first cage, borrowed
// from the Instant On range — was what this connector used to write. These
// groups are what the web UI EXPANDS to enumerate the sockets
// (`expandPorts`), so changing the prefix here is what makes the UI name the
// ports like the device does. The old family's names remain ACCEPTED on input
// by [Facade.Index]: intents already recorded keep landing on the right port.
//
// The neighbouring RouterOS connector has done exactly this since its first
// delivery, and for the same reason — its two groups are `ether` from 1 and
// `sfp-sfpplus` from 1. Two ranges from the same vendor named as they name
// themselves are better than two ranges numbered alike and differently from
// their firmware.
func (id Identity) PortGroups() []map[string]any {
	var out = []map[string]any{}
	if id.CopperPorts > 0 {
		var label = fmt.Sprintf("1–%d", id.CopperPorts)
		if id.PoE {
			label += " · PoE"
		}
		out = append(out, map[string]any{
			"prefix": CopperPortPrefix, "count": id.CopperPorts, "from": 1,
			"speed": "1G", "poe": id.PoE, "label": label,
		})
	}
	if id.SFPPorts > 0 {
		var speed, name = "1G", "SFP"
		if id.SFPPlus {
			speed, name = "10G", "SFP+"
		}
		// `from` is 1 and not `SFPFirst + 1`: the cages have their OWN counter,
		// and that is what distinguishes this faceplate from the old one. The
		// label keeps the "+" when the reference carries it — that is also what
		// makes internal/portrules classify the socket as SFP+.
		out = append(out, map[string]any{
			"prefix": SFPPortPrefix, "count": id.SFPPorts, "from": 1,
			"speed": speed, "poe": false, "label": name,
		})
	}
	return out
}
