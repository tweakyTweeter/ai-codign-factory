package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// HumanReview is a local operator decision, not integration or release authority.
// Request fields are immutable after publish intent; gate closures alone are
// never interpreted as approval. Local single-user bd authority is assumed.
type HumanReview struct {
	Version        int               `json:"version"`
	RunID          string            `json:"run_id"`
	TaskID         string            `json:"task_id"`
	Actor          string            `json:"actor"`
	Head           string            `json:"head"`
	Worktree       string            `json:"worktree"`
	Fingerprint    string            `json:"fingerprint"`
	Bundle         string            `json:"bundle"`
	Artifacts      map[string]string `json:"artifacts"`
	BeadsArgv      []string          `json:"beads_argv,omitempty"`
	CreateArgv     []string          `json:"create_argv,omitempty"`
	Marker         string            `json:"marker"`
	ApproveReason  string            `json:"approve_reason"`
	RejectReason   string            `json:"reject_reason"`
	Title          string            `json:"title,omitempty"`
	Description    string            `json:"description,omitempty"`
	PublishState   string            `json:"publish_state"`
	GateID         string            `json:"gate_id,omitempty"`
	Decision       string            `json:"decision"`
	Detail         string            `json:"detail"`
	ObservedReason string            `json:"observed_reason,omitempty"`
	ObservedAt     string            `json:"observed_at,omitempty"`
}

type reviewManifest struct {
	Version     int               `json:"version"`
	RunID       string            `json:"run_id"`
	TaskID      string            `json:"task_id"`
	Head        string            `json:"head"`
	Fingerprint string            `json:"fingerprint"`
	Worktree    string            `json:"worktree"`
	Diff        string            `json:"diff"`
	IndexDiff   string            `json:"index_diff"`
	Evidence    []Evidence        `json:"evidence"`
	Sources     map[string]string `json:"source_sha256"`
	Notice      string            `json:"notice"`
}

const reviewNotice = "Local operator review only. No commit, integration, task close, unclaim or release is authorized or performed. Single-user local bd authority; actor identity is not cryptographically verified."

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Review creates a bounded private bundle without touching candidate or index.
// Publication and decision consumption are deliberately separate commands.
func (c Controller) Review(ctx context.Context, id string) (Run, error) {
	lock, err := c.Repo.Lock()
	if err != nil {
		return Run{}, err
	}
	defer lock.Close()
	r, err := c.Store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if !stable(r.State) {
		return r, errors.New("review requires a stopped, retained run")
	}
	if r.HumanReview != nil {
		if err := c.validateReview(ctx, r); err != nil {
			r.HumanReview.Decision, r.HumanReview.Detail = "not-approved", err.Error()
			dctx, cancel := durableContext()
			defer cancel()
			return r, errors.Join(err, c.Store.SaveHumanReview(dctx, &r, "review invalidated: "+err.Error()))
		}
		return r, nil
	}
	if err := verifyIntegrity(ctx, r); err != nil {
		return r, err
	}
	fp, err := Fingerprint(ctx, r.Worktree)
	if err != nil {
		return r, err
	}
	if r.Fingerprint != "" && fp != r.Fingerprint {
		return r, errors.New("candidate changed since supervised review; new verified run required")
	}
	head, err := gitOutput(ctx, r.Worktree, "rev-parse", "HEAD")
	if err != nil || head != r.Head {
		return r, errors.Join(err, errors.New("candidate HEAD changed"))
	}
	patch, err := candidateDiff(ctx, r.Worktree)
	if err != nil {
		return r, err
	}
	index, err := gitOutput(ctx, r.Worktree, "diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	if err != nil {
		return r, err
	}
	bundleID, err := randomID()
	if err != nil {
		return r, err
	}
	dir := filepath.Join(r.Dir, "human-review-"+bundleID)
	if err := os.Mkdir(dir, 0700); err != nil {
		return r, err
	}
	m := reviewManifest{Version: 1, RunID: r.ID, TaskID: r.TaskID, Head: r.Head,
		Fingerprint: fp, Worktree: r.Worktree, Diff: filepath.Join(dir, "candidate.diff"),
		IndexDiff: filepath.Join(dir, "index.diff"), Evidence: r.Evidence,
		Sources: map[string]string{}, Notice: reviewNotice}
	sources := []string{filepath.Join(r.Dir, "config.json")}
	for _, e := range r.Evidence {
		if e.Log != "" {
			sources = append(sources, e.Log)
		}
		// Producer/specialist phases are not required to emit result.json;
		// the controller still records their expected output path.
		if e.ResultFile != "" && (e.Role == "dev" || e.Role == "qa" || e.Role == "review") {
			sources = append(sources, e.ResultFile)
		}
	}
	for _, path := range sources {
		b, err := readBounded(path)
		if err != nil {
			return r, fmt.Errorf("review source %s: %w", path, err)
		}
		m.Sources[path] = digest(b)
	}
	summary := fmt.Sprintf("# Factory candidate review\n\nRun: %s\nTask: %s\nFingerprint: %s\nHEAD: %s\nWorktree: %s\n\n%s\n\n", r.ID, r.TaskID, fp, r.Head, r.Worktree, reviewNotice)
	if r.Fingerprint == "" {
		summary += "UNVERIFIED generic worker run: no structured independent QA/review claim. Publication disabled.\n\n"
	}
	for _, e := range r.Evidence {
		summary += fmt.Sprintf("- %s (%s): exit=%d truncated=%t error=%q; log=%s; result=%s\n", e.Stage, e.Role, e.ExitCode, e.Truncated, e.Error, e.Log, e.ResultFile)
		if e.Verdict != nil {
			summary += fmt.Sprintf("  Recorded verdict: %s; %s\n", e.Verdict.Verdict, e.Verdict.Summary)
		}
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return r, err
	}
	files := map[string][]byte{"candidate.diff": patch, "index.diff": []byte(index), "summary.md": []byte(summary), "manifest.json": manifest}
	h := &HumanReview{Version: 1, RunID: r.ID, TaskID: r.TaskID, Actor: r.Actor, Head: r.Head,
		Worktree: r.Worktree, Fingerprint: fp, Bundle: dir, Artifacts: map[string]string{},
		Marker:        "factory-review:" + r.ID + ":" + fp,
		ApproveReason: "factory-approve:" + r.ID + ":" + fp,
		RejectReason:  "factory-reject:" + r.ID + ":" + fp,
		PublishState:  "unpublished", Decision: "not-approved", Detail: "bundle only; no human gate published"}
	for name, b := range files {
		if len(b) > outputLimit {
			return r, fmt.Errorf("review artifact %s exceeds 1 MiB", name)
		}
		path := filepath.Join(dir, name)
		if err := writeExclusive(path, b); err != nil {
			return r, err
		}
		h.Artifacts[path] = digest(b)
	}
	after, err := Fingerprint(ctx, r.Worktree)
	if err != nil || after != fp {
		return r, errors.Join(err, errors.New("candidate changed while creating bundle"))
	}
	r.HumanReview = h
	return r, c.Store.SaveHumanReview(ctx, &r, "private review bundle created")
}

// candidateDiff includes ignored/untracked additions too, since Fingerprint
// binds them. Git no-index preserves symlink/executable/binary patch semantics.
// No staging, object writes or alternate index is used.
func candidateDiff(ctx context.Context, dir string) ([]byte, error) {
	tracked, err := gitOutput(ctx, dir, "diff", "--binary", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if tracked != "" {
		out.WriteString(tracked)
		out.WriteByte('\n')
	}
	names, err := gitOutput(ctx, dir, "ls-files", "--others", "-z")
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Split(names, "\x00") {
		if name == "" {
			continue
		}
		child, cancel := context.WithTimeout(ctx, 30*time.Second)
		w := &cappedWriter{dst: &out, remaining: outputLimit - out.Len()}
		code, runErr := executeGroup(child, dir, []string{"git", "--no-pager", "diff", "--no-index", "--binary", "--no-ext-diff", "--no-textconv", "--", "/dev/null", name}, nil, w, nil, true)
		contextErr := child.Err()
		cancel()
		// git diff --no-index returns 1 for a successfully generated difference.
		if w.truncated || code != 0 && code != 1 || contextErr != nil {
			return nil, errors.Join(runErr, errors.New("untracked diff failed, cancelled or exceeded 1 MiB"))
		}
	}
	if out.Len() > outputLimit {
		return nil, errors.New("candidate diff exceeds 1 MiB")
	}
	return out.Bytes(), nil
}

func (c Controller) validateReview(ctx context.Context, r Run) error {
	if err := verifyIntegrity(ctx, r); err != nil {
		return err
	}
	h := r.HumanReview
	if h == nil || h.Version != 1 || h.RunID != r.ID || h.TaskID != r.TaskID || h.Actor != r.Actor ||
		h.Head != r.Head || h.Worktree != r.Worktree ||
		h.Marker != "factory-review:"+r.ID+":"+h.Fingerprint ||
		h.ApproveReason != "factory-approve:"+r.ID+":"+h.Fingerprint ||
		h.RejectReason != "factory-reject:"+r.ID+":"+h.Fingerprint {
		return errors.New("human review request identity mismatch")
	}
	fp, err := Fingerprint(ctx, r.Worktree)
	if err != nil {
		return err
	}
	if fp != h.Fingerprint || r.Fingerprint != "" && fp != r.Fingerprint {
		return errors.New("candidate fingerprint changed; new verified request required")
	}
	if len(h.Artifacts) != 4 {
		return errors.New("incomplete review artifacts")
	}
	for _, name := range []string{"candidate.diff", "index.diff", "summary.md", "manifest.json"} {
		path := filepath.Join(h.Bundle, name)
		b, err := readBounded(path)
		if err != nil || digest(b) != h.Artifacts[path] {
			return errors.Join(err, fmt.Errorf("review artifact changed: %s", path))
		}
	}
	b, err := readBounded(filepath.Join(h.Bundle, "manifest.json"))
	if err != nil {
		return err
	}
	var m reviewManifest
	if err := decodeStrict(b, &m); err != nil {
		return err
	}
	if m.RunID != r.ID || m.TaskID != r.TaskID || m.Head != r.Head || m.Fingerprint != fp || !reflect.DeepEqual(m.Evidence, r.Evidence) {
		return errors.New("review manifest identity/evidence mismatch")
	}
	for path, want := range m.Sources {
		b, err := readBounded(path)
		if err != nil || digest(b) != want {
			return errors.Join(err, fmt.Errorf("review source changed: %s", path))
		}
	}
	return nil
}

// supervisedReview verifies persisted phase evidence, not merely run labels.
func supervisedReview(ctx context.Context, r Run, cfg Config) error {
	if err := verifyIntegrity(ctx, r); err != nil {
		return err
	}
	if r.State != "awaiting_review" || r.CancelRequested || r.TaskID == "" || r.Actor != "factory-"+r.ID ||
		r.Fingerprint == "" || r.QAFingerprint != r.Fingerprint || cfg.Workflow == nil {
		return errors.New("publication requires an uncancelled supervised awaiting_review task run")
	}
	expected := []string{}
	for _, p := range cfg.Workflow.Phases {
		if p.Role == "specialist" && p.Profile != r.Profile {
			continue
		}
		expected = append(expected, p.Role)
		if p.Role == "dev" {
			for range cfg.Checks {
				expected = append(expected, "check")
			}
		}
	}
	if len(expected) != len(r.Evidence) {
		return errors.New("incomplete supervised evidence")
	}
	for i, e := range r.Evidence {
		if e.Role != expected[i] || e.ExitCode != 0 || e.Error != "" || e.Truncated {
			return errors.New("failed, truncated or mismatched supervised evidence")
		}
		if e.Role == "dev" || e.Role == "qa" || e.Role == "review" {
			v, err := readVerdict(e.ResultFile, r.Fingerprint)
			if err != nil || e.Fingerprint != r.Fingerprint || e.Verdict == nil || !reflect.DeepEqual(v, *e.Verdict) {
				return errors.Join(err, errors.New("invalid or changed structured supervised verdict"))
			}
		}
	}
	return nil
}

func (c Controller) reviewTask(ctx context.Context, r Run, argv []string) error {
	b, err := beadCommand(ctx, c.Repo, argv, "show", r.TaskID, "--json")
	if err != nil {
		return err
	}
	var tasks []struct {
		bead
		Dependencies []struct {
			ID   string `json:"id"`
			Type string `json:"dependency_type"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(b, &tasks); err != nil {
		return err
	}
	if len(tasks) != 1 || tasks[0].ID != r.TaskID || tasks[0].Assignee != r.Actor || tasks[0].Status != "in_progress" {
		return errors.New("original task ownership or status changed")
	}
	if r.HumanReview != nil && r.HumanReview.GateID != "" {
		for _, dep := range tasks[0].Dependencies {
			if dep.ID == r.HumanReview.GateID && dep.Type == "blocks" {
				return nil
			}
		}
		return errors.New("known human gate no longer blocks original task")
	}
	return nil // Deliberately no heartbeat: exited controller's lease is not authority.
}

// ReviewPublish creates only a native blocking human gate. An intent is
// committed before creation; an uncertain response is never blindly retried.
func (c Controller) ReviewPublish(ctx context.Context, id string) (Run, error) {
	r, err := c.Review(ctx, id)
	if err != nil {
		return r, err
	}
	lock, err := c.Repo.Lock()
	if err != nil {
		return Run{}, err
	}
	defer lock.Close()
	r, err = c.Store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	h := r.HumanReview
	fail := func(err error) (Run, error) {
		h.Decision, h.Detail = "not-approved", err.Error()
		if h.PublishState == "intent" {
			h.PublishState = "publish-error"
		}
		dctx, cancel := durableContext()
		defer cancel()
		return r, errors.Join(err, c.Store.SaveHumanReview(dctx, &r, "review publication blocked: "+err.Error()))
	}
	if err := c.validateReview(ctx, r); err != nil {
		return fail(err)
	}
	cfg, err := LoadConfig(filepath.Join(r.Dir, "config.json"))
	if err != nil {
		return fail(err)
	}
	if err := supervisedReview(ctx, r, cfg); err != nil {
		return fail(err)
	}
	if err := c.reviewTask(ctx, r, cfg.BeadsArgv); err != nil {
		return fail(err)
	}
	if h.PublishState == "unpublished" {
		h.BeadsArgv = append([]string{}, cfg.BeadsArgv...)
		h.Title = fmt.Sprintf("Factory human review: %s (%s)", r.TaskID, r.ID)
		description, err := c.gateDescription(ctx, r, h, cfg.Checks)
		if err != nil {
			return fail(err)
		}
		h.Description = description
		h.CreateArgv = append(append([]string{}, h.BeadsArgv...), "gate", "create", "--blocks", r.TaskID,
			"--type", "human", "--reason", h.Description, "--title", h.Title, "--json", "--sandbox")
		h.PublishState = "intent"
		if err := c.Store.SaveHumanReview(ctx, &r, "intent: publish native human gate; request and bd argv persisted"); err != nil {
			return r, err
		}
		if r.CancelRequested {
			return fail(errors.New("cancellation requested before gate creation"))
		}
		// Marking intent precedes even list/create: restart after any uncertainty
		// must reconcile, never issue another create automatically.
		gates, err := c.reviewGates(ctx, r)
		if err != nil {
			return fail(err)
		}
		if len(gates) == 0 {
			_, createErr := beadCommand(ctx, c.Repo, h.BeadsArgv, "gate", "create", "--blocks", r.TaskID,
				"--type", "human", "--reason", h.Description, "--title", h.Title, "--json")
			gates, err = c.reviewGates(ctx, r)
			if err != nil || len(gates) == 0 {
				return fail(errors.Join(createErr, err, errors.New("gate creation uncertain; inspect native gates; no automatic retry")))
			}
		}
		return c.bindReviewGate(ctx, r, gates)
	}
	if !reflect.DeepEqual(h.BeadsArgv, cfg.BeadsArgv) {
		return fail(errors.New("persisted bd argv changed"))
	}
	gates, err := c.reviewGates(ctx, r)
	if err != nil {
		return fail(err)
	}
	if len(gates) == 0 {
		return fail(errors.New("publish intent has no matching gate; inspect manually; refusing duplicate-prone retry"))
	}
	return c.bindReviewGate(ctx, r, gates)
}

// nativeGate is intentionally a subset: bd adds metadata across versions.
type nativeGate struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	IssueType   string `json:"issue_type"`
	AwaitType   string `json:"await_type"`
	CloseReason string `json:"close_reason"`
}

func (c Controller) reviewGates(ctx context.Context, r Run) ([]nativeGate, error) {
	b, err := beadCommand(ctx, c.Repo, r.HumanReview.BeadsArgv, "gate", "list", r.TaskID, "--all", "--limit", "0", "--json")
	if err != nil {
		return nil, err
	}
	var all []nativeGate
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("decode native gates: %w", err)
	}
	result := []nativeGate{}
	for _, g := range all {
		if strings.Contains(g.Description, r.HumanReview.Marker) || g.ID == r.HumanReview.GateID {
			result = append(result, g)
		}
	}
	return result, nil
}

func validateNativeGate(h *HumanReview, gates []nativeGate) (nativeGate, error) {
	if len(gates) != 1 {
		return nativeGate{}, errors.New("missing or ambiguous matching human gate")
	}
	g := gates[0]
	if g.ID == "" || strings.HasPrefix(g.ID, "-") || h.GateID != "" && h.GateID != g.ID ||
		g.Title != h.Title || g.Description != nativeGateDescription(h) || g.IssueType != "gate" || g.AwaitType != "human" {
		return g, errors.New("native human gate identity or immutable request fields changed")
	}
	return g, nil
}

func (c Controller) bindReviewGate(ctx context.Context, r Run, gates []nativeGate) (Run, error) {
	h := r.HumanReview
	g, err := validateNativeGate(h, gates)
	if err != nil {
		h.Decision, h.Detail, h.PublishState = "not-approved", err.Error(), "publish-error"
	} else {
		h.GateID, h.PublishState = g.ID, "published"
		// Publishing/reconciling is not decision consumption.
		h.Decision, h.Detail = "not-approved", "native gate published; run review-sync to observe a decision"
		if err = c.reviewTask(ctx, r, h.BeadsArgv); err != nil {
			h.PublishState, h.Detail = "publish-error", err.Error()
		}
	}

	return r, errors.Join(err, c.Store.SaveHumanReview(ctx, &r, h.Detail))
}

func nativeGateDescription(h *HumanReview) string {
	return "Ad-hoc gate blocking " + h.TaskID + "\n\nReason: " + h.Description
}

func fencedDiff(diff string) string {
	fence := "```"
	for strings.Contains(diff, fence) {
		fence += "`"
	}
	return fence + "diff\n" + diff + "\n" + fence + "\n"
}

// ReviewSync never closes/resolves anything. Reopening, cancellation, changed
// ownership/evidence/patch/request or a read error revokes previous approval.
func (c Controller) ReviewSync(ctx context.Context, id string) (Run, error) {
	lock, err := c.Repo.Lock()
	if err != nil {
		return Run{}, err
	}
	defer lock.Close()
	r, err := c.Store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	h := r.HumanReview
	if h == nil {
		return r, errors.New("no review request; publish one explicitly first")
	}
	h.Decision, h.ObservedReason, h.ObservedAt = "not-approved", "", timestamp()
	check := func() error {
		if h.PublishState != "published" || h.GateID == "" {
			return errors.New("no known published gate")
		}
		if err := c.validateReview(ctx, r); err != nil {
			return err
		}
		cfg, err := LoadConfig(filepath.Join(r.Dir, "config.json"))
		if err != nil {
			return err
		}
		if err := supervisedReview(ctx, r, cfg); err != nil {
			return err
		}
		if !reflect.DeepEqual(h.BeadsArgv, cfg.BeadsArgv) {
			return errors.New("persisted bd argv changed")
		}
		if err := c.reviewTask(ctx, r, h.BeadsArgv); err != nil {
			return err
		}
		gates, err := c.reviewGates(ctx, r)
		if err != nil {
			return err
		}
		g, err := validateNativeGate(h, gates)
		if err != nil {
			return err
		}
		h.ObservedReason = g.CloseReason
		if g.Status != "closed" {
			h.Detail = "gate open/pending; any previous approval revoked"
			return nil
		}
		h.Decision = gateDecision(g.CloseReason, h)
		h.Detail = "closed gate observed: " + h.Decision
		// Re-read patch and ownership after the external reads too.
		if err := c.validateReview(ctx, r); err != nil {
			return err
		}
		return c.reviewTask(ctx, r, h.BeadsArgv)
	}
	err = check()
	if err != nil {
		h.Decision, h.Detail = "not-approved", err.Error()
	}
	dctx, cancel := durableContext()
	defer cancel()
	return r, errors.Join(err, c.Store.SaveHumanReview(dctx, &r, h.Detail))
}

func gateDecision(reason string, h *HumanReview) string {
	if reason == h.RejectReason {
		return "rejected"
	}
	if reason == h.ApproveReason {
		return "approved"
	}
	// Scotty 0.3.2 prefixes approval notes. Require that exact UI envelope and
	// a single standalone token line; arbitrary closures/comments do not count.
	lines := strings.Split(reason, "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "Approved by ") || !strings.Contains(lines[0], " via Scotty at ") || lines[1] != "" {
		return "not-approved"
	}
	approve, reject := 0, 0
	for _, line := range lines[2:] {
		if line == h.ApproveReason {
			approve++
		}
		if line == h.RejectReason {
			reject++
		}
	}
	if reject == 1 && approve == 0 {
		return "rejected"
	}
	if approve == 1 && reject == 0 {
		return "approved"
	}
	return "not-approved"
}
