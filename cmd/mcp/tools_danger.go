package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// dangerTools: the gestures that cannot be undone from this API.
//
// # What a confirmation argument is, and what it is not
//
// This server cannot ask a person anything. It has no screen, and a string an
// agent types is not consent — the agent would type whatever the schema asked
// for. So "requires confirmation" here means exactly one thing, and it is worth
// being precise about because overstating it would be worse than having no gate:
//
// A confirmation argument must be a value the caller could only obtain BY
// READING THE OBJECT IT IS ABOUT TO DESTROY, and it is checked against what the
// controller holds at the moment of the call. That makes the accidental
// deletion impossible — a misread tool name, a hallucinated id, an id left over
// from an earlier turn, a retry against an object that has since been renamed,
// all fail closed and change nothing. It does not, and cannot, stop a caller
// that has decided to delete the thing.
//
// Deliberate destruction is gated somewhere else, and in two places that CAN
// hold:
//
//   - at launch, by Mode. None of these tools is offered unless this process was
//     started in admin mode, which is a decision a person made out of band, once,
//     when they wrote the configuration. An agent cannot talk its way into it.
//   - at the call, by the client. Every tool here is annotated destructiveHint,
//     which is the protocol's own channel for "ask the human first". Honouring it
//     is the client's job and not something a server can verify, which is
//     precisely why the two gates above are not left to it alone.
//
// And the strongest limit is not in this file at all: a read-only API token
// (ADR-0011 point 2) makes every one of these fail at the controller regardless
// of how this process was started. That is the one to rely on.
func dangerTools() []Tool {
	return []Tool{
		{
			Name:       "delete_object",
			Title:      "Delete one object, by name",
			Effect:     EffectDestructive,
			Idempotent: false,
			Min:        ModeAdmin,
			Props: []P{
				{Name: "kind", Type: "string", Desc: "What kind of object to delete.", Enum: deletableKinds(), Req: true},
				{Name: "id", Type: "string", Desc: "The object's id.", Req: true},
				{Name: "confirm_name", Type: "string", Desc: "The object's exact current name, as the controller reports it right now. The deletion is refused if it does not match.", Req: true},
				{Name: "site_id", Type: "string", Desc: "Required for kinds that live under a site: device, vlan, dashboard, site_credential, tag_rule, profile.", Req: false},
				{Name: "profile_kind", Type: "string", Desc: `Required for kind="profile": which family ("port", "switch" or "device").`},
			},
			Description: `Delete one object. THIS CANNOT BE UNDONE and this API has no restore.

EVERY DELETION IN THIS SERVER COMES THROUGH THIS ONE TOOL. That is on purpose: fifteen separate delete tools would be fifteen chances for a name to be misread, and they would sit in your context advertising deletion on every request. One door is auditable; it is also the only place that has to be got right.

You must pass confirm_name: the object's exact current name, which the controller re-reads and compares at the moment of the call. An id alone is not enough, so an id that was hallucinated, copied from the wrong turn, or has been renamed or replaced underneath you fails and changes nothing. If you do not know the name, read it first — that is the point of the check.

What deleting each kind actually costs, because they are not equivalent:
  site            — the whole tenancy: its devices, VLANs, profiles, credentials and plans. The largest irreversible gesture in the product.
  device          — the inventory record, its intent, its observed state and its history. The equipment keeps running, configured as it is, now unmanaged.
  vlan            — refused for VLAN 1, which cannot be deleted because every switch ships with it and none can shed it.
  link            — a declared link only. Observed LLDP neighbourhood is not deletable: it is an observation, and it comes back on the next poll.
  proxy           — removes a remote point of presence. Every device collected only through it stops being collected.
  site_credential — the record of a secret and its generations. The secret stays on the hardware; you simply no longer know it. Consider what that locks you out of.
  known_credential, address_domain, scan_prefix, tag_rule, profile, dashboard, group, discovery_candidate, driver_repo, agent_repo — configuration objects. A deleted candidate is simply rediscovered on the next sweep.

Users and roles are NOT deletable here, at any mode: that is the permission graph this client is bounded by, and per ADR-0011 it has no privilege of its own.`,
			Run: runDelete,
		},
		{
			Name:       "power_cycle_ports",
			Title:      "Cut PoE power to reboot what is plugged in",
			Effect:     EffectDestructive,
			Idempotent: false,
			Min:        ModeAdmin,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The switch supplying the power.", Req: true},
				{Name: "ports", Type: "array", Desc: "The ports to cut power on. Enumerate them explicitly; there is no 'all'.", Items: "string", Req: true},
				{Name: "off_for_seconds", Type: "integer", Desc: "How long to leave the power off. The controller caps it."},
				{Name: "confirm", Type: "boolean", Desc: "Must be true. Confirms you intend to reboot the equipment on these ports.", Req: true},
			},
			Description: `Cut PoE power on specific ports and restore it, which REBOOTS WHATEVER IS PLUGGED INTO THEM — access points, cameras, phones.

This is distinct from set_port_admin on purpose, and the distinction matters: one button for both would eventually reboot a building of access points to fix a link light. IF YOU WANT TO BOUNCE A LINK, USE set_port_admin (disable then enable). Use this only when the far-end device itself must restart.

It BLOCKS for the whole off-delay, so the call takes at least off_for_seconds to return. A timeout here does not mean the power stayed on.

Ports must be enumerated one by one. There is no "every port" form, because the gesture that reboots every powered device on a switch should be impossible to express by accident. Note also that PoE capability is per port and read from the device: a port with no power-sourcing hardware has nothing to cycle.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var ports []string
				ports, err = a.StringSlice("ports")
				if err != nil {
					return "", err
				}
				if len(ports) == 0 {
					return "", errors.New("ports is required: name the ports to cycle explicitly")
				}
				if !a.Bool("confirm") {
					return "", errors.New("this REBOOTS the equipment plugged into " +
						strconv.Itoa(len(ports)) + " port(s) on device " + deviceID + " (" +
						strings.Join(ports, ", ") + "). Nothing was done. Pass confirm=true " +
						"if that is what you mean — and if you only need to bounce the link, " +
						"use set_port_admin instead.")
				}
				var body = map[string]any{"ports": ports}
				var off = a.Int("off_for_seconds", 0)
				if off > 0 {
					body["off_for_seconds"] = off
				}
				return send(ctx, c, http.MethodPost,
					"/api/devices/"+esc(deviceID)+"/ports/power-cycle", body)
			},
		},
		{
			Name:       "revoke_proxy",
			Title:      "Cut a proxy's session immediately",
			Effect:     EffectDestructive,
			Idempotent: true,
			Min:        ModeAdmin,
			Props: []P{
				{Name: "proxy_id", Type: "string", Desc: "The proxy to revoke.", Req: true},
				{Name: "confirm_name", Type: "string", Desc: "The proxy's exact current name, as list_proxies reports it.", Req: true},
			},
			Description: `Revoke a proxy: invalidate its credentials and cut its tunnel now.

Everything reached THROUGH that proxy becomes unreachable at once — every device it is the declared collector for stops being polled, its traps have nowhere to arrive, and no write can take the path it provided. A revoked proxy cannot reconnect; bringing the site back means re-enrolling a new one, which needs somebody able to run the installer where it lives.

This is not how you take a proxy out of service tidily. To stop it serving one site, remove that site from its coverage instead; to retire it, revoke it and then delete it. Revoke is the gesture for a proxy you believe is compromised or has been lost.

Requires the proxy's exact current name, re-read at the moment of the call.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var proxyID, err = a.Required("proxy_id")
				if err != nil {
					return "", err
				}
				var confirm string
				confirm, err = a.Required("confirm_name")
				if err != nil {
					return "", err
				}
				var name string
				name, err = proxyName(ctx, c, proxyID)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(confirm) != name {
					return "", errors.New("confirm_name does not match: proxy " + proxyID +
						" is named " + quote(name) + ", you passed " + quote(confirm) +
						". Nothing was done.")
				}
				return send(ctx, c, http.MethodPost,
					"/api/proxies/"+esc(proxyID)+"/revoke", map[string]any{})
			},
		},
		{
			Name:       "prune_site_clients",
			Title:      "Forget learned machines older than N days",
			Effect:     EffectDestructive,
			Idempotent: false,
			Min:        ModeAdmin,
			Props: []P{
				{Name: "site_id", Type: "string", Desc: "The site whose ledger to shorten.", Req: true},
				{Name: "days", Type: "integer", Desc: "Forget clients not seen for more than this many days.", Req: true},
				{Name: "confirm", Type: "boolean", Desc: "Must be true.", Req: true},
			},
			Description: `Forget clients — learned MAC addresses — not seen for more than the given number of days.

The client list is an APPEND-ONLY LEDGER by design, so this is the only thing in the product that can ever shorten it. What it removes is history: where a machine was last seen, and since when. That record cannot be reconstructed, because it was only ever built by observing, and an observation that was not kept did not happen.

Think about it before shortening the window: "which port was this laptop on last month" is a question only this ledger answers, and the usual reason to ask it is an incident.

This forgets records in meshcraft. It changes nothing on any switch and does not disconnect anybody.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				var days = a.Int("days", 0)
				if days <= 0 {
					return "", errors.New("days is required and must be a positive number of days")
				}
				if !a.Bool("confirm") {
					return "", errors.New("this permanently forgets every client of site " +
						siteID + " not seen for " + strconv.Itoa(days) + " days. The ledger " +
						"is append-only, so this history cannot be rebuilt. Nothing was done. " +
						"Pass confirm=true to proceed.")
				}
				return send(ctx, c, http.MethodDelete,
					"/api/sites/"+esc(siteID)+"/clients?days="+strconv.Itoa(days), nil)
			},
		},
	}
}

// deletable describes one kind delete_object accepts.
//
// `path` builds the DELETE route; `name` re-reads the object's current name for
// the confirmation. A kind with no resolvable name is NOT listed: without a name
// to check, confirm_name would be a field the caller fills from its own
// arguments, which is a gate that always opens and is worse than no gate at all
// because it reads like one.
type deletable struct {
	kind      string
	needsSite bool
	path      func(a Args, id string) (string, error)
	name      func(ctx context.Context, c *Client, a Args, id string) (string, error)
}

var deletables = []deletable{
	{
		kind: "site",
		path: func(a Args, id string) (string, error) { return "/api/sites/" + esc(id), nil },
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			return nameFromList(ctx, c, "/api/sites", id, "name")
		},
	},
	{
		kind:      "device",
		needsSite: true,
		path:      func(a Args, id string) (string, error) { return "/api/devices/" + esc(id), nil },
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			var siteID, err = a.Required("site_id")
			if err != nil {
				return "", err
			}
			return nameFromList(ctx, c, "/api/sites/"+esc(siteID)+"/devices", id, "name")
		},
	},
	{
		kind:      "vlan",
		needsSite: true,
		path:      func(a Args, id string) (string, error) { return "/api/vlans/" + esc(id), nil },
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			var siteID, err = a.Required("site_id")
			if err != nil {
				return "", err
			}
			return nameFromList(ctx, c, "/api/sites/"+esc(siteID)+"/vlans", id, "name")
		},
	},
	{
		kind: "proxy",
		path: func(a Args, id string) (string, error) { return "/api/proxies/" + esc(id), nil },
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			return proxyName(ctx, c, id)
		},
	},
	{
		kind:      "profile",
		needsSite: true,
		path: func(a Args, id string) (string, error) {
			var kind, err = a.Required("profile_kind")
			if err != nil {
				return "", errors.New(`kind="profile" also needs profile_kind ("port", "switch" or "device")`)
			}
			return "/api/profiles/" + esc(kind) + "/" + esc(id), nil
		},
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			var siteID, err = a.Required("site_id")
			if err != nil {
				return "", err
			}
			var kind string
			kind, err = a.Required("profile_kind")
			if err != nil {
				return "", err
			}
			return nameFromList(ctx, c,
				"/api/sites/"+esc(siteID)+"/profiles/"+esc(kind), id, "name")
		},
	},
	{
		kind:      "dashboard",
		needsSite: true,
		path:      func(a Args, id string) (string, error) { return "/api/dashboards/" + esc(id), nil },
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			var siteID, err = a.Required("site_id")
			if err != nil {
				return "", err
			}
			return nameFromList(ctx, c, "/api/sites/"+esc(siteID)+"/dashboards", id, "name")
		},
	},
	{
		kind:      "site_credential",
		needsSite: true,
		path: func(a Args, id string) (string, error) {
			var siteID, err = a.Required("site_id")
			if err != nil {
				return "", err
			}
			return "/api/sites/" + esc(siteID) + "/credentials/" + esc(id), nil
		},
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			var siteID, err = a.Required("site_id")
			if err != nil {
				return "", err
			}
			return credentialLabel(ctx, c, siteID, id)
		},
	},
	{
		kind: "address_domain",
		path: func(a Args, id string) (string, error) {
			return "/api/discovery/domains/" + esc(id), nil
		},
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			return nameFromList(ctx, c, "/api/discovery/domains", id, "name")
		},
	},
	{
		kind: "known_credential",
		path: func(a Args, id string) (string, error) {
			return "/api/discovery/credentials/" + esc(id), nil
		},
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			return nameFromList(ctx, c, "/api/discovery/credentials", id, "username", "name", "label")
		},
	},
	{
		kind: "driver_repo",
		path: func(a Args, id string) (string, error) { return "/api/driver-repos/" + esc(id), nil },
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			return nameFromList(ctx, c, "/api/driver-repos", id, "name", "url")
		},
	},
	{
		kind: "agent_repo",
		path: func(a Args, id string) (string, error) { return "/api/agent-repos/" + esc(id), nil },
		name: func(ctx context.Context, c *Client, a Args, id string) (string, error) {
			return nameFromList(ctx, c, "/api/agent-repos", id, "name", "url")
		},
	},
}

func deletableKinds() []string {
	var out = make([]string, 0, len(deletables))
	var d deletable
	for _, d = range deletables {
		out = append(out, d.kind)
	}
	sort.Strings(out)
	return out
}

func runDelete(ctx context.Context, c *Client, a Args) (string, error) {
	var kind, err = a.Required("kind")
	if err != nil {
		return "", err
	}
	var id string
	id, err = a.Required("id")
	if err != nil {
		return "", err
	}
	var confirm string
	confirm, err = a.Required("confirm_name")
	if err != nil {
		return "", errors.New("confirm_name is required: pass the object's exact current " +
			"name. Read it first if you do not know it — that is what the check is for.")
	}

	var chosen deletable
	var found bool
	var d deletable
	for _, d = range deletables {
		if d.kind == kind {
			chosen, found = d, true
			break
		}
	}
	if !found {
		return "", errors.New("kind must be one of: " + strings.Join(deletableKinds(), ", ") +
			". Users and roles are deliberately not deletable from this server.")
	}
	if chosen.needsSite {
		var _, siteErr = a.Required("site_id")
		if siteErr != nil {
			return "", errors.New(`kind="` + kind + `" also needs site_id, so that the ` +
				"object's name can be re-read before anything is deleted")
		}
	}

	// Resolve the CURRENT name before touching anything. A failure here is a
	// refusal, never a reason to proceed: not being able to confirm what we are
	// about to destroy is exactly when to stop.
	var actual string
	actual, err = chosen.name(ctx, c, a, id)
	if err != nil {
		return "", errors.New("could not read the current name of " + kind + " " + id +
			", so nothing was deleted: " + err.Error())
	}
	if strings.TrimSpace(confirm) != actual {
		return "", errors.New("confirm_name does not match. " + kind + " " + id +
			" is currently named " + quote(actual) + " and you passed " + quote(confirm) +
			". NOTHING WAS DELETED. If you meant this object, pass its name exactly; " +
			"if the name surprises you, read it again before deciding — it may not be " +
			"the object you think it is.")
	}

	var path string
	path, err = chosen.path(a, id)
	if err != nil {
		return "", err
	}
	var out string
	out, err = send(ctx, c, http.MethodDelete, path, nil)
	if err != nil {
		return "", err
	}
	return "Deleted " + kind + " " + quote(actual) + " (" + id + "). This cannot be undone.\n" + out, nil
}

// nameFromList finds an object in a list route and returns its name.
//
// Several keys may be tried because the lists are not uniform — a known
// credential has a username where a site has a name — and the first non-empty
// one wins.
func nameFromList(ctx context.Context, c *Client, path, id string, keys ...string) (string, error) {
	var body, err = c.GetJSON(ctx, path)
	if err != nil {
		return "", err
	}
	var list, ok = body.([]any)
	if !ok {
		return "", errors.New(path + " did not return a list")
	}
	var entry any
	for _, entry = range list {
		var object, isMap = entry.(map[string]any)
		if !isMap {
			continue
		}
		var objectID, _ = object["id"].(string)
		if objectID != id {
			continue
		}
		var key string
		for _, key = range keys {
			var name, isStr = object[key].(string)
			if isStr && strings.TrimSpace(name) != "" {
				return name, nil
			}
		}
		return "", errors.New("the object exists but carries no name to confirm against")
	}
	return "", errors.New("no object with id " + id + " at " + path +
		" (it may already be gone, or belong to another site)")
}

func proxyName(ctx context.Context, c *Client, id string) (string, error) {
	return nameFromList(ctx, c, "/api/proxies", id, "name", "hostname")
}

func quote(s string) string { return `"` + s + `"` }

func sortStrings(s []string) { sort.Strings(s) }
