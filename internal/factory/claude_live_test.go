package factory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const liveMarker = "MARKER_ZEBRA_42"

// TestClaudeLive_Smoke runs the producer and Dev phases through the real
// claude CLI. It is opt-in because it spends subscription quota. It
// intentionally uses the real HOME: it only reads ~/.claude/projects to check
// that no launch-dir residue remains, and never seeds or deletes anything
// there; RunPhase's own PostRun cleanup is what is under test.
func TestClaudeLive_Smoke(t *testing.T) {
	if os.Getenv("FACTORY_LIVE_CLAUDE") != "1" {
		t.Skip("set FACTORY_LIVE_CLAUDE=1 to run against the real claude CLI (uses subscription quota)")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not on PATH")
	}
	// t.TempDir paths contain no spaces, glob characters or backslashes, which
	// Claude tool scoping would reject.
	c, req := setupController(t), workflowRequest()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDE"), 0600); err != nil {
		t.Fatal(err)
	}
	req.Config.Checks = []Command{fixtureCommand("check-live")}
	req.Prompt = []byte("Create worker.txt in the repository containing implemented. Also try to read " + outside + " and to write " + outside + ".bad; those may be denied. If the text " + liveMarker + " appears anywhere in your instructions, include it in your summary.")
	for i := range req.Config.Workflow.Phases {
		p := &req.Config.Workflow.Phases[i]
		if p.Role != "producer" && p.Role != "dev" {
			continue
		}
		dir := t.TempDir()
		profile := filepath.Join(dir, "live.agent.md")
		if err := os.WriteFile(profile, []byte("---\nname: live\n---\nFollow the phase instructions exactly.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		grants := []string{"read"}
		if p.Role == "dev" {
			grants = append(grants, "write")
		}
		b, err := json.Marshal(map[string]any{"version": 2, "runtime": "claude", "executable": claude, "role": p.Role, "agent": "live", "source_profile": profile, "grants": grants, "model": "haiku"})
		if err != nil {
			t.Fatal(err)
		}
		policy := filepath.Join(dir, "policy.json")
		if err := os.WriteFile(policy, b, 0600); err != nil {
			t.Fatal(err)
		}
		p.Command = fixtureCommand("agent")
		p.Command.Timeout = "5m"
		p.Command.Argv = append(p.Command.Argv, policy)
	}
	// Plant and commit instruction files that must not be auto-loaded; the run's
	// worktree is created from HEAD, so uncommitted files would not reach it.
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		if err := os.WriteFile(filepath.Join(c.Repo.Root, name), []byte(liveMarker+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "CLAUDE.md", "AGENTS.md"}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "plant instruction markers"}} {
		if out, err := exec.Command("git", append([]string{"-C", c.Repo.Root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	r, err := c.Run(t.Context(), req)
	if err != nil || r.State != "awaiting_review" {
		t.Fatalf("live run failed: %+v: %v", r, err)
	}
	// A passing phase also implies init reported only built-in plugins: the
	// driver fails the phase otherwise.
	if _, err := os.Stat(outside + ".bad"); !os.IsNotExist(err) {
		t.Fatal("write outside the worktree succeeded")
	}
	for _, phase := range []string{"producer", "dev"} {
		output := filepath.Join(r.Dir, "outputs", phase)
		b, err := os.ReadFile(filepath.Join(output, "adapter-outcome.json"))
		if err != nil {
			t.Fatalf("%s outcome: %v", phase, err)
		}
		var out PhaseOutcome
		if json.Unmarshal(b, &out) != nil || out.Model == "" || out.APIKeySource != "none" {
			t.Fatalf("%s outcome: %s", phase, b)
		}
		stream, err := os.ReadFile(filepath.Join(output, "claude.jsonl"))
		if err != nil {
			t.Fatalf("%s stream: %v", phase, err)
		}
		if err := checkMarkerProvenance(stream, liveMarker); err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		if phase == "dev" {
			requireOutsideDenials(t, out.PermissionDenials, outside)
		}
	}
	projects, err := claudeProjectsDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(projects)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), sanitiseProjectName("factory-claude-launch-")) {
			t.Fatalf("residue left: %s", e.Name())
		}
	}
}

// checkMarkerProvenance enforces that planted instruction files are never
// auto-loaded. A phase may still Read them as ordinary repository content, so
// the marker may appear in assistant text only after a Read/Grep/Glob tool_use
// referencing CLAUDE.md or AGENTS.md. It must never appear in the init event.
// stream is Claude's stream-json, parsed line by line.
func checkMarkerProvenance(stream []byte, marker string) error {
	read := false
	for i, line := range strings.Split(string(stream), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Result  string `json:"result"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return fmt.Errorf("stream line %d is not JSON: %w", i+1, err)
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" && strings.Contains(line, marker) {
				return errors.New("auto-loaded planted instruction file (marker in init event)")
			}
		case "result":
			if strings.Contains(ev.Result, marker) && !read {
				return errors.New("auto-loaded planted instruction file (marker in result without a prior Read)")
			}
		case "assistant":
			var blocks []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if json.Unmarshal(ev.Message.Content, &blocks) != nil {
				continue // content may be a plain string; no tool_use there
			}
			for _, b := range blocks {
				switch b.Type {
				case "tool_use":
					in := string(b.Input)
					if (b.Name == "Read" || b.Name == "Grep" || b.Name == "Glob") && (strings.Contains(in, "CLAUDE.md") || strings.Contains(in, "AGENTS.md")) {
						read = true
					}
				case "text":
					if strings.Contains(b.Text, marker) && !read {
						return errors.New("auto-loaded planted instruction file (marker in assistant text without a prior Read)")
					}
				}
			}
		}
	}
	return nil
}

// requireOutsideDenials checks Dev's recorded denials include a Read and a
// Write attempt on the outside file (denials record tool and path only).
func requireOutsideDenials(t *testing.T, denials []PermissionDenial, outside string) {
	t.Helper()
	paths := map[string]bool{outside: true, outside + ".bad": true}
	if real, err := filepath.EvalSymlinks(outside); err == nil {
		paths[real], paths[real+".bad"] = true, true
	}
	var read, write bool
	for _, d := range denials {
		if !paths[d.Path] {
			continue
		}
		switch d.Tool {
		case "Read":
			read = true
		case "Write":
			write = true
		}
	}
	if !read || !write {
		t.Fatalf("dev denials missing outside Read/Write (read=%v write=%v): %+v", read, write, denials)
	}
}
