package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// writeTools: gestures that change stored intent, or push it onto hardware.
//
// None of these is irreversible — the previous intent can be written back, and a
// port that was applied can be applied again. The gestures that CANNOT be undone
// live in tools_danger.go, behind confirmation, and are not offered in this mode.
func writeTools() []Tool {
	return []Tool{
		{
			Name:       "set_port_intent",
			Title:      "Record what ports should carry",
			Effect:     EffectWrite,
			Idempotent: true,
			Min:        ModeWrite,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device's id.", Req: true},
				{Name: "ports", Type: "object", Desc: `Map of port name to its intent, e.g. {"1": {"mode":"access","pvid":20}, "2": {"mode":"trunk","tagged":[10,20]}}. Port names must be the standard faceplate names this device reports — read them from get_device.`, Req: true},
			},
			Description: `Record the intent for one or more ports: what VLANs they carry, access or trunk, PVID, PoE.

This writes INTENT, not hardware. Reconciliation then compiles it, reads the device, diffs, applies and READS BACK — nothing is considered applied on the strength of a return code. Automatic apply coalesces changes over about six seconds and runs one apply at a time per device.

Rules that decide what your map means:
  - Absent means unmanaged. A port you do not mention is left alone. You cannot clear a port by omitting it.
  - A trunk's VLAN list is a FLOOR, not an exact set: the device may carry more without that being a drift.
  - A trunk with no explicit native VLAN gets VLAN 1 as native; an access port with no PVID gets none.
  - PoE is recorded here but NO CONNECTOR PUSHES IT from the intent. To change power on a port, use apply_port_feature with feature="poe".

Prefer apply_switch_profile when the change is "every SFP port should be a trunk" — a rule set survives a model change, an enumerated port map does not.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var ports map[string]any
				ports, err = a.Map("ports")
				if err != nil {
					return "", err
				}
				if len(ports) == 0 {
					return "", errors.New("ports is required and must name at least one port")
				}
				return send(ctx, c, http.MethodPut,
					"/api/devices/"+esc(deviceID)+"/ports",
					map[string]any{"ports": ports})
			},
		},
		{
			Name:       "apply_switch_profile",
			Title:      "Evaluate a rule set against a faceplate, and apply it",
			Effect:     EffectWrite,
			Idempotent: true,
			Min:        ModeWrite,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device whose faceplate the rules are evaluated against.", Req: true},
				{Name: "profile_id", Type: "string", Desc: `A saved switch profile to evaluate, from list_profiles(kind="switch"). Give this or spec.`},
				{Name: "spec", Type: "object", Desc: `A rule set that is not saved, in the shape a switch profile's own spec takes. Give this or profile_id.`},
				{Name: "dry_run", Type: "boolean", Desc: "Default TRUE: evaluate and report, write nothing. Set false to apply."},
			},
			Description: `Evaluate a switch profile's rules against this device's front panel and report, per rule, which sockets it matched and which it won — then optionally write the result into the port intent.

DEFAULT IS A PREVIEW. dry_run is true unless you set it false, because the preview is the only way to see a masked rule before it has already been applied.

Read the report with the resolution rules in mind: first matching rule wins, so a catch-all written first masks everything after it; a slice (first N, last N, range) counts on RANK, not on port number; one socket takes exactly one rule's configuration with no per-field merging; selectors are evaluated against the whole faceplate, not against what earlier rules left over. This is why "matched" and "won" are reported separately — a rule matching 30 sockets and winning 0 is masked by an earlier rule, not broken.

Applying writes the port INTENT only. Hardware follows through normal reconciliation, and sockets no rule covers are left untouched.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var body = map[string]any{}
				var profileID string
				profileID, err = a.String("profile_id")
				if err != nil {
					return "", err
				}
				var spec map[string]any
				spec, err = a.Map("spec")
				if err != nil {
					return "", err
				}
				if profileID == "" && spec == nil {
					return "", errors.New("give either profile_id (a saved switch profile) " +
						"or spec (a rule set that is not saved)")
				}
				if profileID != "" {
					body["profile_id"] = profileID
				}
				if spec != nil {
					body["spec"] = spec
				}
				// Preview unless explicitly told otherwise, and the asymmetry is the
				// point: a forgotten flag must produce the harmless gesture.
				var dry = true
				var _, present = a["dry_run"]
				if present {
					dry = a.Bool("dry_run")
				}
				var verb = "/switch-profile/preview"
				if !dry {
					verb = "/switch-profile/apply"
				}
				var out, sendErr = send(ctx, c, http.MethodPost,
					"/api/devices/"+esc(deviceID)+verb, body)
				if sendErr != nil {
					return "", sendErr
				}
				if dry {
					return "PREVIEW ONLY — nothing was written. Call again with dry_run=false to apply.\n\n" + out, nil
				}
				return out, nil
			},
		},
		{
			Name:       "apply_port_feature",
			Title:      "Push a per-port feature onto the device now",
			Effect:     EffectWrite,
			Idempotent: false,
			Min:        ModeWrite,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device's id.", Req: true},
				{Name: "feature", Type: "string", Desc: "Which per-port feature to write.", Enum: []string{"poe", "dhcp_snooping_trust", "storm_control"}, Req: true},
				{Name: "ports", Type: "object", Desc: "Map of port name to the value for this feature. See the description for the shape each feature takes.", Req: true},
			},
			Description: `Write a per-port feature straight onto the switch. This reaches hardware immediately; it is not a stored intent that reconciliation will carry.

The shape of "ports" depends on the feature, and each has a trap:

feature="poe" — map port to power class, e.g. {"5":"54v","6":"none"}.
  Classes: "none" (administratively off, no voltage), "24v" (PASSIVE 24 V — nothing negotiates first, and it can damage a device that expected standard PoE), "54v" (standard 802.3af/at negotiation).
  NOT ALL PORTS ARE CAPABLE, and this is read from the device rather than guessed from the model: a PoE switch is not a uniform slab of identical jacks. Check the per-port "capable" flag from get_device before writing, because some sockets refuse every mode but off.

feature="dhcp_snooping_trust" — map port to a boolean, e.g. {"24":true}.
  THE BOOLEAN IS TRUST, NOT "SNOOPING HERE", and conflating the two is the classic error. Snooping is enabled device-wide and per VLAN; a port is then trusted or untrusted. UNTRUSTED IS WHERE THE PROTECTION LIVES — DHCP server replies arriving there are dropped — and TRUSTED IS THE EXCEPTION, for the uplink toward the real DHCP server or toward another switch. Trusting a port you did not mean to trust removes protection rather than adding it.
  The connector also turns the device-wide feature and the site's VLANs on as part of the same gesture, because a trust flag on a device that inspects nothing protects nothing. It says so in the result notes, and it never turns the feature off.

feature="storm_control" — map port to a LIST of limits, one per traffic class, each {"class":"broadcast"|"multicast"|"unknown-unicast", "enabled":true, "rate":10, "unit":"percent"}. Read read_live_storm_control first to see the units this platform speaks.
  EVERY RATE MUST CARRY ITS UNIT and no connector converts between units. A bare number is a tenth of a gigabit on one platform and ten packets a second on the next. If the platform does not accept the unit you send, it refuses and names the unit it does take — send that, do not convert the number yourself.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var feature string
				feature, err = a.Required("feature")
				if err != nil {
					return "", err
				}
				var ports map[string]any
				ports, err = a.Map("ports")
				if err != nil {
					return "", err
				}
				if len(ports) == 0 {
					return "", errors.New("ports is required and must name at least one port")
				}
				var path string
				switch feature {
				case "poe":
					path = "/poe/apply"
				case "dhcp_snooping_trust":
					path = "/dhcp-snooping/apply"
				case "storm_control":
					path = "/storm-control/apply"
				default:
					return "", errors.New("feature must be poe, dhcp_snooping_trust or storm_control")
				}
				return send(ctx, c, http.MethodPost,
					"/api/devices/"+esc(deviceID)+path, map[string]any{"ports": ports})
			},
		},
		{
			Name:       "apply_device_feature",
			Title:      "Push a device-wide gesture onto the switch now",
			Effect:     EffectWrite,
			Idempotent: false,
			Min:        ModeWrite,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device's id.", Req: true},
				{Name: "feature", Type: "string", Desc: "Which device-wide gesture to perform.", Enum: []string{"read_state", "config", "snmp", "trap_destination", "stp", "credentials"}, Req: true},
			},
			Description: `Perform one device-wide gesture through the device's connector.

feature="read_state" — RE-READ the device and record what it carries. Writes no intent and changes nothing on the switch. This is the gesture that makes drift visible: there is no scheduler, so a device's observed state is only as fresh as the last read-back, and a device never read back is "unknown" rather than conforming. Start here when diagnosing.

feature="config" — apply the stored configuration intent now, rather than waiting for automatic reconciliation.
  Three results must not be conflated. "skipped" means a safety guard-rail declined, and an operator may force it. "unsupported" means it is outside this connector's capabilities — nothing to retry, it has to be implemented. "error" means it failed and can be replayed. An apply that completes but leaves gaps is reported as stalled rather than failed, because re-running it would do exactly the same thing.

feature="snmp" — write SNMP identity so the device can be polled. Preview it first with api_get on /api/devices/<id>/snmp-plan.

feature="trap_destination" — write the address the device should send traps to. The return path of the gesture above: one makes the device queryable, the other makes it talkative.
  REQUIRES A DECLARED COLLECTOR. Without one the device is collected by every proxy covering its site, and "several proxies" is not one address. It refuses with a 422 naming the repair.

feature="stp" — push spanning-tree configuration.

feature="credentials" — apply this device's service identity, writing the expected secret onto this one device. Rotation changes what is expected; this is what changes what is installed. For a whole site, use deploy_credential.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var feature string
				feature, err = a.Required("feature")
				if err != nil {
					return "", err
				}
				var path string
				switch feature {
				case "read_state":
					path = "/read-state"
				case "config":
					path = "/config/apply"
				case "snmp":
					path = "/snmp-apply"
				case "trap_destination":
					path = "/trap-apply"
				case "stp":
					path = "/stp/apply"
				case "credentials":
					path = "/credentials/apply"
				default:
					return "", errors.New("feature must be read_state, config, snmp, " +
						"trap_destination, stp or credentials")
				}
				return send(ctx, c, http.MethodPost, "/api/devices/"+esc(deviceID)+path, map[string]any{})
			},
		},
		{
			Name:       "set_device_attachment",
			Title:      "Collector, address domain and management addressing",
			Effect:     EffectWrite,
			Idempotent: true,
			Min:        ModeWrite,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device's id.", Req: true},
				{Name: "collector_proxy_id", Type: "string", Desc: `Designate the proxy that SNMP-polls this device and whose address is written in as trap destination. Pass "" (empty string) to detach and return to collection by coverage.`},
				{Name: "address_domain_id", Type: "string", Desc: "Declare which network this device's address lives in."},
				{Name: "addressing", Type: "object", Desc: `Management addressing intent. Exactly these keys: {"mode":"inherit"|"dhcp"|"static", "static_ip":"10.0.0.5/24", "static_gateway":"10.0.0.1", "static_dns":["10.0.0.2"]}.`},
			},
			Description: `Set the three declarations that decide how meshcraft reaches and supervises one device. Pass only the ones you mean to change.

collector_proxy_id — a DECLARATION, not an election, so that it never changes by itself and silently invalidate a trap destination already written into hardware. Empty means "collect by coverage": any covering proxy polls it.
  THE TRAP TO AVOID: attaching a device to a proxy that does NOT cover its site does not move collection, IT STOPS COLLECTION ENTIRELY — that proxy cannot see the device, and the covering ones are no longer designated. ICMP ping continues from every covering proxy, so the status light STAYS GREEN while SNMP and traps are dead. The API refuses such an attachment with a 422; believe it. Check /api/sites/<id>/collector-proxies first.

address_domain_id — which declared space this device's address belongs to, used as the identity key (domain, IP) when there is no serial or MAC, and to arbitrate which device a proxy may report state for at a given address. Needed for a hand-entered device that inherits no domain. Getting it wrong cuts supervision, so it is site_admin.

addressing — how the device gets its address inside the management subnet.
  "inherit" is the default and means meshcraft imposes NOTHING; a device already on a fixed address stays there with no gesture.
  "dhcp" means meshcraft wants it as a DHCP client. MESHCRAFT IS NOT A DHCP SERVER and allocates nothing.
  "static" makes the three fields (static_ip, static_gateway, static_dns) authoritative. The prefix on static_ip is completed from the plan's subnet when the address falls inside it and is REQUIRED otherwise — it is never guessed.
  This records the intent only. Pushing it is a separate gesture, and the dangerous forms of it (the three-step management migration, the single-address jump) are deliberately NOT exposed by this server: done wrong they cut the device off the network, and only somebody physically present can recover it.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var done []string
				var base = "/api/devices/" + esc(deviceID)

				var collector, present = a["collector_proxy_id"]
				if present {
					var id, ok = collector.(string)
					if !ok {
						return "", errors.New("collector_proxy_id must be a string")
					}
					var out, sErr = send(ctx, c, http.MethodPut, base+"/collector",
						map[string]any{"proxy_id": id})
					if sErr != nil {
						return "", sErr
					}
					done = append(done, "collector: "+out)
				}
				var domain string
				domain, err = a.String("address_domain_id")
				if err != nil {
					return "", err
				}
				if domain != "" {
					var out, sErr = send(ctx, c, http.MethodPut, base+"/address-domain",
						map[string]any{"address_domain_id": domain})
					if sErr != nil {
						return "", sErr
					}
					done = append(done, "address domain: "+out)
				}
				var addressing map[string]any
				addressing, err = a.Map("addressing")
				if err != nil {
					return "", err
				}
				if addressing != nil {
					var out, sErr = send(ctx, c, http.MethodPut, base+"/mgmt-addressing", addressing)
					if sErr != nil {
						return "", sErr
					}
					done = append(done, "addressing intent: "+out)
				}
				if len(done) == 0 {
					return "", errors.New("nothing to do: pass at least one of " +
						"collector_proxy_id, address_domain_id or addressing")
				}
				return strings.Join(done, "\n\n"), nil
			},
		},
		{
			Name:       "set_device_tags",
			Title:      "Add or remove a device's tags",
			Effect:     EffectWrite,
			Idempotent: true,
			Min:        ModeWrite,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device's id.", Req: true},
				{Name: "add", Type: "array", Desc: "Tags to put on the device.", Items: "string"},
				{Name: "remove", Type: "array", Desc: "Tags to take off the device.", Items: "string"},
			},
			Description: `Put tags on a device or take them off. A device may carry many.

A tag says WHICH SETS a device belongs to and carries no configuration of its own. What makes it consequential is that a tag can decide which credential a device receives: credential resolution is total and deterministic, ordered by specificity (a credential scoped to a tag the device carries beats one scoped to its group, which beats the site default), then priority, then tag name. The whole list is returned rather than just the winner, so an ambiguity is reported rather than silently resolved.

A switch profile may also carry an assignment tag: putting that tag on a device evaluates and applies the profile.

Two namespaces are TECHNICAL, posted by meshcraft itself, and must not be hand-edited: "snmpv3-<auth>-<priv>", because an SNMPv3 key derives from the algorithm pair and that is cryptography rather than preference, and "pwpolicy-len<N>", a hardware password-length ceiling naming the class of secrets that fit. Tags whose source is a rule are managed by the site's tagging rules and a replay will remove them; manual tags are never removed by a replay.

Re-tagging a device never changes a password by itself.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var add, remove []string
				add, err = a.StringSlice("add")
				if err != nil {
					return "", err
				}
				remove, err = a.StringSlice("remove")
				if err != nil {
					return "", err
				}
				if len(add) == 0 && len(remove) == 0 {
					return "", errors.New("pass at least one tag in add or remove")
				}
				var done []string
				var tag string
				for _, tag = range add {
					var out, sErr = send(ctx, c, http.MethodPut,
						"/api/devices/"+esc(deviceID)+"/tags/"+esc(tag), nil)
					if sErr != nil {
						return "", sErr
					}
					done = append(done, "added "+tag+": "+out)
				}
				for _, tag = range remove {
					var out, sErr = send(ctx, c, http.MethodDelete,
						"/api/devices/"+esc(deviceID)+"/tags/"+esc(tag), nil)
					if sErr != nil {
						return "", sErr
					}
					done = append(done, "removed "+tag+": "+out)
				}
				return strings.Join(done, "\n"), nil
			},
		},
		{
			Name:       "adopt_device",
			Title:      "Turn a discovery candidate into a device",
			Effect:     EffectWrite,
			Idempotent: false,
			Min:        ModeWrite,
			Props: []P{
				{Name: "candidate_id", Type: "string", Desc: "The candidate's id, from list_discovery_candidates.", Req: true},
				{Name: "site_id", Type: "string", Desc: "The site this device will belong to. This is the decision being made.", Req: true},
				{Name: "name", Type: "string", Desc: "Name for the device. The candidate proposes one; this overrides it."},
				{Name: "location", Type: "string", Desc: "Free-text location."},
				{Name: "config_mode", Type: "string", Desc: `What becomes the initial intent: "import" takes the configuration read from the device, "push" makes meshcraft the source of truth and starts the device blank in the model.`, Enum: []string{"import", "push"}},
			},
			Description: `Adopt a discovery candidate into the inventory.

CHOOSING THE SITE IS THE ESSENCE OF THIS GESTURE. A candidate belongs to nobody and has no site; this is the only moment a device acquires one, and it is never inferred from the proxy that made the discovery. Requires operator on the chosen site.

Adoption also picks up the physical faceplate the connector read, provisions SNMP identity and the trap destination, and applies automatic tagging.

config_mode decides the initial intent, and it is the choice to think about. "push" makes meshcraft the source of truth: the device starts blank in the model and meshcraft's configuration will be pushed to it. "import" makes the configuration currently ON the device become the intent, so nothing is changed under you. FOR EQUIPMENT ALREADY IN PRODUCTION, "import" is almost always what you want.

A freshly adopted device has never been read back, so reconciliation files it as "unknown" — which is the truth, not a fault. Trying credentials and a full read-back are separate gestures; use apply_device_feature(feature="read_state").`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var candidateID, err = a.Required("candidate_id")
				if err != nil {
					return "", err
				}
				var siteID string
				siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				var body = map[string]any{"site_id": siteID}
				var name string
				name, err = a.String("name")
				if err != nil {
					return "", err
				}
				if name != "" {
					body["name"] = name
				}
				var location string
				location, err = a.String("location")
				if err != nil {
					return "", err
				}
				if location != "" {
					body["location"] = location
				}
				var configMode string
				configMode, err = a.String("config_mode")
				if err != nil {
					return "", err
				}
				if configMode != "" {
					if configMode != "import" && configMode != "push" {
						return "", errors.New(`config_mode must be "import" or "push"`)
					}
					body["config_mode"] = configMode
				}
				return send(ctx, c, http.MethodPost,
					"/api/discovery/candidates/"+esc(candidateID)+"/adopt", body)
			},
		},
		{
			Name:       "api_call",
			Title:      "Any write route this server has no dedicated tool for",
			Effect:     EffectWrite,
			Idempotent: false,
			Min:        ModeWrite,
			Props: []P{
				{Name: "method", Type: "string", Desc: "HTTP method.", Enum: []string{"POST", "PUT", "PATCH"}, Req: true},
				{Name: "path", Type: "string", Desc: "An /api/... path.", Req: true},
				{Name: "body", Type: "object", Desc: "JSON body, if the route takes one."},
			},
			Description: `Perform an authenticated POST, PUT or PATCH on a controller route this server has no dedicated tool for — creating a VLAN, renaming a device, saving a profile, editing the management plan.

Deliberate limits, and they are not negotiable from here:
  - DELETE IS NOT AVAILABLE through this tool, at any mode. Deletion exists only as delete_object, which demands the object's exact current name. This door can therefore never destroy anything, so a misread name or a hallucinated path cannot cost a row.
  - A set of paths is refused outright because the gesture is irreversible, cuts the device off the network, publishes data anonymously, or changes who may do what. The refusal names the reason. Among them: the three-step management migration and the single-address jump (done wrong they strand the device and only somebody physically present can recover it), dashboard share links (they publish a site's state to an anonymous URL), user and role administration (an automation client must not edit the permission graph it is bounded by), factory reset and serial procedures, and credential revelation.

Useful paths:
  POST  /api/sites/<id>/vlans                 — create a VLAN.
  PATCH /api/vlans/<id>                       — rename or recolour one.
  POST  /api/sites/<id>/devices               — create a device by hand.
  PATCH /api/devices/<id>                     — rename; the device's own system name is then reconciled onto it, so there is no separate hostname gesture.
  POST  /api/sites/<id>/profiles/<kind>       — create a port or switch profile.
  PUT   /api/sites/<id>/mgmt-plan             — the management plan.
  PUT   /api/sites/<id>/tag-rules             — a tagging rule.
  POST  /api/sites/<id>/tag-rules/apply       — replay the rules. WITHOUT a body this is a PREVIEW; it only applies when asked to. A replay removes only rule-assigned tags, never manual ones.
  POST  /api/sites/<id>/links                 — declare a link.
  POST  /api/devices/<id>/snmp-walk           — a read-only SNMP walk (a POST because the question carries a body).
  POST  /api/devices/<id>/lags                — aggregates. REPLACE SEMANTICS: send the COMPLETE desired list, because what you omit is dissolved. An aggregate is a named bundle of ports that behaves like one port and is drawn as one.
  PUT   /api/sites/<id>/stp-root-bridges      — root bridge designation. Designating ONE root is ordinary and reversible. A SECOND root is a different thing wearing the same word: a tree has one root by definition, so two only mean something as roots of different trees, which requires BPDUs to stop crossing the link between them — the one control here that can CAUSE a broadcast storm. The apply order is the safety property: filter first, then root. If it completes only the first half it leaves that half in place and says so, and must not be rolled back.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var method, err = a.Required("method")
				if err != nil {
					return "", err
				}
				method = strings.ToUpper(method)
				if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
					return "", errors.New("method must be POST, PUT or PATCH. " +
						"DELETE is not available through api_call: deletion is delete_object, " +
						"which requires the object's exact current name")
				}
				var path string
				path, err = a.Required("path")
				if err != nil {
					return "", err
				}
				err = checkReadPath(path)
				if err != nil {
					return "", err
				}
				err = checkWritePath(path)
				if err != nil {
					return "", err
				}
				var body map[string]any
				body, err = a.Map("body")
				if err != nil {
					return "", err
				}
				if body == nil {
					body = map[string]any{}
				}
				return send(ctx, c, method, path, body)
			},
		},
	}
}

// forbidden is a path fragment this server refuses to drive, with the reason.
//
// # Why exclusion rather than confirmation
//
// A confirmation an agent types is only ever evidence that it read the object.
// It is the right guard for a gesture whose damage is a lost row, which a backup
// or a re-entry undoes. It is the WRONG guard for a gesture whose failure mode is
// that nobody can reach the equipment any more: there is no undo to reach for,
// and the repair is a person with a console cable in a building. Those gestures
// are not gated here, they are absent, and the two reasons are:
//
//   - the management migration and the single-address jump strand the device
//     when they go wrong, and the commit's own answer is routinely lost in the
//     act of switching the interface it was answering on — so even "did it work"
//     is not reliably knowable from here;
//   - user and role administration is the permission graph that bounds this
//     client. ADR-0011 is explicit that the MCP has no privilege of its own and
//     is not a trusted component; a client able to edit roles could lift its own
//     ceiling, which makes every other limit in this file decorative.
//
// Dashboard share links are here for a third reason: one call publishes a site's
// state to a URL that needs no login. That is data exfiltration with a friendly
// name, and it is not recoverable by deleting the link afterwards.
var forbidden = []struct {
	fragment string
	why      string
}{
	{"/mgmt-migrate", "the three-step management migration can strand a device with no way back " +
		"except physical access, and the commit's own response is routinely lost when the " +
		"interface it answers on is the one being switched. Do this from the web interface, " +
		"with somebody able to reach the equipment."},
	{"/mgmt-addressing/jump", "a single-address leap cuts the device the moment it is wrong, " +
		"and it is unrecoverable remotely. Do this from the web interface."},
	{"/mgmt-addressing/probe-static-owner", "this is a bring-up probe that puts a temporary " +
		"static interface on a device. Do it from the web interface, on a bench."},
	{"/share", "a dashboard share link publishes a site's state at a URL that requires no login. " +
		"Creating one is a disclosure decision for a person to make, not an automation client."},
	{"/api/users", "user and role administration is the permission graph that bounds this client. " +
		"Per ADR-0011 this server has no privilege of its own and is not a trusted component, " +
		"so it does not edit who may do what."},
	{"/roles", "role assignment is the permission graph that bounds this client; it is not " +
		"editable from here."},
	{"/reveal", "revealing a secret is a separate journaled gesture, and this server does not " +
		"relay secrets. Use the web interface."},
	{"/reset-procedures", "factory reset is physical and unrecoverable. It is not driven from here."},
	{"/serial", "the serial console wizard needs a human at a console cable."},
	{"/socks-token", "this server does not mint credentials."},
	{"/onboard", "onboarding a not-yet-adopted device writes to equipment that is not in any " +
		"site yet. Adopt it first with adopt_device."},
}

// checkWritePath refuses the paths this server does not drive.
func checkWritePath(path string) error {
	var entry struct {
		fragment string
		why      string
	}
	for _, entry = range forbidden {
		if strings.Contains(path, entry.fragment) {
			return errors.New("this server does not perform " + entry.fragment + ": " + entry.why)
		}
	}
	return nil
}

// send performs a write and returns the body, or the controller's refusal.
func send(ctx context.Context, c *Client, method, path string, body any) (string, error) {
	var raw, err = c.Do(ctx, method, path, body)
	if err != nil && !errors.Is(err, errTruncated) {
		return "", err
	}
	var text = strings.TrimSpace(string(raw))
	if text == "" {
		return "OK (" + method + " " + path + ", the controller returned no body)", nil
	}
	return text, nil
}
