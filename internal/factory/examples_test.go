package factory

import (
	"path/filepath"
	"testing"
)

func TestExampleConfigs_StayCompatibleWithParser(t *testing.T) {
	for _, name := range []string{"factory.json", "supervised.json"} {
		if _, err := LoadConfig(filepath.Join("..", "..", "examples", name)); err != nil {
			t.Fatalf("example %s: %v", name, err)
		}
	}
	for _, role := range []string{"producer", "dev", "qa", "review"} {
		data, err := readBounded(filepath.Join("..", "..", "examples", role+"-policy.json"))
		if err != nil {
			t.Fatal(err)
		}
		var p CopilotPolicy
		if err := decodeStrict(data, &p); err != nil {
			t.Fatal(err)
		}
		if err := validatePolicy(p, role); err != nil {
			t.Fatalf("example policy %s: %v", role, err)
		}
	}
}
