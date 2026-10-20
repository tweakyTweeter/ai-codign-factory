package factory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// This fixture implements the installed bd gate contract in a real subprocess.
// It refuses all commands except reads, the run's existing claim/heartbeat,
// and native gate create. In particular close/unclaim/release are unsupported.
type reviewBDState struct {
	Gates      []nativeGate `json:"gates"`
	Mode       string       `json:"mode"`
	Creates    int          `json:"creates"`
	NoDep      bool         `json:"no_dep"`
	TaskStatus string       `json:"task_status"`
	Notes      string       `json:"notes"`
	Closes     int          `json:"closes"`
}

func TestReviewBeadsFixture(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(reviewBeadsFixture(os.Args[i+1:]))
		}
	}
}

func reviewBeadsFixture(args []string) int {
	if len(args) == 0 || args[len(args)-1] != "--sandbox" {
		return 90
	}
	args = args[:len(args)-1]
	var state reviewBDState
	data, err := os.ReadFile("review-bd.json")
	if err == nil && json.Unmarshal(data, &state) != nil {
		return 91
	}
	write := func() error {
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		return os.WriteFile("review-bd.json", data, 0600)
	}
	output := func(value any) int {
		if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
			return 92
		}
		return 0
	}
	if args[0] == "show" {
		for _, gate := range state.Gates {
			if args[1] == gate.ID {
				return output([]nativeGate{gate})
			}
		}
		owner, err := os.ReadFile("bd-owner")
		if err != nil {
			return fixture("bd-ready", args)
		}
		status := state.TaskStatus
		if status == "" {
			status = "in_progress"
		}
		deps := []map[string]string{}
		if !state.NoDep {
			for _, gate := range state.Gates {
				deps = append(deps, map[string]string{"id": gate.ID, "dependency_type": "blocks"})
			}
		}
		return output([]any{map[string]any{"id": "task-1", "status": status, "assignee": string(owner), "dependencies": deps, "notes": state.Notes}})
	}
	if args[0] == "update" && strings.Contains(strings.Join(args, " "), "--status closed") {
		get := func(key string) string {
			for i, arg := range args {
				if arg == key && i+1 < len(args) {
					return args[i+1]
				}
			}
			return ""
		}
		owner, err := os.ReadFile("bd-owner")
		if err != nil || get("--if-assignee") != string(owner) || get("--if-status") != "in_progress" ||
			get("--actor") != string(owner) || !strings.HasPrefix(get("--append-notes"), "factory-integrated:") ||
			state.TaskStatus == "closed" {
			return 13
		}
		if state.Mode == "close-failure" {
			return 1
		}
		state.TaskStatus, state.Notes = "closed", state.Notes+"\n"+get("--append-notes")
		state.Closes++
		if err := write(); err != nil {
			return 96
		}
		if state.Mode == "close-response-loss" {
			return 1
		}
		return output([]any{map[string]any{"id": "task-1", "status": "closed"}})
	}
	if args[0] != "gate" {
		if args[0] != "ready" && args[0] != "update" && args[0] != "heartbeat" {
			return 93
		}
		return fixture("bd-ready", args)
	}
	if len(args) < 2 {
		return 94
	}
	switch args[1] {
	case "list":
		if len(args) != 7 || args[2] != "task-1" || strings.Join(args[3:], " ") != "--all --limit 0 --json" {
			return 95
		}
		if state.Mode == "list-fails-once" {
			state.Mode = ""
			if err := write(); err != nil {
				return 96
			}
			return 1
		}
		return output(state.Gates) // nil deliberately serializes as null.
	case "create":
		get := func(key string) string {
			for i, arg := range args {
				if arg == key && i+1 < len(args) {
					return args[i+1]
				}
			}
			return ""
		}
		if get("--blocks") != "task-1" || get("--type") != "human" || get("--title") == "" {
			return 97
		}
		state.Creates++
		g := nativeGate{ID: "gate-1", Title: get("--title"),
			Description: "Ad-hoc gate blocking task-1\n\nReason: " + get("--reason"),
			Status:      "open", IssueType: "gate", AwaitType: "human"}
		if state.Mode != "no-create" {
			state.Gates = append(state.Gates, g)
		}
		if state.Mode == "duplicate" {
			g.ID = "gate-2"
			state.Gates = append(state.Gates, g)
		}
		lost := state.Mode == "response-loss" || state.Mode == "list-loss" || state.Mode == "no-create"
		if state.Mode == "list-loss" {
			state.Mode = "list-fails-once"
		}
		if err := write(); err != nil {
			return 98
		}
		if lost {
			return 1
		}
		return output(g)
	default:
		return 99
	}
}

func reviewState(t *testing.T, c Controller) reviewBDState {
	t.Helper()
	var state reviewBDState
	path := filepath.Join(c.Repo.Root, "review-bd.json")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state
	}
	if err != nil || json.Unmarshal(b, &state) != nil {
		t.Fatalf("read fixture state: %v", err)
	}
	return state
}

func setReviewState(t *testing.T, c Controller, s reviewBDState) {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Repo.Root, "review-bd.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func reviewRun(t *testing.T) (Controller, Run) {
	t.Helper()
	c, req := setupController(t), workflowRequest()
	req.TaskID = "task-1"
	req.Config.BeadsArgv = []string{os.Args[0], "-test.run=^TestReviewBeadsFixture$", "--"}
	r, err := c.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}

func publishedReview(t *testing.T) (Controller, Run) {
	t.Helper()
	c, r := reviewRun(t)
	r, err := c.ReviewPublish(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}

func closeReviewGate(t *testing.T, c Controller, reason string) {
	t.Helper()
	s := reviewState(t, c)
	s.Gates[0].Status, s.Gates[0].CloseReason = "closed", reason
	setReviewState(t, c, s)
}

func assertRetained(t *testing.T, c Controller, id string) {
	t.Helper()
	r := requireState(t, c, id, "awaiting_review")
	active, err := c.Store.Active(t.Context())
	if err != nil || active.ID != r.ID {
		t.Fatalf("review released active lock: %+v: %v", active, err)
	}
	if b, err := os.ReadFile(filepath.Join(c.Repo.Root, "bd-owner")); err != nil || string(b) != r.Actor {
		t.Fatalf("review changed original owner: %s: %v", b, err)
	}
	if head, err := gitOutput(t.Context(), r.Worktree, "rev-parse", "HEAD"); err != nil || head != r.Head {
		t.Fatalf("review changed HEAD: %s: %v", head, err)
	}
}

func TestReview_BundleIncludesTrackedStagedUntrackedIgnoredBinarySymlinkWithoutMutation(t *testing.T) {
	c := setupController(t)
	r, err := c.Run(t.Context(), request())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(r.Worktree, name), data, mode); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked.txt", []byte("staged\n"), 0600)
	if _, err := gitOutput(t.Context(), r.Worktree, "add", "--", "tracked.txt"); err != nil {
		t.Fatal(err)
	}
	write("tracked.txt", []byte("unstaged\n"), 0600)
	write("untracked space\nname", []byte("new content\n"), 0700)
	write("binary", []byte{0, 1, 2, 3}, 0600)
	write(".gitignore", []byte("ignored\n"), 0600)
	write("ignored", []byte("ignored content\n"), 0600)
	if err := os.Symlink("missing-target", filepath.Join(r.Worktree, "link")); err != nil {
		t.Fatal(err)
	}
	before, err := Fingerprint(t.Context(), r.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	// Git chooses a worktree administration basename; ask Git for it instead.
	indexPath, gitErr := gitOutput(t.Context(), r.Worktree, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if gitErr != nil {
		t.Fatal(gitErr)
	}
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err = c.Review(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(filepath.Join(r.HumanReview.Bundle, "candidate.diff"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"+unstaged", "+implemented", "+new content", "new file mode 100755", "GIT binary patch", "new file mode 120000", "+missing-target", "+ignored content"} {
		if !strings.Contains(string(patch), fragment) {
			t.Errorf("diff omitted %q:\n%s", fragment, patch)
		}
	}
	indexPatch, err := os.ReadFile(filepath.Join(r.HumanReview.Bundle, "index.diff"))
	if err != nil || !strings.Contains(string(indexPatch), "+staged") {
		t.Fatalf("staged evidence omitted: %s: %v", indexPatch, err)
	}
	after, err := Fingerprint(t.Context(), r.Worktree)
	if err != nil || after != before {
		t.Fatalf("review mutated candidate: %s -> %s: %v", before, after, err)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatalf("review mutated index: %v", err)
	}
	info, err := os.Stat(r.HumanReview.Bundle)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("bundle directory not private: %v", err)
	}
	for path := range r.HumanReview.Artifacts {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("artifact not private: %s: %v", path, err)
		}
	}
	if _, err := c.ReviewPublish(t.Context(), r.ID); err == nil {
		t.Fatal("generic worker run advertised as independently reviewed")
	}
}

func TestReviewPublish_NativeVisiblePayloadAndIdempotency(t *testing.T) {
	c, r := publishedReview(t)
	h := r.HumanReview
	state := reviewState(t, c)
	if state.Creates != 1 || len(state.Gates) != 1 || h.GateID != "gate-1" || h.Decision != "not-approved" {
		t.Fatalf("missing gate binding: %+v: %+v", h, state)
	}
	for _, text := range []string{r.ID, r.TaskID, r.Fingerprint, h.Bundle, h.ApproveReason, h.RejectReason,
		"```diff\n", "+implemented", "Recorded verdict: pass", "No commit"} {
		if !strings.Contains(state.Gates[0].Description, text) {
			t.Errorf("inbox payload omitted %q", text)
		}
	}
	for range 2 {
		if _, err := c.ReviewPublish(t.Context(), r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if state := reviewState(t, c); state.Creates != 1 {
		t.Fatalf("repeat publication created another gate: %+v", state)
	}
	assertRetained(t, c, r.ID)
}

func TestReviewPublish_UncertainCreationReconcilesWithoutDuplicates(t *testing.T) {
	for _, mode := range []string{"response-loss", "list-loss", "duplicate", "no-create"} {
		t.Run(mode, func(t *testing.T) {
			c, r := reviewRun(t)
			setReviewState(t, c, reviewBDState{Mode: mode})
			got, err := c.ReviewPublish(t.Context(), r.ID)
			if mode == "response-loss" {
				if err != nil || got.HumanReview.GateID == "" {
					t.Fatalf("lost create response not reconciled: %+v: %v", got, err)
				}
			} else if err == nil {
				t.Fatal("uncertain or ambiguous publication accepted")
			}
			got, err = c.ReviewPublish(t.Context(), r.ID)
			if mode == "response-loss" || mode == "list-loss" {
				if err != nil || got.HumanReview.GateID == "" {
					t.Fatalf("retry failed to discover exact known request: %+v: %v", got, err)
				}
			} else if err == nil {
				t.Fatal("ambiguous/missing gate silently retried")
			}
			if s := reviewState(t, c); s.Creates != 1 {
				t.Fatalf("duplicate-prone create retry: %+v", s)
			}
			assertRetained(t, c, r.ID)
		})
	}
}

func TestReviewSync_OnlyExplicitExactCandidateDecisionCounts(t *testing.T) {
	c, r := publishedReview(t)
	h := r.HumanReview
	for _, tc := range []struct {
		name, reason, want string
	}{
		{"pending", "", "not-approved"},
		{"unmarked close", "Approved", "not-approved"},
		{"dismissal", "Dismissed by human", "not-approved"},
		{"wrong run", "factory-approve:other:" + h.Fingerprint, "not-approved"},
		{"wrong fingerprint", "factory-approve:" + r.ID + ":" + strings.Repeat("0", 64), "not-approved"},
		{"embedded token", "approval " + h.ApproveReason, "not-approved"},
		{"reject", h.RejectReason, "rejected"},
		{"approve", h.ApproveReason, "approved"},
		{"native without note", "Approved by human via Scotty at 2026-10-20T08:00:00.000Z", "not-approved"},
		{"native exact note", "Approved by human via Scotty at 2026-10-20T08:00:00.000Z\n\nReviewed diff.\n" + h.ApproveReason, "approved"},
		{"native rejection", "Approved by human via Scotty at 2026-10-20T08:00:00.000Z\n\n" + h.RejectReason, "rejected"},
		{"conflicting", "Approved by human via Scotty at 2026-10-20T08:00:00.000Z\n\n" + h.ApproveReason + "\n" + h.RejectReason, "not-approved"},
		{"reopen", "", "not-approved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := reviewState(t, c)
			s.Gates[0].Status, s.Gates[0].CloseReason = "closed", tc.reason
			if tc.name == "pending" || tc.name == "reopen" {
				s.Gates[0].Status = "open"
			}
			setReviewState(t, c, s)
			got, err := c.ReviewSync(t.Context(), r.ID)
			if err != nil || got.HumanReview.Decision != tc.want {
				t.Fatalf("decision %q, want %q: %v", got.HumanReview.Decision, tc.want, err)
			}
			assertRetained(t, c, r.ID)
		})
	}
}

func TestReviewSync_UnsafeChangesRevokePreviouslyStoredApproval(t *testing.T) {
	for _, change := range []string{"patch", "index", "head", "owner", "status", "cancel", "gate description", "gate title", "gate identity", "gate type", "dependency", "duplicate", "source", "artifact", "run identity", "read error", "released"} {
		t.Run(change, func(t *testing.T) {
			c, r := publishedReview(t)
			closeReviewGate(t, c, r.HumanReview.ApproveReason)
			r, err := c.ReviewSync(t.Context(), r.ID)
			if err != nil || r.HumanReview.Decision != "approved" {
				t.Fatalf("setup approval failed: %v", err)
			}
			s := reviewState(t, c)
			switch change {
			case "patch":
				err = os.WriteFile(filepath.Join(r.Worktree, "worker.txt"), []byte("changed"), 0600)
			case "index":
				_, err = gitOutput(t.Context(), r.Worktree, "add", "--", "worker.txt")
			case "head":
				_, err = gitOutput(t.Context(), r.Worktree, "update-ref", "HEAD", r.Head+"^{tree}")
				// update-ref refuses non-commit HEAD; use an explicit failing
				// candidate instead of assuming the fixture has another commit.
				if err != nil {
					err = os.WriteFile(filepath.Join(r.Worktree, ".git"), []byte("invalid gitdir"), 0600)
				}
			case "owner":
				err = os.WriteFile(filepath.Join(c.Repo.Root, "bd-owner"), []byte("other-owner"), 0600)
			case "status":
				s.TaskStatus = "closed"
			case "cancel":
				err = c.Store.RequestCancel(t.Context(), r.ID)
			case "gate description":
				s.Gates[0].Description += "\nedited"
			case "gate title":
				s.Gates[0].Title += " edited"
			case "gate identity":
				s.Gates[0].ID = "different-gate"
			case "gate type":
				s.Gates[0].AwaitType = "timer"
			case "dependency":
				s.NoDep = true
			case "duplicate":
				g := s.Gates[0]
				g.ID = "gate-2"
				s.Gates = append(s.Gates, g)
			case "source":
				err = os.WriteFile(r.Evidence[3].ResultFile, []byte(`{}`), 0600)
			case "artifact":
				err = os.WriteFile(filepath.Join(r.HumanReview.Bundle, "candidate.diff"), []byte("changed"), 0600)
			case "run identity":
				r.TaskID = "different-task"
				err = c.Store.SaveHumanReview(t.Context(), &r, "fixture identity change")
			case "read error":
				s.Mode = "list-fails-once"
			case "released":
				_, err = c.Release(t.Context(), r.ID, "operator acknowledgment, not integration", false)
			}
			if err != nil {
				t.Fatal(err)
			}
			setReviewState(t, c, s)
			got, err := c.ReviewSync(t.Context(), r.ID)
			if err == nil || got.HumanReview.Decision != "not-approved" {
				t.Fatalf("unsafe %s left approval: %+v: %v", change, got.HumanReview, err)
			}
			persisted, getErr := c.Store.Get(t.Context(), r.ID)
			if getErr != nil || persisted.HumanReview.Decision != "not-approved" {
				t.Fatalf("revocation not persisted: %+v: %v", persisted.HumanReview, getErr)
			}
		})
	}
}

func TestReview_RespectsControllerLockAndOutputBounds(t *testing.T) {
	c, r := reviewRun(t)
	lock, err := c.Repo.Lock()
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() (Run, error){
		func() (Run, error) { return c.Review(t.Context(), r.ID) },
		func() (Run, error) { return c.ReviewPublish(t.Context(), r.ID) },
		func() (Run, error) { return c.ReviewSync(t.Context(), r.ID) },
	} {
		if _, err := call(); err == nil {
			t.Fatal("bridge bypassed controller lock")
		}
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	// Direct diff bound exercises the output path independently of supervised
	// fingerprint mismatch admission.
	if err := os.WriteFile(filepath.Join(r.Worktree, "huge"), []byte(strings.Repeat("line\n", outputLimit/2)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := candidateDiff(t.Context(), r.Worktree); err == nil {
		t.Fatal("oversized diff silently truncated")
	}
}

func TestReviewPublish_RequiresIntactSupervisedSourcesBeforeCreatingGate(t *testing.T) {
	for _, change := range []string{"missing QA", "failed check", "truncated", "missing verdict", "stale fingerprint"} {
		t.Run(change, func(t *testing.T) {
			c, r := reviewRun(t)
			switch change {
			case "missing QA":
				r.Evidence = append(r.Evidence[:3], r.Evidence[4:]...)
			case "failed check":
				r.Evidence[2].ExitCode = 1
			case "truncated":
				r.Evidence[3].Truncated = true
			case "missing verdict":
				err := os.Remove(r.Evidence[3].ResultFile)
				if err != nil {
					t.Fatal(err)
				}
			case "stale fingerprint":
				r.QAFingerprint = "stale"
			}
			if err := c.Store.SaveHumanReview(t.Context(), &r, "fixture source change"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.ReviewPublish(t.Context(), r.ID); err == nil {
				t.Fatal("invalid evidence published")
			}
			if s := reviewState(t, c); s.Creates != 0 {
				t.Fatalf("gate created without accepted evidence: %+v", s)
			}
		})
	}
}

func TestReview_ManifestEvidenceAndRequestAreExact(t *testing.T) {
	c, r := publishedReview(t)
	b, err := os.ReadFile(filepath.Join(r.HumanReview.Bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m reviewManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Evidence, r.Evidence) || m.Fingerprint != r.Fingerprint || m.TaskID != r.TaskID {
		t.Fatalf("manifest not candidate-bound: %+v", m)
	}
	events, err := c.Store.Events(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent, published := -1, -1
	for i, e := range events {
		if strings.HasPrefix(e.Detail, "intent: publish") {
			intent = i
		}
		if strings.HasPrefix(e.Detail, "native gate published") {
			published = i
		}
	}
	if intent < 0 || published <= intent {
		t.Fatalf("publication intent not journaled before binding: %v", fmt.Sprint(events))
	}
}
