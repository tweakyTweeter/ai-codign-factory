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

func TestInspectPhasePolicy(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "p.md")
	os.WriteFile(profile, []byte("x"), 0600)
	policy := filepath.Join(dir, "policy.json")
	os.WriteFile(policy, []byte(`{"version":2,"runtime":"claude","executable":"`+os.Args[0]+`","role":"qa","agent":"p","source_profile":"`+profile+`","grants":["read"]}`), 0600)
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
