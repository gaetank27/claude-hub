package session

import (
	"os"
	"path/filepath"
	"testing"
)

// TestComputeUsage locks the journal aggregation against the frontend's
// aggregateUsage + contextOf: per-turn tokens sum, total_cost_usd is summed as a
// per-run delta (resetting on --resume), and context is the LAST assistant
// message's usage (not the result usage).
func TestComputeUsage(t *testing.T) {
	// Two CLI runs in one journal. Run 1 costs climb 0.01 -> 0.03; run 2 resets
	// and climbs 0.005 -> 0.02 (the drop marks a new run). Expected cost =
	// 0.03 (run 1 final) + 0.005 + 0.015 (run 2 deltas) = 0.05.
	lines := []string{
		`{"type":"system","subtype":"init","permissionMode":"plan"}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":5,"cache_creation_input_tokens":10,"cache_read_input_tokens":100,"output_tokens":3}}}`,
		`{"type":"result","total_cost_usd":0.01,"duration_ms":1000,"usage":{"input_tokens":100,"output_tokens":40}}`,
		`{"type":"assistant","message":{"usage":{"input_tokens":2,"cache_creation_input_tokens":20,"cache_read_input_tokens":200,"output_tokens":9}}}`,
		`{"type":"result","total_cost_usd":0.03,"duration_ms":2500,"usage":{"input_tokens":150,"output_tokens":60}}`,
		// --resume: cost resets.
		`{"type":"system","subtype":"init","permissionMode":"plan"}`,
		`{"type":"result","total_cost_usd":0.005,"duration_ms":500,"usage":{"input_tokens":50,"output_tokens":20}}`,
		// Last assistant message defines the context window occupancy.
		`{"type":"assistant","message":{"usage":{"input_tokens":1,"cache_creation_input_tokens":30,"cache_read_input_tokens":255000,"output_tokens":12}}}`,
		`{"type":"result","total_cost_usd":0.02,"duration_ms":3000,"usage":{"input_tokens":70,"output_tokens":30}}`,
	}
	path := filepath.Join(t.TempDir(), "j.jsonl")
	var buf []byte
	for _, l := range lines {
		buf = append(buf, l...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	u := computeUsage(path)

	if got, want := u.CostUSD, 0.05; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("CostUSD = %v, want %v", got, want)
	}
	if got, want := u.InputTokens, 100+150+50+70; got != want {
		t.Errorf("InputTokens = %d, want %d", got, want)
	}
	if got, want := u.OutputTokens, 40+60+20+30; got != want {
		t.Errorf("OutputTokens = %d, want %d", got, want)
	}
	// Context = last assistant: 1 + 30 + 255000.
	if got, want := u.ContextTokens, 1+30+255000; got != want {
		t.Errorf("ContextTokens = %d, want %d", got, want)
	}
	// Compute time sums per-turn durations directly: 1000+2500+500+3000.
	if got, want := u.ComputeMS, 1000+2500+500+3000; got != want {
		t.Errorf("ComputeMS = %d, want %d", got, want)
	}
}

// TestComputeUsageMissing returns a zero value for an absent journal.
func TestComputeUsageMissing(t *testing.T) {
	u := computeUsage(filepath.Join(t.TempDir(), "nope.jsonl"))
	if u.CostUSD != 0 || u.InputTokens != 0 || u.OutputTokens != 0 ||
		u.ContextTokens != 0 || u.ComputeMS != 0 || len(u.Skills) != 0 {
		t.Errorf("missing journal = %+v, want zero", u)
	}
}

// TestComputeUsageSkills extracts Skill invocations from assistant tool_use
// blocks, dedups by tool_use id, and counts repeats per skill.
func TestComputeUsageSkills(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"t1","name":"Skill","input":{"skill":"verify","args":"x"}}]}}`,
		// Same tool_use id repeated on a later line: must not double-count.
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Skill","input":{"skill":"verify"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Skill","input":{"skill":"verify"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"Skill","input":{"skill":"code-review"}}]}}`,
		// A non-Skill tool_use is ignored.
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t4","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"result","total_cost_usd":0.01}`,
	}
	path := filepath.Join(t.TempDir(), "j.jsonl")
	var buf []byte
	for _, l := range lines {
		buf = append(buf, l...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	u := computeUsage(path)
	if got, want := u.Skills["verify"], 2; got != want {
		t.Errorf("Skills[verify] = %d, want %d", got, want)
	}
	if got, want := u.Skills["code-review"], 1; got != want {
		t.Errorf("Skills[code-review] = %d, want %d", got, want)
	}
	if len(u.Skills) != 2 {
		t.Errorf("Skills = %+v, want 2 distinct", u.Skills)
	}
}

// TestComputeUsageRags extracts RAG queries from knowledge_search /
// project_code_search tool_use blocks: a named collection keeps its ref, a
// collection-less search maps to the all-collections sentinel, and the private
// code search maps to the project sentinel. Dedup by tool_use id.
func TestComputeUsageRags(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"r1","name":"mcp__knowledge__knowledge_search","input":{"query":"x","collection":"datacore-swarm","k":3}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"r2","name":"mcp__knowledge__knowledge_search","input":{"query":"y","collection":"datacore-swarm"}}]}}`,
		// No collection → all-collections sentinel.
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"r3","name":"mcp__knowledge__knowledge_search","input":{"query":"z"}}]}}`,
		// Private code RAG → project sentinel; r1 repeated must not double-count.
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"r4","name":"mcp__knowledge__project_code_search","input":{"query":"w"}},{"type":"tool_use","id":"r1","name":"mcp__knowledge__knowledge_search","input":{"collection":"datacore-swarm"}}]}}`,
		`{"type":"result","total_cost_usd":0.01}`,
	}
	path := filepath.Join(t.TempDir(), "j.jsonl")
	var buf []byte
	for _, l := range lines {
		buf = append(buf, l...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	u := computeUsage(path)
	if got, want := u.Rags["datacore-swarm"], 2; got != want {
		t.Errorf("Rags[datacore-swarm] = %d, want %d", got, want)
	}
	if got, want := u.Rags[ragAllCollections], 1; got != want {
		t.Errorf("Rags[all] = %d, want %d", got, want)
	}
	if got, want := u.Rags[ragProjectCode], 1; got != want {
		t.Errorf("Rags[project] = %d, want %d", got, want)
	}
	if len(u.Rags) != 3 {
		t.Errorf("Rags = %+v, want 3 distinct", u.Rags)
	}
}
