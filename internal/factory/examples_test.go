package factory

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleConfigs_StayCompatibleWithParser(t *testing.T) {
	for _, name := range []string{"factory.json", "supervised.json", "supervised-claude.json"} {
		if _, err := LoadConfig(filepath.Join("..", "..", "examples", name)); err != nil {
			t.Fatalf("example %s: %v", name, err)
		}
	}
	policies, err := filepath.Glob(filepath.Join("..", "..", "examples", "*-policy.json"))
	if err != nil || len(policies) == 0 {
		t.Fatalf("no example policies: %v", err)
	}
	covered := map[string]bool{}
	for _, path := range policies {
		data, err := readBounded(path)
		if err != nil {
			t.Fatal(err)
		}
		role := strings.TrimSuffix(filepath.Base(path), "-policy.json")
		if i := strings.LastIndex(role, "-"); i >= 0 {
			role = role[i+1:]
		}
		switch role {
		case "producer", "dev", "qa", "review", "specialist":
		default:
			t.Fatalf("example %s: role %q not derivable from filename", path, role)
		}
		covered[role] = true
		if _, err := LoadPhasePolicy(data, role); err != nil {
			t.Fatalf("example policy %s: %v", path, err)
		}
	}
	for _, r := range []string{"producer", "dev", "qa", "review"} {
		if !covered[r] {
			t.Errorf("no example policy for role %s", r)
		}
	}
}
