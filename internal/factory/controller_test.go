package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_COPILOT_FIXTURE") == "1" && len(os.Args) > 1 && os.Args[1] == "--agent" {
		os.Exit(copilotFixture())
	}
	if isClaudeFixtureInvocation(os.Args) {
		os.Exit(claudeFixture())
	}
	goleak.VerifyTestMain(m)
}

// TestFixture is a real subprocess worker/Beads fixture, not a shell script.
func TestFixture(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && len(os.Args) > i+1 {
			code := fixture(os.Args[i+1], os.Args[i+2:])
			os.Exit(code)
		}
	}
}

func fixture(mode string, args []string) int {
	switch mode {
	case "adapter-tree":
		cmd := fixtureCommand("tree")
		_, err := executeGroup(context.Background(), os.Getenv("FACTORY_WORKTREE"), cmd.Argv, os.Environ(), os.Stdout, nil, false)
		if err != nil {
			return 62
		}
		return 0
	case "agent":
		if len(args) != 1 {
			return 60
		}
		if err := RunPhase(context.Background(), args[0], ""); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 61
		}
		return 0
	case "adapter":
		if len(args) != 1 {
			return 60
		}
		if err := Copilot(context.Background(), args[0]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 61
		}
		return 0
	case "worker", "worker-owner-loss":
		if os.Getenv("FACTORY_RUN_ID") == "" || os.Getenv("FACTORY_WORKTREE") == os.Getenv("FACTORY_REPO") {
			return 20
		}
		prompt, err := os.ReadFile(os.Getenv("FACTORY_PROMPT_FILE"))
		if err != nil || string(prompt) != "implement fixture" {
			return 21
		}
		var contextData map[string]any
		b, err := os.ReadFile(os.Getenv("FACTORY_CONTEXT_FILE"))
		if err != nil || json.Unmarshal(b, &contextData) != nil {
			return 22
		}
		if err := os.WriteFile("worker.txt", []byte("implemented"), 0600); err != nil {
			return 23
		}
		fmt.Print("worker completed\n")
		if mode == "worker-owner-loss" {
			if err := os.WriteFile(filepath.Join(os.Getenv("FACTORY_REPO"), "bd-owner"), []byte("new-owner"), 0600); err != nil {
				return 63
			}
		}
		return 0
	case "check":
		b, err := os.ReadFile("worker.txt")
		if err != nil || string(b) != "implemented" {
			return 24
		}
		fmt.Print("check passed\n")
		return 0
	case "fail":
		fmt.Fprintln(os.Stderr, "intentional fixture failure")
		return 7
	case "flood":
		_, err := io.CopyN(os.Stdout, endless{}, outputLimit*2)
		if err != nil {
			return 25
		}
		return 0
	case "tree", "exit-with-child":
		child := exec.Command(os.Args[0], "-test.run=^TestFixture$", "--", "child")
		child.Env = os.Environ()
		out, err := child.StdoutPipe()
		if err != nil || child.Start() != nil {
			return 26
		}
		ready := make([]byte, 1)
		if _, err := io.ReadFull(out, ready); err != nil {
			return 27
		}
		if err := os.WriteFile("ready", []byte("ready"), 0600); err != nil {
			return 28
		}
		if mode == "exit-with-child" {
			return 0
		}
		<-time.After(time.Hour)
		return 0
	case "child":
		if err := os.WriteFile("descendant.pid", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			return 29
		}
		fmt.Print("R")
		<-time.After(time.Hour)
		return 0
	case "bd-ready", "bd-blocked", "bd-owned", "bd-claim-fail", "bd-bad-json":
		if len(args) == 0 {
			return 30
		}
		switch args[0] {
		case "show":
			if mode == "bd-bad-json" {
				fmt.Print("not JSON")
			} else if mode == "bd-owned" {
				fmt.Print(`[{"id":"task-1","status":"in_progress","assignee":"someone"}]`)
			} else if owner, err := os.ReadFile("bd-owner"); err == nil {
				b, _ := json.Marshal([]bead{{ID: "task-1", Status: "in_progress", Assignee: string(owner)}})
				fmt.Print(string(b))
			} else {
				fmt.Print(`[{"id":"task-1","status":"open","assignee":""}]`)
			}
		case "ready":
			if mode == "bd-blocked" {
				fmt.Print(`[]`)
			} else {
				fmt.Print(`[{"id":"task-1","status":"open"}]`)
			}
		case "update":
			if mode == "bd-claim-fail" {
				fmt.Fprint(os.Stderr, "claim conflict")
				return 1
			}
			actor := ""
			for i, arg := range args {
				if arg == "--actor" && i+1 < len(args) {
					actor = args[i+1]
				}
			}
			if actor == "" {
				return 33
			}
			if err := os.WriteFile("bd-owner", []byte(actor), 0600); err != nil {
				return 34
			}
			fmt.Print(`[{"id":"task-1","status":"in_progress"}]`)
		case "heartbeat":
			actor := ""
			for i, arg := range args {
				if arg == "--actor" && i+1 < len(args) {
					actor = args[i+1]
				}
			}
			owner, err := os.ReadFile("bd-owner")
			if err != nil || string(owner) != actor {
				return 35
			}
			if failure, err := os.ReadFile("bd-heartbeat-fail"); err == nil {
				switch string(failure) {
				case "bad-json":
					fmt.Print(`{"id":`)
					return 0
				case "wrong-owner":
					actor = "different"
				case "wrong-version":
					fmt.Printf(`{"id":"task-1","owner":%q,"schema_version":2,"status":"heartbeat"}`, actor)
					return 0
				default:
					return 36
				}
			}
			b, _ := json.Marshal(map[string]any{"id": "task-1", "owner": actor, "schema_version": 1, "status": "heartbeat"})
			fmt.Print(string(b))
		default:
			return 31
		}
		return 0
	case "phase", "phase-mutate", "phase-missing", "phase-bad-json", "phase-blocking", "phase-stale", "phase-inconclusive", "phase-flood", "phase-check", "phase-owner-loss", "phase-tamper":
		role, output := os.Getenv("FACTORY_ROLE"), os.Getenv("FACTORY_OUTPUT_DIR")
		if mode == "phase-check" {
			return fixture("check", nil)
		}
		var live struct {
			Run   Run `json:"run"`
			Prior []struct {
				Name   string `json:"name"`
				Role   string `json:"role"`
				Output string `json:"output_dir"`
			} `json:"prior_phases"`
		}
		b, err := os.ReadFile(os.Getenv("FACTORY_PHASE_CONTEXT_FILE"))
		if err != nil || json.Unmarshal(b, &live) != nil {
			return 40
		}
		if role == "producer" || role == "specialist" {
			if err := os.WriteFile(filepath.Join(output, "response.txt"), []byte("plan: implement worker.txt"), 0600); err != nil {
				return 41
			}
		}
		if role == "dev" {
			if len(live.Prior) == 0 {
				return 42
			}
			if b, err := os.ReadFile(filepath.Join(live.Prior[0].Output, "response.txt")); err != nil || !strings.Contains(string(b), "plan:") {
				return 43
			}
			if err := os.WriteFile("worker.txt", []byte("implemented"), 0600); err != nil {
				return 44
			}
			wd, err := os.Getwd()
			if err != nil {
				return 49
			}
			fp, err := Fingerprint(context.Background(), wd)
			if err != nil {
				return 50
			}
			v := Verdict{Version: 1, Verdict: "pass", Summary: "implementation complete", Fingerprint: fp, Findings: []Finding{}}
			b, _ := json.Marshal(v)
			if err := os.WriteFile(os.Getenv("FACTORY_RESULT_FILE"), b, 0600); err != nil {
				return 51
			}
		}
		if role == "qa" || role == "review" {
			if len(live.Prior) < 2 || len(live.Run.Evidence) < 3 {
				return 45
			}
			foundCheck := false
			for _, e := range live.Run.Evidence {
				if e.Stage == "check-01" && e.ExitCode == 0 {
					foundCheck = true
				}
			}
			if !foundCheck {
				return 46
			}
			if mode != "phase-missing" {
				v := Verdict{Version: 1, Verdict: "pass", Summary: "independent assessment", Fingerprint: os.Getenv("FACTORY_PATCH_FINGERPRINT"), Findings: []Finding{}}
				if mode == "phase-blocking" {
					v.Findings = []Finding{{Severity: "blocking", Summary: "broken acceptance"}}
				}
				if mode == "phase-stale" {
					v.Fingerprint = strings.Repeat("0", 64)
				}
				if mode == "phase-inconclusive" {
					v.Verdict = "inconclusive"
				}
				b, _ := json.Marshal(v)
				if mode == "phase-bad-json" {
					b = []byte(`{"version":`)
				}
				if err := os.WriteFile(os.Getenv("FACTORY_RESULT_FILE"), b, 0600); err != nil {
					return 47
				}
			}
		}
		if mode == "phase-mutate" {
			if err := os.WriteFile("nonwriter.txt", []byte("forbidden"), 0600); err != nil {
				return 48
			}
		}
		if mode == "phase-owner-loss" {
			if err := os.WriteFile(filepath.Join(os.Getenv("FACTORY_REPO"), "bd-owner"), []byte("new-owner"), 0600); err != nil {
				return 64
			}
		}
		if mode == "phase-tamper" {
			path := os.Getenv("GO_TAMPER_PATH")
			if !filepath.IsAbs(path) {
				path = filepath.Join(live.Run.Dir, path)
			}
			if err := os.WriteFile(path, []byte("changed evidence"), 0600); err != nil {
				return 65
			}
		}
		if mode == "phase-flood" {
			return fixture("flood", nil)
		}
		return 0
	default:
		return 32
	}
}

type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func fixtureCommand(mode string) Command {
	return Command{Argv: []string{os.Args[0], "-test.run=^TestFixture$", "--", mode}, Timeout: "5s"}
}

func fixtureConfig() Config {
	return Config{Version: 1, Worker: fixtureCommand("worker"), Checks: []Command{fixtureCommand("check")}, BeadsArgv: fixtureCommand("bd-ready").Argv}
}

func setupController(t *testing.T) Controller {
	t.Helper()
	root := t.TempDir()
	gitFixture := func(input string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.Stdin = strings.NewReader(input)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %s: %v", args, b, err)
		}
		return strings.TrimSpace(string(b))
	}
	gitFixture("", "init", "-q", "--initial-branch=main")
	// Test-only Git plumbing creates an immutable baseline without commit hooks,
	// touching the implementation repository, or making a worktree commit.
	blob := gitFixture("original\n", "hash-object", "-w", "--stdin")
	tree := gitFixture("100644 blob "+blob+"\ttracked.txt\n", "mktree")
	commit := gitFixture("", "commit-tree", tree, "-m", "fixture baseline")
	gitFixture("", "update-ref", "refs/heads/main", commit)
	gitFixture("", "read-tree", "-m", "-u", "HEAD")
	repo, err := Discover(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return Controller{Repo: repo, Store: store}
}

func request() Request {
	return Request{Config: fixtureConfig(), Prompt: []byte("implement fixture")}
}

func requireState(t *testing.T, c Controller, id, expected string) Run {
	t.Helper()
	r, err := c.Store.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != expected {
		t.Fatalf("state = %q, want %q; detail: %s", r.State, expected, r.Detail)
	}
	return r
}

func TestRun_HappyPathRetainsSeparateWorktreeAndDirtyMain(t *testing.T) {
	c := setupController(t)
	mainFile := filepath.Join(c.Repo.Root, "tracked.txt")
	if err := os.WriteFile(mainFile, []byte("dirty main"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Repo.Root, "untracked.txt"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := gitOutput(t.Context(), c.Repo.Root, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Run(t.Context(), request())
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "awaiting_review" || r.Worktree == c.Repo.Root || r.ID == "" {
		t.Fatalf("unexpected result: %+v", r)
	}
	requireState(t, c, r.ID, "awaiting_review")
	b, err := os.ReadFile(filepath.Join(r.Worktree, "tracked.txt"))
	if err != nil || string(b) != "original\n" {
		t.Fatalf("worktree baseline = %q: %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(r.Worktree, "untracked.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("main untracked file was copied: %v", err)
	}
	after, err := gitOutput(t.Context(), c.Repo.Root, "status", "--porcelain")
	if err != nil || before != after {
		t.Fatalf("main dirty status changed: before %q after %q: %v", before, after, err)
	}
	if b, _ := os.ReadFile(mainFile); string(b) != "dirty main" {
		t.Fatalf("main modified: %q", b)
	}
	head, err := gitOutput(t.Context(), r.Worktree, "rev-parse", "HEAD")
	if err != nil || head != r.Head {
		t.Fatalf("worker committed unexpectedly: %s != %s (%v)", head, r.Head, err)
	}
	if len(r.Evidence) != 2 || r.Evidence[0].ExitCode != 0 || r.Evidence[1].ExitCode != 0 {
		t.Fatalf("missing quality evidence: %+v", r.Evidence)
	}
	for _, e := range r.Evidence {
		info, err := os.Stat(e.Log)
		if err != nil || info.Mode().Perm() != 0600 || info.Size() == 0 {
			t.Fatalf("log missing/not private: %v %v", info, err)
		}
	}
	events, err := c.Store.Events(t.Context(), r.ID)
	if err != nil || len(events) < 8 || events[0].State != "admitted" {
		t.Fatalf("journal incomplete: %+v: %v", events, err)
	}
	reopened, err := Open(t.Context(), c.Repo)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Get(t.Context(), r.ID)
	if err != nil || persisted.State != "awaiting_review" {
		t.Fatalf("reopen lost state: %+v: %v", persisted, err)
	}
	if _, err := c.Run(t.Context(), request()); err == nil {
		t.Fatal("awaiting_review did not block admission")
	}
	linked, err := Discover(t.Context(), r.Worktree)
	if err != nil || linked.Common != c.Repo.Common {
		t.Fatalf("linked worktree identity mismatch: %+v: %v", linked, err)
	}
	if _, err := (Controller{Repo: linked, Store: reopened}).Run(t.Context(), request()); err == nil {
		t.Fatal("linked checkout bypassed repository-global admission")
	}
	released, err := c.Release(t.Context(), r.ID, "human review pending elsewhere", false)
	if err != nil || released.State != "released" {
		t.Fatalf("release: %+v: %v", released, err)
	}
	if _, err := os.Stat(r.Worktree); err != nil {
		t.Fatalf("release deleted worktree: %v", err)
	}
	if _, err := c.Run(t.Context(), request()); err != nil {
		t.Fatalf("explicit release did not permit fresh run: %v", err)
	}
}

func TestRun_WorkerAndCheckFailuresPersistEvidence(t *testing.T) {
	for _, stage := range []string{"worker", "check"} {
		t.Run(stage, func(t *testing.T) {
			c := setupController(t)
			req := request()
			if stage == "worker" {
				req.Config.Worker = fixtureCommand("fail")
			} else {
				req.Config.Checks[0] = fixtureCommand("fail")
			}
			r, err := c.Run(t.Context(), req)
			if err == nil {
				t.Fatal("nonzero command succeeded")
			}
			persisted := requireState(t, c, r.ID, "failed")
			last := persisted.Evidence[len(persisted.Evidence)-1]
			if last.ExitCode != 7 || last.Error == "" {
				t.Fatalf("exit evidence missing: %+v", last)
			}
			b, err := os.ReadFile(last.Log)
			if err != nil || !strings.Contains(string(b), "intentional fixture failure") {
				t.Fatalf("failure log missing: %q: %v", b, err)
			}
			if _, err := c.Run(t.Context(), request()); err == nil {
				t.Fatal("failed run silently retried")
			}
		})
	}
}

func TestRun_TimeoutKillsDescendants(t *testing.T) {
	c := setupController(t)
	req := request()
	req.Config.Worker = fixtureCommand("tree")
	req.Config.Worker.Timeout = "1s"
	start := time.Now()
	r, err := c.Run(t.Context(), req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error missing: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("teardown exceeded bound")
	}
	persisted := requireState(t, c, r.ID, "failed")
	if len(persisted.Evidence) != 1 {
		t.Fatal("timeout evidence missing")
	}
	assertDescendantStopped(t, r.Worktree)
}

func assertDescendantStopped(t *testing.T, worktree string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(worktree, "descendant.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		// Linux init/container runtimes may defer reaping an already dead
		// grandchild. A zombie is not running and cannot perform side effects.
		stat, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if readErr == nil && strings.Contains(string(stat), ") Z ") {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("descendant %d still exists/running: %v", pid, err)
		}
	}
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", path)
		}
	}
}

func TestRun_DurableCancellationAndConcurrentAdmission(t *testing.T) {
	c := setupController(t)
	req := request()
	req.Config.Worker = fixtureCommand("tree")
	req.Config.Worker.Timeout = "10s"
	type result struct {
		run Run
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := c.Run(t.Context(), req)
		done <- result{r, err}
	}()
	// Discover admission through the real durable store, with a bounded poll.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var active Run
	for active.ID == "" {
		active, _ = c.Store.Active(t.Context())
		if active.ID != "" {
			break
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("no admitted run")
		}
	}
	waitFile(t, filepath.Join(active.Worktree, "ready"))
	if _, err := c.Run(t.Context(), request()); err == nil {
		t.Fatal("simultaneous foreground run admitted")
	}
	if _, err := c.Reconcile(t.Context()); err == nil {
		t.Fatal("reconcile ignored active controller lock")
	}
	if err := c.Store.RequestCancel(t.Context(), active.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) || result.run.State != "cancelled" {
			t.Fatalf("cancel result %+v: %v", result.run, result.err)
		}
		requireState(t, c, active.ID, "cancelled")
		assertDescendantStopped(t, active.Worktree)
	case <-timer.C:
		t.Fatal("cancel did not tear down foreground worker")
	}
}

func TestReconcile_MarksOrphansWithoutSignallingOrRelaunch(t *testing.T) {
	c := setupController(t)
	r := Run{ID: "orphan", State: "worker_running", ControllerPID: os.Getpid(), WorkerPID: os.Getpid(),
		LaunchToken: "untrusted-stale-identity", Dir: c.Repo.Root, Worktree: c.Repo.Root, Evidence: []Evidence{}}
	if err := c.Store.Save(t.Context(), &r, "worker_running", "injected crash boundary", true); err != nil {
		t.Fatal(err)
	}
	runs, err := c.Reconcile(t.Context())
	if err != nil || len(runs) != 1 || runs[0].State != "needs_attention" {
		t.Fatalf("reconciliation %+v: %v", runs, err)
	}
	if _, err := c.Run(t.Context(), request()); err == nil {
		t.Fatal("orphan was silently relaunched")
	}
	if _, err := c.Release(t.Context(), r.ID, "unsafe", false); err == nil {
		t.Fatal("ambiguous orphan released without acknowledgement")
	}
	if _, err := c.Release(t.Context(), r.ID, "verified manually", true); err != nil {
		t.Fatal(err)
	}
}

func TestRun_BeadsReadinessAndClaimFailClosed(t *testing.T) {
	for _, mode := range []string{"bd-ready", "bd-blocked", "bd-owned", "bd-claim-fail", "bd-bad-json"} {
		t.Run(mode, func(t *testing.T) {
			c := setupController(t)
			req := request()
			req.TaskID = "task-1"
			req.Config.BeadsArgv = fixtureCommand(mode).Argv
			r, err := c.Run(t.Context(), req)
			if mode == "bd-ready" {
				if err != nil {
					t.Fatal(err)
				}
				requireState(t, c, r.ID, "awaiting_review")
				return
			}
			if err == nil {
				t.Fatal("unsafe claim/readiness succeeded")
			}
			state := "failed"
			if mode == "bd-claim-fail" {
				state = "needs_attention"
			}
			requireState(t, c, r.ID, state)
			if _, err := os.Stat(r.Worktree); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("worktree/worker launched after failed readiness or claim")
			}
		})
	}
}

func TestRun_OutputIsBoundedAndProfileSelected(t *testing.T) {
	c := setupController(t)
	req := request()
	req.Config.Profiles = map[string]Command{"chosen": fixtureCommand("flood")}
	req.Profile = "chosen"
	// A successful explicit check that needs no worker output artifact.
	req.Config.Checks = []Command{fixtureCommand("flood")}
	r, err := c.Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range r.Evidence {
		info, err := os.Stat(e.Log)
		if err != nil || info.Size() != outputLimit || !e.Truncated {
			t.Fatalf("output cap failed: %+v %v", e, err)
		}
	}
}

func TestRun_CleansDescendantsEvenWhenLeaderExitsFirst(t *testing.T) {
	c := setupController(t)
	req := request()
	req.Config.Worker = fixtureCommand("exit-with-child")
	req.Config.Checks = []Command{fixtureCommand("flood")}
	r, err := c.Run(t.Context(), req)
	if err != nil || r.State != "awaiting_review" {
		t.Fatalf("early leader exit: %+v: %v", r, err)
	}
	assertDescendantStopped(t, r.Worktree)
}

func TestRun_StartupFailureHasEvidenceAndNoPID(t *testing.T) {
	c := setupController(t)
	req := request()
	req.Config.Worker.Argv = []string{filepath.Join(t.TempDir(), "missing-worker")}
	r, err := c.Run(t.Context(), req)
	if err == nil || r.State != "failed" || r.WorkerPID != 0 {
		t.Fatalf("startup failure result: %+v: %v", r, err)
	}
	if len(r.Evidence) != 1 || r.Evidence[0].ExitCode != -1 || r.Evidence[0].Error == "" {
		t.Fatalf("missing startup evidence: %+v", r.Evidence)
	}
}
