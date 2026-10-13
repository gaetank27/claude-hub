// Command tools-mcp is a stdio MCP server exposing the hub's configured external
// integrations (Proxmox, Telegram, Email, …) as tools to a Claude Code session.
// Like rag-mcp it is a thin bridge: it discovers the available actions and invokes
// them against the backend's loopback tools endpoints, so credentials and
// connector logic stay in the backend. The tool list is dynamic — it reflects the
// currently-enabled tools at the moment the session calls tools/list.
//
// Env (set by the hub in the mcp-config file):
//
//	TOOLS_BASE_URL  loopback base, e.g. http://127.0.0.1:8788
//	TOOLS_TOKEN     shared token sent as X-Rag-Token
//	TOOLS_PROJECT_ID  calling project, sent as X-Hub-Project so the backend applies its tool allow-list
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const protocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for {
		line, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			handle(line, out)
			out.Flush()
		}
		if err != nil {
			return
		}
	}
}

func handle(line []byte, out io.Writer) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return
	}
	notification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		reply(out, req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "claude-hub-tools", "version": "1.0.0"},
		})
	case "tools/list":
		specs, err := listTools()
		if err != nil {
			replyErr(out, req.ID, -32603, err.Error())
			return
		}
		reply(out, req.ID, map[string]any{"tools": specs})
	case "tools/call":
		if notification {
			return
		}
		replyToolCall(out, req)
	case "ping":
		reply(out, req.ID, map[string]any{})
	default:
		if notification {
			return
		}
		replyErr(out, req.ID, -32601, "method not found: "+req.Method)
	}
}

type argSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Items       string `json:"items"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

type toolSpec struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Args        []argSpec `json:"args"`
}

// listTools fetches the enabled connectors' actions and converts them to MCP tool
// specs (with a JSON-Schema inputSchema built from the action args).
func listTools() ([]any, error) {
	body, err := call(http.MethodGet, base()+"/tools/list", nil)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Tools []toolSpec `json:"tools"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]any, 0, len(parsed.Tools))
	for _, t := range parsed.Tools {
		props := map[string]any{}
		var required []any
		for _, a := range t.Args {
			prop := map[string]any{"type": jsonType(a.Type), "description": a.Description}
			if a.Type == "array" {
				items := a.Items
				if items == "" {
					items = "string"
				}
				prop["items"] = map[string]any{"type": jsonType(items)}
			}
			props[a.Name] = prop
			if a.Required {
				required = append(required, a.Name)
			}
		}
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		out = append(out, map[string]any{
			"name": t.Name, "description": t.Description, "inputSchema": schema,
		})
	}
	return out, nil
}

func replyToolCall(out io.Writer, req rpcRequest) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &params)
	body, _ := json.Marshal(map[string]any{"action": params.Name, "args": params.Arguments})
	resp, err := call(http.MethodPost, base()+"/tools/call", body)
	if err != nil {
		reply(out, req.ID, toolError(params.Name+" failed: "+err.Error()))
		return
	}
	var parsed struct {
		OK     bool   `json:"ok"`
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	json.Unmarshal(resp, &parsed)
	if !parsed.OK {
		reply(out, req.ID, toolError(params.Name+": "+parsed.Error))
		return
	}
	text := parsed.Result
	if text == "" {
		text = "OK"
	}
	reply(out, req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
}

func toolError(msg string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": msg}}, "isError": true}
}

func jsonType(t string) string {
	switch t {
	case "integer", "number", "boolean", "array":
		return t
	default:
		return "string"
	}
}

func base() string { return os.Getenv("TOOLS_BASE_URL") }

func call(method, url string, body []byte) ([]byte, error) {
	if base() == "" {
		return nil, fmt.Errorf("TOOLS_BASE_URL not set")
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if pid := os.Getenv("TOOLS_PROJECT_ID"); pid != "" {
		req.Header.Set("X-Hub-Project", pid)
	}
	if tok := os.Getenv("TOOLS_TOKEN"); tok != "" {
		req.Header.Set("X-Rag-Token", tok)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return out, nil
}

func reply(out io.Writer, id json.RawMessage, result any) {
	writeJSON(out, rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func replyErr(out io.Writer, id json.RawMessage, code int, msg string) {
	writeJSON(out, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func writeJSON(out io.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	out.Write(b)
	out.Write([]byte{'\n'})
}
