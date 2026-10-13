// Command ask-mcp is a stdio MCP server exposing a single blocking ask_user tool
// to a Claude Code session. The hub injects it because the built-in
// AskUserQuestion tool is auto-denied in headless stream-json mode (no
// interactive answer channel): the CLI returns an errored tool_result instantly
// and the model continues as if the user declined. ask_user instead calls the
// hub's loopback /ask endpoint, which BLOCKS until the user answers in the UI, so
// the session genuinely waits for a human.
//
// Env (set by the hub in the mcp-config file):
//
//	ASK_BASE_URL    loopback base, e.g. http://127.0.0.1:8788
//	ASK_TOKEN       shared token sent as X-Rag-Token
//	ASK_SESSION_ID  the session this server belongs to (keys the pending question)
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

const protocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// askUserTool mirrors the built-in AskUserQuestion input shape so the hub UI can
// render the questions/options without translation.
var askUserTool = map[string]any{
	"name": "ask_user",
	"description": "Ask the user one or more multiple-choice questions and BLOCK until they " +
		"answer in the UI. Use this whenever you need a decision or a choice from the user; " +
		"the built-in AskUserQuestion tool is disabled in this environment.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question":    map[string]any{"type": "string", "description": "The question to ask."},
						"header":      map[string]any{"type": "string", "description": "Short label for the question."},
						"multiSelect": map[string]any{"type": "boolean", "description": "Allow selecting multiple options."},
						"options": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"label":       map[string]any{"type": "string"},
									"description": map[string]any{"type": "string"},
								},
								"required": []any{"label"},
							},
						},
					},
					"required": []any{"question", "options"},
				},
			},
		},
		"required": []any{"questions"},
	},
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
			"serverInfo":      map[string]any{"name": "claude-hub-ask", "version": "1.0.0"},
		})
	case "tools/list":
		reply(out, req.ID, map[string]any{"tools": []any{askUserTool}})
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

// replyToolCall blocks on the hub's /ask endpoint until the user answers, then
// returns their answer as the tool_result text.
func replyToolCall(out io.Writer, req rpcRequest) {
	answer, err := ask()
	if err != nil {
		reply(out, req.ID, toolError("ask_user failed: "+err.Error()))
		return
	}
	reply(out, req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": answer}}})
}

func toolError(msg string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": msg}}, "isError": true}
}

// ask POSTs to the loopback /ask endpoint and blocks (no client timeout) until
// the backend returns the user's answer or the connection drops.
func ask() (string, error) {
	base := os.Getenv("ASK_BASE_URL")
	if base == "" {
		return "", fmt.Errorf("ASK_BASE_URL not set")
	}
	body, _ := json.Marshal(map[string]any{"sessionId": os.Getenv("ASK_SESSION_ID")})
	httpReq, err := http.NewRequest(http.MethodPost, base+"/ask", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("ASK_TOKEN"); tok != "" {
		httpReq.Header.Set("X-Rag-Token", tok)
	}
	// No timeout: the user may take arbitrarily long to answer. The request ends
	// when the user answers or claude tears down this process (closing the conn).
	resp, err := (&http.Client{}).Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", resp.Status)
	}
	var parsed struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	return parsed.Answer, nil
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
