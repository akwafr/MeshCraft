package instanton

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/snmpv3"
)

// SNMP configuration of an Instant On — step 4 of §5 of
// docs/03-SNMP Security/Identity.
//
// This is the last step in the chain, and the only one that touches the
// fleet: `snmpprov` decides what SHOULD be, this file sets it. The
// separation is not cosmetic — it lets a decision be verified against an
// entire fleet before a single write goes out.
//
// The writing conventions are the ones already established for this range
// (cf. admin.go/SetPassword and apply.go): URL carrying the list of
// sections, `DeviceConfiguration` envelope, action as an ATTRIBUTE of the
// section, and REREAD verification after each write — on this range, a
// `statusCode 0` with no effect is a proven case.
//
// # Numeric codes, and where they come from
//
// Everything below is observed on lab hardware (1930 JL685A), in its own
// bundle and its own dictionary — never guessed:
//
//   - `/hpe/js/modules.js`: `_TRUE = '1'`, `_FALSE = '2'`;
//   - `/hpe/js/out/mainpage.js`, page "Get Connected → SNMP Management":
//     the EXACT gesture the device makes to enable SNMP and set a
//     community (see the Communities section below);
//   - `/hpe/js/out/pages2.js`, page "Switching → SNMP": the radio-button
//     lists, which give the code for each algorithm and the token for the
//     associated label;
//   - `/hpe/english/dictionary1.xml`: the translation of these tokens.
//
// Hence the table, which is the heart of this file:
//
// SNMPEnbl 1 = enabled 2 = disabled
//
//	authenticationMethod   0 = None   1 = MD5    2 = SHA
//	privacyMethod          1 = None              2 = DES
//	accessMode (v2c)       1 = DefaultRead  2 = DefaultWrite  3 = DefaultSuper
//	securityModel (group)  1 = SNMPv1  2 = SNMPv2  3 = SNMPv3  (0 = factory group)
//	securityType (level)   1 = noAuthNoPriv  2 = authNoPriv  3 = authPriv
//	engineIDType           1 = local             2 = remote
//
// TRAP: "none" is not the same code depending on the field — 0 for
// authentication, 1 for encryption. A connector that set `privacyMethod = 0`
// by symmetry would send a value outside the enumeration.
//
// The v2c communities section is called `CommunityList`, not
// `SNMPCommunityList` — the latter name gets `statusCode -1` ("Cannot find
// the section"), exactly like a section that does not exist.
const (
	sectionSNMPGlobal       = "SNMPGlobalSetting"
	sectionCommunities      = "CommunityList"
	sectionSNMPGroups       = "SNMPGroupList"
	sectionSNMPUsers        = "SNMPUserISList"
	snmpEnabledCode         = "1"
	snmpDisabledCode        = "2"
	authCodeNone            = "0"
	authCodeMD5             = "1"
	authCodeSHA             = "2"
	privCodeNone            = "1"
	privCodeDES             = "2"
	accessModeReadOnly      = "1"
	securityModelV3         = "3"
	securityLevelAuthNoPriv = "2"
	securityLevelAuthPriv   = "3"
	engineIDTypeLocal       = "1"
	// viewReadOnly is the factory READ view: it exposes the `iso` tree but
	// hides `usmUser`, `snmpVacmMIB` and `snmpCommunityMIB` — in other
	// words, anything that would allow reading the SNMP secrets themselves.
	// It is exactly what we want to give a collector (read from
	// `ViewOIDList` on the device).
	viewReadOnly = "Default"
	// stationAny: the community applies to any administration station. This
	// is what the device itself sets from its "Get Connected" page. ADDRESS
	// FILTERING does not replace a secret — it is bypassed by spoofing the
	// address — and the real containment of SNMP on this range lies
	// elsewhere: it only listens on the management VLAN.
	stationAny = "0.0.0.0"
	// stationTypeAll is the value the device interface sends systematically
	// in `managementStationType`, including with
	// `managementStationIPAddress = 0.0.0.0`. It is reproduced as-is: its
	// enumeration is unknown, and a field copied identically from what the
	// device does cannot be "the wrong code".
	stationTypeAll = "1"
)

// Input limits, mirroring the validation rules of the device's own forms
// (`pages2.js`). Checking them HERE rather than letting the device refuse
// avoids a round trip whose error message does not say which field is at
// fault.
const (
	communityMaxLen = 20
	userMaxLen      = 20
	groupMaxLen     = 30
	secretMaxLen    = 32
)

// defaultV3Group names the access group that meshcraft creates for its v3
// users. A DEDICATED group, rather than reusing a factory group: factory
// groups are in `securityModel 0` (v1/v2c) and two of them carry a WRITE
// view. Attaching a v3 user to one would grant it write access without
// that having been requested.
const defaultV3Group = "meshcraft-ro"

// --- What the device carries ---

// SNMPCommunity is a community declared on the device.
// AccessMode is left in CODE: translating it here would lose exact
// information, and it is on the code that verification is performed.
type SNMPCommunity struct {
	Name       string `json:"name"`
	AccessMode string `json:"access_mode"`
	Station    string `json:"station,omitempty"`
}

// ReadOnly says if this community only provides reading.
func (c SNMPCommunity) ReadOnly() bool { return c.AccessMode == accessModeReadOnly }

// SNMPGroup is an access group (the “who sees what” of SNMPv3).
type SNMPGroup struct {
	Name          string `json:"name"`
	SecurityModel string `json:"security_model"`
	SecurityLevel string `json:"security_level"`
	ReadView      string `json:"read_view,omitempty"`
	WriteView     string `json:"write_view,omitempty"`
	// NotifyView decides whether the device can ADDRESS a trap to users of
	// this group. It is read and not only written because it governs
	// everything downstream: a v3 user attached to a group with no
	// notification view can be designated as a trap recipient without any
	// trap ever going out, and nothing in the device interface shows it
	// (cf. ApplyTrapReceiver).
	NotifyView string `json:"notify_view,omitempty"`
}

// SNMPUser is a v3 user. Auth and Priv are the device's CODES, not
// algorithm names: that is what it renders, and verification compares what
// we sent against what it returns.
type SNMPUser struct {
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
	Auth  string `json:"auth"`
	Priv  string `json:"priv"`
}

// SNMPState is the observed SNMP state. There is no secret in it: the
// device does not return v3 passwords, and the community — which IS the
// secret — is carried only by its name, which is never logged.
type SNMPState struct {
	Enabled     bool            `json:"enabled"`
	EngineID    string          `json:"engine_id,omitempty"`
	Communities []SNMPCommunity `json:"communities"`
	Groups      []SNMPGroup     `json:"groups"`
	Users       []SNMPUser      `json:"users"`
}

// Community finds a community by name.
func (s SNMPState) Community(name string) (SNMPCommunity, bool) {
	var c SNMPCommunity
	for _, c = range s.Communities {
		if c.Name == name {
			return c, true
		}
	}
	return SNMPCommunity{}, false
}

// Group finds a group by name.
func (s SNMPState) Group(name string) (SNMPGroup, bool) {
	var g SNMPGroup
	for _, g = range s.Groups {
		if g.Name == name {
			return g, true
		}
	}
	return SNMPGroup{}, false
}

// User finds a v3 user by name.
func (s SNMPState) User(name string) (SNMPUser, bool) {
	var u SNMPUser
	for _, u = range s.Users {
		if u.Name == name {
			return u, true
		}
	}
	return SNMPUser{}, false
}

// --- What meshcraft wants to set ---

// `SNMPIdentity` and `SNMPResult` are now the contract types
// (`driver.SNMPIdentity`, `driver.SNMPResult`), by ALIAS: cf. contract.go.
// The fields this package carried are there, with the same JSON tags, and
// both halves — v2c and v3 — remain independent: an empty field means
// "nothing to do here", never "remove". An empty group always defaults to
// [defaultV3Group].
//
// `SNMPIdentity` gains a `Sources` field, which this connector IGNORES, and
// that is the behavior the contract requires. It was born from a RouterOS
// refusal, whose community has an `addresses` field that must not be left
// open to all addresses without being asked. This range has no such
// setting: it confines SNMP to the management VLAN by construction, there
// is nothing to confine. Pretending to read it would give the operator the
// idea of a restriction that does not exist.

// --- Translating algorithms into codes ---

// authAlgCode translates an authentication algorithm name into a device
// code. An unknown algorithm is REFUSED, never rounded to "closest":
// placing MD5 where the caller wanted SHA-256 would produce monitoring
// that works and false compliance.
func authAlgCode(alg string) (string, error) {
	switch snmpv3.Normalize(alg) {
	case "md5":
		return authCodeMD5, nil
	case "sha", "sha1":
		return authCodeSHA, nil
	}
	return "", fmt.Errorf("SNMPv3 authentication %q impossible on this product line: "+
		"it only accepts MD5 or SHA-1 (guide 5200-5879, and enumeration noted"+
		"in the equipment bundle)", alg)
}

// privAlgCode translates an encryption algorithm name. Empty and "none"
// both mean "no encryption" — a degraded but legitimate v3 pair:
// authentication alone already protects against theft.
func privAlgCode(alg string) (string, error) {
	var n = snmpv3.Normalize(alg)
	switch n {
	case "", "none", "nopriv":
		return privCodeNone, nil
	case "des":
		return privCodeDES, nil
	}
	return "", fmt.Errorf("SNMPv3 encryption %q impossible on this product line: "+
		"it only accepts DES (guide 5200-5879, and listing noted in the"+
		"equipment bundle)", alg)
}

// --- Reading ---

var (
	reCommunityEntry = regexp.MustCompile(`(?is)<CommunityEntry>(.*?)</CommunityEntry>`)
	reSNMPGroupBlock = regexp.MustCompile(`(?is)<SNMPGroup>(.*?)</SNMPGroup>`)
)

// sectionOf isolates ONE section from the response. Indispensable as soon
// as several sections are read at once: v3 users are arranged in `<Entry>`
// sections, a name so generic that searching for it across the whole
// document would pick up entries from any other section.
func sectionOf(body, name string) string {
	var re = regexp.MustCompile(`(?is)<` + regexp.QuoteMeta(name) +
		`(?:\s[^>]*)?>(.*?)</` + regexp.QuoteMeta(name) + `>`)
	var m = re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

// ReadSNMP reads the device's SNMP state. READ ONLY: also replayable as
// often as wanted, including on a fleet in production.
//
// All four sections go into ONE query. This is not a micro-optimization:
// this range only accepts five simultaneous web sessions, and reading
// section by section would amount to ousting the human administrator from
// their own interface.
func (a *Admin) ReadSNMP(ctx context.Context) (SNMPState, error) {
	var out = SNMPState{
		Communities: []SNMPCommunity{}, Groups: []SNMPGroup{}, Users: []SNMPUser{},
	}
	var url = a.wcdURL() + "?{" + sectionSNMPGlobal + "}{" + sectionCommunities +
		"}{" + sectionSNMPGroups + "}{" + sectionSNMPUsers + "}"
	var resp, err = a.web.Get(ctx, url)
	if err != nil {
		a.step(false, "reading SNMP configuration", err.Error())
		return out, err
	}

	var global = sectionOf(resp.Body, sectionSNMPGlobal)
	out.Enabled = xmlField(global, "SNMPEnbl") == snmpEnabledCode
	// Local Engine ID is required to create a v3 user
	// in local engineIDType: this is what serves as salt for key derivation.
	out.EngineID = xmlField(global, "localEngineID")

	var block string
	for _, block = range reCommunityEntry.FindAllString(
		sectionOf(resp.Body, sectionCommunities), -1) {
		out.Communities = append(out.Communities, SNMPCommunity{
			Name:       xmlField(block, "communityString"),
			AccessMode: xmlField(block, "accessMode"),
			Station:    xmlField(block, "managementStationIPAddress"),
		})
	}
	for _, block = range reSNMPGroupBlock.FindAllString(
		sectionOf(resp.Body, sectionSNMPGroups), -1) {
		out.Groups = append(out.Groups, SNMPGroup{
			Name:          xmlField(block, "SNMPGroupName"),
			SecurityModel: xmlField(block, "securityModel"),
			SecurityLevel: xmlField(block, "securityType"),
			ReadView:      xmlField(block, "readView"),
			WriteView:     xmlField(block, "writeView"),
			NotifyView:    xmlField(block, "notifyView"),
		})
	}
	for _, block = range reEntryBlock.FindAllString(
		sectionOf(resp.Body, sectionSNMPUsers), -1) {
		out.Users = append(out.Users, SNMPUser{
			Name:  xmlField(block, "userName"),
			Group: xmlField(block, "groupName"),
			Auth:  xmlField(block, "authenticationMethod"),
			Priv:  xmlField(block, "privacyMethod"),
		})
	}

	// The transcript carries COUNTS, never the names: on this range, the
	// name of a community IS the secret that grants access to the device.
	a.step(true, "SNMP configuration read", fmt.Sprintf(
		"SNMP %s — %d community(s), %d group(s), %d user(s) v3",
		enabledLabel(out.Enabled), len(out.Communities), len(out.Groups), len(out.Users)))
	return out, nil
}

func enabledLabel(on bool) string {
	if on {
		return "activated"
	}
	return "disabled"
}

// --- Writing ---

// ApplySNMP sets the SNMP identity decided by provisioning.
//
// The order is not negotiable, and follows from §4 of the spec:
//
//  1. ENABLE SNMP. Devices leaving the factory carry `SNMPEnbl = 2`: as long
//     as it is not enabled, asking for a community or a user will collect
//     nothing, and the operator will look for the fault on the collector
//     side.
//  2. the v2c community, read-only;
//  3. the v3 access group then the user — in that order, since the device
//     indexes the user by its group;
//  4. flash save, without which everything disappears on reboot.
//
// Each write is followed by a REREAD. On this range, `statusCode 0` does
// not mean "done": this is the characteristic failure mode of this
// interface, already encountered on `VLANInterfaceMembershipTable` (see
// apply.go).
//
// What is NOT done here, deliberately: nothing is ever REMOVED. An empty
// field means "I'm not touching this", not "delete". Removing an
// operator's community because meshcraft does not know its value would cut
// monitoring they never asked to lose.
func (a *Admin) ApplySNMP(ctx context.Context, want SNMPIdentity) (SNMPResult, error) {
	var out SNMPResult

	// All validation BEFORE the first writing: better not to ask anything
	// only a half state where SNMP is open with no identity on it.
	var authCode, privCode string
	var err error
	if want.User != "" {
		if err = checkName("SNMPv3 user name", want.User, userMaxLen); err != nil {
			return out, err
		}
		if want.Group == "" {
			want.Group = defaultV3Group
		}
		if err = checkName("SNMPv3 group name", want.Group, groupMaxLen); err != nil {
			return out, err
		}
		if authCode, err = authAlgCode(want.AuthAlg); err != nil {
			return out, err
		}
		if privCode, err = privAlgCode(want.PrivAlg); err != nil {
			return out, err
		}
		// "noAuthNoPriv" exists in the standard and protects nothing:
		// refusing it is better than letting people believe in v3 (same rule
		// as snmpv3.Negotiate, which never offers it).
		if authCode == authCodeNone || want.AuthSecret == "" {
			return out, fmt.Errorf("SNMPv3 user without authentication " +
				"protects nothing: meshcraft refuses to install it")
		}
		if err = checkSecret("authentication password", want.AuthSecret); err != nil {
			return out, err
		}
		if privCode != privCodeNone {
			if err = checkSecret("encryption password", want.PrivSecret); err != nil {
				return out, err
			}
		}
	}
	if want.Community != "" {
		if err = checkName("SNMPv2c community", want.Community, communityMaxLen); err != nil {
			return out, err
		}
	}
	if want.Community == "" && want.User == "" {
		return out, fmt.Errorf("no SNMP identity to set: neither v2c community " +
			"nor v3 user")
	}

	// We never differ against a cached state (Reconciliation §2).
	var st SNMPState
	if st, err = a.ReadSNMP(ctx); err != nil {
		return out, err
	}

	// 1. Enable SNMP.
	if !st.Enabled {
		if err = a.writeSection(ctx, sectionSNMPGlobal, "set",
			"<SNMPEnbl>"+snmpEnabledCode+"</SNMPEnbl>",
			"SNMP enabled on the device"); err != nil {
			return out, err
		}
	}
	out.Enabled = true

	// 2. The v2c community, READ ONLY.
	if want.Community != "" {
		var have, seen = st.Community(want.Community)
		if seen && have.ReadOnly() {
			// Already compliant: sending nothing is better than a round trip, and
			// makes the operation idempotent.
			a.step(true, "SNMPv2c community already in place read-only",
				"no writing required")
		} else {
			if err = a.writeSection(ctx, sectionCommunities, "set",
				communityEntry(want.Community),
				"SNMPv2c community set to READ ONLY"); err != nil {
				return out, err
			}
			out.CommunitySet = true
		}
	}

	// 3. The v3 access group, then the user.
	if want.User != "" {
		var level = securityLevelAuthPriv
		if privCode == privCodeNone {
			level = securityLevelAuthNoPriv
		}
		var group, hasGroup = st.Group(want.Group)
		if !hasGroup || group.SecurityModel != securityModelV3 ||
			group.SecurityLevel != level || group.WriteView != "" {
			if err = a.writeSection(ctx, sectionSNMPGroups, "set",
				groupEntry(want.Group, level),
				"SNMPv3 access group “"+want.Group+"» set to read-only"); err != nil {
				return out, err
			}
			out.GroupCreated = true
		}

		var have, seen = st.User(want.User)
		// "Already compliant" is judged on the NAME, the TWO algorithm codes
		// and the GROUP. A homonymous user set up with MD5 where we want
		// SHA-1 is not the same user: its keys are derived differently.
		var conform = seen && have.Auth == authCode && have.Priv == privCode &&
			have.Group == want.Group
		if conform {
			// We do NOT rewrite it: the device does not offer a `set` on
			// this section — its interface offers only `add` and `delete`
			// — and replacing it would mean deleting it first, hence
			// interrupting collection for nothing.
			a.step(true, "SNMPv3 user already in place",
				"same algorithms and same group: no writing necessary")
		} else {
			if st.EngineID == "" {
				// Without an engine identifier, key derivation has no salt:
				// the user would be asked but no authentication
				// would work. Say it rather than sending a made-up value.
				return out, fmt.Errorf("the device has not declared its localEngineID: " +
					"cannot set an SNMPv3 user without it")
			}
			if seen {
				// It exists but does not match. Replacing it requires two gestures,
				// the device indexing users by name — and deletion recalls the
				// ORIGINAL group and engine, otherwise it matches no entry.
				if err = a.writeSection(ctx, sectionSNMPUsers, "delete",
					"<Entry><userName>"+xmlEscape(want.User)+"</userName>"+
						"<groupName>"+xmlEscape(have.Group)+"</groupName>"+
						"<engineIDType>"+engineIDTypeLocal+"</engineIDType>"+
						"<engineID>"+xmlEscape(st.EngineID)+"</engineID></Entry>",
					"old SNMPv3 user removed before replacement"); err != nil {
					return out, err
				}
			}
			if err = a.writeSection(ctx, sectionSNMPUsers, "add",
				userEntry(want, st.EngineID, authCode, privCode),
				"SNMPv3 user "+want.User+" set ("+
					snmpv3.Normalize(want.AuthAlg)+"/"+privLabel(privCode)+")"); err != nil {
				return out, err
			}
			out.UserSet = true
		}
	}

	// VERIFICATION by reread. It is this that distinguishes "the device
	// answered 0" from "the device did something".
	if err = a.verifySNMP(ctx, want, authCode, privCode); err != nil {
		return out, err
	}

	// 4. Make it durable. An apply that a reboot cancels is worse than an
	//    apply refused, because it was believed to have succeeded.
	a.SaveConfig(ctx) //nolint:errcheck // logged in the transcript
	return out, nil
}

// verifySNMP rereads the device and compares to what was just requested.
//
// Each deviation becomes an EXPLICIT error which names the faulty section: on
// this interface, "code 0 but nothing has changed" is the most common failure mode
// expensive, because it presents itself as a success.
func (a *Admin) verifySNMP(ctx context.Context, want SNMPIdentity,
	authCode, privCode string) error {
	var after, err = a.ReadSNMP(ctx)
	if err != nil {
		return err
	}
	if !after.Enabled {
		return fmt.Errorf("the equipment has accepted the activation of SNMP (code 0) but"+
			"reread %s = %s: write without effect, nothing will be collected",
			sectionSNMPGlobal, snmpDisabledCode)
	}
	if want.Community != "" {
		var c, seen = after.Community(want.Community)
		if !seen {
			return fmt.Errorf("the device has accepted the SNMPv2c community (code 0)"+
				"but rereading %s does not show it: writing without effect",
				sectionCommunities)
		}
		if !c.ReadOnly() {
			// NEVER let it go: a community that gives writing is a
			// clear root password on the network.
			return fmt.Errorf("the SNMPv2c community was installed with accessMode %q"+
				"instead of %q (read-only): meshcraft does not deploy"+
				"writing community", c.AccessMode, accessModeReadOnly)
		}
	}
	if want.User != "" {
		var u, seen = after.User(want.User)
		if !seen {
			return fmt.Errorf("the device has accepted the SNMPv3 user (code 0)"+
				"but rereading %s does not show it: writing without effect",
				sectionSNMPUsers)
		}
		// Codes are compared, not just presence: equipment that
		// silently falling back to MD5 where SHA-1 was requested would give a
		// supervision that works and false compliance.
		if u.Auth != authCode || u.Priv != privCode {
			return fmt.Errorf("the SNMPv3 user has been installed with "+
				"authenticationMethod %q / privacyMethod %q instead of %q / %q "+
				"requested: the device did not retain the algorithm pair",
				u.Auth, u.Priv, authCode, privCode)
		}
		var g, hasGroup = after.Group(want.Group)
		if !hasGroup {
			return fmt.Errorf("device accepted SNMPv3 group %q (code 0) "+
				"but rereading %s does not show it: writing without effect",
				want.Group, sectionSNMPGroups)
		}
		if g.WriteView != "" {
			return fmt.Errorf("SNMPv3 group %q carries a WRITE view (%q): "+
				"meshcraft does not deploy SNMP identity on write",
				want.Group, g.WriteView)
		}
	}
	a.step(true, "SNMP configuration verified by replay",
		"the equipment does what was requested")
	return nil
}

// communityEntry makes a community READ-ONLY, in the exact form that
// installs the equipment from its own “Get Connected → SNMP Management” page
// (noted in `hpe/js/out/mainpage.js`):
//
//	CommunityList.set.CommunityEntry = {communityString, managementStationType:'1',
//	  managementStationIPAddress:'0.0.0.0', Basic:{accessMode:'1', viewName:''}}
//
// `accessMode` 1 is `DefaultRead`. The other two codes in the enumeration —
// 2 `DefaultWrite` and 3 `DefaultSuper` — appear NOWHERE in this file, and
// that is the simplest guarantee that no code path can set up a community
// with write access.
//
// `viewName` remains empty: this is what the device sends, `accessMode` sufficient
// to designate the factory reading view.
func communityEntry(name string) string {
	return "<CommunityEntry>" +
		"<communityString>" + xmlEscape(name) + "</communityString>" +
		"<managementStationType>" + stationTypeAll + "</managementStationType>" +
		"<managementStationIPAddress>" + stationAny + "</managementStationIPAddress>" +
		"<Basic><accessMode>" + accessModeReadOnly + "</accessMode>" +
		"<viewName></viewName></Basic>" +
		"</CommunityEntry>"
}

// groupEntry makes a v3 access group READ-ONLY.
//
// `writeView` is empty, and that is the important point: it is THIS,
// not the password, that decides a v3 user cannot write. `notifyView` is
// set so the device can send its traps to this group — this is what its
// own interface does, checking "Notify" by default for a v3 group.
func groupEntry(name, level string) string {
	return "<SNMPGroup>" +
		"<SNMPGroupName>" + xmlEscape(name) + "</SNMPGroupName>" +
		"<securityModel>" + securityModelV3 + "</securityModel>" +
		"<securityType>" + level + "</securityType>" +
		"<readView>" + viewReadOnly + "</readView>" +
		"<writeView></writeView>" +
		"<notifyView>" + viewReadOnly + "</notifyView>" +
		"</SNMPGroup>"
}

// userEntry renders a v3 user, in the form of the device's own "Add SNMP
// User" form (`hpe/js/out/pages2.js`, `addUserSubmit`).
//
// `engineIDType` 1 = LOCAL: the user is attached to the SNMP engine of the
// device itself, whose identifier serves as salt for key derivation. Remote
// mode (2) is used for Inform trap receivers and would require an entry in
// `SNMPRemoteEngineIDList` — useless for a collector that only polls.
//
// Passwords go out IN CLEAR TEXT, without the `$` marker used for the
// administration password: that marker is specific to `AdminUserSetting`,
// and adding it here would derive a key from an incorrect password.
func userEntry(want SNMPIdentity, engineID, authCode, privCode string) string {
	var entry = "<Entry>" +
		"<userName>" + xmlEscape(want.User) + "</userName>" +
		"<groupName>" + xmlEscape(want.Group) + "</groupName>" +
		"<engineIDType>" + engineIDTypeLocal + "</engineIDType>" +
		"<engineID>" + xmlEscape(engineID) + "</engineID>" +
		"<authenticationMethod>" + authCode + "</authenticationMethod>" +
		"<authenticationPassword>" + xmlEscape(want.AuthSecret) + "</authenticationPassword>" +
		"<privacyMethod>" + privCode + "</privacyMethod>"
	// The encryption password is ONLY sent if there is encryption:
	// the equipment grays out this field when `privacyMethod` is “none”.
	if privCode != privCodeNone {
		entry += "<privacyPassword>" + xmlEscape(want.PrivSecret) + "</privacyPassword>"
	} else {
		entry += "<privacyPassword></privacyPassword>"
	}
	return entry + "</Entry>"
}

func privLabel(code string) string {
	if code == privCodeDES {
		return "des"
	}
	return "no privacy"
}

// checkName verifies an identifier before sending it. The limits are those
// of the device's own forms: applying them here gives a message that names
// the faulty field, where the device just gives an error code.
//
// Spaces and control characters are rejected because a community name
// containing one is indistinguishable, in a log, from one that doesn't —
// and no collector would be able to grab it reliably.
func checkName(what, v string, max int) error {
	if v == "" {
		return fmt.Errorf("%s is empty", what)
	}
	if len(v) > max {
		return fmt.Errorf("%s too long (%d characters, maximum %d on this product line)",
			what, len(v), max)
	}
	var i int
	for i = 0; i < len(v); i++ {
		if v[i] <= ' ' || v[i] == 0x7f {
			return fmt.Errorf("%s: whitespace or control character forbidden", what)
		}
	}
	return nil
}

// checkSecret checks a v3 password. No complexity floor here: it is
// meshcraft's own vault that generates these secrets (pwgen), and imposing
// one more rule in the connector would create two places to change it.
func checkSecret(what, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s is empty", what)
	}
	if len(v) > secretMaxLen {
		return fmt.Errorf("%s too long (%d characters, maximum %d on this product line)",
			what, len(v), secretMaxLen)
	}
	return nil
}

// --- Le chemin RETOUR : les destinataires de traps ---
//
// The above teaches meshcraft to QUERY the device. What follows teaches it
// to make the device SPEAK — the last missing layer of the trap channel:
// the proxy knows how to receive, the controller knows how to correlate,
// and no device had ever received the order to send.
//
// Everything is recorded on `sw-labo-1930` (JL685A, firmware 3.4.0.0) in
// READ ONLY and recorded in docs/01-Architecture/Connecteurs.md, section
// "SNMP trap recipients". Four facts govern this code:
//
//  1. There are TWO sections, not one. v1/v2c lives in `SNMPTrap`, whose
//     `SNMPTrapReceiverList` / `SNMPTrapReceiverEntry` are CHILDREN — the
//     same trap as `CommunityList`, which is not `SNMPCommunityList`:
//     asking for `SNMPTrapReceiverList` as a section responds with
//     `statusCode -1`, exactly like a section that does not exist. v3 lives
//     in `NotificationReceiverList`, which IS the list, with `Receiver`
//     entries — and there is no section named "SNMPTrapV3".
//  2. Two form DEFAULTS are traps. `SNMPVersion` defaults to 1 (SNMPv1):
//     omitting it declares a v1 recipient, which the v2c collector will not
//     be able to make use of. And `notificationType = 1` (Trap) forces
//     `timeout` and `retries` to be EMPTY — this is what the interface does
//     before sending.
//  3. Nothing needs to be enabled per port. The device's 68 interfaces
//     already carry `linkStateTrapEnabled = 1` (`Standard802_3List`): just
//     declare a recipient.
//  4. The WRITE gesture is a RECONSTRUCTION, never executed. It is deduced
//     from `tmpl.post()`/`tmpl.json2xml()` (`modules.js`) composed with the
//     objects from `addTrapReceiversV1V2Submit` / `addTrapReceiversV3Submit`
//     (`pages2.js`). Hence the rule that structures this entire file — and
//     the reason a refusal is translated HERE into a message that
//     distinguishes "the table is full" from "the write hypothesis is
//     false": these are the only two things a failure can mean, and they
//     do not call for the same follow-up at all.
const (
	// sectionSNMPTrap carries the v1/v2c recipients. `SNMPTrapReceiverList`
	// is NOT a section name: it therefore only appears in the BODY, never
	// in a URL — it is this asymmetry that the constant makes visible.
	sectionSNMPTrap = "SNMPTrap"
	// sectionTrapReceiversV3: the section IS the list, without an `SNMP` prefix.
	sectionTrapReceiversV3 = "NotificationReceiverList"
	// notificationType: Trap or Inform. meshcraft only sets TRAPS.
	//
	// The Inform is acknowledged by the receiver, which makes it more
	// reliable on paper — but in v3 it depends on the RECEIVER's SNMP
	// engine, so it requires a `SNMPRemoteEngineIDList` entry and a user
	// with `engineIDType 2`. This point is deduced from the bundle and not
	// verified on hardware: as long as it isn't, using an Inform would mean
	// using something we do not know how to sustain.
	notifyTypeTrap   = "1"
	notifyTypeInform = "2"
	// SNMPRecipient version v1/v2c. The 2 is ALWAYS sent explicitly.
	snmpVersionV1 = "1"
	snmpVersionV2 = "2"
	// filterEnabled: 1 = an OID filter is attached, 2 = none. meshcraft
	// never applies a filter — a filter that hides link events would
	// produce silently incomplete monitoring.
	filterAttached = "1"
	filterNone     = "2"
	// defaultTrapPort: the form default, and the standard port. The
	// accepted range is 1–65535, and the field is an ordinary input: the
	// collector is therefore not obliged to listen on a privileged port.
	defaultTrapPort = 162
)

var (
	reTrapReceiverV2c = regexp.MustCompile(
		`(?is)<SNMPTrapReceiverEntry>(.*?)</SNMPTrapReceiverEntry>`)
	reTrapReceiverV3 = regexp.MustCompile(`(?is)<Receiver>(.*?)</Receiver>`)
)

// TrapReceiver is a trap receiver as the DEVICE renders it.
//
// The codes are not translated, and it is the same choice as for
// SNMPCommunity: it is on them that verification is performed. A
// `SNMPVersion` rendered as 1 where 2 was sent is precisely the difference
// a translation to "SNMPv2c" would make disappear.
type TrapReceiver struct {
	Address    string `json:"address"`
	Port       string `json:"port"`
	NotifyType string `json:"notify_type"`
	// Community: the community that will tag this receiver's v1/v2c traps.
	// It's a SECRET in the same way as the read community: it never goes
	// into the transcript.
	Community   string `json:"-"`
	SNMPVersion string `json:"snmp_version,omitempty"`
	// User / SecurityType: the v3 identity under which the device will sign
	// its traps.
	User         string `json:"user,omitempty"`
	SecurityType string `json:"security_type,omitempty"`
	// Filter / FilterName: OID filtering attached to this recipient.
	Filter     string `json:"filter,omitempty"`
	FilterName string `json:"filter_name,omitempty"`
	// Station is `managementStationIPAddress`. It does NOT exist in the add
	// form: the device generates it, and it is part of the delete key for a
	// v1/v2c recipient. We read it for this reason alone — a deletion
	// constructed without a prior read cannot target its entry.
	Station string `json:"station,omitempty"`
}

// TrapState is what the device carries from the return path.
type TrapState struct {
	// SNMPEnabled: `SNMPEnbl`. SNMP off, nothing comes out — not even a trap.
	SNMPEnabled bool `json:"snmp_enabled"`
	// Emitting / AuthTraps / V1V2Enabled are the three switches that NO
	// interface page writes: the device makes them readable, but none of
	// the three bundles served ever sets them. The connector therefore
	// CHECKS them and does not try to write them (a field that no interface
	// path writes is a field for which we do not know whether writing is
	// accepted).
	//
	// Kept as RAW CODE, and this is deliberate: "" (field missing from the
	// response) does not mean the same thing as "2" (the device says no).
	// A boolean would conflate the two, and would make the write refused on
	// any firmware that does not render the field.
	Emitting    string `json:"traps_enabled"`
	AuthTraps   string `json:"auth_traps_enabled"`
	V1V2Enabled string `json:"v1v2_enabled"`
	// V2c and V3 are TWO distinct tables, in two distinct sections.
	V2c []TrapReceiver `json:"v2c"`
	V3  []TrapReceiver `json:"v3"`
}

// `TrapDestination` and `TrapResult` are now the contract types
// (`driver.TrapDestination`, `driver.TrapResult`), by ALIAS: cf. contract.go.
//
// Two facts about this range, which the common type no longer states but
// which have not disappeared for all that:
//
//   - the SOURCE IP of traps is not a choice. There is no SNMP
//     `source-interface` here, and only one usable IP interface: traps go
//     out from the management address. Moving the management VLAN
//     therefore changes the sender seen by the proxy and silently
//     invalidates its watch list;
//   - the trap community is not necessarily the READ one. The field is
//     free text on the form, which the firmware does not match against any
//     entry in `CommunityList`; requiring it to appear there would be
//     inventing a constraint the device does not have. It is the COLLECTOR
//     that imposes one (cf. driver.TrapDestination.Community).
//
// ONLY ONE COUNTER WHERE THIS PACKAGE HAD TWO: `Known` replaces
// `KnownV2c`/`KnownV3`. "Two distinct tables" is a fact of this range,
// which has no meaning on any other — and these two fields were not read
// by any Go code, only serialized. `Known` therefore carries the size of
// the FAMILY table concerned by the write, and the count from the other
// table goes into `Notes`: it is a fact about the platform, and it must
// not be lost (cf. ApplyTrapReceiver).

// TrapTableFullError: the device refused one more recipient.
//
// This is THE gap in the survey, and it can only be filled this way. No
// ceiling exists on the client side — no validation, no dictionary
// message, no counter — and the firmware only applies its own at the
// moment of adding. The refusal is therefore the only way to find out the
// number of admitted recipients, which makes this error CARRY INFORMATION:
// `Accepted` is the observed ceiling.
//
// It is a type, not a message, so a caller can recognize it (`errors.As`)
// and draw the only useful conclusion: do not try again on this device,
// and record the capability once and for all.
//
// # Why there is NOT yet a "number of recipients" capability
//
// `driver.MonitoringCapabilities` carries a boolean `Traps` and nothing
// more, and this range did not extend it — deliberately. The only number
// that could be written there today is 1, and this 1 is not measured: no
// recipient write has ever been performed on this range, not even the
// first. A capability is a PROMISE made to the operator; this one would
// promise a settable recipient count that we do not yet know how to
// honor. Its absence, on the other hand, leaves the caller with the
// cautious behavior — one collector, the one the proxy declares — which
// is exactly what a 1 would impose on it anyway, without asserting a
// number nobody has observed. The gesture that will inform this
// capability is the one that will measure it: the first real write, then
// this refusal.
type TrapTableFullError struct {
	// Family: "v1/v2c" or "v3" — the two tables are distinct, and nothing
	// says the ceiling is shared.
	Family string
	// Accepted: how many recipients this family already had, so how many
	// the platform at least accepts. Accepted+1 is the first rejected number.
	Accepted int
	// Other: the other table, in case the ceiling is global.
	Other int
	// Silent: the device responded `statusCode 0` and did nothing. This is
	// its characteristic failure mode, and it does not change the conclusion.
	Silent bool
	// Cause: what the device responded, when it responded something.
	Cause error
}

func (e *TrapTableFullError) Error() string {
	var how = "refused"
	if e.Silent {
		how = "responded code 0 then retained nothing (writing without effect), which is " +
			"a refusal"
	}
	var out = fmt.Sprintf("the platform did not accept another trap receiver: it %s "+
		"when it already had %d in %s (and %d in the other table). The number of "+
		"simultaneous receivers admitted by this product line is not documented "+
		"anywhere and the firmware only applies it on add: this refusal is therefore "+
		"the measure itself — %d accepted, %d refused. Do not try again: remove a "+
		"receiver that is no longer needed, or declare only one, and report this "+
		"ceiling in the connector capability",
		how, e.Accepted, e.Family, e.Other, e.Accepted, e.Accepted+1)
	if e.Cause != nil {
		out += " | device response: " + e.Cause.Error()
	}
	return out
}

func (e *TrapTableFullError) Unwrap() error { return e.Cause }

// --- Reading ---

// ReadTrapReceivers rereads the return path: the two recipient tables and
// the emission switches. READ ONLY, replayable on a fleet in production.
//
// The three sections go into ONE request, for the same reason as ReadSNMP:
// this range only accepts five web sessions, and reading section by
// section would amount to ousting the human administrator from their own
// interface.
func (a *Admin) ReadTrapReceivers(ctx context.Context) (TrapState, error) {
	var out = TrapState{V2c: []TrapReceiver{}, V3: []TrapReceiver{}}
	var url = a.wcdURL() + "?{" + sectionSNMPGlobal + "}{" + sectionSNMPTrap +
		"}{" + sectionTrapReceiversV3 + "}"
	var resp, err = a.web.Get(ctx, url)
	if err != nil {
		a.step(false, "reading trap receivers", err.Error())
		return out, err
	}

	var global = sectionOf(resp.Body, sectionSNMPGlobal)
	out.SNMPEnabled = xmlField(global, "SNMPEnbl") == snmpEnabledCode
	out.Emitting = xmlField(global, "trapsEnbl")
	out.AuthTraps = xmlField(global, "authTrapsEnbl")

	// TRAP section isolation: `sectionOf` matches exactly `<SNMPTrap>` and
	// `</SNMPTrap>`, which cannot catch `<SNMPTrapReceiverList>` (you need a
	// `>` or a space right after the name). Without this precision, the
	// child list would be mistaken for the section, and the rest of the
	// document along with it.
	var v1v2 = sectionOf(resp.Body, sectionSNMPTrap)
	out.V1V2Enabled = xmlField(v1v2, "enabled")

	var block string
	for _, block = range reTrapReceiverV2c.FindAllString(v1v2, -1) {
		out.V2c = append(out.V2c, TrapReceiver{
			Address:     xmlField(block, "receiverIPAddress"),
			Port:        xmlField(block, "UDPPortNo"),
			NotifyType:  xmlField(block, "notificationType"),
			Community:   xmlField(block, "communityString"),
			SNMPVersion: xmlField(block, "SNMPVersion"),
			Filter:      xmlField(block, "filterEnabled"),
			FilterName:  xmlField(block, "filterName"),
			Station:     xmlField(block, "managementStationIPAddress"),
		})
	}
	for _, block = range reTrapReceiverV3.FindAllString(
		sectionOf(resp.Body, sectionTrapReceiversV3), -1) {
		out.V3 = append(out.V3, TrapReceiver{
			Address:      xmlField(block, "receiverIPAddress"),
			Port:         xmlField(block, "UDPPortNo"),
			NotifyType:   xmlField(block, "notificationType"),
			User:         xmlField(block, "userName"),
			SecurityType: xmlField(block, "securityType"),
			Filter:       xmlField(block, "filterEnabled"),
			FilterName:   xmlField(block, "filterName"),
		})
	}

	// The transcript carries COUNTS and addresses, never the trap
	// community: it exposes the device to SNMP the same way the read one
	// does.
	a.step(true, "trap receivers read", fmt.Sprintf(
		"%d in v1/v2c, %d in v3 — emission %s, section v1/v2c %s",
		len(out.V2c), len(out.V3), switchLabel(out.Emitting),
		switchLabel(out.V1V2Enabled)))
	return out, nil
}

// switchLabel makes a THREE-state switch readable. The third is the only
// interesting one: a field absent from the response is not the same as a
// "2" field, and confusing them would either wrongly refuse, or silently
// pass over, a device that will not emit anything.
func switchLabel(code string) string {
	switch code {
	case snmpEnabledCode:
		return "activated"
	case snmpDisabledCode:
		return "DISABLED"
	case "":
		return "not declared by the equipment"
	}
	return "code " + code
}

// receiversAt rend les destinataires d'une table qui portent cette adresse.
//
// The COUNT counts as much as the entry: two entries for the same address
// means that the equipment was ADDED where replacement was requested — and
// this is a case that cannot be excluded, the key of an entry not being the
// seule adresse.
func receiversAt(list []TrapReceiver, addr string) []TrapReceiver {
	var out []TrapReceiver
	var r TrapReceiver
	for _, r = range list {
		if r.Address == addr {
			out = append(out, r)
		}
	}
	return out
}

// --- Writing ---

// ApplyTrapReceiver declares the meshcraft collector as the recipient of
// equipment traps. This is the gesture that puts the channel into service.
//
// The order is that imposed by the statement:
//
//  1. Validate EVERYTHING before the first write, including the existence of
//     v3 user and their group's notification view. The equipment,
//     doesn't check any of that: it accepts a non-existent user without
//     flinch and then emits nothing authenticable.
//  2. READ. Without reading, one can neither be idempotent nor construct a
//     deletion (the key of which is returned by the equipment), nor interpret a
//     refusal — knowing how many recipients were already there is what distinguishes
//     “table full” of “wrong writing gesture”.
//  3. Write, once, in the table of the good family.
//  4. RELIRE et comparer champ par champ. Sur cette gamme, `statusCode 0` ne veut
//     not say "fact" - and here the writing itself is a reconstruction
//     never executed: rereading is not a precaution, it is the only
//     preuve qui existe.
//  5. Save in flash. A recipient lost on reboot would leave a
//     equipment that passes for supervised.
//
// Which is NOT done, voluntarily: no third party recipient is removed,
// even to make room. A full table is reported, never “released” —
// couper la supervision de quelqu'un d'autre pour installer la sienne serait le
// Worst service. And the three transmit switches are not written:
// no page in the interface writes them, so nothing says they accept it.
func (a *Admin) ApplyTrapReceiver(ctx context.Context,
	want TrapDestination) (TrapResult, error) {
	var out TrapResult
	var err error

	// 1. Validation. Rien ne part tant que tout n'est pas nommable.
	if (want.Community == "") == (want.User == "") {
		return out, fmt.Errorf("a recipient of traps carries ONE identity: either one" +
			"v2c community, or a v3 user — the two tables are separate" +
			"on this range, and declaring both would cause each event to emit in" +
			"double to the same collector")
	}
	var v3 = want.User != ""
	out.Version = "v2c"
	if v3 {
		out.Version = "v3"
	}
	if err = checkTrapAddress(want.Address); err != nil {
		return out, err
	}
	if want.Port == 0 {
		want.Port = defaultTrapPort
	}
	if want.Port < 1 || want.Port > 65535 {
		return out, fmt.Errorf("destination UDP port %d out of range: this product line "+
			"validates UDPPortNo on 1–65535", want.Port)
	}
	out.Address, out.Port = want.Address, want.Port
	var port = strconv.Itoa(want.Port)
	if v3 {
		if err = checkName("SNMPv3 user name", want.User, userMaxLen); err != nil {
			return out, err
		}
	} else if err = checkName("SNMPv2c trap community", want.Community,
		communityMaxLen); err != nil {
		return out, err
	}

	// 2. Reading. We never differ against a cached state.
	var st TrapState
	if st, err = a.ReadTrapReceivers(ctx); err != nil {
		return out, err
	}
	// `Known` carries the count of the table of the TARGET family, the one in which
	// the writing is going to leave: this is the one whose refusal would measure the ceiling.
	//
	// L'autre table part en NOTE, et il ne faut pas y voir de la statistique. Cette
	// gamme range ses destinataires v1/v2c et v3 dans deux sections distinctes, ce
	// that no other delivered platform does; nothing says that their ceiling
	// is not COMMON, and the firmware only applies it when adding. The day when an addition
	// is refused, the only thing that will allow us to conclude is to know what the
	// DEUX tables portaient (cf. TrapTableFullError.Other). Le contrat n'a qu'un
	// compteur — parce que « deux tables » n'a de sens que ici — donc ce fait de
	// platform goes through the field provided for that: a sentence for the operator.
	out.Known = len(st.V2c)
	var otherFamily, otherCount = "v3", len(st.V3)
	if v3 {
		out.Known = len(st.V3)
		otherFamily, otherCount = "v1/v2c", len(st.V2c)
	}
	out.Notes = append(out.Notes, fmt.Sprintf("this product line keeps TWO receiver "+
		"tables: this one had %d, the other (%s) has %d — this set does not touch "+
		"it, and nothing says the two ceilings are not shared",
		out.Known, otherFamily, otherCount))
	if !st.SNMPEnabled {
		// Placing a receiver on a device whose SNMP is off would give an
		// exact configuration and total silence. ApplySNMP is the gesture that
		// opens SNMP; this path must not do it in passing.
		return out, fmt.Errorf("SNMP is disabled on the device (%s = %s): a "+
			"trap receiver would be of no use — set the SNMP identity "+
			"first (ApplySNMP)", sectionSNMPGlobal, snmpDisabledCode)
	}
	// Emission switches: refuse on a “2” (the device says no),
	// but only NOTE on an absent field. Refusing on absence would cut the
	// trap channel on any firmware that does not render the field, when
	// nothing was observed.
	if st.Emitting == snmpDisabledCode {
		return out, fmt.Errorf("the device carries trapsEnbl = %s: it emits NO "+
			"trap, and declaring a receiver would change nothing. No UI page "+
			"writes this field — meshcraft therefore does not set it, and refuses "+
			"rather than announcing a channel that will stay silent", snmpDisabledCode)
	}
	if !v3 && st.V1V2Enabled == snmpDisabledCode {
		return out, fmt.Errorf("the device carries %s/enabled = %s: the v1/v2c "+
			"receiver table is disabled, a receiver would be inert — "+
			"aim for v3, or restore this switch on the device",
			sectionSNMPTrap, snmpDisabledCode)
	}
	if st.Emitting == "" {
		out.Notes = append(out.Notes, "the equipment did not render trapsEnbl:"+
			"the emission of traps could not be verified")
	}

	// Identity v3 checks AGAINST the device, because it doesn't.
	var securityType string
	if v3 {
		if securityType, err = a.trapUserSecurity(ctx, want.User); err != nil {
			return out, err
		}
	}

	// 3. Idempotence. A recipient already there and compliant cannot be rewritten:
	//    returning to a qualified park must not disturb anything, and above all not
	//    empiler un doublon par provisionnement.
	var list = st.V2c
	if v3 {
		list = st.V3
	}
	var here = receiversAt(list, want.Address)
	if len(here) == 1 && trapConform(here[0], want, port, securityType, v3) {
		a.step(true, "recipient of traps already declared and compliant",
			fmt.Sprintf("%s to %s:%s — no writing required",
				out.Version, want.Address, port))
		return out, nil
	}

	// In v3, the username is part of the KEY of the entry
	// (receiverIPAddress + notificationType + userName + securityType) : un `set`
	// with another name would add an entry instead of replacing the existing one.
	// there, and the old one — inert, since its user is not suitable —
	// continuerait d'occuper une place dans une table dont le plafond est inconnu.
	var old TrapReceiver
	if v3 {
		for _, old = range here {
			if old.User == want.User {
				continue
			}
			if err = a.writeSection(ctx, sectionTrapReceiversV3, "delete",
				deleteReceiverV3(old),
				"old recipient of traps v3 removed before replacement"); err != nil {
				return out, err
			}
			out.Replaced = true
		}
	}

	// 4. Writing, only one, in the table of the good family.
	var section = sectionSNMPTrap
	var inner = receiverEntryV2c(want, port)
	if v3 {
		section = sectionTrapReceiversV3
		inner = receiverEntryV3(want, port, securityType)
	}
	if err = a.writeSection(ctx, section, "set", inner, fmt.Sprintf(
		"recipient of %s traps declared to %s:%s", out.Version, want.Address,
		port)); err != nil {
		return out, trapRefused(err, st, v3, false)
	}

	// 5. La relecture, qui est la seule preuve.
	if err = a.verifyTrapReceiver(ctx, want, port, securityType, v3, st); err != nil {
		return out, err
	}
	out.Set = true

	// 6. Make it sustainable. Failing to save does not cancel the pose — it is
	//    recorded in the transcript, which is the channel provided for this.
	a.SaveConfig(ctx) //nolint:errcheck // logged in the transcript
	return out, nil
}

// trapUserSecurity verifies that the designated v3 user EXISTS and can receive
// traps, and makes the security level to be declared for him.
//
// This additional reading is the price of the documented trap: the field
// `userName` du formulaire de destinataire v3 est un texte libre, sans liste
// drop-down, and the equipment does not oppose a non-existent name. The recipient
// registers, and does not emit anything authenticable. Nobody would notice
// other than by noting the absence of events — that is to say, never.
//
// The security level is DEDUCED from the user, never chosen: it is the same
// pair of algorithms which decides its key. A recipient declared in authPriv
// for a user without encryption would designate an identity that does not exist.
func (a *Admin) trapUserSecurity(ctx context.Context, user string) (string, error) {
	var ident, err = a.ReadSNMP(ctx)
	if err != nil {
		return "", err
	}
	var u, seen = ident.User(user)
	if !seen {
		return "", fmt.Errorf("no SNMPv3 user %q on device: the "+
			"userName field of a trap receiver is free text that "+
			"firmware does not check, the receiver would register without error and "+
			"would emit nothing authenticable — set the user first "+
			"(ApplySNMP)", user)
	}
	if u.Auth == authCodeNone {
		return "", fmt.Errorf("SNMPv3 user %q has no authentication "+
			"(authenticationMethod %s): traps signed by it would prove nothing "+
			"about their origin", user, authCodeNone)
	}
	var level = securityLevelAuthNoPriv
	if u.Priv != privCodeNone {
		level = securityLevelAuthPriv
	}
	// The group's NOTIFICATION view, not the read view: that is what
	// authorizes the device to send a trap to users in the group. Without
	// it, everything looks in place and nothing leaves. `groupEntry` sets it; a
	// user attached to a group created elsewhere may well lack it.
	var g, hasGroup = ident.Group(u.Group)
	if !hasGroup {
		return "", fmt.Errorf("SNMPv3 user %q claims to be in group %q, "+
			"which %s does not contain: impossible to verify that it can receive "+
			"traps", user, u.Group, sectionSNMPGroups)
	}
	if g.NotifyView == "" {
		return "", fmt.Errorf("SNMPv3 group %q of user %q carries "+
			"no notification view (notifyView): the device will not be able to "+
			"address any trap, and nothing in its UI would show it — "+
			"re-run ApplySNMP, which sets this group with its notification view",
			u.Group, user)
	}
	a.step(true, "SNMPv3 user "+user+" checked for trap emission",
		"security level "+level+", group "+u.Group+" with notification view")
	return level, nil
}

// trapConform says if a recipient already declared is THE ONE we want.
//
// The address is already matched by the caller. What is compared here is everything
// on which the exploitability of the traps received depends — the port, the fact that it is a
// Trap and not an Inform, and identity.
func trapConform(have TrapReceiver, want TrapDestination,
	port, securityType string, v3 bool) bool {
	if have.Port != port || have.NotifyType != notifyTypeTrap {
		return false
	}
	// An attached filter discards traps BY OID, silently:
	// link events might never happen without anything saying so.
	// meshcraft does not interpret its content — it only accepts the absence of a filter.
	//
	// The test concerns `filterAttached` and not “different from filterNone”:
	// un firmware qui ne rendrait pas `filterEnabled` rendrait sinon ce
	// recipient eternally non-compliant, therefore rewritten each time.
	if have.Filter == filterAttached || have.FilterName != "" {
		return false
	}
	if v3 {
		return have.User == want.User && have.SecurityType == securityType
	}
	return have.Community == want.Community && have.SNMPVersion == snmpVersionV2
}

// verifyTrapReceiver rereads the device and compares field by field.
//
// C'est la fonction la plus importante de ce bloc. Sur cette gamme, « code 0 mais
// nothing has changed” is a proven failure mode; and the gesture of writing
// recipients is a reconstruction that no one has yet performed. Each
// deviation therefore becomes an error which NAMES the faulty field — this is what will make
// readable the failure of the day when the hypothesis will prove false.
func (a *Admin) verifyTrapReceiver(ctx context.Context, want TrapDestination,
	port, securityType string, v3 bool, before TrapState) error {
	var after, err = a.ReadTrapReceivers(ctx)
	if err != nil {
		return err
	}
	var list, fam, section = after.V2c, "v1/v2c", sectionSNMPTrap
	if v3 {
		list, fam, section = after.V3, "v3", sectionTrapReceiversV3
	}
	var here = receiversAt(list, want.Address)
	if len(here) == 0 {
		// The equipment said yes and did nothing. The number of recipients it
		// was already deciding what that means.
		return trapRefused(nil, before, v3, true)
	}
	if len(here) > 1 {
		return fmt.Errorf("%s holds %d trap receivers for %s: the `set` "+
			"ADDED an entry instead of replacing the one that was there. The table is "+
			"therefore indexed differently than assumed — duplicates must be removed "+
			"by hand before trying again, or a table whose ceiling is unknown "+
			"will saturate", section, len(here), want.Address)
	}
	var got = here[0]
	if got.Port != port {
		return fmt.Errorf("recipient %s was registered on UDP port %q "+
			"instead of %q: the traps would go where the collector is not listening",
			fam, got.Port, port)
	}
	if got.NotifyType != notifyTypeTrap {
		return fmt.Errorf("the %s recipient was registered with notificationType %q "+
			"instead of %q (Trap): an Inform expects an acknowledgement, and in v3 "+
			"drifts against the receiver's SNMP engine — what meshcraft does not "+
			"know how to set", fam, got.NotifyType, notifyTypeTrap)
	}
	if got.Filter == filterAttached || got.FilterName != "" {
		return fmt.Errorf("an OID filter (%q) is attached to receiver %s: "+
			"traps would be silently dismissed, including link ones — "+
			"meshcraft does not declare a receiver whose filtering it does not "+
			"control", got.FilterName, fam)
	}
	if v3 {
		// The user AND the level: together they designate the key
		// the device will use to sign.
		if got.User != want.User {
			return fmt.Errorf("the v3 recipient was registered for user "+
				"%q instead of %q: its traps would not be authenticable "+
				"by the collector", got.User, want.User)
		}
		if got.SecurityType != securityType {
			return fmt.Errorf("the v3 recipient was registered with securityType %q "+
				"instead of %q: the level no longer matches the algorithm pair "+
				"of user %q", got.SecurityType, securityType, want.User)
		}
	} else {
		// THE observation trap: the form on this product line defaults to SNMPv1.
		if got.SNMPVersion != snmpVersionV2 {
			return fmt.Errorf("the recipient was registered with SNMPVersion %q "+
				"instead of %q: the form default on this product line is %s (SNMPv1), and "+
				"v1 traps carry neither the sequence counter nor the event types "+
				"the collector expects", got.SNMPVersion, snmpVersionV2,
				snmpVersionV1)
		}
		// The community is compared, but not named: it is a secret.
		if got.Community != want.Community {
			return fmt.Errorf("the v1/v2c recipient was registered with a " +
				"community that is not the one requested: the collector would reject " +
				"its traps as unrecognized (value not logged: it is a secret)")
		}
	}
	a.step(true, "trap recipient verified by replay", fmt.Sprintf(
		"the equipment carries a trap %s to %s:%s", fam, want.Address, port))
	return nil
}

// trapRefused traduit un refus d'ajout de destinataire — explicite (`statusCode`
// non nul) ou silencieux (code 0 suivi d'une relecture vide).
//
// Toute la valeur de cette fonction est dans la distinction qu'elle fait, et elle
// depends on the number of recipients ALREADY present:
//
//   - there was at least one, so the writing gesture works: this refusal
//     measures the table ceiling, which is not documented anywhere
//     (TrapTableFullError) ;
//   - il n'y en avait aucun, donc ce n'est pas un plafond : c'est le geste
//     of writing itself — reconstituted, never executed — that the equipment
//     do not accept. Saying it explicitly saves the next batch from searching
//     a full table on empty equipment.
func trapRefused(cause error, before TrapState, v3, silent bool) error {
	var accepted, other, fam = len(before.V2c), len(before.V3), "v1/v2c"
	var section = sectionSNMPTrap
	if v3 {
		accepted, other, fam = len(before.V3), len(before.V2c), "v3"
		section = sectionTrapReceiversV3
	}
	if accepted > 0 {
		return &TrapTableFullError{
			Family: fam, Accepted: accepted, Other: other, Silent: silent, Cause: cause,
		}
	}
	var how = "refused"
	var conclusion = ""
	if silent {
		how = "answered code 0 but replay shows no recipient " +
			"(writing WITHOUT EFFECT)"
		conclusion = " A code 0 with no effect is a proven failure mode of this " +
			"interface: it is the request shape that is at fault, not a " +
			"missing right."
	}
	var out = fmt.Sprintf("the device %s the FIRST trap receiver in the "+
		"%s table (%s), which was empty: this is therefore not a table ceiling. The "+
		"writing gesture of this section is a reconstruction never performed "+
		"(docs/01-Architecture/Connecteurs.md, “The writing gesture — HYPOTHESIS to "+
		"validate”) — that is what this refusal challenges: section name, "+
		"child-list nesting, or POST query string.%s",
		how, fam, section, conclusion)
	if cause != nil {
		out += " | device response: " + cause.Error()
	}
	return fmt.Errorf("%s", out)
}

// checkTrapAddress checks the trap address. The scope of the equipment is
// validated in `unicastipv4` on this family: a broadcast address,
// loopback or local link would be refused - we might as well say it here, where the message
// peut nommer la raison.
func checkTrapAddress(addr string) error {
	if addr == "" {
		return fmt.Errorf("trap collector address is empty")
	}
	var ip = net.ParseIP(addr)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("trap collector address %q: this product line "+
			"only accepts unicast IPv4 (field validated in `unicastipv4`)", addr)
	}
	if !ip.IsGlobalUnicast() {
		return fmt.Errorf("trap collector address %q: neither loopback nor "+
			"link-local, nor multicast — the equipment emits its traps from its"+
			"interface de management, vers un collecteur joignable depuis elle", addr)
	}
	return nil
}

// receiverEntryV2c returns a v1/v2c recipient in the reconstructed form of the
// statement (`addTrapReceiversV1V2Submit` composed with `tmpl.json2xml`):
//
//	SNMPTrap.set.SNMPTrapReceiverList.SNMPTrapReceiverEntry = {receiverIPAddress,
//	  UDPPortNo, notificationType, timeout, retries, communityString,
//	  SNMPVersion, filterEnabled, filterName}
//
// Three fields deserve their line:
//
//   - `SNMPVersion` is EXPLICITLY 2. The form default is 1 (SNMPv1):
//     omitting the field or recopying this default would declare a v1 recipient, and the
//     collecteur v2c ne verrait jamais rien d'exploitable ;
//   - `timeout` et `retries` partent VIDES, parce que `notificationType` vaut Trap.
//     This is what the equipment interface does, which dumps them before sending;
//     sending them information on a Trap is an unobserved case;
//   - `filterEnabled` vaut 2 (aucun filtre) et `filterName` reste vide : on ne
//     I definitely don't want an OID filter that would exclude link events.
//
// La liste `SNMPTrapReceiverList` est un ENFANT, jamais une section : elle
// only appears in this body, and never in the URL.
func receiverEntryV2c(want TrapDestination, port string) string {
	return "<SNMPTrapReceiverList><SNMPTrapReceiverEntry>" +
		"<receiverIPAddress>" + xmlEscape(want.Address) + "</receiverIPAddress>" +
		"<UDPPortNo>" + port + "</UDPPortNo>" +
		"<notificationType>" + notifyTypeTrap + "</notificationType>" +
		"<timeout></timeout>" +
		"<retries></retries>" +
		"<communityString>" + xmlEscape(want.Community) + "</communityString>" +
		"<SNMPVersion>" + snmpVersionV2 + "</SNMPVersion>" +
		"<filterEnabled>" + filterNone + "</filterEnabled>" +
		"<filterName></filterName>" +
		"</SNMPTrapReceiverEntry></SNMPTrapReceiverList>"
}

// receiverEntryV3 rend un destinataire v3 (`addTrapReceiversV3Submit`) :
//
//	NotificationReceiverList.set.Receiver = {receiverIPAddress, UDPPortNo,
//	  notificationType, timeout, retries, securityType, userName, filterEnabled,
//	  filterName}
//
// Pas de `communityString` ici, et pas de `SNMPVersion` : c'est le couple
// (userName, securityType) which carries the identity. `securityType` resumes
// the enumeration that `snmp.go` already knows for groups — 1 noAuthNoPriv,
// 2 authNoPriv, 3 authPriv — and it is INFERRED from the user, never chosen.
//
// Empty `timeout`/`retries` for the same reason as in v2c: it's a Trap.
func receiverEntryV3(want TrapDestination, port, securityType string) string {
	return "<Receiver>" +
		"<receiverIPAddress>" + xmlEscape(want.Address) + "</receiverIPAddress>" +
		"<UDPPortNo>" + port + "</UDPPortNo>" +
		"<notificationType>" + notifyTypeTrap + "</notificationType>" +
		"<timeout></timeout>" +
		"<retries></retries>" +
		"<securityType>" + securityType + "</securityType>" +
		"<userName>" + xmlEscape(want.User) + "</userName>" +
		"<filterEnabled>" + filterNone + "</filterEnabled>" +
		"<filterName></filterName>" +
		"</Receiver>"
}

// deleteReceiverV3 recalls the KEY of a v3 recipient, not the entire entry:
// `receiverIPAddress` + `notificationType` + `userName` + `securityType`. Les
// values ​​come from READING, never from what we wanted to pose — a key
// reconstructed from memory would designate no entry, and the deletion
// would succeed without deleting anything.
func deleteReceiverV3(r TrapReceiver) string {
	return "<Receiver>" +
		"<receiverIPAddress>" + xmlEscape(r.Address) + "</receiverIPAddress>" +
		"<notificationType>" + xmlEscape(r.NotifyType) + "</notificationType>" +
		"<userName>" + xmlEscape(r.User) + "</userName>" +
		"<securityType>" + xmlEscape(r.SecurityType) + "</securityType>" +
		"</Receiver>"
}
