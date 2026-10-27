// Command mcp serves the meshcraft API to an agent over the Model Context
// Protocol, on stdio.
//
// # What this is, and what it is not
//
// It is a CLIENT of the controller API carrying ONE user's API token, and it has
// no privilege of its own. ADR-0011 ("a token authenticates, it does not authorise")
// settles the security model and this binary only has to not break it: the token
// proves WHO is calling and never changes WHAT they may do, so every CanSite and
// RequireGlobalAdmin check on the controller applies to these calls exactly as it
// would to that person's browser session. There is no API "for agents" beside the
// API for the web interface — it is the same handler, so if a page can do
// something this can too, and if it cannot, neither can this.
//
// The consequence to keep in mind while reading the rest: this process is not a
// trusted component. Nothing here is a security boundary that the controller
// relies on. The Mode filtering and the confirmation arguments are guards against
// ACCIDENT — a misread tool name, a hallucinated id — and they are useful for
// that. The boundary that actually holds is the token's own scope, which is
// enforced on the other side of the network.
//
// # Running it
//
// Configuration is environment only, and the token in particular is never a
// command-line argument: an argument is visible in the shell history of whoever
// typed it and in `ps` output to every other process on the host, for the whole
// life of the server.
//
//	MESHCRAFT_URL          required. The controller's base URL, e.g. https://meshcraft.example
//	MESHCRAFT_TOKEN        required. An API token, as issued in the user's profile.
//	MESHCRAFT_MCP_MODE     read-only (default) | write | admin
//	MESHCRAFT_TIMEOUT      per-request timeout, Go duration. Default 60s.
//	MESHCRAFT_TLS_SKIP_VERIFY  "1" to accept a self-signed certificate. Lab only.
//
// Registered with a client that speaks stdio MCP:
//
//	{"command": "meshcraft-mcp", "env": {"MESHCRAFT_URL": "...", "MESHCRAFT_TOKEN": "..."}}
//
// The default mode is read-only, and that is a direct reading of ADR-0011: the
// default must be the least powerful, not the most convenient. Pair it with a
// read-only token and the two agree; pair admin mode with a read-only token and
// the token wins, which is the correct direction for the disagreement to resolve.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Version is the product version, injected at link time like the controller's.
var Version = "dev"

// activeMode is the exposure this process was started with. It is read by the
// whoami tool so that an agent can be told what it is talking to rather than
// having to infer it from which tools are missing.
var activeMode = ModeRead

func main() {
	// stdout is the protocol channel: a single stray line on it corrupts the
	// stream and the client's error will not mention this program. Everything
	// diagnostic goes to stderr, always.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	var base = strings.TrimSpace(os.Getenv("MESHCRAFT_URL"))
	if base == "" {
		fatal("MESHCRAFT_URL is not set: this server needs the controller's base URL, " +
			"for example https://meshcraft.example")
	}
	var token = os.Getenv("MESHCRAFT_TOKEN")
	if strings.TrimSpace(token) == "" {
		fatal("MESHCRAFT_TOKEN is not set: this server needs an API token from the " +
			"user's profile. It is read from the environment on purpose and cannot be " +
			"passed as an argument, where it would land in shell history and in ps output.")
	}
	if strings.Contains(base, "://") && !strings.HasPrefix(base, "https://") &&
		!strings.HasPrefix(base, "http://localhost") &&
		!strings.HasPrefix(base, "http://127.0.0.1") {
		// Not fatal — a lab reverse proxy on plain HTTP is a real configuration —
		// but a bearer token on a cleartext link is worth one line on stderr.
		slog.Warn("MESHCRAFT_URL is not https: the API token will cross the network in clear",
			"url", base)
	}

	var mode, modeErr = parseMode(os.Getenv("MESHCRAFT_MCP_MODE"))
	if modeErr != nil {
		fatal(modeErr.Error())
	}
	activeMode = mode

	var timeout = 60 * time.Second
	var raw = strings.TrimSpace(os.Getenv("MESHCRAFT_TIMEOUT"))
	if raw != "" {
		var parsed, err = time.ParseDuration(raw)
		if err != nil {
			fatal("MESHCRAFT_TIMEOUT is not a duration (try 60s, 2m): " + err.Error())
		}
		timeout = parsed
	}
	var skipVerify = os.Getenv("MESHCRAFT_TLS_SKIP_VERIFY") == "1"
	if skipVerify {
		slog.Warn("TLS certificate verification is OFF (MESHCRAFT_TLS_SKIP_VERIFY=1)")
	}

	var client = NewClient(base, token, timeout, skipVerify)
	var tools = Select(allTools(), mode)
	slog.Info("meshcraft mcp ready",
		"controller", base, "mode", mode.String(), "tools", len(tools), "version", Version)

	// A terminating signal must end the loop rather than kill it mid-write: a
	// half-written JSON-RPC response is indistinguishable, to the client, from a
	// server that speaks the protocol badly.
	var ctx, stop = signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var server = NewServer(os.Stdin, os.Stdout, client, tools, Version)
	var err = server.Serve(ctx)
	if err != nil && ctx.Err() == nil {
		fatal(err.Error())
	}
}

func fatal(msg string) {
	slog.Error(msg)
	os.Exit(1)
}

// allTools is the whole catalogue, before Mode filtering.
func allTools() []Tool {
	var out []Tool
	out = append(out, readTools()...)
	out = append(out, writeTools()...)
	out = append(out, longTools()...)
	out = append(out, dangerTools()...)
	return out
}

func modeNote(m Mode) string {
	switch m {
	case ModeWrite:
		return "Reading, configuration writes and applies to hardware are available. " +
			"Deletions and the irreversible gestures are not offered in this mode."
	case ModeAdmin:
		return "Everything this server exposes is available, including deletions and " +
			"the irreversible gestures. Those still require the object's exact current " +
			"name, re-read at the moment of the call."
	}
	return "READ-ONLY: no tool offered by this server can change anything. " +
		"Writes need the server restarted with MESHCRAFT_MCP_MODE=write, which is a " +
		"decision for the person who configured it, not something to work around."
}

// serverInstructions is handed to the client at initialize. It is the one place
// to say what the product IS, since a tool description can only speak for its own
// gesture.
const serverInstructions = `meshcraft is a multi-vendor network orchestrator: it holds an inventory of switches, the configuration they SHOULD carry, and what they are observed to carry, and it reconciles the two.

Start with whoami. It names the identity this server's token belongs to and the role it holds on each site, which bounds everything else — the token carries no powers of its own, only that user's.

Four ideas explain most of what the tools return:

1. INTENT AND OBSERVATION ARE SEPARATE, and both are kept. A device's "ports" is what meshcraft wants; "observed" is what it was last read as carrying. A gap between them is only drift once somebody has read the device back, because drift is discovered by reading, not by the change that caused it. A device never read back is "unknown", and that is the truth rather than a fault.

2. ABSENT MEANS UNMANAGED. A port, a VLAN or a setting meshcraft does not mention is one it does not manage, not one it wants empty. You cannot clear something by omitting it.

3. "UNKNOWN" IS NOT "ABSENT", and the difference governs what you may conclude. "absent" is a certainty and it blocks. "unknown" is ignorance and it must not block. Do not report an ignorance as a fault.

4. EVERY WRITE TO A SWITCH GOES THROUGH A CONNECTOR, which is where the guard-rails live. A connector that refuses a gesture, or declares it unsupported, is telling you something about the platform; it is not an error to retry. A proxy is only a path and a collector, never a write path.

When a call fails, the controller's own sentence is relayed verbatim. This API writes its refusals for a person to read and each one names the repair, so read it rather than retrying — a 422 in particular means the request was understood and declined, and trying again unchanged will decline again.`

// esc escapes one path segment.
//
// Ids in this product are UUIDs and names are not in paths, so this is belt and
// braces rather than a live concern — but a tool argument is attacker-adjacent
// input by construction here (it is written by a model, from text it read
// somewhere), and a segment that contains a slash or a question mark must not be
// able to reshape the request into a different route.
func esc(segment string) string {
	var replacer = strings.NewReplacer(
		"/", "%2F", "?", "%3F", "#", "%23", "&", "%26", " ", "%20",
		"\n", "", "\r", "", "\t", "", "%", "%25",
	)
	return replacer.Replace(segment)
}
