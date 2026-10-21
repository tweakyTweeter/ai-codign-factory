package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anskumar/ai-coding-factory/internal/factory"
)

// decisions waits for human answers to intake decision beads. It needs no
// run state or lock: it only reads Beads.
func decisions(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "await" {
		return &UsageError{errors.New("usage: factory decisions await --ids ID[,ID...] [--poll 15s] [--timeout 0]")}
	}
	set := flag.NewFlagSet("decisions await", flag.ContinueOnError)
	set.SetOutput(stderr)
	repoPath := set.String("repo", ".", "repository root, subdirectory or linked worktree")
	configPath := set.String("config", "", "config JSON for beads_argv (default: common Git dir/factory/config.json if present, else bd)")
	ids := set.String("ids", "", "comma-separated human-needed decision bead IDs")
	poll := set.Duration("poll", 15*time.Second, "Beads polling interval (minimum 1s)")
	timeout := set.Duration("timeout", 0, "give up after this long; 0 waits until interrupted")
	if err := set.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return &UsageError{err}
	}
	if set.NArg() != 0 {
		return &UsageError{errors.New("unexpected positional arguments; flags follow the command")}
	}
	if strings.TrimSpace(*ids) == "" {
		return &UsageError{errors.New("--ids is required")}
	}
	repo, err := factory.Discover(ctx, *repoPath)
	if err != nil {
		return err
	}
	argv := []string{"bd"}
	path := *configPath
	if path == "" {
		path = filepath.Join(repo.State, "config.json")
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			path = ""
		}
	}
	if path != "" {
		config, err := factory.LoadConfig(path)
		if err != nil {
			return err
		}
		argv = config.BeadsArgv
	}
	result, awaitErr := factory.AwaitDecisions(ctx, repo, argv, strings.Split(*ids, ","), *poll, *timeout)
	e := json.NewEncoder(stdout)
	e.SetIndent("", "  ")
	return errors.Join(awaitErr, e.Encode(map[string]any{"decisions": result, "all_answered": awaitErr == nil}))
}
