package factory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// isClaudeFixtureInvocation recognises the claude driver's argv shape. The env
// allowlist strips every test variable, so the fake claude cannot be selected
// by environment the way the Copilot fixture is.
func isClaudeFixtureInvocation(args []string) bool {
	if len(args) == 2 && args[1] == "--version" {
		return true
	}
	return len(args) > 2 && args[1] == "-p" && strings.Contains(args[2], "supervised coding loop")
}

// fixtureEnvAllow is deliberately independent of production's claudeEnvAllow,
// so a bad entry added there is caught here.
var fixtureEnvAllow = map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"SHELL": true, "TMPDIR": true, "LANG": true, "TERM": true, "CLAUDE_CONFIG_DIR": true}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// claudeFixture is a fake `claude -p ... --output-format stream-json`. Its
// behaviour mode comes from the appended profile text (FIXTURE_MODE=...).
func claudeFixture() int {
	if os.Args[1] == "--version" {
		fmt.Println("2.1.306 (Claude Code fixture)")
		return 0
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !fixtureEnvAllow[key] && !strings.HasPrefix(key, "LC_") {
			fmt.Fprintln(os.Stderr, "fixture saw non-allowlisted env", key)
			return 90
		}
	}
	// PWD is not allowlisted, so Getwd returns the kernel's resolved path,
	// which is how real Claude reports cwd.
	cwd, err := os.Getwd()
	if err != nil {
		return 92
	}
	settings := flagValue(os.Args, "--settings")
	if b, err := os.ReadFile(settings); err != nil || string(b) != claudeLaunchSettings {
		return 91
	}
	profile, err := os.ReadFile(flagValue(os.Args, "--append-system-prompt-file"))
	if err != nil {
		return 93
	}
	_, mode, _ := strings.Cut(string(profile), "FIXTURE_MODE=")
	mode = strings.TrimSpace(mode)
	prompt := os.Args[2]
	worktree := flagValue(os.Args, "--add-dir")
	write := func(path, content string) bool {
		return os.WriteFile(path, []byte(content), 0600) == nil
	}
	switch mode {
	case "exit-nonzero":
		fmt.Fprintln(os.Stderr, "fixture simulates a failed exec before any stream")
		return 90
	case "tamper-input":
		if !write(filepath.Join(cwd, "inputs", "prompt.txt"), "changed") {
			return 94
		}
	case "tamper-settings":
		if !write(settings, "{}") {
			return 94
		}
	case "launch-litter":
		if !write(filepath.Join(cwd, "stray.txt"), "x") {
			return 94
		}
	case "residue-file", "residue-empty":
		home, err := os.UserHomeDir()
		if err != nil {
			return 94
		}
		dir := filepath.Join(home, ".claude", "projects", sanitiseProjectName(cwd), "memory")
		if os.MkdirAll(dir, 0700) != nil {
			return 94
		}
		if mode == "residue-file" && !write(filepath.Join(dir, "MEMORY.md"), "leak") {
			return 94
		}
	}
	tools := strings.Split(flagValue(os.Args, "--tools"), ",")
	if mode == "extra-tool" {
		tools = append(tools, "Bash")
	}
	emit := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	emit(map[string]any{"type": "system", "subtype": "init", "tools": tools, "mcp_servers": []any{}, "skills": []any{},
		"plugins": []map[string]string{{"name": "agents-md", "source": "cc-plugin-agents-md@builtin"}}, "permissionMode": flagValue(os.Args, "--permission-mode"),
		"cwd": cwd, "model": "fixture-model", "claude_code_version": "2.1.306", "apiKeySource": "none"})
	content := "Producer plan: implement worker.txt after inspecting acceptance"
	switch {
	case strings.Contains(prompt, "You are the dev phase"):
		if !write(filepath.Join(worktree, "worker.txt"), "implemented") {
			return 95
		}
		content = `{"version":1,"status":"complete","summary":"implemented acceptance"}`
		if mode == "blocked" {
			content = `{"version":1,"status":"blocked","summary":"missing capability"}`
		}
	case strings.Contains(prompt, "You are the qa phase"), strings.Contains(prompt, "You are the review phase"):
		_, tail, _ := strings.Cut(prompt, `"fingerprint":`)
		var fp string
		if len(tail) < 66 || json.Unmarshal([]byte(tail[:66]), &fp) != nil {
			return 96
		}
		if mode == "wrong-fp" {
			fp = strings.Repeat("0", 64)
		}
		b, _ := json.Marshal(Verdict{Version: 1, Verdict: "pass", Summary: "independent evidence assessment", Fingerprint: fp, Findings: []Finding{}})
		content = string(b)
	}
	if mode == "usage-limit" {
		emit(map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "rejected"}})
		emit(map[string]any{"type": "result", "subtype": "success", "is_error": true, "result": "usage limit reached", "permission_denials": []any{}})
		return 0
	}
	emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": content, "permission_denials": []any{}})
	return 0
}
