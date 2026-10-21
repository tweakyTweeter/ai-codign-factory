package factory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func claudeTestPolicy(role string, grants ...string) PhasePolicy {
	return PhasePolicy{Runtime: "claude", Executable: "/abs/claude", Role: role, Agent: "factory-" + role, SourceProfile: "/abs/p.md", Grants: grants}
}

func TestClaudeTools_PathScoped(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	roots := ToolRoots{Worktree: filepath.Join(dir, "wt"), Inputs: filepath.Join(dir, "in"), Profile: filepath.Join(dir, "pr")}
	for _, d := range []string{roots.Worktree, roots.Inputs, roots.Profile} {
		os.MkdirAll(d, 0700)
	}
	qa, err := claudeDriver{}.Tools(claudeTestPolicy("qa", "read"), roots)
	if err != nil || strings.Join(qa.Available, ",") != "Read,Grep,Glob" {
		t.Fatalf("qa tools: %+v: %v", qa, err)
	}
	for _, rule := range qa.Allowed {
		if !strings.HasPrefix(rule, "Read(//") && !strings.HasPrefix(rule, "Grep(//") && !strings.HasPrefix(rule, "Glob(//") || !strings.HasSuffix(rule, "/**)") {
			t.Fatalf("unscoped or non-read rule %q", rule)
		}
	}
	dev, _ := claudeDriver{}.Tools(claudeTestPolicy("dev", "read", "write"), roots)
	if !slices.Contains(dev.Available, "Edit") || !slices.Contains(dev.Allowed, "Edit(/"+roots.Worktree+"/**)") || slices.Contains(dev.Allowed, "Edit(/"+roots.Inputs+"/**)") {
		t.Fatalf("dev write not scoped to worktree only: %+v", dev)
	}
	for _, rule := range dev.Allowed {
		if (strings.HasPrefix(rule, "Edit(") || strings.HasPrefix(rule, "Write(")) && !strings.Contains(rule, roots.Worktree) {
			t.Fatalf("write rule outside worktree: %q", rule)
		}
	}
	for _, forbidden := range []string{"Bash", "WebFetch", "WebSearch", "Agent", "NotebookEdit"} {
		if slices.Contains(dev.Available, forbidden) {
			t.Fatalf("forbidden tool %s granted", forbidden)
		}
	}
}

func TestClaudeArgv_IsolationFlags(t *testing.T) {
	p := claudeTestPolicy("review", "read")
	p.Model = "opus"
	in := LaunchInputs{Launch: "/l", Inputs: "/l/inputs", ProfilePath: "/l/profile-snapshot/a.md", Worktree: "/w", Instructions: "You are the review phase", Settings: LaunchSettings{File: "/l/.claude-launch/settings.json"}, Tools: DriverTools{Available: []string{"Read", "Grep", "Glob"}, Allowed: []string{"Read(//w/**)"}}}
	argv := claudeDriver{}.Argv(p, in)
	joined := strings.Join(argv, " ")
	if argv[0] != "/abs/claude" || argv[1] != "-p" || argv[2] != in.Instructions {
		t.Fatalf("prompt must come first: %q", argv[:3])
	}
	for _, want := range []string{"--append-system-prompt-file /l/profile-snapshot/a.md", "--tools Read,Grep,Glob", "--allowedTools Read(//w/**)", "--permission-mode dontAsk", "--settings /l/.claude-launch/settings.json", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence", "--output-format stream-json --verbose", "--model opus", "--add-dir /w --add-dir /l/inputs"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing %q: %s", want, joined)
		}
	}
	if i := slices.Index(argv, "--setting-sources"); i < 0 || argv[i+1] != "" {
		t.Fatalf("--setting-sources must be an explicit empty value: %q", argv)
	}
	p.Model = ""
	if slices.Contains(claudeDriver{}.Argv(p, in), "--model") {
		t.Fatal("--model passed without policy model")
	}
}

func TestClaudeEnv_StrictAllowlist(t *testing.T) {
	env, dropped := claudeEnv([]string{"PATH=/bin", "HOME=/h", "LC_ALL=C", "ANTHROPIC_API_KEY=k", "CLAUDE_CODE_MESSAGING_TOKEN=t", "CLAUDECODE=1", "GOOGLE_APPLICATION_CREDENTIALS=/c", "FACTORY_ROLE=dev", "CLAUDE_CONFIG_DIR=/cfg"})
	if strings.Join(env, " ") != "PATH=/bin HOME=/h LC_ALL=C CLAUDE_CONFIG_DIR=/cfg" {
		t.Fatalf("env not exact allowlist: %q", env)
	}
	if strings.Join(dropped, ",") != "ANTHROPIC_API_KEY,CLAUDECODE,CLAUDE_CODE_MESSAGING_TOKEN,FACTORY_ROLE,GOOGLE_APPLICATION_CREDENTIALS" {
		t.Fatalf("dropped names wrong or unsorted: %q", dropped)
	}
}

func TestStripFrontmatter(t *testing.T) {
	if got := string(stripFrontmatter([]byte("---\nname: x\nmode: subagent\n---\n\nBody\n"))); got != "Body\n" {
		t.Fatalf("frontmatter not stripped: %q", got)
	}
	if got := string(stripFrontmatter([]byte("No frontmatter\n"))); got != "No frontmatter\n" {
		t.Fatalf("plain profile changed: %q", got)
	}
}

func TestClaudeLaunchSettingsAndProfile(t *testing.T) {
	launch := t.TempDir()
	s, err := claudeDriver{}.LaunchSettings(launch)
	if err != nil || s.SealDir != ".claude-launch" || s.Content != "{\"disableAllHooks\":true}\n" {
		t.Fatalf("settings: %+v: %v", s, err)
	}
	if b, _ := os.ReadFile(s.File); string(b) != s.Content {
		t.Fatal("settings file content mismatch")
	}
	path, err := claudeDriver{}.ProfileLaunchCopy(launch, "factory-qa", []byte("---\nname: q\n---\nRole body\n"))
	if b, _ := os.ReadFile(path); err != nil || filepath.Base(path) != "factory-qa.md" || string(b) != "Role body\n" {
		t.Fatalf("profile copy: %q %q: %v", path, b, err)
	}
}
