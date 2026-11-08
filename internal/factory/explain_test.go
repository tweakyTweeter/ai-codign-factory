package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyRoleContract_DevExplainIsOptionalAndBounded covers the acf-73 Dev
// outcome: explain is optional (Copilot, older runs), and when present each
// field must hold 1 to 300 characters with no unknown keys.
func TestApplyRoleContract_DevExplainIsOptionalAndBounded(t *testing.T) {
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-q")
	writeFile(t, filepath.Join(worktree, "a.txt"), "a\n")
	runGit(t, worktree, "add", ".")
	runGit(t, worktree, "commit", "-qm", "base")
	long := strings.Repeat("é", 300) // 300 characters, 600 bytes
	for _, tc := range []struct {
		name, content string
		want          *Explain
		ok            bool
	}{
		{"absent", `{"version":1,"status":"complete","summary":"done"}`, nil, true},
		{"null", `{"version":1,"status":"complete","summary":"done","explain":null}`, nil, true},
		{"present", `{"version":1,"status":"complete","summary":"done","explain":{"what_changed":"Adds a status line.","why":"Runs looked stuck.","risk":"Low."}}`,
			&Explain{WhatChanged: "Adds a status line.", Why: "Runs looked stuck.", Risk: "Low."}, true},
		{"300 characters", `{"version":1,"status":"complete","summary":"done","explain":{"what_changed":"` + long + `","why":"w","risk":"r"}}`,
			&Explain{WhatChanged: long, Why: "w", Risk: "r"}, true},
		{"301 characters", `{"version":1,"status":"complete","summary":"done","explain":{"what_changed":"` + long + `x","why":"w","risk":"r"}}`, nil, false},
		{"empty field", `{"version":1,"status":"complete","summary":"done","explain":{"what_changed":"a","why":"","risk":"r"}}`, nil, false},
		{"blank field", `{"version":1,"status":"complete","summary":"done","explain":{"what_changed":"a","why":"w","risk":"  "}}`, nil, false},
		{"missing field", `{"version":1,"status":"complete","summary":"done","explain":{"what_changed":"a","why":"w"}}`, nil, false},
		{"unknown key", `{"version":1,"status":"complete","summary":"done","explain":{"what_changed":"a","why":"w","risk":"r","approve":"yes"}}`, nil, false},
		{"not an object", `{"version":1,"status":"complete","summary":"done","explain":"plain"}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := filepath.Join(t.TempDir(), "result.json")
			err := applyRoleContract(t.Context(), "dev", result, worktree, tc.content)
			if !tc.ok {
				if err == nil {
					t.Fatal("invalid explain accepted")
				}
				if _, statErr := os.Stat(result); !os.IsNotExist(statErr) {
					t.Fatal("result written for a rejected outcome")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(result)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == nil && strings.Contains(string(b), "explain") {
				t.Fatalf("absent explain changed the verdict bytes: %s", b)
			}
			var raw map[string]json.RawMessage
			if json.Unmarshal(b, &raw) != nil {
				t.Fatalf("bad result: %s", b)
			}
			fp, err := Fingerprint(t.Context(), worktree)
			if err != nil {
				t.Fatal(err)
			}
			v, err := readVerdict(result, fp)
			if err != nil {
				t.Fatal(err)
			}
			if (v.Explain == nil) != (tc.want == nil) || (v.Explain != nil && *v.Explain != *tc.want) {
				t.Fatalf("explain = %+v, want %+v", v.Explain, tc.want)
			}
		})
	}
}

// TestReadVerdict_BoundsExplain keeps a hand-written result file from
// bypassing the explain bounds the Dev contract enforces.
func TestReadVerdict_BoundsExplain(t *testing.T) {
	write := func(explain string) string {
		path := filepath.Join(t.TempDir(), "result.json")
		body := `{"version":1,"verdict":"pass","summary":"s","fingerprint":"fp","findings":[]` + explain + `}`
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, err := readVerdict(write(""), "fp"); err != nil {
		t.Fatalf("verdict without explain rejected: %v", err)
	}
	if _, err := readVerdict(write(`,"explain":{"what_changed":"a","why":"b","risk":"c"}`), "fp"); err != nil {
		t.Fatalf("valid explain rejected: %v", err)
	}
	for _, bad := range []string{
		`,"explain":{"what_changed":"` + strings.Repeat("x", 301) + `","why":"b","risk":"c"}`,
		`,"explain":{"what_changed":"","why":"b","risk":"c"}`,
		`,"explain":{"what_changed":"a","why":"b","risk":"c","extra":"d"}`,
	} {
		if _, err := readVerdict(write(bad), "fp"); err == nil {
			t.Errorf("invalid explain accepted: %s", bad)
		}
	}
}

// TestRoleInstructions_DevPromptUnchangedForCopilot pins that the plain-language
// request is a separate, Claude-only addition: the shared Dev instructions
// (the Copilot prompt) never mention explain.
func TestRoleInstructions_DevPromptUnchangedForCopilot(t *testing.T) {
	shared := roleInstructions("dev", "/in", "/in/c.json", "/w", "task")
	if strings.Contains(shared, "explain") {
		t.Fatalf("shared Dev instructions mention explain:\n%s", shared)
	}
	for _, want := range []string{`"explain"`, "what_changed", "why", "risk", "300 characters", "not be a developer"} {
		if !strings.Contains(devExplainInstructions, want) {
			t.Errorf("Claude Dev explain instructions omit %q", want)
		}
	}
}
