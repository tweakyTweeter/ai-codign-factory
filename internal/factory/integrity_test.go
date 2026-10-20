package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflow_RejectsChangedControllerInputsAndCompletedArtifacts(t *testing.T) {
	for _, path := range []string{
		"prompt.txt", "config.json", "context.json", "task.json", "claim.json",
		"outputs/dev/context.json", "outputs/producer/response.txt",
		"outputs/producer/injected.txt", "phase-producer.log",
	} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("GO_TAMPER_PATH", path)
			c, req := setupController(t), workflowRequest()
			req.Config.Workflow.Phases[1].Command = fixtureCommand("phase-tamper")
			r, err := c.Run(t.Context(), req)
			if err == nil || !strings.Contains(err.Error(), "evidence integrity changed") || r.State == "awaiting_review" {
				t.Fatalf("tampered %s accepted: %+v: %v", path, r, err)
			}
			if len(r.Evidence) != 2 {
				t.Fatalf("downstream checks/review ran after tampering: %+v", r.Evidence)
			}
			active, err := c.Store.Active(t.Context())
			if err != nil || active.ID != r.ID {
				t.Fatalf("tampering released admission: %+v: %v", active, err)
			}
		})
	}
}

func TestIntegrity_SealsRejectMutationRemovalAdditionModeAndSymlinks(t *testing.T) {
	for _, mutation := range []string{"contents", "remove", "addition", "mode", "symlink-leaf", "symlink-parent"} {
		t.Run(mutation, func(t *testing.T) {
			r := Run{Dir: t.TempDir()}
			dir := filepath.Join(r.Dir, "outputs")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "result.json")
			if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := sealArtifacts(t.Context(), &r, "outputs"); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mutation {
			case "contents":
				err = os.WriteFile(file, []byte("modified"), 0600)
			case "remove":
				err = os.Remove(file)
			case "addition":
				err = os.WriteFile(filepath.Join(dir, "extra"), []byte("added"), 0600)
			case "mode":
				err = os.Chmod(file, 0700)
			case "symlink-leaf":
				if err = os.Remove(file); err == nil {
					err = os.Symlink(filepath.Join(t.TempDir(), "missing"), file)
				}
			case "symlink-parent":
				moved := filepath.Join(r.Dir, "moved")
				if err = os.Rename(dir, moved); err == nil {
					err = os.Symlink(moved, dir)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyIntegrity(t.Context(), r); err == nil {
				t.Fatal("changed evidence accepted")
			}
			if err := sealArtifacts(t.Context(), &r, "outputs"); err == nil {
				t.Fatal("changed evidence was resealed")
			}
		})
	}
}

func TestIntegrity_PersistedSealsProtectReviewAndIntegration(t *testing.T) {
	for _, action := range []string{"review", "publish", "sync", "integrate"} {
		t.Run(action, func(t *testing.T) {
			c, r := publishedReview(t)
			closeReviewGate(t, c, r.HumanReview.ApproveReason)
			if _, err := c.ReviewSync(t.Context(), r.ID); err != nil {
				t.Fatal(err)
			}
			r, err := c.Store.Get(t.Context(), r.ID)
			if err != nil || r.Integrity == nil || r.HumanReview.Decision != "approved" {
				t.Fatalf("seals not persisted: %+v: %v", r.Integrity, err)
			}
			if err := os.WriteFile(filepath.Join(r.Dir, "outputs", "producer", "response.txt"), []byte("rewritten plan"), 0600); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "review":
				r, err = c.Review(t.Context(), r.ID)
			case "publish":
				r, err = c.ReviewPublish(t.Context(), r.ID)
			case "sync":
				r, err = c.ReviewSync(t.Context(), r.ID)
			case "integrate":
				r, err = c.Integrate(t.Context(), r.ID)
			}
			if err == nil || !strings.Contains(err.Error(), "evidence integrity changed") {
				t.Fatalf("%s accepted modified evidence: %v", action, err)
			}
			if r.State == "released" || r.HumanReview.Decision == "approved" {
				t.Fatalf("tampering retained approval or released run: %+v", r)
			}
			persisted, err := c.Store.Get(t.Context(), r.ID)
			if err != nil || persisted.HumanReview.Decision != "not-approved" {
				t.Fatalf("approval revocation not persisted: %+v: %v", persisted.HumanReview, err)
			}
			if _, err := os.Stat(filepath.Join(c.Repo.Root, "worker.txt")); !os.IsNotExist(err) {
				t.Fatalf("tampered evidence integrated: %v", err)
			}
		})
	}
}

func TestIntegrity_LegacyUnsealedRunCannotAcquireApproval(t *testing.T) {
	c, r := reviewRun(t)
	r.Integrity = nil
	if err := c.save(&r, r.State, "legacy unsealed fixture", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReviewPublish(t.Context(), r.ID); err == nil || !strings.Contains(err.Error(), "new verified run") {
		t.Fatalf("unsealed legacy run approved: %v", err)
	}
}

func TestIntegrity_RejectsUnsafeAndOversizedEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large"), make([]byte, outputLimit+1), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".", "..", "../outside", "/absolute", "a/../b", "large"} {
		if _, err := artifactDigest(t.Context(), root, path); err == nil {
			t.Fatalf("invalid evidence accepted: %q", path)
		}
	}
}

func TestCopilotInputs_PrivateCopiesRebaseReferencesAndExcludeLiveState(t *testing.T) {
	_, r := reviewRun(t)
	launch := t.TempDir()
	path := filepath.Join(r.Dir, "outputs", "review", "context.json")
	inputs, err := copilotInputs(t.Context(), launch, path, r.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	b, err := readBounded(filepath.Join(inputs, "outputs", "review", "context.json"))
	if err != nil {
		t.Fatal(err)
	}
	var live struct {
		Run Run `json:"run"`
	}
	if err := json.Unmarshal(b, &live); err != nil {
		t.Fatal(err)
	}
	if live.Run.Dir != inputs || live.Run.Worktree != r.Worktree {
		t.Fatalf("incorrect rebased paths: %+v", live.Run)
	}
	for _, name := range []string{"worktree", "runs.sqlite", "human-review", "outputs/review/result.json"} {
		if _, err := os.Stat(filepath.Join(inputs, name)); !os.IsNotExist(err) {
			t.Fatalf("live/current state copied: %s: %v", name, err)
		}
	}
	for _, name := range []string{"prompt.txt", "check-01.log", "outputs/producer/response.txt", "outputs/qa/result.json"} {
		if _, err := os.Stat(filepath.Join(inputs, name)); err != nil {
			t.Fatalf("required input not copied: %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(inputs, "prompt.txt"), []byte("copy only"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyIntegrity(t.Context(), r); err != nil {
		t.Fatalf("private copy modified original: %v", err)
	}
}
