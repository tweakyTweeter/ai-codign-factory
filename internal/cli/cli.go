// Package cli implements the synchronous factory command-line interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anskumar/ai-coding-factory/internal/factory"
)

const help = `factory <command> [flags]

Commands: init, doctor, run, status, reconcile, cancel, release, review, review-publish, review-sync, integrate, copilot, agent, roborev-import, decisions await
All commands accept --repo PATH (root, subdirectory or linked worktree).
All successful output is JSON. Errors are written to stderr by main.
Trusted local command execution is NOT a sandbox. Integration requires explicit integrate.
`

type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// Run is testable independently from process-level signal handling.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		_, err := io.WriteString(stdout, help)
		return err
	}
	name := args[0]
	if name == "copilot" || name == "agent" || name == "roborev-import" {
		return adapter(ctx, name, args[1:], stdout, stderr)
	}
	if name == "decisions" {
		return decisions(ctx, args[1:], stdout, stderr)
	}
	switch name {
	case "init", "doctor", "run", "status", "reconcile", "cancel", "release", "review", "review-publish", "review-sync", "integrate":
	default:
		return &UsageError{fmt.Errorf("unknown command %q", name)}
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoPath := fs.String("repo", ".", "repository root, subdirectory or linked worktree")
	configPath, promptPath, task, profile, id, reason := new(string), new(string), new(string), new(string), new(string), new(string)
	ackOrphan := new(bool)
	if name == "init" || name == "doctor" || name == "run" {
		fs.StringVar(configPath, "config", "", "config JSON path (default: common Git dir/factory/config.json)")
	}
	if name == "run" {
		fs.StringVar(promptPath, "prompt-file", "", "prompt file; '-' reads stdin (1 MiB maximum)")
		fs.StringVar(task, "task", "", "optional open, unassigned and dependency-ready Beads ID")
	}
	if name == "run" || name == "doctor" {
		fs.StringVar(profile, "profile", "", "configured worker or opt-in specialist profile name")
	}
	if name == "doctor" {
		fs.StringVar(task, "task", "", "also check Beads executable availability (does not claim)")
	}
	isReview := name == "review" || name == "review-publish" || name == "review-sync" || name == "integrate"
	if name == "status" || name == "cancel" || name == "release" || isReview {
		fs.StringVar(id, "run", "", "durable run ID (required except for status)")
	}
	if name == "release" {
		fs.StringVar(reason, "reason", "", "acknowledgement reason; does not mean integrated")
		fs.BoolVar(ackOrphan, "ack-orphan", false, "confirm possible orphans stopped and claim/partial integration inspected manually")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &UsageError{err}
	}
	if fs.NArg() != 0 {
		return &UsageError{errors.New("unexpected positional arguments; flags follow the command")}
	}
	if (name == "cancel" || name == "release" || isReview) && *id == "" {
		return &UsageError{errors.New("--run is required")}
	}
	if name == "run" && *promptPath == "" {
		return &UsageError{errors.New("--prompt-file is required")}
	}
	if name == "release" && strings.TrimSpace(*reason) == "" {
		return &UsageError{errors.New("--reason is required")}
	}
	repo, err := factory.Discover(ctx, *repoPath)
	if err != nil {
		return err
	}
	if *configPath == "" {
		*configPath = filepath.Join(repo.State, "config.json")
	}
	if name == "init" {
		lock, err := repo.Lock()
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := initialize(*configPath); err != nil {
			return err
		}
	}
	store, err := factory.Open(ctx, repo)
	if err != nil {
		return err
	}
	defer store.Close()
	c := factory.Controller{Repo: repo, Store: store}
	encode := func(v any) error {
		e := json.NewEncoder(stdout)
		e.SetIndent("", "  ")
		return e.Encode(v)
	}
	switch name {
	case "review", "review-publish", "review-sync", "integrate":
		var run factory.Run
		var reviewErr error
		switch name {
		case "review":
			run, reviewErr = c.Review(ctx, *id)
		case "review-publish":
			run, reviewErr = c.ReviewPublish(ctx, *id)
		case "review-sync":
			run, reviewErr = c.ReviewSync(ctx, *id)
		case "integrate":
			run, reviewErr = c.Integrate(ctx, *id)
		}
		return errors.Join(reviewErr, encode(run))
	case "init":
		return encode(map[string]any{"repo": repo, "config": *configPath, "notice": "edit worker/check argv before run; commands are trusted, not sandboxed"})
	case "doctor":
		config, err := factory.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		if *profile != "" && config.Workflow == nil {
			worker, ok := config.Profiles[*profile]
			if !ok {
				return fmt.Errorf("unknown profile %q", *profile)
			}
			config.Worker = worker
		}
		commands := append([]factory.Command{config.Worker}, config.Checks...)
		if config.Workflow != nil {
			commands = append([]factory.Command{}, config.Checks...)
			found := *profile == ""
			for _, p := range config.Workflow.Phases {
				if p.Role == "specialist" && p.Profile == *profile {
					found = true
				}
				if p.Role != "specialist" || p.Profile == *profile {
					commands = append(commands, p.Command)
				}
			}
			if !found {
				return fmt.Errorf("unknown specialist profile %q", *profile)
			}
		}
		if *task != "" {
			commands = append(commands, factory.Command{Argv: config.BeadsArgv})
		}
		for _, cmd := range commands {
			if _, err := exec.LookPath(cmd.Argv[0]); err != nil {
				return fmt.Errorf("configured executable %s: %w", cmd.Argv[0], err)
			}
		}
		if _, err := exec.LookPath("git"); err != nil {
			return err
		}
		runtimes := []factory.PhaseRuntime{}
		if config.Workflow != nil {
			for _, p := range config.Workflow.Phases {
				if p.Role == "specialist" && p.Profile != *profile {
					continue
				}
				pr, err := factory.InspectPhasePolicy(ctx, p.Name, p.Role, p.Command.Argv)
				if err != nil {
					return err
				}
				if pr != nil {
					runtimes = append(runtimes, *pr)
				}
			}
		}
		return encode(map[string]any{"repo": repo, "config_valid": true, "sandbox": false, "phase_runtimes": runtimes, "notice": "executables found only; no worker/Conductor capability or success implied"})
	case "run":
		config, err := factory.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		var reader io.Reader = stdin
		if *promptPath != "-" {
			f, err := os.Open(*promptPath)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		prompt, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		if err != nil {
			return err
		}
		run, runErr := c.Run(ctx, factory.Request{Config: config, Prompt: prompt, TaskID: *task, Profile: *profile})
		return errors.Join(runErr, encode(run))
	case "status":
		if *id != "" {
			r, err := store.Get(ctx, *id)
			if err != nil {
				return err
			}
			events, err := store.Events(ctx, *id)
			if err != nil {
				return err
			}
			return encode(map[string]any{"run": r, "events": events})
		}
		runs, err := store.List(ctx)
		if err != nil {
			return err
		}
		return encode(runs)
	case "reconcile":
		runs, err := c.Reconcile(ctx)
		if err != nil {
			return err
		}
		return encode(runs)
	case "cancel":
		// A request is consumed by the lock-holding foreground controller.
		// An orphan has no consumer: reconcile instead, NEVER signal its PID.
		if err := store.RequestCancel(ctx, *id); err != nil {
			return err
		}
		return encode(map[string]any{"run_id": *id, "cancellation_requested": true, "notice": "cooperative request only; for a crashed controller use reconcile; no stored PID signalled"})
	case "release":
		run, err := c.Release(ctx, *id, *reason, *ackOrphan)
		if err != nil {
			return err
		}
		return encode(run)
	}
	return nil
}

func initialize(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // idempotent, never overwrite a user's config
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	config := factory.Config{
		Version:   1,
		Worker:    factory.Command{Argv: []string{"/path/to/trusted-worker"}, Timeout: "10m"},
		Checks:    []factory.Command{{Argv: []string{"go", "test", "./..."}, Timeout: "2m"}},
		BeadsArgv: []string{"bd"},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	return errors.Join(writeErr, f.Sync(), f.Close())
}
