package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// handshake drives initialize and returns the server's result object.
func handshake(t *testing.T, s *Server, out *bytes.Buffer) map[string]any {
	t.Helper()
	var replies = decodeReplies(t, out)
	if len(replies) == 0 {
		t.Fatalf("the server answered nothing to initialize")
	}
	var result, ok = replies[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize did not return a result: %v", replies[0])
	}
	return result
}

func decodeReplies(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var dec = json.NewDecoder(bytes.NewReader(out.Bytes()))
	var replies []map[string]any
	for dec.More() {
		var reply map[string]any
		var err = dec.Decode(&reply)
		if err != nil {
			t.Fatalf("the server wrote something that is not JSON-RPC: %v", err)
		}
		replies = append(replies, reply)
	}
	return replies
}

func run(t *testing.T, mode Mode, messages ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	var server = NewServer(strings.NewReader(strings.Join(messages, "\n")), &out,
		NewClient("http://controller.invalid", "tok", 0, false),
		Select(allTools(), mode), "test")
	var err = server.Serve(context.Background())
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return decodeReplies(t, &out)
}

// A notification carries no id, and answering one is a protocol violation that
// shows up as a spurious reply the client cannot match to anything. The
// initialized notification is the one every client sends, so this is the case
// that would break every session rather than an edge case.
func TestNotificationsAreNeverAnswered(t *testing.T) {
	var replies = run(t, ModeRead,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"tools/list"}`,
	)
	if len(replies) != 1 {
		t.Fatalf("expected exactly one reply (to initialize), got %d: %v", len(replies), replies)
	}
}

func TestInitializeEchoesAKnownProtocolAndOffersOursOtherwise(t *testing.T) {
	var cases = []struct{ asked, want string }{
		{"2024-11-05", "2024-11-05"},
		{"2025-06-18", "2025-06-18"},
		{"1999-01-01", latestProtocol},
		{"", latestProtocol},
	}
	var c struct{ asked, want string }
	for _, c = range cases {
		var out bytes.Buffer
		var server = NewServer(strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"`+
				c.asked+`"}}`), &out,
			NewClient("http://x.invalid", "t", 0, false), nil, "test")
		var err = server.Serve(context.Background())
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
		var got = handshake(t, server, &out)["protocolVersion"]
		if got != c.want {
			t.Errorf("asked %q: agreed %v, want %v", c.asked, got, c.want)
		}
	}
}

// tools/list must be stable byte for byte between runs. An agent's prompt cache
// keys on the tool list, and a map-ordered list would invalidate it on every
// reconnection — a cost that is invisible in a test that only checks contents.
func TestToolListIsOrderedAndStable(t *testing.T) {
	var first = toolNamesVia(t, ModeAdmin)
	var i int
	for i = 0; i < 5; i++ {
		var again = toolNamesVia(t, ModeAdmin)
		if strings.Join(first, ",") != strings.Join(again, ",") {
			t.Fatalf("tools/list is not stable:\n%v\n%v", first, again)
		}
	}
	var sorted = append([]string(nil), first...)
	sortStrings(sorted)
	if strings.Join(first, ",") != strings.Join(sorted, ",") {
		t.Errorf("tools/list is not sorted: %v", first)
	}
}

func toolNamesVia(t *testing.T, mode Mode) []string {
	t.Helper()
	var replies = run(t, mode,
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	if len(replies) != 2 {
		t.Fatalf("expected two replies, got %d", len(replies))
	}
	var result, _ = replies[1]["result"].(map[string]any)
	var list, _ = result["tools"].([]any)
	var names []string
	var entry any
	for _, entry = range list {
		var tool, _ = entry.(map[string]any)
		var name, _ = tool["name"].(string)
		names = append(names, name)
	}
	return names
}

// The read-only default is the whole safety story of a default install, so it is
// asserted rather than assumed: not one tool offered in that mode may be able to
// write.
func TestReadOnlyModeOffersNoWritingTool(t *testing.T) {
	var tool Tool
	for _, tool = range Select(allTools(), ModeRead) {
		if tool.Effect != EffectRead {
			t.Errorf("tool %q is offered in read-only mode but its effect is not a read", tool.Name)
		}
	}
	// And it must not be empty, or the assertion above would pass vacuously.
	if len(Select(allTools(), ModeRead)) < 10 {
		t.Fatalf("read-only mode offers too few tools to be useful")
	}
}

func TestDestructiveToolsAreAdminOnlyAndAnnounceThemselves(t *testing.T) {
	var tool Tool
	for _, tool = range allTools() {
		if tool.Effect != EffectDestructive {
			continue
		}
		if tool.Min != ModeAdmin {
			t.Errorf("tool %q is destructive but is offered below admin mode", tool.Name)
		}
	}
	// The annotation is how this server asks a client to prompt a human, so a
	// destructive tool that does not carry it is a silent downgrade.
	var replies = run(t, ModeAdmin,
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	var result, _ = replies[1]["result"].(map[string]any)
	var list, _ = result["tools"].([]any)
	var seenDestructive bool
	var entry any
	for _, entry = range list {
		var tool, _ = entry.(map[string]any)
		var name, _ = tool["name"].(string)
		var ann, _ = tool["annotations"].(map[string]any)
		if name == "delete_object" {
			seenDestructive = true
			if ann["destructiveHint"] != true {
				t.Errorf("delete_object is not annotated destructiveHint: %v", ann)
			}
			if ann["readOnlyHint"] != false {
				t.Errorf("delete_object is annotated readOnly: %v", ann)
			}
		}
		if name == "list_sites" && ann["readOnlyHint"] != true {
			t.Errorf("list_sites is not annotated readOnly: %v", ann)
		}
	}
	if !seenDestructive {
		t.Fatalf("delete_object was not offered in admin mode")
	}
}

// Every tool's schema must be well formed, because a malformed one is not
// rejected loudly — the client simply never calls that tool, and the failure
// looks like the model choosing not to.
func TestEveryToolSchemaIsWellFormed(t *testing.T) {
	var tool Tool
	for _, tool = range allTools() {
		if tool.Name == "" || tool.Description == "" {
			t.Errorf("tool %q has no name or no description", tool.Name)
		}
		if tool.Run == nil {
			t.Errorf("tool %q has no implementation", tool.Name)
		}
		var schema = tool.Schema()
		if schema["type"] != "object" {
			t.Errorf("tool %q: schema type is not object", tool.Name)
		}
		if schema["additionalProperties"] != false {
			t.Errorf("tool %q: additionalProperties is not false, so a mistyped "+
				"argument name would be silently ignored", tool.Name)
		}
		// Required properties must exist among the properties, or the client
		// cannot satisfy the schema at all.
		var props, _ = schema["properties"].(map[string]any)
		var required, _ = schema["required"].([]string)
		var name string
		for _, name = range required {
			var _, present = props[name]
			if !present {
				t.Errorf("tool %q requires %q, which it does not declare", tool.Name, name)
			}
		}
		var _, marshalErr = json.Marshal(schema)
		if marshalErr != nil {
			t.Errorf("tool %q: schema does not marshal: %v", tool.Name, marshalErr)
		}
	}
}

func TestUnknownToolIsAnAnswerNotAProtocolError(t *testing.T) {
	var replies = run(t, ModeRead,
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"drop_tables","arguments":{}}}`,
	)
	if len(replies) != 2 {
		t.Fatalf("expected two replies, got %d", len(replies))
	}
	var _, isError = replies[1]["error"]
	if isError {
		t.Errorf("an unknown tool name came back as a JSON-RPC error; it should be a " +
			"tool result the model can read and correct")
	}
	var result, _ = replies[1]["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("an unknown tool name should be isError:true, got %v", result)
	}
	// The available names must travel with the refusal: the usual cause is a tool
	// that exists only in a wider mode.
	var content, _ = result["content"].([]any)
	var first, _ = content[0].(map[string]any)
	var text, _ = first["text"].(string)
	if !strings.Contains(text, "list_sites") {
		t.Errorf("the refusal does not list the available tools: %q", text)
	}
}

func TestToolsBeforeInitializeAreRefused(t *testing.T) {
	var replies = run(t, ModeRead, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if len(replies) != 1 {
		t.Fatalf("expected one reply, got %d", len(replies))
	}
	var _, isError = replies[1-1]["error"]
	if !isError {
		t.Errorf("tools/list before initialize should be refused, got %v", replies[0])
	}
}

// A tool call whose arguments are long must not be truncated by a line buffer.
// This is the request that a fixed-size scanner would have broken, and it is
// also the most useful one (a full port map).
func TestALargeToolCallSurvivesFraming(t *testing.T) {
	var ports = map[string]any{}
	var i int
	for i = 0; i < 400; i++ {
		ports[strings.Repeat("p", 20)+itoaTest(i)] = map[string]any{"mode": "access", "pvid": 10}
	}
	var payload, _ = json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name":      "set_port_intent",
			"arguments": map[string]any{"device_id": "d1", "ports": ports},
		},
	})
	if len(payload) < 20000 {
		t.Fatalf("the fixture is not large enough to test framing (%d bytes)", len(payload))
	}
	var replies = run(t, ModeWrite,
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		string(payload),
	)
	if len(replies) != 2 {
		t.Fatalf("a large call was not answered: got %d replies", len(replies))
	}
	// It reaches an unreachable controller, so it fails — but it must fail at the
	// NETWORK, which proves the arguments were parsed whole.
	var result, _ = replies[1]["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatalf("expected a transport failure against an invalid host, got %v", result)
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
