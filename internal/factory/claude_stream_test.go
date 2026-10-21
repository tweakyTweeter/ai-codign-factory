package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func claudeStreamFixture(t *testing.T, edit func(init, result map[string]any) []map[string]any) ([]byte, LaunchInputs) {
	t.Helper()
	in := LaunchInputs{Launch: "/private/l", Tools: DriverTools{Available: []string{"Read", "Grep", "Glob"}}}
	init := map[string]any{"type": "system", "subtype": "init", "tools": []string{"Glob", "Read", "Grep"}, "mcp_servers": []any{}, "skills": []any{}, "plugins": []map[string]string{{"source": "cc-plugin-agents-md@builtin"}, {"source": "cc-plugin-telemetry@builtin"}}, "permissionMode": "dontAsk", "cwd": "/private/l", "model": "claude-x", "claude_code_version": "2.1.306", "apiKeySource": "none"}
	result := map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "done", "permission_denials": []any{}}
	events := []map[string]any{init, {"type": "assistant"}, {"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "allowed"}}, result}
	if edit != nil {
		events = edit(init, result)
	}
	var b strings.Builder
	for _, e := range events {
		line, _ := json.Marshal(e)
		b.Write(line)
		b.WriteByte('\n')
	}
	return []byte(b.String()), in
}

func TestClaudeFinal_Success(t *testing.T) {
	raw, in := claudeStreamFixture(t, nil)
	out := &PhaseOutcome{}
	got, err := claudeFinal(raw, in, out)
	if err != nil || got != "done" || out.Model != "claude-x" || out.ClaudeVersion != "2.1.306" || out.Runtime != "claude" || out.FailureReason != "" {
		t.Fatalf("valid stream rejected: %q %+v: %v", got, out, err)
	}
}

func TestClaudeFinal_FailsClosed(t *testing.T) {
	assistant := map[string]any{"type": "assistant"}
	for name, tc := range map[string]struct {
		reason string
		edit   func(init, result map[string]any) []map[string]any
	}{
		"extra tool": {"isolation_mismatch", func(i, r map[string]any) []map[string]any {
			i["tools"] = []string{"Read", "Grep", "Glob", "Bash"}
			return []map[string]any{i, r}
		}},
		"mcp server": {"isolation_mismatch", func(i, r map[string]any) []map[string]any {
			i["mcp_servers"] = []any{map[string]any{"name": "x"}}
			return []map[string]any{i, r}
		}},
		"skill": {"isolation_mismatch", func(i, r map[string]any) []map[string]any { i["skills"] = []any{"tdd"}; return []map[string]any{i, r} }},
		"user plugin": {"isolation_mismatch", func(i, r map[string]any) []map[string]any {
			i["plugins"] = []map[string]string{{"source": "beads@market"}}
			return []map[string]any{i, r}
		}},
		"permission mode": {"isolation_mismatch", func(i, r map[string]any) []map[string]any {
			i["permissionMode"] = "acceptEdits"
			return []map[string]any{i, r}
		}},
		"wrong cwd": {"isolation_mismatch", func(i, r map[string]any) []map[string]any { i["cwd"] = "/repo"; return []map[string]any{i, r} }},
		"api key": {"isolation_mismatch", func(i, r map[string]any) []map[string]any {
			i["apiKeySource"] = "ANTHROPIC_API_KEY"
			return []map[string]any{i, r}
		}},
		"init not first": {"isolation_mismatch", func(i, r map[string]any) []map[string]any { return []map[string]any{assistant, i, r} }},
		"second init":    {"isolation_mismatch", func(i, r map[string]any) []map[string]any { return []map[string]any{i, i, r} }},
		"no result":      {"stream_invalid", func(i, r map[string]any) []map[string]any { return []map[string]any{i, assistant} }},
		"two results":    {"stream_invalid", func(i, r map[string]any) []map[string]any { return []map[string]any{i, r, r} }},
		"after result":   {"stream_invalid", func(i, r map[string]any) []map[string]any { return []map[string]any{i, r, assistant} }},
		"is_error":       {"stream_invalid", func(i, r map[string]any) []map[string]any { r["is_error"] = true; return []map[string]any{i, r} }},
		"error subtype": {"stream_invalid", func(i, r map[string]any) []map[string]any {
			r["subtype"] = "error_max_turns"
			return []map[string]any{i, r}
		}},
		"empty result":        {"stream_invalid", func(i, r map[string]any) []map[string]any { r["result"] = "  "; return []map[string]any{i, r} }},
		"missing is_error":    {"stream_invalid", func(i, r map[string]any) []map[string]any { delete(r, "is_error"); return []map[string]any{i, r} }},
		"string is_error":     {"stream_invalid", func(i, r map[string]any) []map[string]any { r["is_error"] = "false"; return []map[string]any{i, r} }},
		"missing result text": {"stream_invalid", func(i, r map[string]any) []map[string]any { delete(r, "result"); return []map[string]any{i, r} }},
		"missing type":        {"stream_invalid", func(i, r map[string]any) []map[string]any { return []map[string]any{i, {"x": 1}, r} }},
		"usage limit": {"usage_limit", func(i, r map[string]any) []map[string]any {
			r["is_error"] = true
			return []map[string]any{i, {"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "rejected"}}, r}
		}},
		"http 429": {"usage_limit", func(i, r map[string]any) []map[string]any {
			r["is_error"] = true
			r["api_error_status"] = 429
			return []map[string]any{i, r}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			raw, in := claudeStreamFixture(t, tc.edit)
			out := &PhaseOutcome{}
			if _, err := claudeFinal(raw, in, out); err == nil || out.FailureReason != tc.reason {
				t.Fatalf("want %s failure, got %q: %v", tc.reason, out.FailureReason, err)
			}
		})
	}
	out := &PhaseOutcome{}
	if _, err := claudeFinal([]byte("not json\n"), LaunchInputs{}, out); err == nil || out.FailureReason != "stream_invalid" {
		t.Fatalf("malformed JSON accepted: %v", err)
	}
}
