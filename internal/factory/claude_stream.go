package factory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// claudeBuiltinPlugins are the only plugins an isolated phase may report. A
// new built-in fails closed until it is reviewed and added here.
var claudeBuiltinPlugins = map[string]bool{
	"cc-plugin-agents-md@builtin":        true,
	"cc-plugin-telemetry@builtin":        true,
	"cc-plugin-plugin-authoring@builtin": true,
	// Since 2.1.319. Reviewed 2026-11-07: only a tool.check hook that turns
	// allow into ask (denied under dontAsk) when a shell or web call contains
	// the signed-in account's email; it adds no tools, skills or prompt text.
	"cc-plugin-privacy@builtin": true,
}

type claudeEvent struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	Tools          json.RawMessage `json:"tools"`
	MCPServers     json.RawMessage `json:"mcp_servers"`
	Skills         json.RawMessage `json:"skills"`
	Plugins        json.RawMessage `json:"plugins"`
	PermissionMode string          `json:"permissionMode"`
	Cwd            string          `json:"cwd"`
	Model          string          `json:"model"`
	Version        string          `json:"claude_code_version"`
	APIKeySource   string          `json:"apiKeySource"`
	RateLimitInfo  *struct {
		Status string `json:"status"`
	} `json:"rate_limit_info"`
	IsError        *bool           `json:"is_error"`
	Result         *string         `json:"result"`
	APIErrorStatus json.RawMessage `json:"api_error_status"`
	Denials        json.RawMessage `json:"permission_denials"`
}

// initArray decodes an init field that must be present and a JSON array. A
// missing, null or non-array value proves nothing and is rejected.
func initArray(name string, raw json.RawMessage, v any) error {
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '[' {
		return fmt.Errorf("Claude init did not report %s as an array", name)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("Claude init %s: %w", name, err)
	}
	return nil
}

func checkClaudeInit(e claudeEvent, in LaunchInputs) error {
	var tools []string
	var mcp, skills []json.RawMessage
	var plugins []struct {
		Source string `json:"source"`
	}
	for _, f := range []struct {
		name string
		raw  json.RawMessage
		v    any
	}{{"tools", e.Tools, &tools}, {"mcp_servers", e.MCPServers, &mcp}, {"skills", e.Skills, &skills}, {"plugins", e.Plugins, &plugins}} {
		if err := initArray(f.name, f.raw, f.v); err != nil {
			return err
		}
	}
	want := slices.Clone(in.Tools.Available)
	if in.Schema != "" {
		// --json-schema adds exactly this tool; the model answers through it.
		want = append(want, "StructuredOutput")
	}
	slices.Sort(tools)
	slices.Sort(want)
	if !slices.Equal(tools, want) {
		return fmt.Errorf("Claude reported tools %v, want %v", tools, want)
	}
	if len(mcp) != 0 {
		return errors.New("Claude loaded MCP servers")
	}
	if len(skills) != 0 {
		return errors.New("Claude loaded skills")
	}
	for _, p := range plugins {
		if !claudeBuiltinPlugins[p.Source] {
			return fmt.Errorf("Claude loaded non-built-in plugin %q", p.Source)
		}
	}
	if e.PermissionMode != "dontAsk" {
		return fmt.Errorf("Claude permission mode %q, want dontAsk", e.PermissionMode)
	}
	if e.Cwd != in.Launch {
		return fmt.Errorf("Claude cwd %q, want private launch dir", e.Cwd)
	}
	// Subscription (OAuth) login reports "none"; every other value, including
	// a missing one, is some kind of key and fails closed.
	if e.APIKeySource != "none" {
		return fmt.Errorf("Claude did not authenticate with the subscription (apiKeySource %q)", e.APIKeySource)
	}
	return nil
}

// claudeDenials keeps only the tool name and target path of each permission
// denial; tool inputs can carry file contents and are never recorded.
func claudeDenials(raw json.RawMessage) ([]PermissionDenial, error) {
	if t := bytes.TrimSpace(raw); len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil, nil
	}
	var entries []struct {
		Tool  string `json:"tool_name"`
		Input struct {
			FilePath string `json:"file_path"`
			Path     string `json:"path"`
		} `json:"tool_input"`
	}
	if err := initArray("permission_denials", raw, &entries); err != nil {
		return nil, err
	}
	denials := make([]PermissionDenial, 0, len(entries))
	for _, d := range entries {
		path := d.Input.FilePath
		if path == "" {
			path = d.Input.Path
		}
		denials = append(denials, PermissionDenial{Tool: d.Tool, Path: path})
	}
	return denials, nil
}

// claudeFinal parses stream-json. The init event must prove the isolation the
// flags requested; flags alone are not trusted across CLI updates.
func claudeFinal(raw []byte, in LaunchInputs, out *PhaseOutcome) (string, error) {
	out.Runtime = "claude"
	fail := func(reason string, err error) (string, error) {
		if out.FailureReason == "" {
			out.FailureReason = reason
		}
		return "", err
	}
	unsuccessful := func() string {
		if out.RateLimited {
			return "usage_limit"
		}
		return "stream_invalid"
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), runtimeStreamLimit)
	seenInit, done, content := false, false, ""
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if done {
			return fail("stream_invalid", errors.New("Claude emitted data after the result event"))
		}
		var e claudeEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return fail("stream_invalid", fmt.Errorf("Claude stream-json: %w", err))
		}
		if e.Type == "" {
			return fail("stream_invalid", errors.New("Claude event missing type"))
		}
		if !seenInit {
			if e.Type != "system" || e.Subtype != "init" {
				return fail("isolation_mismatch", errors.New("first Claude event is not system/init"))
			}
			if err := checkClaudeInit(e, in); err != nil {
				return fail("isolation_mismatch", err)
			}
			seenInit = true
			out.Model, out.ClaudeVersion, out.APIKeySource = e.Model, e.Version, e.APIKeySource
			continue
		}
		switch e.Type {
		case "system":
			if e.Subtype == "init" {
				return fail("isolation_mismatch", errors.New("repeated Claude init event"))
			}
		case "rate_limit_event":
			if e.RateLimitInfo != nil && e.RateLimitInfo.Status != "" && e.RateLimitInfo.Status != "allowed" && e.RateLimitInfo.Status != "allowed_warning" {
				out.RateLimited = true
			}
		case "result":
			done = true
			denials, err := claudeDenials(e.Denials)
			if err != nil {
				return fail("stream_invalid", err)
			}
			out.PermissionDenials = denials
			if s := bytes.TrimSpace(e.APIErrorStatus); bytes.Equal(s, []byte("429")) || bytes.Equal(s, []byte(`"429"`)) {
				out.RateLimited = true
			}
			if e.Subtype == "error_max_structured_output_retries" {
				// The model never produced a response matching the role schema.
				return fail("contract", errors.New("Claude response never matched the required schema"))
			}
			if e.Subtype != "success" || e.IsError == nil || *e.IsError || e.Result == nil || strings.TrimSpace(*e.Result) == "" {
				return fail(unsuccessful(), fmt.Errorf("Claude result unsuccessful (subtype %q)", e.Subtype))
			}
			content = *e.Result
		}
	}
	if err := scanner.Err(); err != nil {
		return fail("stream_invalid", err)
	}
	if !seenInit {
		return fail("stream_invalid", errors.New("Claude stream is empty"))
	}
	if !done {
		return fail(unsuccessful(), errors.New("Claude stream missing result event"))
	}
	return content, nil
}
