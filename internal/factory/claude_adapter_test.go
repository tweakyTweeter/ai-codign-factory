package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// claudePolicyFile writes a v2 Claude policy whose executable is this test
// binary (the fake claude in claude_fixture_test.go). The fixture cannot see
// test env variables, so its behaviour mode travels in the profile text.
func claudePolicyFile(t *testing.T, role, mode string) string {
	t.Helper()
	dir := t.TempDir()
	profile := filepath.Join(dir, "fixture.agent.md")
	if err := os.WriteFile(profile, []byte("---\nname: fixture\nmode: subagent\n---\nFIXTURE_MODE="+mode+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	grants := []string{"read"}
	if role == "dev" {
		grants = append(grants, "write")
	}
	b, err := json.Marshal(map[string]any{"version": 2, "runtime": "claude", "executable": os.Args[0], "role": role, "agent": "fixture", "source_profile": profile, "grants": grants})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// claudeEnvSetup isolates HOME (so PostRun never scans the real
// ~/.claude/projects) and plants variables the env allowlist must drop.
func claudeEnvSetup(t *testing.T) (projects string) {
	t.Helper()
	_, projects = isolatedHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-leak")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "must-not-leak")
	t.Setenv("GO_WANT_COPILOT_FIXTURE", "must-not-leak")
	return projects
}

func useClaudePhase(t *testing.T, req Request, role, mode string) {
	t.Helper()
	idx := slices.IndexFunc(req.Config.Workflow.Phases, func(p Phase) bool { return p.Role == role })
	if idx < 0 {
		t.Fatalf("no %s phase", role)
	}
	p := &req.Config.Workflow.Phases[idx]
	p.Command = fixtureCommand("agent")
	p.Command.Argv = append(p.Command.Argv, claudePolicyFile(t, role, mode))
}

func TestClaudeAdapter_PipelinePasses(t *testing.T) {
	projects := claudeEnvSetup(t)
	c, req := setupController(t), workflowRequest()
	for _, p := range req.Config.Workflow.Phases {
		useClaudePhase(t, req, p.Role, "")
	}
	r, err := c.Run(t.Context(), req)
	if err != nil || r.State != "awaiting_review" {
		t.Fatalf("claude pipeline did not pass: %+v: %v", r, err)
	}
	requireState(t, c, r.ID, "awaiting_review")
	for _, p := range req.Config.Workflow.Phases {
		output := filepath.Join(r.Dir, "outputs", p.Name)
		var manifest struct {
			Runtime    string   `json:"runtime"`
			LaunchDir  string   `json:"launch_dir"`
			DroppedEnv []string `json:"dropped_env"`
		}
		b, _ := os.ReadFile(filepath.Join(output, "adapter.json"))
		if json.Unmarshal(b, &manifest) != nil || manifest.Runtime != "claude" || manifest.LaunchDir == "" {
			t.Fatalf("%s manifest: %s", p.Name, b)
		}
		for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_MESSAGING_TOKEN", "GO_WANT_COPILOT_FIXTURE"} {
			if !slices.Contains(manifest.DroppedEnv, name) {
				t.Fatalf("%s: %s not recorded as dropped: %v", p.Name, name, manifest.DroppedEnv)
			}
		}
		if _, err := os.Stat(manifest.LaunchDir); !os.IsNotExist(err) {
			t.Fatalf("%s: private launch dir not removed: %v", p.Name, err)
		}
		var outcome PhaseOutcome
		b, _ = os.ReadFile(filepath.Join(output, "adapter-outcome.json"))
		if json.Unmarshal(b, &outcome) != nil || outcome.Runtime != "claude" || outcome.Model != "fixture-model" ||
			outcome.APIKeySource != "none" || outcome.FailureReason != "" || len(outcome.Residue) != 0 {
			t.Fatalf("%s outcome: %s", p.Name, b)
		}
	}
	if _, err := os.Stat(filepath.Join(r.Worktree, ".claude-launch")); !os.IsNotExist(err) {
		t.Fatal("launch settings leaked into candidate")
	}
	if entries, _ := os.ReadDir(projects); len(entries) != 0 {
		t.Fatalf("global residue left: %v", entries)
	}
}

func TestClaudeAdapter_FailsClosed(t *testing.T) {
	// msg is the distinguishing message of the one control under test, so a
	// mode cannot stay green through some other control's failure.
	for _, tc := range []struct{ mode, role, reason, msg string }{
		{"blocked", "dev", "contract", "Dev did not explicitly report"},
		{"wrong-fp", "qa", "contract", "fingerprint does not match"},
		{"tamper-input", "producer", "isolation_mismatch", "modified private inputs"},
		{"tamper-settings", "producer", "isolation_mismatch", "launch settings changed"},
		{"launch-litter", "producer", "isolation_mismatch", "changed its private launch dir"},
		{"residue-file", "producer", "isolation_mismatch", "Claude wrote global state"},
		{"extra-tool", "producer", "isolation_mismatch", "Claude reported tools"},
		{"usage-limit", "producer", "usage_limit", "Claude result unsuccessful"},
		// A failed exec has its (here empty) stream classified; see classifyRunFailure.
		{"exit-nonzero", "producer", "stream_invalid", "exit status 90"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			projects := claudeEnvSetup(t)
			c, req := setupController(t), workflowRequest()
			useClaudePhase(t, req, tc.role, tc.mode)
			r, err := c.Run(t.Context(), req)
			if err == nil || r.State == "awaiting_review" || len(r.Evidence) == 0 {
				t.Fatalf("%s accepted: %+v", tc.mode, r)
			}
			last := r.Evidence[len(r.Evidence)-1]
			if last.Role != tc.role {
				t.Fatalf("run failed in %s, want %s: %+v", last.Role, tc.role, r.Evidence)
			}
			log, _ := os.ReadFile(last.Log)
			if !strings.Contains(string(log), "failure_reason="+tc.reason) {
				t.Fatalf("want failure_reason=%s in log: %s", tc.reason, log)
			}
			if !strings.Contains(string(log), tc.msg) {
				t.Fatalf("want %q (the control under test) in log: %s", tc.msg, log)
			}
			var outcome PhaseOutcome
			b, _ := os.ReadFile(filepath.Join(last.OutputDir, "adapter-outcome.json"))
			if json.Unmarshal(b, &outcome) != nil || outcome.FailureReason != tc.reason {
				t.Fatalf("outcome: %s", b)
			}
			if tc.mode == "residue-file" {
				// Residue with content is kept as evidence, never deleted.
				if len(outcome.Residue) != 1 || !strings.HasPrefix(outcome.Residue[0], projects+string(filepath.Separator)) {
					t.Fatalf("residue not recorded: %+v", outcome.Residue)
				}
				if _, err := os.Stat(filepath.Join(outcome.Residue[0], "memory", "MEMORY.md")); err != nil {
					t.Fatalf("residue evidence removed: %v", err)
				}
			}
		})
	}
}

func TestClaudeAdapter_EmptyResidueIsCleaned(t *testing.T) {
	projects := claudeEnvSetup(t)
	c, req := setupController(t), workflowRequest()
	useClaudePhase(t, req, "producer", "residue-empty")
	if r, err := c.Run(t.Context(), req); err != nil || r.State != "awaiting_review" {
		t.Fatalf("empty residue failed the run: %+v: %v", r, err)
	}
	if entries, err := os.ReadDir(projects); err != nil || len(entries) != 0 {
		t.Fatalf("empty residue not removed (fixture must have created it): %v: %v", entries, err)
	}
}
