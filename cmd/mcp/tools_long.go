package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// longTools: gestures that return before the work is finished.
//
// Every one of them names its follower in its own description, and they all
// report through the same follow_gesture tool. One follower rather than four is
// deliberate: the asynchronous model — "this has started, it is not done, here is
// how to watch and how to stop" — is a thing an agent has to learn once, and four
// differently-shaped status tools would teach it four times and fit together
// three ways.
func longTools() []Tool {
	return []Tool{
		{
			Name:       "deploy_credential",
			Title:      "Push a secret onto a whole site's switches",
			Effect:     EffectWrite,
			Idempotent: false,
			Min:        ModeAdmin,
			Props: []P{
				{Name: "site_id", Type: "string", Desc: "The credential's site.", Req: true},
				{Name: "credential_id", Type: "string", Desc: "The credential to deploy.", Req: true},
				{Name: "generation", Type: "integer", Desc: "The generation number you believe is active. Read it from list_credentials and pass it back; the controller refuses the run if it has rotated since.", Req: true},
				{Name: "write", Type: "boolean", Desc: "FALSE (default) runs a full rehearsal and writes nothing. TRUE writes the secret onto every device in scope."},
				{Name: "confirm", Type: "string", Desc: `Required only when write is true: pass the credential's exact current label, as list_credentials reports it.`},
			},
			Description: `Deploy a credential across a site: write an administration secret onto every switch in its scope.

THIS RETURNS IMMEDIATELY AND THE RUN CONTINUES IN THE BACKGROUND. Watch it with follow_gesture(kind="credential_deploy", site_id=..., credential_id=...), which also stops it.

THE DEFAULT IS A REHEARSAL, and the asymmetry is deliberate: a forgotten flag must produce the harmless run, not the fleet-wide write. A rehearsal is not a formality either — it opens the real session on every device with the real candidate list and applies every gate right up to the point of sending. Run it first; it is how you find the device that will refuse before you have changed forty others.

Two confirmations are required for a real write, and each catches a different mistake:
  - generation must match the ACTIVE generation. A rotation between reading the screen and pressing the button is not exotic — it is two operators, or an operator and a script — and without this the run would push a generation nobody asked for and record it as success.
  - confirm must be the credential's exact current label, which means you have looked at the object you are about to deploy rather than at an id you were handed.

Reading the result: partial failure is the NORMAL case and does not stop the run. A device that refused a session, has no connector, or whose secret the platform rejected before sending has not been changed; it appears in the outstanding list and the run moves on. One outcome does halt everything, and it is the one to look for: an UNCONFIRMED WRITE, where the device accepted the form and did not confirm. That device may now carry a secret nobody recorded, so the run stops rather than doing the same thing to the next forty.

A credential holding a distinct secret per device is refused: there is no single generation to push fleet-wide.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				var credID string
				credID, err = a.Required("credential_id")
				if err != nil {
					return "", err
				}
				var generation = a.Int("generation", 0)
				if generation <= 0 {
					return "", errors.New("generation is required: name the generation you " +
						"believe is active (read it from list_credentials). A fleet-wide " +
						"secret push does not start on an implicit target")
				}
				var write = a.Bool("write")
				if write {
					var confirm string
					confirm, err = a.Required("confirm")
					if err != nil {
						return "", errors.New("write=true requires confirm: pass the " +
							"credential's exact current label, from list_credentials")
					}
					var label string
					label, err = credentialLabel(ctx, c, siteID, credID)
					if err != nil {
						return "", err
					}
					if strings.TrimSpace(confirm) != label {
						return "", errors.New("confirm does not match: credential " + credID +
							" is labelled " + quote(label) + ", you passed " + quote(confirm) +
							". Nothing was written. Re-read list_credentials and confirm the " +
							"credential you actually mean")
					}
				}
				var body = map[string]any{"generation": generation, "write": write}
				var out, sendErr = send(ctx, c, http.MethodPost,
					"/api/sites/"+esc(siteID)+"/credentials/"+esc(credID)+"/deploy", body)
				if sendErr != nil {
					return "", sendErr
				}
				var banner = "REHEARSAL STARTED — nothing will be written to any device.\n"
				if write {
					banner = "WRITING STARTED — this run is changing secrets on hardware.\n"
				}
				return banner + "This runs in the background. Follow it with " +
					"follow_gesture(kind=\"credential_deploy\", site_id=\"" + siteID +
					"\", credential_id=\"" + credID + "\").\n\n" + out, nil
			},
		},
		{
			Name:       "follow_gesture",
			Title:      "Watch, or stop, something still running",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeWrite,
			Props: []P{
				{Name: "kind", Type: "string", Desc: "Which long gesture to follow.", Enum: []string{"credential_deploy", "relocate", "image_build"}, Req: true},
				{Name: "site_id", Type: "string", Desc: "For kind=credential_deploy."},
				{Name: "credential_id", Type: "string", Desc: "For kind=credential_deploy."},
				{Name: "device_id", Type: "string", Desc: "For kind=relocate."},
				{Name: "repo_id", Type: "string", Desc: "For kind=image_build."},
				{Name: "repo_kind", Type: "string", Desc: `For kind=image_build: "driver" (built and run on the controller) or "agent" (built into an OCI layout for a remote proxy to pull).`, Enum: []string{"driver", "agent"}},
				{Name: "stop", Type: "boolean", Desc: "True to STOP or cancel the gesture instead of reading it. Not available for an image build."},
			},
			Description: `Read the state of a long gesture that is still running, or stop it.

Every tool in this server that returns before its work is finished says so and names the arguments to pass here. Polling is the only way to know an asynchronous gesture finished: nothing calls you back.

  kind="credential_deploy" — needs site_id and credential_id. Reports what is queued, skipped, done and remaining, with a transcript per device and no secrets. stop=true halts the run; devices already written stay written, because a secret on a switch is not undone by stopping the loop that put it there.
  kind="relocate"          — needs device_id. Reports the search for a device that changed address. stop=true cancels it.
  kind="image_build"       — needs repo_id and repo_kind. Returns the BUILD LOG, which is what says why a build failed. There is no stop.

A gesture that has finished still answers here, so this is also how to read the outcome of something that ended while you were doing something else.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var kind, err = a.Required("kind")
				if err != nil {
					return "", err
				}
				var stop = a.Bool("stop")
				switch kind {
				case "credential_deploy":
					var siteID string
					siteID, err = a.Required("site_id")
					if err != nil {
						return "", err
					}
					var credID string
					credID, err = a.Required("credential_id")
					if err != nil {
						return "", err
					}
					var path = "/api/sites/" + esc(siteID) + "/credentials/" + esc(credID) + "/deploy"
					if stop {
						return send(ctx, c, http.MethodDelete, path, nil)
					}
					return relay(ctx, c, path)
				case "relocate":
					var deviceID string
					deviceID, err = a.Required("device_id")
					if err != nil {
						return "", err
					}
					var path = "/api/devices/" + esc(deviceID) + "/relocate"
					if stop {
						return send(ctx, c, http.MethodDelete, path, nil)
					}
					return relay(ctx, c, path)
				case "image_build":
					if stop {
						return "", errors.New("an image build cannot be stopped; read its log instead")
					}
					var repoID string
					repoID, err = a.Required("repo_id")
					if err != nil {
						return "", err
					}
					var repoKind string
					repoKind, err = a.Required("repo_kind")
					if err != nil {
						return "", errors.New(`repo_kind is required: "driver" or "agent"`)
					}
					if repoKind != "driver" && repoKind != "agent" {
						return "", errors.New(`repo_kind must be "driver" or "agent"`)
					}
					return relay(ctx, c, "/api/"+repoKind+"-repos/"+esc(repoID)+"/log")
				}
				return "", errors.New("kind must be credential_deploy, relocate or image_build")
			},
		},
		{
			Name:       "run_discovery_scan",
			Title:      "Sweep a declared prefix for equipment",
			Effect:     EffectWrite,
			Idempotent: false,
			Min:        ModeWrite,
			Props: []P{
				{Name: "prefix_id", Type: "string", Desc: "The scan prefix to sweep, from api_get on /api/discovery/prefixes.", Req: true},
			},
			Description: `Sweep one declared scan prefix and file what answers as discovery candidates.

A scan prefix is a CIDR range declared ON A PROXY — the one that can reach it — and IT HAS NO SITE, because at this point nobody knows which site the equipment belongs to. The prefix carries its address domain rather than the proxy doing so, because one customer can have both a shared server VLAN that two proxies reach and two separate office LANs reusing the same plan. Most specific prefix wins, like a routing table.

This touches equipment that is not in the inventory, so it is a write even though it changes nothing on any device. It can take a while on a large range. Read the outcome with list_discovery_candidates; new candidates appear there, deduplicated by serial, then MAC, then (domain, IP).

A candidate acquires a site only at adoption, never from this scan.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var prefixID, err = a.Required("prefix_id")
				if err != nil {
					return "", err
				}
				var out, sendErr = send(ctx, c, http.MethodPost,
					"/api/discovery/prefixes/"+esc(prefixID)+"/scan", map[string]any{})
				if sendErr != nil {
					return "", sendErr
				}
				return "Sweep requested. New equipment appears via list_discovery_candidates; " +
					"a large range takes time, so an empty list immediately after this is not " +
					"an answer yet.\n\n" + out, nil
			},
		},
		{
			Name:       "relocate_device",
			Title:      "Find a device that changed address",
			Effect:     EffectWrite,
			Idempotent: false,
			Min:        ModeWrite,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device that has gone missing.", Req: true},
				{Name: "address", Type: "string", Desc: "A candidate IPv4 address to try directly, instead of (or alongside) the automatic scan. Bare address, no prefix."},
			},
			Description: `Start the search for a device whose management address changed, so the inventory record follows the equipment instead of going dark.

An automatic scan and a hand-entered address run in PARALLEL — submitting an address does not cancel the scan, and either may be the one that finds it. A candidate address is only promoted to the device's management address once the serial has been verified, which is what stops the search from attaching the record to a different switch that happens to answer on that address.

THIS RETURNS BEFORE THE SEARCH FINISHES. Follow it with follow_gesture(kind="relocate", device_id=...), which also cancels it.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var address string
				address, err = a.String("address")
				if err != nil {
					return "", err
				}
				var base = "/api/devices/" + esc(deviceID) + "/relocate"
				var out string
				if address != "" {
					out, err = send(ctx, c, http.MethodPost, base+"/address",
						map[string]any{"ip": address})
				} else {
					out, err = send(ctx, c, http.MethodPost, base, map[string]any{})
				}
				if err != nil {
					return "", err
				}
				return "Search started. Follow it with follow_gesture(kind=\"relocate\", " +
					"device_id=\"" + deviceID + "\").\n\n" + out, nil
			},
		},
		{
			Name:       "build_image",
			Title:      "Build a connector or agent image from its git repository",
			Effect:     EffectWrite,
			Idempotent: false,
			Min:        ModeAdmin,
			Props: []P{
				{Name: "repo_id", Type: "string", Desc: "The repository to build, from api_get on /api/driver-repos or /api/agent-repos.", Req: true},
				{Name: "repo_kind", Type: "string", Desc: "Which pipeline.", Enum: []string{"driver", "agent"}, Req: true},
			},
			Description: `Build an image from a registered git repository. Global admin only, because registering and building one means compiling and running third-party code on the controller host — an instance decision, never a site one.

The two pipelines share a builder and differ in the PRODUCT, which is the thing to get right:
  repo_kind="driver" — a connector. Built AND RUN HERE: the image is loaded into the engine on the controller and the controller talks to it. A connector is the code that knows how to speak to one platform, and every write to a device goes through one.
  repo_kind="agent"  — a proxy or extension. NEVER RUN HERE: it is exported as an OCI layout on disk, and the built-in registry serves it for a remote proxy to docker pull. Building an agent therefore changes nothing on this host's behaviour; something has to go and pull it.

THIS RETURNS BEFORE THE BUILD FINISHES. Read the log with follow_gesture(kind="image_build", repo_id=..., repo_kind=...) — a failed build explains itself only there. Starting a driver's container after a build is a separate gesture, not part of this one.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var repoID, err = a.Required("repo_id")
				if err != nil {
					return "", err
				}
				var repoKind string
				repoKind, err = a.Required("repo_kind")
				if err != nil {
					return "", err
				}
				if repoKind != "driver" && repoKind != "agent" {
					return "", errors.New(`repo_kind must be "driver" or "agent"`)
				}
				var out, sendErr = send(ctx, c, http.MethodPost,
					"/api/"+repoKind+"-repos/"+esc(repoID)+"/build", map[string]any{})
				if sendErr != nil {
					return "", sendErr
				}
				return "Build started. Read its log with follow_gesture(kind=\"image_build\", " +
					"repo_id=\"" + repoID + "\", repo_kind=\"" + repoKind + "\").\n\n" + out, nil
			},
		},
		{
			Name:       "set_port_admin",
			Title:      "Shut or un-shut ports now",
			Effect:     EffectWrite,
			Idempotent: true,
			Min:        ModeWrite,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device's id.", Req: true},
				{Name: "ports", Type: "object", Desc: `Map of port name to whether it should be ENABLED, e.g. {"12":false,"13":true}. false shuts the port.`, Req: true},
				{Name: "confirm", Type: "boolean", Desc: "Required when any port is being shut: set true to confirm you mean to take those ports off the air."},
				{Name: "force", Type: "boolean", Desc: "Override the connector's refusal to disable a port that carries management. Read the warning in the description before using this."},
			},
			Description: `Enable or disable ports immediately. An ACTION, not an intent: nothing is stored and nothing reconciles it, so a port shut here stays shut until something shuts or opens it again.

Disabling a port takes whatever is behind it off the network, so a map containing any false requires confirm=true. Re-enabling needs no confirmation.

force exists for one case and is dangerous in every other: the connector REFUSES to disable a port that carries the device's own management, because doing so cuts the controller's path to the switch and it cannot be undone from here. force lifts that refusal. Use it only when you know another path to the device exists, and never to make an unexplained refusal go away — the refusal is the connector telling you it can see the port you are standing on.

For a link that needs bouncing rather than cutting, this is the right tool: disable then enable. Do NOT reach for power_cycle_ports to do it, because that reboots the equipment at the far end.`,
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
				var typed = map[string]bool{}
				var shutting []string
				var name string
				var value any
				for name, value = range ports {
					var enabled, ok = value.(bool)
					if !ok {
						return "", errors.New("port " + name +
							": the value must be a boolean saying whether the port is ENABLED")
					}
					typed[name] = enabled
					if !enabled {
						shutting = append(shutting, name)
					}
				}
				if len(shutting) > 0 && !a.Bool("confirm") {
					sortStrings(shutting)
					return "", errors.New("this would SHUT " + strconv.Itoa(len(shutting)) +
						" port(s) on device " + deviceID + " (" + strings.Join(shutting, ", ") +
						"), taking whatever is behind them off the network. Nothing was done. " +
						"Pass confirm=true if that is what you mean.")
				}
				var body = map[string]any{"ports": typed}
				if a.Bool("force") {
					body["force"] = true
				}
				return send(ctx, c, http.MethodPost,
					"/api/devices/"+esc(deviceID)+"/ports/admin", body)
			},
		},
	}
}

// credentialLabel re-reads a credential's label for the confirmation check.
//
// Re-reading is the entire value of the gesture. Comparing against a label the
// caller also supplied would confirm nothing; comparing against the label the
// controller holds RIGHT NOW is what proves the caller looked at the object that
// exists, and it fails closed when the object has been renamed or replaced
// underneath them.
func credentialLabel(ctx context.Context, c *Client, siteID, credID string) (string, error) {
	var creds, err = c.GetJSON(ctx, "/api/sites/"+esc(siteID)+"/credentials")
	if err != nil {
		return "", err
	}
	var list, ok = creds.([]any)
	if !ok {
		return "", errors.New("could not read the credential list for site " + siteID)
	}
	var entry any
	for _, entry = range list {
		var cred, isMap = entry.(map[string]any)
		if !isMap {
			continue
		}
		var id, _ = cred["id"].(string)
		if id != credID {
			continue
		}
		var key string
		for _, key = range []string{"label", "name", "kind"} {
			var label, isStr = cred[key].(string)
			if isStr && label != "" {
				return label, nil
			}
		}
		return "", errors.New("credential " + credID + " has no label to confirm against; " +
			"deploy it from the web interface")
	}
	return "", errors.New("no credential " + credID + " in site " + siteID)
}
