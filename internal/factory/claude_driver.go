package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

type claudeDriver struct{}

const claudeLaunchSettings = "{\"disableAllHooks\":true}\n"

const claudeNotice = "private launch dir outside any checkout; no user/project/local settings sources (no user hooks, plugins, permissions or global CLAUDE.md); disableAllHooks; no MCP; no skills; exact tool list with dontAsk; reads limited to the worktree, private inputs and profile; Dev writes limited to the worktree; env is an exact allowlist; built-in plugins (agents-md, telemetry, plugin-authoring) still load; auto-memory points at an empty per-launch-dir folder that is removed after the run; isolation verified from the init event; not a sandbox"

var claudeEnvAllow = map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "TMPDIR": true, "LANG": true, "TERM": true, "CLAUDE_CONFIG_DIR": true}

func (claudeDriver) Name() string   { return "claude" }
func (claudeDriver) Label() string  { return "Claude" }
func (claudeDriver) Notice() string { return claudeNotice }

func (claudeDriver) LaunchSettings(launch string) (LaunchSettings, error) {
	dir := filepath.Join(launch, ".claude-launch")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return LaunchSettings{}, err
	}
	file := filepath.Join(dir, "settings.json")
	if err := writeExclusive(file, []byte(claudeLaunchSettings)); err != nil {
		return LaunchSettings{}, err
	}
	return LaunchSettings{SealDir: ".claude-launch", File: file, Content: claudeLaunchSettings}, nil
}

// stripFrontmatter drops a leading YAML block: Claude receives the role body as
// appended system prompt text, not as an agent definition. It handles LF and
// CRLF line endings, an empty block, and a closing fence at EOF. Input with no
// opening fence, or an opening fence without a closing one, is returned as is.
func stripFrontmatter(b []byte) []byte {
	s := string(b)
	var rest string
	switch {
	case strings.HasPrefix(s, "---\r\n"):
		rest = s[5:]
	case strings.HasPrefix(s, "---\n"):
		rest = s[4:]
	default:
		return b
	}
	for pos := 0; pos <= len(rest); {
		nl := strings.IndexByte(rest[pos:], '\n')
		line, next := rest[pos:], len(rest)+1
		if nl >= 0 {
			line, next = rest[pos:pos+nl], pos+nl+1
		}
		if strings.TrimSuffix(line, "\r") == "---" {
			if next > len(rest) {
				return nil
			}
			return []byte(strings.TrimLeft(rest[next:], "\r\n"))
		}
		pos = next
	}
	return b
}

func (claudeDriver) ProfileLaunchCopy(launch, agent string, profile []byte) (string, error) {
	dir := filepath.Join(launch, "profile-snapshot")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, agent+".md")
	return path, writeExclusive(path, stripFrontmatter(profile))
}

func scopedRule(tool, root string) string { return tool + "(/" + root + "/**)" }

// unsafeRuleRoot reports whether a root path could change the meaning of a
// permission rule: glob metacharacters, rule delimiters, whitespace or control
// characters. Such paths are rejected, never escaped.
func unsafeRuleRoot(path string) bool {
	return strings.ContainsFunc(path, func(r rune) bool {
		return strings.ContainsRune("*?[]{}(),", r) || unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

// safeScopeRoots returns the given and resolved forms of path, failing closed
// when any form could widen or break a rule.
func safeScopeRoots(label, path string) ([]string, error) {
	forms, err := scopeRoots(path)
	if err != nil {
		return nil, err
	}
	for _, form := range forms {
		if unsafeRuleRoot(form) {
			return nil, fmt.Errorf("claude %s path %q contains characters unsafe in a permission rule (glob, bracket, parenthesis, comma, whitespace or control); use a path without them", label, form)
		}
	}
	return forms, nil
}

func (claudeDriver) Tools(p PhasePolicy, roots ToolRoots) (DriverTools, error) {
	if p.Copilot != nil {
		return DriverTools{}, errors.New("claude runtime requires a version 2 policy")
	}
	t := DriverTools{Available: []string{"Read", "Grep", "Glob"}}
	for _, root := range []struct{ label, path string }{{"worktree", roots.Worktree}, {"inputs", roots.Inputs}, {"profile", roots.Profile}} {
		forms, err := safeScopeRoots(root.label, root.path)
		if err != nil {
			return DriverTools{}, err
		}
		for _, form := range forms {
			for _, tool := range []string{"Read", "Grep", "Glob"} {
				t.Allowed = append(t.Allowed, scopedRule(tool, form))
			}
		}
	}
	if slices.Contains(p.Grants, "write") {
		t.Available = append(t.Available, "Edit", "Write")
		forms, err := safeScopeRoots("worktree", roots.Worktree)
		if err != nil {
			return DriverTools{}, err
		}
		for _, form := range forms {
			t.Allowed = append(t.Allowed, scopedRule("Edit", form), scopedRule("Write", form))
		}
	}
	return t, nil
}

func claudeEnv(parent []string) (env, dropped []string) {
	for _, entry := range parent {
		key, _, _ := strings.Cut(entry, "=")
		if claudeEnvAllow[key] || strings.HasPrefix(key, "LC_") {
			env = append(env, entry)
		} else {
			dropped = append(dropped, key)
		}
	}
	sort.Strings(dropped)
	return env, dropped
}

func (claudeDriver) Env() ([]string, bool, []string) {
	env, dropped := claudeEnv(os.Environ())
	return env, false, dropped
}

const claudeVersionTimeout = 30 * time.Second

func (claudeDriver) Version(ctx context.Context, p PhasePolicy, launch string, env []string, inherit bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, claudeVersionTimeout)
	defer cancel()
	var out, errOut bytes.Buffer
	w := &cappedWriter{dst: &out, remaining: 4096}
	ew := &cappedWriter{dst: &errOut, remaining: 4096}
	code, err := executeStreamsEnv(ctx, launch, []string{p.Executable, "--version"}, env, w, ew, nil, false, inherit)
	if err != nil || code != 0 || w.truncated {
		return "", errors.Join(err, fmt.Errorf("claude --version failed (exit %d): %s", code, strings.TrimSpace(errOut.String())))
	}
	return strings.TrimSpace(out.String()), nil
}

func (claudeDriver) Manifest(m ManifestBase, notice string) any {
	return struct {
		Runtime           string      `json:"runtime"`
		Policy            PhasePolicy `json:"policy"`
		SHA256            string      `json:"source_profile_sha256"`
		ResolvedProfile   string      `json:"resolved_source_profile"`
		LaunchDir         string      `json:"launch_dir"`
		LaunchSettings    string      `json:"launch_settings"`
		Worktree          string      `json:"worktree"`
		Tools             []string    `json:"tools"`
		AllowedTools      []string    `json:"allowed_tools"`
		DroppedEnv        []string    `json:"dropped_env"`
		ExecutableVersion string      `json:"executable_version"`
		Notice            string      `json:"notice"`
	}{"claude", m.Policy, m.SHA256, m.ResolvedProfile, m.LaunchDir, m.Settings.Content, m.Worktree, m.Tools.Available, m.Tools.Allowed, m.DroppedEnv, m.ExecutableVersion, notice}
}

func (claudeDriver) Argv(p PhasePolicy, in LaunchInputs) []string {
	argv := []string{p.Executable, "-p", in.Instructions,
		"--append-system-prompt-file", in.ProfilePath,
		"--tools", strings.Join(in.Tools.Available, ","),
		"--allowedTools"}
	argv = append(argv, in.Tools.Allowed...)
	argv = append(argv, "--permission-mode", "dontAsk", "--setting-sources", "",
		"--settings", in.Settings.File, "--strict-mcp-config", "--disable-slash-commands",
		"--no-session-persistence", "--output-format", "stream-json", "--verbose")
	if p.Model != "" {
		argv = append(argv, "--model", p.Model)
	}
	return append(argv, "--add-dir", in.Worktree, "--add-dir", in.Inputs)
}

func (claudeDriver) Final([]byte, LaunchInputs, *PhaseOutcome) (string, error) {
	return "", errors.New("claude final not implemented")
}

func (claudeDriver) PostRun(string, map[string]string, *PhaseOutcome) error {
	return errors.New("claude postrun not implemented")
}
