package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Effect says what a tool does to the world. It drives the MCP annotations a
// gating client reads, and it is the axis Mode filters on.
type Effect int

const (
	// EffectRead cannot change anything. Note that two "reads" in this product
	// open a live session on a device (a storm-control read, a web probe); they
	// are still EffectRead because they write nothing, but their descriptions
	// say they cost a session.
	EffectRead Effect = iota
	// EffectWrite changes stored intent, or pushes it onto hardware. Recoverable:
	// the previous intent can be written back.
	EffectWrite
	// EffectDestructive cannot be undone from this API. Deleting a row, rebooting
	// a powered device, forgetting a ledger, cutting a proxy's session.
	EffectDestructive
)

// Mode is how much of the surface a given server process exposes.
//
// The default is ModeRead, and that is a direct reading of ADR-0011: "the
// default must be the least powerful, not the most convenient". A server started
// without being asked for more cannot change anything, and — because tools/list
// is filtered rather than merely refused — an agent talking to it cannot even
// SEE a tool that would. That matters more than a refusal would: a tool the
// model never reads is a tool it never tries, never retries, and never reasons
// about in a plan.
//
// This is defence in depth and nothing more. The real boundary is the token: a
// read-only token (ADR-0011 point 2) makes a write fail at the controller no
// matter what mode this process runs in. Mode is the near guard; the token is
// the one that holds when this process is misconfigured.
type Mode int

const (
	ModeRead Mode = iota
	ModeWrite
	ModeAdmin
)

func parseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "read-only", "read", "readonly":
		return ModeRead, nil
	case "write":
		return ModeWrite, nil
	case "admin":
		return ModeAdmin, nil
	}
	return ModeRead, errors.New("MESHCRAFT_MCP_MODE must be read-only, write or admin (got " + s + ")")
}

func (m Mode) String() string {
	switch m {
	case ModeWrite:
		return "write"
	case ModeAdmin:
		return "admin"
	}
	return "read-only"
}

// Tool is one callable gesture.
type Tool struct {
	Name        string
	Title       string
	Description string
	Effect      Effect
	// Idempotent: calling twice with the same arguments has the same result as
	// calling once. False for anything that appends (an event, a generation) or
	// that acts on the wire.
	Idempotent bool
	// Min is the lowest Mode in which this tool is offered at all.
	Min   Mode
	Props []P
	Run   func(ctx context.Context, c *Client, a Args) (string, error)
}

// Schema renders the tool's JSON Schema.
func (t Tool) Schema() map[string]any {
	var props = map[string]any{}
	var required = []string{}
	var p P
	for _, p = range t.Props {
		var entry = map[string]any{"description": p.Desc}
		if p.Type != "" {
			entry["type"] = p.Type
		}
		if len(p.Enum) > 0 {
			entry["enum"] = p.Enum
		}
		if p.Items != "" {
			entry["items"] = map[string]any{"type": p.Items}
		}
		props[p.Name] = entry
		if p.Req {
			required = append(required, p.Name)
		}
	}
	var out = map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	// additionalProperties:false makes a typo in an argument name an immediate,
	// legible failure instead of a silently ignored field. On a tool that writes
	// to a switch, a silently dropped `force` or `confirm` is the difference
	// between a guarded gesture and an unguarded one.
	out["additionalProperties"] = false
	return out
}

// P is one input property.
type P struct {
	Name  string
	Type  string
	Items string
	Desc  string
	Enum  []string
	Req   bool
}

// Args is a tool call's arguments, with accessors that fail loudly.
//
// Every accessor returns an error naming the argument and what was expected,
// because that error goes straight back to the model as the tool's answer: it
// is the only chance to get the next call right.
type Args map[string]any

func (a Args) String(name string) (string, error) {
	var v, present = a[name]
	if !present || v == nil {
		return "", nil
	}
	var s, ok = v.(string)
	if !ok {
		return "", errors.New(name + " must be a string")
	}
	return s, nil
}

// Required returns a non-empty string argument, or an error.
func (a Args) Required(name string) (string, error) {
	var s, err = a.String(name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(s) == "" {
		return "", errors.New(name + " is required and must not be empty")
	}
	return s, nil
}

func (a Args) Bool(name string) bool {
	var b, _ = a[name].(bool)
	return b
}

func (a Args) Int(name string, fallback int) int {
	var v, present = a[name]
	if !present || v == nil {
		return fallback
	}
	// JSON numbers decode as float64; a client that sends a string is tolerated
	// because several do.
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		var parsed, err = strconv.Atoi(n)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

// Map returns an object-valued argument, preserved as-is.
//
// Preserving it unexamined is deliberate on the routes that take a per-port
// intent: the controller and the connector own that vocabulary, they validate
// it, and they refuse what they do not accept with prose that names the repair.
// A second validator here would be a second model of the same rules, and the day
// the two disagree this one would be wrong while looking authoritative.
func (a Args) Map(name string) (map[string]any, error) {
	var v, present = a[name]
	if !present || v == nil {
		return nil, nil
	}
	var m, ok = v.(map[string]any)
	if !ok {
		return nil, errors.New(name + " must be an object")
	}
	return m, nil
}

func (a Args) Slice(name string) ([]any, error) {
	var v, present = a[name]
	if !present || v == nil {
		return nil, nil
	}
	var s, ok = v.([]any)
	if !ok {
		return nil, errors.New(name + " must be an array")
	}
	return s, nil
}

// StringSlice returns an array-of-strings argument.
func (a Args) StringSlice(name string) ([]string, error) {
	var raw, err = a.Slice(name)
	if err != nil || raw == nil {
		return nil, err
	}
	var out = make([]string, 0, len(raw))
	var item any
	for _, item = range raw {
		var s, ok = item.(string)
		if !ok {
			return nil, errors.New(name + " must be an array of strings")
		}
		out = append(out, s)
	}
	return out, nil
}

// jsonOut renders a value as the text an agent reads.
//
// Indented on purpose: these answers are read by a model, and the token cost of
// indentation is far below the cost of a misread nesting level in a device sheet.
func jsonOut(v any) (string, error) {
	var b, err = json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// relay fetches a path and returns it verbatim, appending any truncation notice.
func relay(ctx context.Context, c *Client, path string) (string, error) {
	var v, err = c.GetJSON(ctx, path)
	if err != nil && !errors.Is(err, errTruncated) {
		return "", err
	}
	var out, marshalErr = jsonOut(v)
	if marshalErr != nil {
		return "", marshalErr
	}
	if errors.Is(err, errTruncated) {
		out += "\n\nNOTE: " + errTruncated.Error()
	}
	return out, nil
}

// Select filters the catalogue to what `mode` exposes, sorted by name so that
// tools/list is byte-identical between runs. That stability is not cosmetic: an
// agent's prompt cache keys on the tool list, and a map-ordered list would
// invalidate it on every reconnection.
func Select(all []Tool, mode Mode) []Tool {
	var out = make([]Tool, 0, len(all))
	var t Tool
	for _, t = range all {
		if t.Min <= mode {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
