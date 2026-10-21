package factory

import (
	"context"
	"encoding/json"
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
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
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
	dev, err := claudeDriver{}.Tools(claudeTestPolicy("dev", "read", "write"), roots)
	if err != nil {
		t.Fatal(err)
	}
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

func TestClaudeTools_RejectsRuleWideningPaths(t *testing.T) {
	for name, dirname := range map[string]string{"bracket": "proj[12]", "star": "pr*oj", "question": "pr?oj", "brace": "pr{a,b}oj", "paren": "proj)x", "open paren": "pr(oj", "comma": "pr,oj", "space": "pr oj", "tab": "pr\toj", "newline": "pr\noj"} {
		for _, slot := range []string{"worktree", "inputs", "profile"} {
			t.Run(name+"/"+slot, func(t *testing.T) {
				base, _ := filepath.EvalSymlinks(t.TempDir())
				roots := ToolRoots{Worktree: filepath.Join(base, "wt"), Inputs: filepath.Join(base, "in"), Profile: filepath.Join(base, "pr")}
				bad := filepath.Join(base, dirname)
				switch slot {
				case "worktree":
					roots.Worktree = bad
				case "inputs":
					roots.Inputs = bad
				default:
					roots.Profile = bad
				}
				for _, d := range []string{roots.Worktree, roots.Inputs, roots.Profile} {
					if err := os.MkdirAll(d, 0700); err != nil {
						t.Fatal(err)
					}
				}
				got, err := claudeDriver{}.Tools(claudeTestPolicy("dev", "read", "write"), roots)
				if err == nil || !strings.Contains(err.Error(), "unsafe") {
					t.Fatalf("expected unsafe-path error, got %+v: %v", got, err)
				}
				if len(got.Allowed) != 0 {
					t.Fatalf("rules returned alongside error: %q", got.Allowed)
				}
			})
		}
	}
}

func TestClaudeTools_RejectsMetacharacterInResolvedForm(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	target := filepath.Join(base, "real[1]")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	roots := ToolRoots{Worktree: link, Inputs: link, Profile: link}
	if _, err := (claudeDriver{}).Tools(claudeTestPolicy("qa", "read"), roots); err == nil {
		t.Fatal("symlink resolving to a metacharacter path must be rejected")
	}
}

func TestStripFrontmatter(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"crlf":            {"---\r\nname: x\r\n---\r\n\r\nBody\r\n", "Body\r\n"},
		"closer at eof":   {"---\nname: x\n---", ""},
		"empty block":     {"---\n---\nBody\n", "Body\n"},
		"no closer":       {"---\nname: x\nBody\n", "---\nname: x\nBody\n"},
		"no opener":       {"Body\n---\nx\n---\n", "Body\n---\nx\n---\n"},
		"crlf no closer":  {"---\r\nname: x\r\n", "---\r\nname: x\r\n"},
		"crlf closer eof": {"---\r\nname: x\r\n---", ""},
	} {
		if got := string(stripFrontmatter([]byte(tc.in))); got != tc.want {
			t.Errorf("%s: got %q want %q", name, got, tc.want)
		}
	}
}

func TestClaudeEnv_PrefixLookalikesAndMalformed(t *testing.T) {
	env, dropped := claudeEnv([]string{"PATHX=1", "HOME_EXTRA=2", "LC=3", "LC_ALL=C", "SECRETNOEQ", "PATH=/bin"})
	if strings.Join(env, " ") != "LC_ALL=C PATH=/bin" {
		t.Fatalf("env: %q", env)
	}
	if strings.Join(dropped, ",") != "HOME_EXTRA,LC,PATHX,SECRETNOEQ" {
		t.Fatalf("dropped: %q", dropped)
	}
}

func TestClaudeManifest_Fields(t *testing.T) {
	m := ManifestBase{Policy: claudeTestPolicy("qa", "read"), SHA256: "abc", ResolvedProfile: "/p", LaunchDir: "/l", Worktree: "/w", ExecutableVersion: "2.1.306 (Claude Code)",
		Settings: LaunchSettings{Content: claudeLaunchSettings}, Tools: DriverTools{Available: []string{"Read"}, Allowed: []string{"Read(//w/**)"}}, DroppedEnv: []string{"ANTHROPIC_API_KEY"}}
	raw, err := json.Marshal(claudeDriver{}.Manifest(m, claudeNotice))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["runtime"] != "claude" || got["executable_version"] != "2.1.306 (Claude Code)" || got["notice"] != claudeNotice {
		t.Fatalf("manifest: %s", raw)
	}
	for k, want := range map[string]string{"tools": "Read", "allowed_tools": "Read(//w/**)", "dropped_env": "ANTHROPIC_API_KEY"} {
		arr, _ := got[k].([]any)
		if len(arr) != 1 || arr[0] != want {
			t.Fatalf("%s: %v", k, got[k])
		}
	}
	if strings.Contains(string(raw), "=") && strings.Contains(string(raw), "ANTHROPIC_API_KEY=") {
		t.Fatal("dropped env values leaked")
	}
}

func TestClaudeVersion(t *testing.T) {
	p := claudeTestPolicy("qa", "read")
	p.Executable = "/bin/echo"
	got, err := claudeDriver{}.Version(context.Background(), p, t.TempDir(), []string{"PATH=/usr/bin:/bin"}, false)
	if err != nil || got != "--version" {
		t.Fatalf("version: %q: %v", got, err)
	}
	p.Executable = "/nonexistent/claude"
	if _, err := (claudeDriver{}).Version(context.Background(), p, t.TempDir(), nil, false); err == nil {
		t.Fatal("missing executable must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Executable = "/bin/echo"
	if _, err := (claudeDriver{}).Version(ctx, p, t.TempDir(), nil, false); err == nil {
		t.Fatal("cancelled context must fail")
	}
}

func TestStripFrontmatterBasic(t *testing.T) {
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
