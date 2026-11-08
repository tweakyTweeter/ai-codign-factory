package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func claudeStreamFixture(t *testing.T, edit func(init, result map[string]any) []map[string]any) ([]byte, LaunchInputs) {
	t.Helper()
	in := LaunchInputs{Launch: "/private/l", Tools: DriverTools{Available: []string{"Read", "Grep", "Glob"}}}
	init := map[string]any{"type": "system", "subtype": "init", "tools": []string{"Glob", "Read", "Grep"}, "mcp_servers": []any{}, "skills": []any{}, "plugins": []map[string]string{{"source": "cc-plugin-agents-md@builtin"}, {"source": "cc-plugin-telemetry@builtin"}, {"source": "cc-plugin-privacy@builtin"}}, "permissionMode": "dontAsk", "cwd": "/private/l", "model": "claude-x", "claude_code_version": "2.1.306", "apiKeySource": "none"}
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

func TestClaudeFinal_SchemaAddsExactlyStructuredOutput(t *testing.T) {
	withTool := func(i, r map[string]any) []map[string]any {
		i["tools"] = []string{"Glob", "Read", "Grep", "StructuredOutput"}
		return []map[string]any{i, r}
	}
	raw, in := claudeStreamFixture(t, withTool)
	in.Schema = `{"type":"object"}`
	if _, err := claudeFinal(raw, in, &PhaseOutcome{}); err != nil {
		t.Fatalf("schema run with StructuredOutput rejected: %v", err)
	}
	// Without a schema the extra tool is an isolation mismatch.
	in.Schema = ""
	out := &PhaseOutcome{}
	if _, err := claudeFinal(raw, in, out); err == nil || out.FailureReason != "isolation_mismatch" {
		t.Fatalf("StructuredOutput without a schema accepted: %+v %v", out, err)
	}
	// With a schema, a missing StructuredOutput is also a mismatch.
	raw, in = claudeStreamFixture(t, nil)
	in.Schema = `{"type":"object"}`
	out = &PhaseOutcome{}
	if _, err := claudeFinal(raw, in, out); err == nil || out.FailureReason != "isolation_mismatch" {
		t.Fatalf("schema run without StructuredOutput accepted: %+v %v", out, err)
	}
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
		"unreviewed builtin plugin": {"isolation_mismatch", func(i, r map[string]any) []map[string]any {
			i["plugins"] = []map[string]string{{"source": "cc-plugin-sec-default@builtin"}}
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

func TestClaudeFinal_InitMustProveIsolation(t *testing.T) {
	for _, key := range []string{"tools", "mcp_servers", "skills", "plugins"} {
		for name, set := range map[string]func(i map[string]any){
			"missing": func(i map[string]any) { delete(i, key) },
			"null":    func(i map[string]any) { i[key] = nil },
			"object":  func(i map[string]any) { i[key] = map[string]any{} },
		} {
			t.Run(key+" "+name, func(t *testing.T) {
				raw, in := claudeStreamFixture(t, func(i, r map[string]any) []map[string]any { set(i); return []map[string]any{i, r} })
				out := &PhaseOutcome{}
				if _, err := claudeFinal(raw, in, out); err == nil || out.FailureReason != "isolation_mismatch" {
					t.Fatalf("init without %s proof accepted: %q %v", key, out.FailureReason, err)
				}
			})
		}
	}
	for name, edit := range map[string]func(i map[string]any){
		"duplicate tool":   func(i map[string]any) { i["tools"] = []string{"Read", "Grep", "Glob", "Glob"} },
		"empty source":     func(i map[string]any) { i["plugins"] = []map[string]string{{"source": ""}} },
		"missing source":   func(i map[string]any) { i["plugins"] = []map[string]string{{"name": "x"}} },
		"no apiKeySource":  func(i map[string]any) { delete(i, "apiKeySource") },
		"empty key source": func(i map[string]any) { i["apiKeySource"] = "" },
		"login managed":    func(i map[string]any) { i["apiKeySource"] = "/login managed key" },
		"apiKeyHelper":     func(i map[string]any) { i["apiKeySource"] = "apiKeyHelper" },
		"user key":         func(i map[string]any) { i["apiKeySource"] = "user" },
	} {
		t.Run(name, func(t *testing.T) {
			raw, in := claudeStreamFixture(t, func(i, r map[string]any) []map[string]any { edit(i); return []map[string]any{i, r} })
			out := &PhaseOutcome{}
			if _, err := claudeFinal(raw, in, out); err == nil || out.FailureReason != "isolation_mismatch" {
				t.Fatalf("%s accepted: %q %v", name, out.FailureReason, err)
			}
		})
	}
	raw, in := claudeStreamFixture(t, nil)
	out := &PhaseOutcome{}
	if _, err := claudeFinal(raw, in, out); err != nil || out.APIKeySource != "none" {
		t.Fatalf("subscription auth (none) rejected: %q %v", out.APIKeySource, err)
	}
}

func TestClaudeFinal_PermissionDenialsRecordToolAndPathOnly(t *testing.T) {
	raw, in := claudeStreamFixture(t, func(i, r map[string]any) []map[string]any {
		r["permission_denials"] = []any{
			map[string]any{"tool_name": "Write", "tool_use_id": "u1", "tool_input": map[string]any{"file_path": "/etc/x", "content": "SECRET-CONTENT"}},
			map[string]any{"tool_name": "Grep", "tool_input": map[string]any{"path": "/home", "pattern": "SECRET-PATTERN"}},
		}
		return []map[string]any{i, r}
	})
	out := &PhaseOutcome{}
	if _, err := claudeFinal(raw, in, out); err != nil {
		t.Fatal(err)
	}
	want := []PermissionDenial{{Tool: "Write", Path: "/etc/x"}, {Tool: "Grep", Path: "/home"}}
	if len(out.PermissionDenials) != 2 || out.PermissionDenials[0] != want[0] || out.PermissionDenials[1] != want[1] {
		t.Fatalf("denials %+v, want %+v", out.PermissionDenials, want)
	}
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "SECRET") || !strings.Contains(string(data), `"tool":"Write","path":"/etc/x"`) {
		t.Fatalf("outcome evidence wrong or leaks tool input: %s", data)
	}
	for name, v := range map[string]any{"null": nil, "missing": "<delete>"} {
		raw, in := claudeStreamFixture(t, func(i, r map[string]any) []map[string]any {
			if v == "<delete>" {
				delete(r, "permission_denials")
			} else {
				r["permission_denials"] = v
			}
			return []map[string]any{i, r}
		})
		out := &PhaseOutcome{}
		if _, err := claudeFinal(raw, in, out); err != nil || len(out.PermissionDenials) != 0 {
			t.Fatalf("%s denials: %+v %v", name, out.PermissionDenials, err)
		}
	}
	raw, in = claudeStreamFixture(t, func(i, r map[string]any) []map[string]any {
		r["permission_denials"] = map[string]any{"tool_name": "Write"}
		return []map[string]any{i, r}
	})
	out = &PhaseOutcome{}
	if _, err := claudeFinal(raw, in, out); err == nil || out.FailureReason != "stream_invalid" {
		t.Fatalf("non-array denials accepted: %q %v", out.FailureReason, err)
	}
}

func TestClaudeFinal_RateLimitAndStreamEdges(t *testing.T) {
	limited := func(status any) func(i, r map[string]any) []map[string]any {
		return func(i, r map[string]any) []map[string]any {
			info := map[string]any{}
			if status != nil {
				info["status"] = status
			}
			return []map[string]any{i, {"type": "rate_limit_event", "rate_limit_info": info}, r}
		}
	}
	for name, edit := range map[string]func(i, r map[string]any) []map[string]any{
		"allowed_warning": limited("allowed_warning"),
		"missing status":  limited(nil),
		"empty status":    limited(""),
	} {
		raw, in := claudeStreamFixture(t, edit)
		out := &PhaseOutcome{}
		if _, err := claudeFinal(raw, in, out); err != nil || out.RateLimited {
			t.Fatalf("%s: rate_limited=%t %v", name, out.RateLimited, err)
		}
	}
	// A failed result after a status-less rate-limit event is not a usage limit.
	raw, in := claudeStreamFixture(t, func(i, r map[string]any) []map[string]any {
		r["is_error"] = true
		return limited("")(i, r)
	})
	out := &PhaseOutcome{}
	if _, err := claudeFinal(raw, in, out); err == nil || out.FailureReason != "stream_invalid" {
		t.Fatalf("empty status classified %q: %v", out.FailureReason, err)
	}
	raw, in = claudeStreamFixture(t, func(i, r map[string]any) []map[string]any {
		r["is_error"] = true
		r["api_error_status"] = "429"
		return []map[string]any{i, r}
	})
	out = &PhaseOutcome{}
	if _, err := claudeFinal(raw, in, out); err == nil || out.FailureReason != "usage_limit" {
		t.Fatalf("string 429 classified %q: %v", out.FailureReason, err)
	}
	for name, raw := range map[string][]byte{
		"empty":         nil,
		"blank lines":   []byte("\n  \n"),
		"oversize line": append(bytes.Repeat([]byte("x"), outputLimit+1), '\n'),
	} {
		out := &PhaseOutcome{}
		if _, err := claudeFinal(raw, LaunchInputs{}, out); err == nil || out.FailureReason != "stream_invalid" {
			t.Fatalf("%s stream classified %q: %v", name, out.FailureReason, err)
		}
	}
}

type copilotLikeDriver struct{ claudeDriver }

func (copilotLikeDriver) Final([]byte, LaunchInputs, *PhaseOutcome) (string, error) {
	panic("Final must not run for Copilot during failure classification")
}

func TestClassifyRunFailure(t *testing.T) {
	usage, in := claudeStreamFixture(t, func(i, r map[string]any) []map[string]any {
		r["is_error"] = true
		r["api_error_status"] = 429
		return []map[string]any{i, r}
	})
	valid, _ := claudeStreamFixture(t, nil)
	execErr := errors.New("exit status 1")
	for name, tc := range map[string]struct {
		raw       []byte
		execErr   error
		truncated bool
		integrity error
		preset    string
		want      string
	}{
		"usage limit on exit error":    {usage, execErr, false, nil, "", "usage_limit"},
		"invalid stream on exit error": {[]byte("garbage\n"), execErr, false, nil, "", "stream_invalid"},
		"truncation":                   {usage, execErr, true, nil, "", "stream_invalid"},
		"truncation exit ok":           {valid, nil, true, nil, "", "stream_invalid"},
		"timeout wins":                 {usage, execErr, true, errors.New("seal"), "timeout", "timeout"},
		"tampering beats usage limit":  {usage, execErr, true, errors.New("seal"), "", "isolation_mismatch"},
		"no exec error":                {usage, nil, false, nil, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			out := &PhaseOutcome{Runtime: "claude", FailureReason: tc.preset}
			classifyRunFailure(claudeDriver{}, tc.raw, in, out, tc.execErr, tc.truncated, tc.integrity)
			if out.FailureReason != tc.want {
				t.Fatalf("reason %q, want %q", out.FailureReason, tc.want)
			}
		})
	}
	out := &PhaseOutcome{}
	classifyRunFailure(copilotLikeDriver{}, usage, in, out, execErr, true, errors.New("seal"))
	if !reflect.DeepEqual(*out, PhaseOutcome{}) {
		t.Fatalf("Copilot outcome changed: %+v", out)
	}
}

// residueDriver is a Claude driver double whose PostRun audit fails, as a
// real audit would when the child leaves residue or litters its launch dir.
type residueDriver struct{ claudeDriver }

func (residueDriver) PostRun(string, map[string]string, *PhaseOutcome) error {
	return errors.New("residue left in launch dir")
}

func TestFinishRunPostRunOutranksStream(t *testing.T) {
	usage, in := claudeStreamFixture(t, func(i, r map[string]any) []map[string]any {
		r["is_error"] = true
		r["api_error_status"] = 429
		return []map[string]any{i, r}
	})
	for name, truncated := range map[string]bool{"usage limit": false, "truncated": true} {
		t.Run(name, func(t *testing.T) {
			out := &PhaseOutcome{Runtime: "claude"}
			err := finishRun(residueDriver{}, t.TempDir(), nil, usage, in, out, errors.New("exit status 1"), truncated, nil)
			if out.FailureReason != "isolation_mismatch" {
				t.Fatalf("reason %q, want isolation_mismatch", out.FailureReason)
			}
			for _, part := range []string{"exit status 1", "residue left in launch dir"} {
				if err == nil || !strings.Contains(err.Error(), part) {
					t.Fatalf("error %v lacks %q", err, part)
				}
			}
		})
	}
	out := &PhaseOutcome{}
	err := finishRun(copilotDriver{}, t.TempDir(), nil, usage, in, out, errors.New("exit status 1"), true, nil)
	if want := "exit status 1\nCopilot output/diagnostics truncated"; err == nil || err.Error() != want {
		t.Fatalf("Copilot error %q, want %q", err, want)
	}
	if !reflect.DeepEqual(*out, PhaseOutcome{}) {
		t.Fatalf("Copilot outcome changed: %+v", out)
	}
}
