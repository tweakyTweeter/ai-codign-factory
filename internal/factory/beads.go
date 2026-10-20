package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type bead struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Assignee string `json:"assignee"`
}

var ErrLease = errors.New("Beads ownership/lease could not be confirmed")

// owned checks durable status/assignee as well as renewing the node-local
// lease. An explicit per-run actor is used on every ownership operation.
func (c Controller) owned(ctx context.Context, run Run, cfg Config) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if run.TaskID == "" {
		return nil
	}
	show, err := beadCommand(ctx, c.Repo, cfg.BeadsArgv, "show", run.TaskID, "--actor", run.Actor, "--json")
	if err != nil {
		return errors.Join(ErrLease, err)
	}
	var tasks []bead
	if err := json.Unmarshal(show, &tasks); err != nil {
		return errors.Join(ErrLease, err)
	}
	if len(tasks) != 1 || tasks[0].ID != run.TaskID || tasks[0].Status != "in_progress" || tasks[0].Assignee != run.Actor {
		return fmt.Errorf("%w: status or assignee changed", ErrLease)
	}
	b, err := beadCommand(ctx, c.Repo, cfg.BeadsArgv, "heartbeat", run.TaskID, "--actor", run.Actor, "--json")
	if err != nil {
		return errors.Join(ErrLease, err)
	}
	var hb struct {
		ID      string `json:"id"`
		Owner   string `json:"owner"`
		Version int    `json:"schema_version"`
		Status  string `json:"status"`
	}
	if err := decodeStrict(b, &hb); err != nil {
		return errors.Join(ErrLease, err)
	}
	if hb.ID != run.TaskID || hb.Owner != run.Actor || hb.Version != 1 || hb.Status != "heartbeat" {
		return fmt.Errorf("%w: invalid heartbeat response", ErrLease)
	}
	return nil
}

func beadCommand(ctx context.Context, r Repo, argv []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Per-invocation only: do not mutate BD_* or project configuration, and
	// suppress any configured Dolt auto-push during local preflight/claim.
	args = append(args, "--sandbox")
	return capture(ctx, r.Root, append(append([]string{}, argv...), args...), nil)
}

// preflight uses actual dependency-aware ready semantics, not an inferred
// status. Already in_progress tasks are refused even if claim is idempotent.
func preflight(ctx context.Context, repo Repo, argv []string, id string) ([]byte, error) {
	if strings.HasPrefix(id, "-") || strings.TrimSpace(id) == "" || len(id) > 256 {
		return nil, errors.New("invalid task ID")
	}
	show, err := beadCommand(ctx, repo, argv, "show", id, "--json")
	if err != nil {
		return nil, fmt.Errorf("beads show: %w", err)
	}
	var tasks []bead
	if err := json.Unmarshal(show, &tasks); err != nil {
		return nil, fmt.Errorf("beads show JSON: %w", err)
	}
	if len(tasks) != 1 || tasks[0].ID != id || tasks[0].Status != "open" || tasks[0].Assignee != "" {
		return nil, errors.New("task must be open, unassigned and ready; existing ownership is never reused")
	}
	ready, err := beadCommand(ctx, repo, argv, "ready", "--json", "--limit", "0")
	if err != nil {
		return nil, fmt.Errorf("beads ready: %w", err)
	}
	var available []bead
	if err := json.Unmarshal(ready, &available); err != nil {
		return nil, fmt.Errorf("beads ready JSON: %w", err)
	}
	for _, b := range available {
		if b.ID == id {
			return show, nil
		}
	}
	return nil, errors.New("task not in dependency-aware bd ready result")
}
