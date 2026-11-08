package factory

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
		{"memory-file", "producer", "isolation_mismatch", "Claude wrote global state"},
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
			if tc.mode == "memory-file" {
				// A spill next to any other file keeps the whole entry, spill included.
				if len(outcome.Residue) != 1 || len(outcome.SpillFiles) != 0 {
					t.Fatalf("memory-file outcome: %s", b)
				}
				for _, rel := range []string{"memory/MEMORY.md", "0d8c7e9a-1b2c-4d5e-8f90-a1b2c3d4e5f6/tool-results/toolu_fixture.txt"} {
					if _, err := os.Stat(filepath.Join(outcome.Residue[0], rel)); err != nil {
						t.Fatalf("residue evidence %s removed: %v", rel, err)
					}
				}
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

func TestClaudeAdapter_SpillIsRecordedAndCleaned(t *testing.T) {
	projects := claudeEnvSetup(t)
	c, req := setupController(t), workflowRequest()
	for _, role := range []string{"producer", "dev"} {
		useClaudePhase(t, req, role, "spill")
	}
	r, err := c.Run(t.Context(), req)
	if err != nil || r.State != "awaiting_review" {
		t.Fatalf("spilling run failed: %+v: %v", r, err)
	}
	for _, phase := range []string{"producer", "dev"} {
		var outcome PhaseOutcome
		b, _ := os.ReadFile(filepath.Join(r.Dir, "outputs", phase, "adapter-outcome.json"))
		want := []SpillFile{{Path: "0d8c7e9a-1b2c-4d5e-8f90-a1b2c3d4e5f6/tool-results/toolu_fixture.txt", Size: 20906}}
		if json.Unmarshal(b, &outcome) != nil || outcome.FailureReason != "" || len(outcome.Residue) != 0 || !slices.Equal(outcome.SpillFiles, want) {
			t.Fatalf("%s outcome: %s", phase, b)
		}
		if strings.Contains(string(b), "xxxx") {
			t.Fatalf("%s outcome holds spill content: %s", phase, b)
		}
	}
	if entries, err := os.ReadDir(projects); err != nil || len(entries) != 0 {
		t.Fatalf("spill entry not removed: %v: %v", entries, err)
	}
}

// A Claude event stream larger than the 1 MiB general output cap (here about
// 2.5 MiB before the result) still reaches its result event (acf-73 run 3aeced99).
func TestClaudeAdapter_LargeStreamReachesResult(t *testing.T) {
	claudeEnvSetup(t)
	c, req := setupController(t), workflowRequest()
	useClaudePhase(t, req, "dev", "big-stream")
	r, err := c.Run(t.Context(), req)
	if err != nil || r.State != "awaiting_review" {
		t.Fatalf("large stream run failed: %+v: %v", r, err)
	}
	info, err := os.Stat(filepath.Join(r.Dir, "outputs", "dev", "claude.jsonl"))
	if err != nil || info.Size() <= outputLimit {
		t.Fatalf("stream not larger than the general cap, test proves nothing: %v %v", info, err)
	}
}

// A Claude phase that never finishes hits its command timeout (fixtureCommand:
// 5s). The adapter must stop it at its own, earlier deadline and still audit
// the reserved projects entry and the private launch dir: verified spills
// (and the empty memory/ dir of the live occurrence) are cleaned, anything
// else is kept and recorded as residue (acf-67).
func TestClaudeAdapter_TimeoutStillAudits(t *testing.T) {
	spill := "0d8c7e9a-1b2c-4d5e-8f90-a1b2c3d4e5f6/tool-results/toolu_fixture.txt"
	for _, mode := range []string{"hang-spill", "hang-residue"} {
		t.Run(mode, func(t *testing.T) {
			projects := claudeEnvSetup(t)
			c, req := setupController(t), workflowRequest()
			useClaudePhase(t, req, "producer", mode)
			r, err := c.Run(t.Context(), req)
			if err == nil || r.State == "awaiting_review" || len(r.Evidence) == 0 {
				t.Fatalf("hanging phase accepted: %+v", r)
			}
			last := r.Evidence[len(r.Evidence)-1]
			if last.Role != "producer" {
				t.Fatalf("run failed in %s, want producer: %+v", last.Role, r.Evidence)
			}
			// Exit code 61 is the agent fixture reporting RunPhase's error: the
			// adapter exited by itself rather than being SIGKILLed by the controller.
			log, _ := os.ReadFile(last.Log)
			if last.ExitCode != 61 || !strings.Contains(string(log), "failure_reason=timeout") {
				t.Fatalf("adapter did not stop at its own deadline (exit %d): %s", last.ExitCode, log)
			}
			var outcome PhaseOutcome
			b, err := os.ReadFile(filepath.Join(last.OutputDir, "adapter-outcome.json"))
			if err != nil || json.Unmarshal(b, &outcome) != nil || outcome.Runtime != "claude" || outcome.FailureReason != "timeout" {
				t.Fatalf("outcome not recorded after timeout: %s: %v", b, err)
			}
			var manifest struct {
				LaunchDir string `json:"launch_dir"`
			}
			b, _ = os.ReadFile(filepath.Join(last.OutputDir, "adapter.json"))
			if json.Unmarshal(b, &manifest) != nil || manifest.LaunchDir == "" {
				t.Fatalf("manifest: %s", b)
			}
			if _, err := os.Stat(manifest.LaunchDir); !os.IsNotExist(err) {
				t.Fatalf("private launch dir survived the timeout: %v", err)
			}
			entries, err := os.ReadDir(projects)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "hang-spill":
				if len(entries) != 0 || len(outcome.Residue) != 0 || !slices.Equal(outcome.SpillFiles, []SpillFile{{Path: spill, Size: 20906}}) {
					t.Fatalf("spill-only entry not cleaned after timeout: %v: %+v", entries, outcome)
				}
			case "hang-residue":
				if len(entries) != 1 || len(outcome.Residue) != 1 || outcome.Residue[0] != filepath.Join(projects, entries[0].Name()) || len(outcome.SpillFiles) != 0 {
					t.Fatalf("residue not recorded after timeout: %v: %+v", entries, outcome)
				}
				for _, rel := range []string{"memory/MEMORY.md", spill} {
					if _, err := os.Stat(filepath.Join(outcome.Residue[0], rel)); err != nil {
						t.Fatalf("residue evidence %s removed: %v", rel, err)
					}
				}
			}
		})
	}
}

func TestAdapterDeadline(t *testing.T) {
	start := time.Unix(1000, 0)
	for _, tc := range []struct {
		value string
		ok    bool
		want  time.Duration
		bad   bool
	}{
		{value: ""},
		{value: "nonsense", bad: true},
		{value: "0s", bad: true},
		{value: "-5s", bad: true},
		{value: "5s", ok: true, want: 4 * time.Second},
		{value: "5m", ok: true, want: 5*time.Minute - 30*time.Second},
		{value: "100ms", ok: true, want: 80 * time.Millisecond},
	} {
		got, ok, err := adapterDeadline(start, tc.value)
		if (err != nil) != tc.bad || ok != tc.ok || (ok && got.Sub(start) != tc.want) {
			t.Fatalf("%q: got %v %v %v", tc.value, got.Sub(start), ok, err)
		}
	}
}

// A run that fails after the projects entry is reserved but before the model
// runs (here `claude --version` fails) must not leave the empty entry behind.
func TestClaudeAdapter_EarlyFailureReleasesReservedEntry(t *testing.T) {
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no false binary")
	}
	projects := claudeEnvSetup(t)
	c, req := setupController(t), workflowRequest()
	useClaudePhase(t, req, "producer", "")
	policy := req.Config.Workflow.Phases[0].Command.Argv[len(req.Config.Workflow.Phases[0].Command.Argv)-1]
	b, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.Replace(string(b), `"executable":"`+os.Args[0]+`"`, `"executable":"`+falseBin+`"`, 1))
	if err := os.WriteFile(policy, b, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := c.Run(t.Context(), req)
	if err == nil || r.State == "awaiting_review" {
		t.Fatalf("failing --version accepted: %+v", r)
	}
	log, _ := os.ReadFile(r.Evidence[len(r.Evidence)-1].Log)
	if !strings.Contains(string(log), "claude --version failed") {
		t.Fatalf("run did not fail at --version: %s", log)
	}
	if entries, err := os.ReadDir(projects); err != nil || len(entries) != 0 {
		t.Fatalf("reserved entry left behind: %v: %v", entries, err)
	}
}
