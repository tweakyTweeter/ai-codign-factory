package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func v2JSON(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	p := map[string]any{"version": 2, "runtime": "claude", "executable": "/abs/claude", "role": "dev", "agent": "factory-dev", "source_profile": "/abs/factory-dev.agent.md", "grants": []string{"read", "write"}}
	if edit != nil {
		edit(p)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLoadPhasePolicy_V2Valid(t *testing.T) {
	p, err := LoadPhasePolicy(v2JSON(t, func(m map[string]any) { m["model"] = "claude-sonnet-5-5" }), "dev")
	if err != nil || p.Runtime != "claude" || p.Copilot != nil || p.Model != "claude-sonnet-5-5" || len(p.Grants) != 2 {
		t.Fatalf("valid v2 rejected or misread: %+v: %v", p, err)
	}
}

func TestLoadPhasePolicy_V1IsCopilot(t *testing.T) {
	v1 := `{"version":1,"executable":"/abs/copilot","role":"qa","agent":"qa","source_profile":"/abs/qa.agent.md","available_tools":["view"],"allow_tools":[]}`
	p, err := LoadPhasePolicy([]byte(v1), "qa")
	if err != nil || p.Runtime != "copilot" || p.Copilot == nil || p.Copilot.AvailableTools[0] != "view" {
		t.Fatalf("v1 not decoded as Copilot: %+v: %v", p, err)
	}
}

func TestLoadPhasePolicy_V2RejectsUnsafe(t *testing.T) {
	for name, tc := range map[string]struct {
		role string
		edit func(map[string]any)
	}{
		"unknown runtime":  {"dev", func(m map[string]any) { m["runtime"] = "codex" }},
		"role mismatch":    {"qa", nil},
		"unknown role":     {"boss", func(m map[string]any) { m["role"] = "boss" }},
		"unsafe agent":     {"dev", func(m map[string]any) { m["agent"] = "../dev" }},
		"relative exe":     {"dev", func(m map[string]any) { m["executable"] = "claude" }},
		"relative profile": {"dev", func(m map[string]any) { m["source_profile"] = "dev.md" }},
		"no grants":        {"dev", func(m map[string]any) { m["grants"] = []string{} }},
		"missing read":     {"dev", func(m map[string]any) { m["grants"] = []string{"write"} }},
		"duplicate grant":  {"dev", func(m map[string]any) { m["grants"] = []string{"read", "read"} }},
		"unknown grant":    {"dev", func(m map[string]any) { m["grants"] = []string{"read", "shell"} }},
		"write on qa":      {"qa", func(m map[string]any) { m["role"] = "qa" }},
		"bad model":        {"dev", func(m map[string]any) { m["model"] = "opus; rm -rf" }},
		"copilot model":    {"dev", func(m map[string]any) { m["runtime"] = "copilot"; m["model"] = "gpt-5" }},
		"unknown field":    {"dev", func(m map[string]any) { m["allow_all"] = true }},
		"v1 tools in v2":   {"dev", func(m map[string]any) { m["available_tools"] = []string{"view"} }},
		"version 3":        {"dev", func(m map[string]any) { m["version"] = 3 }},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadPhasePolicy(v2JSON(t, tc.edit), tc.role); err == nil {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
}

func TestLoadPhasePolicy_ErrorsAreClassified(t *testing.T) {
	_, err := LoadPhasePolicy(v2JSON(t, func(m map[string]any) { m["runtime"] = "codex" }), "dev")
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("unclear error: %v", err)
	}
}

func TestLoadPhasePolicy_WriteOnQaFailsForWrite(t *testing.T) {
	_, err := LoadPhasePolicy(v2JSON(t, func(m map[string]any) { m["role"] = "qa" }), "qa")
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Fatalf("expected write rejection, got: %v", err)
	}
}

func TestLoadPhasePolicy_ModelPattern(t *testing.T) {
	long64 := "a" + strings.Repeat("b", 63)
	for model, ok := range map[string]bool{
		"-p": false, "--dangerously-skip-permissions": false, ".hidden": false, "_x": false,
		" ": false, long64 + "c": false,
		"opus": true, "claude-sonnet-5-5": true, "a": true, long64: true,
	} {
		_, err := LoadPhasePolicy(v2JSON(t, func(m map[string]any) { m["model"] = model }), "dev")
		if (err == nil) != ok {
			t.Errorf("model %q: accepted=%v want %v (%v)", model, err == nil, ok, err)
		}
	}
}

func TestLoadPhasePolicy_EdgeCases(t *testing.T) {
	reject := map[string]struct {
		body string
		role string
	}{
		"duplicate version key":  {`{"version":2,"version":2,"runtime":"claude","executable":"/a/c","role":"dev","agent":"factory-dev","source_profile":"/a/p.md","grants":["read"]}`, "dev"},
		"case-variant key":       {`{"version":2,"runtime":"claude","executable":"/a/c","role":"dev","agent":"factory-dev","source_profile":"/a/p.md","Grants":["read"]}`, "dev"},
		"grants null":            {`{"version":2,"runtime":"claude","executable":"/a/c","role":"dev","agent":"factory-dev","source_profile":"/a/p.md","grants":null}`, "dev"},
		"v1 body with version 2": {`{"version":2,"executable":"/a/c","role":"qa","agent":"qa","source_profile":"/a/p.md","available_tools":["view"],"allow_tools":[]}`, "qa"},
		"v2 body with version 1": {`{"version":1,"runtime":"claude","executable":"/a/c","role":"dev","agent":"factory-dev","source_profile":"/a/p.md","grants":["read"]}`, "dev"},
		"specialist write":       {`{"version":2,"runtime":"claude","executable":"/a/c","role":"specialist","agent":"s","source_profile":"/a/p.md","grants":["read","write"]}`, "specialist"},
	}
	for name, tc := range reject {
		t.Run("reject "+name, func(t *testing.T) {
			if _, err := LoadPhasePolicy([]byte(tc.body), tc.role); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	t.Run("whitespace model", func(t *testing.T) {
		if _, err := LoadPhasePolicy(v2JSON(t, func(m map[string]any) { m["model"] = " " }), "dev"); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("specialist read accepted", func(t *testing.T) {
		b := `{"version":2,"runtime":"claude","executable":"/a/c","role":"specialist","agent":"s","source_profile":"/a/p.md","grants":["read"]}`
		if _, err := LoadPhasePolicy([]byte(b), "specialist"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("copilot v2 without model", func(t *testing.T) {
		p, err := LoadPhasePolicy(v2JSON(t, func(m map[string]any) { m["runtime"] = "copilot"; m["role"] = "qa"; m["grants"] = []string{"read"} }), "qa")
		if err != nil || p.Runtime != "copilot" || p.Copilot != nil {
			t.Fatalf("%+v %v", p, err)
		}
	})
}
