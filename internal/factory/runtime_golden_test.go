package factory

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden files")

var sha256Hex = regexp.MustCompile(`[0-9a-f]{64}`)

// normalizeEvidence replaces run-specific paths (longest first) and digests so
// evidence can be compared byte for byte across runs.
func normalizeEvidence(s string, repl map[string]string) string {
	keys := make([]string, 0, len(repl))
	for k := range repl {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		s = strings.ReplaceAll(s, k, repl[k])
	}
	return sha256Hex.ReplaceAllString(s, "<SHA256>")
}

func goldenCompare(t *testing.T, path, got string) {
	t.Helper()
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update-golden): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("evidence drifted from %s\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

func TestCopilotAdapter_GoldenEvidence(t *testing.T) {
	t.Setenv("GO_WANT_COPILOT_FIXTURE", "1")
	// The host's COPILOT_* variables are machine-specific and are passed through
	// to the child; pin them so the env golden is deterministic.
	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); strings.HasPrefix(name, "COPILOT_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("COPILOT_HOME", "/golden/copilot-home")
	t.Setenv("COPILOT_DIR", "/golden/copilot-home")
	c, req := setupController(t), workflowRequest()
	source := filepath.Join(t.TempDir(), "explicit.agent.md")
	if err := os.WriteFile(source, []byte("---\nname: explicit\n---\nExplicit agent source.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range req.Config.Workflow.Phases {
		p := &req.Config.Workflow.Phases[i]
		policy := CopilotPolicy{Version: 1, Executable: os.Args[0], Role: p.Role, Agent: "explicit", SourceProfile: source, AvailableTools: []string{"view", "rg", "glob"}, AllowTools: []string{}}
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
	if err != nil || r.State != "awaiting_review" {
		t.Fatalf("pipeline did not pass: %+v: %v", r, err)
	}
	for _, p := range req.Config.Workflow.Phases {
		output := filepath.Join(r.Dir, "outputs", p.Name)
		read := func(name string) string {
			b, err := os.ReadFile(filepath.Join(output, name))
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		var manifest map[string]any
		if err := json.Unmarshal([]byte(read("adapter.json")), &manifest); err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(manifest))
		for k := range manifest {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		launch, _ := manifest["launch_dir"].(string)
		repl := map[string]string{launch: "<LAUNCH>", r.Worktree: "<WORKTREE>", r.Dir: "<RUN>", os.Args[0]: "<EXE>", source: "<PROFILE>"}
		envLine := ""
		for _, line := range strings.Split(read("copilot.stderr.log"), "\n") {
			if strings.HasPrefix(line, "fixture env: ") {
				envLine = line
			}
		}
		got := strings.Join([]string{
			"== argv", normalizeEvidence(read("adapter-argv.json"), repl),
			"== prompt", normalizeEvidence(read("adapter-prompt.txt"), repl),
			"== manifest keys", strings.Join(keys, "\n"),
			"== notice", manifest["notice"].(string),
			"== launch_settings", manifest["launch_settings"].(string),
			"== env", envLine,
		}, "\n") + "\n"
		goldenCompare(t, filepath.Join("testdata", "copilot-golden", p.Name+".txt"), got)
	}
}
