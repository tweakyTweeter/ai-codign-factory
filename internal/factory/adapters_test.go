package factory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func copilotFixture() int {
	joined := strings.Join(os.Args[1:], " ")
	if os.Getenv("COPILOT_ALLOW_ALL") != "false" || strings.Contains(joined, "--allow-all") || !strings.Contains(joined, "--deny-tool shell") {
		return 70
	}
	// Launch boundary: cwd is a private dir (not the candidate) whose only
	// repo-local setting disables hooks; the worktree is reached via --add-dir.
	// FACTORY_* is stripped from the Copilot environment, so the worktree is
	// taken from the first --add-dir and must be the repository named in the prompt.
	cwd, err := os.Getwd()
	if err != nil || len(os.Args) < 5 || os.Args[3] != "--add-dir" || !filepath.IsAbs(os.Args[4]) ||
		!strings.Contains(os.Args[len(os.Args)-1], "The repository under work is "+os.Args[4]+":") {
		return 76
	}
	worktree := os.Args[4]
	if rel, err := filepath.Rel(worktree, cwd); err != nil || !strings.HasPrefix(rel, "..") {
		return 77
	}
	if settings, err := os.ReadFile(filepath.Join(cwd, ".github", "copilot", "settings.local.json")); err != nil || string(settings) != "{\"disableAllHooks\":true}\n" {
		return 78
	}
	if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 3 {
		return 79
	}
	if !strings.Contains(joined, "--disallow-temp-dir") {
		return 81
	}
	for i, arg := range os.Args[:len(os.Args)-1] {
		if arg == "--add-dir" && os.Args[i+1] != worktree && os.Args[i+1] != filepath.Join(cwd, "profile-snapshot") {
			return 82
		}
	}
	prompt := os.Args[len(os.Args)-1]
	if !strings.Contains(prompt, "absolute paths") {
		return 80
	}
	if mode := os.Getenv("GO_COPILOT_TAMPER"); mode != "" {
		path := filepath.Join(cwd, "inputs", "prompt.txt")
		switch mode {
		case "profile":
			path = filepath.Join(cwd, "profile-snapshot", ".github", "agents", "explicit.agent.md")
		case "settings":
			path = filepath.Join(cwd, ".github", "copilot", "settings.local.json")
		}
		if err := os.WriteFile(path, []byte("changed input"), 0600); err != nil {
			return 83
		}
	}
	contextFile := filepath.Join(cwd, "inputs", "outputs")
	if _, err := os.Stat(contextFile); err != nil {
		return 84
	}
	content := "Producer plan: implement worker.txt after inspecting acceptance"
	if strings.Contains(prompt, "You are the dev phase") {
		if !strings.Contains(joined, "--allow-tool write") {
			return 71
		}

		if err := os.WriteFile(filepath.Join(worktree, "worker.txt"), []byte("implemented"), 0600); err != nil {
			return 72
		}
		content = `{"version":1,"status":"complete","summary":"implemented acceptance"}`
		if os.Getenv("GO_COPILOT_BLOCKED") == "1" {
			content = `{"version":1,"status":"blocked","summary":"missing capability"}`
		}
	} else if strings.Contains(prompt, "You are the qa phase") || strings.Contains(prompt, "You are the review phase") {
		if !strings.Contains(joined, "--deny-tool write") {
			return 73
		}
		_, tail, found := strings.Cut(prompt, `"fingerprint":`)
		if !found || len(tail) < 66 {
			return 74
		}
		var fp string
		if json.Unmarshal([]byte(tail[:66]), &fp) != nil {
			return 75
		}
		v := Verdict{Version: 1, Verdict: "pass", Summary: "independent evidence assessment", Fingerprint: fp, Findings: []Finding{}}
		b, _ := json.Marshal(v)
		content = string(b)
	}
	var copilotEnv []string
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "COPILOT_") {
			copilotEnv = append(copilotEnv, entry)
		}
	}
	sort.Strings(copilotEnv)
	fmt.Fprintf(os.Stderr, "fixture env: %s\n", strings.Join(copilotEnv, " "))
	var sentinels []string
	for _, name := range []string{"FACTORY_GOLDEN_SENTINEL", "GOLDEN_PASSTHROUGH_SENTINEL"} {
		if _, ok := os.LookupEnv(name); ok {
			sentinels = append(sentinels, name+"=present")
		} else {
			sentinels = append(sentinels, name+"=absent")
		}
	}
	fmt.Fprintf(os.Stderr, "fixture env sentinels: %s\n", strings.Join(sentinels, " "))
	fmt.Fprintln(os.Stderr, "fixture diagnostic, deliberately not JSON")
	event := map[string]any{"type": "assistant.message", "data": map[string]any{"phase": "final_answer", "content": content}}
	b, _ := json.Marshal(event)
	fmt.Println(string(b))
	fmt.Println(`{"type":"result","exitCode":0,"sessionId":"fixture"}`)
	return 0
}

func TestCopilotAdapter_PrivateInputMutationFailsClosed(t *testing.T) {
	for _, mode := range []string{"input", "profile", "settings"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GO_WANT_COPILOT_FIXTURE", "1")
			t.Setenv("GO_COPILOT_TAMPER", mode)
			c, req := setupController(t), workflowRequest()
			source := filepath.Join(t.TempDir(), "agent.md")
			if err := os.WriteFile(source, []byte("original profile"), 0600); err != nil {
				t.Fatal(err)
			}
			policy := CopilotPolicy{Version: 1, Executable: os.Args[0], Role: "producer", Agent: "explicit", SourceProfile: source, AvailableTools: []string{"view"}, AllowTools: []string{}}
			data, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "policy.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			req.Config.Workflow.Phases[0].Command = fixtureCommand("adapter")
			req.Config.Workflow.Phases[0].Command.Argv = append(req.Config.Workflow.Phases[0].Command.Argv, path)
			r, err := c.Run(t.Context(), req)
			if err == nil || r.State == "awaiting_review" || len(r.Evidence) != 1 {
				t.Fatalf("mutated private %s accepted: %+v: %v", mode, r, err)
			}
			b, err := os.ReadFile(r.Evidence[0].Log)
			if err != nil || !strings.Contains(string(b), "changed") && !strings.Contains(string(b), "modified private") {
				t.Fatalf("missing integrity diagnostic: %s: %v", b, err)
			}
			if b, err := os.ReadFile(filepath.Join(r.Dir, "prompt.txt")); err != nil || string(b) != string(req.Prompt) {
				t.Fatalf("private copy aliased original input: %q: %v", b, err)
			}
		})
	}
}

func TestCopilotAdapter_ExplicitSnapshotsAndSeparateRoleEvidence(t *testing.T) {
	t.Setenv("GO_WANT_COPILOT_FIXTURE", "1")
	t.Setenv("COPILOT_ALLOW_ALL", "true")
	c, req := setupController(t), workflowRequest()
	source := filepath.Join(t.TempDir(), "explicit.agent.md")
	original := []byte("---\nname: explicit\n---\nExplicit agent source.\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	sourceLink := filepath.Join(t.TempDir(), "linked.agent.md")
	if err := os.Symlink(source, sourceLink); err != nil {
		t.Fatal(err)
	}
	for i := range req.Config.Workflow.Phases {
		p := &req.Config.Workflow.Phases[i]
		policy := CopilotPolicy{Version: 1, Executable: os.Args[0], Role: p.Role, Agent: "explicit", SourceProfile: sourceLink, AvailableTools: []string{"view", "rg", "glob"}, AllowTools: []string{}}
		if p.Role == "dev" {
			policy.AvailableTools = append(policy.AvailableTools, "create", "edit", "apply_patch")
			policy.AllowTools = []string{"write"}
		}
		data, _ := json.Marshal(policy)
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		p.Command = fixtureCommand("adapter")
		p.Command.Argv = append(p.Command.Argv, path)
	}
	r, err := c.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "awaiting_review" {
		t.Fatalf("adapter pipeline did not pass: %+v", r)
	}
	for _, p := range req.Config.Workflow.Phases {
		output := filepath.Join(r.Dir, "outputs", p.Name)
		b, err := os.ReadFile(filepath.Join(output, "profile-snapshot", ".github", "agents", "explicit.agent.md"))
		if err != nil || string(b) != string(original) {
			t.Fatalf("profile was not copied exactly: %q: %v", b, err)
		}
		var manifest struct {
			LaunchDir      string `json:"launch_dir"`
			LaunchSettings string `json:"launch_settings"`
			Worktree       string `json:"worktree"`
		}
		if b, err := os.ReadFile(filepath.Join(output, "adapter.json")); err != nil || json.Unmarshal(b, &manifest) != nil {
			t.Fatalf("adapter manifest: %v", err)
		}
		if manifest.LaunchSettings != "{\"disableAllHooks\":true}\n" || manifest.Worktree != r.Worktree || manifest.LaunchDir == "" {
			t.Fatalf("launch boundary not recorded: %+v", manifest)
		}
		if _, err := os.Stat(manifest.LaunchDir); !os.IsNotExist(err) {
			t.Fatalf("private launch dir not removed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(r.Worktree, ".github", "copilot")); !os.IsNotExist(err) {
			t.Fatal("hook settings leaked into candidate")
		}
		if b, _ := os.ReadFile(filepath.Join(output, "copilot.stderr.log")); !strings.Contains(string(b), "diagnostic") {
			t.Fatal("stderr evidence missing")
		}
	}
	if b, _ := os.ReadFile(source); string(b) != string(original) {
		t.Fatal("source profile modified")
	}
}

func TestCopilotAdapter_DevBlockedExitZeroDoesNotPass(t *testing.T) {
	t.Setenv("GO_WANT_COPILOT_FIXTURE", "1")
	t.Setenv("GO_COPILOT_BLOCKED", "1")
	dir := t.TempDir()
	source := filepath.Join(dir, "agent.md")
	if err := os.WriteFile(source, []byte("explicit source"), 0600); err != nil {
		t.Fatal(err)
	}
	policy := CopilotPolicy{Version: 1, Executable: os.Args[0], Role: "dev", Agent: "explicit", SourceProfile: source, AvailableTools: []string{"apply_patch"}, AllowTools: []string{"write"}}
	data, _ := json.Marshal(policy)
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, req := setupController(t), workflowRequest()
	req.Config.Workflow.Phases[1].Command = fixtureCommand("adapter")
	req.Config.Workflow.Phases[1].Command.Argv = append(req.Config.Workflow.Phases[1].Command.Argv, path)
	r, err := c.Run(t.Context(), req)
	if err == nil || r.State == "awaiting_review" || len(r.Evidence) != 2 {
		t.Fatalf("blocked Dev accepted: %+v: %v", r, err)
	}
}

func TestCopilotPolicy_RejectsImplicitOrBroadPermissions(t *testing.T) {
	valid := CopilotPolicy{Version: 1, Executable: "/absolute/copilot", Role: "qa", Agent: "qa", SourceProfile: "/absolute/qa.agent.md", AvailableTools: []string{"view"}, AllowTools: []string{}}
	for name, edit := range map[string]func(*CopilotPolicy){
		"shell":             func(p *CopilotPolicy) { p.AvailableTools = []string{"bash"} },
		"delegation":        func(p *CopilotPolicy) { p.AvailableTools = []string{"task"} },
		"blanket":           func(p *CopilotPolicy) { p.AllowTools = []string{"*"} },
		"no approvals":      func(p *CopilotPolicy) { p.AllowTools = nil },
		"readonly write":    func(p *CopilotPolicy) { p.AllowTools = []string{"write"} },
		"unapproved create": func(p *CopilotPolicy) { p.Role = "dev"; p.AvailableTools = []string{"create"} },
		"relative source":   func(p *CopilotPolicy) { p.SourceProfile = "implicit.agent.md" },
		"agent traversal":   func(p *CopilotPolicy) { p.Agent = "../dev" },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			edit(&p)
			if err := validatePolicy(p, p.Role); err == nil {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
}

func TestCopilotJSONL_RequiresFinalAnswerAndSuccessfulTerminal(t *testing.T) {
	good := "{\"type\":\"assistant.message\",\"data\":{\"phase\":\"final_answer\",\"content\":\"done\"}}\n{\"type\":\"result\",\"exitCode\":0}\n"
	if content, err := copilotFinal([]byte(good)); err != nil || content != "done" {
		t.Fatalf("valid stream: %q: %v", content, err)
	}
	for _, bad := range []string{
		"", `{"type":"result","exitCode":0}`,
		strings.Replace(good, `"exitCode":0`, `"exitCode":1`, 1),
		strings.Replace(good, `"exitCode":0`, `"unknown":0`, 1),
		strings.Replace(good, `"phase":"final_answer"`, `"phase":"analysis"`, 1),
		good + "{}\n", strings.Split(good, "\n")[0],
		// Regression for real run 061a7115: a global agentStop hook continued
		// the session and produced a second final answer before the terminal result.
		strings.Split(good, "\n")[0] + "\n" + strings.Replace(good, `"done"`, `"hook continuation"`, 1),
	} {
		if _, err := copilotFinal([]byte(bad)); err == nil {
			t.Fatalf("invalid stream accepted: %q", bad)
		}
	}
}

func TestRoborevImport_ValidatesCanonicalSchemaAndAttestedFingerprint(t *testing.T) {
	fp := strings.Repeat("a", 64)
	pass := `{"schema_version":2,"verdict":"pass","summary":"reviewed actual patch","findings":[]}`
	for name, report := range map[string]string{
		"pass":             pass,
		"fail":             strings.Replace(pass, `"pass"`, `"fail"`, 1),
		"inconclusive":     strings.Replace(pass, `"pass"`, `"unable_to_review"`, 1),
		"unknown":          strings.Replace(pass, `"pass"`, `"clean"`, 1),
		"wrong version":    strings.Replace(pass, `"schema_version":2`, `"schema_version":1`, 1),
		"missing findings": `{"schema_version":2,"verdict":"pass","summary":"review"}`,
		"unknown field":    strings.Replace(pass, `"schema_version":2`, `"schema_version":2,"ok":true`, 1),
		"findings":         `{"schema_version":2,"verdict":"pass","summary":"review","findings":[{"severity":"low","problem":"bug","fix":"fix bug","location":null}]}`,
		"missing location": `{"schema_version":2,"verdict":"pass","summary":"review","findings":[{"severity":"low","problem":"bug","fix":"fix bug"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("FACTORY_OUTPUT_DIR", dir)
			t.Setenv("FACTORY_RESULT_FILE", filepath.Join(dir, "result.json"))
			t.Setenv("FACTORY_PATCH_FINGERPRINT", fp)
			path := filepath.Join(dir, "report.json")
			if err := os.WriteFile(path, []byte(report), 0600); err != nil {
				t.Fatal(err)
			}
			err := RoborevImport(path, fp)
			if (err == nil) != (name == "pass") {
				t.Fatalf("report %s outcome: %v", name, err)
			}
		})
	}
	t.Setenv("FACTORY_PATCH_FINGERPRINT", fp)
	if err := RoborevImport("unused", strings.Repeat("b", 64)); err == nil {
		t.Fatal("unattested/stale report accepted")
	}
}

func TestPrivateLaunchDir_FailsClosedInsideCheckout(t *testing.T) {
	checkout := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", checkout)
	if dir, err := privateLaunchDir(); err == nil {
		t.Fatalf("launch dir inside checkout accepted: %s", dir)
	}
	if entries, _ := os.ReadDir(checkout); len(entries) != 1 {
		t.Fatalf("rejected launch dir not cleaned up: %v", entries)
	}
}
