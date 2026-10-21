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
}

type claudeEvent struct {
	Type       string            `json:"type"`
	Subtype    string            `json:"subtype"`
	Tools      []string          `json:"tools"`
	MCPServers []json.RawMessage `json:"mcp_servers"`
	Skills     []json.RawMessage `json:"skills"`
	Plugins    []struct {
		Source string `json:"source"`
	} `json:"plugins"`
	PermissionMode string `json:"permissionMode"`
	Cwd            string `json:"cwd"`
	Model          string `json:"model"`
	Version        string `json:"claude_code_version"`
	APIKeySource   string `json:"apiKeySource"`
	RateLimitInfo  *struct {
		Status string `json:"status"`
	} `json:"rate_limit_info"`
	IsError        *bool           `json:"is_error"`
	Result         *string         `json:"result"`
	APIErrorStatus json.RawMessage `json:"api_error_status"`
	Denials        json.RawMessage `json:"permission_denials"`
}

func checkClaudeInit(e claudeEvent, in LaunchInputs) error {
	got, want := slices.Clone(e.Tools), slices.Clone(in.Tools.Available)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return fmt.Errorf("Claude reported tools %v, want %v", got, want)
	}
	if len(e.MCPServers) != 0 {
		return errors.New("Claude loaded MCP servers")
	}
	if len(e.Skills) != 0 {
		return errors.New("Claude loaded skills")
	}
	for _, p := range e.Plugins {
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
	if strings.Contains(e.APIKeySource, "ANTHROPIC_API_KEY") || e.APIKeySource == "apiKeyHelper" {
		return errors.New("Claude authenticated with an API key, not the subscription")
	}
	return nil
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
	scanner.Buffer(make([]byte, 4096), outputLimit)
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
			if e.RateLimitInfo != nil && e.RateLimitInfo.Status != "allowed" && e.RateLimitInfo.Status != "allowed_warning" {
				out.RateLimited = true
			}
		case "result":
			done = true
			out.PermissionDenials = e.Denials
			if bytes.Equal(bytes.TrimSpace(e.APIErrorStatus), []byte("429")) {
				out.RateLimited = true
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
		return fail("isolation_mismatch", errors.New("Claude stream missing init event"))
	}
	if !done {
		return fail(unsuccessful(), errors.New("Claude stream missing result event"))
	}
	return content, nil
}
