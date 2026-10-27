// Package contracts embeds the wire-contract documents so the controller can
// serve the exact spec it implements. A driver or agent author fetches
// `GET /api/v1/contracts/{name}` and reads what THIS build promises, rather
// than a wiki that drifts from the code.
//
// The documents live at the repository root (contracts/<protocol>/v<major>/)
// because they are consumed by three parties — the controller binary, the
// drivers monorepo (a vendored copy), and human authors — and the root is the
// one place all three already look. This file makes them reachable from Go
// without moving them: go:embed only sees its own subtree, and this package IS
// that subtree.
package contracts

import _ "embed"

//go:embed mdp/v1/openapi.yaml
var mdpOpenAPI []byte

//go:embed mdp/v1/schemas.json
var mdpSchemas []byte

//go:embed agent/v1/openapi.yaml
var agentOpenAPI []byte

//go:embed agent/v1/schemas.json
var agentSchemas []byte

// Document is one served file: its bytes and the content type to serve it as.
type Document struct {
	Bytes       []byte
	ContentType string
}

const yamlType = "application/yaml"
const jsonType = "application/json"

// documents maps a request name to what it serves. The bare protocol name
// serves the human-facing OpenAPI; the "<name>-schemas" form serves the
// generated JSON Schema the OpenAPI references.
var documents = map[string]Document{
	"mdp":           {mdpOpenAPI, yamlType},
	"mdp-schemas":   {mdpSchemas, jsonType},
	"agent":         {agentOpenAPI, yamlType},
	"agent-schemas": {agentSchemas, jsonType},
}

// Get returns the named contract document.
func Get(name string) (Document, bool) {
	var d, ok = documents[name]
	return d, ok
}

// Names lists every served document name, for a not-found message that tells
// the caller what it could have asked for instead.
func Names() []string {
	var out = make([]string, 0, len(documents))
	var name string
	for name = range documents {
		out = append(out, name)
	}
	return out
}
