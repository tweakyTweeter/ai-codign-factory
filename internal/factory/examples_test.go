package factory

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestExampleConfigs_StayCompatibleWithParser(t *testing.T) {
	for _, name := range []string{"factory.json", "supervised.json"} {
		if _, err := LoadConfig(filepath.Join("..", "..", "examples", name)); err != nil {
			t.Fatalf("example %s: %v", name, err)
		}
	}
	policies, err := filepath.Glob(filepath.Join("..", "..", "examples", "*-policy.json"))
	if err != nil || len(policies) == 0 {
		t.Fatalf("no example policies: %v", err)
	}
	for _, path := range policies {
		data, err := readBounded(path)
		if err != nil {
			t.Fatal(err)
		}
		var probe struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPhasePolicy(data, probe.Role); err != nil {
			t.Fatalf("example policy %s: %v", path, err)
		}
	}
}
