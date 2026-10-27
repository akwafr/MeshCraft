// Command mdpschema generates the schemas.json beside each hand-written
// OpenAPI document — the JSON Schema components those documents reference — by
// REFLECTING over the wire types. It covers BOTH wire contracts: MDP
// (meshcraft/wire/mdpv1) and the agent lifeline (meshcraft/wire/agentv1).
//
// Generated rather than written, because the Go types are the source of
// truth (each wire package's header says so) and a hand-maintained copy of a
// hundred-odd types is a drift machine: the field someone adds to
// ApplyResult would reach every Go caller and no non-Go author. `make vet`
// regenerates in memory and fails on any difference, so the committed
// documents cannot lag the code.
//
//	go run ./tools/mdpschema            # rewrites every contract's schemas.json
//	go run ./tools/mdpschema -check     # exits 1 if any is stale
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"meshcraft/wire/agentv1"
	"meshcraft/wire/mdpv1"
)

var checkOnly = flag.Bool("check", false, "verify the committed files are current")

// contract is one wire boundary: the package its types come from (for the
// generated header), the directory that holds its openapi.yaml, and the roots
// every path references.
type contract struct {
	pkg   string
	dir   string
	roots []any
}

var contracts = []contract{
	{pkg: "meshcraft/wire/mdpv1", dir: "contracts/mdp/v1", roots: mdpRoots},
	{pkg: "meshcraft/wire/agentv1", dir: "contracts/agent/v1", roots: agentRoots},
}

// agentRoots: every type the agent OpenAPI paths reference. The LLDP/FDB
// report bodies are deliberately absent — they still live in internal/snmppoll
// and the document references them by name, not by schema, until they move to
// the wire.
var agentRoots = []any{
	agentv1.EnrollRequest{}, agentv1.EnrollResponse{},
	agentv1.HeartbeatRequest{}, agentv1.HeartbeatResponse{},
	agentv1.UpgradeReport{}, agentv1.UpgradeOffer{}, agentv1.MigrateOrder{},
	agentv1.MigratedRequest{}, agentv1.MetricsRequest{},
}

// mdpRoots lists every type the MDP OpenAPI paths reference. Walking them pulls
// in everything they embed; a type absent from the output is a type no
// endpoint mentions, which is the correct meaning of absence.
var mdpRoots = []any{
	// Envelope & lifecycle.
	mdpv1.ProtocolInfo{}, mdpv1.SupportsResponse{}, mdpv1.ErrorEnvelope{},
	mdpv1.FingerprintRequest{}, mdpv1.FingerprintResponse{},
	mdpv1.SessionRequest{}, mdpv1.SessionResponse{}, mdpv1.SessionlessRequest{},
	mdpv1.ReadRequest{}, mdpv1.ReadResponse{},
	// Op payloads.
	mdpv1.SetSecretRequest{}, mdpv1.HostnameRequest{}, mdpv1.MgmtVLANRequest{},
	mdpv1.MgmtAddressedResponse{}, mdpv1.MgmtJumpRequest{}, mdpv1.MgmtJumpResponse{},
	mdpv1.StaticOwnerProbeRequest{}, mdpv1.StaticOwnerProbeResponse{},
	mdpv1.RawReadRequest{}, mdpv1.RawReadResponse{}, mdpv1.AccountRequest{},
	mdpv1.LAGsRequest{}, mdpv1.PortNameRequest{}, mdpv1.PortNameResponse{},
	mdpv1.WebUIResponse{},
	// Contract payloads.
	mdpv1.Info{}, mdpv1.Capabilities{}, mdpv1.ResetProcedure{}, mdpv1.Evidence{},
	mdpv1.State{}, mdpv1.PortPlan{}, mdpv1.ApplyResult{}, mdpv1.Identity{},
	mdpv1.OnboardResult{}, mdpv1.Account{}, mdpv1.SNMPIdentity{}, mdpv1.SNMPResult{},
	mdpv1.TrapDestination{}, mdpv1.TrapResult{},
	mdpv1.DHCPSnoopState{}, mdpv1.DHCPSnoopPlan{},
	mdpv1.StormControlState{}, mdpv1.StormControlPlan{}, mdpv1.StormControlCaps{},
	mdpv1.SpanningTreeState{}, mdpv1.SpanningTreePlan{},
	mdpv1.PoEState{}, mdpv1.PoEPlan{},
	mdpv1.PortAdminPlan{}, mdpv1.PortCyclePlan{}, mdpv1.LAGState{},
	mdpv1.LLDPObservation{}, mdpv1.FDBObservation{}, mdpv1.OpticsState{},
	mdpv1.MgmtAddrState{}, mdpv1.MgmtAddrIntent{}, mdpv1.MgmtAddrResult{},
}

type generator struct {
	defs map[string]map[string]any
}

func main() {
	flag.Parse()
	var c contract
	for _, c = range contracts {
		emit(c)
	}
}

// emit generates one contract's schemas.json, or checks it against the file.
func emit(c contract) {
	var g = generator{defs: map[string]map[string]any{}}
	var r any
	for _, r = range c.roots {
		g.schemaOf(reflect.TypeOf(r))
	}

	var names []string
	var name string
	for name = range g.defs {
		names = append(names, name)
	}
	sort.Strings(names)
	var ordered = map[string]any{}
	for _, name = range names {
		ordered[name] = g.defs[name]
	}
	var doc = map[string]any{
		"$comment": "GENERATED from " + c.pkg + " by tools/mdpschema — do not edit; " +
			"the Go types are the source of truth.",
		"components": map[string]any{"schemas": ordered},
	}
	var buf bytes.Buffer
	var enc = json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)

	var outPath = c.dir + "/schemas.json"
	if *checkOnly {
		var current, err = os.ReadFile(outPath)
		if err != nil || !bytes.Equal(current, buf.Bytes()) {
			fmt.Fprintln(os.Stderr, outPath+" is STALE: "+
				"run `go run ./tools/mdpschema` and commit the result")
			os.Exit(1)
		}
		return
	}
	if os.MkdirAll(c.dir, 0o755) != nil {
		os.Exit(1)
	}
	if os.WriteFile(outPath, buf.Bytes(), 0o644) != nil {
		os.Exit(1)
	}
	fmt.Printf("%s: %d schemas\n", outPath, len(names))
}

// schemaOf returns a schema (or a $ref for named struct types, which are
// added to the components).
func (g *generator) schemaOf(t reflect.Type) map[string]any {
	switch t {
	case reflect.TypeOf(time.Time{}):
		return map[string]any{"type": "string", "format": "date-time"}
	case reflect.TypeOf(json.RawMessage{}):
		// A part result is whatever the part's own schema says; the envelope
		// carries it opaquely.
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return g.schemaOf(t.Elem())
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if t.Kind() == reflect.Int64 && t.PkgPath() == "time" {
			// time.Duration marshals as nanoseconds.
			return map[string]any{"type": "integer",
				"description": "nanoseconds (Go time.Duration)"}
		}
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "format": "byte"}
		}
		return map[string]any{"type": "array", "items": g.schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object",
			"additionalProperties": g.schemaOf(t.Elem())}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		var name = t.Name()
		if name == "" {
			return g.structSchema(t)
		}
		var _, seen = g.defs[name]
		if !seen {
			g.defs[name] = nil // reserve before recursing: cycles exist
			g.defs[name] = g.structSchema(t)
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	default:
		return map[string]any{}
	}
}

func (g *generator) structSchema(t reflect.Type) map[string]any {
	var props = map[string]any{}
	var required []string
	var i int
	for i = 0; i < t.NumField(); i++ {
		var f = t.Field(i)
		if !f.IsExported() {
			continue
		}
		var tag = f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		var parts = strings.Split(tag, ",")
		var jsonName = parts[0]
		if jsonName == "" {
			if f.Anonymous {
				// Embedded without a tag: fields flatten in.
				var embedded = g.rawStruct(f.Type)
				var k string
				var v any
				for k, v = range embedded.props {
					props[k] = v
				}
				required = append(required, embedded.required...)
				continue
			}
			jsonName = f.Name
		}
		var omit bool
		var p string
		for _, p = range parts[1:] {
			if p == "omitempty" {
				omit = true
			}
		}
		props[jsonName] = g.schemaOf(f.Type)
		if !omit {
			required = append(required, jsonName)
		}
	}
	var out = map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		sort.Strings(required)
		out["required"] = required
	}
	return out
}

type flat struct {
	props    map[string]any
	required []string
}

func (g *generator) rawStruct(t reflect.Type) flat {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var s = g.structSchema(t)
	var out = flat{props: map[string]any{}}
	var p, _ = s["properties"].(map[string]any)
	out.props = p
	var req, _ = s["required"].([]string)
	out.required = req
	return out
}
