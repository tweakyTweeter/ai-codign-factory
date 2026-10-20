package factory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationFixture(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "--" || i+1 >= len(os.Args) {
			continue
		}
		mode := os.Args[i+1]
		if mode == "dev" {
			if code := fixture("phase", nil); code != 0 {
				os.Exit(code)
			}
			if err := os.WriteFile("tracked.txt", []byte("approved modification\n"), 0600); err != nil {
				os.Exit(2)
			}
			if err := os.WriteFile("binary", []byte{0, 1, 2}, 0700); err != nil {
				os.Exit(3)
			}
			if err := os.Symlink("tracked.txt", "link"); err != nil {
				os.Exit(4)
			}
			if err := os.Remove("delete.txt"); err != nil {
				os.Exit(5)
			}
			fp, err := Fingerprint(context.Background(), os.Getenv("FACTORY_WORKTREE"))
			if err != nil {
				os.Exit(6)
			}
			v := Verdict{Version: 1, Verdict: "pass", Summary: "mixed file changes", Fingerprint: fp, Findings: []Finding{}}
			b, err := json.Marshal(v)
			if err != nil || os.WriteFile(os.Getenv("FACTORY_RESULT_FILE"), b, 0600) != nil {
				os.Exit(7)
			}
			os.Exit(0)
		}
		if os.Getenv("FACTORY_WORKTREE") != os.Getenv("FACTORY_REPO") {
			os.Exit(fixture("check", nil))
		}
		switch mode {
		case "fail":
			os.Exit(8)
		case "mutate":
			if err := os.WriteFile("unrelated.txt", []byte("changed by check"), 0600); err != nil {
				os.Exit(9)
			}
		case "evidence":
			path := filepath.Join(filepath.Dir(os.Getenv("FACTORY_CONTEXT_FILE")), "outputs", "producer", "response.txt")
			if err := os.WriteFile(path, []byte("changed by integration check"), 0600); err != nil {
				os.Exit(17)
			}
		case "owner-loss":
			if err := os.WriteFile("bd-owner", []byte("other"), 0600); err != nil {
				os.Exit(10)
			}
		case "reopen":
			b, err := os.ReadFile("review-bd.json")
			var state reviewBDState
			if err != nil || json.Unmarshal(b, &state) != nil {
				os.Exit(11)
			}
			state.Gates[0].Status = "open"
			b, err = json.Marshal(state)
			if err != nil || os.WriteFile("review-bd.json", b, 0600) != nil {
				os.Exit(12)
			}
		case "flood":
			if _, err := io.CopyN(os.Stdout, endless{}, outputLimit*2); err != nil {
				os.Exit(13)
			}
		case "cancel":
			repo, err := Discover(context.Background(), os.Getenv("FACTORY_REPO"))
			if err != nil {
				os.Exit(14)
			}
			store, err := Open(context.Background(), repo)
			if err != nil {
				os.Exit(15)
			}
			if err := errors.Join(store.RequestCancel(context.Background(), os.Getenv("FACTORY_RUN_ID")), store.Close()); err != nil {
				os.Exit(16)
			}
		}
		os.Exit(fixture("check", nil))
	}
}

func integrationFixtureCommand(mode string) Command {
	return Command{Argv: []string{os.Args[0], "-test.run=^TestIntegrationFixture$", "--", mode}, Timeout: "5s"}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	argv := append([]string{"git", "-c", "core.hooksPath=/dev/null"}, args...)
	_, err := capture(t.Context(), root, argv, []string{
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func integrationRun(t *testing.T, check string, mixed bool) (Controller, Run) {
	t.Helper()
	c, req := setupController(t), workflowRequest()
	// Beads fixture state is runtime data, like the ignored real Dolt database.
	if err := os.WriteFile(filepath.Join(c.Repo.Common, "info", "exclude"), []byte("review-bd.json\nbd-owner\n"), 0600); err != nil {
		t.Fatal(err)
	}
	req.TaskID = "task-1"
	req.Config.BeadsArgv = []string{os.Args[0], "-test.run=^TestReviewBeadsFixture$", "--"}
	req.Config.Checks = []Command{integrationFixtureCommand(check)}
	if mixed {
		if err := os.WriteFile(filepath.Join(c.Repo.Root, "delete.txt"), []byte("delete me\n"), 0600); err != nil {
			t.Fatal(err)
		}
		runGit(t, c.Repo.Root, "add", "delete.txt")
		runGit(t, c.Repo.Root, "commit", "-qm", "fixture baseline")
		req.Config.Workflow.Phases[1].Command = integrationFixtureCommand("dev")
	}
	r, err := c.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	r, err = c.ReviewPublish(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	closeReviewGate(t, c, r.HumanReview.ApproveReason)
	return c, r
}

func TestIntegrate_AppliesExactChangesPreservesDirtySourcesAndReleasesWithoutCommit(t *testing.T) {
	c, r := integrationRun(t, "pass", true)
	originalIndex, err := gitOutput(t.Context(), c.Repo.Root, "ls-files", "--stage", "-z")
	if err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(c.Repo.Root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("unrelated.txt", "preserve me")
	write("staged.txt", "staged but unrelated")
	runGit(t, c.Repo.Root, "add", "staged.txt")
	index, err := gitOutput(t.Context(), c.Repo.Root, "ls-files", "--stage", "-z")
	if err != nil || index == originalIndex {
		t.Fatal("staged fixture missing", err)
	}
	got, err := c.Integrate(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("integration failed: %+v: %v", got.Integration, err)
	}
	if got.State != "released" || got.Integration.State != "complete" || len(got.Integration.Checks) != 1 {
		t.Fatalf("completion not recorded: %+v", got)
	}
	for _, name := range got.Integration.Paths {
		want, err := identifyFile(r.Worktree, name)
		if err != nil {
			t.Fatal(err)
		}
		have, err := identifyFile(c.Repo.Root, name)
		if err != nil || have != want {
			t.Fatalf("%s does not match approved content/mode: %v != %v: %v", name, have, want, err)
		}
	}
	if text, err := os.ReadFile(filepath.Join(c.Repo.Root, "unrelated.txt")); err != nil || string(text) != "preserve me" {
		t.Fatal("unrelated change lost", err)
	}
	if after, err := gitOutput(t.Context(), c.Repo.Root, "ls-files", "--stage", "-z"); err != nil || after != index {
		t.Fatal("index changed", err)
	}
	if head, err := gitOutput(t.Context(), c.Repo.Root, "rev-parse", "HEAD"); err != nil || head != r.Head {
		t.Fatal("unexpected commit", err)
	}
	if s := reviewState(t, c); s.TaskStatus != "closed" || s.Closes != 1 || !standaloneLine(s.Notes, integrationMarker(r)) {
		t.Fatalf("missing exact task completion: %+v", s)
	}
	if _, err := c.Store.Active(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("admission still held", err)
	}
	if _, err := c.Integrate(t.Context(), r.ID); err != nil || reviewState(t, c).Closes != 1 {
		t.Fatal("completed integration was not idempotent", err)
	}
}

func TestIntegrate_PreconditionsNeverWriteTargetOrCloseTask(t *testing.T) {
	for _, mode := range []string{"open", "unmarked", "reject", "changed-candidate", "dirty-addition", "staged", "owner", "cancel", "head", "candidate-target"} {
		t.Run(mode, func(t *testing.T) {
			c, r := integrationRun(t, "pass", false)
			switch mode {
			case "open":
				s := reviewState(t, c)
				s.Gates[0].Status = "open"
				setReviewState(t, c, s)
			case "unmarked":
				closeReviewGate(t, c, "Approved")
			case "reject":
				closeReviewGate(t, c, r.HumanReview.RejectReason)
			case "changed-candidate":
				if err := os.WriteFile(filepath.Join(r.Worktree, "worker.txt"), []byte("edited"), 0600); err != nil {
					t.Fatal(err)
				}
			case "dirty-addition", "staged":
				if err := os.WriteFile(filepath.Join(c.Repo.Root, "worker.txt"), []byte("do not overwrite"), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "staged" {
					runGit(t, c.Repo.Root, "add", "worker.txt")
				}
			case "owner":
				if err := os.WriteFile(filepath.Join(c.Repo.Root, "bd-owner"), []byte("other"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if err := c.Store.RequestCancel(t.Context(), r.ID); err != nil {
					t.Fatal(err)
				}
			case "head":
				runGit(t, c.Repo.Root, "commit", "--allow-empty", "-qm", "advanced")
			case "candidate-target":
				c.Repo.Root = r.Worktree
			}
			before, index, err := sourceSnapshot(t.Context(), c.Repo.Root, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Integrate(t.Context(), r.ID); err == nil {
				t.Fatal("unsafe integration accepted")
			}
			after, newIndex, err := sourceSnapshot(t.Context(), c.Repo.Root, nil)
			a, _ := json.Marshal(before)
			b, _ := json.Marshal(after)
			if err != nil || !bytes.Equal(a, b) || index != newIndex {
				t.Fatal("failed precondition mutated target", err)
			}
			if s := reviewState(t, c); s.Closes != 0 {
				t.Fatal("failed integration closed task")
			}
			requireState(t, c, r.ID, "awaiting_review")
		})
	}
}

func TestIntegrate_FailedOrMutatingChecksRetainPatchAndAdmission(t *testing.T) {
	for _, mode := range []string{"fail", "mutate", "evidence", "owner-loss", "reopen", "flood", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			c, r := integrationRun(t, mode, false)
			got, err := c.Integrate(t.Context(), r.ID)
			if err == nil || got.State == "released" || got.Integration == nil {
				t.Fatalf("unsafe check accepted: %+v: %v", got, err)
			}
			if mode == "evidence" && !strings.Contains(err.Error(), "evidence integrity changed") {
				t.Fatalf("missing integration evidence diagnostic: %v", err)
			}
			if b, err := os.ReadFile(filepath.Join(c.Repo.Root, "worker.txt")); err != nil || string(b) != "implemented" {
				t.Fatal("failed integration removed evidence", err)
			}
			if s := reviewState(t, c); s.Closes != 0 {
				t.Fatal("failed check closed task")
			}
			if _, err := c.Store.Active(t.Context()); err != nil {
				t.Fatal("failed integration released admission", err)
			}
			if _, err := c.Integrate(t.Context(), r.ID); err == nil {
				t.Fatal("failed/uncertain checks were retried automatically")
			}
			if _, err := c.Release(t.Context(), r.ID, "unsafe bypass", false); err == nil {
				t.Fatal("incomplete integration hold bypassed")
			}
		})
	}
}

func TestIntegrate_LostConditionalCloseResponseIsReadBack(t *testing.T) {
	c, r := integrationRun(t, "pass", false)
	state := reviewState(t, c)
	state.Mode = "close-response-loss"
	setReviewState(t, c, state)
	got, err := c.Integrate(t.Context(), r.ID)
	if err != nil || got.State != "released" || reviewState(t, c).Closes != 1 {
		t.Fatalf("lost response not reconciled: %+v: %v", got.Integration, err)
	}
}

func TestIntegrate_RestartAfterClosureResumesWithoutReapplyingOrRerunningChecks(t *testing.T) {
	for _, stage := range []string{"close-intent", "task-closed"} {
		t.Run(stage, func(t *testing.T) {
			c, r := integrationRun(t, "pass", false)
			completed, err := c.Integrate(t.Context(), r.ID)
			if err != nil {
				t.Fatal(err)
			}
			completed.Integration.State = stage
			if err := c.save(&completed, "awaiting_review", "fixture crash before release", false); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(t.Context(), c.Repo)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			controller := Controller{Repo: c.Repo, Store: reopened}
			got, err := controller.Integrate(t.Context(), r.ID)
			if err != nil || got.State != "released" || len(got.Integration.Checks) != 1 || reviewState(t, c).Closes != 1 {
				t.Fatalf("restart did not reconcile: %+v: %v", got.Integration, err)
			}
		})
	}
}

func TestIntegrate_CommonControllerLockRejectsConcurrentIntegration(t *testing.T) {
	c, r := integrationRun(t, "pass", false)
	lock, err := c.Repo.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := c.Integrate(t.Context(), r.ID); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatal("concurrent integration not rejected", err)
	}
	if _, err := os.Lstat(filepath.Join(c.Repo.Root, "worker.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("concurrent attempt wrote destination", err)
	}
}

func TestIntegrate_UncertainIntentDoesNotReapplyOrRetryClose(t *testing.T) {
	c, r := integrationRun(t, "pass", false)
	s := reviewState(t, c)
	s.Mode = "close-failure"
	setReviewState(t, c, s)
	got, err := c.Integrate(t.Context(), r.ID)
	if err == nil || got.Integration.State != "close-intent" {
		t.Fatalf("missing close intent: %+v: %v", got.Integration, err)
	}
	s = reviewState(t, c)
	s.Mode = ""
	setReviewState(t, c, s)
	if _, err := c.Integrate(t.Context(), r.ID); err == nil || reviewState(t, c).Closes != 0 {
		t.Fatal("uncertain close blindly retried")
	}
	got.Integration.State = "apply-intent"
	if err := c.save(&got, got.State, "fixture interrupted apply", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Integrate(t.Context(), r.ID); err == nil || !strings.Contains(err.Error(), "manual inspection") {
		t.Fatal("uncertain apply did not fail closed", err)
	}
}

func TestIntegrationPaths_RejectSymlinkAncestorsAndDirectoryTransitions(t *testing.T) {
	for _, path := range []string{"..", "../escape", ".git/config", "nested/.GIT/config", "/absolute"} {
		if err := safeIntegrationPath(path); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "parent")); err != nil {
		t.Fatal(err)
	}
	if _, err := identifyFile(root, "parent/file"); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
}

func TestCleanIntegrationPaths_RejectsDirtyTrackedFilesWithoutChangingIndex(t *testing.T) {
	c := setupController(t)
	if err := os.WriteFile(filepath.Join(c.Repo.Root, "tracked.txt"), []byte("user edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, staged := range []bool{false, true} {
		if staged {
			runGit(t, c.Repo.Root, "add", "tracked.txt")
		}
		before, err := gitOutput(t.Context(), c.Repo.Root, "ls-files", "--stage", "-z")
		if err != nil {
			t.Fatal(err)
		}
		if err := cleanIntegrationPaths(t.Context(), c.Repo.Root, []string{"tracked.txt"}); err == nil {
			t.Fatal("dirty tracked destination accepted")
		}
		after, err := gitOutput(t.Context(), c.Repo.Root, "ls-files", "--stage", "-z")
		if err != nil || after != before {
			t.Fatal("preflight changed index", err)
		}
	}
}

func TestStore_IntegrationReleaseCannotOvertakeCancellation(t *testing.T) {
	c := setupController(t)
	r := Run{ID: "integration-cancellation", Integration: &Integration{State: "complete"}}
	if err := c.Store.Save(t.Context(), &r, "checks_passed", "fixture", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.RequestCancel(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.Save(t.Context(), &r, "released", "unsafe release", false); !errors.Is(err, context.Canceled) {
		t.Fatal(fmt.Errorf("release overtook cancellation: %w", err))
	}
	requireState(t, c, r.ID, "checks_passed")
}
