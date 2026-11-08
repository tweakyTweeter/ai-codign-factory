package factory

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteStreamsEnv_ExactEnvWhenNotInheriting(t *testing.T) {
	t.Setenv("FACTORY_LEAK_PROBE", "leak")
	var out bytes.Buffer
	code, err := executeStreamsEnv(t.Context(), t.TempDir(), []string{"/usr/bin/env"}, []string{"ONLY=1"}, &out, &out, nil, true, false)
	if err != nil || code != 0 || strings.TrimSpace(out.String()) != "ONLY=1" {
		t.Fatalf("env not exact: %q (%d): %v", out.String(), code, err)
	}
}

func TestRunPhase_CopilotEntryRejectsOtherRuntime(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "p.md")
	os.WriteFile(profile, []byte("x"), 0600)
	policy := filepath.Join(dir, "policy.json")
	os.WriteFile(policy, []byte(`{"version":2,"runtime":"claude","executable":"/abs/claude","role":"producer","agent":"p","source_profile":"`+profile+`","grants":["read"]}`), 0600)
	out := filepath.Join(dir, "out")
	t.Setenv("FACTORY_ROLE", "producer")
	t.Setenv("FACTORY_OUTPUT_DIR", out)
	t.Setenv("FACTORY_RESULT_FILE", filepath.Join(out, "result.json"))
	t.Setenv("FACTORY_WORKTREE", dir)
	t.Setenv("FACTORY_CONTEXT_FILE", filepath.Join(dir, "context.json"))
	err := RunPhase(t.Context(), policy, "copilot")
	if err == nil || !strings.Contains(err.Error(), "failure_reason=policy") {
		t.Fatalf("runtime conflict accepted: %v", err)
	}
}

// writeValidRelativePolicy creates a valid policy for role at name inside a
// fresh cwd, so a test can prove a relative path is refused rather than read.
func writeValidRelativePolicy(t *testing.T, runtime, role string) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	profile := filepath.Join(dir, "p.md")
	if err := os.WriteFile(profile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	body := `{"version":2,"runtime":"` + runtime + `","executable":"` + os.Args[0] + `","role":"` + role + `","agent":"p","source_profile":"` + profile + `","grants":["read"]}`
	if err := os.WriteFile(filepath.Join(dir, "policy.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunPhase_RejectsRelativePolicy(t *testing.T) {
	for _, expect := range []string{"", "copilot"} {
		for _, rel := range []string{"policy.json", "./policy.json"} {
			t.Run(expect+"/"+rel, func(t *testing.T) {
				runtime := "claude"
				if expect == "copilot" {
					runtime = "copilot"
				}
				dir := writeValidRelativePolicy(t, runtime, "producer")
				out := filepath.Join(dir, "out")
				t.Setenv("FACTORY_ROLE", "producer")
				t.Setenv("FACTORY_OUTPUT_DIR", out)
				t.Setenv("FACTORY_RESULT_FILE", filepath.Join(out, "result.json"))
				t.Setenv("FACTORY_WORKTREE", dir)
				t.Setenv("FACTORY_CONTEXT_FILE", filepath.Join(dir, "context.json"))
				err := RunPhase(t.Context(), rel, expect)
				if err == nil || !strings.HasPrefix(err.Error(), "failure_reason=policy:") || !strings.Contains(err.Error(), "absolute") || !strings.Contains(err.Error(), rel) {
					t.Fatalf("relative policy %q accepted or misreported: %v", rel, err)
				}
				if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
					t.Fatalf("output dir touched for a refused policy: %v", statErr)
				}
			})
		}
	}
}

func TestInspectPhasePolicy_RejectsRelativePolicy(t *testing.T) {
	writeValidRelativePolicy(t, "copilot", "qa")
	for _, rel := range []string{"policy.json", "./policy.json"} {
		for _, args := range [][]string{{"--policy", rel}, {"--policy=" + rel}, {"-policy", rel}, {"-policy=" + rel}} {
			for _, sub := range []string{"agent", "copilot"} {
				argv := append([]string{"/abs/factory", sub}, args...)
				got, err := InspectPhasePolicy(t.Context(), "qa", "qa", argv)
				if err == nil || got != nil || !strings.Contains(err.Error(), "failure_reason=policy") || !strings.Contains(err.Error(), "absolute") || !strings.Contains(err.Error(), rel) {
					t.Fatalf("%v: relative policy accepted or misreported: %+v: %v", argv, got, err)
				}
			}
		}
	}
	// The same policy through its absolute path is still accepted.
	abs, err := filepath.Abs("policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := InspectPhasePolicy(t.Context(), "qa", "qa", []string{"/abs/factory", "copilot", "--policy", abs}); err != nil || got == nil || got.Runtime != "copilot" {
		t.Fatalf("absolute policy: %+v: %v", got, err)
	}
}

func TestInspectPhasePolicy(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "p.md")
	if err := os.WriteFile(profile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policy, []byte(`{"version":2,"runtime":"claude","executable":"`+os.Args[0]+`","role":"qa","agent":"p","source_profile":"`+profile+`","grants":["read"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := InspectPhasePolicy(t.Context(), "qa", "qa", []string{"/abs/factory", "agent", "--policy", policy})
	if err != nil || got == nil || got.Runtime != "claude" || !strings.Contains(got.Version, "2.1.306") {
		t.Fatalf("inspect: %+v: %v", got, err)
	}
	if got, err := InspectPhasePolicy(t.Context(), "check", "qa", []string{"go", "test"}); got != nil || err != nil {
		t.Fatalf("non-adapter phase inspected: %+v: %v", got, err)
	}
	if _, err := InspectPhasePolicy(t.Context(), "dev", "dev", []string{"/abs/factory", "agent", "--policy", policy}); err == nil {
		t.Fatal("role mismatch accepted by doctor")
	}
}

func TestInspectPhasePolicy_FlagSpellings(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "p.md")
	if err := os.WriteFile(profile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policy, []byte(`{"version":2,"runtime":"claude","executable":"`+os.Args[0]+`","role":"qa","agent":"p","source_profile":"`+profile+`","grants":["read"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--policy", policy}, {"--policy=" + policy}, {"-policy", policy}, {"-policy=" + policy}} {
		{
			argv := append([]string{"/abs/factory", "agent"}, args...)
			got, err := InspectPhasePolicy(t.Context(), "qa", "qa", argv)
			if err != nil || got == nil || got.Runtime != "claude" {
				t.Fatalf("%v: %+v: %v", argv, got, err)
			}
			if _, err := InspectPhasePolicy(t.Context(), "dev", "dev", argv); err == nil {
				t.Fatalf("%v: role mismatch accepted", argv)
			}
		}
	}
	for _, args := range [][]string{{"--policy"}, {"--policy", ""}, {"--policy="}, {"-policy="}, {}, {"--bogus", "x"}, {"--policy", policy, "extra"}} {
		for _, sub := range []string{"agent", "copilot"} {
			argv := append([]string{"/abs/factory", sub}, args...)
			if got, err := InspectPhasePolicy(t.Context(), "qa", "qa", argv); err == nil {
				t.Fatalf("%v accepted: %+v", argv, got)
			}
		}
	}
	for _, argv := range [][]string{{"othertool", "--policy", "/nope"}, {"/abs/factory", "doctor", "--policy", "/nope"}, {"/abs/factory"}, nil} {
		if got, err := InspectPhasePolicy(t.Context(), "x", "qa", argv); got != nil || err != nil {
			t.Fatalf("%v treated as policy-driven: %+v: %v", argv, got, err)
		}
	}
}
