package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"meshcraft/wire/agentv1"
	"meshcraft/wire/mdpv1"
)

// serveContract drives handleContract through a chi router so the {name} URL
// param resolves exactly as it does in production — the handler reads it with
// chi.URLParam, which is empty without the router.
func serveContract(name string) *httptest.ResponseRecorder {
	var s = &Server{}
	var r = chi.NewRouter()
	r.Get("/api/v1/contracts/{name}", s.handleContract)
	var rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/contracts/"+name, nil))
	return rec
}

// TestContractsServedAnonymously: both OpenAPI documents answer 200 as YAML,
// their schema companions as JSON. A contract an author cannot fetch is a
// contract that does not exist for them.
func TestContractsServedAnonymously(t *testing.T) {
	var cases = []struct {
		name string
		ct   string
		must string
	}{
		{"agent", "application/yaml", "Meshcraft Agent Protocol"},
		{"mdp", "application/yaml", "Meshcraft Driver Protocol"},
		{"agent-schemas", "application/json", "EnrollRequest"},
		{"mdp-schemas", "application/json", "ApplyResult"},
	}
	var c struct {
		name string
		ct   string
		must string
	}
	for _, c = range cases {
		var rec = serveContract(c.name)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", c.name, rec.Code)
			continue
		}
		var gotCT = rec.Header().Get("Content-Type")
		if gotCT != c.ct {
			t.Errorf("%s: content-type %q, want %q", c.name, gotCT, c.ct)
		}
		if !strings.Contains(rec.Body.String(), c.must) {
			t.Errorf("%s: body does not mention %q", c.name, c.must)
		}
	}
}

// TestUnknownContractNamesTheKnownOnes: a 404 that lists what could have been
// asked for is the difference between a dead end and a hint.
func TestUnknownContractNamesTheKnownOnes(t *testing.T) {
	var rec = serveContract("nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "agent") || !strings.Contains(rec.Body.String(), "mdp") {
		t.Errorf("404 body does not name the known contracts: %s", rec.Body.String())
	}
}

// TestAgentSchemasCoverEveryDocumentedRef: the agent OpenAPI references
// schemas by name; a $ref to a component the generator does not emit is a
// document that describes a shape nothing serves. The generated schemas.json
// (served as agent-schemas) must contain every component the paths ref.
func TestAgentSchemasCoverEveryDocumentedRef(t *testing.T) {
	var rec = serveContract("agent-schemas")
	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	var uerr = json.Unmarshal(rec.Body.Bytes(), &doc)
	if uerr != nil {
		t.Fatalf("agent schemas are not valid JSON: %v", uerr)
	}
	// The types the OpenAPI's $ref lines name (contracts/agent/v1/openapi.yaml).
	var refd = []string{
		"EnrollRequest", "EnrollResponse", "HeartbeatRequest",
		"HeartbeatResponse", "MigratedRequest", "MetricsRequest",
	}
	var name string
	for _, name = range refd {
		var _, ok = doc.Components.Schemas[name]
		if !ok {
			t.Errorf("agent OpenAPI references %q but the generated schemas omit it", name)
		}
	}
	// And the protocol majors are the ones the contracts announce.
	if agentv1.Protocol != 1 || mdpv1.Protocol != 1 {
		t.Errorf("protocol majors drifted: agent=%d mdp=%d", agentv1.Protocol, mdpv1.Protocol)
	}
}
