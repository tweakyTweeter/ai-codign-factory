package factory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func workflowRequest() Request {
	req := request()
	req.Config.Worker = Command{}
	req.Config.Workflow = &Workflow{Phases: []Phase{
		{Name: "producer", Role: "producer", Command: fixtureCommand("phase")},
		{Name: "dev", Role: "dev", Command: fixtureCommand("phase")},
		{Name: "qa", Role: "qa", Command: fixtureCommand("phase")},
		{Name: "review", Role: "review", Command: fixtureCommand("phase")},
	}}
	return req
}

func TestWorkflow_IndependentEvidenceReachesHumanGate(t *testing.T) {
	c := setupController(t)
	r, err := c.Run(t.Context(), workflowRequest())
	if err != nil {
		t.Fatal(err)
	}
	requireState(t, c, r.ID, "awaiting_review")
	if len(r.Evidence) != 5 || len(r.Fingerprint) != 64 || r.Fingerprint != r.QAFingerprint {
		t.Fatalf("missing bound evidence: %+v", r)
	}
	for _, e := range r.Evidence[3:] {
		if e.Verdict == nil || e.Verdict.Verdict != "pass" || e.Fingerprint != r.Fingerprint || e.ResultFile == "" {
			t.Fatalf("missing independent verdict: %+v", e)
		}
	}
	// Candidate contains an untracked Dev file, and its content is bound too.
	if err := os.WriteFile(filepath.Join(r.Worktree, "worker.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	fp, err := Fingerprint(t.Context(), r.Worktree)
	if err != nil || fp == r.Fingerprint {
		t.Fatalf("untracked content not bound: %s: %v", fp, err)
	}
}

func TestWorkflow_FailsClosedOnMutationOrInvalidEvidence(t *testing.T) {
	for _, tc := range []struct {
		phase    int
		mode     string
		contains string
	}{
		{0, "phase-mutate", "nonwriting producer"},
		{2, "phase-mutate", "nonwriting qa"},
		{3, "phase-mutate", "nonwriting review"},
		{2, "phase-missing", "qa evidence"},
		{3, "phase-missing", "review evidence"},
		{2, "phase-bad-json", "qa evidence"},
		{3, "phase-blocking", "blocking"},
		{3, "phase-stale", "fingerprint"},
		{2, "phase-inconclusive", "inconclusive"},
		{3, "phase-flood", "truncated"},
		{3, "fail", "exit status"},
	} {
		t.Run(tc.mode+"-"+string(rune('0'+tc.phase)), func(t *testing.T) {
			c, req := setupController(t), workflowRequest()
			req.Config.Workflow.Phases[tc.phase].Command = fixtureCommand(tc.mode)
			r, err := c.Run(t.Context(), req)
			if err == nil || !strings.Contains(err.Error(), tc.contains) || r.State == "awaiting_review" {
				t.Fatalf("unsafe workflow accepted: %+v: %v", r, err)
			}
			if _, err := os.Stat(r.Worktree); err != nil {
				t.Fatalf("failed worktree removed: %v", err)
			}
		})
	}
}

func TestWorkflow_SpecialistsAreExplicitlySelectedAndNonwriting(t *testing.T) {
	for _, profile := range []string{"", "api", "unknown"} {
		t.Run(profile, func(t *testing.T) {
			c, req := setupController(t), workflowRequest()
			phases := req.Config.Workflow.Phases
			req.Config.Workflow.Phases = append([]Phase{phases[0], {Name: "api-plan", Role: "specialist", Profile: "api", Command: fixtureCommand("phase")}}, phases[1:]...)
			req.Profile = profile
			r, err := c.Run(t.Context(), req)
			if profile == "unknown" {
				if err == nil || r.ID != "" {
					t.Fatal("unknown profile admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 5
			if profile != "" {
				want++
			}
			if len(r.Evidence) != want {
				t.Fatalf("specialist invocation count: %d", len(r.Evidence))
			}
		})
	}
	c, req := setupController(t), workflowRequest()
	phases := req.Config.Workflow.Phases
	req.Config.Workflow.Phases = append([]Phase{phases[0], {Name: "api-plan", Role: "specialist", Profile: "api", Command: fixtureCommand("phase-mutate")}}, phases[1:]...)
	req.Profile = "api"
	if r, err := c.Run(t.Context(), req); err == nil || r.State == "awaiting_review" {
		t.Fatal("specialist mutation accepted")
	}
}

func TestWorkflow_InvalidOrdersAreRejected(t *testing.T) {
	for _, roles := range [][]string{
		{"dev", "producer", "qa", "review"}, {"producer", "dev", "review", "qa"},
		{"producer", "dev", "dev", "qa", "review"}, {"producer", "qa", "dev", "review"},
		{"producer", "dev", "qa", "review", "review"}, {"producer", "dev", "qa"},
	} {
		req := workflowRequest()
		req.Config.Workflow.Phases = nil
		for i, role := range roles {
			req.Config.Workflow.Phases = append(req.Config.Workflow.Phases, Phase{Name: role + string(rune('a'+i)), Role: role, Command: fixtureCommand("phase")})
		}
		if _, err := validateConfig(req.Config); err == nil {
			t.Fatalf("invalid order accepted: %v", roles)
		}
	}
}

func TestVerdict_RejectsMalformedUnknownMissingAndOversizedEvidence(t *testing.T) {
	v := Verdict{Version: 1, Verdict: "pass", Summary: "checked", Fingerprint: strings.Repeat("a", 64), Findings: []Finding{}}
	valid, _ := json.Marshal(v)
	for name, b := range map[string][]byte{
		"version":          []byte(strings.Replace(string(valid), `"version":1`, `"version":2`, 1)),
		"unknown":          []byte(strings.Replace(string(valid), `"version":1`, `"version":1,"extra":0`, 1)),
		"duplicate":        []byte(strings.Replace(string(valid), `"verdict":"pass"`, `"verdict":"fail","verdict":"pass"`, 1)),
		"case alias":       []byte(strings.Replace(string(valid), `"verdict"`, `"Verdict"`, 1)),
		"unknown severity": []byte(strings.Replace(string(valid), `"findings":[]`, `"findings":[{"severity":"unknown","summary":"finding"}]`, 1)),
		"missing findings": []byte(strings.Replace(string(valid), `"findings":[]`, `"findings":null`, 1)),
		"empty summary":    []byte(strings.Replace(string(valid), `"checked"`, `""`, 1)),
		"unknown verdict":  []byte(strings.Replace(string(valid), `"pass"`, `"maybe"`, 1)),
		"trailing":         append(append([]byte{}, valid...), valid...),
		"truncated":        valid[:len(valid)-1],
		"oversized":        []byte(strings.Repeat("x", outputLimit+1)),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result.json")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readVerdict(path, v.Fingerprint); err == nil {
				t.Fatal("invalid verdict accepted")
			}
		})
	}
}

func TestRun_OwnershipAndHeartbeatLossStopOwnedWorker(t *testing.T) {
	for _, loss := range []string{"owner", "heartbeat", "cancel"} {
		t.Run(loss, func(t *testing.T) {
			c := setupController(t)
			c.leaseInterval = 20 * time.Millisecond
			c.cancelInterval = 5 * time.Millisecond
			req := request()
			req.TaskID = "task-1"
			req.Config.Worker = fixtureCommand("tree")
			req.Config.Worker.Timeout = "15s"
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			type outcome struct {
				r   Run
				err error
			}
			done := make(chan outcome, 1)
			go func() { r, err := c.Run(ctx, req); done <- outcome{r, err} }()
			active := waitActive(t, c)
			waitFile(t, filepath.Join(active.Worktree, "ready"))
			switch loss {
			case "owner":
				if err := os.WriteFile(filepath.Join(c.Repo.Root, "bd-owner"), []byte("new-owner"), 0600); err != nil {
					t.Fatal(err)
				}
			case "heartbeat":
				if err := os.WriteFile(filepath.Join(c.Repo.Root, "bd-heartbeat-fail"), []byte("fail"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if err := c.Store.RequestCancel(ctx, active.ID); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case got := <-done:
				if got.err == nil || got.r.State != "needs_attention" {
					t.Fatalf("unsafe lease outcome: %+v: %v", got.r, got.err)
				}
				if loss != "cancel" && !errors.Is(got.err, ErrLease) {
					t.Fatalf("missing lease cause: %v", got.err)
				}
				assertDescendantStopped(t, active.Worktree)
				requireState(t, c, active.ID, "needs_attention")
			case <-ctx.Done():
				t.Fatal("ownership monitor failed to stop worker")
			}
		})
	}
}

func waitActive(t *testing.T, c Controller) Run {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if r, err := c.Store.Active(t.Context()); err == nil {
			return r
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("run not admitted")
		}
	}
}

func TestMonitor_StopAndCancelJoinEveryGoroutine(t *testing.T) {
	c := setupController(t)
	c.cancelInterval = time.Nanosecond
	c.leaseInterval = time.Nanosecond
	r := Run{ID: "monitor-test"}
	if err := c.Store.Save(t.Context(), &r, "admitted", "fixture", true); err != nil {
		t.Fatal(err)
	}

	for range 100 {
		ctx, cancel := context.WithCancelCause(t.Context())
		stop := c.monitor(ctx, cancel, r, fixtureConfig())
		done := make(chan struct{})
		go func() { defer close(done); cancel(context.Canceled) }()
		stop()
		<-done
		stop()
	}
}

func TestRun_TimeoutAlsoStopsNestedAdapterChildren(t *testing.T) {
	c, req := setupController(t), request()
	req.Config.Worker = fixtureCommand("adapter-tree")
	req.Config.Worker.Timeout = "1s"
	r, err := c.Run(t.Context(), req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("nested adapter timeout: %v", err)
	}
	assertDescendantStopped(t, r.Worktree)
}

func TestRun_OwnershipIsCheckedBeforeChecksAndFinalGate(t *testing.T) {
	for _, where := range []string{"before-check", "final-gate"} {
		t.Run(where, func(t *testing.T) {
			c := setupController(t)
			c.leaseInterval = time.Hour // prove boundary checks, not periodic detection
			req := request()
			if where == "before-check" {
				req.Config.Worker = fixtureCommand("worker-owner-loss")
			} else {
				req = workflowRequest()
				req.Config.Workflow.Phases[3].Command = fixtureCommand("phase-owner-loss")
			}
			req.TaskID = "task-1"
			r, err := c.Run(t.Context(), req)
			if !errors.Is(err, ErrLease) || r.State != "needs_attention" {
				t.Fatalf("ownership loss accepted at %s: %+v: %v", where, r, err)
			}
			if where == "before-check" && len(r.Evidence) != 1 {
				t.Fatal("checks launched after ownership changed")
			}
			if where == "final-gate" && len(r.Evidence) != 5 {
				t.Fatal("fixture did not reach final gate")
			}
		})
	}
}

func TestRun_InvalidHeartbeatCannotAuthorizeLaunch(t *testing.T) {
	for _, response := range []string{"bad-json", "wrong-owner", "wrong-version"} {
		t.Run(response, func(t *testing.T) {
			c, req := setupController(t), request()
			req.TaskID = "task-1"
			if err := os.WriteFile(filepath.Join(c.Repo.Root, "bd-heartbeat-fail"), []byte(response), 0600); err != nil {
				t.Fatal(err)
			}
			r, err := c.Run(t.Context(), req)
			if !errors.Is(err, ErrLease) || r.State != "needs_attention" || len(r.Evidence) != 0 {
				t.Fatalf("invalid heartbeat authorized work: %+v: %v", r, err)
			}
		})
	}
}
