package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stub is a fake controller. Each handler records what it was asked.
type stub struct {
	t        *testing.T
	server   *httptest.Server
	client   *Client
	requests []string
	routes   map[string]func(w http.ResponseWriter, r *http.Request)
}

func newStub(t *testing.T) *stub {
	t.Helper()
	var s = &stub{t: t, routes: map[string]func(http.ResponseWriter, *http.Request){}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every call must carry the bearer token and never a cookie: ADR-0011
		// point 4, and the reason the token is immune to CSRF.
		var auth = r.Header.Get("Authorization")
		if auth != "Bearer secret-token" {
			s.t.Errorf("%s %s arrived without the bearer token (got %q)",
				r.Method, r.URL.Path, auth)
		}
		if len(r.Cookies()) > 0 {
			s.t.Errorf("%s %s carried a cookie; the token must travel only in the header",
				r.Method, r.URL.Path)
		}
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		var handler, found = s.routes[r.Method+" "+r.URL.Path]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(s.server.Close)
	s.client = NewClient(s.server.URL, "secret-token", 10*time.Second, false)
	return s
}

func (s *stub) on(method, path string, status int, body string) {
	s.routes[method+" "+path] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func (s *stub) called(method, path string) bool {
	var req string
	for _, req = range s.requests {
		if req == method+" "+path {
			return true
		}
	}
	return false
}

func toolNamed(t *testing.T, name string) Tool {
	t.Helper()
	var tool Tool
	for _, tool = range allTools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool named %q", name)
	return Tool{}
}

// The controller writes its refusals for a person to read and each one names the
// repair. Relaying that sentence verbatim is the single most useful thing this
// server does on a failure, so it is tested on the exact shape the controller
// produces and on the two other shapes that exist in the product.
func TestTheControllersOwnSentenceIsRelayedVerbatim(t *testing.T) {
	var sentence = "this credential holds a DISTINCT secret per device: there is no " +
		"single generation to deploy fleet-wide. Rotate and push these one device at a time"
	var cases = []struct {
		name string
		body string
	}{
		{"respondErr shape", `{"error":` + jsonString(sentence) + `}`},
		{"MDP envelope shape", `{"error":{"code":"refused","because":` + jsonString(sentence) + `}}`},
		{"payload-level because", `{"because":` + jsonString(sentence) + `}`},
	}
	var c struct {
		name string
		body string
	}
	for _, c = range cases {
		var got = becauseFrom([]byte(c.body))
		if got != sentence {
			t.Errorf("%s: relayed %q, want the controller's sentence verbatim", c.name, got)
		}
	}
	// A body we cannot model is still evidence and must not be replaced by a
	// generic message.
	var raw = becauseFrom([]byte("upstream connect error"))
	if raw != "upstream connect error" {
		t.Errorf("an unmodellable body was discarded: %q", raw)
	}
}

func TestAPIErrorCarriesStatusMeaningAndProse(t *testing.T) {
	var s = newStub(t)
	s.on(http.MethodGet, "/api/sites", http.StatusUnprocessableEntity,
		`{"error":"the collector does not cover this site"}`)
	var _, err = s.client.GetJSON(context.Background(), "/api/sites")
	if err == nil {
		t.Fatalf("expected a refusal")
	}
	var text = err.Error()
	if !strings.Contains(text, "the collector does not cover this site") {
		t.Errorf("the controller's prose is missing: %q", text)
	}
	if !strings.Contains(text, "422") {
		t.Errorf("the status is missing: %q", text)
	}
	// A 422 means "understood and refused"; saying so is what stops a pointless retry.
	if !strings.Contains(text, "REFUSED") {
		t.Errorf("the meaning of 422 is not spelled out: %q", text)
	}
}

// api_get is the escape hatch, so what it REFUSES is load-bearing: it must never
// be able to send this process's token somewhere nobody chose, and it must never
// be a second door onto the gestures the named tools gate.
func TestAPIGetRefusesWhatIsNotAReadOnThisController(t *testing.T) {
	var cases = []struct {
		path string
		why  string
	}{
		{"https://evil.example/api/sites", "an absolute URL would send the token to another host"},
		{"//evil.example/api/sites", "a protocol-relative URL is still another host"},
		{"/etc/passwd", "not an /api path"},
		{"/api/../../etc/passwd", "path traversal"},
		{"/api/devices/x/console/ws", "a shell on a switch"},
		{"/api/auth/socks-token", "minting a credential"},
	}
	var tool = toolNamed(t, "api_get")
	var c struct {
		path string
		why  string
	}
	for _, c = range cases {
		var _, err = tool.Run(context.Background(), nil, Args{"path": c.path})
		if err == nil {
			t.Errorf("api_get accepted %q (%s)", c.path, c.why)
		}
	}
	// And it must still accept an ordinary read, or the guard is just a wall.
	var s = newStub(t)
	s.on(http.MethodGet, "/api/sites/s1/mgmt-plan", http.StatusOK, `{"vlan_id":10}`)
	var out, err = tool.Run(context.Background(), s.client, Args{"path": "/api/sites/s1/mgmt-plan"})
	if err != nil {
		t.Fatalf("api_get refused a legitimate read: %v", err)
	}
	if !strings.Contains(out, "10") {
		t.Errorf("api_get did not relay the body: %q", out)
	}
}

// The generic write door must not be a way around the named gates. This is the
// hole a naive escape hatch leaves, so it is asserted directly.
func TestAPICallCannotDeleteOrReachTheForbiddenGestures(t *testing.T) {
	var tool = toolNamed(t, "api_call")
	var s = newStub(t)

	var _, err = tool.Run(context.Background(), s.client,
		Args{"method": "DELETE", "path": "/api/sites/s1"})
	if err == nil {
		t.Fatalf("api_call performed a DELETE")
	}
	if !strings.Contains(err.Error(), "delete_object") {
		t.Errorf("the refusal does not point at the tool that does deletions: %v", err)
	}

	var forbiddenPaths = []string{
		"/api/devices/d1/mgmt-migrate/commit",
		"/api/devices/d1/mgmt-addressing/jump",
		"/api/dashboards/x/share",
		"/api/users/u1",
		"/api/users/u1/roles",
		"/api/sites/s1/credentials/c1/reveal",
	}
	var path string
	for _, path = range forbiddenPaths {
		_, err = tool.Run(context.Background(), s.client,
			Args{"method": "POST", "path": path})
		if err == nil {
			t.Errorf("api_call accepted the forbidden path %q", path)
		}
	}
	if len(s.requests) != 0 {
		t.Errorf("a refused api_call still reached the controller: %v", s.requests)
	}
}

// A deletion must fail closed on anything other than the object's real, current
// name. These are the accidents the gate exists for: a stale id, a renamed
// object, a confirmation echoed back from the caller's own arguments.
func TestDeleteRefusesUnlessTheNameMatchesWhatTheControllerHoldsNow(t *testing.T) {
	var s = newStub(t)
	s.on(http.MethodGet, "/api/sites", http.StatusOK,
		`[{"id":"s1","name":"Paris HQ"},{"id":"s2","name":"Lyon"}]`)
	s.on(http.MethodDelete, "/api/sites/s1", http.StatusOK, `{"deleted":true}`)

	var tool = toolNamed(t, "delete_object")

	// Wrong name: the object exists, but this is not the one the caller looked at.
	var _, err = tool.Run(context.Background(), s.client,
		Args{"kind": "site", "id": "s1", "confirm_name": "Lyon"})
	if err == nil {
		t.Fatalf("the deletion went through with the wrong name")
	}
	if !strings.Contains(err.Error(), "Paris HQ") {
		t.Errorf("the refusal does not say what the object is actually called: %v", err)
	}
	if s.called(http.MethodDelete, "/api/sites/s1") {
		t.Fatalf("the controller was asked to delete despite the name mismatch")
	}

	// No name at all.
	_, err = tool.Run(context.Background(), s.client, Args{"kind": "site", "id": "s1"})
	if err == nil {
		t.Errorf("the deletion went through with no confirmation")
	}

	// An id that does not exist must refuse rather than fall through to a DELETE
	// the controller would have to reject.
	_, err = tool.Run(context.Background(), s.client,
		Args{"kind": "site", "id": "ghost", "confirm_name": "Paris HQ"})
	if err == nil {
		t.Errorf("the deletion went through on an unknown id")
	}
	if s.called(http.MethodDelete, "/api/sites/ghost") {
		t.Errorf("an unknown id still reached the controller as a DELETE")
	}

	// And the right name works, or the gate is just a wall.
	var out string
	out, err = tool.Run(context.Background(), s.client,
		Args{"kind": "site", "id": "s1", "confirm_name": "Paris HQ"})
	if err != nil {
		t.Fatalf("the deletion was refused with the correct name: %v", err)
	}
	if !s.called(http.MethodDelete, "/api/sites/s1") {
		t.Fatalf("the deletion did not reach the controller")
	}
	if !strings.Contains(out, "cannot be undone") {
		t.Errorf("the result does not say the deletion is irreversible: %q", out)
	}
}

func TestDeleteRefusesKindsThatAreNotExposed(t *testing.T) {
	var tool = toolNamed(t, "delete_object")
	var kind string
	for _, kind = range []string{"user", "role", "session", "api_token"} {
		var _, err = tool.Run(context.Background(), nil,
			Args{"kind": kind, "id": "x", "confirm_name": "x"})
		if err == nil {
			t.Errorf("delete_object accepted kind %q", kind)
		}
	}
}

// A fleet-wide secret push must not start because a flag was forgotten. The
// product itself makes the rehearsal the default for this reason, and this
// server must not undo that.
func TestCredentialDeployRehearsesByDefaultAndNeedsBothConfirmations(t *testing.T) {
	var s = newStub(t)
	s.on(http.MethodGet, "/api/sites/s1/credentials", http.StatusOK,
		`[{"id":"c1","label":"admin core","kind":"admin"}]`)
	var sent []map[string]any
	s.routes[http.MethodPost+" /api/sites/s1/credentials/c1/deploy"] =
		func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			sent = append(sent, body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"running":true}`))
		}
	var tool = toolNamed(t, "deploy_credential")
	var base = Args{"site_id": "s1", "credential_id": "c1", "generation": float64(3)}

	// Default: a rehearsal, and it must say so loudly.
	var out, err = tool.Run(context.Background(), s.client, copyArgs(base))
	if err != nil {
		t.Fatalf("the rehearsal was refused: %v", err)
	}
	if len(sent) != 1 || sent[0]["write"] != false {
		t.Fatalf("the default run was not a rehearsal: %v", sent)
	}
	if !strings.Contains(out, "REHEARSAL") {
		t.Errorf("the rehearsal does not announce itself: %q", out)
	}
	// And it must hand back the means to follow it.
	if !strings.Contains(out, "follow_gesture") {
		t.Errorf("an asynchronous run did not say how to follow it: %q", out)
	}

	// write=true with no confirmation must not reach the controller.
	var withWrite = copyArgs(base)
	withWrite["write"] = true
	_, err = tool.Run(context.Background(), s.client, withWrite)
	if err == nil {
		t.Fatalf("a fleet-wide write started with no confirmation")
	}
	if len(sent) != 1 {
		t.Fatalf("the unconfirmed write reached the controller: %v", sent)
	}

	// write=true with the WRONG label must not reach the controller either.
	withWrite["confirm"] = "admin edge"
	_, err = tool.Run(context.Background(), s.client, withWrite)
	if err == nil {
		t.Fatalf("a fleet-wide write started on a mismatched label")
	}
	if len(sent) != 1 {
		t.Fatalf("the mismatched write reached the controller: %v", sent)
	}

	// The real label works, and the banner changes.
	withWrite["confirm"] = "admin core"
	out, err = tool.Run(context.Background(), s.client, withWrite)
	if err != nil {
		t.Fatalf("the confirmed write was refused: %v", err)
	}
	if len(sent) != 2 || sent[1]["write"] != true {
		t.Fatalf("the confirmed write did not ask for a write: %v", sent)
	}
	if !strings.Contains(out, "WRITING STARTED") {
		t.Errorf("a real write is not announced differently from a rehearsal: %q", out)
	}

	// A missing generation is refused before anything else: the product demands
	// the echo precisely because a rotation between reading and pressing is normal.
	var noGen = Args{"site_id": "s1", "credential_id": "c1"}
	_, err = tool.Run(context.Background(), s.client, noGen)
	if err == nil {
		t.Errorf("a deploy started without naming the generation")
	}
}

// The preview must be the default here too, for the same reason: it is the only
// way to see a masked rule before it has already been written.
func TestSwitchProfileAppliesOnlyWhenAsked(t *testing.T) {
	var s = newStub(t)
	s.on(http.MethodPost, "/api/devices/d1/switch-profile/preview", http.StatusOK, `{"rules":[]}`)
	s.on(http.MethodPost, "/api/devices/d1/switch-profile/apply", http.StatusOK, `{"applied":4}`)
	var tool = toolNamed(t, "apply_switch_profile")

	var out, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "profile_id": "p1"})
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if !s.called(http.MethodPost, "/api/devices/d1/switch-profile/preview") {
		t.Errorf("the default call did not go to the preview route: %v", s.requests)
	}
	if !strings.Contains(out, "PREVIEW ONLY") {
		t.Errorf("the preview does not say nothing was written: %q", out)
	}

	_, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "profile_id": "p1", "dry_run": false})
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !s.called(http.MethodPost, "/api/devices/d1/switch-profile/apply") {
		t.Errorf("dry_run=false did not apply: %v", s.requests)
	}

	// Neither a saved profile nor an inline spec is an evaluable request, and
	// it must be refused here rather than reaching a handler that would silently
	// evaluate an empty rule set.
	_, err = tool.Run(context.Background(), s.client, Args{"device_id": "d1"})
	if err == nil {
		t.Errorf("a switch-profile call with no profile_id and no spec was accepted")
	}
}

// The body key names are the one thing in this server that cannot be verified
// from inside it: send the wrong one and the controller decodes a zero value,
// which on a PUT means "set this to empty" rather than "ignore me". The address
// domain is the sharp case — the wrong key would DETACH the device's domain
// while reporting success — so the keys the tools emit are pinned here against
// the names the handlers actually declare.
func TestWriteToolsEmitTheBodyKeysTheHandlersDeclare(t *testing.T) {
	var s = newStub(t)
	var bodies = map[string]map[string]any{}
	var record = func(path string) {
		s.routes[http.MethodPut+" "+path] = func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies[path] = body
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}
	record("/api/devices/d1/address-domain")
	record("/api/devices/d1/mgmt-addressing")
	record("/api/devices/d1/collector")

	var tool = toolNamed(t, "set_device_attachment")
	var _, err = tool.Run(context.Background(), s.client, Args{
		"device_id":          "d1",
		"address_domain_id":  "dom-1",
		"collector_proxy_id": "px-1",
		"addressing": map[string]any{
			"mode": "static", "static_ip": "10.0.0.5/24", "static_gateway": "10.0.0.1",
		},
	})
	if err != nil {
		t.Fatalf("set_device_attachment failed: %v", err)
	}
	// internal/api/conformity.go declares `address_domain_id`.
	if bodies["/api/devices/d1/address-domain"]["address_domain_id"] != "dom-1" {
		t.Errorf("the address domain is not sent under address_domain_id: %v",
			bodies["/api/devices/d1/address-domain"])
	}
	// internal/api/api.go declares `proxy_id`.
	if bodies["/api/devices/d1/collector"]["proxy_id"] != "px-1" {
		t.Errorf("the collector is not sent under proxy_id: %v",
			bodies["/api/devices/d1/collector"])
	}
	// internal/api/deviceaddr.go declares mode / static_ip / static_gateway.
	var addr = bodies["/api/devices/d1/mgmt-addressing"]
	if addr["mode"] != "static" || addr["static_ip"] != "10.0.0.5/24" ||
		addr["static_gateway"] != "10.0.0.1" {
		t.Errorf("the addressing keys do not match deviceAddrInput: %v", addr)
	}
}

// Adoption takes config_mode, a string, and not a boolean. A boolean would have
// decoded as the zero value, which is "push" — the mode that makes meshcraft the
// source of truth and starts a production device blank in the model.
func TestAdoptSendsConfigModeAsTheStringTheHandlerDeclares(t *testing.T) {
	var s = newStub(t)
	var body map[string]any
	s.routes[http.MethodPost+" /api/discovery/candidates/c1/adopt"] =
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"d1"}`))
		}
	var tool = toolNamed(t, "adopt_device")
	var _, err = tool.Run(context.Background(), s.client,
		Args{"candidate_id": "c1", "site_id": "s1", "config_mode": "import"})
	if err != nil {
		t.Fatalf("adopt failed: %v", err)
	}
	if body["config_mode"] != "import" {
		t.Errorf("config_mode was not sent as the handler declares it: %v", body)
	}
	if body["site_id"] != "s1" {
		t.Errorf("site_id missing, yet choosing the site is the whole gesture: %v", body)
	}
	// A value outside the closed set must be refused rather than forwarded.
	_, err = tool.Run(context.Background(), s.client,
		Args{"candidate_id": "c1", "site_id": "s1", "config_mode": "keep"})
	if err == nil {
		t.Errorf("an invalid config_mode was forwarded to the controller")
	}
}

// Relocation submits under `ip`. Under any other key the handler sees an empty
// string and answers "ip required", so the tool would be inert.
func TestRelocateSubmitsUnderTheIPKey(t *testing.T) {
	var s = newStub(t)
	var body map[string]any
	s.routes[http.MethodPost+" /api/devices/d1/relocate/address"] =
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"state":"searching"}`))
		}
	var tool = toolNamed(t, "relocate_device")
	var out, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "address": "192.0.2.42"})
	if err != nil {
		t.Fatalf("relocate failed: %v", err)
	}
	if body["ip"] != "192.0.2.42" {
		t.Errorf("the address was not sent under ip: %v", body)
	}
	if !strings.Contains(out, "follow_gesture") {
		t.Errorf("an asynchronous search did not say how to follow it: %q", out)
	}
}

// Shutting a port takes whatever is behind it off the air, so an unconfirmed
// shut must not reach the device — while re-enabling needs no ceremony.
func TestShuttingPortsNeedsConfirmationButEnablingDoesNot(t *testing.T) {
	var s = newStub(t)
	s.on(http.MethodPost, "/api/devices/d1/ports/admin", http.StatusOK, `{"ok":true}`)
	var tool = toolNamed(t, "set_port_admin")

	var _, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "ports": map[string]any{"12": false}})
	if err == nil {
		t.Fatalf("an unconfirmed shut reached the device")
	}
	if !strings.Contains(err.Error(), "12") {
		t.Errorf("the refusal does not name the ports it would have shut: %v", err)
	}
	if len(s.requests) != 0 {
		t.Fatalf("the unconfirmed shut was sent: %v", s.requests)
	}

	// Enabling only: no confirmation needed.
	_, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "ports": map[string]any{"12": true}})
	if err != nil {
		t.Fatalf("re-enabling a port was refused: %v", err)
	}

	// Confirmed shut goes through.
	_, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "ports": map[string]any{"12": false}, "confirm": true})
	if err != nil {
		t.Fatalf("a confirmed shut was refused: %v", err)
	}

	// A non-boolean value must be rejected rather than coerced: "off" silently
	// read as false would be the worst possible reading.
	_, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "ports": map[string]any{"12": "off"}, "confirm": true})
	if err == nil {
		t.Errorf("a non-boolean port value was accepted")
	}
}

func TestPowerCycleNeedsExplicitPortsAndConfirmation(t *testing.T) {
	var s = newStub(t)
	s.on(http.MethodPost, "/api/devices/d1/ports/power-cycle", http.StatusOK, `{"ok":true}`)
	var tool = toolNamed(t, "power_cycle_ports")

	var _, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "ports": []any{"5"}})
	if err == nil {
		t.Fatalf("an unconfirmed power cycle reached the device")
	}
	// The refusal must point at the gentler tool, because wanting to bounce a
	// link is the usual reason somebody arrives here by mistake.
	if !strings.Contains(err.Error(), "set_port_admin") {
		t.Errorf("the refusal does not offer the non-rebooting alternative: %v", err)
	}

	_, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "ports": []any{}, "confirm": true})
	if err == nil {
		t.Errorf("a power cycle with no ports was accepted")
	}
	if len(s.requests) != 0 {
		t.Fatalf("a refused power cycle was sent: %v", s.requests)
	}
}

// get_device exists because there is no single-device route, and the reason is
// that the conformity verdict rides on the LIST. If this ever starts fetching a
// per-device route instead, the verdicts would silently read as absent.
func TestGetDeviceProjectsOutOfTheListSoVerdictsSurvive(t *testing.T) {
	var s = newStub(t)
	s.on(http.MethodGet, "/api/sites/s1/devices", http.StatusOK,
		`[{"id":"d1","name":"core01","hostname":{"state":"unknown"}},{"id":"d2","name":"edge"}]`)
	s.on(http.MethodGet, "/api/devices/d1/tags", http.StatusOK, `["pwpolicy-len15"]`)
	var tool = toolNamed(t, "get_device")

	var out, err = tool.Run(context.Background(), s.client,
		Args{"device_id": "d1", "site_id": "s1"})
	if err != nil {
		t.Fatalf("get_device failed: %v", err)
	}
	if !strings.Contains(out, "core01") {
		t.Errorf("the device is missing from the answer: %q", out)
	}
	if strings.Contains(out, "edge") {
		t.Errorf("get_device returned the whole site instead of one device: %q", out)
	}
	// The verdict that only the list carries must still be there.
	if !strings.Contains(out, "unknown") {
		t.Errorf("the conformity verdict was dropped: %q", out)
	}
	if !strings.Contains(out, "pwpolicy-len15") {
		t.Errorf("the tags were not attached: %q", out)
	}
}

// Without a site_id the device has to be searched for, and the cost must be
// reported rather than hidden — otherwise nobody learns to pass site_id.
func TestGetDeviceSearchesSitesAndSaysSo(t *testing.T) {
	var s = newStub(t)
	s.on(http.MethodGet, "/api/sites", http.StatusOK, `[{"id":"s1"},{"id":"s2"}]`)
	s.on(http.MethodGet, "/api/sites/s1/devices", http.StatusOK, `[]`)
	s.on(http.MethodGet, "/api/sites/s2/devices", http.StatusOK, `[{"id":"d9","name":"found"}]`)
	s.on(http.MethodGet, "/api/devices/d9/tags", http.StatusOK, `[]`)

	var tool = toolNamed(t, "get_device")
	var out, err = tool.Run(context.Background(), s.client, Args{"device_id": "d9"})
	if err != nil {
		t.Fatalf("the search failed: %v", err)
	}
	if !strings.Contains(out, "found") {
		t.Errorf("the device was not found: %q", out)
	}
	if !strings.Contains(out, "searched_sites") {
		t.Errorf("the search cost is not reported: %q", out)
	}
}

// A composite read must not turn one dead sub-request into a clean-looking
// answer: a summary silently missing its pipelines reads as "nothing is stuck".
func TestSiteHealthReportsWhatItCouldNotRead(t *testing.T) {
	var s = newStub(t)
	s.on(http.MethodGet, "/api/sites/s1/devices", http.StatusOK, `[{"id":"d1"}]`)
	s.on(http.MethodGet, "/api/sites/s1/pipelines", http.StatusServiceUnavailable,
		`{"error":"database unavailable"}`)
	s.on(http.MethodGet, "/api/sites/s1/events", http.StatusOK, `[]`)

	var tool = toolNamed(t, "site_health")
	var out, err = tool.Run(context.Background(), s.client, Args{"site_id": "s1"})
	if err != nil {
		t.Fatalf("site_health failed entirely on one dead sub-request: %v", err)
	}
	if !strings.Contains(out, "could_not_read") {
		t.Errorf("a failed sub-request was silently dropped: %q", out)
	}
	if !strings.Contains(out, "database unavailable") {
		t.Errorf("the reason the sub-request failed was lost: %q", out)
	}
}

// Path segments come from a model, so a crafted id must not be able to reshape
// the request into a different route.
func TestPathSegmentsCannotEscapeTheirPosition(t *testing.T) {
	var got = esc("../../api/users")
	if strings.Contains(got, "/") {
		t.Errorf("esc left a slash in a path segment: %q", got)
	}
	got = esc("x?admin=1")
	if strings.Contains(got, "?") {
		t.Errorf("esc left a query separator in a path segment: %q", got)
	}
	if esc("plain-uuid-1234") != "plain-uuid-1234" {
		t.Errorf("esc mangled an ordinary id")
	}
}

func TestModeParsingRejectsTypos(t *testing.T) {
	var mode, err = parseMode("")
	if err != nil || mode != ModeRead {
		t.Errorf("the empty default must be read-only, got %v %v", mode, err)
	}
	mode, err = parseMode("admin")
	if err != nil || mode != ModeAdmin {
		t.Errorf("admin did not parse: %v %v", mode, err)
	}
	// A typo must not silently fall back to a MORE permissive mode, nor to a
	// less permissive one that looks like it worked.
	_, err = parseMode("wrtie")
	if err == nil {
		t.Errorf("a misspelled mode was accepted")
	}
}

func copyArgs(a Args) Args {
	var out = Args{}
	var k string
	var v any
	for k, v = range a {
		out[k] = v
	}
	return out
}

func jsonString(s string) string {
	var b, _ = json.Marshal(s)
	return string(b)
}
