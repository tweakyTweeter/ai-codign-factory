package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Integration records explicit local application, never a commit or merge.
// Uncertain apply/check attempts are retained for manual inspection, not retried.
type Integration struct {
	Target      string                  `json:"target"`
	State       string                  `json:"state"`
	Detail      string                  `json:"detail"`
	Fingerprint string                  `json:"fingerprint"`
	Marker      string                  `json:"marker"`
	Paths       []string                `json:"paths"`
	Expected    map[string]fileIdentity `json:"expected"`
	Index       string                  `json:"index"`
	Checks      []Evidence              `json:"checks"`
}

type fileIdentity struct {
	Kind   string `json:"kind"`
	Digest string `json:"sha256"`
	Exec   bool   `json:"executable"`
}

// Integrate applies an approved patch to the invoking checkout under the common
// controller lock. Dirty unrelated sources are preserved and checked for mutation.
func (c Controller) Integrate(ctx context.Context, id string) (r Run, result error) {
	lock, err := c.Repo.Lock()
	if err != nil {
		return r, err
	}
	defer lock.Close()
	r, err = c.Store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if r.Integration != nil && r.Integration.State == "complete" && r.State == "released" {
		return r, nil // Historical result; never reapply a completed integration.
	}
	if r.Integration != nil && (r.Integration.Target != c.Repo.Root ||
		r.Integration.Fingerprint != r.Fingerprint ||
		r.Integration.Marker != integrationMarker(r)) {
		return r, errors.New("integration identity mismatch")
	}
	if c.Repo.Root == r.Worktree {
		return r, errors.New("integration target must not be the candidate worktree")
	}
	defer func() {
		if result != nil && r.Integration != nil {
			r.Integration.Detail = result.Error()
			state := r.State
			dctx, cancel := durableContext()
			defer cancel()
			if requested, err := c.Store.CancelRequested(dctx, r.ID); err != nil {
				result = errors.Join(result, err)
			} else if requested {
				state = "needs_attention"
				if r.HumanReview != nil {
					r.HumanReview.Decision = "not-approved"
				}
			}
			result = errors.Join(result, c.save(&r, state, "integration blocked: "+result.Error(), false))
		}
	}()
	cfg, err := LoadConfig(filepath.Join(r.Dir, "config.json"))
	if err != nil {
		return r, err
	}
	if err := c.integrationApproval(ctx, &r, cfg); err != nil {
		return r, err
	}
	if r.Integration == nil {
		if err := c.reviewTask(ctx, r, cfg.BeadsArgv); err != nil {
			return r, err
		}
		if err := c.integrationBlockers(ctx, r, cfg); err != nil {
			return r, err
		}
		if err := c.owned(ctx, r, cfg); err != nil {
			return r, err
		}
		paths, err := integrationPaths(ctx, r.Worktree)
		if err != nil {
			return r, err
		}
		if len(paths) == 0 {
			return r, errors.New("candidate has no changes to integrate")
		}
		if err := cleanIntegrationPaths(ctx, c.Repo.Root, paths); err != nil {
			return r, err
		}
		before, index, err := sourceSnapshot(ctx, c.Repo.Root, paths)
		if err != nil {
			return r, err
		}
		for _, path := range paths {
			state, err := identifyFile(r.Worktree, path)
			if err != nil {
				return r, err
			}
			if state.Kind == "" {
				delete(before, path)
			} else {
				before[path] = state
			}
		}
		r.Integration = &Integration{Target: c.Repo.Root, State: "apply-intent",
			Fingerprint: r.Fingerprint, Marker: integrationMarker(r), Paths: paths, Expected: before, Index: index}
		if err := c.save(&r, r.State, "intent: explicitly apply approved candidate; no commit or staging", false); err != nil {
			return r, err
		}
		patch := filepath.Join(r.HumanReview.Bundle, "candidate.diff")
		if _, err := gitOutput(ctx, c.Repo.Root, "apply", "--check", "--binary", "--whitespace=nowarn", patch); err != nil {
			r.Integration.State = "blocked"
			return r, err
		}
		if err := c.integrationApproval(ctx, &r, cfg); err != nil {
			return r, err
		}
		if err := c.reviewTask(ctx, r, cfg.BeadsArgv); err != nil {
			return r, err
		}
		if err := cleanIntegrationPaths(ctx, c.Repo.Root, paths); err != nil {
			return r, err
		}
		if _, err := gitOutput(ctx, c.Repo.Root, "apply", "--binary", "--whitespace=nowarn", patch); err != nil {
			return r, fmt.Errorf("apply outcome requires manual inspection: %w", err)
		}
		if err := c.integrationTarget(ctx, r); err != nil {
			return r, err
		}
		r.Integration.State = "applied"
		if err := c.save(&r, r.State, "approved patch applied; checks not yet accepted", false); err != nil {
			return r, err
		}
	} else {
		switch r.Integration.State {
		case "applied", "checked", "close-intent", "task-closed":
		default:
			return r, errors.New("uncertain or failed integration requires manual inspection; no automatic apply/check retry")
		}
		if err := c.integrationTarget(ctx, r); err != nil {
			return r, err
		}
	}
	if r.Integration.State == "applied" {
		if err := c.integrationChecks(ctx, &r, cfg); err != nil {
			return r, err
		}
		r.Integration.State = "checked"
		if err := c.save(&r, r.State, "integration checks passed with unchanged target and index", false); err != nil {
			return r, err
		}
	}
	if err := c.integrationApproval(ctx, &r, cfg); err != nil {
		return r, err
	}
	if err := c.integrationTarget(ctx, r); err != nil {
		return r, err
	}
	return c.finishIntegration(ctx, r, cfg)
}

func integrationMarker(r Run) string {
	return "factory-integrated:" + r.ID + ":" + r.Fingerprint
}

func (c Controller) integrationApproval(ctx context.Context, r *Run, cfg Config) error {
	requested, err := c.Store.CancelRequested(ctx, r.ID)
	if err != nil {
		return err
	}
	r.CancelRequested = requested
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.validateReview(ctx, *r); err != nil {
		if r.HumanReview != nil {
			r.HumanReview.Decision, r.HumanReview.Detail = "not-approved", err.Error()
			dctx, cancel := durableContext()
			defer cancel()
			return errors.Join(err, c.Store.SaveHumanReview(dctx, r, "integration approval invalidated: "+err.Error()))
		}
		return err
	}
	if err := supervisedReview(ctx, *r, cfg); err != nil {
		return err
	}
	h := r.HumanReview
	if h.PublishState != "published" || h.GateID == "" || !reflect.DeepEqual(h.BeadsArgv, cfg.BeadsArgv) {
		return errors.New("no known immutable published review")
	}
	gates, err := c.reviewGates(ctx, *r)
	if err != nil {
		return err
	}
	g, err := validateNativeGate(h, gates)
	if err != nil {
		return err
	}
	if g.Status != "closed" || gateDecision(g.CloseReason, h) != "approved" {
		h.Decision = "not-approved"
		return errors.New("current native gate does not approve this exact candidate")
	}
	head, err := gitOutput(ctx, c.Repo.Root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != r.Head {
		return errors.New("target HEAD moved; automatic rebase/integration is not supported")
	}
	h.Decision, h.ObservedReason, h.ObservedAt = "approved", g.CloseReason, timestamp()
	return nil
}

func (c Controller) integrationTarget(ctx context.Context, r Run) error {
	current, index, err := sourceSnapshot(ctx, c.Repo.Root, r.Integration.Paths)
	if err != nil {
		return err
	}
	if index != r.Integration.Index || !reflect.DeepEqual(current, r.Integration.Expected) {
		return errors.New("target source or index differs from approved application; retained for manual inspection")
	}
	return nil
}

func (c Controller) integrationChecks(ctx context.Context, r *Run, cfg Config) error {
	checkCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if err := c.owned(checkCtx, *r, cfg); err != nil {
		return err
	}
	stop := c.monitor(checkCtx, cancel, *r, cfg)
	defer stop()
	r.Integration.State = "checks-running"
	if err := c.save(r, r.State, "intent: run integration checks in target checkout", false); err != nil {
		return err
	}
	for i, command := range cfg.Checks {
		if err := c.integrationApproval(checkCtx, r, cfg); err != nil {
			return err
		}
		if err := c.reviewTask(checkCtx, *r, cfg.BeadsArgv); err != nil {
			return err
		}
		stage := fmt.Sprintf("integration-check-%02d", i+1)
		log := filepath.Join(r.Dir, stage+".log")
		f, err := os.OpenFile(log, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		timeout, err := time.ParseDuration(command.Timeout)
		if err != nil {
			return errors.Join(err, f.Close())
		}
		child, done := context.WithTimeout(checkCtx, timeout)
		w := &cappedWriter{dst: f, remaining: outputLimit}
		e := Evidence{Role: "check", Stage: stage, Log: log, StartedAt: timestamp()}
		e.ExitCode, err = execute(child, c.Repo.Root, command.Argv, []string{
			"FACTORY_RUN_ID=" + r.ID, "FACTORY_WORKTREE=" + c.Repo.Root,
			"FACTORY_REPO=" + c.Repo.Root, "FACTORY_TASK_ID=" + r.TaskID,
			"FACTORY_BEADS_ACTOR=" + r.Actor,
			"FACTORY_ROLE=check", "FACTORY_PHASE=" + stage,
			"FACTORY_PROMPT_FILE=" + filepath.Join(r.Dir, "prompt.txt"),
			"FACTORY_CONTEXT_FILE=" + filepath.Join(r.Dir, "context.json"),
		}, w, nil)
		done()
		err = errors.Join(err, f.Sync(), f.Close(), context.Cause(checkCtx))
		err = errors.Join(err, sealArtifacts(checkCtx, r, stage+".log"))
		if w.truncated {
			err = errors.Join(err, errors.New("integration check output truncated"))
		}
		e.FinishedAt, e.Truncated = timestamp(), w.truncated
		if err == nil {
			err = c.integrationTarget(checkCtx, *r)
		}
		if err != nil {
			e.Error = err.Error()
		}
		r.Integration.Checks = append(r.Integration.Checks, e)
		if saveErr := c.save(r, r.State, stage+" evidence recorded", false); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		if err != nil {
			r.Integration.State = "blocked"
			return fmt.Errorf("%s failed (log %s): %w", stage, log, err)
		}
	}
	return nil
}

func (c Controller) finishIntegration(ctx context.Context, r Run, cfg Config) (Run, error) {
	// The exact marker lets a retry reconcile a lost conditional-close response.
	show := func() (bead, string, error) {
		b, err := beadCommand(ctx, c.Repo, cfg.BeadsArgv, "show", r.TaskID, "--json")
		if err != nil {
			return bead{}, "", err
		}
		var tasks []struct {
			bead
			Notes string `json:"notes"`
		}
		if err := json.Unmarshal(b, &tasks); err != nil {
			return bead{}, "", err
		}
		if len(tasks) != 1 || tasks[0].ID != r.TaskID || tasks[0].Assignee != r.Actor {
			return bead{}, "", errors.New("integration task ownership changed")
		}
		return tasks[0].bead, tasks[0].Notes, nil
	}
	task, notes, err := show()
	if err != nil {
		return r, err
	}
	if task.Status == "in_progress" && r.Integration.State == "checked" {
		if err := c.reviewTask(ctx, r, cfg.BeadsArgv); err != nil {
			return r, err
		}
		// Inspect all current blockers before the conditional status update.
		if err := c.integrationBlockers(ctx, r, cfg); err != nil {
			return r, err
		}
		if err := c.integrationApproval(ctx, &r, cfg); err != nil {
			return r, err
		}
		r.Integration.State = "close-intent"
		if err := c.save(&r, r.State, "intent: conditionally close integrated task", false); err != nil {
			return r, err
		}
		_, closeErr := beadCommand(ctx, c.Repo, cfg.BeadsArgv, "update", r.TaskID,
			"--status", "closed", "--if-status", "in_progress", "--if-assignee", r.Actor,
			"--append-notes", r.Integration.Marker, "--actor", r.Actor, "--json")
		task, notes, err = show()
		if err != nil || task.Status != "closed" || !standaloneLine(notes, r.Integration.Marker) {
			return r, errors.Join(closeErr, err, errors.New("conditional task completion uncertain; inspect before retry"))
		}
	} else if task.Status != "closed" || !standaloneLine(notes, r.Integration.Marker) ||
		(r.Integration.State != "close-intent" && r.Integration.State != "task-closed") {
		return r, errors.New("task completion does not match durable integration intent")
	}
	r.Integration.State = "task-closed"
	if err := c.save(&r, r.State, "integrated task closure verified", false); err != nil {
		return r, err
	}
	if err := c.integrationApproval(ctx, &r, cfg); err != nil {
		return r, err
	}
	if err := c.integrationTarget(ctx, r); err != nil {
		return r, err
	}
	if err := c.integrationBlockers(ctx, r, cfg); err != nil {
		return r, err
	}
	r.Integration.State, r.Integration.Detail = "complete", "approved patch applied, checks passed, task closed; no commit or push"
	err = c.save(&r, "released", r.Integration.Detail, false)
	if err != nil {
		r.Integration.State, r.Integration.Detail = "task-closed", "task closure verified; release not confirmed"
	}
	return r, err
}

func standaloneLine(text, line string) bool {
	for _, s := range strings.Split(text, "\n") {
		if s == line {
			return true
		}
	}
	return false
}

func (c Controller) integrationBlockers(ctx context.Context, r Run, cfg Config) error {
	data, err := beadCommand(ctx, c.Repo, cfg.BeadsArgv, "show", r.TaskID, "--json")
	if err != nil {
		return err
	}
	var tasks []struct {
		Dependencies []struct {
			ID   string `json:"id"`
			Type string `json:"dependency_type"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &tasks); err != nil {
		return err
	}
	if len(tasks) != 1 {
		return errors.New("missing task dependencies")
	}
	for _, dep := range tasks[0].Dependencies {
		if dep.Type != "blocks" {
			continue
		}
		data, err := beadCommand(ctx, c.Repo, cfg.BeadsArgv, "show", dep.ID, "--json")
		if err != nil {
			return err
		}
		var blockers []bead
		if err := json.Unmarshal(data, &blockers); err != nil {
			return err
		}
		if len(blockers) != 1 || blockers[0].ID != dep.ID || blockers[0].Status != "closed" {
			return errors.New("task still has an unresolved blocker")
		}
	}
	return nil
}

func integrationPaths(ctx context.Context, root string) ([]string, error) {
	set := map[string]bool{}
	for _, args := range [][]string{
		{"diff", "--no-renames", "--name-only", "-z", "HEAD", "--"},
		{"ls-files", "--others", "-z"},
	} {
		out, err := gitOutput(ctx, root, args...)
		if err != nil {
			return nil, err
		}
		for _, path := range strings.Split(out, "\x00") {
			if path != "" {
				set[path] = true
			}
		}
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		if err := safeIntegrationPath(path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func safeIntegrationPath(path string) error {
	if path == "." || path == ".." || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe integration path %q", path)
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.EqualFold(part, ".git") {
			return fmt.Errorf("Git administration path cannot be integrated: %q", path)
		}
	}
	return nil
}

func cleanIntegrationPaths(ctx context.Context, root string, paths []string) error {
	// Reject both staged and unstaged edits, even when their combined diff is empty.
	for _, args := range [][]string{
		{"diff", "--name-only", "-z", "--"},
		{"diff", "--cached", "--name-only", "-z", "HEAD", "--"},
	} {
		out, err := gitOutput(ctx, root, args...)
		if err != nil {
			return err
		}
		for _, dirty := range strings.Split(out, "\x00") {
			for _, path := range paths {
				if dirty == path {
					return fmt.Errorf("destination has staged/unstaged changes: %s", path)
				}
			}
		}
	}
	for _, path := range paths {
		if _, err := identifyFile(root, path); err != nil {
			return err
		}
		// Git ls-tree also detects unsupported gitlinks (submodules).
		entry, err := gitOutput(ctx, root, "--literal-pathspecs", "ls-tree", "HEAD", "--", path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry, "160000 ") || strings.HasPrefix(entry, "040000 ") {
			return fmt.Errorf("directory/submodule transitions are not supported: %s", path)
		}
		if entry == "" {
			if _, err := os.Lstat(filepath.Join(root, path)); err == nil {
				return fmt.Errorf("destination addition already exists: %s", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func identifyFile(root, path string) (fileIdentity, error) {
	if err := safeIntegrationPath(path); err != nil {
		return fileIdentity{}, err
	}
	for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
		info, err := os.Lstat(filepath.Join(root, parent))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fileIdentity{}, err
		}
		if !info.IsDir() {
			return fileIdentity{}, fmt.Errorf("non-directory/symlink integration ancestor: %s", parent)
		}
	}
	full := filepath.Join(root, path)
	info, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return fileIdentity{}, nil
	}
	if err != nil {
		return fileIdentity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		return fileIdentity{Kind: "symlink", Digest: digest([]byte(target))}, err
	}
	if !info.Mode().IsRegular() {
		return fileIdentity{}, fmt.Errorf("unsupported integration file type: %s", path)
	}
	b, err := readBounded(full)
	return fileIdentity{Kind: "file", Digest: digest(b), Exec: info.Mode()&0111 != 0}, err
}

// Snapshot source-visible files, plus explicitly approved ignored paths.
// Ignored runtime/database/cache files are outside this source mutation guard.
func sourceSnapshot(ctx context.Context, root string, extra []string) (map[string]fileIdentity, string, error) {
	out, err := gitOutput(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, "", err
	}
	paths := append(strings.Split(out, "\x00"), extra...)
	states := map[string]fileIdentity{}
	seen := map[string]bool{}
	var total int64
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if len(states) >= 100000 {
			return nil, "", errors.New("integration snapshot exceeds 100000 files")
		}
		state, err := identifyFile(root, path)
		if err != nil {
			return nil, "", err
		}
		if state.Kind != "" {
			states[path] = state
			info, err := os.Lstat(filepath.Join(root, path))
			if err != nil {
				return nil, "", err
			}
			total += info.Size()
			if total > 128<<20 {
				return nil, "", errors.New("integration snapshot exceeds 128 MiB")
			}
		}
	}
	index, err := gitOutput(ctx, root, "ls-files", "--stage", "-z")
	return states, digest([]byte(index)), err
}
