package factory

import (
	"os"
	"strings"
	"testing"
)

func TestMergedEnv_PreservesBeadsAndReplacesFactoryContext(t *testing.T) {
	t.Setenv("BD_ACTOR", "fixture-actor")
	t.Setenv("FACTORY_RUN_ID", "stale")
	t.Setenv("FACTORY_UNKNOWN", "stale")
	result := mergedEnv([]string{"FACTORY_RUN_ID=current"})
	env := strings.Join(result, "\n")
	if !strings.Contains(env, "BD_ACTOR=fixture-actor") || !strings.Contains(env, "FACTORY_RUN_ID=current") ||
		strings.Contains(env, "FACTORY_RUN_ID=stale") || strings.Contains(env, "FACTORY_UNKNOWN=") {
		t.Fatalf("unexpected environment handling (keys only): inherited %d entries, output %d entries", len(os.Environ()), len(result))
	}
}
