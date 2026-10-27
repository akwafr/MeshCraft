package swos

import (
	"context"
	"fmt"
)

// The SwOS SNMP agent: what the platform actually exposes, and what it does
// not.
//
// /snmp.b fits in four fields, and that is ALL the SNMP SwOS has:
//
//	{en:0x01, com:'<community, hex>', ci:'<contact>', loc:'<location>'}
//
// What this page ESTABLISHES, and which is solid:
//
//   - an agent exists and it is enabled on the reference device;
//   - it has ONLY ONE community, at most 64 bytes (`ga:64` in the UI);
//   - there is NO SNMPv3 on this platform. No user, no authentication
//     algorithm, no encryption — no field, in any tab, nor in the device's
//     complete configuration backup. It is not a hidden page as on the
//     Instant On range: the entire web UI of a SwOS fits in one file, and it
//     does not know v3;
//   - there is NO trap receiver, hence nothing to push.
//
// What this page does NOT establish, and never will: which protocol version
// the agent answers. There is no version selector, and a community does not
// say whether the agent serves v1, v2c or both. This page therefore long
// justified an EMPTY `SNMPVersions` — not out of caution on principle, but
// because those lists decide the secret actually pushed and recorded in the
// database, and a credential present in the database and nowhere else is
// worse than an absent one: the UI shows it.
//
// The answer did not come from the page, it came from the network. The walk
// of 29 July 2026 on 192.0.2.36 obtained `sysName.0` = `lan-m6` (the `id` field
// of /sys.b) and `ifNumber.0` = 26 (the `prt:0x1a` of /link.b): an agent
// answering for other hardware would return neither that name nor that port
// count. `Capabilities().SNMPVersions` is now `["v2c"]` (cf. swos.go). v1 is not
// advertised — probable, never seen answering — and v3 remains the absence
// established above.
//
// The code that READS and WRITES the community is here, verified on the
// page's real capture. It had been written before it had a use, on the
// argument that one day the version list would gain a line; that day has
// passed, and the use exists. What is not claimed here: that a write has been
// exercised on the reference hardware. It has not.

// SNMPConfig is what the device carries as SNMP configuration.
type SNMPConfig struct {
	// Enabled: does the agent answer.
	Enabled bool `json:"enabled"`
	// Community: the community, ONLY ONE. For reading as for writing — SwOS
	// does not distinguish read from write, there is only one string.
	Community string `json:"community"`
	Contact   string `json:"contact,omitempty"`
	Location  string `json:"location,omitempty"`
}

// maxSNMPTextLen bounds the text fields of /snmp.b: 64 bytes, the bound the
// device's UI imposes on itself (`ga:64`). It REFUSES beyond that, without a
// message.
const maxSNMPTextLen = 64

// ReadSNMP reads the SNMP configuration. READ ONLY.
func (a *Admin) ReadSNMP(ctx context.Context) (SNMPConfig, error) {
	var out SNMPConfig
	var page, err = a.Get(ctx, PageSNMP)
	if err != nil {
		return out, err
	}
	return snmpFrom(page)
}

// snmpFrom translates the page. A pure function, hence verifiable on a
// capture.
//
// A missing field is not an empty string: /snmp.b having only four fields,
// missing one means we are not reading the page we think — and a write that
// followed would send back an empty community, silently cutting monitoring.
func snmpFrom(page *Page) (SNMPConfig, error) {
	var out SNMPConfig
	var en, ok = page.Num(FieldSNMPEnabled)
	if !ok {
		return out, fmt.Errorf("/snmp.b does not carry the \"%s\" field", FieldSNMPEnabled)
	}
	out.Enabled = en != 0

	out.Community, ok = page.Text(FieldSNMPCommunity)
	if !ok {
		return out, fmt.Errorf("/snmp.b does not carry the \"%s\" field", FieldSNMPCommunity)
	}
	// Contact and location are present on the reference firmware, but they
	// carry no consequence: their absence does not justify refusing the read.
	out.Contact, _ = page.Text(FieldSNMPContact)
	out.Location, _ = page.Text(FieldSNMPLocation)
	return out, nil
}

// SetCommunity sets the SNMP community.
//
// NOT VERIFIED ON HARDWARE: no write could be carried out from the
// development host (cf. the box in apply.go). The produced body is verified on
// the page's real capture.
//
// READ-MODIFY-REWRITE, like every write on this platform: the page goes out
// with its four fields, contact and location included. Sending back only the
// community would leave the device free to erase the other two.
//
// The agent is enabled in passing. Setting a community on an agent that is
// off would give configured and mute monitoring, which is the worst of both
// worlds: nothing answers, and nothing says why.
func (a *Admin) SetCommunity(ctx context.Context, community string) error {
	if community == "" {
		return fmt.Errorf("empty community refused: it would make the SNMP agent " +
			"unusable without disabling it, hence mute with nothing saying so")
	}
	if len(community) > maxSNMPTextLen {
		return fmt.Errorf("community of %d bytes: the device accepts at most %d, "+
			"and refuses beyond that without a message", len(community), maxSNMPTextLen)
	}
	var page, err = a.Get(ctx, PageSNMP)
	if err != nil {
		return err
	}
	page.SetText(FieldSNMPCommunity, community)
	page.SetNum(FieldSNMPEnabled, 1)

	var fields, ok = WritableFields(PageSNMP)
	if !ok {
		return fmt.Errorf("writable field set of %s unknown: write refused",
			PageSNMP)
	}
	var body *Page
	var missing []string
	body, missing = page.Only(fields)
	if len(missing) > 0 {
		var e = fmt.Errorf("/snmp.b: this firmware does not serve the fields %v expected by "+
			"the connector — write refused, sending back the page without them would let "+
			"the device erase them", missing)
		a.step(false, "unexpected /snmp.b field set", e.Error())
		return e
	}
	// The community does NOT go into the transcript: it is a secret, and a
	// transcript is made to be read and kept.
	return a.postRawSecret(ctx, PageSNMP, body.Encode(),
		"SNMP community applied and agent enabled")
}
