// MCP wire protocol: JSON-RPC 2.0 over stdio.
//
// # Why this is hand-written and not a library
//
// The repository builds in containers with nothing installed on the host, and it
// carries eleven direct Go dependencies for a network controller, a proxy, SNMP,
// gNMI and Postgres. The slice of MCP a server needs is `initialize`,
// `tools/list`, `tools/call` and `ping` over newline-delimited JSON — the code
// below, which has no dependency and can be read end to end in one sitting.
// Importing an SDK to obtain it would add a module tree to `go.sum` for every
// build of the controller and the proxy, and would put the product's supply
// chain in exchange for about two hundred lines. That trade is not worth making
// here; if this server ever needs resources, prompts, or sampling, revisit it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// The protocol revisions this server knows how to speak. The newest is offered
// when a client asks for something unrecognised; a client that insists on a
// version it cannot share with us will say so and disconnect, which is the
// spec's intended outcome and better than guessing.
var supportedProtocols = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const latestProtocol = "2025-06-18"

// JSON-RPC 2.0 error codes used here.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// contentBlock is one piece of a tool's answer. Only text is produced: every
// answer this server gives is JSON or prose an agent reads.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolResult is the result of tools/call.
//
// IsError is the protocol's channel for "the tool ran and the attempt failed",
// as distinct from a JSON-RPC error, which means "the call itself was
// malformed". A refusal by the controller is the FORMER: the agent asked a
// well-formed question and got a real answer it must read, so the controller's
// prose lands in Content with IsError set, where the model sees it.
type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

func textResult(s string) toolResult {
	return toolResult{Content: []contentBlock{{Type: "text", Text: s}}}
}

func errorResult(s string) toolResult {
	return toolResult{Content: []contentBlock{{Type: "text", Text: s}}, IsError: true}
}

// Server speaks MCP on one pair of streams.
type Server struct {
	in      io.Reader
	out     io.Writer
	mu      sync.Mutex
	enc     *json.Encoder
	tools   []Tool
	byName  map[string]Tool
	client  *Client
	version string
	// initialised guards against a client calling tools before the handshake.
	// Refusing early is the spec's contract and it also catches a transport
	// that silently reordered messages.
	initialised bool
}

func NewServer(in io.Reader, out io.Writer, c *Client, tools []Tool, version string) *Server {
	var byName = make(map[string]Tool, len(tools))
	var t Tool
	for _, t = range tools {
		byName[t.Name] = t
	}
	return &Server{
		in:      in,
		out:     out,
		enc:     json.NewEncoder(out),
		tools:   tools,
		byName:  byName,
		client:  c,
		version: version,
	}
}

// Serve reads messages until the input stream ends.
//
// A json.Decoder over the stream reads consecutive JSON values regardless of how
// they are separated, so newline-delimited framing is handled without a line
// buffer to size wrongly — a tool call carrying a port map can be long, and a
// scanner with a fixed cap would fail on exactly the largest useful request.
func (s *Server) Serve(ctx context.Context) error {
	var dec = json.NewDecoder(s.in)
	for {
		var req rpcRequest
		var err = dec.Decode(&req)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// A stream we can no longer parse cannot be resynchronised: the
			// decoder's position is unknown. Report and stop, rather than spin.
			s.reply(nil, nil, &rpcError{Code: codeParse, Message: "malformed JSON: " + err.Error()})
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.dispatch(ctx, req)
	}
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) {
	// A notification has no id and must never be answered, not even to complain.
	var isNotification = len(req.ID) == 0

	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "notifications/initialized", "notifications/cancelled":
		// Nothing to do, and nothing to say.
	case "ping":
		if !isNotification {
			s.reply(req.ID, map[string]any{}, nil)
		}
	case "tools/list":
		if isNotification {
			return
		}
		if !s.initialised {
			s.reply(req.ID, nil, &rpcError{Code: codeInvalidRequest, Message: "initialize first"})
			return
		}
		s.reply(req.ID, map[string]any{"tools": s.toolSchemas()}, nil)
	case "tools/call":
		if isNotification {
			return
		}
		if !s.initialised {
			s.reply(req.ID, nil, &rpcError{Code: codeInvalidRequest, Message: "initialize first"})
			return
		}
		s.handleCall(ctx, req)
	default:
		if !isNotification {
			s.reply(req.ID, nil, &rpcError{
				Code:    codeMethodNotFound,
				Message: "this server implements tools only: " + req.Method + " is not supported",
			})
		}
	}
}

func (s *Server) handleInitialize(req rpcRequest) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &params)
	}
	// Echo the client's version when we share it; otherwise offer our newest and
	// let the client decide. Inventing a version we do not implement would move
	// the failure from the handshake, where it is legible, to the first call.
	var agreed = latestProtocol
	var v string
	for _, v = range supportedProtocols {
		if v == params.ProtocolVersion {
			agreed = v
			break
		}
	}
	s.initialised = true
	if len(req.ID) == 0 {
		return
	}
	s.reply(req.ID, map[string]any{
		"protocolVersion": agreed,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo": map[string]any{
			"name":    "meshcraft",
			"title":   "meshcraft network orchestrator",
			"version": s.version,
		},
		"instructions": serverInstructions,
	}, nil)
}

func (s *Server) handleCall(ctx context.Context, req rpcRequest) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	var err = json.Unmarshal(req.Params, &params)
	if err != nil {
		s.reply(req.ID, nil, &rpcError{Code: codeInvalidParams, Message: err.Error()})
		return
	}
	var tool, found = s.byName[params.Name]
	if !found {
		// Not a JSON-RPC error: an unknown tool name is something the model can
		// correct on its next turn, so it belongs in the result where the model
		// reads it. The available names go with it, because the usual cause is a
		// tool that exists in a wider mode than this server was started in.
		s.reply(req.ID, errorResult("no tool named "+params.Name+
			". Tools available in this mode: "+strings.Join(s.toolNames(), ", ")), nil)
		return
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}

	var out, runErr = tool.Run(ctx, s.client, params.Arguments)
	if runErr != nil {
		var apiErr *APIError
		if errors.As(runErr, &apiErr) {
			s.reply(req.ID, errorResult(apiErr.Error()), nil)
			return
		}
		s.reply(req.ID, errorResult(runErr.Error()), nil)
		return
	}
	s.reply(req.ID, textResult(out), nil)
}

func (s *Server) toolNames() []string {
	var names = make([]string, 0, len(s.tools))
	var t Tool
	for _, t = range s.tools {
		names = append(names, t.Name)
	}
	return names
}

func (s *Server) toolSchemas() []map[string]any {
	var out = make([]map[string]any, 0, len(s.tools))
	var t Tool
	for _, t = range s.tools {
		var entry = map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.Schema(),
		}
		if t.Title != "" {
			entry["title"] = t.Title
		}
		// Annotations are the protocol's own vocabulary for what a tool does to
		// the world. A client that gates calls reads these, so they must be
		// honest: ReadOnly is set only where the call cannot write, and
		// Destructive only where an effect cannot be undone from here.
		entry["annotations"] = map[string]any{
			"title":           t.Title,
			"readOnlyHint":    t.Effect == EffectRead,
			"destructiveHint": t.Effect == EffectDestructive,
			"idempotentHint":  t.Idempotent,
			"openWorldHint":   true,
		}
		out = append(out, entry)
	}
	return out
}

func (s *Server) reply(id json.RawMessage, result any, rpcErr *rpcError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == nil {
		id = json.RawMessage("null")
	}
	var err = s.enc.Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
	if err != nil {
		// stdout is the protocol channel; a log line there would corrupt it.
		slog.Error("write response", "err", err)
	}
}
