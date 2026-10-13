// Command rag-mcp is a tiny stdio MCP server exposing a single `knowledge_search`
// tool, wired into every Claude Code session by the hub (via claude --mcp-config).
// It is a thin bridge: each tool call POSTs to the backend's loopback retrieval
// endpoint, so embedding + vector search stay centralized in the backend.
//
// The MCP stdio transport is newline-delimited JSON-RPC 2.0. Only the handful of
// methods Claude Code uses are implemented; everything else returns a method-not-
// found error. Configuration comes from the environment, set in the mcp-config
// file the hub writes:
//
//	RAG_SEARCH_URL   loopback retrieval endpoint, e.g. http://127.0.0.1:8788/search
//	RAG_SEARCH_TOKEN shared token sent as X-Rag-Token
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const protocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent => notification
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
			return // EOF or read error: the host closed the pipe
		}
	}
}

func handle(line []byte, out io.Writer) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return // unparseable; nothing we can reply to
	}
	// Notifications (no id) are fire-and-forget.
	notification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		reply(out, req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "claude-hub-rag", "version": "1.0.0"},
		})
	case "tools/list":
		tools := []any{searchToolSpec(), listToolSpec()}
		// When launched for a project session, the runner-set env scopes a private
		// per-project code search to that project's collection.
		if os.Getenv("PROJECT_RAG_COLLECTION") != "" {
			tools = append(tools, projectSearchToolSpec())
		}
		reply(out, req.ID, map[string]any{"tools": tools})
	case "tools/call":
		if notification {
			return
		}
		replyToolCall(out, req)
	case "ping":
		reply(out, req.ID, map[string]any{})
	default:
		// Initialized/cancelled/etc. notifications need no response.
		if notification {
			return
		}
		replyErr(out, req.ID, -32601, "method not found: "+req.Method)
	}
}

func searchToolSpec() map[string]any {
	return map[string]any{
		"name": "knowledge_search",
		"description": "Search the shared RAG knowledge base (technical and business " +
			"documents ingested into Claude Hub) for context relevant to the task. " +
			"Call this whenever you need project- or domain-specific information that " +
			"may live in the knowledge base rather than guessing. The base is split " +
			"into named collections; omit `collection` to search them all, or pass a " +
			"collection name/slug (see list_knowledge_collections) to narrow the search.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":      map[string]any{"type": "string", "description": "What to look for"},
				"collection": map[string]any{"type": "string", "description": "Optional collection name or slug to restrict the search"},
				"k":          map[string]any{"type": "integer", "description": "Max results (default 5)"},
			},
			"required": []any{"query"},
		},
	}
}

func listToolSpec() map[string]any {
	return map[string]any{
		"name": "list_knowledge_collections",
		"description": "List the available knowledge-base collections (name, slug, " +
			"description) so you can pick one to pass as `collection` to knowledge_search.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func projectSearchToolSpec() map[string]any {
	return map[string]any{
		"name": "project_code_search",
		"description": "Search THIS project's private RAG index (its source code, " +
			"config, docs and memory) for relevant snippets. Use it for fast code/" +
			"sequence lookup, bug hunting or impact analysis across the whole project. " +
			"Scoped to the current project unless `project` names another project this one " +
			"has been granted access to (see PROJECT_RAG_EXTRA).",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":   map[string]any{"type": "string", "description": "What to look for"},
				"k":       map[string]any{"type": "integer", "description": "Max results (default 5)"},
				"project": map[string]any{"type": "string", "description": "Optional slug of another accessible project" + extraProjectsHint()},
			},
			"required": []any{"query"},
		},
	}
}

// extraProjects parses the granted-projects map (slug -> collection id).
func extraProjects() map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal([]byte(os.Getenv("PROJECT_RAG_EXTRA")), &m)
	return m
}

func extraProjectsHint() string {
	var names []string
	for slug := range extraProjects() {
		names = append(names, slug)
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return " (" + strings.Join(names, ", ") + ")"
}

func replyToolCall(out io.Writer, req rpcRequest) {
	var params struct {
		Name      string `json:"name"`
		Arguments struct {
			Query      string `json:"query"`
			Collection string `json:"collection"`
			Project    string `json:"project"`
			K          int    `json:"k"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &params)
	var (
		text string
		err  error
	)
	switch params.Name {
	case "knowledge_search":
		text, err = search(params.Arguments.Query, params.Arguments.Collection, params.Arguments.K)
	case "project_code_search":
		// Own collection by default; another project only if the hub granted it.
		coll := os.Getenv("PROJECT_RAG_COLLECTION")
		if p := params.Arguments.Project; p != "" {
			c, ok := extraProjects()[p]
			if !ok {
				reply(out, req.ID, toolError("project not accessible: "+p))
				return
			}
			coll = c
		}
		text, err = search(params.Arguments.Query, coll, params.Arguments.K)
	case "list_knowledge_collections":
		text, err = listCollections()
	default:
		reply(out, req.ID, toolError("unknown tool: "+params.Name))
		return
	}
	if err != nil {
		reply(out, req.ID, toolError(params.Name+" failed: "+err.Error()))
		return
	}
	reply(out, req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	})
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": msg}},
		"isError": true,
	}
}

type searchResult struct {
	Text       string  `json:"text"`
	Title      string  `json:"title"`
	Source     string  `json:"source"`
	Collection string  `json:"collection"`
	Score      float32 `json:"score"`
}

func search(query, collection string, k int) (string, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "collection": collection, "k": k})
	resp, err := callBackend(http.MethodPost, os.Getenv("RAG_SEARCH_URL"), body)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Results []searchResult `json:"results"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Results) == 0 {
		return "No relevant documents found in the knowledge base.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d result(s) from the knowledge base:\n\n", len(parsed.Results))
	for i, r := range parsed.Results {
		src := r.Source
		if src == "" {
			src = r.Title
		}
		coll := ""
		if r.Collection != "" {
			coll = " · " + r.Collection
		}
		fmt.Fprintf(&b, "### %d. %s (%s%s) — score %.2f\n%s\n\n", i+1, r.Title, src, coll, r.Score, r.Text)
	}
	return strings.TrimSpace(b.String()), nil
}

func listCollections() (string, error) {
	url := os.Getenv("RAG_SEARCH_URL")
	// The collections endpoint is a sibling of /search on the same host.
	url = strings.TrimSuffix(url, "/search") + "/collections"
	resp, err := callBackend(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Collections []struct {
			Name        string `json:"name"`
			Slug        string `json:"slug"`
			Description string `json:"description"`
		} `json:"collections"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Collections) == 0 {
		return "No knowledge collections exist yet.", nil
	}
	var b strings.Builder
	b.WriteString("Available knowledge collections (pass name or slug as `collection`):\n\n")
	for _, c := range parsed.Collections {
		fmt.Fprintf(&b, "- %s (slug: %s)%s\n", c.Name, c.Slug,
			func() string {
				if c.Description != "" {
					return " — " + c.Description
				}
				return ""
			}())
	}
	return strings.TrimSpace(b.String()), nil
}

// callBackend issues an authenticated request to the loopback retrieval endpoint
// and returns the response body, or an error on a non-200.
func callBackend(method, url string, body []byte) ([]byte, error) {
	if url == "" {
		return nil, fmt.Errorf("RAG_SEARCH_URL not set")
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
	if tok := os.Getenv("RAG_SEARCH_TOKEN"); tok != "" {
		req.Header.Set("X-Rag-Token", tok)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(out)))
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
