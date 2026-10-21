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
