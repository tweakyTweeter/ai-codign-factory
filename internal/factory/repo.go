package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type Repo struct {
	Root   string `json:"root"`
	Common string `json:"common_git_dir"`
	State  string `json:"state_dir"`
	// Worktrees holds run worktrees. It sits beside the repository, outside
	// the common git dir, because agents (Claude Code) refuse writes under .git.
	Worktrees string `json:"worktrees_dir"`
}

func Discover(ctx context.Context, path string) (Repo, error) {
	root, err := gitOutput(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repo{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Repo{}, err
	}
	common, err := gitOutput(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Repo{}, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return Repo{}, err
	}
	return Repo{Root: root, Common: common, State: filepath.Join(common, "factory"), Worktrees: worktreesDir(common)}, nil
}

// worktreesDir derives the run worktrees root from the common git dir, so every
// linked worktree of a repository agrees: /x/proj/.git -> /x/proj.factory-runs,
// bare /x/proj.git -> /x/proj.git.factory-runs. Bare repositories are not
// supported in practice (Discover needs a work tree); with --separate-git-dir
// the root sits beside the git store, not beside the work tree.
func worktreesDir(common string) string {
	base := common
	if filepath.Base(common) == ".git" {
		base = filepath.Dir(common)
	}
	return filepath.Join(filepath.Dir(base), filepath.Base(base)+".factory-runs")
}

// checkRunWorktreePath fails closed unless worktree is an absolute, clean path
// outside the common git dir with no ".git" component.
func checkRunWorktreePath(common, worktree string) error {
	if !filepath.IsAbs(worktree) || filepath.Clean(worktree) != worktree {
		return fmt.Errorf("run worktree path %q must be absolute and clean", worktree)
	}
	rel, err := filepath.Rel(common, worktree)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("run worktree path %q is inside the common git dir %q; refusing to create it", worktree, common)
	}
	for _, part := range strings.Split(worktree, string(filepath.Separator)) {
		if part == ".git" {
			return fmt.Errorf("run worktree path %q contains a .git component; refusing to create it", worktree)
		}
	}
	return nil
}

// prepareWorktreesRoot creates the shared run worktrees root if needed and
// fails closed unless it is a real directory (not a symlink) owned by this
// user with no group or other permissions, whose resolved path is unchanged
// and still passes checkRunWorktreePath for worktree. It never repairs, chmods
// or deletes a bad root; it refuses instead. Call it before git worktree add.
func prepareWorktreesRoot(common, root, worktree string) error {
	if err := checkRunWorktreePath(common, worktree); err != nil {
		return err
	}
	if filepath.Dir(worktree) != root {
		return fmt.Errorf("run worktree path %q is not directly under the worktrees root %q; refusing to create it", worktree, root)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return fmt.Errorf("create run worktrees root %q: %w", root, err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect run worktrees root %q: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("run worktrees root %q is a symlink; refusing to create a worktree through it", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("run worktrees root %q is not a directory; refusing to create a worktree", root)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("run worktrees root %q is not owned by the current user; refusing to create a worktree", root)
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		return fmt.Errorf("run worktrees root %q has mode %#o, want 0700 or stricter; refusing to create a worktree", root, perm)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve run worktrees root %q: %w", root, err)
	}
	if resolved != root {
		return fmt.Errorf("run worktrees root %q resolves to %q; refusing to create a worktree", root, resolved)
	}
	return checkRunWorktreePath(common, filepath.Join(resolved, filepath.Base(worktree)))
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	child, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := capture(child, dir, append([]string{"git", "--no-pager"}, args...), nil)
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// Lock is held for the entire foreground run. Its fd is not inherited by workers.
type Lock struct{ f *os.File }

func (r Repo) Lock() (*Lock, error) {
	if err := os.MkdirAll(r.State, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(r.State, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("repository controller is busy: %w", err)
	}
	return &Lock{f: f}, nil
}

func (l *Lock) Close() error {
	return errors.Join(unix.Flock(int(l.f.Fd()), unix.LOCK_UN), l.f.Close())
}
