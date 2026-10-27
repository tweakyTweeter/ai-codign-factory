package factory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Controller struct {
	Repo  Repo
	Store *Store
	// Test seams only; production cadence is comfortably below bd's 5m TTL.
	leaseInterval  time.Duration
	cancelInterval time.Duration
}

type Request struct {
	Config  Config
	Prompt  []byte
	TaskID  string
	Profile string
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// durableContext is intentionally detached from a cancelled worker context:
// cancellation outcomes must still reach disk. Every such write is bounded.
func durableContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (c Controller) save(r *Run, state, detail string, create bool) error {
	ctx, cancel := durableContext()
	defer cancel()
	if !create {
		requested, err := c.Store.CancelRequested(ctx, r.ID)
		if err != nil {
			return err
		}
		r.CancelRequested = requested
	}
	return c.Store.Save(ctx, r, state, detail, create)
}

func (c Controller) Run(ctx context.Context, req Request) (run Run, result error) {
	config, err := validateConfig(req.Config)
	if err != nil {
		return run, err
	}
	req.Config = config
	lock, err := c.Repo.Lock()
	if err != nil {
		return run, err
	}
	defer lock.Close()
	if active, err := c.Store.Active(ctx); err == nil {
		return run, fmt.Errorf("run %s is %s; reconcile/review and explicitly release it first", active.ID, active.State)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return run, err
	}
	worker := req.Config.Worker
	if req.Profile != "" && req.Config.Workflow == nil {
		var ok bool
		worker, ok = req.Config.Profiles[req.Profile]
		if !ok {
			return run, fmt.Errorf("unknown profile %q", req.Profile)
		}
	}
	if req.Config.Workflow != nil && req.Profile != "" {
		found := false
		for _, p := range req.Config.Workflow.Phases {
			if p.Role == "specialist" && p.Profile == req.Profile {
				found = true
			}
		}
		if !found {
			return run, fmt.Errorf("unknown specialist profile %q", req.Profile)
		}
	}
	if len(req.Prompt) == 0 || len(req.Prompt) > outputLimit {
		return run, errors.New("prompt must contain 1 byte to 1 MiB")
	}
	head, err := gitOutput(ctx, c.Repo.Root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return run, err
	}
	id, err := randomID()
	if err != nil {
		return run, err
	}
	token, err := randomID()
	if err != nil {
		return run, err
	}
	dir := filepath.Join(c.Repo.State, "runs", id)
	run = Run{
		ID: id, TaskID: req.TaskID, Profile: req.Profile, Head: head,
		Branch: "factory/" + id, Worktree: filepath.Join(c.Repo.Worktrees, id), Dir: dir,
		ControllerPID: os.Getpid(), LaunchToken: token, CreatedAt: timestamp(),
		Actor:    "factory-" + id,
		Evidence: []Evidence{},
	}
	if err := c.save(&run, "admitted", "run reserved; no external side effects yet", true); err != nil {
		return run, err
	}
	// Any unexpected return after admission is conservative. No retry, no
	// deleting worktrees, no unclaiming tasks whose ownership may be ambiguous.
	defer func() {
		if result != nil && (errors.Is(result, ErrLease) || (req.TaskID != "" && (errors.Is(result, context.Canceled) || ctx.Err() != nil))) {
			result = errors.Join(result, c.save(&run, "needs_attention", "claim retained; cancellation or lease/ownership loss: "+result.Error(), false))
		}
		if result != nil && !stable(run.State) {
			result = errors.Join(result, c.save(&run, "needs_attention", "interrupted setup/transition: "+result.Error(), false))
		}
	}()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return run, err
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.txt"), req.Prompt, 0600); err != nil {
		return run, err
	}
	configJSON, err := json.MarshalIndent(req.Config, "", "  ")
	if err != nil {
		return run, err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), configJSON, 0600); err != nil {
		return run, err
	}
	if req.TaskID != "" {
		if err := c.save(&run, "preflight", "checking Beads task and dependency readiness", false); err != nil {
			return run, err
		}
		show, err := preflight(ctx, c.Repo, req.Config.BeadsArgv, req.TaskID)
		if err != nil {
			err = errors.Join(err, c.save(&run, "failed", "Beads preflight: "+err.Error(), false))
			return run, err
		}
		if err := os.WriteFile(filepath.Join(dir, "task.json"), show, 0600); err != nil {
			return run, err
		}
		if err := c.save(&run, "claiming", "intent: bd update --claim; ownership may be ambiguous after interruption", false); err != nil {
			return run, err
		}
		claim, err := beadCommand(ctx, c.Repo, req.Config.BeadsArgv, "update", req.TaskID, "--claim", "--actor", run.Actor, "--json")
		writeErr := os.WriteFile(filepath.Join(dir, "claim.json"), claim, 0600)
		if err != nil || writeErr != nil {
			return run, errors.Join(err, writeErr) // retain claiming => needs_attention
		}
		if err := c.save(&run, "claimed", "Beads claim command succeeded; never automatically unclaim or close", false); err != nil {
			return run, err
		}
		if err := c.owned(ctx, run, req.Config); err != nil {
			return run, err
		}
	}
	workerCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stopMonitor := c.monitor(workerCtx, cancel, run, req.Config)
	defer func() {
		stopMonitor()
		if result != nil && workerCtx.Err() != nil {
			result = errors.Join(result, context.Cause(workerCtx))
		}
	}()
	contextJSON, err := json.MarshalIndent(struct {
		Run           Run    `json:"run"`
		Repo          Repo   `json:"repo"`
		PromptFile    string `json:"prompt_file"`
		TaskFile      string `json:"task_file,omitempty"`
		TrustBoundary string `json:"trust_boundary"`
	}{run, c.Repo, filepath.Join(dir, "prompt.txt"), filepath.Join(dir, "task.json"),
		"trusted local commands, not a sandbox; committed HEAD only; no automatic commit, merge, push, deploy or task closure"}, "", "  ")
	if err != nil {
		return run, err
	}
	if err := os.WriteFile(filepath.Join(dir, "context.json"), contextJSON, 0600); err != nil {
		return run, err
	}
	if err := sealArtifacts(workerCtx, &run, "prompt.txt", "config.json", "context.json", "task.json", "claim.json"); err != nil {
		return run, err
	}
	if err := c.save(&run, "creating_worktree", "intent: create isolated branch/worktree at explicit HEAD", false); err != nil {
		return run, err
	}
	if err := prepareWorktreesRoot(c.Repo.Common, c.Repo.Worktrees, run.Worktree); err != nil {
		return run, err
	}
	if _, err := gitOutput(workerCtx, c.Repo.Root, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-b", run.Branch, run.Worktree, run.Head); err != nil {
		return run, err
	}
	env := []string{
		"FACTORY_RUN_ID=" + run.ID, "FACTORY_WORKTREE=" + run.Worktree,
		"FACTORY_REPO=" + c.Repo.Root, "FACTORY_TASK_ID=" + run.TaskID,
		"FACTORY_BEADS_ACTOR=" + run.Actor,
		"FACTORY_PROMPT_FILE=" + filepath.Join(dir, "prompt.txt"),
		"FACTORY_CONTEXT_FILE=" + filepath.Join(dir, "context.json"),
	}
	if req.Config.Workflow != nil {
		if err := c.workflow(workerCtx, &run, req.Config, env, req.Profile); err != nil {
			return run, errors.Join(err, context.Cause(workerCtx))
		}
	} else {
		if err := c.owned(workerCtx, run, req.Config); err != nil {
			return run, err
		}
		if err := c.command(workerCtx, &run, "worker", worker, env); err != nil {
			return run, err
		}
		for i, check := range req.Config.Checks {
			if err := c.owned(workerCtx, run, req.Config); err != nil {
				return run, err
			}
			if err := c.command(workerCtx, &run, fmt.Sprintf("check-%02d", i+1), check, env); err != nil {
				return run, err
			}
		}
	}
	stopMonitor()
	if err := verifyIntegrity(workerCtx, run); err != nil {
		return run, err
	}
	if err := c.owned(workerCtx, run, req.Config); err != nil {
		return run, err
	}
	if req.Config.Workflow != nil {
		fp, err := Fingerprint(workerCtx, run.Worktree)
		if err != nil {
			return run, err
		}
		if fp != run.Fingerprint {
			return run, errors.New("patch changed before human gate")
		}
	}
	requested, err := c.Store.CancelRequested(workerCtx, run.ID)
	if err != nil || requested {
		return run, errors.Join(context.Canceled, err)
	}
	if err := workerCtx.Err(); err != nil {
		err = errors.Join(context.Cause(workerCtx), c.save(&run, "cancelled", "cancelled before review gate", false))
		return run, err
	}
	detail := "checks passed; human review/integration required; branch and worktree retained"
	if req.Config.Workflow != nil {
		detail = "Dev completion, checks and independent QA/review verdicts accepted; human integration required; recheck patch and ownership before integration"
	}
	err = c.save(&run, "awaiting_review", detail, false)
	if errors.Is(err, context.Canceled) {
		err = errors.Join(err, c.save(&run, "cancelled", "durable cancellation won the human gate", false))
	}
	return run, err
}

func stable(state string) bool {
	switch state {
	case "awaiting_review", "failed", "cancelled", "needs_attention", "released":
		return true
	default:
		return false
	}
}

func (c Controller) command(parent context.Context, run *Run, stage string, command Command, env []string) error {
	if err := verifyIntegrity(parent, *run); err != nil {
		return err
	}
	run.Stage = stage
	if stage == "worker" {
		run.Role = "dev"
	}
	if strings.HasPrefix(stage, "check-") {
		run.Role = "check"
	}
	requested, err := c.Store.CancelRequested(parent, run.ID)
	if err != nil || requested || parent.Err() != nil {
		return errors.Join(context.Canceled, err, c.save(run, "cancelled", "cancelled before "+stage+" launch", false))
	}
	timeout, err := time.ParseDuration(command.Timeout)
	if err != nil {
		return err
	}
	if err := c.save(run, stage+"_starting", "intent: launch configured "+stage, false); err != nil {
		return err
	}
	logPath := filepath.Join(run.Dir, stage+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	w := &cappedWriter{dst: f, remaining: outputLimit}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	e := Evidence{Stage: stage, Role: run.Role, Log: logPath, StartedAt: timestamp()}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "FACTORY_OUTPUT_DIR":
			e.OutputDir = value
		case "FACTORY_RESULT_FILE":
			e.ResultFile = value
		}
	}
	e.ExitCode, err = execute(ctx, run.Worktree, command.Argv, env, w, func(pid int) error {
		run.WorkerPID = pid
		return c.save(run, stage+"_running", "owned child started; PID is evidence only, never a recovery kill target", false)
	})
	e.FinishedAt, e.Truncated = timestamp(), w.truncated
	err = errors.Join(err, f.Sync(), f.Close())
	err = errors.Join(err, verifyIntegrity(parent, *run))
	if err == nil {
		names := []string{stage + ".log"}
		if e.OutputDir != "" {
			name, relErr := filepath.Rel(run.Dir, e.OutputDir)
			if relErr != nil {
				err = relErr
			} else {
				names = append(names, name)
			}
		}
		if err == nil {
			err = sealArtifacts(parent, run, names...)
		}
	}
	if parent.Err() != nil {
		err = errors.Join(err, context.Cause(parent))
	}
	if w.truncated && strings.HasPrefix(stage, "phase-") {
		err = errors.Join(err, errors.New("phase output truncated"))
	}
	if err != nil {
		e.Error = err.Error()
	}
	run.WorkerPID = 0
	run.Evidence = append(run.Evidence, e)
	if err != nil {
		state := "failed"
		if errors.Is(err, context.Canceled) {
			state = "cancelled"
		}
		if errors.Is(err, ErrTeardown) {
			state = "needs_attention"
		}
		return errors.Join(fmt.Errorf("%s: %w (evidence: %s)", stage, err, logPath), c.save(run, state, stage+" failed: "+err.Error(), false))
	}
	return c.save(run, stage+"_passed", "evidence recorded: "+logPath, false)
}

// monitor owns no Run mutation. The controller joins it on every return, so a
// cancelled DB read/heartbeat cannot outlive its run or race durable snapshots.
func (c Controller) monitor(ctx context.Context, cancel context.CancelCauseFunc, run Run, cfg Config) func() {
	monitorCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	leaseEvery, cancelEvery := c.leaseInterval, c.cancelInterval
	if leaseEvery <= 0 {
		leaseEvery = 30 * time.Second
	}
	if cancelEvery <= 0 {
		cancelEvery = 100 * time.Millisecond
	}
	go func() {
		defer close(done)
		lease := time.NewTicker(leaseEvery)
		poll := time.NewTicker(cancelEvery)
		defer lease.Stop()
		defer poll.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-poll.C:
				requested, err := c.Store.CancelRequested(monitorCtx, run.ID)
				if err != nil || requested {
					if monitorCtx.Err() == nil {
						cause := errors.Join(context.Canceled, err)
						if err != nil {
							cause = errors.Join(ErrLease, cause)
						}
						cancel(cause)
					}
					return
				}
			case <-lease.C:
				if err := c.owned(monitorCtx, run, cfg); err != nil {
					if monitorCtx.Err() == nil {
						cancel(err)
					}
					return
				}
			}
		}
	}()
	return func() { stop(); <-done }
}

// Reconcile only runs with the common-dir lock. No PID is ever signalled,
// no work is relaunched, and incomplete worktrees/claims are retained.
func (c Controller) Reconcile(ctx context.Context) ([]Run, error) {
	lock, err := c.Repo.Lock()
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	runs, err := c.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		if !stable(runs[i].State) {
			if err := c.save(&runs[i], "needs_attention", "controller lock was free but run was incomplete; possible orphan/claim; no PID killed and no relaunch", false); err != nil {
				return nil, err
			}
		}
	}
	return runs, nil
}

func (c Controller) Release(ctx context.Context, id, reason string, ackOrphan bool) (Run, error) {
	lock, err := c.Repo.Lock()
	if err != nil {
		return Run{}, err
	}
	defer lock.Close()
	r, err := c.Store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if !stable(r.State) || r.State == "released" {
		return r, errors.New("run must be reconciled/reviewed before release")
	}
	if r.Integration != nil && r.Integration.State != "complete" && !ackOrphan {
		return r, errors.New("incomplete integration retains admission; inspect target, task and possible surviving checks before manual release with --ack-orphan")
	}
	if r.State == "needs_attention" && !ackOrphan {
		return r, errors.New("possible orphan: inspect and stop descendants manually, verify Beads ownership, then use --ack-orphan")
	}
	if reason == "" {
		return r, errors.New("release requires a nonempty reason")
	}
	err = c.save(&r, "released", "human acknowledgement (NOT integration): "+reason, false)
	return r, err
}
